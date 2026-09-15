package app

// TestACoordinatorReadsAnswersAndMessagesItsWorkerThroughTheRealHelper is
// nocx-6q1uh.14's remaining half: the SAME user-level script
// TestACoordinatorReadsAnswersAndMessagesItsWorker (session_surface_happypath_test.go)
// already drives through the real MCP bridge, tool endpoint and the session
// surface's own production wiring — but with the pane's session held by a
// REAL local helper daemon (cmd/nocx-helper, built and installed exactly as
// internal/app/local_pane_test.go's own newLocalPaneApp does), talking the
// real internal/helper/session wire ops (Snapshot/Target/Intent/IntentStatus/
// AccessBump) over a real unix socket to a real PTY, through the real
// libghostty-vt emulator ADR-0066 puts beside it.
//
// # What is real here that session_surface_happypath_test.go's own doc says
// its test does not reach
//
// The helper seam pane_access.go's own doc names as this package's one
// substitution — paneHelpers/paneHelperLookup — is production's own
// implementation: panescreen.go's *paneScreen and helperPaneClient, wrapping
// a real *helperclient.Client dialled to a real daemon this test installs
// and starts. session.New is given NO local pty factory (nil, the same
// composition app.go's own comment insists on: "the registry has no local
// pty factory... a local open reaches the helper, and if it cannot, it
// refuses"), so EVERY session this stand opens — the coordinator's own pane
// included — is a real helper-hosted session, never a backend-forked one.
//
// # What still substitutes for a real Claude, and why that is the epic's own
// licence rather than this task's shortcut
//
// The program named "claude" a worker's real shell finds on PATH is
// internal/app/testdata/s14mock (see its own file doc): it repaints its own
// stdout with agentcapture.Paint of the SAME verified corpus frames
// (claude-idle, claude-permission, claude-working) the fake helper test
// already trusts, so the REAL emulator inside the REAL helper reconstructs
// them faithfully — TestPaintingAndReplayingAFrameDoesNotMoveTheVerdict is
// the existing proof that round trip is sound. It reacts to the real bytes
// production's own session.keys/session.message write to its real stdin
// (plain text is echoed into the input box; a bare Enter confirms a menu or
// clears a pending echo), which is genuinely how a real Claude's own pane
// would look from the OUTSIDE for the two things this test's own script asks
// of it. What it cannot do is decide, the way a real model does, THAT a
// permission question should appear at all — nothing but a real model
// decides that — so a second, file-based cue this program polls plays the
// same explicit, test-driven role session_surface_happypath_test.go's own
// s14FakeHelper.setPhase played for its fake, moved out of process because
// there is no struct left in this process to call a method on.
// worker_orchestration_task_test.go's own doc already settles why substituting
// the MODEL is legitimate where substituting the ORCHESTRATION is not: "the
// agent is the thing being orchestrated, not the orchestration."
//
// # A finding this test surfaces rather than papers over
//
// pane_messages.go's pasteReady/confirmSubmission read the input box's
// emptiness as regionText over the WHOLE TargetInput span — session_targets.go's
// chooseTargetRows names it o.InputBox.First..Last, unmodified, which
// claude.rule.json's own inputBox={anchor:"topRule", to:"bottomRule"} binds
// to the box's own top and bottom RULE rows, not only its content row.
// Replayed off the real corpus (see this file's own reproduction, run by
// hand: a small program built the same way this test's own newS14RealStand
// replays claude-idle at 11000ms and joins regionText over rows 35..37), that
// span is two rows of "─" repeated the full width plus the content row, and
// neither strings.TrimSpace nor strings.TrimPrefix(s, "❯") remove box-drawing
// glyphs — so the trimmed box this check compares to "" is never empty
// against a genuinely free_text- or working-classified real Claude frame.
// If that reading is right, session.message's when="now"/when="free" steps
// below may observe "refused" or hang at their own waittest.WaitFor rather
// than "submitted" — a defect (if it is one) in already-merged production
// code this task did not touch, surfaced only because this test's screen is
// real rather than a fake's own bookkeeping. See this file's own commit
// message and the worker's report for what to check before trusting a red
// run here as this test's fault.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	coordsock "github.com/shady2k/nocx/internal/coordinator"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/procwatch"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
	"github.com/shady2k/nocx/internal/workspace"
)

