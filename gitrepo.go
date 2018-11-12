package prbuild

import (
	"fmt"
	"os"

	"github.com/brinick/logging"
	"github.com/brinick/shell"
)

type gitRepos []gitRepo

func (g *gitRepos) update(remote, branch string) error {
	// TODO: pass in shell.Cancel with the build shutdown channel
	// to stop the repo updates if necessary

	for _, repo := range *g {
		Info("Trying to update local repo", logging.F("repo", repo.path))
		if err := repo.update(remote, branch); err != nil {
			Info("Unable to update local repo", ErrField(err))
			return err
		}
	}

	return nil
}

// ------------------------------------------------------------------

type gitRepo struct {
	path string
	gc   *gcRunner
}

// headRef changes into the repo path, and grabs the sha of the branch tip,
// before changing back to the original directory.
func (repo *gitRepo) headRef() (string, error) {
	defer os.Chdir(func() string {
		p, _ := os.Getwd()
		return p
	}())

	if err := os.Chdir(repo.path); err != nil {
		return "", err
	}

	result := git.headRef()
	if result.IsError() {
		return "", result.Error
	}

	return result.Stdout.Text(true), nil
}

func (repo *gitRepo) update(remote, branch string) error {
	defer os.Chdir(func() string {
		p, _ := os.Getwd()
		return p
	}())

	logging.Error("gitcmds: repo.path", logging.F("p", repo.path))

	if err := os.Chdir(repo.path); err != nil {
		return err
	}

	var r *shell.Result
	logging.Error("gitcmds: running headAbbrev")

	// Get the name of the local current branch
	if r = git.currentBranch(); r.IsError() {
		return r.Error
	}

	logging.Error("gitcmds: localBranch")

	var localBranch string
	if localBranch = r.Stdout.Text(true); localBranch == "HEAD" {
		return nil
	}

	logging.Error("gitcmds: deleteRemoteRefs")

	if res := git.deleteRemoteRefs(remote, branch); git.Error() != nil {
		// we log for posterity but then drop it
		Error(res.Stderr.Text(false))
		Error(
			"Unable to delete git refs - continuing nevertheless",
			Field("path", repo.path),
			ErrField(git.Error()),
		)
		git.DropError()
	}

	if repo.gc.shouldRun() {
		if repo.gc.run(); git.Error() != nil {
			Info(
				"git gc failed - continuing nevertheless",
				Field("path", repo.path),
				ErrField(git.Error()),
			)
			git.DropError()
		}
	}

	// Try to reset to corresponding remote branch
	// (assume it's origin/<branch>)
	branch = fmt.Sprintf(
		"+%s:refs/remotes/%s/%s",
		localBranch,
		remote,
		localBranch,
	)
	if r := git.fetch(remote, branch, cfg.timeout.short); r.IsError() {
		Info("git fetch failed", ErrField(r.Error))
	}

	git.reset(fmt.Sprintf("--hard %s/%s", remote, localBranch))
	git.clean("-fxd")

	return git.Error()
}
