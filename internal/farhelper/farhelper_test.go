package farhelper_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/waittest"
)

const (
	machine      = "SHA256:test-machine"
	otherMachine = "SHA256:other-machine"
	testHash     = "test-build-hash"
)

// ── the connection source ────────────────────────────────────────────────

type fakeConnections struct {
	mu       sync.Mutex
	conns    map[string]farhelper.Connection
	watchers map[uint64]func(farhelper.Connection)
	next     uint64
}

func newFakeConnections() *fakeConnections {
	return &fakeConnections{
		conns:    make(map[string]farhelper.Connection),
		watchers: make(map[uint64]func(farhelper.Connection)),
	}
}

func (f *fakeConnections) List() []farhelper.Connection {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]farhelper.Connection, 0, len(f.conns))
	for _, c := range f.conns {
		out = append(out, c)
	}
	return out
}

func (f *fakeConnections) Watch(fn func(farhelper.Connection)) (stop func()) {
	f.mu.Lock()
	f.next++
	id := f.next
	f.watchers[id] = fn
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { f.mu.Lock(); delete(f.watchers, id); f.mu.Unlock() }) }
}

func (f *fakeConnections) Put(c farhelper.Connection) {
	f.mu.Lock()
	f.conns[c.ID] = c
	watchers := make([]func(farhelper.Connection), 0, len(f.watchers))
	for _, fn := range f.watchers {
		watchers = append(watchers, fn)
	}
	f.mu.Unlock()
	for _, fn := range watchers {
		fn(c)
	}
}

func (f *fakeConnections) update(id string, mutate func(*farhelper.Connection)) {
	f.mu.Lock()
	c := f.conns[id]
	f.mu.Unlock()
	mutate(&c)
	f.Put(c)
}

func (f *fakeConnections) end(id string) {
	f.update(id, func(c *farhelper.Connection) { c.State = hostconn.Ended })
}

// ── the two stores ───────────────────────────────────────────────────────

type fakeModes struct {
	mu       sync.Mutex
	values   map[string]hostmode.Mode
	watchers map[uint64]func()
	next     uint64
}

func newFakeModes() *fakeModes {
	return &fakeModes{values: make(map[string]hostmode.Mode), watchers: make(map[uint64]func())}
}

func (m *fakeModes) Lookup(fingerprint string) (hostmode.Mode, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mode, ok := m.values[fingerprint]
	return mode, ok
}

func (m *fakeModes) Watch(fn func()) (stop func()) {
	m.mu.Lock()
	m.next++
	id := m.next
	m.watchers[id] = fn
	m.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { m.mu.Lock(); delete(m.watchers, id); m.mu.Unlock() }) }
}

func (m *fakeModes) Set(fingerprint string, mode hostmode.Mode) {
	m.mu.Lock()
	m.values[fingerprint] = mode
	watchers := make([]func(), 0, len(m.watchers))
	for _, fn := range m.watchers {
		watchers = append(watchers, fn)
	}
	m.mu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

type fakeConsents struct {
	mu       sync.Mutex
	values   map[string]consent.Answer
	watchers map[uint64]func()
	next     uint64
}

func newFakeConsents() *fakeConsents {
	return &fakeConsents{values: make(map[string]consent.Answer), watchers: make(map[uint64]func())}
}

func (c *fakeConsents) Lookup(fingerprint string) (consent.Answer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	answer, ok := c.values[fingerprint]
	return answer, ok
}

func (c *fakeConsents) Watch(fn func()) (stop func()) {
	c.mu.Lock()
	c.next++
	id := c.next
	c.watchers[id] = fn
	c.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { c.mu.Lock(); delete(c.watchers, id); c.mu.Unlock() }) }
}