// ── the mock agent binary ────────────────────────────────────────────────────

// s14RealBuildMock builds internal/app/testdata/s14mock once and returns its
// path — this test's own equivalent of local_pane_test.go's
// realHelperArtifacts, and built for the same reason stated there: it must
// happen BEFORE storagetest.IsolateWithHome moves $HOME, because `go build`
// resolves GOPATH/build cache from it.
func s14RealBuildMock(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	out, err := exec.Command("go", "build", "-o", bin, "./testdata/s14mock").CombinedOutput() //nolint:gosec // this test's own build, fixed arguments
	if err != nil {
		t.Fatalf("building the mock agent (internal/app/testdata/s14mock): %s: %v", out, err)
	}
	return dir
}

// ── the stand: newS14Stand's own composition (session_surface_happypath_test.go),
// with the fake pty factory and fake helper replaced by a real local helper
// daemon and production's own paneScreen/helperPaneClient. Everything this
// does NOT touch — the worker record, the pane enrolment stack, the session
// surface's paneAccessHub/paneReader/paneKeys/paneMessages construction, the
// tool authorizer and endpoint — is copied unchanged from newS14Stand, so a
// difference between the two tests is a difference in the helper seam and
// nothing else. ──────────────────────────────────────────────────────────────

// s14PinChannel is a session.Channel over no real process: newS14RealStand
// Adopts one directly (never through the real local helper, so
// helper_local.go's own RecordOwnedProcessPID call never touches it) purely
// so this test's admission root can be os.Getpid() with nothing to collide
// with — see newS14RealStand's own doc on the session it backs. Nothing
// reads or writes it; Read blocks on Close so the session's own read paths
// see EOF rather than a busy loop, and Close is idempotent because
// reg.Close's own cleanup sweep may reach it after this stand's explicit one
// already has.
type s14PinChannel struct {
	done chan struct{}
}

func (c *s14PinChannel) Read([]byte) (int, error) {
	<-c.done
	return 0, io.EOF
}

func (c *s14PinChannel) Write(p []byte) (int, error) { return len(p), nil }

func (c *s14PinChannel) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return nil
}

func (c *s14PinChannel) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }

func (c *s14PinChannel) Done() <-chan struct{} { return c.done }

type s14RealStand struct {
	reg      *session.Reg
	tp       *transport.WSServer
	grid     *paneviewtest.Views
	store    *workers.MemoryStore
	record   *workers.Registrar
	endpoint *toolendpoint.Endpoint
	local    *localHelperOpener
	stateDir string
	coord    session.Session
}

