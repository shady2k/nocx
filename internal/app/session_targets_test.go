package app

// Independent tests for PaneReader (design §6.1, §6.2, §6.3, Task 8):
// session.read's helper-backed read path for a sessionId naming one of the
// caller's descendants. These exercise the seam this package owns —
// Resolve, the per-session helper lookup, classification and target
// minting — with fakes; the wire-level round trip (schema conformance, the
// renderer never being called for a descendant) is
// internal/assistant/session_test.go and internal/toolendpoint/contract_test.go's.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// fakeAgentDriverObserver is an agentdriver.Observer a test drives
// directly, ignoring the frame it is handed: PaneReader's OWN contract
// (which snapshot's frame classification and minting read) is what these
// tests check, not the real Claude rule's extraction (agentdriver's own
// tests and the manifest cover that, Task 1).
type fakeAgentDriverObserver struct {
	agent       string
	observation agentdriver.Observation
}

func (d *fakeAgentDriverObserver) Agent() string { return d.agent }

func (d *fakeAgentDriverObserver) Classify(paneview.Frame) agentdriver.State {
	return d.observation.State
}

func (d *fakeAgentDriverObserver) Observe(paneview.Frame) agentdriver.Observation {
	return d.observation
}

// fakePaneAgentSource is a paneAgent a test drives directly: which agent
// (if any) a sessionId is enrolled as, from a fixed table — the same
// question paneobserve.Watcher.Snapshot answers from its own cache.
type fakePaneAgentSource struct {
	agents map[string]string
}

func (f *fakePaneAgentSource) Snapshot(paneID string) (paneobserve.Observation, bool) {
	agent, ok := f.agents[paneID]
	if !ok {
		return paneobserve.Observation{}, false
	}
	return paneobserve.Observation{Agent: agent}, true
}

// fakePaneReaderHelper is a paneHelpers a test drives directly: a fixed
// sequence of snapshots (consumed in order, so a test can model "the
// screen changed between reads"), and a configurable Target answer or
// error.
type fakePaneReaderHelper struct {
	snapshots []proto.SnapshotResult
	snapCalls int
	snapErr   error

	targetCalls  []proto.TargetParams
	targetErr    error
	targetResult proto.TargetResult
}

func (h *fakePaneReaderHelper) Snapshot(context.Context, string) (proto.SnapshotResult, error) {
	if h.snapErr != nil {
		return proto.SnapshotResult{}, h.snapErr
	}
	idx := h.snapCalls
	if idx >= len(h.snapshots) {
		idx = len(h.snapshots) - 1
	}
	h.snapCalls++
	return h.snapshots[idx], nil
}

func (h *fakePaneReaderHelper) Target(_ context.Context, _ string, p proto.TargetParams) (proto.TargetResult, error) {
	h.targetCalls = append(h.targetCalls, p)
	if h.targetErr != nil {
		return proto.TargetResult{}, h.targetErr
	}
	return h.targetResult, nil
}

func (h *fakePaneReaderHelper) AccessBump(context.Context, string, uint64) (uint64, error) {
	return 0, errors.New("fakePaneReaderHelper: AccessBump not configured")
}

// testDescendant registers one participant under controller "sess-C" and
// returns its session id, a hub bound to a lookup that answers helper for
// it, and the access Resolve is exercised through.
func testDescendant(t *testing.T, helper paneHelpers) (sessionID string, hub *paneAccessHub, access *DescendantPaneAccess) {
	t.Helper()
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	w, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-C", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register descendant: %v", err)
	}
	sessionID = string(w.Participant.ID)
	lookup := &fakeLookup{helpers: map[string]paneHelpers{}}
	if helper != nil {
		lookup.helpers[sessionID] = helper
	}
	hub = newPaneAccessHub(registrar, lookup, nil)
	access = hub.Bind("sess-C", session.Identity{InstanceID: "backend-A", Epoch: 1}, EndpointAuthority{AdmissionEpoch: 1})
	return sessionID, hub, access
}

func frameOf(rows ...string) proto.ScreenFrame {
	lines := make([][]proto.ScreenCell, len(rows))
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	for i, r := range rows {
		cells := make([]proto.ScreenCell, 0, len(r))
		for _, c := range r {
			cells = append(cells, proto.ScreenCell{Text: string(c), Width: 1})
		}
		lines[i] = cells
	}
	return proto.ScreenFrame{Cols: cols, Rows: len(rows), Lines: lines}
}

