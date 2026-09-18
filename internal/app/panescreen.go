package app

// The coordinator's half of the pane screen: which helper holds a pane, and how
// a frame is asked for.
//
// The composition root owns this because it is the only place that holds both
// halves: the session registry (which knows what kind of session a pane is) and
// the two helper routes (this machine's daemon, and one process per remote
// generation).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
)

// paneScreenTimeout bounds ONE frame read.
//
// It is a REQUEST bound and not a policy, exactly as the helper client's signal
// seam states it: the callers are a one-second observer sweep and a write gate
// is mid-decision, and neither has a lifetime to thread, so the call that
// reaches another process bounds itself. Without it a helper that stopped
// answering would park the sweep for as long as the socket lives, and every
// other pane's observation behind it.
const paneScreenTimeout = 5 * time.Second

// errNoPaneRuntime is the refusal a pane gets when no helper holds its terminal.
//
// It is a sentence rather than a code because a person reads it: the enrolment
// this refuses is one a shell asked for, and the answer reaches that shell's own
// pane (D4 — "no enrolment, no orchestration, and the pane says so").
//
// WHAT RAISES IT NOW (nocx-50w7p.5). The predicate is owner() below, and it
// raises this for a session that is neither this machine's opener's nor a
// remote registry's — a pane no helper holds, which is exactly what the
// sentence says. Before this bead the same predicate also caught a pane whose
// terminal WAS held: an ssh pane opened by this machine's helper has a remote
// destination, and an owner that routed by kind sent it looking for a far
// helper's registry entry it could never have. The doc's old claim — that
// nocx-ygxjv.13 would make this unreachable by moving every PTY under a helper
// — was right about the direction and wrong about the trigger: the panes became
// the helper's in 08e90002, and the refusal stayed reachable because the
// predicate was still asking the wrong question. It asks the opener now, so
// what is left here is the honest case, and this error is genuinely "no helper
// holds it" and nothing else.
var errNoPaneRuntime = errors.New(
	"nocx cannot watch this pane: no helper is holding its terminal, so there is no screen to read")

// paneScreen is the ONE implementation of paneview.Source.
//
// There is deliberately no second, in-process one. The owner's decision is that
// every session's PTY or channel is held by a helper, so a coordinator-side
// runtime does not exist to read — and a second implementation written "for
// later" would be the second emulator ADR-0066 refuses, arriving by the front
// door.
type paneScreen struct {
	log      *slog.Logger
	registry *session.Reg
	local    *localHelperOpener
	remote   *helperRegistry
}

var _ paneview.Source = (*paneScreen)(nil)

func newPaneScreen(lg *slog.Logger, registry *session.Reg, local *localHelperOpener, remote *helperRegistry) *paneScreen {
	return &paneScreen{log: lg, registry: registry, local: local, remote: remote}
}

// Available reports whether a pane's screen can be read at all.
//
// It asks the generation for the frame and DROPS it, which is one round trip
// and one frame's bytes at enrolment rather than at every sweep. That is the
// honest probe: the question "can this peer report a screen at all" is answered
// by the peer, and a generation older than the runtime is exactly the case this
// must catch before a watch exists over a pane nothing can answer about.
func (p *paneScreen) Available(paneID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), paneScreenTimeout)
	defer cancel()
	c, id, err := p.owner(ctx, paneID)
	if err != nil {
		return err
	}
	if _, err := c.Screen(ctx, id); err != nil {
		return err
	}
	return nil
}

// Screen reads what a pane's runtime holds now.
func (p *paneScreen) Screen(paneID string) (paneview.Frame, error) {
	ctx, cancel := context.WithTimeout(context.Background(), paneScreenTimeout)
	defer cancel()
	c, id, err := p.owner(ctx, paneID)
	if err != nil {
		return paneview.Frame{}, err
	}
	return c.Screen(ctx, id)
}

