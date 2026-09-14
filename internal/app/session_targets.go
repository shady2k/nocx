package app

// session.read's helper-backed read path for a sessionId naming one of the
// caller's descendants (design §6.1, §6.2, §6.3, Task 8).
//
// Read asks the descendant's helper for a SNAPSHOT, classifies that exact
// frame with the agent rule, and — only when a target kind was asked for —
// mints a target from the SAME snapshotId. Classification, rows, menu
// identity and digest therefore always describe one frame (design §6.1):
// nothing here re-snapshots between classifying and minting, except the
// one retry §6.1 names for an evicted snapshot (snapshot_gone).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// paneAgent answers which agent a pane is enrolled as, from the same cache
// paneobserve.Watcher already keeps at enrolment (AGENTS.md, "look for the
// existing answer before you write a second one") — never a second
// derivation of "what agent runs here". Only .Agent is read: the frame
// session.read classifies is THIS reader's own helper snapshot (design
// §6.1), never Watcher's cached frame.
type paneAgent interface {
	Snapshot(paneID string) (paneobserve.Observation, bool)
}

// targetRecord is the coordinator's own record of a minted target (design
// §6.2): the bound capability it was minted under, the view handed back to
// the caller, the participant's enrolment incarnation and the delegation
// chain generations StillHolds re-checks before a write spends it. Task 9's
// PaneKeys is this record's first consumer; keeping it here means that
// wiring needs no second store keyed by the same tokenId.
type targetRecord struct {
	Access      *DescendantPaneAccess
	View        assistant.TargetView
	Enrolment   workers.Liveness
	Chain       workers.Chain
	AccessEpoch uint64
	SessionID   string
}

// paneReader is assistant.PaneReader's one production implementation.
//
// It reuses hub.lookup (paneAccessHub's own paneHelperLookup) rather than
// taking a second, independent helper lookup: revocation and a read resolve
// the identical question — which helper holds this session's pane — and a
// PaneReader built over a second answer to it would be a second owner of
// that question (AGENTS.md, "two surfaces may never own the same input").
type paneReader struct {
	hub    *paneAccessHub
	agents paneAgent
	rules  *agentdriver.Registry

	mu      sync.Mutex
	records map[string]targetRecord
}

// newPaneReader builds a PaneReader over hub (for both Resolve's registrar
// and the per-session helper lookup revocation already uses), agents (the
// enrolment cache's Agent name, narrowed to paneAgent), and rules (the
// agent rule registry that classifies a frame and extracts its menu/input
// box/menu zone, Task 1).
func newPaneReader(hub *paneAccessHub, agents paneAgent, rules *agentdriver.Registry) *paneReader {
	return &paneReader{hub: hub, agents: agents, rules: rules, records: make(map[string]targetRecord)}
}

var _ assistant.PaneReader = (*paneReader)(nil)

// maxSnapshotRetries is §6.1's "an evicted snapshot refuses snapshot_gone
// and session.read retries once from a fresh snapshot" — one retry, so two
// attempts total.
const maxSnapshotRetries = 2

// Read implements assistant.PaneReader. access is RunContext.PaneAccess,
// forwarded from internal/assistant untouched as `any`; it is asserted
// back to *DescendantPaneAccess here, this package's one point of use for
// it (registry.go's own note on why PaneAccess travels as `any`). A value
// of the wrong type, or nil, is refused exactly as an unbound
// DescendantPaneAccess already is: not reachable, never a panic.
func (p *paneReader) Read(ctx context.Context, access any, sessionID string, want *sessionruntime.TargetKind, rows *sessionruntime.RowRange) (assistant.PaneRead, error) {
	da, ok := access.(*DescendantPaneAccess)
	if !ok || da == nil {
		return assistant.PaneRead{}, workers.ErrNotReachable
	}
	reach, err := da.Resolve(ctx, sessionID, workers.EffectObserve)
	if err != nil {
		return assistant.PaneRead{}, err
	}
	if p.hub == nil || p.hub.lookup == nil {
		return assistant.PaneRead{}, fmt.Errorf("session.read: %w", errNoPaneRuntime)
	}
	helper, ok := p.hub.lookup.HelperFor(ctx, sessionID)
	if !ok {
		return assistant.PaneRead{}, fmt.Errorf("session.read: %w", errNoPaneRuntime)
	}

	// "none" for a pane that is not running a recognized agent at all — a
	// stronger claim than agentdriver.StateUnknown, which means an agent
	// IS running but the rule could not classify this particular frame.
	agent := ""
	if p.agents != nil {
		if o, agentOK := p.agents.Snapshot(sessionID); agentOK {
			agent = o.Agent
		}
	}

	var lastErr error
	for attempt := 0; attempt < maxSnapshotRetries; attempt++ {
		snap, snapErr := helper.Snapshot(ctx, sessionID)
		if snapErr != nil {
			return assistant.PaneRead{}, fmt.Errorf("session.read: %w", snapErr)
		}
		frame := helperclient.FrameFromSnapshot(snap)
		classification := agentdriver.State("none")
		var observation agentdriver.Observation
		if agent != "" && p.rules != nil {
			observation = p.rules.Observe(agent, frame)
			classification = observation.State
		}
		read := assistant.PaneRead{
			Frame:          frame,
			Classification: classification,
			ReadBarrier:    snap.ReadBarrier,
		}
		if want == nil {
			return read, nil
		}
		kind, first, last, menu := chooseTargetRows(*want, observation, rows, frame)
		result, mintErr := helper.Target(ctx, sessionID, proto.TargetParams{
			SnapshotID: snap.SnapshotID, Kind: string(kind), First: first, Last: last,
		})
		if mintErr != nil {
			if isSnapshotGone(mintErr) && attempt+1 < maxSnapshotRetries {
				lastErr = mintErr
				continue
			}
			return assistant.PaneRead{}, fmt.Errorf("session.read: mint target: %w", mintErr)
		}
		view := assistant.TargetView{
			Token:     result.Token,
			TokenID:   result.TokenID,
			Kind:      kind,
			Rows:      sessionruntime.RowRange{First: first, Last: last},
			Menu:      menu,
			ExpiresAt: time.UnixMilli(result.ExpiresAtMs),
		}
		if kind == sessionruntime.TargetRegion {
			view.Region = regionText(frame, first, last)
		}
		p.recordTarget(result.TokenID, targetRecord{
			Access: da, View: view, Enrolment: reach.Participant.Liveness,
			Chain: reach.Chain, AccessEpoch: snap.AccessEpoch, SessionID: sessionID,
		})
		read.Target = &view
		return read, nil
	}
	return assistant.PaneRead{}, fmt.Errorf("session.read: mint target: %w", lastErr)
}