// newS14RealStand builds the stand over a REAL local helper daemon. Callers
// MUST set S14_CAPTURES_DIR, S14_STATE_DIR and PATH (s14RealSetEnv) before
// calling this: the coordinator's own pane, opened at the end of this
// function, is what lazily starts the daemon (helper_local.go's own doc:
// "it installs and it does not START anything... a daemon is begun by the
// first caller that reaches for the endpoint"), and the daemon's own process
// environment — inherited by every shell it forks, this stand's coordinator
// pane and every worker alike — is fixed at THAT moment, not at whatever
// later moment a worker happens to spawn.
func newS14RealStand(t *testing.T, mockDir, stateDir string) *s14RealStand {
	t.Helper()
	ctx := context.Background()
	slogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	logger := log.NewSlogAdapter(slogger)

	// The helper binary is built BEFORE $HOME moves, for the same GOPATH
	// reason local_pane_test.go's own realHelperArtifacts states.
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	binary := filepath.Join(helperRoot(home, src.hash()), "nocx-helper")
	t.Cleanup(func() { endTheDaemon(t, binary) })

	installed, err := helperlocal.Install(ctx, src, home)
	if err != nil {
		t.Fatalf("installing this machine's helper generation: %v", err)
	}

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

	// NO LOCAL PTY FACTORY (nil): app.go's own composition root comment on
	// this exact call is the reason — every local open reaches the helper or
	// refuses, never a second, backend-forked owner of a pane's terminal.
	reg := session.New(logger, nil)

	local := &localHelperOpener{log: slogger, procs: procwatch.New(logger), spawnTokens: &spawnTokens{}}
	local.routeDir(home)
	local.installedLocalGeneration(installed)
	local.registry = reg
	t.Cleanup(local.close)

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
	// The pty factory drove the channel against the publisher in the fake
	// stand (factory.kernel = pub); the real local opener is the same seam
	// here (app.go's own "localOpener.kernel = lifecyclePub").
	local.kernel = pub

	hosted := &hostedOpeners{local: local} // remote is nil: this test opens no ssh pane
	tp := transport.NewWSServer(logger, reg,
		transport.WithHelperSessionOpener(hosted),
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
		// A real daemon spawn (install verification, a real fork, the real
		// shell-integration handshake over a real lifecycle channel) is
		// slower than newHappyStand's in-process fork; this bound is
		// generous rather than tight on purpose (never the thing this test
		// asserts on — waittest.WaitFor below is what actually waits).
		workers.WithEnrolmentDeadline(30*time.Second),
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

	// The session surface's own production wiring (design §7, §8, Task 8-10)
	// over production's OWN paneHelpers/paneHelperLookup — panescreen.go's
	// *paneScreen and helperPaneClient, talking a real *helperclient.Client
	// to the real daemon `local` connects to. remote is nil: every session
	// this test opens is local, and paneScreen.owner asks the local opener
	// first (see its own doc).
	screen := newPaneScreen(slogger, reg, local, nil)
	hub := newPaneAccessHub(record, screen, systemMonoClock{})
	// See newS14Stand's own note (session_surface_happypath_test.go): a
	// caller names a descendant by workers.spawn's participant id, and
	// Resolve needs enrol's translation to reach its real backend session.
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

	// Opening the coordinator's own pane is what lazily starts the real
	// daemon (see this function's own doc) — a real shell, on the real
	// helper, exactly as local_pane_test.go's own tests already prove for a
	// local pane in general.
	coordOpened, err := tp.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open coordinator session: %v", err)
	}
	coord := coordOpened.Session
	if watchErr := grid.Watch(string(coord.ID()), 80, 24); watchErr != nil {
		t.Fatalf("enrol coordinator pane: %v", watchErr)
	}

	// admittedPeer (worker_auth.go) pins peer.PID against a session's OWN
	// recorded root, and coord.ID() already has one: helper_local.go's own
	// RecordOwnedProcessPID call recorded the real forked shell's pid the
	// instant OpenSession returned above — the correct root for THAT pane's
	// own descendants. RecordOwnedProcessPID refuses a second, DIFFERENT
	// pid for the same session outright (internal/session/session.go), so
	// this stand cannot also record os.Getpid() there: it collided
	// (nocx-6q1uh.18's own failing run — "owned process pid already
	// recorded for session"). The MCP client below dials the tool
	// endpoint's unix socket directly from THIS TEST PROCESS, never from a
	// descendant of that shell, so admittedPeer's walk from peer.PID up the
	// process tree can never reach the shell's pid either way — it needs a
	// root of its own. A session Adopted directly, never handed to the real
	// local helper, is never touched by helper_local.go's own recording
	// call, so it can carry os.Getpid() as an admission root with nothing
	// to collide with — "a separate coordinator session the helper never
	// enrolled". newS14Stand (session_surface_happypath_test.go) reaches
	// this same os.Getpid() root over its own fake PTY factory, which never
	// records a pid at all; this stand has no fake factory to lean on
	// (composed with a nil one, on purpose — see this function's own doc),
	// so it mints this one session for exactly that purpose instead.
	pinCh := &s14PinChannel{done: make(chan struct{})}
	pinSess, err := reg.Adopt(ctx, session.Config{Cols: 80, Rows: 24}, session.ID("s14-real-admission-root"), pinCh)
	if err != nil {
		t.Fatalf("adopt admission-root session: %v", err)
	}
	if recordErr := reg.RecordOwnedProcessPID(pinSess.ID(), os.Getpid()); recordErr != nil {
		t.Fatalf("record admission root: %v", recordErr)
	}
	if watchErr := grid.Watch(string(pinSess.ID()), 80, 24); watchErr != nil {
		t.Fatalf("enrol admission-root pane: %v", watchErr)
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
		grid.Withdraw(string(coord.ID()))
		grid.Withdraw(string(pinSess.ID()))
		_ = tp.Stop(context.Background())
	})

	return &s14RealStand{
		reg: reg, tp: tp, grid: grid, store: store, record: record,
		endpoint: endpoint, local: local, stateDir: stateDir, coord: coord,
	}
}

