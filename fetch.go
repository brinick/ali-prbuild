package prbuild

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/brinick/ali-ci/pullrequest"
	"github.com/brinick/logging"
)

// --------------------------------------------------------

// TODO: why do we need to define this interface?
type prFetcher interface {
	Start()
	Stop() error
	Done() <-chan struct{}
	IsRunning() bool
	Data() *priorityPRData
}

// --------------------------------------------------------

type priorityPRData struct {
	c chan *priorityPR
}

func (p *priorityPRData) read() []*priorityPR {
	data := []*priorityPR{}

outer:
	for {
		select {
		case pr, ok := <-p.c:
			// protect against the zero value
			if !ok {
				break outer
			}

			Error("Channel data read ok, appending data")
			data = append(data, pr)
		default:
			// no (more) data
			break outer
		}
	}

	return data
}

// write sends the list of priorityPR structs onto the
// wrapped data channel. If the channel is closed, an error
// is returned.
func (p *priorityPRData) write(items []*priorityPR) error {
	if p.closed() {
		return fmt.Errorf("channel closed for writing")
	}

	for _, item := range items {
		p.c <- item
	}

	return nil
}

func (p *priorityPRData) closed() bool {
	var isClosed bool
	select {
	case _, isClosed = <-p.c:
		//
	}

	return !isClosed
}

// --------------------------------------------------------

// FetcherTimes holds on to useful timing info related to PR fetching
type FetcherTimes struct {
	lastFetch       int64 // epoch of last PR successful fetch attempt
	lastFailedFetch int64 // epoch of last PR failed fetch attempt
	lastSend        int64 // epoch of last PR send on the prs channel
	lastNew         int64 // epoch of last PRs that were new
}

// FetcherCounts holds on to useful counts info related to PR fetching
type FetcherCounts struct {
	nFetches       int // number times we tried to fetch new PRs
	nFailedFetches int // number times there was an error fetching PRs
	nSends         int // number of times we pushed PRs on to the send channel
	nNewPRs        int // number times we had new PRs to push to the send channel
}

// Fetcher fetches PRs in a goroutine, and pushes them onto its prs channel
type Fetcher struct {
	// The channel on which we push PRs to be tested, and that are
	// retrieved by mainLoop goroutine in main.go
	prs chan *priorityPR

	// Useful info concerning PR fetching
	times *FetcherTimes
	stats *FetcherCounts

	// Pull requests already retrieved in a previous fetch.
	// We use this to know if we should send back old prs
	knownPRs *knownPRs

	// shutdown the fetching goroutine and exit if this is closed
	shutdown chan struct{}

	// closed when the fetcher routine spawned in Start() exits
	done chan struct{}

	// Set to true when the fetcher.Start() is first called
	// and indicates that the fetcher is fetching
	running bool
}

// --------------------------------------------------------

// NewFetcher constructs a Fetcher type
func NewFetcher() *Fetcher {
	return &Fetcher{
		prs:      make(chan *priorityPR, cfg.prQueueSize),
		times:    &FetcherTimes{},
		stats:    &FetcherCounts{},
		knownPRs: &knownPRs{},
	}
}

func (f *Fetcher) relaunchIfPanic() {
	f.running = false

	if r := recover(); r != nil {
		Info(
			"PR Fetcher goroutine panicked. Restarting.",
			Field("recover", r),
		)
		f.Start()
		return
	}

	// There was no panic, close up shop
	close(f.done)
}