func (c *fakeConsents) Set(fingerprint string, answer consent.Answer) {
	c.mu.Lock()
	c.values[fingerprint] = answer
	watchers := make([]func(), 0, len(c.watchers))
	for _, fn := range c.watchers {
		watchers = append(watchers, fn)
	}
	c.mu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

func (c *fakeConsents) Revoke(fingerprint string) {
	c.mu.Lock()
	delete(c.values, fingerprint)
	watchers := make([]func(), 0, len(c.watchers))
	for _, fn := range c.watchers {
		watchers = append(watchers, fn)
	}
	c.mu.Unlock()
	for _, fn := range watchers {
		fn()
	}
}

// ── the remote seams ─────────────────────────────────────────────────────

type gate struct {
	once sync.Once
	open chan struct{}
}

func newGate() *gate { return &gate{open: make(chan struct{})} }

func (g *gate) release() { g.once.Do(func() { close(g.open) }) }

func (g *gate) wait(ctx context.Context, ignoreCancel bool) error {
	if ignoreCancel {
		<-g.open
		return nil
	}
	select {
	case <-g.open:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type fakeProber struct {
	mu           sync.Mutex
	calls        int
	platform     farhelper.Platform
	err          error
	entered      chan struct{}
	gate         *gate
	ignoreCancel bool
}

func newFakeProber() *fakeProber {
	return &fakeProber{
		platform: farhelper.Platform{GOOS: "linux", GOARCH: "arm64"},
		entered:  make(chan struct{}, 64),
	}
}

func (p *fakeProber) Probe(ctx context.Context, _ farhelper.Target) (farhelper.Platform, error) {
	p.mu.Lock()
	p.calls++
	gate, ignore, platform, err := p.gate, p.ignoreCancel, p.platform, p.err
	p.mu.Unlock()
	select {
	case p.entered <- struct{}{}:
	default:
	}
	if gate != nil {
		if werr := gate.wait(ctx, ignore); werr != nil {
			return farhelper.Platform{}, werr
		}
	}
	return platform, err
}

func (p *fakeProber) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type fakeInstaller struct {
	mu           sync.Mutex
	findCalls    int
	found        bool
	findErr      error
	calls        int
	result       farhelper.Installed
	err          error
	entered      chan struct{}
	finished     chan struct{}
	gate         *gate
	ignoreCancel bool
	targets      []farhelper.Target
}

func newFakeInstaller() *fakeInstaller {
	return &fakeInstaller{
		result:   farhelper.Installed{Dir: "/home/u/.nocx/helper", Generation: testHash},
		entered:  make(chan struct{}, 64),
		finished: make(chan struct{}, 64),
	}
}

func (i *fakeInstaller) Find(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.findCalls++
	return i.result, i.found, i.findErr
}

func (i *fakeInstaller) Install(ctx context.Context, target farhelper.Target, _ farhelper.Platform) (farhelper.Installed, error) {
	i.mu.Lock()
	i.calls++
	i.targets = append(i.targets, target)
	gate, ignore, result, err := i.gate, i.ignoreCancel, i.result, i.err
	i.mu.Unlock()
	select {
	case i.entered <- struct{}{}:
	default:
	}
	if gate != nil {
		if werr := gate.wait(ctx, ignore); werr != nil {
			return farhelper.Installed{}, werr
		}
	}
	select {
	case i.finished <- struct{}{}:
	default:
	}
	return result, err
}

func (i *fakeInstaller) count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
}

func (i *fakeInstaller) findCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.findCalls
}

func (i *fakeInstaller) installedTargets() []farhelper.Target {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]farhelper.Target(nil), i.targets...)
}

type fakeDialer struct {
	mu           sync.Mutex
	calls        int
	err          error
	entered      chan struct{}
	returned     chan struct{}
	gate         *gate
	ignoreCancel bool
	peers        []*fakePeer
}

func newFakeDialer() *fakeDialer {
	return &fakeDialer{entered: make(chan struct{}, 64), returned: make(chan struct{}, 64)}
}

func (d *fakeDialer) Dial(ctx context.Context, _ farhelper.Target, _ farhelper.Installed) (*client.Client, error) {
	d.mu.Lock()
	d.calls++
	gate, ignore, err := d.gate, d.ignoreCancel, d.err
	d.mu.Unlock()
	select {
	case d.entered <- struct{}{}:
	default:
	}
	if gate != nil {
		if werr := gate.wait(ctx, ignore); werr != nil {
			return nil, werr
		}
	}
	if err != nil {
		select {
		case d.returned <- struct{}{}:
		default:
		}
		return nil, err
	}
	peer := newFakePeer(d.logger())
	d.mu.Lock()
	d.peers = append(d.peers, peer)
	d.mu.Unlock()
	select {
	case d.returned <- struct{}{}:
	default:
	}
	return peer.client, nil
}

func (d *fakeDialer) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (d *fakeDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *fakeDialer) peerCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.peers)
}

func (d *fakeDialer) lastPeer(t *testing.T) *fakePeer {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.peers) == 0 {
		t.Fatalf("no peer was dialled")
	}
	return d.peers[len(d.peers)-1]
}

// ── a real client over a pipe with a hello peer ──────────────────────────

type fakeLane struct {
	closeBlock   <-chan struct{}
	closeEntered chan struct{}
	conn         net.Conn
	done         chan struct{}
	waitDone     chan struct{}
	closeOnce    sync.Once
	doneOnce     sync.Once
	waitOnce     sync.Once
	lostErr      error
}

