package app

// TestACoordinatorReadsAnswersAndMessagesItsWorker is Task 14's end-to-end
// check (design §1, §13's "Happy path"): through the REAL MCP bridge
// (internal/mcpstdio), the REAL tool endpoint (internal/toolendpoint), the
// REAL dispatch/authorizer layer (internal/assistant, worker_auth.go) and the
// REAL session-surface production code this epic built — paneAccessHub,
// paneReader, paneKeys, paneMessages (pane_access.go, session_targets.go,
// session_keys.go, pane_messages.go) — a coordinator spawns a worker, reads
// its screen, answers its permission menu, messages it during and after a
// turn, and is refused when a target has gone stale or the worker has closed.
//
// # What is real, and what is this test's own
//
// Real: the MCP bridge (a live mcpstdio.Server driven over pipes, exactly the
// protocol translation cmd/nocx-helper's own "mcp" subcommand runs), the
// tool endpoint over a real unix socket, the coordinator's admission and its
// bound DescendantPaneAccess, workers.spawn's real enrolment through a real
// bash PTY (newHappyStand's own happyRealPTYFactory, the same one
// worker_orchestration_task_test.go's TestACoordinatorSpawnsAWorkerAndTypesItsTask
// already trusts for this), and the whole of paneAccessHub/paneReader/
// paneKeys/paneMessages: target minting, one-shot spending, structural
// staleness, the message queue, the paste/echo/Enter delivery sequence and
// its phase set.
//
// This test's own: the "helper" seam pane_access.go's own doc says a real
// implementation needs — the wire ops a live daemon would answer
// (session.snapshot/target/intent). newHappyStand has never stood one up
// (nocx-6q1uh.9's pane_messages_test.go says so explicitly: "a real helper
// runtime and PTY were not this task's time budget either"), and building
// the real internal/helper/session wire stack from this package is not
// reachable without exporting internals that package deliberately keeps
// private. s14FakeHelper is that seam: it answers Snapshot/Target/Intent
// against the SAME verified corpus frames every other worker test in this
// package trusts (internal/agentdriver/testdata/captures, replayed exactly
// as newHappyVerifiedClaudeCalibration and verify_corpus_test.go do), and it
// is a plain, explicit state machine — the test itself calls setPhase
// between the calls that provoke a real Claude to move — rather than a
// second, reactive fake agent process. What the fake owns is content and
// one-shot bookkeeping a real helper would derive from a live PTY; what it
// hands to the code under test is otherwise identical to what a real helper
// would answer, which is what lets the acceptance below exercise the real
// paneAccessHub/paneReader/paneKeys/paneMessages rather than a description
// of them.
//
// The spawned worker's OWN pane still runs a real bash PTY under a real
// program on PATH (a fake "claude" replaying a real idle capture, exactly
// TestACoordinatorSpawnsAWorkerAndTypesItsTask's technique) — so enrolment,
// the pane grid and workers.spawn's own task-typing gate are exercised for
// real; only the content session.read/keys/message VALIDATE against is the
// fake helper's.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	coordsock "github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/mcpstdio"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
	"github.com/shady2k/nocx/internal/workspace"
)

// ── the fake helper: the one seam a real daemon would own (see file doc) ───

// s14Phase is which of the corpus's own verified frames s14FakeHelper answers
// Snapshot with for one worker session, and it is driven EXPLICITLY by the
// test between calls — there is no reactive program behind these frames.
// What the mock "claude" process on the worker's own PATH does is unrelated
// to this: it only has to enrol and show a free_text screen for
// workers.spawn's own task-typing gate (worker_orchestration_task_test.go's
// own technique), which is a claim this test does not repeat.
type s14Phase int

const (
	s14Idle s14Phase = iota
	s14Menu
	s14Working
)

// s14Session is one worker's state in the fake helper: which frame it is on,
// and — while a session.message delivery is mid-flight — the text nocx's own
// paste intent most recently wrote, overlaid onto the input box the next
// Snapshot reports (design §8.2 step 2's echo).
type s14Session struct {
	phase s14Phase
	echo  string
	// written is bytes this fake has committed to this pane via Intent,
	// tracked on the session object itself (never by the id string a
	// particular caller happened to use) because alias() may point two
	// different id spellings at one *s14Session, and the "zero bytes"
	// assertions below must read the SAME counter regardless of which
	// spelling production code used to reach Intent.
	written int
}

// s14Target is what the fake helper remembers about one minted, unspent
// target: which session it was minted for and what the session looked like
// at that moment (phase + pending echo). Intent's whole "did the screen
// change" check is comparing this against the session's CURRENT phase/echo
// at spend time — a coarser structural comparison than the real digest
// (design §6.2), but the same question, and the one this test's refusal
// assertions are about.
type s14Target struct {
	sessionID string
	phase     s14Phase
	echo      string
}

