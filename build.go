package prbuild

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brinick/ali-build"
	"github.com/brinick/ali-ci/pullrequest"
	"github.com/brinick/github/object"
	"github.com/brinick/logging"
	"github.com/brinick/shell"
)

// ------------------------------------------------------------------

// Build wraps the instructions for building a pull request
type Build struct {

	// pr is the pull request to build
	pr *pullrequest.PR

	// baseSHA is the reference of the code into which the
	// pull request code will be merged
	baseSHA string

	// epoch when this Build struct was instantiated
	created int64

	// epoch when the pull request building was launched
	started int64

	// epoch when the build completed, or was shut down
	finished int64

	// channel used by the build to say it's done
	done chan struct{}

	// channel used by parent to stop this build
	abort chan struct{}

	// Contains any "mechanical" build error, that is:
	// problems arising from trying to merge the PR, run
	// aliBuild doctor/build. It is not for errors simply
	// because aliBuild failed due to a problem with the PR code etc.
	err error

	// the alibuild instance used for doing the build
	aliBuild *alibuild.AliBuild
}

// ShutdownError represents the error returned if an abort request is made
type ShutdownError struct{ error }

// ------------------------------------------------------------------
// ------------------------------------------------------------------
// ------------------------------------------------------------------

// NewBuild creates a Build instance for the given pull request
// func NewBuild(pr *pullrequest.PR) *Build {
func NewBuild(pr PullRequestor) *Build {
	return &Build{
		pr:      pr,
		created: now(),
		done:    make(chan struct{}),
		abort:   make(chan struct{}),
		aliBuild: alibuild.New(
			cfg.alibuild.executable,
			pr.PackageName,
			[]string{
				"GITLAB_USER=",
				"GITLAB_PASS=",
				"GITHUB_TOKEN=",
				"INFLUXDB_WRITE_URL=",
				"CODECOV_TOKEN=",
			},
		),
	}
}

// ------------------------------------------------------------------

func (b *Build) Error() error {
	return b.err
}

// ------------------------------------------------------------------

// HasError checks if the build has an error
func (b *Build) HasError() bool {
	return b.err != nil
}

// ------------------------------------------------------------------

// SetDone
func (b *Build) SetDone() {
	close(b.done)
}

// ------------------------------------------------------------------

func (b *Build) Running() bool {
	return b.started > 0 && !b.Done()
}

// ------------------------------------------------------------------

