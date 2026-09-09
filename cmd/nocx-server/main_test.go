package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/app"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/coordinator"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/version"
)

type stubPTYFactory struct{ stub pty.Pty }

func (f *stubPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	return f.stub, nil
}

// TestTheSocketHandsBackWhatTheTransportMinted is the assertion this
// binary exists for, and it is not provable one layer down: the coordinator
// package's own tests hand it a fake backend, so they can only show it
// reports whatever it was given. Here the token is one a real WSServer
// minted for a real launch, and the socket is the only route by which
// anything learns it (design §6).
func TestTheSocketHandsBackWhatTheTransportMinted(t *testing.T) {
	logger := nocxlog.NewSlogAdapter(nil)
	reg := session.New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})
	ws := transport.NewWSServer(logger, reg)

	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("transport Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	dir, err := os.MkdirTemp("", "nocxsrv")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	socket, err := coordinator.NewServer(coordinator.Config{
		Dir:     filepath.Join(dir, "run"),
		Build:   coordinator.Build{Version: version.Version, Commit: version.Commit},
		Backend: backend{ws: ws},
		Peers:   coordinator.SystemPeerCredentials{},
		Owner:   coordinator.SystemPathOwner{},
		SelfUID: coordinator.SelfUID(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if startErr := socket.Start(); startErr != nil {
		t.Fatalf("socket Start: %v", startErr)
	}
	defer func() { _ = socket.Close() }()

	conn, err := net.Dial("unix", socket.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if encErr := json.NewEncoder(conn).Encode(coordinator.Request{
		Type:   coordinator.RequestHello,
		Client: &coordinator.ClientIdentity{Version: "test", Protocol: coordinator.ProtocolVersion},
	}); encErr != nil {
		t.Fatalf("encode: %v", encErr)
	}
	var resp coordinator.Response
	if decErr := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); decErr != nil {
		t.Fatalf("decode: %v", decErr)
	}

	if resp.Hello == nil {
		t.Fatalf("no hello: %+v", resp)
	}
	if resp.Hello.WSToken == "" {
		t.Fatal("the hello carried an empty token")
	}
	if resp.Hello.WSToken != ws.Token() {
		t.Errorf("hello token = %q, want the minted %q", resp.Hello.WSToken, ws.Token())
	}
	if resp.Hello.WSAddress != ws.Addr() {
		t.Errorf("hello address = %q, want the bound %q", resp.Hello.WSAddress, ws.Addr())
	}
	if resp.Hello.Build.Version != version.Version {
		t.Errorf("hello version = %q, want %q", resp.Hello.Build.Version, version.Version)
	}
	if resp.Hello.Build.Commit != version.Commit {
		t.Errorf("hello commit = %q, want %q", resp.Hello.Build.Commit, version.Commit)
	}
	if resp.Hello.Protocol != coordinator.ProtocolVersion {
		t.Errorf("hello protocol = %d, want %d", resp.Hello.Protocol, coordinator.ProtocolVersion)
	}

	// And the address it named is one a client can actually reach.
	tcp, err := net.Dial("tcp", resp.Hello.WSAddress)
	if err != nil {
		t.Fatalf("dial the advertised address %s: %v", resp.Hello.WSAddress, err)
	}
	_ = tcp.Close()
}

// The adapter is two lines and both of them are a wiring decision: a
// transposed pair here would hand every client the token as an address.
func TestBackendAdapterReportsTheTransportsOwnFacts(t *testing.T) {
	fake := fakeWS{addr: "127.0.0.1:9", token: "t0ken"}
	b := backend{ws: fake}
	if got := b.WSAddress(); got != fake.addr {
		t.Errorf("WSAddress() = %q, want %q", got, fake.addr)
	}
	if got := b.WSToken(); got != fake.token {
		t.Errorf("WSToken() = %q, want %q", got, fake.token)
	}
}

type fakeWS struct{ addr, token string }

func (f fakeWS) Addr() string  { return f.addr }
func (f fakeWS) Token() string { return f.token }

type workerTestAuthorizer struct {
	invocation assistant.ToolInvocation
}

func (a workerTestAuthorizer) Admit(toolendpoint.Peer) (assistant.ToolInvocation, func(), error) {
	return a.invocation, func() {}, nil
}

type workerTestDispatcher struct {
	result  string
	err     error
	catalog []agenttools.Tool
}

func (d workerTestDispatcher) Dispatch(assistant.ToolInvocation) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	return d.result, nil
}

// Catalogue is required at composition, not at call time: toolendpoint.New
// refuses a dispatcher that cannot enumerate what a grant admits, so a stub
// without it does not stand in for the real dispatcher at all.
func (d workerTestDispatcher) Catalogue(content.Grant) []agenttools.Tool {
	return d.catalog
}

type workerTestPeers struct{}

func (workerTestPeers) PeerUID(*net.UnixConn) (uint32, error) {
	return coordinator.SelfUID(), nil
}