// s14FakeHelper is this test's paneHelpers AND paneHelperLookup
// (internal/app/pane_access.go): see the file doc for what is real either
// side of it. One instance serves every worker session this test spawns,
// keyed by whatever session id string production code hands it — which
// pane_access.go's own doc says is workers.Reach.SessionID, resolved fresh
// by the registrar from the caller-visible worker id session.read/keys/
// message actually take. Both id spellings a test could plausibly hold are
// aliased onto the same *s14Session by alias(), so the test can drive
// setPhase from the id workers.spawn returned without caring which one the
// production code underneath happens to look up by.
type s14FakeHelper struct {
	t      *testing.T
	claude *agentdriver.Registry

	mu       sync.Mutex
	sessions map[string]*s14Session
	frames   map[s14Phase]paneview.Frame

	snapSeq uint64
	snapAt  map[uint64]s14Target

	tokSeq  uint64
	targets map[string]s14Target

	epoch map[string]uint64
}

func newS14FakeHelper(t *testing.T, claude *agentdriver.Registry) *s14FakeHelper {
	t.Helper()
	h := &s14FakeHelper{
		t:        t,
		claude:   claude,
		sessions: make(map[string]*s14Session),
		frames:   make(map[s14Phase]paneview.Frame),
		snapAt:   make(map[uint64]s14Target),
		targets:  make(map[string]s14Target),
		epoch:    make(map[string]uint64),
	}
	// The exact captures and marks newHappyVerifiedClaudeCalibration already
	// verifies the shipped claude rule against (worker_happypath_test.go),
	// so this introduces no new claim about the corpus.
	h.frames[s14Idle] = happyReplayCapture(t, "claude-idle", 11000)
	h.frames[s14Menu] = happyReplayCapture(t, "claude-permission", 49000)
	h.frames[s14Working] = happyReplayCapture(t, "claude-working", 17000)
	return h
}

// alias makes both id spellings a test can plausibly hold for one worker
// resolve to the same *s14Session, so setPhase(callerID, ...) reaches the
// session that Snapshot(reachID, ...) is actually asked about, whichever of
// the two production code uses to look this helper up by.
func (h *s14FakeHelper) alias(a, b string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sa, oka := h.sessions[a]
	sb, okb := h.sessions[b]
	switch {
	case oka && okb && sa != sb:
		// Two independent sessions already exist under the two names; keep
		// a's and repoint b's id at it.
		h.sessions[b] = sa
	case oka:
		h.sessions[b] = sa
	case okb:
		h.sessions[a] = sb
	default:
		s := &s14Session{phase: s14Idle}
		h.sessions[a] = s
		h.sessions[b] = s
	}
}

func (h *s14FakeHelper) sessionLocked(id string) *s14Session {
	s, ok := h.sessions[id]
	if !ok {
		s = &s14Session{phase: s14Idle}
		h.sessions[id] = s
	}
	return s
}

// setPhase is the test's own advance of a worker's screen — see the type
// doc's "no reactive program" note.
func (h *s14FakeHelper) setPhase(id string, phase s14Phase) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessionLocked(id)
	s.phase = phase
	s.echo = ""
}

func (h *s14FakeHelper) writtenBytes(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionLocked(id).written
}

// HelperFor implements paneHelperLookup: this fake answers for every
// session id it is asked about, exactly as a real per-machine daemon would
// for every pane it holds.
func (h *s14FakeHelper) HelperFor(context.Context, string) (paneHelpers, bool) {
	return h, true
}

func (h *s14FakeHelper) Snapshot(_ context.Context, sessionID string) (proto.SnapshotResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessionLocked(sessionID)
	frame := h.frames[s.phase]
	if s.echo != "" {
		frame = s14OverlayInputBox(h.claude, frame, s.echo)
	}
	h.snapSeq++
	id := h.snapSeq
	h.snapAt[id] = s14Target{sessionID: sessionID, phase: s.phase, echo: s.echo}
	return proto.SnapshotResult{
		SnapshotID:   id,
		Frame:        s14ToScreenFrame(frame),
		Revision:     id,
		InputFence:   id,
		Completeness: proto.CompletenessComplete,
		AccessEpoch:  h.epoch[sessionID],
		ReadBarrier:  true,
	}, nil
}

func (h *s14FakeHelper) Target(_ context.Context, sessionID string, p proto.TargetParams) (proto.TargetResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	at, ok := h.snapAt[p.SnapshotID]
	if !ok || at.sessionID != sessionID {
		return proto.TargetResult{}, fmt.Errorf("s14FakeHelper: unknown snapshot %d for %q", p.SnapshotID, sessionID)
	}
	h.tokSeq++
	tok := fmt.Sprintf("s14-tok-%d", h.tokSeq)
	h.targets[tok] = at
	return proto.TargetResult{
		Token:       tok,
		TokenID:     tok,
		ExpiresAtMs: time.Now().Add(time.Minute).UnixMilli(),
	}, nil
}