// Done indicates if the given build has finished
func (b *Build) Done() bool {
	if b.finished == 0 {
		return false
	}

	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// ------------------------------------------------------------------

// start sets up and launches the build of a given pull request.
// An error is returned if the build was already started.
func (b *Build) start() {
	if b.Running() || b.Done() {
		b.err = BuildAlreadyStartedError(b.started)
		return
	}

	Debug(
		"Starting build",
		Field("pr", b.pr.Number),
		Field("repo", b.pr.RepoPath),
	)

	b.started = now()
	go b.exec()
}

// ------------------------------------------------------------------

func (b *Build) stop() {
	if !b.Running() {
		b.err = BuildNotRunningError()
		return
	}

	// Trigger the build shutdown in the exec() method
	close(b.abort)

	// TODO: can this hang for a long while?
	<-b.done
}

// ------------------------------------------------------------------
// exec is where the meat of the build happens:
// Setting up the environment, running aliDoctor and aliBuild, and then
// pushing a coverage report (assuming the build was successful).
func (b *Build) exec() {
	defer func() {
		if r := recover(); r != nil {
			Error(
				"build.exec function panicked",
				Field("pr", b.pr.Number),
				Field("repo", b.pr.RepoPath),
				Field("panic", r),
			)
		}

		b.finished = now()
		b.SetDone()
	}()

	// md := monalisaData{}
	services["monalisa"].send((&monalisaData{}).prChecker("state", "pr_processing"))
	services["analytics"].send((&analyticsData{}).screenview("pr_processing"))
	services["influxdb"].send(influxDBData{state: "pr_processing"})

	Info("Launching build", Field("pr", b.pr.Number), Field("repo", b.pr.RepoPath))

	b.runAliBuild()
	/*
		if b.init() {

				if b.runAliDoctor() {
					if b.runAliBuild() {
						Info("Pushing coverage report")
						services["coverage"].send(coverageData{
							commitSHA: b.pr.SHA,
							prBranch:  b.pr.Name,
							prNumber:  b.pr.Number,
						})
					}
				}
		}
	*/
}

// ------------------------------------------------------------------

type CodeBuilder interface {
	Clean(bool)
	Doctor()
	Build()
	HasFetchReposOption() bool
}

func (b *Build) init() bool {
	// Clean build directories to avoid space usage explosion
	// and inconsistent build results due to previous artifacts
	b.aliBuild.Clean(cfg.alibuild.debug)

	var (
		err      error
		repos    []gitRepo
		root     = "."
		maxdepth = 2
		ignore   = []string{"ali-bot"}
	)

	if repos, err = findLocalRepos(root, maxdepth, ignore); err != nil {
		Info(
			"Unable to find local Git dirs",
			Field("root", root),
			ErrField(err),
		)
		return false
	}

	// TODO: we need to have a context in here somewhere to cancel if the build is stopped
	return updateLocalRepos(repos) && b.updatePRRepo()
}

// ------------------------------------------------------------------

func (b *Build) updatePRRepo() bool {
	defer os.Chdir(func() string {
		p, _ := os.Getwd()
		Debug("Changing directory", logging.F("dir", p))
		return p
	}())

	Debug("Changing directory", logging.F("dir", cfg.pr.repoCheckout))

	os.Chdir(cfg.pr.repoCheckout)

	Info("Updating pr_repo", logging.F("repo", cfg.pr.repo))

	var r *shell.Result

	git.config(
		fmt.Sprintf(
			"--add remote.%s.fetch +refs/pull/*/head:refs/remotes/%s/pr/*",
			cfg.pr.remote,
			cfg.pr.remote,
		),
	)

	// Only fetch destination branch for PRs (for merging),
	// and the PR we are checking now
	prBranch := fmt.Sprintf(
		"+%s:refs/remotes/%s/%s",
		cfg.pr.branch,
		cfg.pr.remote,
		cfg.pr.branch,
	)

	if r = git.fetch(cfg.pr.remote, prBranch, cfg.timeout.short); r.IsError() {
		Info(
			"Unable to fetch remote refs, ignoring problem",
			Field("branch", prBranch),
			ErrField(r.Error),
		)
		git.DropError()
	}

	if b.pr.Number > 0 {
		prBranch := fmt.Sprintf("+pull/%d/head", b.pr.Number)
		if r = git.fetch(cfg.pr.remote, prBranch, cfg.timeout.short); r.IsError() {
			Info(
				"Unable to fetch remote refs, ignoring problem",
				Field("branch", prBranch),
				ErrField(r.Error),
			)
			git.DropError()
		}
	}

	// Reset to branch target of PRs
	git.reset(fmt.Sprintf("--hard %s/%s", cfg.pr.remote, cfg.pr.branch))
	git.clean("-fxd")

	// Before we do the merge, let's grab the HEAD hash which we will store in baseSHA
	if r = git.headRef(); r.IsError() {
		Info("Unable to get the HEAD base sha, stopping build", ErrField(r.Error))
		return false
	}

	// Grab stdout as text, and true = remove trailing line char
	b.baseSHA = r.Stdout.Text(true)

	// Now we get the size of the tree below current dir = pre-merge size
	root, _ := os.Getwd()
	ignoreDirs := []string{".git"}
	preMergeSize, _ := shell.DirTreeSize(root, ignoreDirs)

	// Do the merge
	r = git.merge(b.pr.SHA, "--no-edit")

	// Clean up whatever happens with the merge
	git.reset("--hard HEAD")
	git.clean("-fxd")

	// Merge failed, report this fact and leave
	if r.IsError() {
		Info("Git merge failed")
		b.handleMergeFailed(r)
		return false
	}

	Debug("Git merge succeeded")

	// Merge OK, grab the post-merge tree size
	postMergeSize, _ := shell.DirTreeSize(root, ignoreDirs)

	// Check the pre- vs post- merge size diff and reject the PR if too big
	diffMergeSize := postMergeSize - preMergeSize
	if diffMergeSize > int64(cfg.pr.maxMergeDiffSize) {
		Info(
			"Pull request exceeds maximum allowed merge size",
			Field("diff", diffMergeSize),
			Field("max", cfg.pr.maxMergeDiffSize),
		)

		b.handleMergeTooBig(diffMergeSize)
		return false
	}

	return true
}

// ------------------------------------------------------------------

// handleMergeFailed handles the case where of a failed attempt to merge
// the pull request into the test area.
func (b *Build) handleMergeFailed(result *shell.Result) {
	var err error

	sendTo(Info, result.Stderr.Lines())

	// ----------------------------------------------------
	// Set a Github status for this commit
	// ----------------------------------------------------
	commit, _ := b.pr.Commit()
	err = b.setGithubStatus(commit, "Cannot merge PR into test area")
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to set the Github status, will report to analytics", ErrField(err))

	// ----------------------------------------------------
	// Set Github status failed, report to analytics
	// ----------------------------------------------------
	err = b.reportAnalytics("setGithubStatus failed on cannot merge")
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to report to analytics", ErrField(err))
}

// ------------------------------------------------------------------

// handleMergeTooBig handles the case where the git merge succeeded,
// however the post-merge size is too big compared to the pre-merge size.
func (b *Build) handleMergeTooBig(diff int64) {
	var err error

	// ----------------------------------------------------
	// Set a Github status for this commit
	// ----------------------------------------------------
	commit, _ := b.pr.Commit()
	err = b.setGithubStatus(commit, "Diff too big. Rejecting.")
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to set the Github status, will report to analytics", ErrField(err))

	// ----------------------------------------------------
	// Set Github status failed, report to analytics
	// ----------------------------------------------------
	err = b.reportAnalytics("setGithubStatus failed on cannot merge")
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to report to analytics", ErrField(err))

	// ----------------------------------------------------
	// Report PR error to Github
	// ----------------------------------------------------
	msg := "Your pull request exceeded the allowed size. " +
		"If you need to commit large files, " +
		"[have a look here]" +
		"(http://alisw.github.io/git-advanced/#how-to-use-large-data-files-for-analysis)."

	err = b.reportPRErrors(b.pr, cfg.buildInfo.checkName, msg, false)
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to report pull request error to Github")

	// ----------------------------------------------------
	// Report PR error failed, report to analytics
	// ----------------------------------------------------
	err = b.reportAnalytics("reportPRErrors failed on merge diff too big")
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to report to analytics", ErrField(err))
}

// ------------------------------------------------------------------

func (b *Build) setGithubStatus(commit *object.RepoCommit, msg string) error {
	var err error

	// Add both a timeout and cancel context, the latter
	// being cancelled if the build is aborted
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := time.Duration(cfg.timeout.short) * time.Second
	ctxTimeout, cancelTimeout := context.WithTimeout(ctx, d)
	defer cancelTimeout()

	done := make(chan struct{})

	Debug("Launching worker goroutine", Field("name", "setGithubStatus"))

	// Launch the work in the background
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Info(
					"Worker routine panicked",
					Field("recover", r),
					Field("name", "setGithubStatus"),
				)
			}
			close(done)
		}()

		// TODO: check this works
		_, err = commit.SetStatusWithContext(
			ctxTimeout,
			&object.CommitStatus{
				State:       "error",
				Description: msg,
				Context:     cfg.buildInfo.checkName,
			},
		)
	}()

	// Wait for either a build abort, or the worker to complete
	select {
	case <-b.abort:
		Info("Build abort received, will cancel setGithubStatus")
		cancel()
		err = ShutdownError{}
	case <-done:
	}

	return err
}

