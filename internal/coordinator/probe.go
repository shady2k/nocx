package coordinator

// The launcher's answer to the updater's pair question (design D4).
//
// internal/update states the question and refuses to certify without an
// answer (update/pair.go); it deliberately says nothing about how the
// answer is obtained, because sockets, handshakes and tokens are not the
// updater's business (AD-8). This file is the other half: the launcher
// already asked the discovery socket who is serving this window and holds
// the reply, so the fact is here and nowhere else.
//
// It is the HANDSHAKE'S answer, never a derivation. A version read off the
// binary beside the executable, or off the bundle that was just swapped
// in, describes a file on disk; the defect D4 exists for is precisely a
// file on disk that is not the process running.

import (
	"context"
	"errors"
	"sync"

	"github.com/shady2k/nocx/internal/update"
)

// LaunchProbe is the production [update.CoordinatorProbe]: it reports the
// build of the coordinator this window actually attached to.
//
// It is settable rather than constructed with its answer because of the
// order the composition root is obliged to run in. The updater must exist
// and Reconcile BEFORE the launcher runs — reconciliation is what counts
// launch attempts and rolls back a release that cannot start, so a build
// too broken to raise a coordinator is exactly the build that needs it —
// and the coordinator's identity is not known until afterwards. So the
// probe is wired empty and filled by the launch it belongs to.
//
// Empty is not "assume it is fine": [LaunchProbe.AnsweringCoordinator]
// fails until a launch has attached one, and the updater turns that into
// ErrPairUnverifiable and keeps the rollback journal.
type LaunchProbe struct {
	mu       sync.RWMutex
	build    Build
	attached bool
}

// NewLaunchProbe returns a probe with no coordinator attached yet.
func NewLaunchProbe() *LaunchProbe { return &LaunchProbe{} }

// Attach records the coordinator a [Launcher] resolved for this window.
//
// It takes the whole [Launch] rather than a version string so the caller
// cannot pass a number it got from somewhere else: the only value that
// reaches here is the one the daemon put on the wire.
func (p *LaunchProbe) Attach(l Launch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.build = l.Hello.Build
	p.attached = true
}

// AnsweringCoordinator reports the attached coordinator's build.
//
// The context is unused and that is not an oversight: the question was
// answered over the discovery socket before the renderer was allowed to
// connect, so there is nothing left to wait on and nothing to cancel.
// Answering from the recorded handshake is also what makes the answer
// honest at the moment health is reported — a fresh dial could reach a
// coordinator this window never spoke to.
func (p *LaunchProbe) AnsweringCoordinator(context.Context) (update.CoordinatorBuild, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.attached {
		return update.CoordinatorBuild{}, errors.New(
			"coordinator: this window has not attached to a coordinator, so there is no backend build to report")
	}
	return update.CoordinatorBuild{Version: p.build.Version, Commit: p.build.Commit}, nil
}
