package app

// The race nocx-ui8q6.4 closes: workerSpawner.Spawn used to write the
// participant's command line microseconds after OpenSession returned, before
// the session's shell-integration axis had said anything at all. The agent
// wrapper then raced the backend's own accept-flush (nocx-ui8q6.2) enrolling
// over the same authenticated lifecycle channel the shell's hello uses, and
// lost the race intermittently rather than always — which is worse to
// diagnose than a certainty.
//
// These tests exercise workerSpawner.Spawn directly against a double of the
// new integrationAwaiterSeam, exactly the way internal/workers/registrar_test.go
// exercises Registrar against doubles of Spawner and Enrolments: the seam
// under test is Spawn's own call to AwaitIntegration, and faking what that
// call answers is the sanctioned way to assert a spawn's behaviour for
// `integrated`, `conventional`, and "never answered" without needing a real
// shell to actually integrate or fail to. The session opener and the layout's
// tab store are real enough to prove the two things the acceptance criteria
// ask about directly: the pty's own recorded bytes (was the command actually
// written), and whether a refused spawn leaves a tab or a session behind.
//
// What these tests do NOT cover: "no participant in the store" for a Spawn
// failure. That is internal/workers.Registrar's compensation, already
// covered generically for ANY Spawn error by
// TestAFailedRegistrationLeavesNoDelegation and
// TestARefusalAtTheBoundForksNothingAndRecordsNothing in registrar_test.go —
// Registrar's compensate() does not inspect why Spawn failed, so those tests
// already prove the store gets no live participant regardless of which of
// the reasons below produced the error.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// fakeAxisTabs is the paneMinter double for these tests: it records every
// CreateTab and DeleteTab call, which is what proves a refused spawn left no
// orphan tab behind (nocx-ui8q6.4's second fix, beside the axis wait itself).
type fakeAxisTabs struct {
	mu      sync.Mutex
	created []string
	deleted []string
}

func (f *fakeAxisTabs) CreateTab(_ context.Context, tab content.Tab, _ content.Pane) (content.Created[content.NewTab], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, tab.ID)
	return content.Created[content.NewTab]{}, nil
}

func (f *fakeAxisTabs) DeleteTab(_ context.Context, id string, _ content.Replacement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeAxisTabs) snapshot() (created, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...), append([]string(nil), f.deleted...)
}

// fakeAxisOpener opens a REAL session over a real registry (the same stub-pty
// factory worker_test.go uses), so EnqueueWrite, ID and Identity all behave
// as they do in production; only the integration axis beside it is a double.
type fakeAxisOpener struct {
	reg *session.Reg
	// closeBeforeReturn reproduces the pre-existing orphan-tab bug's own
	// trigger: a session that is already going away by the time Spawn tries
	// to write its first line into it.
	closeBeforeReturn bool

	mu   sync.Mutex
	last session.Session
}

func (f *fakeAxisOpener) OpenSession(ctx context.Context, spec transport.OpenSpec) (transport.OpenedSession, error) {
	sess, err := f.reg.Open(ctx, session.Config{
		Kind: session.KindLocal, Cols: spec.Cols, Rows: spec.Rows, PaneID: spec.PaneID,
	})
	if err != nil {
		return transport.OpenedSession{}, err
	}
	if f.closeBeforeReturn {
		_ = f.reg.Close(sess.ID())
	}
	f.mu.Lock()
	f.last = sess
	f.mu.Unlock()
	return transport.OpenedSession{Session: sess}, nil
}

func (f *fakeAxisOpener) lastSessionID() session.ID {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last == nil {
		return ""
	}
	return f.last.ID()
}

// failingAxisOpener never gets as far as a session — it reproduces the OTHER
// pre-existing orphan-tab gap, where OpenSession itself fails after
// CreateTab already minted the row.
type failingAxisOpener struct{ err error }

func (f failingAxisOpener) OpenSession(context.Context, transport.OpenSpec) (transport.OpenedSession, error) {
	return transport.OpenedSession{}, f.err
}

// fakeAxisAwaiter is the integrationAwaiterSeam double. Each test tells it
// exactly what AwaitIntegration should answer, which is the seam's own
// contract and not a second opinion about the transport's real axis
// machinery — that machinery is exercised at the internal/transport level.
type fakeAxisAwaiter struct {
	mu      sync.Mutex
	calls   []session.ID
	outcome transport.IntegrationOutcome
	err     error
	// block, when true, makes AwaitIntegration wait for ctx.Done() instead of
	// answering at all — the "shell never answers" case.
	block bool
}

