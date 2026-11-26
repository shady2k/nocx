package main

// nocx-oalo1.12.5, at the seam a panel actually reaches: one real helper
// connection — the real host, the real handshake, the real client, the real
// session service and the real fs service — with the daemon's deadlines fired
// by hand instead of by a clock.
//
// A unit test of the lifecycle proves the rule; this proves the WIRING of it,
// which is the half that fails silently: the dispatcher has to notice the
// request, the session service has to be the one told, and the release has to
// hand the connection's own answer to the bound. A helper that retired in the
// middle of a panel's upload would pass every test that mounts a piece.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	fshostsvc "github.com/shady2k/nocx/internal/filesystem/hostsvc"
	filesystemlocal "github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
	portshostsvc "github.com/shady2k/nocx/internal/portsprobe/hostsvc"
	"github.com/shady2k/nocx/internal/waittest"
)

// manualTimers is the daemon's clock, owned by the test: every deadline the
// lifecycle arms is recorded and fired at the moment its rule is about.
type manualTimers struct {
	mu      sync.Mutex
	pending []manualDeadline
}

type manualDeadline struct {
	after time.Duration
	fire  func()
}

func (m *manualTimers) AfterFunc(after time.Duration, fire func()) session.LifecycleTimer {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = append(m.pending, manualDeadline{after: after, fire: fire})
	return manualTimer{}
}

// fire runs every recorded deadline of that length, including ones a real
// clock would have stopped: the rule must survive its own timer firing after
// it was disarmed, which is the race a loaded helper loses.
func (m *manualTimers) fire(after time.Duration) {
	m.mu.Lock()
	pending := append([]manualDeadline(nil), m.pending...)
	m.mu.Unlock()
	for _, d := range pending {
		if d.after == after {
			d.fire()
		}
	}
}

func (m *manualTimers) count(after time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, d := range m.pending {
		if d.after == after {
			n++
		}
	}
	return n
}

type manualTimer struct{}

func (manualTimer) Stop() bool { return true }

// panelStand is one real helper daemon minus the process: the services
// cmd/nocx-helper's accept loop registers, over one real connection.
type panelStand struct {
	sessions *session.Service
	files    *fshostsvc.Service
	ports    *portshostsvc.Service
	timers   *manualTimers
	drained  chan struct{}
	client   *client.Client
	out      *frameWatch
}

// frameWatch is the helper's end of the socket. It records when a frame
// carrying a marker reaches the wire, so a test asks the one question the
// ordering is about — "was THIS answer written" — rather than "did any byte
// move", which a notification or a previous response would answer wrongly.
type frameWatch struct {
	w      io.Writer
	mu     sync.Mutex
	marker []byte
	seen   int
}

func (c *frameWatch) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.mu.Lock()
	if n > 0 && len(c.marker) > 0 && bytes.Contains(p, c.marker) {
		c.seen++
	}
	c.mu.Unlock()
	return n, err
}

// watch starts counting frames that carry marker.
func (c *frameWatch) watch(marker string) {
	c.mu.Lock()
	c.marker = []byte(marker)
	c.mu.Unlock()
}

func (c *frameWatch) reset() {
	c.mu.Lock()
	c.seen = 0
	c.mu.Unlock()
}

func (c *frameWatch) saw() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen > 0
}

func standUpPanelDaemon(t *testing.T) *panelStand {
	t.Helper()
	return standUpPanelDaemonWithAdmission(t, nil)
}

