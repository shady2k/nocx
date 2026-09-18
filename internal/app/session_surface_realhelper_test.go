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
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log/logtest"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	coordsock "github.com/shady2k/nocx/internal/coordinator"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/paneview"
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

// s14AdmissionRootSessionID is the fixed id the admission-root session below
// (over an s14PinChannel) is Adopted under. Named once so the two places
// that must agree on it — the Adopt call and s14ExemptScreen's exemption —
// cannot drift apart the way two string literals would.
const s14AdmissionRootSessionID = "s14-real-admission-root"

// s14ExemptScreen answers paneview.Source over the real screen source,
// except for one session id it declares always available with an empty
// frame — nocx-gantk's fix for the admission-root session, which is
// deliberately never opened through the real local helper (see
// s14PinChannel's own doc) and so has no runtime the real source could ever
// find an owner for. It still has to be "watched" (worker_auth.go's
// admission check reads enrolments.Watched for every admitted peer, this
// stand's own root included), which is the one thing this wrapper buys it.
type s14ExemptScreen struct {
	paneview.Source
	exempt string
}

func (s *s14ExemptScreen) Available(paneID string) error {
	if paneID == s.exempt {
		return nil
	}
	return s.Source.Available(paneID)
}

func (s *s14ExemptScreen) Screen(paneID string) (paneview.Frame, error) {
	if paneID == s.exempt {
		return paneview.Frame{}, nil
	}
	return s.Source.Screen(paneID)
}

// s14RealConfig is everything a caller asks of the stand beyond the
// composition session_surface_realhelper_test.go's own check needs.
type s14RealConfig struct {
	// cockpit turns the stand into the one the coordinator journey
	// (nocx-luqz9.7) runs on, and it changes TWO things that are the same
	// fact about who calls the endpoint.
	//
	// Its coordinator is a REAL helper-hosted pane running the mock agent, so
	// the screen it is typed at, the emulator that decides whether typing is
	// safe, and the process that receives the wake line are all real — and the
	// mailbox is that pane's own session.
	//
	// And there is NO ADMISSION-ROOT SESSION: every call in this mode is made
	// by an agent inside a pane (the mock, over the shipped MCP bridge), never
	// by the test process. That is not tidiness. An admission root is a
	// session whose recorded pid is an ancestor of the CALLER, and this test
	// process is the parent of the helper daemon and so of every shell the
	// daemon forks — so a caller inside a pane would sit in TWO sessions'
	// process trees at once, and worker_auth.go refuses an ambiguous peer on
	// purpose ("a peer matching two live enrolled roots has no unambiguous
	// session authority"). Measured, not reasoned: the first run of this
	// journey had the worker's own report refused with "not in a pane nocx has
	// enrolled" for exactly that reason.
	cockpit bool
	// retryPause and attempts are the wake's two injected numbers (design
	// §5.4). They are the injected clock: no assertion in the journey waits
	// for a duration, and these decide only how soon the pause is available
	// to be observed.
	retryPause time.Duration
	attempts   int
}

type s14RealOption func(*s14RealConfig)

// withCockpit turns the stand into the one nocx-luqz9.7's check runs on. The
// two numbers are deliberately small: they compress the product's own two
// minutes into something a test can reach, and nothing asserts on them.
func withCockpit() s14RealOption {
	return func(c *s14RealConfig) {
		c.cockpit = true
		c.retryPause = 2 * time.Second
		c.attempts = s14JourneyAttempts
	}
}

// s14JourneyAttempts is how many lines one batch gets before the human is
// told: two, which is the smallest number that shows BOTH halves the design
// names — a retry at the pause, and the notice once the pause after the last
// line has gone unanswered.
const s14JourneyAttempts = 2

type s14RealStand struct {
	reg      *session.Reg
	tp       *transport.WSServer
	store    *workers.MemoryStore
	record   *workers.Registrar
	endpoint *toolendpoint.Endpoint
	local    *localHelperOpener
	stateDir string
	coord    session.Session
	// db is the real content store: the layout chain the spawner mints a
	// worker's tab in is the same one the close takes it out of, and a journey
	// that asserts the tab left the window reads it here.
	db content.ContentDB
	// binary is the installed helper this stand's daemon runs from, which is
	// also the path an agent's MCP bridge is started with.
	binary string
	// root is the admission-root session's channel, nil in the cockpit mode
	// (that mode adopts no such session — see s14RealConfig.cockpit).
	root *s14PinChannel
	// seams is what the journey reads and drives: the production observation
	// bridge the sweep feeds, the wake whose decisions are under test, the
	// escalation the human would be reached through, and the tab record.
	observe *WorkerObservation
	wake    *workers.Wake
	raiser  *s14Raiser
	seats   *workerTabs
	rules   *agentdriver.Registry
	views   *paneview.Store
	watch   *paneobserve.Watcher
	typist  *agenttyping.Typist
}

