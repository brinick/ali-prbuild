package prbuild

// Reports pull request errors to Github

import (
	"context"
	"crypto/sha1"
	"fmt"
	"github.com/brinick/ali-ci/pullrequest"
	"github.com/brinick/github/object"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ------------------------------------------------------------------

func reportPRErrorsWithContext(
	ctx context.Context,
	pr *pullrequest.PR,
	checkName, message string,
	noComments bool,
) error {

	if pr.IsBranch() {
		handleBranch(ctx, pr, checkName, message)
	} else {
		// pr is a digit
		handlePR(ctx, pr, checkName, message, noComments)
	}

	return nil
}

func reportPRErrors(pr *pullrequest.PR, checkName, message string, noComments bool) error {
	return reportPRErrorsWithContext(
		context.TODO(),
		pr,
		checkName,
		message,
		noComments,
	)
}

// ------------------------------------------------------------------

func handleBranch(
	ctx context.Context,
	pr *pullrequest.PR,
	checkName, message string,
) error {
	branch, err := pr.Branch()
	if err != nil {
		return err
	}

	commit := branch.HeadCommit()
	sha := commit.SHA

	msg := fmt.Sprintf(
		"Error while checking %s for %s:\n",
		checkName,
		sha,
	)

	if len(message) > 0 {
		msg += message
	} else {
		logs := getBuildLogs(pr)
		logsURL := filepath.Join(cfg.pr.logsURL, logPath(pr, false))

		msg += fmt.Sprintf(
			"```\n%s\n```\nFull log [here](%s).\n",
			logs.errorlines,
			logsURL,
		)
	}

	status := object.CommitStatus{State: "error", Context: checkName}
	_, err = commit.SetStatusWithContext(ctx, &status)

	return err
}

// ------------------------------------------------------------------

func handlePR(
	ctx context.Context,
	pr *pullrequest.PR,
	checkName, message string,
	noComments bool,
) error {
	// TODO: context is only used inside set github status,
	// we should run the rest in a goroutine and check for ctx.Done

	// TODO: why do we get the commit object - we already
	// have the sha in the pr. These 3 lines are pointless..

	commit, err := pr.Commit()
	if err != nil {
		return err
	}

	sha := commit.SHA

	msg := fmt.Sprintf(
		"Error while checking %s for %s:\n",
		checkName,
		sha,
	)

	logsURL := filepath.Join(cfg.pr.logsURL, logPath(pr, false))

	if len(message) > 0 {
		msg += message
	} else {
		logs := getBuildLogs(pr)

		msg += fmt.Sprintf(
			"```\n%s\n```\nFull log [here](%s).\n",
			logs.errorlines,
			logsURL,
		)
	}

	status := object.CommitStatus{
		State:     "error",
		Context:   checkName,
		TargetURL: logsURL,
	}
	_, err = commit.SetStatusWithContext(ctx, &status)

	if noComments {
		return err
	}

	newCommentHash := calculateMessageHash(msg)

	prefix := fmt.Sprintf("Error while checking %s for %s", checkName, sha)

	issue, err := pr.Repo().Issue(pr.Number())
	if err != nil {
		return err
	}

	comments, _ := issue.Comments()

	for comments.HasNext() {
		comment := comments.Item()
		if !strings.HasPrefix(comment.Body, prefix) {
			continue
		}

		existingCommentHash := calculateMessageHash(comment.Body)

		// Same hash i.e. same comment, just exit
		if existingCommentHash == newCommentHash {
			Debug("Found same comment for the same commit")
			return nil
		}
		// Different hash i.e. different comment, let's update it
		data := map[string]string{"body": message}
		if _, err := comment.UpdateWithContext(ctx, data); err != nil {
			Error(
				"Unable to update issue comment",
				ErrField(err),
			)
		}

		return err
	}

	// No existing comment matched at all, let's create
	// a new comment then for this issue
	data := map[string]string{"body": message}
	if _, err = issue.PostComment(data); err != nil {
		Error(
			"Unable to post new comment for issue",
			ErrField(err),
			Field("issue", issue.Number),
			Field("title", issue.Title),
		)

	}

	return err
}

// ------------------------------------------------------------------

func calculateMessageHash(msg string) string {
	// Anything which can resemble a hash or a date is filtered out.
	re := regexp.MustCompile("[0-9a-f-A-F]")
	msg = re.ReplaceAllString(msg, "")
	splitMsg := strings.Split(msg, "\n")
	sort.Strings(splitMsg)
	msg = strings.Join(splitMsg, "\n")
	sha := fmt.Sprintf("%x", sha1.Sum([]byte(msg)))
	return sha[0:10]
}

// ------------------------------------------------------------------

func getBuildLogs(pr *pullrequest.PR) *buildLogs {
	logs := NewBuildLogs(cfg.workDir, cfg.develPrefix)

	// grep for error lines, or failing that, tail the log files
	logs.grepForErrors(&grepParams{patt: ": error:", nbefore: 3, nafter: 3})

	// Now we save the build logs by concatenating them all
	// into a single file, and then rsync-ing it

	copyLogsDir := "copy-logs"

	// Clean slate: rm -rf
	os.RemoveAll(copyLogsDir)

	tgtfile := filepath.Join(copyLogsDir, logPath(pr, false))
	logs.cat(tgtfile)

	if pr.IsBranch() {
		tgtfile := filepath.Join(copyLogsDir, logPath(pr, true))
		logs.cat(tgtfile)
	}

	rsyncLogs(copyLogsDir, cfg.pr.logsDest)

	return logs
}

// ------------------------------------------------------------------

// logPath constructs the relative path to the file to which we will
// cat all the individual build logs. If we do not ask to use the
// latest, the path will be constructed with the pull request
// commit SHA instead.
func logPath(pr *pullrequest.PR, useLatest bool) string {
	latest := "latest"
	if !useLatest {
		latest = pr.SHA()
	}

	// Replace all chars in checkName that are not in the
	// following list with an underscore
	re := regexp.MustCompile("[^a-zA-Z0-9_-]")
	normStatus := re.ReplaceAllString(cfg.buildInfo.checkName, "_")

	return filepath.Join(
		pr.RepoPath(),
		fmt.Sprintf("%d", pr.Number()),
		latest,
		normStatus,
		"fullLog.txt",
	)
}