func (l *fakeLane) Stdin() io.WriteCloser { return l.conn }
func (l *fakeLane) Stdout() io.Reader     { return l.conn }
func (l *fakeLane) Stderr() io.Reader     { return bytesReader{} }
func (l *fakeLane) Start(string) error    { return nil }
func (l *fakeLane) Wait() (int, error) {
	<-l.waitDone
	return 0, nil
}
func (l *fakeLane) Done() <-chan struct{} { return l.done }
func (l *fakeLane) LostErr() error        { return l.lostErr }
func (l *fakeLane) Close() error {
	l.closeOnce.Do(func() {
		if l.closeEntered != nil {
			close(l.closeEntered)
		}
		if l.closeBlock != nil {
			<-l.closeBlock
		}
		_ = l.conn.Close()
		l.waitOnce.Do(func() { close(l.waitDone) })
	})
	return nil
}

func (l *fakeLane) lose(err error) {
	l.lostErr = err
	l.doneOnce.Do(func() { close(l.done) })
	_ = l.conn.Close()
	l.waitOnce.Do(func() { close(l.waitDone) })
}

type bytesReader struct{}

func (bytesReader) Read([]byte) (int, error) { return 0, io.EOF }

type fakePeer struct {
	client *client.Client
	lane   *fakeLane
	closed chan struct{}
}

func (p *fakePeer) Lose() { p.lane.lose(errors.New("helper daemon gone")) }

func (p *fakePeer) wasClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

func newFakePeer(logger *slog.Logger) *fakePeer {
	clientEnd, serverEnd := net.Pipe()
	lane := &fakeLane{conn: clientEnd, done: make(chan struct{}), waitDone: make(chan struct{})}
	go serveHello(serverEnd)
	c, err := client.Dial(context.Background(), client.Config{
		Exec: lane, ExpectHash: testHash, Log: logger, SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		panic("farhelper test: hello peer: " + err.Error())
	}
	peer := &fakePeer{client: c, lane: lane, closed: make(chan struct{})}
	go func() {
		<-lane.waitDone
		close(peer.closed)
	}()
	return peer
}

// serveHello answers the client's hello on the far side of the pipe and then
// holds the wire open, which is what a serving helper does between calls.
func serveHello(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	dec := proto.NewDecoder(func(ty proto.FrameType, _ uint32, _ uint32, payload []byte) {
		if ty != proto.TypeHello {
			return
		}
		var hello proto.Hello
		_ = json.Unmarshal(payload, &hello)
		_, _ = fmt.Fprintf(conn, "nocx-helper %s ready\n", proto.Version)
		ok := proto.HelloOK{Version: proto.Version, Nonce: hello.Nonce, ContentHash: testHash, InstanceID: "instance-test"}
		raw, _ := json.Marshal(ok)
		_, _ = conn.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, raw))
	}, nil)
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			_ = dec.Feed(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// ── the harness ──────────────────────────────────────────────────────────

// countingClock is the harness clock with a count of After registrations. The
// entry registers its next wait only after it has accepted the result that
// armed it, so the count says the reducer has applied a result and moved on —
// unlike a dial returning, which only says the reducer is about to see it.
type countingClock struct {
	*hostconntest.Clock

	mu     sync.Mutex
	afters int
}

func (c *countingClock) After(d time.Duration) <-chan monoclock.Nanos {
	c.mu.Lock()
	c.afters++
	c.mu.Unlock()
	return c.Clock.After(d)
}

func (c *countingClock) afterRegistrations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.afters
}

type harness struct {
	t        *testing.T
	clock    *countingClock
	conns    *fakeConnections
	modes    *fakeModes
	consents *fakeConsents
	probe    *fakeProber
	install  *fakeInstaller
	dial     *fakeDialer
	reg      *farhelper.Registry

	mu       sync.Mutex
	statuses []published
}

type published struct {
	id     string
	status farhelper.Status
}

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	_, logger := logtest.New(t)
	return logger
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		clock:    &countingClock{Clock: hostconntest.New()},
		conns:    newFakeConnections(),
		modes:    newFakeModes(),
		consents: newFakeConsents(),
		probe:    newFakeProber(),
		install:  newFakeInstaller(),
		dial:     newFakeDialer(),
	}
	h.reg = farhelper.New(farhelper.Config{
		Connections: h.conns,
		Modes:       h.modes,
		Consents:    h.consents,
		Probe:       h.probe,
		Install:     h.install,
		Dial:        h.dial,
		Clock:       h.clock,
		Log:         testLogger(t),
	})
	h.reg.Watch(func(id string, status farhelper.Status) {
		h.mu.Lock()
		h.statuses = append(h.statuses, published{id: id, status: status})
		h.mu.Unlock()
	})
	t.Cleanup(h.reg.Close)
	return h
}

func (h *harness) granted(fp string) {
	h.modes.Set(fp, hostmode.Helper)
	h.consents.Set(fp, consent.Granted)
}

