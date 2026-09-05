package waveendpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/coordinator"
)

type testPeers struct {
	uid uint32
	pid int
	err error
}

func (p testPeers) PeerUID(*net.UnixConn) (uint32, error) { return p.uid, p.err }
func (p testPeers) PeerPID(*net.UnixConn) (int, error)    { return p.pid, p.err }

type testOwner struct{ uid uint32 }

func (o testOwner) OwnerUID(string) (uint32, error) { return o.uid, nil }

type testContextKey struct{}

type testAuthorizer struct {
	mu      sync.Mutex
	peer    Peer
	calls   int
	inv     assistant.WaveInvocation
	err     error
	release func()
	invoked chan struct{}
}

func (a *testAuthorizer) Admit(peer Peer) (assistant.WaveInvocation, func(), error) {
	a.mu.Lock()
	a.peer = peer
	a.calls++
	inv := a.inv
	err := a.err
	release := a.release
	a.mu.Unlock()
	if a.invoked != nil {
		select {
		case a.invoked <- struct{}{}:
		default:
		}
	}
	if err == nil && release == nil {
		release = func() {}
	}
	return inv, release, err
}

func (a *testAuthorizer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

type testDispatcher struct {
	mu        sync.Mutex
	calls     []assistant.WaveInvocation
	out       string
	err       error
	started   chan struct{}
	cancelled chan struct{}
	release   <-chan struct{}
}

func (d *testDispatcher) Dispatch(inv assistant.WaveInvocation) (string, error) {
	d.mu.Lock()
	d.calls = append(d.calls, inv)
	out, err := d.out, d.err
	d.mu.Unlock()
	if d.started != nil {
		close(d.started)
		select {
		case <-inv.Context.Done():
			close(d.cancelled)
		case <-d.release:
		}
		<-d.release
	}
	return out, err
}

func (d *testDispatcher) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func endpointConfig(t *testing.T, auth *testAuthorizer, dispatch *testDispatcher) Config {
	t.Helper()
	return Config{
		Dir:      t.TempDir(),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatch,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func startEndpoint(t *testing.T, cfg Config) *Endpoint {
	t.Helper()
	ep, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := ep.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	return ep
}

func readResponse(t *testing.T, conn net.Conn) rpcResponse {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var response rpcResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	return response
}

func dialEndpoint(t *testing.T, ep *Endpoint) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", ep.SocketPath())
	if err != nil {
		t.Fatalf("dial endpoint: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestNewRejectsMissingAuthorizerOrDispatcher(t *testing.T) {
	dispatch := &testDispatcher{}
	auth := &testAuthorizer{}
	for name, cfg := range map[string]Config{
		"authorizer": endpointConfig(t, nil, dispatch),
		"dispatcher": endpointConfig(t, auth, nil),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("New() succeeded with a nil required dependency")
			}
		})
	}
}

func TestEndpointDispatchesWithAuthorizerInvocationAndAtomicSocket(t *testing.T) {
	auth := &testAuthorizer{inv: assistant.WaveInvocation{Context: context.WithValue(context.Background(), testContextKey{}, "bound")}}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	ep := startEndpoint(t, endpointConfig(t, auth, dispatch))

	info, err := os.Stat(ep.SocketPath())
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o, want 600", info.Mode().Perm())
	}

	conn := dialEndpoint(t, ep)
	request := `{"jsonrpc":"2.0","id":"request-1","method":"wave.holdings","params":{"sessionId":"some-other-session"}}` + "\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error != nil {
		t.Fatalf("response error = %+v", response.Error)
	}
	if string(response.ID) != `"request-1"` || string(response.Result) != `{"held":[]}` {
		t.Fatalf("response = %+v, want request id and result", response)
	}
	if dispatch.callCount() != 1 {
		t.Fatalf("dispatch calls = %d, want 1", dispatch.callCount())
	}
	dispatch.mu.Lock()
	inv := dispatch.calls[0]
	dispatch.mu.Unlock()
	if inv.Method != "wave.holdings" || string(inv.RawParams) != `{"sessionId":"some-other-session"}` {
		t.Fatalf("invocation = %+v, want method and untouched params", inv)
	}
	if inv.Context == nil || inv.Context.Value(testContextKey{}) != "bound" {
		t.Fatal("endpoint did not preserve authorizer context")
	}
	auth.mu.Lock()
	gotPeer := auth.peer
	auth.mu.Unlock()
	if gotPeer != (Peer{UID: 1000, PID: 1234}) {
		t.Fatalf("authorizer peer = %+v, want uid and pid", gotPeer)
	}
}

