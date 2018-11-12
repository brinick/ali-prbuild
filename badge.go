package prbuild

import (
	"fmt"
	"os"
	"strings"

	"github.com/brinick/logging"
	"github.com/brinick/shell"
)

type badge struct {
	label    string
	status   string
	color    string
	filetype string
}

func (b *badge) localName() string {
	// This assumes that the label is comprised of two terms: <checkName> <branch>
	tokens := strings.Split(b.label, "/")
	return fmt.Sprintf(
		"%s.%s",
		strings.Replace(tokens[0], "/", "_", -1),
		b.filetype,
	)
}

func (b *badge) remoteName() string {
	name := fmt.Sprintf("%s-%s-%s", b.label, b.status, b.color)
	name = strings.Replace(name, "-", "--", -1)
	name = strings.Replace(name, "_", "__", -1)
	name = strings.Replace(name, " ", "_", -1)
	name = strings.Replace(name, "/", "%2F", -1)
	return fmt.Sprintf("%s.%s", name, b.filetype)
}

// ------------------------------------------------------------------

func createBadge(status, filetype string) {
	var (
		b            badge
		baseBadgeDir = "copy-badge"
	)

	logging.Error("badge creation currently disabled")
	return

	defer os.RemoveAll(baseBadgeDir)

	// badgeDir := filepath.Join(baseBadgeDir, cfg.pr.repo, cfg.pr.branch)

	b = badge{
		label:    fmt.Sprintf("%s %s", cfg.buildInfo.checkName, cfg.pr.branch),
		filetype: "svg",
		status:   status,
	}

	if status == "passing" {
		b.color = "brightgreen"
	} else if status == "failing" {
		b.color = "red"
	}

	curl := fmt.Sprintf(
		"curl -L -o %s %s",
		b.localName(),
		fmt.Sprintf("https://img.shields.io/badge/%s", b.remoteName()),
	)
	rsync := fmt.Sprintf(
		"rsync -a %s/ rsync://repo.marathon.mesos/store/buildstatus/",
		baseBadgeDir,
	)
	cmds := []string{curl, rsync}

	for _, cmd := range cmds {
		if res := shell.Run(cmd); res.IsError() {
			// ignore...
		}
	}
}

// ------------------------------------------------------------------

func createBadgeFailing() {
	createBadge("failing", "svg")
}

func createBadgePassing() {
	createBadge("passing", "svg")
}