// ── driving the mock: a phase change is a file write plus a wait on the REAL
// observable it causes (AGENTS.md: never on a duration), because there is no
// in-process struct left to call a synchronous setPhase on. ─────────────────

func (s *s14RealStand) cuePath(sessionID string) string {
	return filepath.Join(s.stateDir, "cue", sessionID)
}

func (s *s14RealStand) logPath(sessionID string) string {
	return filepath.Join(s.stateDir, "log", sessionID)
}

// writtenBytes is how many bytes the mock at sessionID has read from its own
// stdin so far — a real count of what production actually wrote to a real
// PTY (this file's own s14mock, appending every byte it reads to this file
// verbatim), not a bookkeeping field a fake kept of itself.
func (s *s14RealStand) writtenBytes(t *testing.T, sessionID string) int {
	t.Helper()
	info, err := os.Stat(s.logPath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat mock log for %s: %v", sessionID, err)
	}
	return int(info.Size())
}

// setPhaseAndWait writes sessionID's cue file and waits for the classification
// session.read reports to actually become want — the mock's own cue-file poll
// (15ms) and this test's own daemon-hop round trip both take real, variable
// time, so this is a wait on an OBSERVABLE (the real classification a real
// snapshot reports), never a sleep.
func (s *s14RealStand) setPhaseAndWait(t *testing.T, client *s14MCPClient, sessionID, cue, want string) {
	t.Helper()
	if err := os.WriteFile(s.cuePath(sessionID), []byte(cue), 0o600); err != nil { // #nosec G306 -- this test's own state directory
		t.Fatalf("write cue %q for %s: %v", cue, sessionID, err)
	}
	waittest.WaitFor(t, fmt.Sprintf("%s to reach %s", sessionID, want), func() bool {
		read := s14Read(t, client, sessionID)
		return read.Classification == want
	})
}

// s14RealSpawn is s14Spawn's own real-helper cousin: it calls workers.spawn
// and waits for the participant to settle, returning both the caller-visible
// worker id (what session.read/keys/message name a session by) and the
// underlying pty session id workers.spawn caused a real helper to mint —
// which is also exactly the id shellintegration exports as NOCX_SESSION_ID
// into the pane's own shell (AD-7; internal/session/session.go's own doc),
// so it doubles as s14mock's own file discriminator. Unlike s14Spawn there is
// no alias() to reconcile: paneScreen (production) already resolves the
// caller-visible id to this same session id on its own, for real, which is
// the whole point of this test existing beside the fake one.
func s14RealSpawn(t *testing.T, stand *s14RealStand, client *s14MCPClient, command, task string) (workerID, ptySessionID string) {
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
	return spawned.ID, stored.Liveness.SessionID
}

