package shellintegration

import (
	"context"
	"errors"
	"sync"
)

// §6.1: nothing is minted before it can be used.
//
// A bearer is never handed over before we have proven it can be exercised.
// The design fixes the order and this file is the half of it the backend can
// enforce on its own:
//
//	1. mux ownership proven;
//	2. the publish and the loader start CONCURRENTLY;
//	3. frame 1 received and verified;
//	4. the lifecycle transport and its receiver fully ready;
//	5. the publish attempt reaches a terminal outcome;
//	6. only after 4 AND 5 is the capability and fence pair minted;
//	7. frame 2 delivered to stage-1;
//	8. far-side verification of the generation as it now stands;
//	9. exec, or a named conventional outcome.
//
// # Why step 5 is here at all, and what it is NOT
//
// It closes a mutation race, and it does not let the publish decide validity.
// Without it stage-1 can verify the manifest microseconds before an atomic
// commit and degrade a session whose publish then succeeds. The converse is
// the half that is easy to get backwards: after a FAILED publish the far side
// may still accept a generation installed earlier, so a failed publish is not
// a refusal — every PublishOutcome below opens the gate, and only the RECEIVER
// can close it.
//
// # Why steps 4 and 5 are the two the forgery rules lean on
//
// Both are facts of the backend's OWN (§5.5, §6.1): the receiver is a listener
// we opened, the publish outcome is a call we made, and nothing written on the
// session's PTY can force either. Step 3 is different — "frame 1 received and
// verified" is known to us only because stage-1 says so, on the terminal, and
// that token is forgeable by anyone who can write it. So the gate is what
// bounds a forgery: no forged token brings a mint forward past a receiver that
// does not exist or a publish that has not settled. The rules that shrink the
// remainder to a race live in bootstrap.go (the token gate and the seal).

// PublishOutcome is the terminal outcome of the publish attempt — §6.1 step
// 5's "committed, unchanged, failed or contended". The set is closed and every
// member opens the gate: the far side re-proves the generation as it now
// stands (step 8), and it is the only party entitled to answer that.
type PublishOutcome string

const (
	// PublishCommitted: this attempt atomically replaced the manifest.
	PublishCommitted PublishOutcome = "committed"
	// PublishUnchanged: the far side already carried this content digest, so
	// nothing was replaced and nothing needed to be.
	PublishUnchanged PublishOutcome = "unchanged"
	// PublishFailed: the attempt did not commit. NOT a refusal — see above.
	PublishFailed PublishOutcome = "failed"
	// PublishContended: another publisher held the destination (§6.3). Also
	// not a refusal; the far side decides on the generation it finds.
	PublishContended PublishOutcome = "contended"
	// PublishNotAttempted: no publisher was wired for this session, so the
	// attempt reached its terminal outcome before it began. It is a settled
	// outcome rather than an absent one, because an absent one would leave
	// the gate waiting for a fact nobody will ever supply.
	PublishNotAttempted PublishOutcome = "not-attempted"
)

// ErrReceiverUnavailable is what Await returns when the lifecycle transport
// or its receiver could not be brought up. The caller mints NOTHING and
// hands stage-1 a non-secret refusal (§6.1): delivering a secret and then
// discarding it hands a bearer across a boundary before establishing that it
// has any use.
var ErrReceiverUnavailable = errors.New("shellintegration: lifecycle receiver unavailable")

// MintGate is §6.1 steps 4 and 5 as one object: the two backend facts that
// must both be in before FRAME 2 is delivered — of which minting is the case
// where there is something to mint. A session with no lifecycle channel mints
// nothing and still waits here, because the frame it sends instead is followed
// by the same step 8.
//
// It holds NO timer of its own, deliberately. Every deadline in this design is
// driven by an injected clock and asserted as an event (§11's opening
// sentence), so the bound on waiting here belongs to the context the caller
// passes — which is the attempt's own budget, bounded at the composition root
// by PublishDeadline below. A gate that started its own timer would be a
// second, unsynchronised clock in the one place the design is arithmetic
// about.
type MintGate struct {
	mu sync.Mutex
	// receiverErr is non-nil once the receiver has failed, and receiverC is
	// closed once either answer is in. The pair is what makes "not answered
	// yet" distinguishable from "answered nil".
	receiverAnswered bool
	receiverErr      error
	receiverC        chan struct{}

	publishAnswered bool
	publishOutcome  PublishOutcome
	publishC        chan struct{}
}

// NewMintGate returns a gate with neither fact in.
func NewMintGate() *MintGate {
	return &MintGate{
		receiverC: make(chan struct{}),
		publishC:  make(chan struct{}),
	}
}

// ReceiverReady records §6.1 step 4: the lifecycle transport and its receiver
// are fully ready. The FIRST answer wins — a gate is answered once per fact,
// and a later answer never reopens it.
func (g *MintGate) ReceiverReady() { g.answerReceiver(nil) }

// ReceiverUnavailable records that step 4 will never be true. Nothing is
// minted after it, on any path.
func (g *MintGate) ReceiverUnavailable(err error) {
	if err == nil {
		err = ErrReceiverUnavailable
	}
	g.answerReceiver(err)
}

func (g *MintGate) answerReceiver(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.receiverAnswered {
		return
	}
	g.receiverAnswered = true
	g.receiverErr = err
	close(g.receiverC)
}

// PublishSettled records §6.1 step 5. The first answer wins.
func (g *MintGate) PublishSettled(o PublishOutcome) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.publishAnswered {
		return
	}
	g.publishAnswered = true
	g.publishOutcome = o
	close(g.publishC)
}

// Await blocks until BOTH facts are in and reports the publish outcome, or
// returns an error and mints nothing.
//
// BOTH, including when the receiver answered with a refusal. This file's first
// draft returned on the receiver's error alone, and that is the whole of what
// made a refused lifecycle forward degrade a session it should have left
// intact: step 5 does not gate the MINT, it gates FRAME 2, because step 8 —
// the far side re-proving the generation as it now stands — happens after
// frame 2 whether that frame carries a bearer or a refusal. Returning early
// re-opened, for the refusal path only, exactly the mutation race step 5
// exists to close: stage-1 read its frame while the publish was still in
// flight, found no launch carrier, and named generation-unavailable for a
// session whose publish committed a moment later.
//
// A cancelled context is an error for the same reason a refused receiver is:
// the attempt is over, and a bearer minted for it could only be handed to
// somebody who is no longer waiting for one.
func (g *MintGate) Await(ctx context.Context) (PublishOutcome, error) {
	select {
	case <-g.receiverC:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case <-g.publishC:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.receiverErr != nil {
		return "", g.receiverErr
	}
	return g.publishOutcome, nil
}

// ---------------------------------------------------------------------------
// The publish wall-clock
// ---------------------------------------------------------------------------

// PublishDeadline is T from §7: the publish wall-clock. Exported here rather
// than duplicated, so the schedule and the publisher cannot drift. It is what
// bounds the mint's wait on the gate at the composition root — a publish that
// never settles must not hold the frame, and the session in `starting`, for
// the life of the tab.
//
// The rest of §7's arithmetic — the whole bootstrap as a graph, and the proof
// that its longest path closes the integration deadline — is a claim ABOUT
// this package rather than behaviour of it, and lives with the assertion that
// consumes it in ordering_test.go. Nothing the product runs reads the graph;
// putting it here would be a second declaration of the same numbers for no
// reader.
const PublishDeadline = publishDeadline