func (h *s14FakeHelper) Intent(_ context.Context, sessionID string, p proto.IntentParams) (proto.IntentResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, ok := h.targets[p.Token]
	if !ok {
		return proto.IntentResult{State: "refused", Refusal: &proto.IntentRefusal{Cause: "token_spent"}}, nil
	}
	// One-shot, whether it is honoured or refused: a spent or a refused
	// token never authorises a second attempt (design §6.2).
	delete(h.targets, p.Token)
	cur := h.sessionLocked(sessionID)
	if rec.sessionID != sessionID || rec.phase != cur.phase || rec.echo != cur.echo {
		return proto.IntentResult{State: "refused", Refusal: &proto.IntentRefusal{Cause: "stale_target"}}, nil
	}
	switch p.Kind {
	case "text":
		cur.echo = string(p.Payload)
	case "key":
		// Enter (or any other key this test sends) closes whatever the
		// pane was showing in its box, the same as a real repaint would
		// once a menu confirms or a message's Enter lands (design §8.2
		// step 4: "box cleared, or working, or the queued indicator" —
		// clearing here satisfies the first of those for every case this
		// test drives).
		cur.echo = ""
	}
	cur.written += len(p.Payload)
	return proto.IntentResult{State: "executed", BytesWritten: len(p.Payload)}, nil
}

func (h *s14FakeHelper) IntentStatus(context.Context, string, string) (proto.IntentStatusResult, error) {
	return proto.IntentStatusResult{State: "unknown"}, nil
}

func (h *s14FakeHelper) AccessBump(_ context.Context, sessionID string, above uint64) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.epoch[sessionID] <= above {
		h.epoch[sessionID] = above + 1
	}
	return h.epoch[sessionID], nil
}

// s14ToScreenFrame carries a replayed paneview.Frame onto the wire shape
// paneHelpers answers with — the same conversion a real helper's own
// session.snapshot response performs over its live emulator frame.
func s14ToScreenFrame(f paneview.Frame) proto.ScreenFrame {
	lines := make([][]proto.ScreenCell, len(f.Lines))
	for i, row := range f.Lines {
		cells := make([]proto.ScreenCell, len(row))
		for j, c := range row {
			cells[j] = proto.ScreenCell{Text: c.Text, Width: c.Width}
		}
		lines[i] = cells
	}
	return proto.ScreenFrame{
		Cols: f.Cols, Rows: f.Rows,
		CursorX: f.CursorX, CursorY: f.CursorY,
		CursorVisible: f.CursorVisible, AltScreen: f.AltScreen,
		Lines: lines,
	}
}

// s14OverlayInputBox returns a copy of frame whose input-box row shows text
// right after the agent's own prompt marker — the shape a real repaint takes
// once nocx pastes into the box (design §8.2 step 2's echo). It touches
// nothing a classifier reads: only the one row Document.InputBox names, so
// the copy's classification is exactly frame's.
func s14OverlayInputBox(reg *agentdriver.Registry, frame paneview.Frame, text string) paneview.Frame {
	obs := reg.Observe("claude", frame)
	if obs.InputBox.Last < obs.InputBox.First {
		return frame
	}
	out := frame
	out.Lines = append([][]paneview.Cell(nil), frame.Lines...)
	for row := obs.InputBox.First; row <= obs.InputBox.Last && row < len(out.Lines); row++ {
		src := out.Lines[row]
		marker := -1
		for i, c := range src {
			if c.Text == "❯" { // ❯, the prompt marker claude.rule.json's "prompt" anchor requires
				marker = i
				break
			}
		}
		if marker < 0 {
			continue
		}
		dst := append([]paneview.Cell(nil), src...)
		col := marker + 2 // right after "❯ "
		for _, r := range text {
			if col >= len(dst) {
				break
			}
			dst[col] = paneview.Cell{Text: string(r), Width: 1}
			col++
		}
		out.Lines[row] = dst
		return out // exactly one row is the box's own content row
	}
	return out
}

// ── the stand: newHappyStand's own composition, extended with the session
// surface's production wiring app.go itself builds (pane_access.go's own
// doc names the lines: accessHub, descendantPaneReader/Keys/Messages, the
// four Bind* calls) — which newHappyStand has never had a reason to build,
// since no test before this one drove session.read/keys/message through it.
// This is new code in THIS file; newHappyStand itself is untouched. ─────────

type s14Stand struct {
	reg      *session.Reg
	tp       *transport.WSServer
	grid     *paneviewtest.Views
	store    *workers.MemoryStore
	record   *workers.Registrar
	endpoint *toolendpoint.Endpoint
	helper   *s14FakeHelper
	coord    session.Session
}