// ------------------------------------------------------------------

func (b *Build) reportAnalytics(msg string) error {
	var err error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := time.Duration(cfg.timeout.short) * time.Second
	ctxTimeout, cancelTimeout := context.WithTimeout(ctx, d)
	defer cancelTimeout()

	done := make(chan struct{})

	Debug("Launching worker goroutine", Field("name", "reportAnalytics"))

	// Launch the work in the background
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Info(
					"Worker routine panicked",
					Field("recover", r),
					Field("name", "reportAnalytics"),
				)
			}
			close(done)
		}()

		services["analytics"].sendWithContext(
			ctxTimeout,
			(&analyticsData{}).exception(msg, false),
		)
	}()

	// Wait for either a build abort, or the worker to complete
	select {
	case <-b.abort:
		Info("Build abort received, will cancel sending to analytics")
		cancel()
		err = ShutdownError{}
	case <-done:
	}

	return err
}

// ------------------------------------------------------------------

func (b *Build) reportPRErrors(pr *pullrequest.PR, checkName, msg string, noComments bool) error {
	var err error

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := time.Duration(cfg.timeout.short) * time.Second
	ctxTimeout, cancelTimeout := context.WithTimeout(ctx, d)
	defer cancelTimeout()

	done := make(chan struct{})

	Debug("Launching worker goroutine", Field("name", "reportPRErrors"))

	go func() {
		defer func() {
			if r := recover(); r != nil {
				Info(
					"Worker routine panicked",
					Field("recover", r),
					Field("name", "reportPRErrors"),
				)
			}
			close(done)
		}()

		err = reportPRErrorsWithContext(ctxTimeout, pr, checkName, msg, noComments)
	}()

	// Wait for either a build abort, or the worker to complete
	select {
	case <-b.abort:
		Info("Build abort received, will cancel sending to analytics")
		cancel()
		err = ShutdownError{}
	case <-done:
	}

	return err
}