func (f *fakeAxisAwaiter) AwaitIntegration(ctx context.Context, sid session.ID) (transport.IntegrationOutcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, sid)
	block, outcome, err := f.block, f.outcome, f.err
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return transport.IntegrationOutcome{}, ctx.Err()
	}
	return outcome, err
}

func (f *fakeAxisAwaiter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// axisGateStand is the minimal composition these tests assert against: a
// real session registry over stub ptys, a recorded-calls tab store, and a
// controllable integration axis.
type axisGateStand struct {
	reg     *session.Reg
	ptys    *workerTestPTYFactory
	tabs    *fakeAxisTabs
	opener  *fakeAxisOpener
	awaiter *fakeAxisAwaiter
	spawner *workerSpawner
}

func newAxisGateStand(t *testing.T, closeBeforeReturn bool) *axisGateStand {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
	})
	tabs := &fakeAxisTabs{}
	opener := &fakeAxisOpener{reg: reg, closeBeforeReturn: closeBeforeReturn}
	awaiter := &fakeAxisAwaiter{}
	enrol := newWorkerEnrolments(logger, reg)
	spawner := &workerSpawner{
		layout:      tabs,
		opener:      opener,
		sessions:    reg,
		integration: awaiter,
		enrolments:  enrol,
		workspace:   "ws-test",
		log:         logger,
	}
	return &axisGateStand{reg: reg, ptys: ptys, tabs: tabs, opener: opener, awaiter: awaiter, spawner: spawner}
}

// Criterion: the command is NOT written while the axis reads `starting`, and
// the axis reaching `integrated` writes it.
func TestSpawnWritesTheCommandOnlyAfterTheAxisIntegrates(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.outcome = transport.IntegrationOutcome{
		Registered: true, Status: transport.IntegrationIntegrated,
	}

	spawned, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-integrated", Group: "worker-1", Task: "t", Command: "run-agent",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if spawned == nil {
		t.Fatal("Spawn returned no participant on an integrated axis")
	}
	if got := stand.awaiter.callCount(); got != 1 {
		t.Fatalf("AwaitIntegration called %d times, want exactly 1", got)
	}
	pty := stand.ptys.last()
	if pty == nil {
		t.Fatal("no pty was opened")
	}
	// EnqueueWrite only accepts the line; writeLoop drains it onto the pty on
	// its own goroutine (the doc on EnqueueWrite: "ACCEPTED IS NOT
	// DELIVERED"), so the assertion has to wait for delivery rather than read
	// immediately after Spawn returns.
	waittest.WaitFor(t, "the command line to reach the pty", func() bool {
		return pty.read() == "run-agent\n"
	})
	created, deleted := stand.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 0 {
		t.Fatalf("tabs created=%v deleted=%v, want one created tab and no deletions", created, deleted)
	}
}

// Criterion: the axis reaching `conventional` refuses the spawn PROMPTLY —
// not the enrolment deadline — with a reason naming that the pane cannot be
// watched, and leaves no orphan tab or session behind.
func TestSpawnRefusesPromptlyWhenTheShellAnswersConventional(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.outcome = transport.IntegrationOutcome{
		Registered: true, Status: transport.IntegrationConventional, Reason: ssh.ReasonUnsupportedShell,
	}

	start := time.Now()
	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-conventional", Group: "worker-1", Task: "t", Command: "run-agent",
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Spawn succeeded for a pane the axis answered conventional")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Spawn took %s to refuse a conventional pane — that is not prompt, it is a bound expiring", elapsed)
	}
	if !strings.Contains(err.Error(), "cannot be watched") {
		t.Fatalf("error %q does not say the pane cannot be watched", err)
	}
	if !strings.Contains(err.Error(), "shell answered") {
		t.Fatalf("error %q does not name that the shell answered (as opposed to never answering)", err)
	}

	if pty := stand.ptys.last(); pty != nil && pty.read() != "" {
		t.Fatalf("the command was written into a pane the axis had already refused: %q", pty.read())
	}
	created, deleted := stand.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 1 || created[0] != deleted[0] {
		t.Fatalf("tabs created=%v deleted=%v, want the one created tab deleted and nothing else", created, deleted)
	}
	if sid := stand.opener.lastSessionID(); sid != "" {
		if _, getErr := stand.reg.Get(sid); getErr == nil {
			t.Fatalf("session %s is still in the registry after a conventional refusal", sid)
		}
	}
}