// newS14Stand is newHappyStand's own composition (worker_happypath_test.go),
// always with the real observation/typing stack it calls
// withHappyStandRealTyping for, extended with the session surface's
// production wiring: a paneAccessHub, a paneReader/paneKeys/paneMessages
// over s14FakeHelper (the one seam a real daemon would own — see the file
// doc), and the four Bind* calls app.go's own composition root makes
// (pane_access.go's doc names the lines). Everything above that line is
// copied from newHappyStand's own body rather than reused from it, because
// newHappyStand builds and starts its endpoint before returning and never
// exposes the *toolAuthorizer it bound — there is no seam on the returned
// *happyStand to bind a pane access hub onto after the fact.
func newS14Stand(t *testing.T) *s14Stand {
	t.Helper()
	ctx := context.Background()
	logger := log.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil)))

	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path: filepath.Join(dir, "content.db"), Key: key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	lanes := newSessionRegistry()
	grid := paneviewtest.NewViews(logger)
	factory := &happyRealPTYFactory{log: logger, lanes: lanes, lanesBySession: make(map[string]lifecycle.LaneID), views: grid}
	reg := session.New(logger, factory)
	enrol := newWorkerEnrolments(logger, reg)

	paneDrivers, driversErr := agentdriver.NewRegistry(agentdriver.Claude())
	if driversErr != nil {
		t.Fatalf("pane drivers: %v", driversErr)
	}
	realWatch := paneobserve.New(logger, grid.Store, paneDrivers, paneobserve.Config{})

	paneEnrol, err := newPaneEnroller(logger, lanes, grid.Store, realWatch, allowPaneApproval{})
	if err != nil {
		t.Fatalf("pane enroller: %v", err)
	}
	paneEnrol = enrol.hookInto(paneEnrol)
	report := &workerReporter{lanes: lanes, enrol: enrol, log: logger, now: time.Now}
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel,
		lifecyclepub.WithAgentEnroller(paneEnrol),
		lifecyclepub.WithAgentReporter(report),
	)
	pub.SetEmitter(happyLifecycleEmitter{})
	factory.kernel = pub
	opener := &happyRealHelperOpener{reg: reg, factory: factory, views: grid}
	tp := transport.NewWSServer(logger, reg,
		transport.WithHelperSessionOpener(opener),
		transport.WithPaneScreens(grid.Store),
		transport.WithPaneObserver(realWatch),
	)
	realWatch.SetEmitter(tp.EmitPaneObservation)
	sweepCtx, cancelSweep := context.WithCancel(context.Background())
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sweepCtx.Done():
				return
			case <-ticker.C:
				realWatch.Sweep()
			}
		}
	}()
	t.Cleanup(func() { cancelSweep(); <-sweepDone })

	store := workers.NewMemoryStore()
	sup := &workerSupervisor{sessions: reg, log: logger}
	spawner := &workerSpawner{
		layout: db.Layout(), opener: tp, sessions: reg, enrolments: enrol,
		workspace: string(workspace.Default), log: logger,
		readiness: realWatch,
	}
	record := workers.NewRegistrar(store, spawner, enrol, sup,
		workers.WithEnrolmentDeadline(20*time.Second),
		workers.WithLogger(logger),
		workers.WithCloser(&workerCloser{sessions: reg, log: logger}),
	)
	report.declare = func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, d workers.Declaration) error {
		_, declareErr := record.Declared(ctx, id, l, d)
		return declareErr
	}
	sup.exited = func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, e workers.Exit) {
		_, _ = record.Exited(ctx, id, l, e)
	}

	// The session surface's own production wiring (design §7, §8, Task 8-10),
	// over s14FakeHelper rather than a real helper daemon connection — see
	// the file doc for exactly what that substitutes and what it does not.
	helper := newS14FakeHelper(t, paneDrivers)
	hub := newPaneAccessHub(record, helper, systemMonoClock{})
	// The real composition root's own wiring (app.go): a caller names a
	// descendant by workers.spawn's participant id, never its real backend
	// session, so Resolve needs enrol's own translation to find it — exactly
	// why s14Spawn aliases the fake helper onto BOTH spellings (this file's
	// doc on s14FakeHelper), because production code resolves through this
	// binding rather than ever seeing the participant id again downstream.
	hub.BindParticipants(enrol)
	reader := newPaneReader(hub, realWatch, paneDrivers)
	keysImpl := newPaneKeys(reader, hub)
	messagesImpl := newPaneMessages(keysImpl, reader, hub, paneDrivers, time.Now())
	reader.SetMessages(messagesImpl)
	record.SetTaskQueue(messagesImpl)

	registry := toolRegistry(t)
	auth, err := newToolAuthorizer(peerpin.SystemPinner{}, reg, grid, record, workerTestWorkspace, allowWorkerApproval{})
	if err != nil {
		t.Fatalf("tool authorizer: %v", err)
	}
	auth.BindPaneAccess(hub)
	auth.BindSessionReads(reader)
	auth.BindSessionKeys(keysImpl)
	auth.BindSessionMessages(messagesImpl)

	dispatcher, err := assistant.NewToolDispatcher(registry, record, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	dispatcher, err = assistant.NewAttemptRecordingDispatcher(registry, dispatcher, db.Ledger())
	if err != nil {
		t.Fatalf("wrap dispatcher with ledger: %v", err)
	}
	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      filepath.Join(shortWorkerSocketDir(t), "runtime"),
		Peers:    coordsock.SystemPeerCredentials{},
		SelfUID:  uint32(os.Getuid()), //nolint:gosec // uid is not a signed quantity
		Owner:    happyEndpointOwner{},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Auth:     auth,
		Dispatch: dispatcher,
	})
	if err != nil {
		t.Fatalf("new endpoint: %v", err)
	}
	if startErr := endpoint.Start(); startErr != nil {
		t.Fatalf("start endpoint: %v", startErr)
	}

	coordOpened, err := tp.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open coordinator session: %v", err)
	}
	coord := coordOpened.Session
	if err := reg.RecordOwnedProcessPID(coord.ID(), os.Getpid()); err != nil {
		t.Fatalf("record coordinator root: %v", err)
	}
	if err := grid.Watch(string(coord.ID()), 80, 24); err != nil {
		t.Fatalf("enrol coordinator pane: %v", err)
	}

	t.Cleanup(func() {
		_ = endpoint.Close()
		for _, sess := range reg.List() {
			_ = reg.Close(sess.ID())
		}
		waittest.WaitFor(t, "worker record to settle", func() bool {
			open, err := store.NonTerminal(context.Background(), workers.ID(coord.ID()))
			return err == nil && len(open) == 0
		})
		factory.closeAdapters()
		grid.Withdraw(string(coord.ID()))
		_ = tp.Stop(context.Background())
	})

	return &s14Stand{reg: reg, tp: tp, grid: grid, store: store, record: record, endpoint: endpoint, helper: helper, coord: coord}
}