func connected(id string, generation uint64) farhelper.Connection {
	return farhelper.Connection{
		ID:                 id,
		Destination:        proto.SSHDestination{Host: "host.example", Port: 22, User: "u"},
		HostKeyFingerprint: machine,
		Generation:         generation,
		State:              hostconn.Connected,
		Shells:             1,
	}
}

func (h *harness) want(id string, state farhelper.State, reason farhelper.Reason, action farhelper.Action) {
	h.t.Helper()
	waittest.WaitForDetail(h.t, fmt.Sprintf("axis %s to be %s/%s/%s", id, state, reason, action), func() string {
		status, ok := h.reg.Axis(id)
		return fmt.Sprintf("axis is %+v (known=%v)", status, ok)
	}, func() bool {
		status, ok := h.reg.Axis(id)
		if !ok {
			return false
		}
		return status.State == state && status.Reason == reason && status.Action == action
	})
}

func (h *harness) sawState(state farhelper.State) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.statuses {
		if p.status.State == state {
			return true
		}
	}
	return false
}

// ── scenarios ────────────────────────────────────────────────────────────

// A healthy connection decides once, installs once and serves. It is the
// normal pair for every fault test below.
func TestAHealthyConnectionProbesInstallsAndServes(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("c1", 1))

	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	if got := h.probe.count(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d, want 1", got)
	}
	if got := h.dial.count(); got != 1 {
		t.Fatalf("dial calls = %d, want 1", got)
	}
	lease, err := h.reg.Acquire(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lease.Client == nil || lease.Ctx == nil {
		t.Fatalf("Acquire answered %+v", lease)
	}
	// A pause cancels the lease's context: nothing may use a client whose
	// transport is gone.
	h.conns.update("c1", func(c *farhelper.Connection) { c.State = hostconn.Stalled })
	h.want("c1", farhelper.StatePaused, farhelper.ReasonConnection, farhelper.ActionNone)
	select {
	case <-lease.Ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the lease context outlived the connection")
	}
}

// N events of one generation are one decision: one probe, one install, one
// dial.
func TestOneGenerationDecidesOnce(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	var seq uint64
	for i := 0; i < 20; i++ {
		c := connected("c1", 1)
		seq++
		c.Seq = seq
		c.Slow = i%2 == 0
		c.Shells = 1 + i%3
		h.conns.Put(c)
	}
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	if got := h.probe.count(); got != 1 {
		t.Fatalf("probe calls = %d, want 1 for one generation", got)
	}
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d, want 1", got)
	}
	if got := h.dial.count(); got != 1 {
		t.Fatalf("dial calls = %d, want 1", got)
	}
}

// raw is an answer, and no remote command is run to discover it.
func TestRawModeRunsNoProbe(t *testing.T) {
	h := newHarness(t)
	h.modes.Set(machine, hostmode.Raw)
	h.consents.Set(machine, consent.Granted)
	h.conns.Put(connected("c1", 1))

	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonModeRaw, farhelper.ActionHostSettings)
	if got := h.probe.count(); got != 0 {
		t.Fatalf("probe calls = %d, want 0 on a raw host", got)
	}
	if got := h.install.count(); got != 0 || h.dial.count() != 0 {
		t.Fatalf("install=%d dial=%d, want 0/0", h.install.count(), h.dial.count())
	}
	if _, err := h.reg.Acquire(context.Background(), "c1"); err == nil {
		t.Fatal("Acquire on a raw host answered no error")
	} else {
		var unavailable *farhelper.Unavailable
		if !errors.As(err, &unavailable) || unavailable.Reason != farhelper.ReasonModeRaw {
			t.Fatalf("Acquire error = %v, want *Unavailable mode-raw", err)
		}
	}
}

// C9: no install while the transport is slow, and none before a shell.
func TestInstallWaitsForASoundTransportAndAShell(t *testing.T) {
	t.Run("slow", func(t *testing.T) {
		h := newHarness(t)
		h.granted(machine)
		c := connected("c1", 1)
		c.Slow = true
		h.conns.Put(c)
		// The probe is allowed while slow; the install is not.
		waittest.WaitFor(t, "the probe", func() bool { return h.probe.count() == 1 })
		if got := h.install.count(); got != 0 {
			t.Fatalf("install calls = %d, want 0 while slow", got)
		}
		h.conns.update("c1", func(c *farhelper.Connection) { c.Slow = false })
		waittest.WaitFor(t, "the install", func() bool { return h.install.count() == 1 })
		h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	})

	t.Run("before a shell", func(t *testing.T) {
		h := newHarness(t)
		h.granted(machine)
		c := connected("c1", 1)
		c.Shells = 0
		h.conns.Put(c)
		waittest.WaitFor(t, "the probe", func() bool { return h.probe.count() == 1 })
		if got := h.install.count(); got != 0 {
			t.Fatalf("install calls = %d, want 0 before a shell", got)
		}
		h.conns.update("c1", func(c *farhelper.Connection) { c.Shells = 1 })
		waittest.WaitFor(t, "the install", func() bool { return h.install.count() == 1 })
		if got := h.install.count(); got != 1 {
			t.Fatalf("install calls = %d, want exactly 1", got)
		}
	})
}