// newS14RealStand builds the stand over a REAL local helper daemon. Callers
// MUST set S14_CAPTURES_DIR, S14_STATE_DIR and PATH before calling this — the
// mock agent reads the first two and is found on the third: the coordinator's
// own pane, opened a few lines below, is what lazily starts the daemon
// (helper_local.go's own doc:
// "it installs and it does not START anything... a daemon is begun by the
// first caller that reaches for the endpoint"), and the daemon's own process
// environment — inherited by every shell it forks, this stand's coordinator
// pane and every worker alike — is fixed at THAT moment, not at whatever
// later moment a worker happens to spawn.
func newS14RealStand(t *testing.T, mockDir, stateDir string, opts ...s14RealOption) *s14RealStand {
	t.Helper()
	ctx := context.Background()
	slogger := logtest.Slog(t)
	logger := log.NewSlogAdapter(slogger)

	cfg := s14RealConfig{}
	for _, o := range opts {
		o(&cfg)
	}
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

	// NO LOCAL PTY FACTORY (nil): app.go's own composition root comment on
	// this exact call is the reason — every local open reaches the helper or
	// refuses, never a second, backend-forked owner of a pane's terminal.
	reg := session.New(logger, nil)

	local := &localHelperOpener{log: slogger, procs: procwatch.New(logger), spawnTokens: &spawnTokens{}}
	local.routeDir(home)
	local.installedLocalGeneration(installed)
	local.registry = reg
	t.Cleanup(local.close)

	// THE REAL SCREEN SOURCE (nocx-gantk), not a byte-fed fake: app.go's own
	// composition root builds paneWatch and paneScreens over the SAME pair
	// (newPaneScreen wrapped in paneview.NewStore) for exactly this reason —
	// a helper-hosted pane's content lives in the helper's own emulator
	// (ADR-0066) and is read from it live, never fed in by hand. Before this
	// fix the pane-readiness axis (paneobserve, below) read a
	// paneviewtest.Views that nothing in this file ever called Feed on, so
	// every worker's pane classified as StateUnknown forever and
	// workers.spawn timed out waiting for it to become typable — the whole
	// point of this test using a real helper was defeated by a fake source
	// standing in for the one thing that has to be real to prove it. This
	// file's own doc already claimed paneScreen/helperPaneClient were in use
	// (they are, for session.read/session.keys via `screen` below) but
	// paneobserve and WithPaneScreens were wired to the fake anyway.
	screen := newPaneScreen(slogger, reg, local, nil)
	// THE STORE'S SOURCE, and the two modes differ here for one reason.
	//
	// The COCKPIT journey opens a real helper-hosted pane for its coordinator
	// and exempts nothing: the real source answers for every session this
	// stand opens, which is what makes the coordinator's screen a real one.
	//
	// The other check ADOPTS a synthetic session as its admission root (see
	// s14PinChannel's own doc — the process that dials the endpoint is this
	// test, which is in no pane), and that session has no runtime the real
	// source could ever find an owner for. It still has to be "watched" —
	// worker_auth.go's admission check reads enrolments.Watched for every
	// admitted peer, that stand's own root included — so the real source is
	// wrapped for it and the one synthetic id is answered with an empty frame,
	// which is the honest reading of a session nothing draws into.
	paneViews := paneview.NewStore(logger, screen)
	if !cfg.cockpit {
		paneViews = paneview.NewStore(logger, &s14ExemptScreen{
			Source: screen, exempt: s14AdmissionRootSessionID,
		})
	}

	enrol := newWorkerEnrolments(logger, reg)

	paneDrivers, driversErr := agentdriver.NewRegistry(agentdriver.Claude())
	if driversErr != nil {
		t.Fatalf("pane drivers: %v", driversErr)
	}
	realWatch := paneobserve.New(logger, paneViews, paneDrivers, paneobserve.Config{})

	paneEnrol, err := newPaneEnroller(logger, lanes, paneViews, realWatch, allowPaneApproval{})
	if err != nil {
		t.Fatalf("pane enroller: %v", err)
	}
	paneEnrol = enrol.hookInto(paneEnrol)
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel,
		lifecyclepub.WithAgentEnroller(paneEnrol),
	)
	pub.SetEmitter(happyLifecycleEmitter{})
	// The pty factory drove the channel against the publisher in the fake
	// stand (factory.kernel = pub); the real local opener is the same seam
	// here (app.go's own "localOpener.kernel = lifecyclePub").
	local.kernel = pub
	// THE OTHER HALF OF A LOCAL PANE'S LANE REGISTRATION (nocx-gantk).
	// helper_local.go's OpenHosted calls this for EVERY local-hosted pane
	// whenever it gets a lifecycle lane, not only a nested sudo/su child
	// domain — app.go's own composition root wires it for exactly this
	// reason ("A HELPER-HOSTED LOCAL PANE REGISTERS THE SAME TWO FACTS, by
	// two seams rather than one closure"). Without it `lanes` (the
	// lane→session map paneEnroller.Enrol reads) never learns of ANY
	// pane's lane, local or nested, and every agent_enrol this stand's
	// mock claude sends is refused with "the lane maps to no session" —
	// which is what made workers.spawn time out waiting for an enrolment
	// that could never arrive, on the very first worker this test spawns.
	local.noteChildDomainParent = func(_ lifecycle.TransportID, lane lifecycle.LaneID, sid string) {
		lanes.register(lane, sid)
	}

	hosted := &hostedOpeners{local: local} // remote is nil: this test opens no ssh pane
	tp := transport.NewWSServer(logger, reg,
		transport.WithHelperSessionOpener(hosted),
		transport.WithPaneScreens(paneViews),
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

	// Opening the coordinator's own pane is what lazily starts the real
	// daemon (see this function's own doc) — a real shell, on the real
	// helper, exactly as local_pane_test.go's own tests already prove for a
	// local pane in general.
	//
	// THE COCKPIT OPENS IT AT THE PARTICIPANT GEOMETRY and puts the mock in
	// it, because in that mode this pane is a coordinator: the frames a pane
	// is classified from are 120x40 everywhere else in this stand
	// (participantCols/Rows), and a pane of another size would wrap them
	// differently and be classified as something else.
	spec := transport.OpenSpec{Cols: 80, Rows: 24}
	if cfg.cockpit {
		spec.Cols, spec.Rows = participantCols, participantRows
	}
	coordOpened, err := tp.OpenSession(ctx, spec)
	if err != nil {
		t.Fatalf("open coordinator session: %v", err)
	}
	coord := coordOpened.Session
	if watchErr := paneViews.Enrol(string(coord.ID())); watchErr != nil {
		t.Fatalf("enrol coordinator pane: %v", watchErr)
	}
	if cfg.cockpit {
		// `command claude` and not `claude`: the shell integration wraps that
		// one name in a function that enrols the agent for orchestration, and
		// this pane must NOT be in the observation sweep's set (the readings
		// this journey asserts on are attributed one at a time — see
		// s14RealStand.readCoordinator). The wrapper is what would put it
		// there, and `command` is how a shell says "the program, not the
		// function".
		if !coord.EnqueueWrite([]byte("command claude\n")) {
			t.Fatalf("the coordinator's pane refused its own first line")
		}
		waittest.WaitFor(t, "the coordinator's pane to come up showing the mock's idle screen", func() bool {
			f, ferr := paneViews.Frame(string(coord.ID()))
			return ferr == nil && paneDrivers.Classify(wakeAgent, f) == agentdriver.StateFreeText
		})
	}

	store := workers.NewMemoryStore()
	sup := &workerSupervisor{sessions: reg, log: logger}
	// seats is app.go's own tab record (`workerSeats` there, `newWorkerTabs`
	// here): the participant→tab pairing the spawner writes and the closer
	// takes from, which is what makes a close able to end a worker AND take
	// its tab out of the window (nocx-xn63t.4.6).
	seats := newWorkerTabs()
	spawner := &workerSpawner{
		layout: db.Layout(), opener: tp, sessions: reg, enrolments: enrol,
		workspace: string(workspace.Default), log: logger,
		readiness: realWatch,
		// announce and tabs are the product's own, exactly as app.go wires
		// them: without them a spawn mints a tab nothing records and a close
		// has nothing to take out of the window.
		announce: tp, tabs: seats,
	}
	// The wake and its two injected numbers. A stand that is not running the
	// coordinator journey gets the record's own default (an unmounted wake:
	// nothing to type with and nobody to tell), which is the same absence the
	// record's constructor installs.
	raiser := &s14Raiser{}
	wakeOpts := []workers.WakeOption{}
	// typing is the shipped Typist the wake's waker submits through, and nil
	// for a stand with no coordinator — the same absence the product's own
	// waker reports when a backend has no way to type into a pane.
	var typing *agenttyping.Typist
	recordOpts := []workers.Option{
		// A real daemon spawn (install verification, a real fork, the real
		// shell-integration handshake over a real lifecycle channel) is
		// slower than newHappyStand's in-process fork; this bound is
		// generous rather than tight on purpose (never the thing this test
		// asserts on — waittest.WaitFor below is what actually waits).
		workers.WithEnrolmentDeadline(30 * time.Second),
		workers.WithLogger(logger),
		workers.WithCloser(&workerCloser{sessions: reg, layout: db.Layout(), tabs: seats, announce: tp, log: logger}),
	}
	if cfg.cockpit {
		// THE COORDINATOR'S OWN TYPING SEAM. It is the shipped primitive
		// built from the shipped pieces — the same frames, the same rule, the
		// same calibration verdict that permits typing
		// (agentcalib.Verdict.MayType, unwritable outside its own package, so
		// a stand that wanted to skip the calibration would be faking the
		// gate it exists to exercise), and the same input queue every
		// keystroke travels. The one substitution is the enrolment seam:
		// paneTypist's own `paneAgents{watch}` answers from the sweep's watch
		// list, and the coordinator's pane is deliberately not in it (see the
		// note on OnReading above), so this answers for that one pane id and
		// delegates every other pane to the shipped implementation. What it
		// answers is not a shortcut: the coordinator's pane really is enrolled
		// under the claude rule, and the frame a decision is taken on is read
		// from the real emulator through the real store.
		typing = agenttyping.New(logger, paneViews, paneDrivers, verifiedClaude(t),
			s14CoordinatorAgents{base: paneAgents{watch: realWatch}, coord: string(coord.ID()), agent: wakeAgent},
			paneInput{registry: reg})
		wakeOpts = append(wakeOpts,
			workers.WithRetryPause(cfg.retryPause),
			workers.WithAttemptLimit(cfg.attempts))
		recordOpts = append(recordOpts,
			// ZERO, and a configuration rather than a mistake (see
			// worker_wake_test.go's own note): it means "the second reading
			// of a state is a settled one", which is what a stand at this
			// level needs, because it cannot move the record's clock. The
			// journey's settle-window probe is written against exactly that
			// rule — the FIRST reading admits nothing.
			workers.WithSettleWindow(0))
	}
	wake := workers.NewWake(&workerWaker{typist: typing, log: logger}, &workerEscalation{raise: raiser}, store, wakeOpts...)
	recordOpts = append(recordOpts, workers.WithWake(wake))
	record := workers.NewRegistrar(store, spawner, enrol, sup, recordOpts...)
	sup.exited = func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, e workers.Exit) {
		_, _ = record.Exited(ctx, id, l, e)
	}

	// THE OBSERVATION BRIDGE, as app.go binds it (nocx-luqz9.2): the sweep's
	// second reader maps a pane's classification onto the record's own
	// vocabulary and hands it on, and the coordinator's own pane goes to
	// ObserveCoordinator. It is wired only for the coordinator journey: the
	// other check asserts the session surface, and a bridge nothing there
	// reads would be wiring for a test that does not exist.
	var obs *WorkerObservation
	if cfg.cockpit {
		obs = &WorkerObservation{
			enrolments: enrol,
			observe: func(ctx context.Context, id workers.ParticipantID, l workers.Liveness, s workers.ObservedState) error {
				return record.Observe(ctx, id, l, s)
			},
			coordinate: func(sessionID string, s workers.ObservedState) {
				record.ObserveCoordinator(context.Background(), sessionID, s)
			},
			liveness: enrol.livenessOf,
			log:      logger,
		}
		// The WORKER panes' readings arrive from the real sweep, over the
		// real helper's own emulator — that is the half of this journey that
		// only the real stand can prove. The coordinator's own pane is NOT in
		// the watcher's set (see the stand's own note where it is enrolled),
		// because the settle rule is what the journey asserts and a reading
		// on a 20 ms ticker cannot be attributed to the first or the second
		// one.
		realWatch.OnReading(obs.ObserveSession)
	}

	// The session surface's own production wiring (design §7, §8, Task 8-10)
	// over production's OWN paneHelpers/paneHelperLookup — panescreen.go's
	// *paneScreen and helperPaneClient, talking a real *helperclient.Client
	// to the real daemon `local` connects to. remote is nil: every session
	// this test opens is local, and paneScreen.owner asks the local opener
	// first (see its own doc). The SAME screen built above feeds both this
	// hub and paneobserve/WithPaneScreens — one real source, not two.
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
	auth, err := newToolAuthorizer(peerpin.SystemPinner{}, reg, paneViews, record, workerTestWorkspace, allowWorkerApproval{})
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
		Logger:   logtest.Slog(t),
		Auth:     auth,
		Dispatch: dispatcher,
	})
	if err != nil {
		t.Fatalf("new endpoint: %v", err)
	}
	if startErr := endpoint.Start(); startErr != nil {
		t.Fatalf("start endpoint: %v", startErr)
	}

	// THE ADMISSION ROOT, which the cockpit does not open at all (see
	// s14RealConfig.cockpit for the measured reason).
	//
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
	var pinCh *s14PinChannel
	var pinSess session.Session
	if !cfg.cockpit {
		pinCh = &s14PinChannel{done: make(chan struct{})}
		pinSess, err = reg.Adopt(ctx, session.Config{Cols: 80, Rows: 24}, session.ID(s14AdmissionRootSessionID), pinCh)
		if err != nil {
			t.Fatalf("adopt admission-root session: %v", err)
		}
		if recordErr := reg.RecordOwnedProcessPID(pinSess.ID(), os.Getpid()); recordErr != nil {
			t.Fatalf("record admission root: %v", recordErr)
		}
		if watchErr := paneViews.Enrol(string(pinSess.ID())); watchErr != nil {
			t.Fatalf("enrol admission-root pane: %v", watchErr)
		}
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
		paneViews.Withdraw(string(coord.ID()))
		if pinSess != nil {
			paneViews.Withdraw(string(pinSess.ID()))
		}
		_ = tp.Stop(context.Background())
	})

	return &s14RealStand{
		reg: reg, tp: tp, store: store, record: record,
		endpoint: endpoint, local: local, stateDir: stateDir, coord: coord,
		db: db, binary: binary, root: pinCh,
		observe: obs, wake: wake, raiser: raiser, seats: seats,
		rules: paneDrivers, views: paneViews, watch: realWatch, typist: typing,
	}
}

