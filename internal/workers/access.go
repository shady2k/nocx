package workers

// Reachability and revocation (design §7, spec revision 6).
//
// A DELEGATION proves one session may act on one participant. What an
// orchestrating agent needs is a stronger claim: that a PANE belongs to a
// chain of such delegations reaching all the way back to the session it is
// bound as. Resolve answers that by walking Delegation.ControllerSession
// upward, one hop per intermediate participant, until it reaches the bound
// controller or runs out of chain.
//
// REVOCATION MUST NOT BE OUTRUN. §7.2 puts one mutex around delegation
// creation, delegation ending (with its generation bump) and chain
// resolution, so that a spawn racing a revocation of its own controller
// either commits before the revocation is visible at all, or commits after
// and is unreachable from the first Resolve that asks. There is no window
// where it could commit as reachable.

import (
	"context"
	"errors"
	"fmt"
)

// ChainLink is one hop of a resolved chain: the participant whose delegation
// carried it, and the generation that delegation had when it was read. A
// later Resolve or Revoke that changes that delegation's generation makes
// every Chain holding this link stale, which is what StillHolds checks.
type ChainLink struct {
	Participant ParticipantID
	Generation  uint64
}

// Chain is a resolved path, from the target participant up to (but not
// including) the bound controller session.
type Chain []ChainLink

// Reach is what a successful Resolve answers: which participant the session
// belongs to, the session itself (so a caller does not have to re-derive
// it), and the chain that proved it.
type Reach struct {
	Participant Participant
	SessionID   string
	Chain       Chain
}

// ErrNotReachable is every way a session fails to belong to the caller's
// subtree: no participant runs in it, an intermediate delegation does not
// permit the effect, an intermediate delegation has left DelegationActive
// (or been bumped past the generation this walk observed elsewhere), or the
// chain never reaches the bound controller at all. A plain shell is never a
// participant, so it is always this — never a distinct "not a participant"
// answer, because a caller asking whether it may act on a pane does not need
// a second vocabulary for "you may not".
var ErrNotReachable = errors.New("workers: not_reachable")

// maxChainDepth bounds the upward walk. The delegation graph is a DAG by
// construction (a participant is registered once, under one controller
// session, and Register never lets a session delegate to itself), but this
// is a caller-facing entry point and a defensive bound costs nothing against
// a future defect that introduces a cycle.
const maxChainDepth = 64

// Resolve walks Delegation.ControllerSession upward from sessionID's own
// participant until it reaches controller (the session the caller is bound
// as), under storeMu — the same mutex a concurrent Revoke holds while
// ending the delegations this walk reads, so the two serialise rather than
// the walk observing a half-torn subtree.
//
// Every link on the way must be DelegationActive (or InputSuspended,
// through DelegationState.Permits) and must permit e. sessionID naming a
// plain shell, a sibling's descendant, or the controller's own session all
// fail the same way: ErrNotReachable.
func (r *Registrar) Resolve(ctx context.Context, controller, sessionID string, e Effect) (Reach, error) {
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	return r.resolveLocked(ctx, controller, sessionID, e)
}

func (r *Registrar) resolveLocked(ctx context.Context, controller, sessionID string, e Effect) (Reach, error) {
	if controller == "" || sessionID == "" || sessionID == controller {
		return Reach{}, ErrNotReachable
	}
	target, err := r.store.ParticipantBySession(ctx, sessionID)
	if err != nil {
		return Reach{}, ErrNotReachable
	}
	var chain Chain
	cur := target
	for depth := 0; depth < maxChainDepth; depth++ {
		del, err := r.store.Delegation(ctx, cur.ID)
		if err != nil {
			return Reach{}, ErrNotReachable
		}
		if !del.Permits(e) {
			return Reach{}, ErrNotReachable
		}
		chain = append(chain, ChainLink{Participant: cur.ID, Generation: del.Generation})
		if del.ControllerSession == controller {
			return Reach{Participant: target, SessionID: sessionID, Chain: chain}, nil
		}
		next, err := r.store.ParticipantBySession(ctx, del.ControllerSession)
		if err != nil {
			// The chain's next hop names no live participant: either the
			// controller is a session with no worker record (and it is not
			// the one the caller is bound as, checked above), or that
			// intermediate participant has ended. Either way the chain
			// does not reach the bound controller.
			return Reach{}, ErrNotReachable
		}
		cur = next
	}
	return Reach{}, ErrNotReachable
}

