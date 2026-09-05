package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/app"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/coordinator"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/version"
	"github.com/shady2k/nocx/internal/waveendpoint"
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

type waveTestAuthorizer struct {
	invocation assistant.WaveInvocation
}

func (a waveTestAuthorizer) Admit(waveendpoint.Peer) (assistant.WaveInvocation, func(), error) {
	return a.invocation, func() {}, nil
}

type waveTestDispatcher struct {
	result string
}

func (d waveTestDispatcher) Dispatch(assistant.WaveInvocation) (string, error) {
	return d.result, nil
}

type waveTestPeers struct{}

func (waveTestPeers) PeerUID(*net.UnixConn) (uint32, error) {
	return coordinator.SelfUID(), nil
}

func TestWaveEndpointWiringPublishesAndServesHoldings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	appRoot := &app.App{
		WaveAuthorizer: waveTestAuthorizer{
			invocation: assistant.WaveInvocation{},
		},
		WaveDispatcher: waveTestDispatcher{result: `{"held":[{"id":"p-1"}]}`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	endpoint, err := startWaveEndpoint(
		appRoot, dir, waveTestPeers{}, coordinator.SystemPathOwner{},
		coordinator.SelfUID(), logger,
	)
	if err != nil {
		t.Fatalf("start wave endpoint: %v", err)
	}
	if endpoint == nil {
		t.Fatal("startWaveEndpoint returned nil for a composed authorizer and dispatcher")
	}

	socketPath := filepath.Join(dir, "wave.sock")
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat wave socket: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("wave socket mode = %o, want 600", info.Mode().Perm())
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial wave socket: %v", err)
	}
	request := `{"jsonrpc":"2.0","id":"holdings-1","method":"wave.holdings","params":{}}` + "\n"
	if _, err := io.WriteString(conn, request); err != nil {
		_ = conn.Close()
		t.Fatalf("write wave request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var response struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		_ = conn.Close()
		t.Fatalf("decode wave response: %v", err)
	}
	_ = conn.Close()
	if len(response.Error) != 0 && string(response.Error) != "null" {
		t.Fatalf("wave holdings error: %s", response.Error)
	}
	if string(response.ID) != `"holdings-1"` || string(response.Result) != `{"held":[{"id":"p-1"}]}` {
		t.Fatalf("wave holdings response = id %s result %s", response.ID, response.Result)
	}

	if err := endpoint.Close(); err != nil {
		t.Fatalf("close wave endpoint: %v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("wave socket after close: err = %v, want not exists", err)
	}
}

func TestWaveEndpointWiringRefusesToPublishWithoutAuthorizer(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	appRoot := &app.App{
		WaveDispatcher: waveTestDispatcher{result: `{"held":[]}`},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	endpoint, err := startWaveEndpoint(
		appRoot, dir, waveTestPeers{}, coordinator.SystemPathOwner{},
		coordinator.SelfUID(), logger,
	)
	if err != nil {
		t.Fatalf("start wave endpoint without authorizer: %v", err)
	}
	if endpoint != nil {
		_ = endpoint.Close()
		t.Fatal("startWaveEndpoint published an endpoint without an authorizer")
	}
	if _, err := os.Stat(filepath.Join(dir, "wave.sock")); !os.IsNotExist(err) {
		t.Fatalf("wave socket without authorizer: err = %v, want not exists", err)
	}
}