// s14Spawn calls workers.spawn over client and waits for the participant to
// reach live, returning its caller-visible worker id (the id session.read/
// session.keys/session.message name it by) and aliasing that id in stand's
// fake helper onto the participant's own underlying pty session id — see
// s14FakeHelper's own doc for why both are needed.
func s14Spawn(t *testing.T, stand *s14Stand, client *s14MCPClient, command, task string) string {
	t.Helper()
	result := client.call(t, "workers.spawn", map[string]any{"command": command, "task": task})
	var spawned struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(result, &spawned); err != nil {
		t.Fatalf("decode workers.spawn result %s: %v", result, err)
	}
	if spawned.ID == "" || spawned.State != string(workers.StateLive) {
		t.Fatalf("workers.spawn = %s, want a live participant id", result)
	}
	var stored workers.Participant
	waittest.WaitFor(t, "spawned worker to settle in the record", func() bool {
		var err error
		stored, err = stand.store.Participant(context.Background(), workers.ParticipantID(spawned.ID))
		return err == nil && stored.Liveness.SessionID != ""
	})
	stand.helper.alias(spawned.ID, stored.Liveness.SessionID)
	return spawned.ID
}

// s14FakeClaude writes a program named "claude" that a worker's shell finds
// on PATH: it replays a real, verified idle capture (the same one
// worker_orchestration_task_test.go's TestACoordinatorSpawnsAWorkerAndTypesItsTask
// trusts for this) so workers.spawn's own task-typing gate — which refuses to
// type into anything but a positively identified free_text screen — succeeds,
// and then swallows everything else. What session.read/keys/message actually
// validate against is s14FakeHelper's, not this pane's real screen (see the
// file doc); this program's only job is to let the worker reach live.
func s14FakeClaude(t *testing.T, dir string) {
	t.Helper()
	idleFile := filepath.Join(dir, "idle.bin")
	if err := os.WriteFile(idleFile, happyIdleCaptureBytes(t), 0o600); err != nil {
		t.Fatalf("write idle capture: %v", err)
	}
	fakeClaude := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"stty raw -echo 2>/dev/null || true\n" +
		"cat \"" + idleFile + "\"\n" +
		"exec cat >/dev/null\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o700); err != nil { //nolint:gosec // the test launcher must be executable
		t.Fatalf("write fake claude: %v", err)
	}
}

// ── the MCP client: a minimal, concurrent-capable driver of a live
// mcpstdio.Server over pipes — this is "the real MCP bridge" the task names,
// not a raw dial of the tool endpoint socket. Every request this test sends
// is a real "tools/call" MCP message; the bridge translates it to the
// endpoint's own JSON-RPC and back, exactly as cmd/nocx-helper's "mcp"
// subcommand does for a real coordinator (mcpstdio.Serve). ─────────────────

