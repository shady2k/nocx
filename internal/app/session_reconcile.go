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

// generationOwnedInventory identifies the helper generation this inventory
// answers for. It is optional so existing coordinator-local inventories keep
// their Owns decision, while persisted helper generations are matched before
// any helper call is made.
type generationOwnedInventory interface {
	Generation() string
}

// targetOwnedInventory identifies the execution host and account answered by
// an inventory. Generation alone is not enough: the same helper build can run
// on multiple hosts.
type targetOwnedInventory interface {
	Host() string
	Account() string
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
	readopt sessionReadopter,
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

	// Answers are memoized per inventory, but an inventory is queried only
	// after a pending session's persisted generation has selected it. This
	// ordering is the safety property: asking a helper about another
	// generation's id space could turn its truthful absence into deletion.
	answers := make([]map[string]struct{}, len(inventories))
	failures := make([]error, len(inventories))
	asked := make([]bool, len(inventories))

	// judgeFrom turns ONE inventory's answer into ONE verdict, and it is the
	// only place in this file that does. Two callers — an inventory this
	// coordinator already held, and one a re-adoption brought into existence —
	// and one rule for both, because "an error is unknown, an answer that names
	// the session is live, an answer that does not is absent" is a single
	// decision and two copies of it would be two decisions that agree until
	// they do not (AD-8).
	judgeFrom := func(j *content.SessionJudgement, sessionID string, live map[string]struct{}, err error) {
		switch {
		case err != nil:
			j.Verdict = content.VerdictUnknown
			j.Cause = causeFor(err)
			j.Detail = err.Error()
		default:
			if _, isLive := live[sessionID]; isLive {
				j.Verdict = content.VerdictLive
			} else {
				// The one path to absent: an inventory that owns this id
				// space was asked, answered, and does not report it.
				j.Verdict = content.VerdictAbsent
			}
		}
	}

	for _, p := range pending {
		j := content.SessionJudgement{SessionID: p.SessionID}
		owner := -1
		matches := 0
		for i, inv := range inventories {
			if typed, ok := inv.(generationOwnedInventory); ok {
				if p.Generation == "" || typed.Generation() != p.Generation {
					continue
				}
			}
			if typed, ok := inv.(targetOwnedInventory); ok {
				if p.Host != typed.Host() || p.Account != typed.Account() {
					continue
				}
			} else if p.Host != "" || p.Account != "" {
				// A stored target must not be judged by an inventory that
				// cannot prove it answers that target.
				continue
			}
			if inv.Owns(p.SessionID) {
				owner = i
				matches++
			}
		}
		switch {
		case matches > 1:
			// Multiple owners indicate duplicate registration. Choosing one
			// would make a broken ownership map silently first-wins.
			j.Verdict = content.VerdictUnknown
			j.Cause = content.CauseAmbiguousInventory
			j.Detail = "multiple inventories claim this session id space"
		case matches == 1:
			if !asked[owner] {
				answers[owner], failures[owner] = inventories[owner].LiveSessions(ctx)
				asked[owner] = true
			}
			judgeFrom(&j, p.SessionID, answers[owner], failures[owner])
		case readopt == nil:
			j.Verdict = content.VerdictUnknown
			j.Cause = content.CauseNoInventory
		default:
			// NOBODY THIS COORDINATOR ALREADY HOLDS CAN JUDGE IT — which on a
			// cold start is every carried-over session, because helper channels
			// are opened by tabs and a process that has opened no tab has none.
			// This is where the binding stops being only a fact and becomes a
			// connection: the readopter dials the generation the binding names,
			// asks it once, and takes the session back if it is there
			// (nocx-k6p18.30).
			//
			// THE ANSWER IS NOT REUSED FOR THE NEXT SESSION, deliberately. Each
			// carried-over session is offered to the readopter on its own, so
			// two sessions on one host are two re-adoptions rather than one
			// re-adoption and one session judged live and left behind. The cost
			// is one helper channel per session, which is what OpenHosted
			// already spends and for the same reason.
			inv, readoptErr := readopt.Readopt(ctx, p)
			switch {
			case readoptErr != nil:
				// A failure is never a verdict, here least of all: this is the
				// branch a host that is simply switched off arrives on, and
				// `absent` would delete the recording of a build that is merely
				// out of reach.
				j.Verdict = content.VerdictUnknown
				j.Cause = causeFor(readoptErr)
				j.Detail = readoptErr.Error()
			case inv == nil || !inv.Owns(p.SessionID):
				// No route was recorded, or what answered does not own this id
				// space. Either way nobody may judge it, which is exactly the
				// answer that stood before re-adoption existed.
				j.Verdict = content.VerdictUnknown
				j.Cause = content.CauseNoInventory
			default:
				live, liveErr := inv.LiveSessions(ctx)
				judgeFrom(&j, p.SessionID, live, liveErr)
			}
		}
		if applyErr := rec.Apply(ctx, j); applyErr != nil {
			// The session stays pending and the next pass repeats it.
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