// standUpPanelDaemonWithAdmission builds the same stand with a test-owned
// admission for the fs service, or with the daemon's own when it is nil.
// Owning it is how a test arranges the write half's ordering: the daemon's
// release is what begins the drain, and a test may watch the moment it runs
// (review M2).
func standUpPanelDaemonWithAdmission(t *testing.T, admission fshostsvc.Admission) *panelStand {
	t.Helper()
	drained := make(chan struct{})
	var once sync.Once
	stand := &panelStand{timers: &manualTimers{}, drained: drained}
	// The deadlines are the production ones; only the clock is the test's.
	stand.sessions = session.New(session.Options{
		Generation:      "gen-panel-under-test",
		StartupGrace:    session.MeasuredStartupGrace,
		ConnectedGrace:  session.ConnectedIdleGrace,
		LifecycleTimers: stand.timers,
		OnDrained:       func() { once.Do(func() { close(drained) }) },
	})
	t.Cleanup(stand.sessions.Close)

	// main.go's own binding, in the same order: the fs service is constructed
	// with the admission and the session service fills it in afterwards.
	daemon := &daemonAdmission{}
	if admission == nil {
		admission = daemon
	} else {
		daemon = nil
	}
	files, filesErr := fshostsvc.New(filesystemlocal.New(), fshostsvc.JournalConfig{
		Root:       t.TempDir(),
		Hostname:   "test-host",
		InstanceID: fmt.Sprintf("%032x", 1),
	}, fshostsvc.WithAdmission(admission))
	if filesErr != nil {
		t.Fatalf("fs service: %v", filesErr)
	}
	if daemon != nil {
		daemon.sessions = stand.sessions
	}
	t.Cleanup(func() {
		if closeErr := files.Close(); closeErr != nil {
			t.Errorf("closing the fs service: %v", closeErr)
		}
	})
	stand.files = files
	// The ports service the accept loop registers beside it: the host it runs
	// on is the stand's own quiet runner, so a sample never executes a probe on
	// the machine running the tests.
	stand.ports = portshostsvc.New(quietPortTools{})

	helperEnd, coordEnd := net.Pipe()
	// The helper's end of the socket is watched, so a test can tell whether the
	// answer existed at the moment the daemon's release ran.
	stand.out = &frameWatch{w: helperEnd}
	h := host.New(helperEnd, stand.out, helperHash, "instance-1", discardLog())
	// The same services the daemon registers, in the same order the accept
	// loop binds them: the connection is bound to the session service, which
	// is what starts the connected-idle bound in the first place.
	h.Register(stubService{"git-stub"})
	h.Register(stand.sessions)
	h.Register(stand.files)
	h.Register(stand.ports)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	served := make(chan struct{})
	go func() {
		defer close(served)
		// The accept loop's own two lines: the connection is bound to the
		// session service before the protocol engine runs, which is what arms
		// the daemon's deadlines at all.
		release := stand.sessions.Bind(h)
		defer release()
		_ = h.Serve(ctx)
	}()
	t.Cleanup(func() { cancel(); <-served })

	c, err := client.Dial(ctx, client.Config{
		Exec:        client.NewSocketConn(coordEnd),
		ExpectHash:  helperHash,
		SentinelTTL: 5 * time.Second,
		Log:         discardLog(),
	})
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	// Closed BEFORE the connection goroutine is waited for, so a failing test
	// still lets Serve see EOF instead of hanging until go test's timeout.
	t.Cleanup(func() { _ = c.Close() })
	stand.client = c
	return stand
}

// TestAPanelClientOutlivesTheConnectedIdleBound: the client asks `fs.inventory`
// — the call nocx-oalo1.12.4's coordinator makes after the hello, and the one
// the plan names as marking an otherwise idle panel client — and the bound the
// bind armed must retire nothing. When the client leaves, the bound is armed
// again and the daemon does end: a panel client is not a daemon that never
// retires, it is one whose retirement waits for its work.
func TestAPanelClientOutlivesTheConnectedIdleBound(t *testing.T) {
	stand := standUpPanelDaemon(t)

	var inventory proto.FSInventoryResult
	if err := stand.client.Call(context.Background(), proto.ServiceFS, proto.OpInventory, proto.FSInventoryParams{}, &inventory); err != nil {
		t.Fatalf("fs.inventory: %v", err)
	}
	if len(inventory.Stranded) != 0 {
		t.Fatalf("a fresh daemon reported stranded temporaries: %v", inventory.Stranded)
	}

	stand.timers.fire(session.ConnectedIdleGrace)
	select {
	case <-stand.drained:
		t.Fatal("a helper retired while a panel client was working on it")
	default:
	}

	// The connection ends. Its release is the observable: the bound is armed
	// again, so the daemon's clock holds a deadline it did not hold before.
	armedBefore := stand.timers.count(session.ConnectedIdleGrace)
	if err := stand.client.Close(); err != nil {
		t.Fatalf("closing the connection: %v", err)
	}
	waittest.WaitFor(t, "the released panel connection to re-arm the connected-idle bound", func() bool {
		return stand.timers.count(session.ConnectedIdleGrace) > armedBefore
	})
	stand.timers.fire(session.ConnectedIdleGrace)
	waittest.WaitFor(t, "the daemon to retire after its panel client left", func() bool {
		select {
		case <-stand.drained:
			return true
		default:
			return false
		}
	})
}

