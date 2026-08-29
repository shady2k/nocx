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

	"github.com/shady2k/nocx/internal/coordinator"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
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