// ------------------------------------------------------------------

func (b *Build) runAliDoctor() bool {
	defaults := cfg.alibuild.defaults
	if defaults != "" {
		defaults = fmt.Sprintf("--defaults %s", defaults)
	}

	Debug("Running aliBuild doctor command")
	result := b.aliBuild.Doctor(
		defaults,
		shell.Timeout(cfg.timeout.short),
		shell.Cancel(b.abort),
	)

	if !result.IsError() {
		sendTo(Debug, result.Stdout.Lines())
		return true
	}

	b.handleAliDoctorFailed(result)
	return false
}

// ------------------------------------------------------------------

func (b *Build) handleAliDoctorFailed(result *shell.Result) {
	switch {
	case result.Cancelled:
		Info(
			"aliBuild doctor command was aborted",
			Field("pr", b.pr.Number),
			Field("repo", b.pr.RepoPath),
		)
		return

	case result.TimedOut:
		Info(
			"aliBuild doctor command timedout",
			Field("pr", b.pr.Number),
			Field("repo", b.pr.RepoPath),
		)
		return
	}

	var err error

	// TODO: alidoctor by default outputs all text to stderr, even non-errors?

	Error("aliBuild doctor failed - stderr:\n" + result.Stderr.Text(false))
	// sendTo(Error, result.Stderr.Lines())

	// ----------------------------------------------------
	// Set a Github status for this commit
	// ----------------------------------------------------
	commit, _ := b.pr.Commit()
	err = b.setGithubStatus(commit, "aliBuild doctor error")
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to set the Github status, will report to analytics", ErrField(err))

	// ----------------------------------------------------
	// Set Github status failed, report to analytics
	// ----------------------------------------------------
	err = b.reportAnalytics("setGithubStatus failed on aliBuild doctor error")
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to report to analytics", ErrField(err))
}

// ------------------------------------------------------------------

func (b *Build) runAliBuild() bool {
	if err := b.prepareAlibuildRun(); err != nil {
		return false
	}

	// env := []string{}

	args := b.constructAliBuildArgs()
	result := b.aliBuild.Build(
		args,
		shell.Timeout(cfg.timeout.long),
		shell.Cancel(b.abort),
		shell.Env([]string{
			fmt.Sprintf("ALIBUILD_HEAD_HASH=%s", b.pr.SHA),
			fmt.Sprintf("ALIBUILD_BASE_HASH=%s", b.baseSHA),
		}),
	)

	buildDuration := (time.Now().Unix() - b.started)
	buildOK := b.handleAliBuildResult(result)

	b.reportBuildToInflux(buildOK, buildDuration)

	// TODO: when do we return false in this method?
	return true
}