// Scenario 2: the last pane closes while the helper installs. The connection
// lives, so the entry keeps deciding, and a late install result is dropped
// when the connection has ended meanwhile.
func TestTheLastPaneClosingDoesNotCancelAnInstall(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.install.gate = newGate()
	h.install.ignoreCancel = true
	h.conns.Put(connected("c1", 1))
	select {
	case <-h.install.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the install never started")
	}
	// The pane goes; a tunnel (or the connection itself) keeps it alive.
	h.conns.update("c1", func(c *farhelper.Connection) { c.Shells = 0 })
	h.install.gate.release()

	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	if got := h.dial.count(); got != 1 {
		t.Fatalf("dial calls = %d, want 1: the connection outlived the pane", got)
	}
}

func TestALateInstallResultIsDroppedWhenTheConnectionEnded(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.install.gate = newGate()
	h.install.ignoreCancel = true
	h.conns.Put(connected("c1", 1))
	select {
	case <-h.install.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the install never started")
	}
	h.conns.end("c1")
	if _, ok := h.reg.Axis("c1"); ok {
		t.Fatal("the axis survived its connection")
	}
	h.install.gate.release()
	select {
	case <-h.install.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the install never finished")
	}
	if got := h.dial.count(); got != 0 {
		t.Fatalf("dial calls = %d, want 0: the entry was removed", got)
	}
}

// Scenario 3: a cut pauses a serving axis; the return serves only after a hello
// at the new generation. An absent axis stays absent through both.
func TestACutPausesAndTheReturnHelloesTheNewGeneration(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("c1", 1))
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)

	// The same generation and same state is not a pause.
	h.conns.Put(connected("c1", 1))
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	if got := h.dial.count(); got != 1 {
		t.Fatalf("dial calls = %d, want 1 while serving", got)
	}

	h.conns.update("c1", func(c *farhelper.Connection) { c.State = hostconn.Stalled })
	h.want("c1", farhelper.StatePaused, farhelper.ReasonConnection, farhelper.ActionNone)

	// The return: a new generation. While the hello is pending the axis is no
	// longer paused, but it is not serving either.
	h.dial.gate = newGate()
	c := connected("c1", 2)
	c.State = hostconn.Reconnecting
	h.conns.Put(c)
	select {
	case <-h.dial.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the redial never started")
	}
	c.State = hostconn.Connected
	h.conns.Put(c)
	if status, _ := h.reg.Axis("c1"); status.State == farhelper.StateServing {
		t.Fatalf("serving before the hello completed: %+v", status)
	}
	h.dial.gate.release()
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	if got := h.dial.count(); got != 2 {
		t.Fatalf("dial calls = %d, want 2 (initial + return)", got)
	}
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d, want 1: a return redials, never reinstalls", got)
	}
}

func TestAnAbsentAxisStaysAbsentThroughACut(t *testing.T) {
	h := newHarness(t)
	h.modes.Set(machine, hostmode.Helper)
	h.consents.Set(machine, consent.Denied)
	h.conns.Put(connected("c1", 1))
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonConsentDenied, farhelper.ActionNone)

	h.conns.update("c1", func(c *farhelper.Connection) { c.State = hostconn.Stalled })
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonConsentDenied, farhelper.ActionNone)

	c := connected("c1", 2)
	c.State = hostconn.Reconnecting
	h.conns.Put(c)
	c.State = hostconn.Connected
	h.conns.Put(c)
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonConsentDenied, farhelper.ActionNone)
	if got := h.probe.count(); got != 0 {
		t.Fatalf("probe calls = %d, want 0 for a denied machine", got)
	}
}