type s14MCPResult struct{ raw []byte }

type s14MCPClient struct {
	writer io.WriteCloser

	mu      sync.Mutex
	seq     int64
	pending map[string]chan s14MCPResult
}

// newS14MCPClient starts a real mcpstdio.Server bound to socket, driven over
// in-process pipes, and completes the MCP handshake before returning — the
// same "one connection for the life of the session" the bridge's own doc
// describes (internal/mcpstdio/mcpstdio.go), so every call below shares one
// admission interval and the endpoint's own concurrent dispatch is what the
// "in flight" assertion exercises.
func newS14MCPClient(t *testing.T, socket string) *s14MCPClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv, err := mcpstdio.New(mcpstdio.Config{Socket: socket})
	if err != nil {
		t.Fatalf("mcpstdio.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, inR, outW) }()

	c := &s14MCPClient{writer: inW, pending: make(map[string]chan s14MCPResult)}
	go c.readLoop(outR)
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		<-done
	})

	c.mu.Lock()
	c.seq++
	id := fmt.Sprintf("m%d", c.seq)
	initCh := make(chan s14MCPResult, 1)
	c.pending[id] = initCh
	c.mu.Unlock()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-11-25"},
	})
	if err != nil {
		t.Fatalf("marshal initialize: %v", err)
	}
	if _, err := c.writer.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	select {
	case <-initCh:
	case <-time.After(10 * time.Second):
		t.Fatal("initialize timed out")
	}
	return c
}

func (c *s14MCPClient) readLoop(r io.Reader) {
	reader := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			c.deliver(line)
		}
		if err != nil {
			return
		}
	}
}

func (c *s14MCPClient) deliver(line []byte) {
	var head struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(line, &head) != nil {
		return
	}
	var idStr string
	if json.Unmarshal(head.ID, &idStr) != nil {
		var n json.Number
		if json.Unmarshal(head.ID, &n) == nil {
			idStr = n.String()
		}
	}
	c.mu.Lock()
	ch, ok := c.pending[idStr]
	if ok {
		delete(c.pending, idStr)
	}
	c.mu.Unlock()
	if ok {
		ch <- s14MCPResult{raw: append([]byte(nil), line...)}
	}
}

// startCall sends one "tools/call" request and returns the channel its
// response will arrive on — sending, not waiting, is the whole point: the
// "in flight" assertion starts one call, starts a SECOND before reading the
// first's answer, and only then waits on either.
func (c *s14MCPClient) startCall(t *testing.T, method string, args any) chan s14MCPResult {
	t.Helper()
	c.mu.Lock()
	c.seq++
	id := fmt.Sprintf("m%d", c.seq)
	ch := make(chan s14MCPResult, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": method, "arguments": args},
	})
	if err != nil {
		t.Fatalf("marshal tools/call %s: %v", method, err)
	}
	if _, err := c.writer.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write tools/call %s: %v", method, err)
	}
	return ch
}

// toolResponse is one tools/call answer, decoded far enough for this test's
// own assertions: the structured JSON-RPC result the endpoint actually
// returned (present on every legitimate business outcome, refusals
// included — a refusal is a normal, structured session.keys/session.message
// result, design §8.3/§6.5), or the text of a genuine MCP-level failure
// (bad params, an unreachable session, the endpoint itself erroring).
type toolResponse struct {
	result json.RawMessage
	failed string
}