// ------------------------------------------------------------------

func (b *Build) handleAliBuildResult(result *shell.Result) bool {
	ok := !result.IsError()

	if ok {
		b.handleAliBuildSucceeded(result)
	} else {
		b.handleAliBuildFailed(result)
	}
	return ok
}

// ------------------------------------------------------------------

func (b *Build) handleAliBuildSucceeded(result *shell.Result) {
	sendTo(Debug, result.Stdout.Lines())
	createBadgePassing()
	if b.pr.IsBranch() {
		createBuildLog()
	}
}

func (b *Build) handleAliBuildFailed(result *shell.Result) {
	switch {
	case result.Cancelled:
		// The abort channel was closed, probably because
		// the build is being prematurely shut down. No need to
		// report pr errors, analytics etc
		Info(
			"aliBuild build command was aborted",
			Field("pr", b.pr.Number),
			Field("repo", b.pr.RepoPath),
		)
		return

	case result.TimedOut:
		Info(
			"aliBuild doctor command timedout",
			Field("pr", b.pr.Number),
			Field("repo", b.pr.RepoPath),
		)
		return
	}

	var err error

	// The aliBuild command failed either due to a timeout,
	// a legitimate failure or some other error
	Info(fmt.Sprintf("%v", result.Error))
	sendTo(Info, result.Stderr.Lines())

	createBadgeFailing()

	// ----------------------------------------------------
	// Report PR error to Github
	// ----------------------------------------------------
	msg := ""
	err = b.reportPRErrors(b.pr, cfg.buildInfo.checkName, msg, cfg.pr.dontCommentOnError)
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to report pull request error to Github")

	// ----------------------------------------------------
	// Report PR error failed, report to analytics
	// ----------------------------------------------------
	err = b.reportAnalytics("reportPRErrors failed on aliBuild build error")
	if err == nil || isShutDownError(err) {
		return
	}

	Info("Unable to report to analytics", ErrField(err))
}

// ------------------------------------------------------------------

// prepareAlibuildRun sets up the environement ready for an aliBuild build run
func (b *Build) prepareAlibuildRun() error {
	var err error

	buildDir := cfg.alibuild.buildDir

	if err = os.MkdirAll(buildDir, 0777); err != nil {
		Info(
			"Unable to make base build dir",
			Field("dir", buildDir),
			ErrField(err),
		)
		return err
	}

	// Each round we delete the "latest" symlink, to avoid
	// reporting errors from a previous one. In any case they
	// will be recreated if needed when we build.
	if err = shell.RemoveFiles(buildDir, "*latest*", 1, []string{}); err != nil {
		Info(
			"Problem removing latest symlinks from build dir",
			Field("dir", buildDir),
			ErrField(err),
		)
		return err
	}

	// Delete coverage files from one run to the next to avoid
	// reporting them twice under erroneous circumstances
	err = shell.RemoveFiles(
		buildDir,
		cfg.service.coverage.filename,
		4,
		[]string{},
	)
	if err != nil {
		Info(
			"Problem removing Coverage files from build dir",
			Field("dir", buildDir),
			ErrField(err),
		)
		return err
	}

	// For private ALICE repos
	err = dumpGitlabCredentials(cfg.gitlabCredentialsPath)
	return err
}

// ------------------------------------------------------------------

func (b *Build) constructAliBuildArgs() string {
	// build up the alibuild argument list
	jobs := fmt.Sprintf(
		"-j %s",
		fmt.Sprintf("%d", cfg.alibuild.jobs),
	)

	fetch := ""
	if b.aliBuild.HasFetchReposOption() {
		fetch = "--fetch-repos"
	}

	defaults := cfg.alibuild.defaults
	if defaults != "" {
		defaults = fmt.Sprintf("--defaults %s", defaults)
	}

	externals := ""
	if cfg.alibuild.noConsistentExternals != "" {
		externals = ""
	}

	refSources := fmt.Sprintf("--reference-sources %s", cfg.buildInfo.mirror)

	remoteStore := cfg.alibuild.remoteStore
	if remoteStore != "" {
		remoteStore = fmt.Sprintf("--remote-store %s", remoteStore)
	}

	debug := ""
	if cfg.alibuild.debug {
		debug = "--debug"
	}

	argsList := []string{
		jobs,
		fetch,
		defaults,
		externals,
		refSources,
		remoteStore,
		debug,
	}
	return strings.Join(argsList, " ")
}

