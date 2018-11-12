package prbuild

import (
	"fmt"
	"os"
	"time"

	"github.com/brinick/shell"
)

// ------------------------------------------------------------------
// Error types related to merging pull requests
// ------------------------------------------------------------------

type errMergeTooBig struct {
	msg  string
	size int64
}

func (e errMergeTooBig) Error() string {
	return fmt.Sprintf("%s (%d bytes)", e.msg, e.size)
}

type errMergeFailed struct{}

func (e errMergeFailed) Error() string {
	return "PR Merge failed"
}

// ------------------------------------------------------------------

type gcRunner struct {
	freq int
	last int64
}

func (g *gcRunner) shouldRun() bool {
	return (time.Now().Unix() - g.last) >= int64(g.freq)
}

func (g *gcRunner) run() {
	if git.reflogExpire("--expire=now --all"); git.Error() != nil {
		Info(
			"Failed to run git reflog --expire, ignoring",
			ErrField(git.Error()),
		)

		git.DropError()
	}

	if git.gc("--prune now"); git.Error() != nil {

	}

	g.last = time.Now().Unix()
}

// ------------------------------------------------------------------

// findLocalRepos returns a list of paths containing .git dirs
// which are at most @depth sub-directories below the @root directory,
// and which are not in the ignore list of directories.
func findLocalRepos(root string, maxdepth int, ignore []string) ([]gitRepo, error) {
	dirs, err := shell.FindDirs(root, ".git", maxdepth, ignore)
	if err != nil {
		return []gitRepo{}, err
	}

	repos := []gitRepo{}
	for _, d := range dirs {
		repo := gitRepo{
			path: d,
			gc: &gcRunner{
				freq: cfg.gitRepoGCFrequency,
			},
		}
		repos = append(repos, repo)
	}

	return repos, nil
}

// ------------------------------------------------------------------

// dumpGitlabCredentials outputs Gitlab credentials to the
// file at the provided path
func dumpGitlabCredentials(path string) error {
	creds := []string{
		"protocol=https",
		"host=gitlab.cern.ch",
		fmt.Sprintf("username=%s", os.Getenv("GITHUB_USER")),
		fmt.Sprintf("password=%s", os.Getenv("GITHUB_PASS")),
	}

	if res := git.credentialStore(creds, path); res.IsError() {
		Error(res.Stderr.Text(false))
		Error("git credential-store failed", ErrField(res.Error))
		return res.Error
	}

	// TODO: put this back
	/*
		cmd := fmt.Sprintf(
			"--global credential.helper \"store --file %s\"",
			path,
		)
		if res := git.config(cmd); res.IsError() {
			Error(res.Stderr.Text(false))
			Error("git config failed", ErrField(res.Error))
			return res.Error
		}
	*/
	return nil
}