// Start launches a goroutine, fetching pull requests until told
// to stop on the Fetcher shutdown channel. Fetched pull requests are
// pushed on to the Fetcher prs channel and retrieved via the Data() method.
func (f *Fetcher) Start() {
	if f.running {
		return // no-op
	}

	Info("Pull request fetcher: starting")

	// On start up, make available the channels to shut us down
	f.shutdown = make(chan struct{})
	f.done = make(chan struct{})

	go func() {
		defer f.relaunchIfPanic()

		f.running = true

	outer:
		for {
			Debug("Launching a background pull request fetch")
			fetch := f.doBackgroundFetch()

			select {
			case <-fetch.done:
				// Once the background fetch is done, if it took
				// less time than the configured delay that we should
				// wait between fetches, then we wait some more
				remaining := fetch.duration - cfg.fetch.delayBetweenFetches
				if remaining < 0 {
					if f.sleepFor(-1 * remaining) {
						Info("Shut down requested, stop fetching pull requests")
						<-fetch.done
						f.drainDataPipe()
						break outer
					}
				}

			case <-f.shutdown:
				Info("Shut down requested, stop fetching pull requests")
				Debug("Waiting for background fetch to complete")
				<-fetch.done
				f.drainDataPipe()
				break outer
			}
		}

		Info("Pull request fetcher: stopped")
	}()
}

// --------------------------------------------------------

type bkgdFetch struct {
	done     chan struct{}
	duration int64
}

func (f *Fetcher) doBackgroundFetch() *bkgdFetch {
	bf := &bkgdFetch{
		done: make(chan struct{}),
	}

	go func() {
		start := time.Now().Unix()

		defer func() {
			bf.duration = time.Now().Unix() - start
			close(bf.done)
		}()

		f.doFetch()
	}()

	return bf
}

// --------------------------------------------------------

// sleepFor sleeps for the given amount of seconds,
// interrupting if the fetcher shutdown channel is closed.
// Returning true means the sleep was interrupted.
func (f *Fetcher) sleepFor(sleepSecs int64) bool {
	select {
	case <-f.shutdown:
		return true
	case <-time.After(time.Duration(sleepSecs) * time.Second):
		return false
	}
}

// --------------------------------------------------------

func (f *Fetcher) Data() *priorityPRData {
	return &priorityPRData{
		c: f.prs,
	}
}

/*
// Data returns the channel on which fetched PRs are pushed
func (f *Fetcher) dataPipe() <-chan *priorityPR {
	return f.prs
}
*/

func (f *Fetcher) drainDataPipe() {
	Debug("Pull request fetcher: draining the channel")
	for {
		select {
		case _, isOpen := <-f.prs:
			// protect against already closed channel and the zero value
			if !isOpen {
				return
			}
		default:
			return
		}
	}
}

// --------------------------------------------------------

func (f *Fetcher) IsRunning() bool {
	return f.running
}

// --------------------------------------------------------

// Stop will set the Fetcher shutdown channel
func (f *Fetcher) Stop() error {
	if f.running {
		// only shutdown a running fetcher
		Info("Pull request fetcher: stopping")
		close(f.shutdown)
	}
	return nil
}

// --------------------------------------------------------

func (f *Fetcher) Done() <-chan struct{} {
	return f.done
}

// --------------------------------------------------------

// doFetch executes the meat of the fetching process, updating
// the FetcherCounts and Times structs with latest info, as well
// as pushing fetched pull requests onto the data channel for
// retrieval in main.go
func (f *Fetcher) doFetch() {
	done := make(chan struct{})
	defer close(done)

	cancelCxt, cancel := context.WithCancel(context.Background())

	// shut down the pull request fetching
	// if a shutdown request is detected
	go func() {
		defer cancel()
		select {
		case <-done:
		case <-f.shutdown:
		}
	}()

	fetched, err := getPRs(cancelCxt)

	if err != nil {
		switch err {
		case context.Canceled, context.DeadlineExceeded:
			// just return
			Debug("Context done, stopping fetching")
		default:
			Info("Problem fetching pull requests", ErrField(err))
			f.times.lastFailedFetch = time.Now().Unix()
			f.stats.nFailedFetches++
		}
		return
	}

	// TODO: push a metric somewhere???

	f.times.lastFetch = time.Now().Unix()
	f.stats.nFetches++

	pprs := prioritise(fetched)
	f.processFetchedPRs(cancelCxt, pprs)
}

