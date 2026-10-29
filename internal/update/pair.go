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
// Commit is carried for diagnostics, not compared for certification:
// ADR-0104 permits a compatible same-version coordinator from an earlier
// nightly. The reporting window, however, must match the installed build
// identity recorded from the signed manifest in the transaction journal.
//
// Compatible reports the launcher's wire predicate: true when the attached
// coordinator speaks this build's protocol. It does not grant cross-Version
// reuse (ADR-0104). The probe records the actual attached pair, including an
// older window that attached under ADR-0101 before the takeover policy. The
// transaction keeps that legacy held state distinct from an incompatible
// mixed pair, without weakening certification of the installed version.
type CoordinatorBuild struct {
	Version    string
	Commit     string
	Compatible bool
}

// CoordinatorProbe answers one question: which build is the coordinator
// that is actually answering this client, right now.
//
// One method, because the transaction needs one fact and any second method
// would be a reason for this seam to know about sockets, tokens or
// handshakes — none of which are the updater's business (AD-8).
//
// The production implementation lives with the launcher, which already
// holds the discovery socket's Hello: coordinator.LaunchProbe. There is
// deliberately no "assume this process is the backend" implementation —
// every build in this repo raises a coordinator, so such a probe would
// have no honest caller, and an updater given one would certify exactly
// the mixed pair this file exists to refuse.
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
	ErrPairMismatch = errors.New("update: the reporting pair does not match the build this update installed")

	// ErrPairHeld retains the ADR-0101 held state for already attached
	// windows; new launches take over across Version changes (ADR-0104). The
	// reporting build is healthy, and the coordinator answering it is an
	// OLDER one this window's launcher deliberately kept because it speaks
	// the same protocol. The update cannot finalise yet — its server has
	// not run — so the journal stays; but the pair is held, not wrong, and
	// the auto-rollback is suspended while health reports keep arriving.
	// The finish is a full restart, which the update surface offers as an
	// action, never a force.
	ErrPairHeld = errors.New("update: a compatible older coordinator is still serving — restart fully to finish this update")
)