// Scenario 4: the daemon dies with the transport up. The axis says helper-lost
// with refresh, the redial backs off, and refresh redials at once without
// installing anything.
func TestALostDaemonBacksOffAndRefreshes(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("c1", 1))
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	peer := h.dial.lastPeer(t)

	peer.Lose()
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonHelperLost, farhelper.ActionRefresh)

	// The first redial is one second away.
	h.dial.mu.Lock()
	h.dial.err = errors.New("still gone")
	h.dial.mu.Unlock()
	// helper-lost is published from inside the accept, before the entry arms
	// the redial's wait. Advancing in that window would deliver to no waiter
	// and the redial would never start. Startup arms no wait, so the first
	// registration is the redial's one-second wait.
	waittest.WaitFor(t, "the first redial to be armed", func() bool {
		return h.clock.afterRegistrations() >= 1
	})
	h.clock.Advance(time.Second)
	waittest.WaitFor(t, "the first redial", func() bool { return h.dial.count() == 2 })
	// The attempt failed; wait for the entry to apply the failure and arm its
	// next wait, so Refresh below is not racing that result. A dial returning
	// is not this moment: the result is still in flight, and a Refresh that
	// overtook it would have its reset overwritten by the late failure, which
	// re-arms a wait this test never advances. The loss armed the first wait
	// and the advance above consumed it, so the second is the failure's own.
	waittest.WaitForDetail(t, "the failed redial to arm its next wait", func() string {
		st, _ := h.reg.Axis("c1")
		return fmt.Sprintf("afters=%d dials=%d axis=%+v", h.clock.afterRegistrations(), h.dial.count(), st)
	}, func() bool {
		return h.clock.afterRegistrations() >= 2
	})
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d, want 1: a redial never installs", got)
	}

	// Refresh redials at once, without waiting for the backoff. The doubling
	// schedule itself is asserted by TestTheRedialSchedule in backoff_test.go.
	if err := h.reg.Refresh(context.Background(), "c1"); err != nil {
		t.Fatalf("refresh c1: %v", err)
	}
	waittest.WaitFor(t, "the refresh redial", func() bool { return h.dial.count() == 3 })
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d after refresh, want 1", got)
	}

	// A daemon that answers again serves. The refresh's own attempt carries
	// the failure too, so serving waits on the redial that failure arms, and
	// advancing the clock is what starts it. The schedule itself is not
	// asserted here.
	h.dial.mu.Lock()
	h.dial.err = nil
	h.dial.mu.Unlock()
	waittest.WaitForDetail(t, "axis c1 to be serving", func() string {
		status, ok := h.reg.Axis("c1")
		return fmt.Sprintf("axis is %+v (known=%v)", status, ok)
	}, func() bool {
		status, ok := h.reg.Axis("c1")
		if ok && status.State == farhelper.StateServing && status.Reason == "" && status.Action == farhelper.ActionNone {
			return true
		}
		h.clock.Advance(30 * time.Second)
		return false
	})
}

func TestAnInstallFailureOffersNoRetry(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.install.err = errors.New("the host refused the write")
	h.conns.Put(connected("c1", 1))
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonInstallFailed, farhelper.ActionNone)

	if err := h.reg.Refresh(context.Background(), "c1"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d after refresh, want 1", got)
	}
	if got := h.dial.count(); got != 0 {
		t.Fatalf("dial calls = %d, want 0", got)
	}
}

// Scenario 5: a result tagged with a replaced transport generation is dropped.
func TestAResultFromAnOldGenerationIsDropped(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.probe.gate = newGate()
	h.probe.ignoreCancel = true
	h.conns.Put(connected("c1", 1))
	select {
	case <-h.probe.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first probe never started")
	}
	// A new link while the first decision is in flight.
	h.conns.Put(connected("c1", 2))
	h.probe.gate.release()
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)

	installs := h.install.installedTargets()
	if len(installs) != 1 {
		t.Fatalf("install calls = %d, want 1", len(installs))
	}
	if installs[0].Generation != 2 {
		t.Fatalf("the install ran under generation %d, want 2: the old result was published", installs[0].Generation)
	}
	if got := h.probe.count(); got != 2 {
		t.Fatalf("probe calls = %d, want 2 (one per generation)", got)
	}
}

// Scenario 6: the mode changes to raw while serving. The axis turns
// unavailable on the connection and the client is closed; nothing is installed.
func TestModeRawWhileServingClosesTheClient(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("c1", 1))
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	peer := h.dial.lastPeer(t)

	h.modes.Set(machine, hostmode.Raw)
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonModeRaw, farhelper.ActionHostSettings)
	waittest.WaitFor(t, "the client to close", peer.wasClosed)
	if got := h.probe.count(); got != 1 {
		t.Fatalf("probe calls = %d, want 1: a mode change runs nothing remotely", got)
	}
}