// ------------------------------------------------------------------

func (b *Build) reportBuildToInflux(buildOK bool, buildDuration int64) error {
	var err error

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := time.Duration(cfg.timeout.short) * time.Second
	ctxTimeout, cancelTimeout := context.WithTimeout(ctx, d)
	defer cancelTimeout()

	done := make(chan struct{})

	Debug("Launching worker goroutine", Field("name", "reportBuildToInflux"))

	go func() {
		defer func() {
			if r := recover(); r != nil {
				Info(
					"Worker routine panicked",
					Field("recover", r),
					Field("name", "reportBuildToInflux"),
				)
			}
			close(done)
		}()

		// TODO: does it matter that this send command may be aborted
		// (by the context), and thus no processing_done state is registered?
		services["influxdb"].sendWithContext(
			ctxTimeout,
			influxDBData{
				state:       "pr_processing_done",
				prNumber:    b.pr.Number,
				prBuildTime: buildDuration,
				prSucceeded: buildOK,
			},
		)
	}()

	// Wait for either a build abort, or the worker to complete
	select {
	case <-b.abort:
		Info("Build abort received, will cancel sending to influxDB")
		cancel()
		err = ShutdownError{}
	case <-done:
	}

	return err
}

// ------------------------------------------------------------------
// ------------------------------------------------------------------
// ------------------------------------------------------------------

// toStatusPath takes a given status and returns a path snippet
// with status prefixed by cfg.buildInfo.checkName
func toStatusPath(status string) string {
	return filepath.Join(cfg.buildInfo.checkName, status)
}

// ------------------------------------------------------------------

func now() int64 {
	return time.Now().Unix()
}

func isShutDownError(err error) bool {
	switch err.(type) {
	case ShutdownError:
		return true
	}
	return false
}

// ------------------------------------------------------------------

// createBuildLog creates and rsyncs a log file containing a success message
func createBuildLog() error {
	baseDir := "copy-emptylog"
	defer func() {
		if err := os.RemoveAll(baseDir); err != nil {
			Info(
				"Unable to remove directory",
				Field("path", baseDir),
				ErrField(err),
			)
		}
	}()

	checkName := strings.Replace(cfg.buildInfo.checkName, "/", "_", -1)
	destDir := fmt.Sprintf(
		"%s/%s/%s/latest/%s",
		baseDir,
		cfg.pr.repo,
		cfg.pr.branch,
		checkName,
	)
	destFile := filepath.Join(destDir, "fullLog.txt")

	if err := os.MkdirAll(destDir, 0777); err != nil {
		Info(
			"Unable to create directory",
			Field("dir", destDir),
			ErrField(err),
		)
		return err
	}

	rome, _ := time.LoadLocation("Europe/Rome")
	now := time.Now().In(rome).Format("Mon Jan _2 15:04:05 CEST 2006")

	contents := []byte(fmt.Sprintf(
		"Build of the %s branch of %s successful at %s",
		cfg.pr.branch,
		cfg.pr.repo,
		now,
	))

	if err := ioutil.WriteFile(destFile, contents, 0774); err != nil {
		Info("Unable to write file", Field("file", destFile), ErrField(err))
	}

	from := baseDir
	to := "rsync://repo.marathon.mesos/store/logs/"
	cmd := fmt.Sprintf("rsync -a %s/ %s", from, to)
	if r := shell.Run(cmd); r.IsError() {
		Info("Unable to rsync", Field("src", baseDir), Field("tgt", to))
	}

	return nil
}

// ------------------------------------------------------------------

func sendTo(logFn func(string, ...logging.Field), lines []string) {
	for _, line := range lines {
		logFn(line)
	}
}
