package prbuild

import (
	"context"
	"fmt"
	"github.com/brinick/shell"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ------------------------------------------------------------------

type coverageData struct {
	commitSHA string
	prBranch  string
	prNumber  int
}

// ------------------------------------------------------------------

func newCoverageSvc(url string, maxCurlTime int, sources, filename, infoBaseDir string) *codeCovSvc {
	return &codeCovSvc{
		codeCovURL:      url,
		maxCurlTime:     maxCurlTime,
		sources:         sources,
		codeCovFileName: filename,
		infoFileBaseDir: infoBaseDir,
		senderService: senderService{
			started: time.Now().Unix(),
		},
	}
}

// ------------------------------------------------------------------

type codeCovSvc struct {
	senderService
	name            string
	codeCovURL      string
	maxCurlTime     int
	sources         string
	infoFileBaseDir string
	codeCovFileName string
}

func (c *codeCovSvc) Name() string {
	return "coverage"
}

func (c *codeCovSvc) send(data interface{}) error {
	return c.sendWithContext(context.TODO(), data)
}

func (c *codeCovSvc) sendWithContext(ctx context.Context, data interface{}) error {
	var (
		err     error
		infoDir string
	)

	covdata, ok := data.(*coverageData)
	if !ok {
		// wrong type passed in
		return fmt.Errorf(
			"Wrong data type passed to Coverage service send() - got %T, need *coverageData",
			data,
		)
	}

	// change back to the original dir on exit
	defer os.Chdir(func() string {
		p, _ := os.Getwd()
		return p
	}())

	os.Chdir(infoDir)

	// update the stats on the sending
	defer func() {
		c.updateSendCounts(err)
	}()

	if infoDir, err = c.findInfoFileDir(ctx); err != nil {
		return fmt.Errorf("No coverage file found below %s (%s)", c.infoFileBaseDir, err)
	}

	cmd := fmt.Sprintf(
		"bash <(%s) %s",
		c.constructCurlCmd(),
		c.constructCovScriptOpts(covdata),
	)

	err = shell.RunWithContext(ctx, cmd).Error
	return err
}

func (c *codeCovSvc) constructCurlCmd() string {
	return fmt.Sprintf("curl --max-time %d -s %s", c.maxCurlTime, c.codeCovURL)
}

func (c *codeCovSvc) constructCovScriptOpts(data *coverageData) string {
	prNumber := ""
	if data.prNumber > 0 {
		// pr is
		prNumber = fmt.Sprintf("-P %d", data.prNumber)
	}

	prBranch := ""
	if len(data.prBranch) > 0 {
		prBranch = fmt.Sprintf("-B %s", data.prBranch)
	}

	opts := []string{
		fmt.Sprintf("-R %s", c.sources),
		fmt.Sprintf("-f %s", c.codeCovFileName),
		fmt.Sprintf("-C %s", data.commitSHA),
		prBranch,
		prNumber,
	}

	return strings.Join(opts, " ")
}

// FindInfoFileDir returns the path to the directory containing a coverage
// file, or empty string if not found or an error occured
func (c *codeCovSvc) findInfoFileDir(ctx context.Context) (string, error) {
	var (
		files []string
		path  string
		err   error
	)

	done := make(chan struct{})

	go func() {
		defer close(done)
		if files, err = shell.FindFiles(c.infoFileBaseDir, c.codeCovFileName, 4, []string{}); err == nil {
			path = filepath.Dir(files[0])
		}
	}()

	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-done:
	}

	return path, err
}