// StillHolds re-checks a chain's generations under storeMu. A chain resolved
// before a revocation and the same chain re-checked after it never compare
// equal: the revoked link's generation moved.
func (r *Registrar) StillHolds(ctx context.Context, c Chain) bool {
	if len(c) == 0 {
		return false
	}
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	for _, link := range c {
		del, err := r.store.Delegation(ctx, link.Participant)
		if err != nil || del.Generation != link.Generation {
			return false
		}
	}
	return true
}

// Revoke ends the delegation over root and, because root's own session may
// itself control further participants, recurses into everything IT
// controls — the inclusive subtree §7.2 describes. It returns the pane
// session of every participant whose delegation this call actually ended,
// which is what a caller uses to bump each one's helper access epoch
// (internal/app.paneAccessHub.revoke); a participant already out of
// DelegationActive is left alone and contributes nothing; it did not need
// revoking twice; and neither does whatever it controls, because whatever
// reads the CURRENT chain, is already unreachable through it (Resolve reads
// the ancestor's state fresh on every call — a subtree that was not
// re-walked this time is not thereby reachable again).
//
// cause is carried into the log line only. It decides nothing: the trigger
// set is closed at the call sites (participant terminalized, closed,
// controller admission retired, controller session ended), and Revoke does
// not re-derive which of those happened.
func (r *Registrar) Revoke(ctx context.Context, root ParticipantID, cause string) ([]string, error) {
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	return r.revokeSubtreeLocked(ctx, root, cause, map[ParticipantID]bool{})
}

// RevokeController ends every delegation whose ControllerSession is
// controller, and (through the same subtree walk Revoke uses per
// participant) whatever each of those participants itself controls. Unlike
// Revoke, there is no single Delegation "over" controller to bump first:
// controller may be a coordinator's own session, which is never itself a
// participant.
func (r *Registrar) RevokeController(ctx context.Context, controller string, cause string) ([]string, error) {
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	children, err := r.store.DelegationsBy(ctx, controller)
	if err != nil {
		return nil, fmt.Errorf("worker: revoke controller %q: %w", controller, err)
	}
	visited := map[ParticipantID]bool{}
	var sessions []string
	for _, d := range children {
		s, err := r.revokeSubtreeLocked(ctx, d.Participant, cause, visited)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s...)
	}
	return sessions, nil
}

// revokeSubtreeLocked does the work of Revoke and RevokeController's inner
// loop, under storeMu already held. visited stops a defect that introduced
// a cycle from looping forever; it is never expected to turn anything away
// in a correct tree.
func (r *Registrar) revokeSubtreeLocked(ctx context.Context, id ParticipantID, cause string, visited map[ParticipantID]bool) ([]string, error) {
	if visited[id] {
		return nil, nil
	}
	visited[id] = true

	del, err := r.store.Delegation(ctx, id)
	switch {
	case errors.Is(err, ErrNotDelegated):
		// Nothing controllable over this participant (a registration that
		// never reached step 5, or one already revoked and not since
		// recreated — PutDelegation never removes a row, so this is the
		// "no row at all" case specifically). Nothing to bump, and nothing
		// to cascade: without a delegation this participant controls
		// nothing itself, because DelegationsBy below is keyed by ITS OWN
		// session, which existing only once it is live, and a live
		// participant always has a delegation from step 5.
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("worker: revoke %q: %w", id, err)
	}
	if del.State != DelegationActive && del.State != DelegationInputSuspended && del.State != DelegationScopeSuspended {
		// Already ended. Idempotent: do not re-bump the generation, and do
		// not re-cascade to whatever this participant itself controls —
		// see the doc comment on Revoke for why that is still correct
		// (Resolve reads the ancestor's revoked state fresh every call, so
		// a subtree not re-walked this time is not thereby reachable).
		return nil, nil
	}
	del.State = DelegationRevoked
	del.Generation++
	if putErr := r.store.PutDelegation(ctx, del); putErr != nil {
		return nil, fmt.Errorf("worker: revoke %q: %w", id, putErr)
	}
	r.log.WithContext(ctx).Info("worker: delegation revoked",
		"participant", string(id), "cause", cause, "generation", del.Generation)

	p, err := r.store.Participant(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("worker: revoke %q: %w", id, err)
	}
	var sessions []string
	if p.Liveness.SessionID != "" {
		sessions = append(sessions, p.Liveness.SessionID)
		children, err := r.store.DelegationsBy(ctx, p.Liveness.SessionID)
		if err != nil {
			return nil, fmt.Errorf("worker: revoke %q: %w", id, err)
		}
		for _, c := range children {
			childSessions, err := r.revokeSubtreeLocked(ctx, c.Participant, cause, visited)
			if err != nil {
				return nil, err
			}
			sessions = append(sessions, childSessions...)
		}
	}
	return sessions, nil
}