// s14Raiser is the far end of the escalation for this stand: the notify seam
// and not the whole pipeline. What an escalation may REACH is internal/notify's
// and is asserted there, against a routing table this stand does not own (the
// same substitution worker_wake_test.go's own recordingRaiser makes, and for
// the same reason).
//
// IT IS LOCKED, unlike that one, because the notice this journey waits for is
// raised from the wake's OWN ALARM goroutine — the pause elapsing on the
// product's clock, not a call the test makes — so an append here and a read
// there really are two goroutines racing.
type s14Raiser struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *s14Raiser) Raise(_ context.Context, ev notify.Event) notify.Outcome {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	return notify.Outcome{Event: ev}
}

// raised is every notice so far, in order.
func (r *s14Raiser) raised() []notify.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Event(nil), r.events...)
}

// s14CoordinatorAgents is paneTypist's own enrolment seam with one answer
// added: the coordinator's pane, which is not in the sweep's watch set (see
// the stand's note on OnReading) and would otherwise be answered "not
// watched" — and a pane nocx is not watching receives nothing at all, which
// is the typing gate's own rule rather than a gap here.
type s14CoordinatorAgents struct {
	base  paneAgents
	coord string
	agent string
}

func (a s14CoordinatorAgents) AgentOn(paneID string) (string, bool) {
	if paneID == a.coord {
		return a.agent, true
	}
	return a.base.AgentOn(paneID)
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
		Phase        string `json:"phase"`
		BoxContents  string `json:"boxContents"`
		BytesWritten int    `json:"bytesWritten"`
	}
	s14DecodeInto(t, nowResp.result, &nowResult)
	if nowResult.Phase != "submitted" {
		// The record's own reading of what the box held, and how much of the
		// paste landed, are what tell a `partial` here apart from a `partial`
		// whose echo WAS confirmed — the difference between a box the
		// delivery could not read and a step after the echo that did not
		// execute (nocx-xn63t.4.13's own two shapes).
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