func (p *paneReader) recordTarget(tokenID string, rec targetRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records[tokenID] = rec
}

// Record answers a minted target's own bookkeeping (design §6.2): the
// capability it was bound under, the participant's enrolment incarnation
// and the delegation chain generations, for a caller (Task 9's PaneKeys)
// that must re-check all three before spending the token.
func (p *paneReader) Record(tokenID string) (targetRecord, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.records[tokenID]
	return rec, ok
}

// chooseTargetRows picks the rows a mint should name for want, from the
// same observation session.read just classified the frame with (design
// §6.3): menu is the question through the last option; input is the input
// box, cursor included; working is the menu zone, so a menu appearing
// refuses; region is the caller's own named rows, or the whole screen. A
// kind whose rows this frame does not offer — a menu with no found body
// boundary, an empty input box, an empty menu zone — falls back to a
// region target over the whole screen rather than refusing outright:
// TestAMenuWithoutABodyBoundaryGetsARegionTargetOnly is exactly this case.
func chooseTargetRows(want sessionruntime.TargetKind, o agentdriver.Observation, rows *sessionruntime.RowRange, frame paneview.Frame) (sessionruntime.TargetKind, int, int, *agentdriver.Menu) {
	wholeScreen := func() (int, int) { return 0, len(frame.Lines) - 1 }
	switch want {
	case sessionruntime.TargetMenu:
		if m, ok := o.Menu(); ok {
			menu := m
			return sessionruntime.TargetMenu, m.Rows.First, m.Rows.Last, &menu
		}
	case sessionruntime.TargetInput:
		if o.InputBox.Last >= o.InputBox.First {
			return sessionruntime.TargetInput, o.InputBox.First, o.InputBox.Last, nil
		}
	case sessionruntime.TargetWorking:
		if o.MenuZone.Last >= o.MenuZone.First {
			return sessionruntime.TargetWorking, o.MenuZone.First, o.MenuZone.Last, nil
		}
	case sessionruntime.TargetRegion:
		if rows != nil {
			return sessionruntime.TargetRegion, rows.First, rows.Last, nil
		}
	}
	first, last := wholeScreen()
	return sessionruntime.TargetRegion, first, last, nil
}

// regionText joins a frame's rows first..last (inclusive) into text, the
// same cell-join executeSessionScreen's renderer path already uses —
// blanks kept, cells in column order.
func regionText(f paneview.Frame, first, last int) string {
	if first < 0 {
		first = 0
	}
	if last >= len(f.Lines) {
		last = len(f.Lines) - 1
	}
	if last < first {
		return ""
	}
	lines := make([]string, 0, last-first+1)
	for _, row := range f.Lines[first : last+1] {
		var b strings.Builder
		for _, cell := range row {
			b.WriteString(cell.Text)
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
}

// isSnapshotGone reports whether err is the helper's own snapshot_gone
// refusal (design §6.1) — the one mint failure a read retries, once, from a
// fresh snapshot.
func isSnapshotGone(err error) bool {
	var refusal *helperclient.RefusalError
	return errors.As(err, &refusal) && refusal.Code == "snapshot_gone"
}