// TestAPortsPanelClientOutlivesTheConnectedIdleBound is nocx-oalo1.13.2's half
// of the .12.5 rule, at the seam a Ports panel reaches: this connection asked
// `ports.sample`, which is panel work, so the bound armed by the bind must
// retire nothing. The silent client beside it is still retired — the exemption
// is the errand and not the connection.
func TestAPortsPanelClientOutlivesTheConnectedIdleBound(t *testing.T) {
	stand := standUpPanelDaemon(t)

	var sample proto.PortsSampleResult
	if err := stand.client.Call(context.Background(), proto.ServicePorts, proto.OpPortsSample, proto.PortsSampleParams{}, &sample); err != nil {
		t.Fatalf("%s.%s: %v", proto.ServicePorts, proto.OpPortsSample, err)
	}
	if sample.State != proto.PortsStateAvailable {
		t.Fatalf("sample state = %q (%s), want available", sample.State, sample.Classification)
	}

	stand.timers.fire(session.ConnectedIdleGrace)
	select {
	case <-stand.drained:
		t.Fatal("a helper retired while a panel client was sampling its host's ports")
	default:
	}
}

// TestASilentClientIsStillRetired is the other half, end to end (nocx-398ca
// stays fixed): a client that connects and asks for nothing but the handshake
// is exactly the abandoned client the bound exists for.
func TestASilentClientIsStillRetired(t *testing.T) {
	stand := standUpPanelDaemon(t)
	stand.timers.fire(session.ConnectedIdleGrace)
	waittest.WaitFor(t, "a silent client's daemon to retire", func() bool {
		select {
		case <-stand.drained:
			return true
		default:
			return false
		}
	})
}

// TestAPanelClientIsNotASessionClient pins the distinction the rule turns on:
// asking the session service something is not panel work, and the bound keeps
// asking — this is the state of a coordinator that is on its way to a spawn.
func TestAPanelClientIsNotASessionClient(t *testing.T) {
	stand := standUpPanelDaemon(t)
	var listing proto.SessionsResult
	if err := stand.client.Call(context.Background(), proto.ServiceSession, proto.OpSessions, proto.SessionsParams{}, &listing); err != nil {
		t.Fatalf("session.sessions: %v", err)
	}
	stand.timers.fire(session.ConnectedIdleGrace)
	waittest.WaitFor(t, "a daemon whose client asked only the session service to retire", func() bool {
		select {
		case <-stand.drained:
			return true
		default:
			return false
		}
	})
}

// observedAdmission is a daemon reservation whose release a test watches. The
// stand's own OnDrained only records the retirement, which is what lets a test
// see the ordering WITHOUT the transport dying underneath it: what the write
// half owes is that the answer exists on the wire before the release runs
// (review M2).
type observedAdmission struct {
	answerWritten func() bool

	mu          sync.Mutex
	afterAnswer bool
	ran         bool
	released    chan struct{}
	once        sync.Once
}