// Scenario 7: a coordinator restart rebuilds an entry for every connection the
// local helper reports, a zero-pane one included, and reads nothing from disk.
func TestARestartRebuildsEveryReportedConnection(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	// Both are already in the source before the registry exists, as they are
	// after a coordinator restart.
	h.conns.conns["with-pane"] = connected("with-pane", 1)
	zero := connected("zero-pane", 1)
	zero.Shells = 0
	h.conns.conns["zero-pane"] = zero

	// A fresh registry over the same source is the restart.
	h.reg.Close()
	h.reg = farhelper.New(farhelper.Config{
		Connections: h.conns, Modes: h.modes, Consents: h.consents,
		Probe: h.probe, Install: h.install, Dial: h.dial,
		Clock: h.clock, Log: testLogger(t),
	})
	h.want("with-pane", farhelper.StateServing, "", farhelper.ActionNone)
	status, ok := h.reg.Axis("zero-pane")
	if !ok {
		t.Fatal("the zero-pane connection was not rebuilt")
	}
	if status.State != farhelper.StateStarting {
		t.Fatalf("zero-pane axis = %+v, want starting (no shell yet)", status)
	}
	if got := h.probe.count(); got != 2 {
		t.Fatalf("probe calls = %d, want 2 (one per connection)", got)
	}
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d, want 1: the zero-pane one is gated by C9", got)
	}

	// The normal pair: once a shell exists on it, it decides like any other.
	h.conns.update("zero-pane", func(c *farhelper.Connection) { c.Shells = 1 })
	h.want("zero-pane", farhelper.StateServing, "", farhelper.ActionNone)
}

// Scenario 10: the mode changes to raw while an install is held pending. The
// work is cancelled, its late answer is dropped, and serving is never published.
func TestModeRawWhileAnInstallIsPendingCancelsTheWork(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.install.gate = newGate()
	h.install.ignoreCancel = true
	h.conns.Put(connected("c1", 1))
	select {
	case <-h.install.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the install never started")
	}
	h.modes.Set(machine, hostmode.Raw)
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonModeRaw, farhelper.ActionHostSettings)
	h.install.gate.release()
	select {
	case <-h.install.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the install never finished")
	}
	if got := h.dial.count(); got != 0 {
		t.Fatalf("dial calls = %d, want 0: the late install was dropped", got)
	}
	if h.sawState(farhelper.StateServing) {
		t.Fatal("serving was published after the mode left helper")
	}
}

func TestModeRawWhileAHelloIsPendingCancelsTheWork(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.dial.gate = newGate()
	h.dial.ignoreCancel = true
	h.conns.Put(connected("c1", 1))
	select {
	case <-h.dial.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial never started")
	}
	h.modes.Set(machine, hostmode.Raw)
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonModeRaw, farhelper.ActionHostSettings)
	h.dial.gate.release()
	waittest.WaitFor(t, "the late hello", func() bool { return h.dial.peerCount() >= 1 })
	if h.sawState(farhelper.StateServing) {
		t.Fatal("serving was published for a hello begun before the mode changed")
	}
	// The client the late hello answered is closed, not kept.
	peer := h.dial.lastPeer(t)
	waittest.WaitFor(t, "the late client to close", peer.wasClosed)
}

// D25: uninstall closes the clients first, and a revoked consent after the
// removal leaves the axis consent-pending rather than reinstalling.
func TestUninstallClosesClientsAndRevokedConsentInstallsNothing(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("c1", 1))
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	peer := h.dial.lastPeer(t)

	h.reg.CloseFor(machine)
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonHelperLost, farhelper.ActionNone)
	waittest.WaitFor(t, "the client to close before removal", peer.wasClosed)

	// While frozen nothing redials, however long the clock runs.
	h.clock.Advance(10 * time.Minute)
	if got := h.dial.count(); got != 1 {
		t.Fatalf("dial calls = %d while closing, want 1", got)
	}

	// The removal revoked consent; the finish finds no answer.
	h.consents.Revoke(machine)
	h.reg.FinishFor(machine)
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonConsentPending, farhelper.ActionNone)
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d after the removal, want 1", got)
	}
}

func TestAFinishedUninstallWithConsentStillGrantedRedials(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("c1", 1))
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)

	h.reg.CloseFor(machine)
	h.want("c1", farhelper.StateUnavailable, farhelper.ReasonHelperLost, farhelper.ActionNone)
	h.reg.FinishFor(machine)
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	if got := h.install.count(); got != 1 {
		t.Fatalf("install calls = %d, want 1: a finished close redials the known build", got)
	}
}