func TestGroupEndpointWiringPublishesAndServesHoldings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	appRoot := &app.App{
		ToolAuthorizer: workerTestAuthorizer{
			invocation: assistant.ToolInvocation{},
		},
		ToolDispatcher: workerTestDispatcher{result: `{"held":[{"id":"p-1"}]}`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	endpoint, err := startToolEndpoint(
		appRoot, dir, workerTestPeers{}, coordinator.SystemPathOwner{},
		coordinator.SelfUID(), logger,
	)
	if err != nil {
		t.Fatalf("start worker endpoint: %v", err)
	}
	if endpoint == nil {
		t.Fatal("startToolEndpoint returned nil for a composed authorizer and dispatcher")
	}

	socketPath := filepath.Join(dir, "tool.sock")
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat worker socket: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("worker socket mode = %o, want 600", info.Mode().Perm())
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial worker socket: %v", err)
	}
	request := `{"jsonrpc":"2.0","id":"holdings-1","method":"workers.holdings","params":{}}` + "\n"
	if _, err := io.WriteString(conn, request); err != nil {
		_ = conn.Close()
		t.Fatalf("write worker request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var response struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		_ = conn.Close()
		t.Fatalf("decode worker response: %v", err)
	}
	_ = conn.Close()
	if len(response.Error) != 0 && string(response.Error) != "null" {
		t.Fatalf("worker holdings error: %s", response.Error)
	}
	if string(response.ID) != `"holdings-1"` || string(response.Result) != `{"held":[{"id":"p-1"}]}` {
		t.Fatalf("worker holdings response = id %s result %s", response.ID, response.Result)
	}

	if err := endpoint.Close(); err != nil {
		t.Fatalf("close worker endpoint: %v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("worker socket after close: err = %v, want not exists", err)
	}
}

func TestGroupEndpointWiringRefusesToPublishWithoutAuthorizer(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	appRoot := &app.App{
		ToolDispatcher: workerTestDispatcher{result: `{"held":[]}`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	endpoint, err := startToolEndpoint(
		appRoot, dir, workerTestPeers{}, coordinator.SystemPathOwner{},
		coordinator.SelfUID(), logger,
	)
	if err != nil {
		t.Fatalf("start worker endpoint without authorizer: %v", err)
	}
	if endpoint != nil {
		_ = endpoint.Close()
		t.Fatal("startToolEndpoint published an endpoint without an authorizer")
	}
	if _, err := os.Stat(filepath.Join(dir, "tool.sock")); !os.IsNotExist(err) {
		t.Fatalf("worker socket without authorizer: err = %v, want not exists", err)
	}
}

// A DISPATCH FAILURE IS READABLE IN THE BACKEND'S LOG FILE (nocx-halpn).
//
// It was not. This process wrote through two loggers — the app's, which opens
// nocx.log, and cmd/nocx-server's own, which writes to os.Stderr — and
// internal/coordinator/spawn.go gives the daemon it launches a nil Stderr,
// which os/exec makes /dev/null. So the tool endpoint's diagnostic for an
// unclassified failure, the one line nocx-1w3my added for exactly this case,
// was discarded in the shipped product. The dev stand kept it only because
// scripts/dev-web.sh redirects into a temp file.
//
// The assertion is on the FILE and not on a logger having been called: a
// spy would have passed the whole time this was broken.
func TestAnUnclassifiedDispatchFailureIsReadableInTheLogFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	logPath := filepath.Join(t.TempDir(), "nocx.log")
	logFile, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // the path is this test's temp dir
	if openErr != nil {
		t.Fatalf("open log file: %v", openErr)
	}
	defer func() { _ = logFile.Close() }()

	appRoot := &app.App{
		ToolAuthorizer: workerTestAuthorizer{invocation: assistant.ToolInvocation{}},
		ToolDispatcher: workerTestDispatcher{err: errors.New("the participant's session refused its first line")},
	}
	endpoint, err := startToolEndpoint(
		appRoot, dir, workerTestPeers{}, coordinator.SystemPathOwner{},
		coordinator.SelfUID(), slog.New(slog.NewTextHandler(logFile, nil)),
	)
	if err != nil {
		t.Fatalf("start worker endpoint: %v", err)
	}
	defer func() { _ = endpoint.Close() }()

	conn, err := net.Dial("unix", filepath.Join(dir, "tool.sock"))
	if err != nil {
		t.Fatalf("dial worker socket: %v", err)
	}
	request := `{"jsonrpc":"2.0","id":"spawn-1","method":"workers.spawn","params":{}}` + "\n"
	if _, err := io.WriteString(conn, request); err != nil {
		_ = conn.Close()
		t.Fatalf("write worker request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		_ = conn.Close()
		t.Fatalf("decode worker response: %v", err)
	}
	_ = conn.Close()
	// The wire stays generic on purpose — that half is nocx-1w3my's — and it
	// is what makes the log the only place the cause can be read.
	if response.Error.Message != "internal error" {
		t.Fatalf("wire answer = %q, want the generic sentence", response.Error.Message)
	}

	written, readErr := os.ReadFile(logPath) //nolint:gosec // the path is this test's temp dir
	if readErr != nil {
		t.Fatalf("read log file: %v", readErr)
	}
	out := string(written)
	for _, want := range []string{
		"unclassified dispatch failure",
		"workers.spawn",
		"the participant's session refused its first line",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log file does not carry %q:\n%s", want, out)
		}
	}
}