// A read classifies and mints from ONE snapshot (design §6.1): the helper
// fake's screen changes between the moment Snapshot is answered and any
// LATER read the fake could serve, but PaneReader never asks again before
// minting — Target is asked to mint from the FIRST snapshot's own id, the
// one classification actually read, never a fresher one.
func TestAReadClassifiesAndMintsFromOneSnapshot(t *testing.T) {
	helper := &fakePaneReaderHelper{
		snapshots: []proto.SnapshotResult{
			{SnapshotID: 100, Frame: frameOf("first frame"), Completeness: proto.CompletenessComplete, ReadBarrier: true, AccessEpoch: 1},
			{SnapshotID: 200, Frame: frameOf("second frame, after it changed"), Completeness: proto.CompletenessComplete, ReadBarrier: true, AccessEpoch: 1},
		},
		targetResult: proto.TargetResult{Token: "tok", TokenID: "tid", ExpiresAtMs: 1000},
	}
	sessionID, hub, access := testDescendant(t, helper)
	driver := &fakeAgentDriverObserver{agent: "claude", observation: agentdriver.Observation{State: agentdriver.StateWorking}}
	registry, err := agentdriver.NewRegistry(driver)
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	agents := &fakePaneAgentSource{agents: map[string]string{sessionID: "claude"}}
	reader := newPaneReader(hub, agents, registry)

	region := sessionruntime.TargetRegion
	read, err := reader.Read(context.Background(), access, sessionID, &region, nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Classification != agentdriver.StateWorking {
		t.Fatalf("classification = %q, want working (from the FIRST snapshot)", read.Classification)
	}
	if helper.snapCalls != 1 {
		t.Fatalf("Snapshot calls = %d, want exactly 1", helper.snapCalls)
	}
	if len(helper.targetCalls) != 1 || helper.targetCalls[0].SnapshotID != 100 {
		t.Fatalf("target calls = %+v, want one call naming snapshotId 100 (the one classified)", helper.targetCalls)
	}
	if read.Target == nil || read.Target.TokenID != "tid" {
		t.Fatalf("target = %+v, want the minted token", read.Target)
	}
}

// A menu target requested where the rule found no body boundary (Menu()
// returns ok=false, design §6.3) falls back to a region target over the
// whole screen — it is minted, not refused.
func TestAMenuWithoutABodyBoundaryGetsARegionTargetOnly(t *testing.T) {
	helper := &fakePaneReaderHelper{
		snapshots:    []proto.SnapshotResult{{SnapshotID: 1, Frame: frameOf("a", "b", "c"), Completeness: proto.CompletenessComplete}},
		targetResult: proto.TargetResult{Token: "tok", TokenID: "tid"},
	}
	sessionID, hub, access := testDescendant(t, helper)
	// permission_choice with no menu extras at all: Menu() returns false
	// because len(above) == 0 (agentdriver.go's own documented answer for
	// "a permission_choice frame whose cursor row has no question above
	// it").
	driver := &fakeAgentDriverObserver{agent: "claude", observation: agentdriver.Observation{State: agentdriver.StatePermissionChoice}}
	registry, err := agentdriver.NewRegistry(driver)
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	agents := &fakePaneAgentSource{agents: map[string]string{sessionID: "claude"}}
	reader := newPaneReader(hub, agents, registry)

	menu := sessionruntime.TargetMenu
	read, err := reader.Read(context.Background(), access, sessionID, &menu, nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Target == nil {
		t.Fatal("target = nil, want a region target minted as the fallback")
	}
	if read.Target.Kind != sessionruntime.TargetRegion {
		t.Fatalf("target kind = %q, want region (menu asked for, no body boundary found)", read.Target.Kind)
	}
	if len(helper.targetCalls) != 1 || helper.targetCalls[0].Kind != string(sessionruntime.TargetRegion) {
		t.Fatalf("target call = %+v, want kind region", helper.targetCalls)
	}
	if helper.targetCalls[0].First != 0 || helper.targetCalls[0].Last != 2 {
		t.Fatalf("target rows = %d..%d, want the whole 3-row frame (0..2)", helper.targetCalls[0].First, helper.targetCalls[0].Last)
	}
}

// A session with no participant record at all — a plain shell — is never
// reachable, paired with the same descendant reading successfully.
func TestAPlainShellIsNotReachable(t *testing.T) {
	helper := &fakePaneReaderHelper{
		snapshots:    []proto.SnapshotResult{{SnapshotID: 1, Frame: frameOf("hi")}},
		targetResult: proto.TargetResult{Token: "tok", TokenID: "tid"},
	}
	sessionID, hub, access := testDescendant(t, helper)
	agents := &fakePaneAgentSource{}
	registry, err := agentdriver.NewRegistry()
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	reader := newPaneReader(hub, agents, registry)

	if _, err := reader.Read(context.Background(), access, "a-plain-shell-nobody-registered", nil, nil); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("read a plain shell: err = %v, want ErrNotReachable", err)
	}
	// Paired ordinary success: the actual descendant reads fine.
	if _, err := reader.Read(context.Background(), access, sessionID, nil, nil); err != nil {
		t.Fatalf("read the real descendant: %v", err)
	}
}

// A session belonging to a DIFFERENT controller's subtree is not
// reachable through this access, paired with the same descendant reading
// successfully through its OWN access.
func TestANonDescendantIsNotReachable(t *testing.T) {
	registrar, _ := newGroupTwoCallersRecord()
	ctx := context.Background()
	wA, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-A", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register under A: %v", err)
	}
	wB, err := registrar.Register(ctx, workers.RegisterRequest{
		CoordinatorSession: "sess-B", Role: workers.RoleWorker,
		Task: "t", Command: "agent", Environment: "env-local",
	})
	if err != nil {
		t.Fatalf("register under B: %v", err)
	}
	helperA := &fakePaneReaderHelper{snapshots: []proto.SnapshotResult{{SnapshotID: 1, Frame: frameOf("hi from A's descendant")}}}
	helperB := &fakePaneReaderHelper{snapshots: []proto.SnapshotResult{{SnapshotID: 1, Frame: frameOf("hi from B's descendant")}}}
	lookup := &fakeLookup{helpers: map[string]paneHelpers{
		string(wA.Participant.ID): helperA,
		string(wB.Participant.ID): helperB,
	}}
	hub := newPaneAccessHub(registrar, lookup, nil)
	accessA := hub.Bind("sess-A", session.Identity{}, EndpointAuthority{AdmissionEpoch: 1})
	accessB := hub.Bind("sess-B", session.Identity{}, EndpointAuthority{AdmissionEpoch: 2})
	agents := &fakePaneAgentSource{}
	registry, err := agentdriver.NewRegistry()
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	reader := newPaneReader(hub, agents, registry)

	if _, err := reader.Read(context.Background(), accessA, string(wB.Participant.ID), nil, nil); !errors.Is(err, workers.ErrNotReachable) {
		t.Fatalf("A read B's descendant: err = %v, want ErrNotReachable", err)
	}
	// Paired ordinary success: each access still reads its OWN descendant
	// fine — the refusal above is about the PAIR (access, sessionID), not
	// a fact that sticks to either side of it.
	if _, err := reader.Read(context.Background(), accessA, string(wA.Participant.ID), nil, nil); err != nil {
		t.Fatalf("A read its own descendant: %v", err)
	}
	if _, err := reader.Read(context.Background(), accessB, string(wB.Participant.ID), nil, nil); err != nil {
		t.Fatalf("B read its own descendant: %v", err)
	}
}

