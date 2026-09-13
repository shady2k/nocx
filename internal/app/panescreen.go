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
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
)

// paneScreenTimeout bounds ONE frame read.
//
// It is a REQUEST bound and not a policy, exactly as the helper client's signal
// seam states it: the callers are a 120ms observer sweep and a write gate that
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
// THE END OF THIS REFUSAL IS nocx-ygxjv.13. Today the PTY of a pane with no
// helper is held by the coordinator itself (a direct-channel SSH pane), and
// there is no runtime beside it to read. .13 puts that PTY under THIS MACHINE'S
// helper — the owner's decision of 2026-09-13: every session's PTY or channel is
// owned by a helper, never by the coordinator — at which point the pane resolves
// through the local route like every other and this error stops being reachable.
// The predicate that raises it is owner() below, and .13 deletes it in one
// place.
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
// THE ORDER OF THE TWO ROUTES IS THE TWO FACTS. A local session is this
// machine's daemon's — ADR-0057 refuses a fallback there, so asking anything
// else would be a second route to one pane. Everything else is a helper-hosted
// remote session if the registry holds a host for it, and a pane with NEITHER
// is the case nocx-ygxjv.13 ends.
func (p *paneScreen) owner(ctx context.Context, paneID string) (*helperclient.Client, helperclient.HostSessionID, error) {
	sid := session.ID(paneID)
	sess, err := p.registry.Get(sid)
	if err != nil {
		// The session is gone. It is the same refusal as "no helper holds it",
		// because the answer a caller acts on is the same: there is no screen
		// to read, and there will not be one.
		return nil, helperclient.HostSessionID{}, fmt.Errorf("%w: %s", errNoPaneRuntime, paneID)
	}
	if sess.Kind() == session.KindLocal {
		return p.local.screenClient(ctx, paneID)
	}
	if h, ok := p.remote.hostFor(sid); ok {
		return h.screenClient(ctx, paneID)
	}
	return nil, helperclient.HostSessionID{}, fmt.Errorf("%w: %s", errNoPaneRuntime, paneID)
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

// paneScreens is the app's view of the store (AD-8): the readers in this
// package — the worker screener, the answerer's settle check and the menu
// helpers — may READ a frame and may do nothing else with the interval.
//
// One method and not two: the question "is this pane watched" is asked by the
// authorizer, which declares its own seam for it (workerAuthEnrolments), and a
// reader that had to answer for the interval as well would be a second place
// the interval could be decided.
type paneScreens interface {
	Frame(paneID string) (paneview.Frame, error)
}
