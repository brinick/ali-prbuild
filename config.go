package prbuild

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var cfg = func() *configuration {
	c := &configuration{
		ciName:                env("CI_NAME", ""),
		architecture:          "slc7-x86_64",
		workDir:               env("WORK_DIR", "sw"),
		oneShot:               boolEnv("ONESHOT", false),
		buildSuffix:           env("BUILD_SUFFIX", "master"),
		develPrefix:           env("DEVEL_PREFIX", ""),
		prQueueSize:           100,
		gitRepoGCFrequency:    4 * 3600,
		gitlabCredentialsPath: env("GITLAB_CREDENTIALS_STORE", filepath.Join(env("HOME", "~/"), ".git-creds")),
		pr: &pullreq{
			remote: env("PR_REMOTE_ALIAS", "origin"),
			branch: env("PR_BRANCH", "master"),
			repo:   env("PR_REPO", "alisw/alidist"),

			// max size in bytes allowed between pre- and post- git merge
			maxMergeDiffSize: intEnv("MAX_DIFF_SIZE", 20000000),

			// if there is a pull request error, should we refrain
			// from adding a comment in the feedback to Github
			dontCommentOnError: boolEnv("DONT_USE_COMMENTS", false),

			// When leaving a comment on a pull request error, max number of
			// lines we should make visible
			maxCommentLines: 50,

			// Destination path for log files
			logsURL:  "https://ali-ci.cern.ch/repo/logs",
			logsDest: "rsync://repo.marathon.mesos/store/logs",
		},
		fetch: &prFetch{
			delayBetweenFetches: int64(intEnv("DELAY_BETWEEN_PR_FETCHES", 30)),
			showMainBranch:      true,
			maxWaitNoPRs:        int64(intEnv("MAX_WAIT_NO_PRS", 1200)),
			maxWaitNoNewPRs:     int64(intEnv("MAX_WAIT_NO_NEW_PRS", intEnv("DELAY", 1200))),
		},
		buildInfo: &prHandle{
			mirror: env("MIRROR", "/build/mirror"),
			// TODO: is it valid that this be different than the PR_REPO suffix path?

			packageName:     env("PACKAGE", "AliPhysics"),
			checkNameStatus: env("CHECK_NAME_STATUS", "success"),
		},
		alibuild: &alibuildInfo{
			executable: "aliBuild", // assume it's pip-installed and in $PATH
			buildDir:   "sw/BUILD",
			repo:       env("ALIBUILD_REPO", "alisw/alibuild"),
			defaults:   env("ALIBUILD_DEFAULTS", "release"),
			runO2Tests: intEnv("ALIBUILD_O2_TESTS", 0),

			jobs:                  intEnv("JOBS", runtime.NumCPU()),
			debug:                 boolEnv("DEBUG", false),
			remoteStore:           env("REMOTE_STORE", ""),
			noConsistentExternals: env("NO_ASSUME_CONSISTENT_EXTERNALS", ""),

			// Set in the environment before any doctor/build commands run
			defaultEnvVars: []string{
				"GITLAB_USER=",
				"GITLAB_PASS=",
				"GITHUB_TOKEN=",
				"INFLUXDB_WRITE_URL=",
				"CODECOV_TOKEN=",
			},
		},
		/*
			alidoctor: &alidoctorInfo{
				executable: "alibuild/aliDoctor",
			},
		*/
		alidist: &alidistInfo{
			repo: env("ALIDIST_REPO", "alisw/alidist"),
		},
		worker: &workerInfo{
			// Worker index, zero-based. Set to 0 if unset
			// (i.e. when not running on Aurora)
			index:    intEnv("WORKER_INDEX", 0),
			poolSize: intEnv("WORKERS_POOL_SIZE", 1),
		},
		trust: &trustInfo{
			collaborators: boolEnv("TRUST_COLLABORATORS", false),
			users:         splitTrim(env("TRUSTED_USERS", "review"), ","),
		},
		timeout: &timeoutInfo{
			gitCmd: intEnv("GIT_CMD_TIMEOUT", 120),
			short:  intEnv("TIMEOUT", 600),
			long:   intEnv("LONG_TIMEOUT", 36000),
		},
		service: &servicesInfo{
			monalisa: &monalisa{
				host: env("MONALISA_HOST", ""),
				port: intEnv("MONALISA_PORT", 0),
			},
			coverage: &coverage{
				filename:    "coverage.info",
				url:         "https://codecov.io",
				maxCurlTime: 600,
			},
			influxdb: &influxdb{
				writeURL:    env("INFLUXDB_WRITE_URL", ""),
				maxCurlTime: 20,
			},
			analytics: &analytics{
				id:      env("ALIBOT_ANALYTICS_ID", ""),
				appName: env("ALIBOT_ANALYTICS_APP_NAME", "continuous-builder.go"),
				appVers: env("ALIBOT_ANALYTICS_APP_VERSION", ""),
			},
		},
	}

	// ----------------------------------------------------------------------
	// Post-config for stuff that depends on other config items
	// ----------------------------------------------------------------------

	defaultCheckName := fmt.Sprintf(
		"build/%s/%s",
		c.buildInfo.packageName,
		c.alibuild.defaults,
	)
	c.buildInfo.checkName = env("CHECK_NAME", defaultCheckName)

	// ensure CHECK_NAME is in the environment
	os.Setenv("CHECK_NAME", c.buildInfo.checkName)

	c.pr.repoCheckout = env("PR_REPO_CHECKOUT", filepath.Base(c.pr.repo))

	// -----------------------------------
	// Coverage service
	// -----------------------------------
	c.service.coverage.sources = c.pr.repoCheckout

	// root of the directory tree in which coverage files will be found
	c.service.coverage.infoBaseDir = c.alibuild.buildDir

	// -----------------------------------
	// InfluxDB service
	// -----------------------------------
	if strings.HasPrefix(c.service.influxdb.writeURL, "insecure_https:") {
		c.service.influxdb.allowInsecureCurl = true
		c.service.influxdb.writeURL = string(c.service.influxdb.writeURL[9:])
	}

	// -----------------------------------
	// Analytics service
	// -----------------------------------
	host, _ := os.Hostname()
	c.service.analytics.userID = env(
		"ALIBOT_ANALYTICS_USER_UUID",
		fmt.Sprintf("%s-%d-%s", host, c.worker.index, c.ciName),
	)

	return c
}()