// --------------------------------------------------------

func (f *Fetcher) processFetchedPRs(ctx context.Context, prs []*priorityPR) {
	if len(prs) == 0 {
		return
	}

	Debug("pull requests", logging.F("n", len(prs)))

	done := make(chan struct{})

	go func() {
		defer close(done)

		// remove obsolete pull requests from the local known list
		f.knownPRs.removeMissing(prs)

		var prsToSend []*priorityPR

		// TODO: what if there is a mix of new/old prs. At present
		// we just drop the old ones, is that correct?

		// identify any new/unseen pull requests
		if newPRs := f.knownPRs.filterNew(prs); len(newPRs) > 0 {
			// TODO: send metric # new prs
			prsToSend = f.processNewPRs(newPRs)
		} else {
			Debug("No new pull requests found")

			age := cfg.fetch.maxWaitNoNewPRs
			if oldPRs := f.knownPRs.olderThan(age); len(oldPRs) > 0 {
				prsToSend = f.processOldPRs(oldPRs)
			}
		}

		// Send on the fetcher data channel
		f.send(ctx, prsToSend)
	}()

	select {
	case <-ctx.Done():
		<-done
	case <-done:
	}
}

// --------------------------------------------------------

func (f *Fetcher) processNewPRs(prs []*priorityPR) []*priorityPR {
	f.times.lastNew = time.Now().Unix()
	f.stats.nNewPRs++

	// log the numbers of the new prs
	Info(
		"Found new pull requests",
		Field("n", len(prs)),
		Field("id", strings.Join(toPRNumbers(prs), ", ")),
	)

	f.knownPRs.add(prs)

	prs = f.retestMainBranch(prs)

	// TODO: send metric fetch time for new prs
	return prs
}

// --------------------------------------------------------

func (f *Fetcher) processOldPRs(prs []*priorityPR) []*priorityPR {
	Info(
		"Found old pull requests to re-test",
		Field("n", len(prs)),
		Field("id", strings.Join(toPRNumbers(prs), ", ")),
	)

	prs = f.retestMainBranch(prs)

	// reset the fetched time on these pull requests
	f.knownPRs.reset(prs)
	return prs
}

// --------------------------------------------------------

// retestMainBranch will add the main branch commit to the PRs
// for re-testing, if it previously failed
func (f *Fetcher) retestMainBranch(prs []*priorityPR) []*priorityPR {
	if !containsMainBranchPR(prs) {
		if main := f.knownPRs.mainBranchPR; main != nil && main.pr.pr.TestFailed() {
			Debug("Will re-test previously tested and failed main branch")
			prs = append(prs, main.pr)
		}
	}

	return prs
}

// --------------------------------------------------------

// send pushes the given prs onto the fetcher channel
func (f *Fetcher) send(ctx context.Context, prs []*priorityPR) {
	if len(prs) == 0 {
		return
	}

	Debug("Sending prs on data channel", logging.F("n", len(prs)))

	for _, pr := range prs {
		// TODO: this could potentially go through all the prs
		// without sending a single one...
		select {
		case <-ctx.Done():
			return
		case f.prs <- pr: // ok, buffer was not full and we could send it
			Debug(
				"Successfully sent the PR on the channel",
				logging.F("id", pr.pr.Number),
			)
		default: // wait to see if the channel can be emptied a bit
			Debug("Data channel is full, sleeping for a second")
			time.Sleep(time.Second)
		}
	}

	f.times.lastSend = time.Now().Unix()
	f.stats.nSends++
}

// --------------------------------------------------------
// --------------------------------------------------------
// --------------------------------------------------------

// priorityPR combines a pull request with a priority
type priorityPR struct {
	priority int
	pr       *pullrequest.PR
}

// --------------------------------------------------------