// Criterion: the axis never answering (stuck at `starting` past the bound)
// is refused too, and says so DIFFERENTLY from `conventional` — "the shell
// never answered" needs a different fix from "the shell answered no".
func TestSpawnRefusesDistinctlyWhenTheShellNeverAnswers(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.block = true

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := stand.spawner.Spawn(ctx, workers.SpawnRequest{
		Participant: "p-never", Group: "worker-1", Task: "t", Command: "run-agent",
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Spawn succeeded for a shell that never answered")
	}
	if elapsed > time.Second {
		t.Fatalf("Spawn took %s to give up on a shell that never answers; it should stop at the given ctx bound", elapsed)
	}
	if !strings.Contains(err.Error(), "never answered") {
		t.Fatalf("error %q does not say the shell never answered", err)
	}
	if strings.Contains(err.Error(), "cannot be watched") {
		t.Fatalf("error %q reads like the conventional refusal; the two need different fixes and different sentences", err)
	}

	created, deleted := stand.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 1 || created[0] != deleted[0] {
		t.Fatalf("tabs created=%v deleted=%v, want the one created tab deleted and nothing else", created, deleted)
	}
	if sid := stand.opener.lastSessionID(); sid != "" {
		if _, getErr := stand.reg.Get(sid); getErr == nil {
			t.Fatalf("session %s is still in the registry after a never-answered refusal", sid)
		}
	}
}

// Absence (RegisterIntegration never called for this session) is a fast
// answer, not a wait and not a refusal: a spawner with nothing to watch lets
// the command through rather than hanging or inventing a failure for a
// question the session was never asked.
func TestSpawnProceedsWhenNoAxisIsTrackingTheSession(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.outcome = transport.IntegrationOutcome{Registered: false}

	spawned, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-absent", Group: "worker-1", Task: "t", Command: "run-agent",
	})
	if err != nil {
		t.Fatalf("Spawn refused a session with no axis at all: %v", err)
	}
	if spawned == nil {
		t.Fatal("Spawn returned no participant")
	}
	pty := stand.ptys.last()
	if pty == nil {
		t.Fatal("no pty was opened")
	}
	waittest.WaitFor(t, "the command line to reach the pty", func() bool {
		return pty.read() == "run-agent\n"
	})
	created, deleted := stand.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 0 {
		t.Fatalf("tabs created=%v deleted=%v, want one created tab and no deletions", created, deleted)
	}
}

// Regression: a queue write refused by an already-departing session used to
// leave the tab CreateTab minted standing in the store — the orphan this
// bead's brief called out as pre-existing. It must not any more.
func TestARefusedFirstLineNowDeletesTheOrphanTab(t *testing.T) {
	stand := newAxisGateStand(t, true) // the session is closed before Spawn writes to it
	stand.awaiter.outcome = transport.IntegrationOutcome{
		Registered: true, Status: transport.IntegrationIntegrated,
	}

	_, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-refused-line", Group: "worker-1", Task: "t", Command: "run-agent",
	})
	if err == nil {
		t.Fatal("Spawn succeeded despite a session whose input queue was already closed")
	}
	created, deleted := stand.tabs.snapshot()
	if len(created) != 1 || len(deleted) != 1 || created[0] != deleted[0] {
		t.Fatalf("the pre-existing orphan-tab bug regressed: created=%v deleted=%v", created, deleted)
	}
}

// Regression: a session that fails to OPEN at all (after CreateTab already
// minted the row) is the other pre-existing orphan-tab gap.
func TestASessionOpenFailureDeletesTheOrphanTab(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	tabs := &fakeAxisTabs{}
	spawner := &workerSpawner{
		layout: tabs,
		opener: failingAxisOpener{err: errors.New("boom: the helper is unreachable")},
		// enrolments and sessions are never reached on this path: OpenSession
		// fails before either is touched.
		workspace: "ws-test",
		log:       logger,
	}

	_, err := spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-open-fails", Group: "worker-1", Task: "t", Command: "run-agent",
	})
	if err == nil {
		t.Fatal("Spawn succeeded despite OpenSession failing")
	}
	created, deleted := tabs.snapshot()
	if len(created) != 1 || len(deleted) != 1 || created[0] != deleted[0] {
		t.Fatalf("an OpenSession failure left an orphan tab: created=%v deleted=%v", created, deleted)
	}
}
