package prbuild

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brinick/ali-ci/task"
	"github.com/brinick/github/object"
	"github.com/brinick/logging"
)

const (
	// TaskName is the name of this task
	TaskName = "prbuild"

	// TaskDesc describes succintly what this task does
	TaskDesc = "Builds Github pull requests"
)

// Logging shortcuts for the package
var (
	Debug = logging.Debug
	Info  = logging.Info
	Error = logging.Error
	Field = logging.F

	ErrField = func(e error) logging.Field {
		return logging.F("err", e)
	}

	// Background-running services to which we send data
	services = backgroundServices{
		"monalisa": &backgroundService{
			c: make(chan channelData, 50),
			svc: newMonalisaSvc(
				cfg.service.monalisa.host,
				cfg.service.monalisa.port,
			),
			timeout: cfg.timeout.short,
		},
		"influxdb": &backgroundService{
			c: make(chan channelData, 50),
			svc: newInfluxDBSvc(
				hostName(),
				gitHash(),
				cfg.buildInfo.checkName,
				cfg.service.influxdb.writeURL,
				cfg.service.influxdb.allowInsecureCurl,
				cfg.service.influxdb.maxCurlTime,
			),
		},
		"analytics": &backgroundService{
			c: make(chan channelData, 50),
			svc: newAnalyticsSvc(
				cfg.architecture,
				cfg.service.analytics.id,
				cfg.service.analytics.appName,
				cfg.service.analytics.appVers,
				cfg.service.analytics.maxCurlTime,
			),
			timeout: cfg.timeout.short,
		},
		"coverage": &backgroundService{
			c: make(chan channelData, 50),
			svc: newCoverageSvc(
				cfg.service.coverage.url,
				cfg.service.coverage.maxCurlTime,
				cfg.service.coverage.sources,
				cfg.service.coverage.filename,
				cfg.service.coverage.infoBaseDir,
			),
			timeout: cfg.timeout.short,
		},
	}
)

// ------------------------------------------------------------------
// ------------------------------------------------------------------
// ------------------------------------------------------------------

// NewTask constructs a new prbuild task and returns it
func NewTask() *Task {
	t := &Task{
		Task:    *task.NewTask(TaskName, TaskDesc),
		fetcher: NewFetcher(),
		builder: NewBuilder(),
	}

	t.PreExecutor = t
	t.Executor = t
	t.WorkFinder = t
	return t
}

// ------------------------------------------------------------------

// Task is the main data structure for this pull request building task
type Task struct {
	task.Task
	fetcher prFetcher
	builder prBuilder

	// the priority queue of fetcher retrieved PRs
	queue *priorityQueue

	// the currently being built priority queue item
	current *priorityQueueItem

	// epoch of the last time PRs were retrieved
	// on the fetcher queue
	last int64
}

// ------------------------------------------------------------------

// FindWork checks if the task has work it could do,
// and updates the HaveWork boolean attribute accordingly
// It is run during state Seek. It will abort its action if
// the context is cancelled.
func (t *Task) FindWork(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			Info("FindWork: recovering from panic", logging.F("r", r))
			select {
			case <-ctx.Done():

			}
			logging.Debug("FindWork: stopping background pull request fetching")
			t.fetcher.Stop()
			<-t.fetcher.Done()
		}
	}()

	// Is a no-op if already running
	t.fetcher.Start()

	prs := t.fetcher.Data().read()
	t.HaveWork = (len(prs) > 0)

	// TODO: push prs back on queue

	select {
	case <-ctx.Done():
		// If the context is cancelled, it's likely that we are
		// changing task state meaning we should shut down everything
		// related to finding work
		t.HaveWork = false
		t.fetcher.Stop()
		<-t.fetcher.Done()
	default:
		//
	}
}

// ------------------------------------------------------------------