func (a *observedAdmission) Admit() (func(), error) {
	return func() {
		a.mu.Lock()
		a.afterAnswer = a.answerWritten != nil && a.answerWritten()
		a.ran = true
		a.mu.Unlock()
		a.once.Do(func() { close(a.released) })
	}, nil
}

func (a *observedAdmission) releasedAfterAnswer() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ran && a.afterAnswer
}

// TestACommitAnswerIsOnTheWireBeforeTheDrainItReleases is review M2 at the
// seam a panel reaches. The reservation is what the daemon's last-session drain
// waits for, so releasing it inside the handler starts the drain before the
// answer is written: a caller can be told nothing about a file that has already
// landed, and replay into an outcome it cannot settle. The answer is written
// first, and only then is the daemon free to retire.
func TestACommitAnswerIsOnTheWireBeforeTheDrainItReleases(t *testing.T) {
	admission := &observedAdmission{released: make(chan struct{})}
	stand := standUpPanelDaemonWithAdmission(t, admission)
	stand.out.watch(`"outcome":"` + proto.FSOutcomeCommitted + `"`)
	admission.answerWritten = stand.out.saw

	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := fmt.Sprintf("%032x", 0x12_5a_00_01)
	if err := stand.client.Call(ctx, proto.ServiceFS, proto.OpWriteOpen, proto.FSWriteOpenParams{ID: id, Path: target}, &proto.FSWriteOpenResult{}); err != nil {
		t.Fatalf("fs.write-open: %v", err)
	}
	if err := stand.client.Call(ctx, proto.ServiceFS, proto.OpWrite, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("new\n")}, &proto.FSWriteResult{}); err != nil {
		t.Fatalf("fs.write: %v", err)
	}

	// From here the helper's writes are watched for the commit's own answer.
	stand.out.reset()
	var committed proto.FSWriteCommitResult
	if err := stand.client.Call(ctx, proto.ServiceFS, proto.OpWriteCommit, proto.FSWriteCommitParams{
		ID: id, Path: target, Size: int64(len("new\n")),
	}, &committed); err != nil {
		t.Fatalf("the commit's answer never reached its caller: %v", err)
	}
	if committed.Outcome != proto.FSOutcomeCommitted || committed.Version == nil {
		t.Fatalf("the commit answered %+v, want a committed version", committed)
	}
	waittest.WaitFor(t, "the commit's reservation to be released", func() bool {
		select {
		case <-admission.released:
			return true
		default:
			return false
		}
	})
	if !admission.releasedAfterAnswer() {
		t.Fatal("the daemon's drain began before the commit's answer was written")
	}
}

// TestADrainingDaemonRefusesAWriteOpenAsDraining is review m1 over the wire:
// the fs service takes the session service's reservation, and a generation
// that has stopped admitting work must answer `draining` — the code the session
// service has always used — rather than `internal`, which reads as a broken
// helper and sends the panel into a replay against a daemon that is leaving.
func TestADrainingDaemonRefusesAWriteOpenAsDraining(t *testing.T) {
	stand := standUpPanelDaemon(t)
	// No session, no panel work: the connected-idle bound retires this
	// generation, which closes admission. The stand records the retirement
	// without closing the socket, so the last request a coordinator sends to a
	// draining helper is what this test makes.
	stand.timers.fire(session.ConnectedIdleGrace)
	waittest.WaitFor(t, "the daemon to retire", func() bool {
		select {
		case <-stand.drained:
			return true
		default:
			return false
		}
	})

	var out proto.FSWriteOpenResult
	err := stand.client.Call(context.Background(), proto.ServiceFS, proto.OpWriteOpen, proto.FSWriteOpenParams{
		ID: fmt.Sprintf("%032x", 0x12_5a_00_02), Path: "/nonexistent/note.txt",
	}, &out)
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("fs.write-open on a draining daemon = %v, want a wire refusal", err)
	}
	if refusal.Code != proto.ErrCodeDraining {
		t.Fatalf("fs.write-open on a draining daemon answered %q, want %q", refusal.Code, proto.ErrCodeDraining)
	}
}