func TestEndpointCloseWaitsForAHandlerAfterReadError(t *testing.T) {
	releaseHandler := make(chan struct{})
	dispatchStarted := make(chan struct{})
	dispatchCanceled := make(chan struct{})
	dispatch := &testDispatcher{
		out:       `{"held":[]}`,
		started:   dispatchStarted,
		cancelled: dispatchCanceled,
		release:   releaseHandler,
	}
	releaseAdmission := make(chan struct{})
	auth := &testAuthorizer{release: func() { close(releaseAdmission) }}
	ep := startEndpoint(t, endpointConfig(t, auth, dispatch))
	conn := dialEndpoint(t, ep)
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"wave.holdings","params":{}}`+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	select {
	case <-dispatchStarted:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not start")
	}

	_ = conn.Close()
	select {
	case <-dispatchCanceled:
	case <-time.After(time.Second):
		t.Fatal("read error did not cancel the in-flight handler")
	}
	select {
	case <-releaseAdmission:
		t.Fatal("admission released before the in-flight handler settled")
	default:
	}
	ep.mu.Lock()
	tracked := len(ep.conns)
	ep.mu.Unlock()
	if tracked == 0 {
		t.Fatal("endpoint stopped tracking the connection before its handler settled")
	}

	closed := make(chan struct{})
	go func() {
		_ = ep.Close()
		close(closed)
	}()
	close(releaseHandler)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("endpoint Close did not wait for the handler to settle")
	}
	select {
	case <-releaseAdmission:
	case <-time.After(time.Second):
		t.Fatal("admission release did not follow handler settlement")
	}
}

func TestEndpointRefusesMalformedOversizedUnknownAndNotification(t *testing.T) {
	tests := []struct {
		name string
		line string
		code int
	}{
		{name: "malformed", line: `{"jsonrpc":"2.0","id":1,"method":`, code: -32700},
		{name: "oversized", line: `{"jsonrpc":"2.0","id":1,"method":"wave.holdings","params":{"x":"` + strings.Repeat("x", 1<<20) + `"}}"`, code: -32600},
		{name: "unknown method", line: `{"jsonrpc":"2.0","id":1,"method":"wave.nope","params":{}}`, code: -32601},
		{name: "notification", line: `{"jsonrpc":"2.0","method":"wave.holdings","params":{}}`, code: -32600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth := &testAuthorizer{}
			dispatch := &testDispatcher{err: assistant.ErrUnknownMethod}
			ep := startEndpoint(t, endpointConfig(t, auth, dispatch))
			conn := dialEndpoint(t, ep)
			if _, err := io.WriteString(conn, tc.line+"\n"); err != nil && tc.name != "oversized" {
				t.Fatalf("write request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error == nil || response.Error.Code != tc.code {
				t.Fatalf("response error = %+v, want code %d", response.Error, tc.code)
			}
			if tc.name != "unknown method" && dispatch.callCount() != 0 {
				t.Fatalf("dispatch calls = %d, want 0", dispatch.callCount())
			}
			if tc.name == "unknown method" && dispatch.callCount() != 1 {
				t.Fatalf("dispatch calls = %d, want 1", dispatch.callCount())
			}
		})
	}
}

func TestEndpointMapsDispatcherRefusalsToJSONRPC(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{name: "invalid params", err: assistant.ErrInvalidParams, code: -32602},
		{name: "unreachable", err: assistant.ErrUnreachableMethod, code: -32000},
		{name: "invalid result", err: assistant.ErrInvalidResult, code: -32000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatch := &testDispatcher{err: tc.err}
			ep := startEndpoint(t, endpointConfig(t, &testAuthorizer{}, dispatch))
			conn := dialEndpoint(t, ep)
			if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"wave.holdings","params":{}}`+"\n"); err != nil {
				t.Fatalf("write request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error == nil || response.Error.Code != tc.code {
				t.Fatalf("response error = %+v, want code %d", response.Error, tc.code)
			}
		})
	}
}

func TestEndpointRefusesPeerBeforeParsing(t *testing.T) {
	cases := []struct {
		name   string
		peer   testPeers
		reason string
	}{
		{name: "credentials unavailable", peer: testPeers{err: errors.New("no credentials")}, reason: "peer credentials unavailable"},
		{name: "foreign uid", peer: testPeers{uid: 2000, pid: 1234}, reason: "peer uid is not permitted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &testAuthorizer{}
			dispatch := &testDispatcher{}
			cfg := endpointConfig(t, auth, dispatch)
			cfg.Peers = tc.peer
			ep := startEndpoint(t, cfg)
			conn := dialEndpoint(t, ep)
			if _, err := io.WriteString(conn, `not JSON and contains secret task text`+"\n"); err != nil && tc.name != "foreign uid" {
				t.Fatalf("write request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error == nil || !strings.Contains(response.Error.Message, tc.reason) {
				t.Fatalf("response error = %+v, want %q", response.Error, tc.reason)
			}
			if auth.callCount() != 0 || dispatch.callCount() != 0 {
				t.Fatalf("peer refusal reached auth=%d or dispatch=%d", auth.callCount(), dispatch.callCount())
			}
		})
	}
}

func TestEndpointDoesNotLogRequestPayloads(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{err: assistant.ErrInvalidParams}
	cfg := endpointConfig(t, auth, dispatch)
	cfg.Logger = logger
	ep := startEndpoint(t, cfg)
	conn := dialEndpoint(t, ep)
	secret := "task secret and message body"
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"wave.holdings","params":{"message":"`+secret+`"}}`+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = readResponse(t, conn)
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("log contains request payload: %q", logs.String())
	}
}

func TestEndpointMapsAuthorizerRefusalWithoutDispatch(t *testing.T) {
	auth := &testAuthorizer{err: ErrNotEnrolled}
	dispatch := &testDispatcher{}
	ep := startEndpoint(t, endpointConfig(t, auth, dispatch))
	conn := dialEndpoint(t, ep)
	_, _ = io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"wave.holdings","params":{}}`+"\n")
	response := readResponse(t, conn)
	if response.Error == nil || response.Error.Code != rpcPeerRefused {
		t.Fatalf("response error = %+v, want peer refusal", response.Error)
	}
	if dispatch.callCount() != 0 {
		t.Fatalf("dispatch calls = %d, want 0", dispatch.callCount())
	}
}

var (
	_ coordinator.PeerCredentials = testPeers{}
	_ coordinator.PeerProcess     = testPeers{}
)
