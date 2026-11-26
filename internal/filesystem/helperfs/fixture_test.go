package helperfs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem/hostsvc"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/log/logtest"
)

const (
	panelID    = "panel"
	peerHash   = "fixture-hash"
	testBudget = 10 * time.Second
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testBudget)
	t.Cleanup(cancel)
	return ctx
}

func receive[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(testBudget):
		t.Fatal("timed out waiting for fixture event")
		var zero T
		return zero
	}
}

func tempTree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-owned file.
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sameHostService(t *testing.T, p *local.Provider, journal, instance string) *hostsvc.Service {
	t.Helper()
	s, err := hostsvc.New(p, hostsvc.JournalConfig{Root: journal, Hostname: "fixture", InstanceID: instance})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("closing fs service: %v", err)
		}
	})
	return s
}

func newHostService(t *testing.T, p *local.Provider) *hostsvc.Service {
	t.Helper()
	return sameHostService(t, p, t.TempDir(), strings.Repeat("a", 32))
}

// fsObserver records logical RPCs at the real host's service boundary: a
// chunked response may have several frames but is still one request.
type fsObserver struct {
	*hostsvc.Service
	mu        sync.Mutex
	calls     []rpc
	intercept func(context.Context, string, json.RawMessage, func() (any, error)) (any, error)
}

type rpc struct {
	op     string
	params json.RawMessage
}

func (s *fsObserver) Call(ctx context.Context, op string, p json.RawMessage) (any, error) {
	s.mu.Lock()
	s.calls = append(s.calls, rpc{op: op, params: append(json.RawMessage(nil), p...)})
	s.mu.Unlock()
	if s.intercept != nil {
		return s.intercept(ctx, op, p, func() (any, error) { return s.Service.Call(ctx, op, p) })
	}
	return s.Service.Call(ctx, op, p)
}

func (s *fsObserver) observed() []rpc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]rpc(nil), s.calls...)
}

// pipeLane is only the transport seam. Both ends run their production
// handshake, framing, cancellation, and request implementations.
type pipeLane struct {
	send, reply *io.PipeWriter
	recv        *io.PipeReader
	lost        chan struct{}
	served      chan struct{}
	serveErr    error
}

func (l *pipeLane) Stdin() io.WriteCloser { return l.send }
func (l *pipeLane) Stdout() io.Reader     { return l.recv }
func (*pipeLane) Stderr() io.Reader       { return strings.NewReader("") }
func (*pipeLane) Start(string) error      { return nil }
func (l *pipeLane) Wait() (int, error)    { <-l.served; return 0, l.serveErr }
func (l *pipeLane) Done() <-chan struct{} { return l.lost }
func (*pipeLane) LostErr() error          { return io.ErrUnexpectedEOF }
func (l *pipeLane) Close() error          { return errors.Join(l.send.Close(), l.recv.Close()) }

func (l *pipeLane) dropReply() {
	_ = l.reply.CloseWithError(io.ErrUnexpectedEOF)
	close(l.lost)
}

func servePeer(t *testing.T, svc *fsObserver, instance string) (*client.Client, *pipeLane) {
	t.Helper()
	in, send := io.Pipe()
	recv, reply := io.Pipe()
	l := &pipeLane{send: send, reply: reply, recv: recv, lost: make(chan struct{}), served: make(chan struct{})}
	logger := logtest.Slog(t)
	h := host.New(in, reply, peerHash, instance, logger)
	h.Register(svc)
	go func() {
		l.serveErr = h.Serve(context.Background())
		_ = reply.Close()
		close(l.served)
	}()
	t.Cleanup(func() {
		_ = l.Close()
		receive(t, l.served)
	})
	c, err := client.Dial(testContext(t), client.Config{Exec: l, Command: "/fixture/helper", ExpectHash: peerHash, Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, l
}

type panelConnections struct {
	change func(farhelper.Connection)
}

func panelConnection(id string) farhelper.Connection {
	return farhelper.Connection{ID: id, HostKeyFingerprint: id, State: hostconn.Connected, Generation: 1, Shells: 1}
}
func (*panelConnections) List() []farhelper.Connection {
	return []farhelper.Connection{panelConnection(panelID)}
}
func (s *panelConnections) Watch(f func(farhelper.Connection)) func() { s.change = f; return func() {} }

type panelConsent struct {
	mu     sync.Mutex
	denied bool
	change func()
}

func (s *panelConsent) Lookup(string) (consent.Answer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.denied {
		return consent.Denied, true
	}
	return consent.Granted, true
}
func (s *panelConsent) Watch(f func()) func() { s.change = f; return func() {} }
func (s *panelConsent) deny() {
	s.mu.Lock()
	s.denied = true
	s.mu.Unlock()
	s.change()
}

type panelRemote struct {
	mu    sync.Mutex
	peers map[string][]*client.Client
}

func (*panelRemote) Probe(context.Context, farhelper.Target) (farhelper.Platform, error) {
	return farhelper.Platform{GOOS: "linux", GOARCH: "amd64"}, nil
}
func (*panelRemote) Find(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, bool, error) {
	return farhelper.Installed{Dir: "/fixture", Generation: peerHash}, true, nil
}
func (*panelRemote) Install(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, error) {
	return farhelper.Installed{}, errors.New("fixture already has a helper")
}
func (p *panelRemote) Dial(_ context.Context, target farhelper.Target, _ farhelper.Installed) (*client.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	peers := p.peers[target.ConnectionID]
	if len(peers) == 0 {
		return nil, errors.New("fixture has no next peer on this connection")
	}
	p.peers[target.ConnectionID] = peers[1:]
	return peers[0], nil
}

func registry(t *testing.T, clock hostconn.Clock, peers map[string][]*client.Client) (*farhelper.Registry, *panelConnections, *panelConsent) {
	t.Helper()
	conns, consents := &panelConnections{}, &panelConsent{}
	remote := &panelRemote{peers: peers}
	_, logger := logtest.New(t)
	r := farhelper.New(farhelper.Config{Connections: conns, Consents: consents, Probe: remote, Install: remote, Dial: remote, Clock: clock, Log: logger})
	t.Cleanup(r.Close)
	if _, err := r.Acquire(testContext(t), panelID); err != nil {
		t.Fatalf("initial lease: %v", err)
	}
	return r, conns, consents
}

func singlePeer(t *testing.T, svc *fsObserver) (*farhelper.Registry, *panelConsent) {
	t.Helper()
	c, _ := servePeer(t, svc, "instance-1")
	r, _, consent := registry(t, nil, map[string][]*client.Client{panelID: {c}})
	return r, consent
}
