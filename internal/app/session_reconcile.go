package app

// The composition root's half of restart reconciliation (nocx-k6p18.5;
// internal/content/reconcile.go carries the decision).
//
// THE ORDER IS THE WHOLE DESIGN. `Open` cannot ask whether an inherited
// session still exists, because asking needs a carrier, the carrier may need
// the vault, and the vault needs the store. So the store opens and judges
// nothing, the carriers come up, an inventory is asked, and each session is
// reconciled here — on the store's one connection, so ADR-0043 is untouched.
//
// A FAILURE IS NEVER A VERDICT. Everything in this file exists to make the
// honest answer the easy one: an inventory that could not be reached produces
// `unknown` with the cause it failed for, and the only path to `absent` runs
// through an inventory that ANSWERED. `absent` deletes a recording and closes
// a block; `unknown` costs a week of disk. They are not symmetric and the code
// must not treat them as though they were.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/vault"
)

// sessionInventory answers, for one id space, which sessions a reachable
// generation still holds. It is the seam the helper's `sessions` op fills
// (internal/helper/proto.OpSessions, nocx-k6p18.3) once a coordinator records
// WHICH generation a stored session belongs to.
//
// Two methods rather than one, and the split is the safety property: `Owns`
// says whether this inventory is entitled to judge that id at all, and
// `LiveSessions` is asked only about ids it owns. An inventory answering about
// an id space it does not own would turn "I do not hold that" — a true
// statement about a helper's own sessions — into `absent` for somebody else's,
// which deletes live work.
type sessionInventory interface {
	Owns(sessionID string) bool
	// LiveSessions is the ids this generation reports. An error is NEVER a
	// verdict: the caller turns it into `unknown` with a cause.
	LiveSessions(ctx context.Context) (map[string]struct{}, error)
}

// reconcileSessions is one pass: every session the store carried over gets
// exactly one verdict, and then the age bound runs.
//
// It is idempotent and resumable by construction — it reads the pending set
// each time and a session that fails to be swept stays pending — so calling it
// again is always safe and is what a later carrier does.
func reconcileSessions(
	ctx context.Context,
	rec content.SessionReconciler,
	inventories []sessionInventory,
	retention time.Duration,
	logger *slog.Logger,
) {
	pending, err := rec.Pending(ctx)
	if err != nil {
		logger.Warn("the sessions carried over from the last start could not be listed", "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	// One ask per inventory, not per session: an inventory is a list, and
	// asking it once per row would be N round trips for one answer. The error
	// is KEPT rather than logged and dropped, because it is the cause every
	// session under that inventory is then reported with.
	answers := make([]map[string]struct{}, len(inventories))
	failures := make([]error, len(inventories))
	for i, inv := range inventories {
		answers[i], failures[i] = inv.LiveSessions(ctx)
	}

	for _, p := range pending {
		j := content.SessionJudgement{SessionID: p.SessionID}
		owner := -1
		for i, inv := range inventories {
			if inv.Owns(p.SessionID) {
				owner = i
				break
			}
		}
		switch {
		case owner < 0:
			// NOBODY OWNS THIS ID SPACE, so there is nothing to ask and the
			// answer is unknown. It is the ordinary case today: no coordinator
			// yet records which generation a stored session belongs to, so no
			// inventory is entitled to judge one. Asking a helper about a
			// session it never spawned would get a truthful "I do not hold
			// that" and turn it into a deletion.
			j.Verdict = content.VerdictUnknown
			j.Cause = content.CauseNoInventory
		case failures[owner] != nil:
			j.Verdict = content.VerdictUnknown
			j.Cause = causeFor(failures[owner])
			j.Detail = failures[owner].Error()
		default:
			if _, live := answers[owner][p.SessionID]; live {
				j.Verdict = content.VerdictLive
			} else {
				// The one path to absent: an inventory that owns this id was
				// asked, answered, and does not report it.
				j.Verdict = content.VerdictAbsent
			}
		}
		if applyErr := rec.Apply(ctx, j); applyErr != nil {
			// The session stays pending and the next pass repeats it. Not a
			// startup failure: a terminal that refuses to open because one
			// verdict could not be written is worse than one that opens and
			// tries again.
			logger.Warn("a carried-over session could not be reconciled",
				"session", p.SessionID, "verdict", string(j.Verdict), "error", applyErr)
		}
	}

	// The replacement bound, and it runs every pass rather than only at
	// startup: `dropDeadSessions` was the ONLY bound on session recordings,
	// and the absent path restores it only for hosts that come back. This is
	// the other half, for the ones that never do.
	if swept, sweepErr := rec.SweepStale(ctx, retention); sweepErr != nil {
		logger.Warn("the recordings of unreachable hosts could not be bounded by age", "error", sweepErr)
	} else if swept > 0 {
		logger.Info("recordings of sessions nobody could ask about were removed by age",
			"sessions", swept, "age", retention.String())
	}
}

// causeFor classifies why an inventory could not answer, into the closed
// vocabulary the renderer picks its sentence from.
//
// EVERY BRANCH RETURNS AN UNKNOWN CAUSE. There is deliberately no path from an
// error to `absent`, and this function's type is what says so: it cannot
// return a verdict at all. A refused connection, a timeout, a sealed vault and
// an unreachable host are four different sentences and one verdict.
//
// The default is `hostUnreachable` rather than anything more specific: an
// error this build cannot classify is still an error, and an unclassified
// failure must not fall through to something that reads as an answer.
func causeFor(err error) content.UnreconciledCause {
	switch {
	case err == nil:
		return content.CauseNotYetAsked
	case errors.Is(err, vault.ErrVaultSealed), errors.Is(err, vault.ErrVaultUninitialized),
		errors.Is(err, vault.ErrNoUnlockClient), errors.Is(err, vault.ErrUnlockSuspended):
		return content.CauseVaultSealed
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return content.CauseTimedOut
	case errors.Is(err, syscall.ECONNREFUSED):
		return content.CauseConnectionRefused
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return content.CauseTimedOut
	}
	// The string check is the LAST resort and it is honest about being one:
	// SSH and the helper lane wrap their failures in errors that carry no
	// sentinel, so the alternative to reading the words is reporting every
	// one of them as the same thing. Either way the verdict is unknown, so the
	// worst a wrong guess costs is a less precise sentence.
	if strings.Contains(strings.ToLower(err.Error()), "connection refused") {
		return content.CauseConnectionRefused
	}
	return content.CauseHostUnreachable
}