// PreExec runs setup code before the real task run
func (t Task) PreExec(ctx context.Context) {
	// Init the logging

	done := make(chan struct{})
	defer close(done)

	setAliBotEnvVars()
	setAlidistRef()
	setAlidistGithubStatus(t.Abort())

	// Launch our background services
	for _, svc := range services {
		svc.Launch()
	}

	// TODO: where else do we abort services? why only here?

	// Shutdown the services if the passed in context is done
	go func() {
		select {
		case <-ctx.Done():
			for _, svc := range services {
				svc.SetAbort()
			}
		case <-done:
			return
		}
	}()

	// NOTE: these add the data to each service's data channel, and
	// then return. There is no guarantee that the sends will have completed
	// before we exit this function. If this is a problem, then a sync waitgroup
	// should be added.
	services["monalisa"].send((&monalisaData{}).prChecker("state", "started"))
	services["analytics"].send((&analyticsData{}).screenview("started"))
	services["influxdb"].send(influxDBData{state: "started"})
}

// ------------------------------------------------------------------

// Exec performs the meat of the pull request building activity
// It will exit if an external task abort is detected (the done channel
// is then closed), if the context is cancelled or if the task decides
// to stop running for now (because no pull requests have been available
// in the past N minutes)
func (t Task) Exec(ctx context.Context) {
	workerDone := make(chan struct{})
	panicked := make(chan struct{})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := "prbuild main.run() function panicked, " +
					" will clean up the current pull request build and restart"
				Error(msg, Field("recover", r))

				close(panicked)
			} else {
				close(workerDone)
			}
		}()

		// The meat of our task. It blocks until it, or the context, is done.
		t.run(ctx)
	}()

	// Wait until:
	// - context is done, or
	// - inner worker is done, or
	// - inner worker panicked
	select {
	case <-ctx.Done():
		select {
		case <-workerDone:
			return
		case <-panicked:
			// Make sure we shutdown the builder
			if t.builder.isActive() {
				t.builder.Stop()
			}
			return
		}

	case <-workerDone:
		// No pull requests in the past N minutes, the inner worker
		// has stopped for now.
		return

	case <-panicked:
		// clean up the build that may have been the source of
		// the panic, and restart
		if t.builder.isActive() {
			t.builder.Stop()
		}
		t.Exec(ctx)
	}
}

// ------------------------------------------------------------------

// run executes the main loop for the task, only returning when
// a shutdown is requested, the context is cancelled, or there have been
// no new pull requests for a given, configurable, period of time
func (t *Task) run(ctx context.Context) {
	var (
		// Number of times fetchNewPRs has been
		// called (used by ONESHOT)
		nCallsFetchPRs int
	)

	// Launch the fetcher in the background
	Debug("Starting background pull request fetcher")
	t.fetcher.Start()

	// Loop for ever, stopping if:
	// 1. No new or old PRs found within the past N minutes (cf. config.go)
	// 2. The task Abort() channel is closed
outer:
	for {
		// Fetch new PRs off the fetcher channel if necessary, and
		// push them onto the local priority queue. If there are no
		// prs *and* it has been too long since we last found any,
		// we exit the task
		if t.shouldRetrievePRs(nCallsFetchPRs) {
			if err := t.loadNextPRs(ctx); err != nil {
				// Clean up before leaving
				if t.builder.isActive() {
					t.builder.Stop()
				}
				break outer
			}
		}

		if next := t.nextPR(); next != nil {
			t.current = next
			t.builder.Start(t.current.pr)
		}

		select {
		case <-t.Abort():
			Info("Task abort is requested, stopping pull request building")
			if t.builder.isActive() {
				t.builder.Stop()
			}

			t.fetcher.Stop()
			t.SetDone()
			break outer

		case <-ctx.Done():
			if t.builder.isActive() {
				t.builder.Stop()
			}
			break outer

		case <-time.After(5 * time.Second):
			// go round the loop once more
		}
	}
}

// ------------------------------------------------------------------

func (t *Task) haveWaitedTooLong() bool {
	// Check how long since we last found any pull requests to test.
	// If this is longer than maxWaitNoPRs, stop running
	if t.last <= t.State.When {
		// we haven't yet had any data, use the task start time instead
		t.last = t.State.When
	}
	return time.Now().Unix()-t.last > int64(cfg.fetch.maxWaitNoPRs)
}