// prioritise creates a list of priorityPR structs
// based on the status of each of the input PR.
// priority is a positive integer where bigger means higher priority.
// Higher priority pull requests will be tested first.
func prioritise(prs []*pullrequest.PR) []*priorityPR {
	pprs := []*priorityPR{}
	for _, pr := range prs {
		// default, lowest priority = 0
		ppr := priorityPR{priority: 0, pr: pr}

		if !pr.IsReviewed() {
			Debug("Ignoring unreviewed pull request", Field("id", pr.Number))
			continue
		}

		if pr.IsMainBranch {
			ppr.priority = 3
		} else {
			if !pr.IsTested() {
				ppr.priority = 2
			} else if pr.TestFailed() {
				ppr.priority = 1
			}
		}

		pprs = append(pprs, &ppr)
	}
	return pprs
}

// --------------------------------------------------------

// knownPR is a previously fetched pull request
type knownPR struct {
	fetched int64
	pr      *priorityPR
}

// olderThan determines if this knownPR was fetched more than
// the given number of seconds ago
func (k *knownPR) olderThan(secs int64) bool {
	return time.Now().Unix()-k.fetched > secs
}

// --------------------------------------------------------

// knownPRs is a list of previously fetched pull requests
type knownPRs struct {
	prs          map[prKey]knownPR
	mainBranchPR *knownPR
}

func (k *knownPRs) String() string {
	o := []string{}
	for key, val := range k.prs {
		o = append(
			o,
			fmt.Sprintf("%d/%s => %d, %v", key.number, key.sha, val.fetched, val.pr),
		)
	}

	return strings.Join(o, "\n")
}

// add will append newly fetched pull requests to the list
// of previously fetched pull requests (excluding those already known)
func (k *knownPRs) add(pprs []*priorityPR) {
	if k.prs == nil {
		// maps must be initialised before we can add to them
		k.prs = make(map[prKey]knownPR)
	}
	// Outside the for loop to ensure all added prs have same fetched time
	now := time.Now().Unix()

	for _, ppr := range pprs {
		key := prKey{number: ppr.pr.Number, sha: ppr.pr.SHA}

		if _, found := k.prs[key]; !found {
			kpr := knownPR{
				fetched: now,
				pr:      ppr,
			}

			// Can this get added > 1 by error?
			if ppr.pr.IsMainBranch {
				k.mainBranchPR = &kpr
			}

			k.prs[key] = kpr
		}
	}
}

// removeExisting will delete all pull requests found in the
// list that are already known
func (k *knownPRs) removeExisting(pprs []*priorityPR) {
	for _, ppr := range pprs {
		key := k.makeKey(ppr)
		delete(k.prs, key) // no-op if key not present
	}
}

// removeMissing will delete all known pull requests not found
// in the provided slice
func (k *knownPRs) removeMissing(prs []*priorityPR) {
	/* Each time we call the pullrequest.fetch() method, it should
	   return all open, reviewed pull requests. This list will include
	   some mix of unseen and previously seen requests. If there are
	   previously seen pull requests that are not in the newly retrieved list,
	   this would imply that those missing have been closed, un-reviewed...
	   in any case, we no longer need to track them and we remove them
	   from our local list of known pull requests.
	*/

	fetchedKeys := func(prs []*priorityPR) prKeys {
		var keys prKeys
		for _, pr := range prs {
			keys = append(keys, k.makeKey(pr))
		}
		return keys
	}(prs)

	for prKey := range k.prs {
		if !fetchedKeys.contains(prKey) {
			delete(k.prs, prKey)
		}
	}
}

// reset will mark the fetched time for each pr in the provided
// slice as "now".
func (k *knownPRs) reset(prs []*priorityPR) {
	now := time.Now().Unix()
	for _, pr := range prs {
		key := k.makeKey(pr)

		knownPR, found := k.prs[key]
		if found {
			knownPR.fetched = now
			k.prs[key] = knownPR
		}
	}
}

