package prbuild

import (
	"fmt"
	"strings"

	"github.com/brinick/logging"
	"github.com/brinick/shell"
)

// ------------------------------------------------------------------

var (
	gitExe = "git"
	git    = gitExecutable{exe: gitExe}
)

// ------------------------------------------------------------------

type gitExecutable struct {
	// path to the git executable, or just "git" if it's on the PATH
	exe string

	// last git command error stored here
	err error
}

func (g *gitExecutable) execCmd(cmd string, print bool) *shell.Result {
	if print {
		logging.Debug(cmd)
	}
	res := shell.Run(cmd)
	g.err = res.Error
	return res
}

func (g *gitExecutable) makeCmd(subcmd string, args ...string) string {
	cmd := []string{g.exe, subcmd}
	cmd = append(cmd, args...)
	cmdStr := strings.Join(cmd, " ")
	logging.Debug(cmdStr)
	return cmdStr
}

// Error returns the last git command error
func (g *gitExecutable) Error() error {
	return g.err
}

// DropError sets the last git command error to nil
func (g *gitExecutable) DropError() {
	g.err = nil
}

func (g *gitExecutable) gc(opts string) *shell.Result {
	return g.execCmd(g.makeCmd("gc", opts), true)
}

func (g *gitExecutable) merge(sha, opts string) *shell.Result {
	return g.execCmd(g.makeCmd("merge", opts, sha), true)
}

func (g *gitExecutable) config(opts string) *shell.Result {
	return g.execCmd(g.makeCmd("config", opts), true)
}

func (g *gitExecutable) clean(opts string) *shell.Result {
	return g.execCmd(g.makeCmd("clean", opts), true)
}

func (g *gitExecutable) revParse(opts string) *shell.Result {
	return g.execCmd(g.makeCmd("rev-parse", opts), true)
}

func (g *gitExecutable) headRef() *shell.Result {
	return g.revParse("--verify HEAD")
}

func (g *gitExecutable) currentBranch() *shell.Result {
	return g.revParse("--abbrev-ref HEAD")
}

func (g *gitExecutable) reset(opts string) *shell.Result {
	return g.execCmd(g.makeCmd("reset", opts), true)
}

func (g *gitExecutable) credentialStore(creds []string, store string) *shell.Result {
	credsCmd := "printf " + fmt.Sprintf("\"%s\n\"", strings.Join(creds, "\n"))

	cmd := fmt.Sprintf(
		"%s | %s credential-store --file %s store",
		credsCmd,
		g.exe,
		store,
	)
	return g.execCmd(cmd, false)
}

func (g *gitExecutable) fetch(remote, branch string, timeout int) *shell.Result {
	var res *shell.Result

	cmd := g.makeCmd("fetch", remote, branch)
	logging.Debug(cmd)
	if timeout > 0 {
		res = shell.Run(cmd, shell.Timeout(timeout))
	} else {
		res = shell.Run(cmd)
	}
	g.err = res.Error
	return res
}

func (g *gitExecutable) deleteRemoteRefs(remote, branch string) *shell.Result {
	return g.updateRef(fmt.Sprintf("-d refs/remotes/%s/%s/*", remote, branch))
}

func (g *gitExecutable) updateRef(opts string) *shell.Result {
	return g.execCmd(g.makeCmd("update-ref", opts), true)
}

func (g *gitExecutable) reflogExpire(opts string) *shell.Result {
	return g.execCmd(g.makeCmd("reflog expire", opts), true)
}

// ------------------------------------------------------------------