// owner finds the helper that holds a pane's terminal, and the handle that
// helper knows it by.
//
// THE QUESTION IS "WHICH HELPER HOLDS IT", AND THE OPENER THAT OPENED IT IS
// WHERE THAT FACT LIVES (nocx-50w7p.5). It is NOT the session's kind: kind says
// where the DESTINATION is, and since this machine's daemon opens ssh panes the
// two stopped coinciding — an ssh pane has a remote destination and the local
// carrier, so a kind-led owner sent it to the far-helper registry, which knows
// only panes IT opened, and then refused a pane whose terminal is very much
// being held. The backend answers every session; that one it could not answer
// at all.
//
// ASKING THE OPENER FIRST IS NOT A PREFERENCE, it is the same order the
// composition root already uses to OPEN a pane: this machine's opener claims
// what is its own, and the remote registry answers for a helper that is not
// here. A session is in exactly one of the two, which is what keeps ADR-0057's
// no-fallback property: the local route cannot answer for a far helper's id
// space any more than that helper can answer for this daemon's.
//
// THE SESSION'S KIND IS NOT CONSULTED AT ALL, and that absence is the fix.
// Kind says where the destination is; this question is about the carrier, and
// the two stopped coinciding the moment this machine's daemon could open an ssh
// pane. Keeping kind as a second, fallback answer — "local if the kind says
// local" — would be two derivations of one fact (AD-8), and the one that
// disagrees would be the one that runs. A local pane is covered without it:
// this opener notes every session it opens or re-adopts, and a local pane is
// one of those.
func (p *paneScreen) owner(ctx context.Context, paneID string) (*helperclient.Client, helperclient.HostSessionID, error) {
	sid := session.ID(paneID)
	if _, err := p.registry.Get(sid); err != nil {
		// The session is gone. It is the same refusal as "no helper holds it",
		// because the answer a caller acts on is the same: there is no screen
		// to read, and there will not be one.
		return nil, helperclient.HostSessionID{}, fmt.Errorf("%w: %s", errNoPaneRuntime, paneID)
	}
	if p.local != nil && p.local.holds(sid) {
		return p.local.screenClient(ctx, paneID)
	}
	if h, ok := p.remote.hostFor(sid); ok {
		return h.screenClient(ctx, paneID)
	}
	return nil, helperclient.HostSessionID{}, fmt.Errorf("%w: %s", errNoPaneRuntime, paneID)
}

// HelperFor implements paneHelperLookup (pane_access.go): the same question
// owner answers for a screen read, wrapped for the ops revocation and
// session.read both need (AccessBump, Snapshot, Target) — never a second
// derivation of "which helper holds this pane's terminal". ok is false for
// exactly the same reason owner returns errNoPaneRuntime: a session nothing
// holds any more has no helper to ask.
func (p *paneScreen) HelperFor(ctx context.Context, sessionID string) (paneHelpers, bool) {
	c, id, err := p.owner(ctx, sessionID)
	if err != nil {
		return nil, false
	}
	return helperPaneClient{client: c, id: id}, true
}

// helperPaneClient adapts a helper's wire client to paneHelpers for ONE
// session's HostSessionID, resolved once by HelperFor above. The sessionID
// parameter each method still takes is part of the paneHelpers contract
// (a single implementation could in principle serve several sessions); this
// adapter is bound to one and ignores it.
type helperPaneClient struct {
	client *helperclient.Client
	id     helperclient.HostSessionID
}

func (h helperPaneClient) AccessBump(ctx context.Context, _ string, above uint64) (uint64, error) {
	result, err := h.client.AccessBump(ctx, h.id, above)
	if err != nil {
		return 0, err
	}
	return result.Epoch, nil
}

func (h helperPaneClient) Snapshot(ctx context.Context, _ string) (proto.SnapshotResult, error) {
	return h.client.Snapshot(ctx, h.id)
}

func (h helperPaneClient) Target(ctx context.Context, _ string, p proto.TargetParams) (proto.TargetResult, error) {
	p.Session = proto.HostSessionID{Generation: proto.GenerationID(h.id.Generation), Session: h.id.Session}
	return h.client.Target(ctx, p)
}

func (h helperPaneClient) Intent(ctx context.Context, _ string, p proto.IntentParams) (proto.IntentResult, error) {
	p.Session = proto.HostSessionID{Generation: proto.GenerationID(h.id.Generation), Session: h.id.Session}
	return h.client.Intent(ctx, p)
}

func (h helperPaneClient) IntentStatus(ctx context.Context, _ string, tokenID string) (proto.IntentStatusResult, error) {
	return h.client.IntentStatus(ctx, h.id, tokenID)
}

// paneReplay is the calibration replay, over the LOCAL helper.
//
// It is the local daemon and not the pane's own helper — a stored set belongs
// to no session, and the owner's decision is that this machine's helper is what
// answers (the alternative would be asking a remote host to replay a file it
// does not have).
type paneReplay struct {
	local *localHelperOpener
}

var _ agentcapture.Replay = paneReplay{}

// Replay feeds a capture's bytes to the helper's PTY-less emulator.
//
// The context is the caller's: the two callers are the calibration RPC handlers
// and a settings poll, both of which can be abandoned, and the helper supports
// cancelling an operation it is in the middle of.
func (p paneReplay) Replay(ctx context.Context, header agentcapture.Header, chunks []agentcapture.Chunk, through []int) ([]paneview.Frame, error) {
	c, _, err := p.local.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("replay a capture on this machine's helper: %w", err)
	}
	data := make([][]byte, len(chunks))
	for i, ch := range chunks {
		data[i] = []byte(ch.Data)
	}
	frames, err := c.Replay(ctx, header.Cols, header.Rows, data, through)
	if err != nil {
		// The reader's sentence, not the wire's: a capture too large to send
		// in one request is a fact about the FILE, and saying so is the
		// difference between a person knowing to trim it and a person
		// watching a refusal they cannot act on.
		if errors.Is(err, helperclient.ErrRequestTooLarge) {
			return nil, fmt.Errorf("this capture is larger than one replay request can carry: %w", err)
		}
		return nil, err
	}
	return frames, nil
}
