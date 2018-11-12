package prbuild

import (
	"fmt"
	"time"
)

// ------------------------------------------------------------------

// logicalError represents the base type for logical errors such
// as trying to start a build that has already started, or building
// a pull request that has not been provided.
type logicalError struct {
	msg string
}

func (e *logicalError) Error() string {
	return e.msg
}

// ------------------------------------------------------------------

// buildError represents any error that occured in performing
// the build such as an inability to correctly run aliDoctor
// or aliBuild. It is not used for expected failures such as
// when aliBuild ran correctly, but failed.
type buildError struct {
	msg string
}

func (e buildError) Error() string {
	return e.msg
}

// ------------------------------------------------------------------

func BuildOngoingError(pr int) *buildOngoingError {
	return &buildOngoingError{
		logicalError{fmt.Sprintf("Build already ongoing for PR #%d", pr)},
	}

}

// BuildOngoingError is created if a build is already ongoing
// when a new build request is made (only one build at a time is allowed)
type buildOngoingError struct {
	logicalError
}

// ------------------------------------------------------------------

func NothingToBuildError(msg string) *nothingToBuildError {
	return &nothingToBuildError{
		logicalError{msg},
	}
}

type nothingToBuildError struct {
	logicalError
}

// ------------------------------------------------------------------

func BuildAlreadyStartedError(startTime int64) *buildAlreadyStarted {
	return &buildAlreadyStarted{
		logicalError{fmt.Sprintf("Build was already started at %s", time.Unix(startTime, 0))},
	}
}

type buildAlreadyStarted struct {
	logicalError
}

// ------------------------------------------------------------------

func BuildNotRunningError() *buildNotRunningError {
	return &buildNotRunningError{
		logicalError{"Cannot stop a non-running build"},
	}
}

type buildNotRunningError struct {
	logicalError
}

// ------------------------------------------------------------------

func NoBuildToStopError() *noBuildToStopError {
	return &noBuildToStopError{
		logicalError{"Cannot stop an inexistant build"},
	}
}

type noBuildToStopError struct {
	logicalError
}