// ------------------------------------------------------------

type configuration struct {
	ciName                string
	architecture          string
	workDir               string
	oneShot               bool
	buildSuffix           string
	develPrefix           string
	gitRepoGCFrequency    int
	gitlabCredentialsPath string
	prQueueSize           uint // max channel size for PR fetching
	pr                    *pullreq
	fetch                 *prFetch
	buildInfo             *prHandle
	alibuild              *alibuildInfo
	// alidoctor             *alidoctorInfo
	alidist *alidistInfo
	worker  *workerInfo
	trust   *trustInfo
	timeout *timeoutInfo
	service *servicesInfo
}

type pullreq struct {
	remote             string
	branch             string
	repo               string
	repoCheckout       string
	maxMergeDiffSize   int
	dontCommentOnError bool
	maxCommentLines    int
	logsDest           string
	logsURL            string
}

type prFetch struct {
	delayBetweenFetches int64
	showMainBranch      bool
	maxWaitNoPRs        int64
	maxWaitNoNewPRs     int64
}

type prHandle struct {
	mirror          string
	packageName     string
	checkName       string
	checkNameStatus string
}

type alibuildInfo struct {
	executable            string
	repo                  string
	buildDir              string
	defaults              string
	runO2Tests            int
	jobs                  int
	debug                 bool
	remoteStore           string
	noConsistentExternals string
	defaultEnvVars        []string
}

type alidoctorInfo struct {
	executable string
}

type alidistInfo struct {
	repo string

	// the ref of the local alidist repo we are using
	ref string
}

type workerInfo struct {
	index    int
	poolSize int
}

type trustInfo struct {
	collaborators bool
	users         []string
}

type timeoutInfo struct {
	gitCmd int
	short  int
	long   int
}

type servicesInfo struct {
	monalisa  *monalisa
	coverage  *coverage
	influxdb  *influxdb
	analytics *analytics
}

type monalisa struct {
	host string
	port int
}

type coverage struct {
	filename    string
	sources     string
	infoBaseDir string
	url         string
	maxCurlTime int
}

type influxdb struct {
	writeURL          string
	allowInsecureCurl bool

	// max time in secs allowed for the curl operation
	maxCurlTime int
}

type analytics struct {
	id          string
	userID      string
	appName     string
	appVers     string
	maxCurlTime int
}

// ------------------------------------------------------------

func env(key string, defaultVal string) string {
	val, found := os.LookupEnv(key)
	if !found {
		val = defaultVal
	}

	return val
}

func intEnv(key string, defaultVal int) int {
	val, found := os.LookupEnv(key)
	if !found {
		return defaultVal
	}

	if intVal, err := strconv.Atoi(val); err == nil {
		return intVal
	}

	return defaultVal
}

func boolEnv(key string, defaultVal bool) bool {
	val, found := os.LookupEnv(key)
	if !found {
		return defaultVal
	}

	if boolVal, err := strconv.ParseBool(val); err == nil {
		return boolVal
	}
	return defaultVal
}

// splitTrim splits the src string on the cut string,
// and trims whitespace from each token, before
// returning the list of tokens
func splitTrim(src, cut string) []string {
	var tokens []string
	for _, s := range strings.Split(src, cut) {
		tokens = append(tokens, strings.TrimSpace(s))
	}
	return tokens
}