// ------------------------------------------------------------------

func (t *Task) shouldRetrievePRs(nCalls int) bool {
	return !cfg.oneShot || nCalls < 1
}

// ------------------------------------------------------------------

// loadNextPRs fetches any pull requests from the fetcher data pipe,
// and pushes them onto the priority queue. If no pull requests are
// found and if the last time we did find any was greater than some
// configurable amount, then we stop the builder and exit with an error.
// An error is also returned if the context is done.
func (t *Task) loadNextPRs(ctx context.Context) error {
	prs := retrievePRs(t.fetcher.Data())

	// bail if the context was cancelled
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		// continue
	}

	// TODO: this function does too much

	if len(prs) > 0 {
		t.last = time.Now().Unix()
		if t.queue == nil {
			// creates and adds the prs to the queue
			t.queue = createPriorityQueue(prs)
		} else {
			t.queue.PushAll(prs)
		}
	} else {
		if t.haveWaitedTooLong() {
			if t.builder.isActive() {
				t.builder.Stop()
			}

			return fmt.Errorf(
				"No pull requests in the past %d seconds",
				cfg.fetch.maxWaitNoPRs,
			)
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// ------------------------------------------------------------------

// TODO: does this need a context passed to it??? or is it fast enough?
func (t *Task) nextPR() *priorityQueueItem {
	Info("Grabbing the next priorityQueueItem")
	if t.builder.isActive() {
		return t.currentlyBuilding()
	}

	return t.notCurrentlyBuilding()
}

// ------------------------------------------------------------------

// haveHigherPriorityPR indicates if the next pull request
// in the queue has a higher priority than the currently building one
func (t *Task) haveHigherPriorityPR() bool {
	nextPriority := t.queue.Next().(*priorityQueueItem).priority
	return nextPriority > t.current.priority
}

// ------------------------------------------------------------------

// popQueue pops the next item from the priority queue
func (t *Task) popQueue() *priorityQueueItem {
	return t.queue.Pop().(*priorityQueueItem)
}

// ------------------------------------------------------------------

func (t *Task) notCurrentlyBuilding() *priorityQueueItem {
	if t.queue.Empty() {
		Debug("PR queue empty, nothing to do")
		return nil
	}

	return t.popQueue()
	// log.WithField("pr", currentQueueItem.pr.Number).Info("Found item")
}

// ------------------------------------------------------------------

func (t *Task) currentlyBuilding() *priorityQueueItem {
	if t.queue.Empty() || !t.haveHigherPriorityPR() {
		// log.WithFields(log.Fields{
		// 	"pr": currentQueueItem.pr.Number,
		//	}).Debug("PR queue empty, keep testing")
		return nil
	}

	t.builder.Stop()

	// put back the stopped build pr for another time
	t.queue.Push(t.current)

	return t.popQueue()
}

// ------------------------------------------------------------------

// Stop will shut down the spawned fetcher and builder routines
func (t *Task) Stop() {
	if t.builder.isActive() {
		t.builder.Stop()
	}
	// stop the fetcher
	t.fetcher.Stop()
}

// ------------------------------------------------------------------
// ------------------------------------------------------------------
// ------------------------------------------------------------------

// retrievePRs grabs any prs from the prs channel
// and creates priorityQueueItems from them
func retrievePRs(channel *priorityPRData) []*priorityQueueItem {
	data := []*priorityQueueItem{}

	for _, pr := range channel.read() {
		data = append(
			data,
			&priorityQueueItem{
				pr:       pr.pr,
				priority: pr.priority,
			})
	}

	Error("retrievePRs, returning data", logging.F("N", len(data)))
	return data
}

/*
// retrievePRs grabs any prs from the prs channel
// and creates priorityQueueItems from them
func retrievePRs(ctx context.Context, prs <-chan *priorityPR) []*priorityQueueItem {
	data := []*priorityQueueItem{}

outer:
	for {
		select {
		case pr, channelIsOpen := <-prs:
			// protect against the channel nil zero value on channel closing
			if !channelIsOpen {
				break outer
			}
			item := &priorityQueueItem{
				pr:       pr.pr,
				priority: pr.priority,
			}
			data = append(data, item)
		case <-ctx.Done():
			break outer
		default:
			// no (more) data
			break outer
		}
	}

	return data
}
*/

// ------------------------------------------------------------------

// setAliBotEnvVars dumps some analytics variables to the environment
func setAliBotEnvVars() {
	os.Setenv("ALIBOT_ANALYTICS_ID", cfg.service.analytics.id)
	os.Setenv("ALIBOT_ANALYTICS_USER_UUID", cfg.service.analytics.userID)
	os.Setenv("ALIBOT_ANALYTICS_ARCHITECTURE", cfg.architecture)
	os.Setenv("ALIBOT_ANALYTICS_APP_NAME", cfg.service.analytics.appName)
}

// ------------------------------------------------------------------

// setAlidistRef gets the head ref of the local alidist repo
// and puts it in the configuration parameter cfg.alidst.ref
func setAlidistRef() {
	repo := gitRepo{path: "alidist"}
	alidistRef, err := repo.headRef()
	if err != nil {
		here, e := os.Getwd()
		if e != nil {
			// an error occured while processing the error...
			Error("Unable to get the current directory", ErrField(e))
			here = ""
		}

		fullpath := filepath.Join(here, "alidist")
		Error(
			"Unable to get the local alidist repo HEAD ref",
			ErrField(err),
			Field("path", fullpath),
		)
	}

	Info("Setting alidist head reference", logging.F("sha", alidistRef))
	cfg.alidist.ref = alidistRef
}

// ------------------------------------------------------------------

func setAlidistGithubStatus(abort <-chan struct{}) {
	// Set the alidist github status for this head commit to pending
	// TODO: why do we do this?

	var err error

	// Add both a timeout and cancel context, the latter
	// being cancelled if the build is aborted
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := time.Duration(cfg.timeout.short) * time.Second
	ctxTimeout, cancelTimeout := context.WithTimeout(ctx, d)
	defer cancelTimeout()

	done := make(chan struct{})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				Info(
					"Worker routine panicked",
					Field("recover", r),
					Field("name", "setAlidistGithubStatus"),
				)
			}
			close(done)
		}()

		repo := object.NewRepoFromPath(cfg.alidist.repo)
		commit, _ := repo.Commit(cfg.alidist.ref)

		_, err = commit.SetStatusWithContext(
			ctxTimeout,
			&object.CommitStatus{
				State:   "pending",
				Context: cfg.buildInfo.checkName,
			},
		)
	}()

	select {
	case <-abort:
		Info("Task abort request found, cancelling context")
		cancel()
	case <-done:
		Info(
			"Unable to set alidist HEAD Github status",
			ErrField(err),
			Field("repo", cfg.alidist.repo),
			Field("sha", cfg.alidist.ref),
			Field("checkName", cfg.buildInfo.checkName),
		)
	}
}

// ------------------------------------------------------------------

func hostName() string {
	var (
		name string
		err  error
	)

	if name, err = os.Hostname(); err != nil {
		Info("Unable to get hostname, setting to empty string", ErrField(err))
	}

	return name
}

// ------------------------------------------------------------------

// gitHash gets the HEAD sha of the current local repo for this code
func gitHash() string {
	return "v1.0"
	/*
		ex, err := os.Executable()
		if err != nil {
			panic(
				fmt.Sprintf("Unable to grab the Git HEAD sha for the local repo (%s)", err),
			)
		}

		// TODO: this will make no sense in a single binary executable...
		repo := gitRepo{path: filepath.Dir(ex)}
		sha, err := repo.headRef()
		if err != nil {
			panic(
				fmt.Sprintf("Unable to grab the Git HEAD sha for the local repo (%s)", err),
			)
		}

		return sha
	*/
}

// ------------------------------------------------------------------
