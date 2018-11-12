package prbuild

// Internal tests which access package internal information

import "testing"

func TestBuildDone(t *testing.T) {
	b := NewBuild()

	if b.Done() {
		t.Fatal("Newly created build is already marked done")
	}

	b.SetDone()
	b.finished = 1 // bump
	if !b.Done() {
		t.Fatal("Build should be marked as done")
	}
}

func TestBuildRunning(t *testing.T) {
	b := NewBuild()
	if b.Running() {
		t.Fatal("Newly created build claims to be running")
	}
	b.started = 1 // bump
	if !b.Running() {
		t.Fatal("Build should be marked as running")
	}
}

func TestStartFailsIfBuildRunning(t *testing.T) {
	b := NewBuild()
	b.started = 1
	b.start()
	if b.Error() != BuildAlreadyStartedError {
		t.Fatal("Calling start() on a running build should fail")
	}
}

func TestStopFailsIfBuildNotRunning(t *testing.T) {
	b := NewBuild()
	b.stop()
	err := b.Error()

	if err != BuildNotRunningError {
		t.Fatal(fmt.Sprintf(
			"Called stop() on a non-running build. "+
				"Expected BuildNotRunningError, got %v", err,
		))
	}

}