func s14Await(t *testing.T, ch chan s14MCPResult) toolResponse {
	t.Helper()
	select {
	case res := <-ch:
		var env struct {
			Result *struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError           bool            `json:"isError"`
				StructuredContent json.RawMessage `json:"structuredContent"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
				Data    struct {
					Reason string `json:"reason"`
				} `json:"data"`
			} `json:"error"`
		}
		if err := json.Unmarshal(res.raw, &env); err != nil {
			t.Fatalf("decode tool response %s: %v", res.raw, err)
		}
		if env.Error != nil {
			reason := env.Error.Message
			if env.Error.Data.Reason != "" {
				reason = env.Error.Data.Reason
			}
			return toolResponse{failed: reason}
		}
		if env.Result == nil {
			t.Fatalf("tool response carries neither result nor error: %s", res.raw)
		}
		if len(env.Result.StructuredContent) > 0 {
			return toolResponse{result: env.Result.StructuredContent}
		}
		if env.Result.IsError && len(env.Result.Content) > 0 {
			return toolResponse{failed: env.Result.Content[0].Text}
		}
		if len(env.Result.Content) > 0 {
			return toolResponse{result: json.RawMessage(env.Result.Content[0].Text)}
		}
		t.Fatalf("tool response carries no content: %s", res.raw)
		return toolResponse{}
	case <-time.After(30 * time.Second):
		t.Fatal("tool call timed out")
		return toolResponse{}
	}
}

// call is startCall+s14Await for the ordinary, sequential case, and it
// fails the test on a genuine MCP-level failure — every call this test
// makes is expected to be well-formed, so that would be this test's own bug
// rather than a business refusal to assert on.
func (c *s14MCPClient) call(t *testing.T, method string, args any) json.RawMessage {
	t.Helper()
	resp := s14Await(t, c.startCall(t, method, args))
	if resp.failed != "" {
		t.Fatalf("%s: %s", method, resp.failed)
	}
	return resp.result
}

func s14DecodeInto(t *testing.T, raw json.RawMessage, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

// s14ReadTarget calls session.read against sessionID for the given target
// kind and returns the whole result, decoded.
func s14ReadTarget(t *testing.T, client *s14MCPClient, sessionID, target string) s14ReadResult {
	t.Helper()
	raw := client.call(t, "session.read", map[string]any{"sessionId": sessionID, "target": target})
	var out s14ReadResult
	s14DecodeInto(t, raw, &out)
	return out
}

// s14ReadResult is session.read's result, narrowed to the fields this test
// asserts on (contracts/tools/session.read.schema.json is the full shape).
type s14ReadResult struct {
	SessionID      string `json:"sessionId"`
	Classification string `json:"classification"`
	Target         *struct {
		TokenID string `json:"tokenId"`
		Kind    string `json:"kind"`
		Menu    *struct {
			Question string   `json:"question"`
			Options  []string `json:"options"`
			Selected int      `json:"selected"`
		} `json:"menu"`
	} `json:"target"`
	Pending []struct {
		ID    string `json:"id"`
		Phase string `json:"phase"`
	} `json:"pendingMessages"`
}

// s14Read calls session.read without minting a target — a plain screen/
// pendingMessages read.
func s14Read(t *testing.T, client *s14MCPClient, sessionID string) s14ReadResult {
	t.Helper()
	raw := client.call(t, "session.read", map[string]any{"sessionId": sessionID})
	var out s14ReadResult
	s14DecodeInto(t, raw, &out)
	return out
}

// TestACoordinatorReadsAnswersAndMessagesItsWorker is Task 14's own
// acceptance check — see the file doc above for what it exercises for real
// and what its own fake helper stands in for.
func TestACoordinatorReadsAnswersAndMessagesItsWorker(t *testing.T) {
	stand := newS14Stand(t)

	fakeDir := t.TempDir()
	s14FakeClaude(t, fakeDir)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	client := newS14MCPClient(t, stand.endpoint.SocketPath())

	// Spawn the worker this test answers, messages and eventually closes,
	// and a second, unrelated worker used only for the "in flight"
	// concurrency assertion below.
	primary := s14Spawn(t, stand, client, "claude", "please continue")
	secondary := s14Spawn(t, stand, client, "claude", "please continue")

	// ── session.read gets a menu target, with real options from the
	// verified permission capture (design §6.3). ───────────────────────────
	stand.helper.setPhase(primary, s14Menu)
	menuRead := s14ReadTarget(t, client, primary, "menu")
	if menuRead.Classification != string(agentdriver.StatePermissionChoice) {
		t.Fatalf("session.read classification = %q, want %q", menuRead.Classification, agentdriver.StatePermissionChoice)
	}
	if menuRead.Target == nil || menuRead.Target.Kind != "menu" || menuRead.Target.Menu == nil || len(menuRead.Target.Menu.Options) == 0 {
		t.Fatalf("session.read target = %+v, want a menu target with options", menuRead.Target)
	}
	menu := *menuRead.Target.Menu
	if menu.Selected < 0 || menu.Selected >= len(menu.Options) {
		t.Fatalf("menu.selected = %d, want an index into %v", menu.Selected, menu.Options)
	}
	staleToken := menuRead.Target.TokenID
	chosen := menu.Options[menu.Selected]

	// ── a key under a menu target that has since changed writes zero bytes
	// (design §6.2's one-shot target, structural staleness). The screen
	// moves on from underneath the token before it is spent. ──────────────
	before := stand.helper.writtenBytes(primary)
	stand.helper.setPhase(primary, s14Working)
	staleResp := s14Await(t, client.startCall(t, "session.keys", map[string]any{
		"sessionId": primary, "tokenId": staleToken, "option": chosen,
	}))
	if staleResp.failed == "" {
		var keysResult struct {
			State string `json:"state"`
		}
		s14DecodeInto(t, staleResp.result, &keysResult)
		if keysResult.State == "executed" {
			t.Fatalf("session.keys on a changed menu = %+v, want a refusal", keysResult)
		}
	}
	if got := stand.helper.writtenBytes(primary); got != before {
		t.Fatalf("session.keys on a changed menu wrote %d bytes, want 0 (before=%d)", got-before, before)
	}

	// ── session.keys option answers the menu for real, off a FRESH read
	// against the menu that is actually still showing. ─────────────────────
	stand.helper.setPhase(primary, s14Menu)
	menuRead = s14ReadTarget(t, client, primary, "menu")
	if menuRead.Target == nil || menuRead.Target.Menu == nil {
		t.Fatalf("second session.read did not mint a menu target: %+v", menuRead)
	}
	menu = *menuRead.Target.Menu
	answer := client.call(t, "session.keys", map[string]any{
		"sessionId": primary, "tokenId": menuRead.Target.TokenID, "option": menu.Options[menu.Selected],
	})
	var answerResult struct {
		State string `json:"state"`
	}
	s14DecodeInto(t, answer, &answerResult)
	if answerResult.State != "executed" {
		t.Fatalf("session.keys option = %+v, want executed", answerResult)
	}

	// The menu is answered; the pane moves into its turn.
	stand.helper.setPhase(primary, s14Working)

	// While the primary's own message call is about to go in flight, prove
	// the endpoint's concurrent dispatch by starting a session.read on the
	// UNRELATED secondary worker first and reading it back only after the
	// primary's call has also been started — both share one MCP connection
	// (design §10's "coordinator concurrent"; brief item 10).
	workingRead := s14ReadTarget(t, client, primary, "input")
	if workingRead.Target == nil || workingRead.Target.Kind != "input" {
		t.Fatalf("session.read target=input while working = %+v, want an input target", workingRead.Target)
	}
	nowCall := client.startCall(t, "session.message", map[string]any{
		"sessionId": primary, "text": "keep going", "when": "now",
		"id": "msg-now-1", "tokenId": workingRead.Target.TokenID,
	})
	inFlightRead := client.startCall(t, "session.read", map[string]any{"sessionId": secondary})
	inFlightResp := s14Await(t, inFlightRead)
	if inFlightResp.failed != "" {
		t.Fatalf("session.read on the other worker while a call is in flight: %s", inFlightResp.failed)
	}
	var secondaryRead s14ReadResult
	s14DecodeInto(t, inFlightResp.result, &secondaryRead)
	if secondaryRead.SessionID != secondary {
		t.Fatalf("in-flight session.read answered about %q, want %q", secondaryRead.SessionID, secondary)
	}

	nowResp := s14Await(t, nowCall)
	if nowResp.failed != "" {
		t.Fatalf("session.message when=now during a turn: %s", nowResp.failed)
	}
	var nowResult struct {
		Phase string `json:"phase"`
	}
	s14DecodeInto(t, nowResp.result, &nowResult)
	if nowResult.Phase != "submitted" {
		t.Fatalf("session.message when=now during a turn = %+v, want submitted", nowResult)
	}

	// ── session.message when=free after the turn, queued and delivered. ────
	stand.helper.setPhase(primary, s14Idle)
	freeResult := client.call(t, "session.message", map[string]any{
		"sessionId": primary, "text": "and once more", "when": "free", "id": "msg-free-1",
	})
	var freeQueued struct {
		Phase string `json:"phase"`
	}
	s14DecodeInto(t, freeResult, &freeQueued)
	if freeQueued.Phase != "queued" {
		t.Fatalf("session.message when=free = %+v, want queued immediately", freeQueued)
	}
	waittest.WaitFor(t, "the free-delivered message to reach submitted", func() bool {
		read := s14Read(t, client, primary)
		for _, pending := range read.Pending {
			if pending.ID == "msg-free-1" {
				return pending.Phase == "submitted"
			}
		}
		return false
	})

	// ── a key sent after workers.close of that worker writes zero bytes. ──
	beforeClose := stand.helper.writtenBytes(primary)
	stand.helper.setPhase(primary, s14Menu)
	closeRead := s14ReadTarget(t, client, primary, "menu")
	client.call(t, "workers.close", map[string]any{"worker": primary})
	if closeRead.Target != nil {
		closedResp := s14Await(t, client.startCall(t, "session.keys", map[string]any{
			"sessionId": primary, "tokenId": closeRead.Target.TokenID, "key": "Enter",
		}))
		if closedResp.failed == "" {
			var closedResult struct {
				State string `json:"state"`
			}
			s14DecodeInto(t, closedResp.result, &closedResult)
			if closedResult.State == "executed" {
				t.Fatalf("session.keys after workers.close = %+v, want a refusal", closedResult)
			}
		}
	}
	if got := stand.helper.writtenBytes(primary); got != beforeClose {
		t.Fatalf("session.keys after workers.close wrote %d bytes, want 0", got-beforeClose)
	}
}