// makeKey creates a prKey struct instance from the given pull request
func (k *knownPRs) makeKey(pr *priorityPR) prKey {
	return prKey{
		number: pr.pr.Number,
		sha:    pr.pr.SHA,
	}
}

// isKnown checks if a given pull request is in the known list
func (k *knownPRs) isKnown(pr *priorityPR) bool {
	key := k.makeKey(pr)
	_, found := k.prs[key]
	return found
}

// filterNew returns those pull requests in the provided slice that are new
func (k *knownPRs) filterNew(prs []*priorityPR) []*priorityPR {
	newPRs := []*priorityPR{}
	for _, pr := range prs {
		if !k.isKnown(pr) {
			newPRs = append(newPRs, pr)
		}
	}

	return newPRs
}

// olderThan returns all known pull requests that were fetched further
// back in time than the given number of seconds
func (k *knownPRs) olderThan(age int64) []*priorityPR {
	var oldPRs []*priorityPR

	for _, known := range k.prs {
		if known.olderThan(age) {
			oldPRs = append(oldPRs, known.pr)
		}
	}

	return oldPRs
}

// --------------------------------------------------------

type prKeys []prKey

func (p prKeys) contains(tgt prKey) bool {
	for _, key := range p {
		if key.isSame(tgt) {
			return true
		}
	}

	return false
}

// --------------------------------------------------------

// prKey is used to identify a given pull request within the knownPRs list
type prKey struct {
	number int
	sha    string
}

// isSame checks if two keys represent the same pull request
func (p *prKey) isSame(other prKey) bool {
	return (p.sha == other.sha) && (p.number == other.number)
}

func (p *prKey) String() string {
	return fmt.Sprintf("%d -- %s", p.number, p.sha)
}

// --------------------------------------------------------

func containsMainBranchPR(pprs []*priorityPR) bool {
	for _, ppr := range pprs {
		if ppr.pr.IsMainBranch {
			return true
		}
	}

	return false
}

// --------------------------------------------------------

func toPRNumbers(prs []*priorityPR) []string {
	numbers := []string{}
	for _, pr := range prs {
		numbers = append(numbers, strconv.Itoa(pr.pr.Number))
	}

	return numbers
}

// --------------------------------------------------------

// Decide whether to accept a given pull request
var acceptPR = func(pr *pullrequest.PR) bool {
	first := string(pr.SHA[0])
	base := 16
	bitSize := 32
	index, err := strconv.ParseInt(first, base, bitSize)
	if err != nil {
		Info(
			"Unable to convert sha first char to int, will not accept PR",
			Field("sha", pr.SHA),
			Field("pr", pr.Number),
		)
	}
	index = index % int64(cfg.worker.poolSize)
	return index == int64(cfg.worker.index)
}

// --------------------------------------------------------

// getPRs fetches the open pull requests from the user
// configured Github repo and branch, and returns a list
// of those that pass the acceptPR() filter criteria.
// It bails early if the context is marked done.
func getPRs(ctx context.Context) ([]*pullrequest.PR, error) {
	// cfg is defined in config.go
	fetcher := pullrequest.NewFetcher(cfg.pr.repo)
	fetcher.SetBranch(cfg.pr.branch)
	fetcher.SetTrustedUsers(cfg.trust.users)
	fetcher.SetTrustCollaborators(cfg.trust.collaborators)
	fetcher.SetShowMainBranch(cfg.fetch.showMainBranch)

	prs, err := fetcher.FetchWithContext(
		ctx,
		cfg.buildInfo.checkName,
		cfg.buildInfo.checkNameStatus,
	)

	if err != nil {
		return nil, err
	}

	accepted := prs[:0]
	for _, pr := range prs {
		if acceptPR(pr) {
			accepted = append(accepted, pr)
		}
	}

	return accepted, nil
}