// A snapshot the runtime cannot claim completeness for is reported
// honestly on the frame it produces — never silently upgraded to
// "complete" and never turned into a refusal outright (design §6.1's
// completeness is a fact about the STREAM, orthogonal to whether a read
// can still classify what IS on screen).
func TestASnapshotWithUnknownCompletenessIsReportedHonestly(t *testing.T) {
	helper := &fakePaneReaderHelper{
		snapshots: []proto.SnapshotResult{{SnapshotID: 1, Frame: frameOf("hi"), Completeness: proto.CompletenessUnknown}},
	}
	sessionID, hub, access := testDescendant(t, helper)
	agents := &fakePaneAgentSource{agents: map[string]string{sessionID: "claude"}}
	driver := &fakeAgentDriverObserver{agent: "claude", observation: agentdriver.Observation{State: agentdriver.StateFreeText}}
	registry, err := agentdriver.NewRegistry(driver)
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	reader := newPaneReader(hub, agents, registry)

	read, err := reader.Read(context.Background(), access, sessionID, nil, nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Frame.Completeness != sessionruntime.CompletenessUnknown {
		t.Fatalf("frame completeness = %v, want unknown, reported honestly rather than upgraded", read.Frame.Completeness)
	}
}

// No helper holds the session's terminal at all (a helper down, or one
// that never registered this pane) — a read answers an error rather than
// a fabricated frame.
func TestAHelperDownAnswersFrameUnavailable(t *testing.T) {
	sessionID, hub, access := testDescendant(t, nil) // no helper registered for it
	agents := &fakePaneAgentSource{}
	registry, err := agentdriver.NewRegistry()
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	reader := newPaneReader(hub, agents, registry)

	if _, err := reader.Read(context.Background(), access, sessionID, nil, nil); err == nil {
		t.Fatal("read with no helper holding the pane succeeded, want an error")
	}
}

// An agent IS enrolled but the rule cannot classify THIS frame: the read
// answers agentdriver.StateUnknown, distinct from "none" (no recognized
// agent at all) — a driver that classifies nothing for a frame it was
// asked about is exactly StateUnknown by the package's own closed set.
func TestAnAgentThatCannotClassifyThisFrameAnswersUnknown(t *testing.T) {
	helper := &fakePaneReaderHelper{
		snapshots: []proto.SnapshotResult{{SnapshotID: 1, Frame: frameOf("hi")}},
	}
	sessionID, hub, access := testDescendant(t, helper)
	agents := &fakePaneAgentSource{agents: map[string]string{sessionID: "claude"}}
	driver := &fakeAgentDriverObserver{agent: "claude", observation: agentdriver.Observation{State: agentdriver.StateUnknown}}
	registry, err := agentdriver.NewRegistry(driver)
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	reader := newPaneReader(hub, agents, registry)

	read, err := reader.Read(context.Background(), access, sessionID, nil, nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Classification != agentdriver.StateUnknown {
		t.Fatalf("classification = %q, want unknown", read.Classification)
	}
}

// A descendant that runs no recognized agent at all (nothing enrolled it)
// reports "none" — a stronger, distinct claim from StateUnknown.
func TestANonAgentDescendantClassifiesAsNone(t *testing.T) {
	helper := &fakePaneReaderHelper{
		snapshots: []proto.SnapshotResult{{SnapshotID: 1, Frame: frameOf("$ ")}},
	}
	sessionID, hub, access := testDescendant(t, helper)
	agents := &fakePaneAgentSource{} // nothing enrolled
	registry, err := agentdriver.NewRegistry()
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	reader := newPaneReader(hub, agents, registry)

	read, err := reader.Read(context.Background(), access, sessionID, nil, nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Classification != "none" {
		t.Fatalf("classification = %q, want none", read.Classification)
	}
}

// A snapshot_gone target refusal is retried once from a fresh snapshot
// (design §6.1); a second snapshot_gone in a row is reported as the
// mint failure it is, not retried forever.
func TestATargetMintRetriesOnceOnSnapshotGoneThenGivesUp(t *testing.T) {
	helper := &fakePaneReaderHelper{
		snapshots: []proto.SnapshotResult{
			{SnapshotID: 1, Frame: frameOf("a")},
			{SnapshotID: 2, Frame: frameOf("b")},
		},
		targetErr: &helperclient.RefusalError{Code: "snapshot_gone"},
	}
	sessionID, hub, access := testDescendant(t, helper)
	agents := &fakePaneAgentSource{agents: map[string]string{sessionID: "claude"}}
	driver := &fakeAgentDriverObserver{agent: "claude", observation: agentdriver.Observation{State: agentdriver.StateFreeText}}
	registry, err := agentdriver.NewRegistry(driver)
	if err != nil {
		t.Fatalf("agent registry: %v", err)
	}
	reader := newPaneReader(hub, agents, registry)

	region := sessionruntime.TargetRegion
	_, err = reader.Read(context.Background(), access, sessionID, &region, nil)
	if err == nil {
		t.Fatal("read succeeded despite every mint attempt refusing snapshot_gone")
	}
	if helper.snapCalls != 2 {
		t.Fatalf("Snapshot calls = %d, want exactly 2 (the retry, and no more)", helper.snapCalls)
	}
	if len(helper.targetCalls) != 2 {
		t.Fatalf("Target calls = %d, want exactly 2 (the retry, and no more)", len(helper.targetCalls))
	}
}
