package session

// The helper-side half of the access epoch a revocation cannot be outrun by
// (nocx-6q1uh.5, spec §7.2). A revocation, still inside its own coordinator-
// side call, sends `access-bump(session, above)` to every descendant pane's
// helper; this file is what the owner does with it: raise the epoch, refuse
// every intent already queued that predates the raise, and answer only once
// none of them can still commit.
//
// # Why the bump jumps the queue rather than joining it
//
// An ordinary item waits its turn in o.pending and is only looked at once
// the writer is free (owner.go's advance()). A revocation cannot wait for
// that: the whole point of a bump is to bound how long an OLDER intent can
// still land, and an intent already blocked behind a stuck write (spec
// §5.7) would leave the bump waiting exactly as long as that write does —
// which is the wait the commitBy deadline exists to bound, not the bump
// itself. So run() (owner.go) special-cases itemAccessBump the moment it
// comes off o.incoming, before it would ever be appended to o.pending, and
// applyAccessBump both raises the epoch and clears out o.pending in the
// same step: everything still queued there is by definition uncommitted,
// and every uncommitted itemIntent this session holds is refused on the
// spot, so the bump's own answer can return the instant this function does.

import (
	"errors"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// errAccessRevoked names an intent refused because its access epoch is
// older than the one now in force (spec §7.2): either it was still queued
// when a bump raised the epoch (applyAccessBump's own sweep, below), or it
// arrived carrying an epoch nobody had bumped it away from — tokenGate's own
// check, tokens.go, catches that second case, because a bump that has
// already finished sweeping o.pending will never see that intent again.
var errAccessRevoked = errors.New("session: access_revoked")

// errCommitDeadline names an intent refused because its commitBy deadline
// has passed. It is checked in two places for the two ways it can fire:
// tokenGate (tokens.go) refuses it AT RECEIPT, before the intent is ever
// admitted to the runtime's queue; commitIntent (owner.go) refuses it AGAIN
// at the commit point, for the intent that was still within its deadline
// when it arrived and stopped being so while queued behind other input —
// time passing needs no bump to make an old deadline true, unlike the epoch
// check above, which only ever changes at a bump.
var errCommitDeadline = errors.New("session: commit_deadline")

// applyAccessBump is itemAccessBump's own handling, run the moment run()
// (owner.go) receives one — never appended to o.pending, which is what
// makes it "ahead of queued intents" rather than merely first in line for
// its own turn.
//
// It is idempotent on above (spec §7.2): a bump naming an epoch this session
// has already reached or passed changes nothing and revokes nothing, which
// is what lets a coordinator that lost the acknowledgement to a first bump
// send it again without a second, needless sweep of intents that were
// already dealt with (or, worse, of intents a LATER, legitimate bump has
// since queued and gated under the epoch already in force).
func (o *sessionOwner) applyAccessBump(it ownerItem) {
	current := o.accessEpoch.Load()
	proposed := it.above + 1
	if proposed <= current {
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateExecuted, Epoch: current})
		return
	}
	o.accessEpoch.Store(proposed)

	// Every itemIntent still in o.pending predates this bump — an intent
	// that arrived AFTER it would have been gated (tokenGate, tokens.go)
	// against the epoch this store just raised, and would already answer
	// access_revoked there rather than ever reaching this queue. So nothing
	// here needs to inspect an intent's own claimed epoch: being queued at
	// all is what makes it "older".
	kept := o.pending[:0]
	for _, queued := range o.pending {
		// Only a TOKEN-bearing intent is this protocol's concern at all
		// (spec §7.2 is entirely about session.intent's own token/epoch
		// pair); a token-less intent — Task 2's own tests, still the one
		// production path with nothing to check — has no epoch to be older
		// than and is left queued exactly as an itemResize or itemReply is.
		if queued.kind != itemIntent || queued.intent.Token.ID == (TokenID{}) {
			kept = append(kept, queued)
			continue
		}
		res := ownerResult{State: sessionruntime.IntentStateRefused, Err: errAccessRevoked}
		o.recordTokenOutcome(queued.intent, res)
		o.resolve(queued, res)
	}
	o.pending = kept

	// Nothing is in flight (writerBusy) UNDER THE NEW EPOCH by construction:
	// commitIntent only ever runs on an item this bump's own sweep just
	// removed, or on one gated after this Store — either way its epoch is
	// current. An item already writing when this bump arrived was gated,
	// admitted and committed under a PRIOR epoch and is left to finish: spec
	// §7.2 bounds it by commitBy, not by asking the owner to abandon bytes
	// already in flight to the program.
	o.resolve(it, ownerResult{State: sessionruntime.IntentStateExecuted, Epoch: proposed})
}

// currentAccessEpoch answers this session's access epoch as of now (spec
// §7.2, §6.1: "reported in every snapshot"). It is read from outside the
// owner's own goroutine — takeSnapshot, snapshots.go, the same caller
// inputFence already serves — which is why accessEpoch is an atomic rather
// than a plain field like most of sessionOwner's state.
func (o *sessionOwner) currentAccessEpoch() uint64 {
	return o.accessEpoch.Load()
}
