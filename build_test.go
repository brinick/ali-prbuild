package prbuild_test

import (
	"testing"

	"github.com/brinick/ali-prbuild"
)

// ------------------------------------------------------------------

type fakePR struct{}

func (f fakePR) RepoPath() string {
	return "fake/repo"
}

func (f fakePR) Number() int {
	return 1
}

// ------------------------------------------------------------------

type fakeAlibuild struct{}

func (f fakeAlibuild) Build() {
}
func (f fakeAlibuild) Doctor() {

}

// ------------------------------------------------------------------

func createNewBuild() *prbuild.Build {
	f := fakePR{}
	return prbuild.NewBuild(f)
}

// ------------------------------------------------------------------

func TestBuildDone(t *testing.T) {
	b := createNewBuild()

	if b.Done() {
		t.Fatal("Newly created build is already marked done")
	}

	b.SetDone()
	// this won't work...
	b.finished = 1 // bump
	if !b.Done() {
		t.Fatal("Build expected to be done")
	}
}

func TestBuildRunning(t *testing.T) {
	b := createNewBuild()
	if b.Running() {
		t.Fatal("Newly created build claims to be running")
	}
}
