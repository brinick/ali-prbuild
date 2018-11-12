package prbuild

import (
	"github.com/brinick/ali-ci/pullrequest"
)

// ------------------------------------------------------------------

// TODO: do we really need this interface?
type prBuilder interface {
	Start(*pullrequest.PR) error
	Stop() error
	isActive() bool
}

// ------------------------------------------------------------------

// Builder is the type that manages building of pull requests
type Builder struct {
	current  *Build
	previous *Build
}

// ------------------------------------------------------------------

// NewBuilder returns a builder that oversees
// the building of pull requests
func NewBuilder() *Builder {
	return &Builder{}
}

// Start will launch aliBuild on the given pull request.
// If a build is already ongoing, a BuildOngoingError is returned.
func (b *Builder) Start(pr *pullrequest.PR) error {
	if b.isActive() {
		return BuildOngoingError(b.current.pr.Number())
	}

	// builder may have a reference to a done build
	if b.current != nil {
		b.rotate()
	}

	b.current = NewBuild(pr)
	return b.launch()
}

// Stop shut downs the ongoing pull request build
func (b *Builder) Stop() error {
	if b.current == nil {
		return NoBuildToStopError()
	}

	b.current.stop()
	if b.current.HasError() {
		return b.current.Error()
	}

	b.rotate()
	return nil
}

// isActive returns a boolean indicating if there
// is a pull request build currently ongoing
func (b Builder) isActive() bool {
	return b.current != nil && b.current.Running()
}

func (b *Builder) rotate() {
	b.previous, b.current = b.current, nil
}

// launch will start the underlying pull request build
func (b *Builder) launch() error {
	if b.current == nil {
		return NothingToBuildError("Builder has no pull request to build")
	}

	b.current.start()
	return b.current.Error()
}
