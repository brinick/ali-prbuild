package prbuild

import (
	"fmt"
	"github.com/brinick/shell"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

// ------------------------------------------------------------------

func NewBuildLogs(logsRootDir, develPrefix string) *buildLogs {
	logs := &buildLogs{}
	// load up the build logs list
	logs.find(logsRootDir, develPrefix)
	return logs
}

type buildLogs struct {
	logs       []*buildLog
	errorlines []string
}

func (bl *buildLogs) find(root, develPrefix string) {
	suffix := strings.Trim(fmt.Sprintf("latest-%s", develPrefix), "-")

	searchPath := filepath.Join(root, "BUILD/*latest*/log")
	Debug("Looking for build logs", Field("path", searchPath))

	for _, match := range globFiles(searchPath) {
		if strings.HasSuffix(filepath.Dir(match), suffix) {
			bl.logs = append(bl.logs, &buildLog{path: match})
		}
	}

	Info(fmt.Sprintf("Found %d build logs", len(bl.logs)))
	for i, log := range bl.logs {
		Debug(fmt.Sprintf("#%d: %s", i, log.path))
	}
}

// grepForErrors will search each build log for errors. If a given log has
// none, or grep failed, the last N lines of the log are taken
// instead (N is configurable as cfg.pr.maxCommentLines). We
// accept no more than N total lines, and stop grepping build logs
// as soon as our quota is met.
func (bl *buildLogs) grepForErrors(gp *grepParams) {
	var elines *errlines

	for _, log := range bl.logs {
		grep := log.grep(gp)

		if grep.hasMatches() {
			elines.append(grep.matches)
		} else {
			// grep either failed or returned no matches:
			// we tail the log instead
			tail := log.tail(&tailParams{cfg.pr.maxCommentLines})
			if !tail.failed() {
				// tail worked
				elines.append(tail.lines)
			} else {
				msg := "Unable to grep or tail build log, skipping"
				Info(msg, ErrField(tail.err), Field("log", log.path))
				continue
			}
		}

		// Before we go to the next log, let's check how many
		// lines we already have. If we have reached our upper
		// limit, we break out of the loop and return.
		// TODO: this is a curious algorithm. If a log file has no
		// errors we take its last N lines, where N = our max limit.
		// In other words, at the first log with no errors, we will
		// by design ignore any remaining logs, even if they have
		// errors in them.
		if elines.length() > cfg.pr.maxCommentLines {
			elines.truncate(cfg.pr.maxCommentLines)
			break
		}
	}

	bl.errorlines = elines.lines
}

// cat will dump all build logs into a single destination file.
// If the destination file parent directory does not exist,
// it will be created.
func (bl *buildLogs) cat(destfile string) {
	os.MkdirAll(filepath.Dir(destfile), 0755)

	// Now we dump all logs to the dest file
	for _, log := range bl.logs {
		log.appendTo(destfile)
	}
}

// ------------------------------------------------------------------

func rsyncLogs(srcDir, dst string) {
	defer os.Chdir(func() string {
		p, _ := os.Getwd()
		return p
	}())
	os.Chdir(srcDir)

	cmd := fmt.Sprintf("rsync -av ./ %s", dst)
	if res := shell.Run(cmd); res.IsError() {
		for _, line := range res.Stdout.Lines() {
			Info(line)
		}
		for _, line := range res.Stderr.Lines() {
			Info(line)
		}
		Info(
			"Unable to rsync build logs to store",
			ErrField(res.Error),
			Field("store", dst),
		)
	}
}

// ------------------------------------------------------------------

type buildLog struct {
	path string
}

func (bl *buildLog) grep(params *grepParams) *grepResult {
	cmd := grepCmd{params: params}
	cmd.run(bl.path)
	return cmd.result
}

func (bl *buildLog) tail(params *tailParams) *tailResult {
	cmd := tailCmd{params: params}
	cmd.run(bl.path)
	return cmd.result
}

func (bl *buildLog) cat() []byte {
	var contents []byte
	contents, _ = ioutil.ReadFile(bl.path)
	return contents
}

func (bl *buildLog) appendTo(tgtfile string) {
	fd, err := os.OpenFile(tgtfile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		Info(
			"Unable to open file for appending log",
			ErrField(err),
			Field("log", bl.path),
			Field("tgt", tgtfile),
		)
		return

	}
	defer fd.Close()
	fd.Write(bl.cat())
}

// ------------------------------------------------------------------

type errlines struct {
	lines []string
}

func (e *errlines) append(lines []string) {
	e.lines = append(e.lines, lines...)
}

func (e *errlines) length() int {
	return len(e.lines)
}

func (e *errlines) truncate(nlines int) {
	e.lines = e.lines[:nlines]
}

// ------------------------------------------------------------------

func globFiles(patt string) []string {
	// good practice 101: ignore the eventual error
	matches, _ := filepath.Glob(patt)
	return matches
}

// ------------------------------------------------------------------

type grepResult struct {
	file    string
	matches []string
	err     error
}

func (gr *grepResult) hasMatches() bool {
	return !gr.noMatches()
}

func (gr *grepResult) noMatches() bool {
	return len(gr.matches) == 0
}

func (gr *grepResult) failed() bool {
	return gr.err != nil
}

// ------------------------------------------------------------------

type grepParams struct {
	patt    string
	nbefore int
	nafter  int
}

type grepCmd struct {
	params *grepParams
	result *grepResult
}

func (g *grepCmd) cmd(tgtfile string) string {
	return fmt.Sprintf(
		"grep -e '%s' -B %d -A %d %s",
		g.params.patt,
		g.params.nbefore,
		g.params.nafter,
		tgtfile,
	)
}

func (g *grepCmd) run(path string) {
	res := shell.Run(g.cmd(path))
	if res.IsError() {
		g.result = &grepResult{path, nil, res.Error}
	}

	g.result = &grepResult{path, res.Stdout.Lines(), nil}
}

func (g *grepCmd) failed() bool {
	return g.result != nil && g.result.err != nil
}

func (g *grepCmd) hasMatches() bool {
	return g.result != nil && len(g.result.matches) > 0
}

// ------------------------------------------------------------------

type tailParams struct {
	nlines int
}

type tailCmd struct {
	params *tailParams
	result *tailResult
}

func (t *tailCmd) cmd(tgtfile string) string {
	return fmt.Sprintf(
		"tail -n %d %s",
		t.params.nlines,
		tgtfile,
	)
}

func (t *tailCmd) run(path string) {
	res := shell.Run(t.cmd(path))
	if res.IsError() {
		t.result = &tailResult{nil, res.Error}
	}

	t.result = &tailResult{res.Stdout.Lines(), nil}
}

// ------------------------------------------------------------------

type tailResult struct {
	lines []string
	err   error
}

func (t *tailResult) failed() bool {
	return t.err != nil
}
