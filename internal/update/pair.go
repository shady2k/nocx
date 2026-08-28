package update

// The pair check (D4 of the nocx-server design). An update is certified by
// the UI reporting itself healthy, and until now that report said nothing
// about which BACKEND answered it. With a coordinator that outlives the
// window, the two are separate processes with separate versions, and the
// old one survives the bundle swap: version N+1's renderer mounts, opens a
// pane against version N's coordinator, reports healthy, and finalisation
// deletes the rollback journal for a release whose server has never run.
//
// So health is a claim about a PAIR — the UI that reports it and the
// coordinator that answered it — and this file is the fact the transaction
// needs to check that claim. It states nothing about how the fact is
// obtained: the launcher holds the discovery socket and the handshake
// (internal/coordinator), and it is the launcher that injects a probe.

import (
	"context"
	"errors"
)

// CoordinatorBuild is what the coordinator answering this client says it
// was built from. It is the daemon's own answer, never a guess derived
// from a path or a file's mtime — a binary on disk is not the binary
// running, which is the whole defect this file exists for.
//
// Commit is carried because the handshake carries it and a bug report
// needs it. It is deliberately NOT compared: the release manifest names a
// version and no commit, so the journal has no expected commit to compare
// against, and a check against a fact nobody recorded is theatre.
type CoordinatorBuild struct {
	Version string
	Commit  string
}

// CoordinatorProbe answers one question: which build is the coordinator
// that is actually answering this client, right now.
//
// One method, because the transaction needs one fact and any second method
// would be a reason for this seam to know about sockets, tokens or
// handshakes — none of which are the updater's business (AD-8).
//
// The production implementation lives with the launcher, which already
// holds the discovery socket's Hello. [NewInProcessCoordinatorProbe] is
// for a build whose backend is the window's own process.
type CoordinatorProbe interface {
	// AnsweringCoordinator returns the build of the coordinator serving
	// this client. An error means the question could not be answered,
	// which is NOT the same as a mismatch and is never treated as one:
	// both refuse to certify, and they say different things to the user.
	AnsweringCoordinator(ctx context.Context) (CoordinatorBuild, error)
}

var (
	// ErrPairUnverifiable reports that the answering coordinator's build
	// could not be established at all — no probe was wired, or the probe
	// failed. Finalisation does not happen and the journal stays, which
	// is the direction to fail in: an update that cannot be certified
	// keeps its rollback.
	ErrPairUnverifiable = errors.New("update: cannot establish which coordinator is answering")

	// ErrPairMismatch reports that the answering coordinator is a
	// different build from the one this update installed — the mixed
	// pair. Same consequence, different cause, and a user reading the
	// message needs to be told which of the two happened.
	ErrPairMismatch = errors.New("update: the coordinator answering is not the version this update installed")
)

// inProcessProbe answers with a build the caller states, for the case
// where there is no separate coordinator to ask: the backend is this
// process, so its version is the running binary's.
//
// It exists so that "there is no daemon" is something the composition
// root SAYS rather than something the updater infers from a nil field. A
// nil probe means nobody decided, and the whole defect above is what
// happens when an unstated assumption about the backend's identity is
// allowed to certify an update.
type inProcessProbe struct{ build CoordinatorBuild }

// NewInProcessCoordinatorProbe returns a probe for a build whose backend
// runs inside the window's own process. Pass internal/version's Version
// and Commit: they describe the binary that is executing this call, which
// in that arrangement is also the backend.
func NewInProcessCoordinatorProbe(version, commit string) CoordinatorProbe {
	return inProcessProbe{build: CoordinatorBuild{Version: version, Commit: commit}}
}

func (p inProcessProbe) AnsweringCoordinator(context.Context) (CoordinatorBuild, error) {
	return p.build, nil
}