// TestACoordinatorReadsAnswersAndMessagesItsWorkerThroughTheRealHelper is this
// bead's own acceptance check — see the file doc above for what is real here
// beyond session_surface_happypath_test.go's own test, and the finding it
// surfaces about pane_messages.go's own emptiness check.
func TestACoordinatorReadsAnswersAndMessagesItsWorkerThroughTheRealHelper(t *testing.T) {
	mockDir := s14RealBuildMock(t)
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "cue"), 0o750); err != nil {
		t.Fatalf("mkdir cue dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "log"), 0o750); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	capturesDir, err := filepath.Abs(filepath.Join("..", "agentdriver", "testdata", "captures"))
	if err != nil {
		t.Fatalf("resolve captures dir: %v", err)
	}

	// Set BEFORE the stand: the real daemon this stand's coordinator pane
	// lazily starts inherits this process's environment at THAT moment, and
	// every shell it forks afterwards — the coordinator's own pane and every
	// worker alike — inherits the daemon's (see newS14RealStand's own doc).
	t.Setenv("PATH", mockDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("S14_CAPTURES_DIR", capturesDir)
	t.Setenv("S14_STATE_DIR", stateDir)

	stand := newS14RealStand(t, mockDir, stateDir)
	client := newS14MCPClient(t, stand.endpoint.SocketPath())

	// Spawn the worker this test answers, messages and eventually closes,
	// and a second, unrelated worker used only for the "in flight"
	// concurrency assertion below — both real panes on the real daemon,
	// both running the real mock claude on PATH.
	primary, primaryPTY := s14RealSpawn(t, stand, client, "claude", "please continue")
	secondary, _ := s14RealSpawn(t, stand, client, "claude", "please continue")

	// ── session.read gets a menu target, with real options read off a real,
	// verified permission-capture frame the real emulator reconstructed
	// (design §6.3). ─────────────────────────────────────────────────────────
	stand.setPhaseAndWait(t, client, primaryPTY, "menu", string(agentdriver.StatePermissionChoice))
	menuRead := s14ReadTarget(t, client, primary, "menu")
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
	// to the real pty (design §6.2's one-shot target, structural staleness —
	// this time a real digest over a real, changed screen rather than the
	// fake's own tuple compare). The wait below is what makes the race
	// deterministic: production's own re-read inside sendOption must see the
	// real "working" frame, not whatever the mock had not yet repainted. ───
	before := stand.writtenBytes(t, primaryPTY)
	stand.setPhaseAndWait(t, client, primaryPTY, "working", string(agentdriver.StateWorking))
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
	if got := stand.writtenBytes(t, primaryPTY); got != before {
		t.Fatalf("session.keys on a changed menu wrote %d bytes to the real pty, want 0 (before=%d)", got-before, before)
	}

	// ── session.keys option answers the menu for real, off a FRESH read
	// against the menu that is actually still showing, choosing the option
	// already under the cursor so sendOption's own loop (session_keys.go)
	// needs no arrow-key movement — the mock's own file doc explains why
	// this test's flow never asks it to move one. ──────────────────────────
	stand.setPhaseAndWait(t, client, primaryPTY, "menu", string(agentdriver.StatePermissionChoice))
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

	// The menu is answered: production wrote a real Enter to the real pty,
	// the mock (see its own onEnter) hands off to the working frame on its
	// own — a real reaction to a real keystroke rather than a second,
	// test-driven setPhase call. The wait below is on that real transition
	// actually reaching the classifier before this test reads an input
	// target off it.
	var workingRead s14ReadResult
	waittest.WaitFor(t, "primary to reach working with an input target", func() bool {
		workingRead = s14ReadTarget(t, client, primary, "input")
		return workingRead.Classification == string(agentdriver.StateWorking) &&
			workingRead.Target != nil && workingRead.Target.Kind == "input"
	})

	// While the primary's own message call is about to go in flight, prove
	// the endpoint's concurrent dispatch by starting a session.read on the
	// UNRELATED secondary worker first and reading it back only after the
	// primary's call has also been started — both share one MCP connection
	// (design §10's "coordinator concurrent"; brief item 10).
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

	// See this file's own doc, "A finding this test surfaces rather than
	// papers over": the emptiness check the delivery steps below wait on may
	// never be satisfied against a real InputBox span. If this fails here,
	// read pane_messages.go's pasteReady/confirmSubmission before assuming
	// this test (rather than that check) is wrong.
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
	stand.setPhaseAndWait(t, client, primaryPTY, "idle", string(agentdriver.StateFreeText))
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

	// ── a key sent after workers.close of that worker writes zero bytes to
	// the real pty. ────────────────────────────────────────────────────────
	beforeClose := stand.writtenBytes(t, primaryPTY)
	stand.setPhaseAndWait(t, client, primaryPTY, "menu", string(agentdriver.StatePermissionChoice))
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
	if got := stand.writtenBytes(t, primaryPTY); got != beforeClose {
		t.Fatalf("session.keys after workers.close wrote %d bytes to the real pty, want 0", got-beforeClose)
	}
}