// Acquire waits while a decision is starting, answers a lease once it serves,
// and refuses with the axis's own reason afterwards.
func TestAcquireWaitsForTheDecisionAndRefusesWithItsReason(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.probe.gate = newGate()
	h.conns.Put(connected("c1", 1))
	select {
	case <-h.probe.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the decision never started")
	}

	started := make(chan struct{})
	type answer struct {
		lease *farhelper.Lease
		err   error
	}
	got := make(chan answer, 1)
	go func() {
		close(started)
		lease, err := h.reg.Acquire(context.Background(), "c1")
		got <- answer{lease: lease, err: err}
	}()
	<-started
	// The decision is still blocked in its probe, so Acquire must be waiting,
	// not answering a starting axis with a reason the wire does not have.
	select {
	case a := <-got:
		t.Fatalf("Acquire answered while the axis was starting: lease=%v err=%v", a.lease, a.err)
	default:
	}
	h.probe.gate.release()
	h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	select {
	case a := <-got:
		if a.err != nil || a.lease == nil {
			t.Fatalf("Acquire after serving = %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire never answered after the axis served")
	}

	// A pause refuses with the connection reason, which is the wire's word for
	// "the transport is not connected".
	h.conns.update("c1", func(c *farhelper.Connection) { c.State = hostconn.Stalled })
	h.want("c1", farhelper.StatePaused, farhelper.ReasonConnection, farhelper.ActionNone)
	_, err := h.reg.Acquire(context.Background(), "c1")
	var unavailable *farhelper.Unavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != farhelper.ReasonConnection {
		t.Fatalf("Acquire while paused = %v, want *Unavailable/connection", err)
	}

	// An unknown connection is refused, not panicked on.
	if _, err := h.reg.Acquire(context.Background(), "never-seen"); !errors.As(err, &unavailable) {
		t.Fatalf("Acquire on an unknown connection = %v", err)
	}
}

// A mode or consent that returns to helper is a new decision, not a verdict
// that stuck: the axis leaves the input-derived absence and decides again.
func TestModeAndConsentReturningToHelperDecideAgain(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		h := newHarness(t)
		h.granted(machine)
		h.conns.Put(connected("c1", 1))
		h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)

		h.modes.Set(machine, hostmode.Raw)
		h.want("c1", farhelper.StateUnavailable, farhelper.ReasonModeRaw, farhelper.ActionHostSettings)
		h.modes.Set(machine, hostmode.Helper)
		h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
		if got := h.install.count(); got != 1 {
			t.Fatalf("install calls = %d, want 1: the build is still installed", got)
		}
		if got := h.dial.count(); got != 2 {
			t.Fatalf("dial calls = %d, want 2 (initial + return to helper)", got)
		}
	})

	t.Run("consent", func(t *testing.T) {
		h := newHarness(t)
		h.modes.Set(machine, hostmode.Helper)
		h.consents.Set(machine, consent.Denied)
		h.conns.Put(connected("c1", 1))
		h.want("c1", farhelper.StateUnavailable, farhelper.ReasonConsentDenied, farhelper.ActionNone)

		h.consents.Set(machine, consent.Granted)
		h.want("c1", farhelper.StateServing, "", farhelper.ActionNone)
	})
}

func TestABuildAlreadyOnTheHostServesWithoutAShell(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(fmt.Sprintf("slow=%v", slow), func(t *testing.T) {
			h := newHarness(t)
			h.granted(machine)
			h.install.found = true
			c := connected("installed", 1)
			c.Shells = 0
			c.Slow = slow
			h.conns.Put(c)
			h.want("installed", farhelper.StateServing, "", farhelper.ActionNone)
			if got := h.install.count(); got != 0 {
				t.Fatalf("Install calls = %d, want 0", got)
			}
		})
	}
}

func TestAFindErrorDoesNotInstall(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.install.findErr = errors.New("read failed")
	h.conns.Put(connected("find-error", 1))
	waittest.WaitFor(t, "Find attempted", func() bool { return h.install.findCount() > 0 })
	status, ok := h.reg.Axis("find-error")
	if !ok || status.State == farhelper.StateUnavailable && status.Reason == farhelper.ReasonInstallFailed {
		t.Fatalf("Find failure became install failure: %+v", status)
	}
	if h.install.count() != 0 {
		t.Fatalf("Install calls = %d, want 0", h.install.count())
	}
}

func TestReviewFailedHelloAfterServingRedials(t *testing.T) {
	h := newHarness(t)
	h.granted(machine)
	h.conns.Put(connected("failed-hello", 1))
	h.want("failed-hello", farhelper.StateServing, "", farhelper.ActionNone)
	peer := h.dial.lastPeer(t)
	peer.Lose()
	h.want("failed-hello", farhelper.StateUnavailable, farhelper.ReasonHelperLost, farhelper.ActionRefresh)
	h.dial.mu.Lock()
	h.dial.err = fmt.Errorf("%w: sentinel", farhelper.ErrExecForbidden)
	h.dial.mu.Unlock()
	h.clock.Advance(time.Second)
	waittest.WaitFor(t, "failed redial", func() bool { return h.dial.count() == 2 })
	select {
	case <-h.dial.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("failed hello did not return")
	}
	h.want("failed-hello", farhelper.StateUnavailable, farhelper.ReasonHelperLost, farhelper.ActionRefresh)
}
