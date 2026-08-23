package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ---------------------------------------------------------------------------
// In-process SSH server — minimal: accepts the fixture key, answers pty-req,
// records every exec/shell request, and echoes writes back ("echo:" prefix).
// The launcher command is never executed: the assertion is what the session
// STARTED with, which is what the far host would run.
// ---------------------------------------------------------------------------

type reachSSHServer struct {
	t          *testing.T
	hostSigner gossh.Signer
	userKey    ed25519.PrivateKey
	listener   net.Listener
	addr       string

	mu         sync.Mutex
	execCmds   []string
	shellReqs  int
	shellReady chan struct{}
	readyOnce  sync.Once
}

func reachSigner(t *testing.T) (gossh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer, priv
}

func startReachSSHServer(t *testing.T) *reachSSHServer {
	t.Helper()
	hostSigner, _ := reachSigner(t)
	userSigner, userKey := reachSigner(t)

	config := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), userSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown public key")
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &reachSSHServer{
		t:          t,
		hostSigner: hostSigner,
		userKey:    userKey,
		listener:   listener,
		addr:       listener.Addr().String(),
		shellReady: make(chan struct{}),
	}
	go srv.acceptLoop(config)
	t.Cleanup(func() { _ = listener.Close() })
	return srv
}

func (s *reachSSHServer) acceptLoop(config *gossh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn, config)
	}
}

func (s *reachSSHServer) serveConn(conn net.Conn, config *gossh.ServerConfig) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, reqs, err := newChan.Accept()
		if err != nil {
			return
		}
		go s.handleSession(ch, reqs)
	}
	_ = sshConn.Close()
}

func (s *reachSSHServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	go func() {
		for req := range reqs {
			switch req.Type {
			case "pty-req":
				_ = req.Reply(true, nil)
			case "shell":
				_ = req.Reply(true, nil)
				s.mu.Lock()
				s.shellReqs++
				s.mu.Unlock()
				s.readyOnce.Do(func() { close(s.shellReady) })
			case "exec":
				var m struct{ Command string }
				if err := gossh.Unmarshal(req.Payload, &m); err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				s.mu.Lock()
				s.execCmds = append(s.execCmds, m.Command)
				s.mu.Unlock()
				s.readyOnce.Do(func() { close(s.shellReady) })
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()

	// Echo loop: every write comes back prefixed with "echo:" — the session
	// is usable only while this round-trips.
	buf := make([]byte, 4096)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			_, _ = ch.Write(append([]byte("echo:"), buf[:n]...))
		}
		if err != nil {
			return
		}
	}
}

func (s *reachSSHServer) waitForExec(t *testing.T, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		cmds := append([]string{}, s.execCmds...)
		s.mu.Unlock()
		if len(cmds) > 0 {
			return cmds[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the launcher's exec request on the test server")
	return ""
}

func (s *reachSSHServer) waitForShell(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-s.shellReady:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a plain-shell request on the test server")
	}
}

func (s *reachSSHServer) shellRequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shellReqs
}

func (s *reachSSHServer) execCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.execCmds)
}

// ---------------------------------------------------------------------------
// Fixture plumbing: client key file, known_hosts, resolvers.
// ---------------------------------------------------------------------------

func reachWriteKeyFile(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func reachWriteKnownHosts(t *testing.T, srv *reachSSHServer) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{srv.addr}, srv.hostSigner.PublicKey())
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// reachResolver is a no-op ssh.ConfigResolver: no aliases, no overrides, so
// the ConnectConfig the transport builds is exactly what the client uses.
type reachResolver struct{}

func (reachResolver) ResolveHost(_ context.Context, host string) (string, error) { return host, nil }

func (reachResolver) ResolveConfig(_ context.Context, host string) (*ssh.HostConfig, error) {
	return &ssh.HostConfig{HostName: host}, nil
}

func (reachResolver) ResolveArgv(_ context.Context, argv []string) (*ssh.HostConfig, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty argv")
	}
	return &ssh.HostConfig{HostName: argv[len(argv)-1]}, nil
}

// reachProfileResolver stands in for the profile resolver the transport
// consults: it returns the test server as the target with the fixture key.
type reachProfileResolver struct {
	host    string
	keyFile string
}

func (r *reachProfileResolver) Resolve(_ string) (string, *ssh.ConnectConfig, error) {
	return r.host, &ssh.ConnectConfig{User: "test", KeyFile: r.keyFile}, nil
}

// reachStack builds the REAL composition: session registry, real SSH client,
// real transport server — the only fakes are the profile resolver and the
// SSH server itself. launcher nil means no WithRemoteLauncher was wired.
func reachStack(t *testing.T, srv *reachSSHServer, launcher ssh.RemoteLauncher) (*transport.WSServer, *websocket.Conn) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)

	client, err := ssh.NewReal(
		logger,
		ssh.WithKnownHostsFile(reachWriteKnownHosts(t, srv)),
		ssh.WithConfigResolver(reachResolver{}),
	)
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	reg = reg.WithSSHFactory(&sshFactoryAdapter{client: client})

	opts := []transport.WSServerOption{
		transport.WithProfileResolver(&reachProfileResolver{host: srv.addr, keyFile: reachWriteKeyFile(t, srv.userKey)}),
	}
	if launcher != nil {
		opts = append(opts, transport.WithRemoteLauncher(launcher))
	}
	ws := transport.NewWSServer(logger, reg, opts...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("ws.Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws, reachConnectWS(t, ws)
}

type reachPTYFactory struct{ stub *pty.Stub }

func (f *reachPTYFactory) NewPTY(_ context.Context, _ pty.Config) (pty.Pty, error) {
	return f.stub, nil
}

func reachConnectWS(t *testing.T, ws *transport.WSServer) *websocket.Conn {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", ws.Port()), Path: "/session"}
	d := websocket.Dialer{Subprotocols: []string{"nocx.token." + ws.Token()}}
	conn, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func reachJSONRPCCall(t *testing.T, conn *websocket.Conn, method string, params any) json.RawMessage {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var check struct {
			ID *json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(resp, &check)
		if check.ID == nil {
			continue // notification (exit, ack)
		}
		var idCheck struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(resp, &idCheck)
		if idCheck.ID != 1 {
			continue
		}
		return resp
	}
}

// reachOpenSSH opens the profile's session and returns the id plus the
// refusal reason the product actually receives.
//
// The reason no longer rides the ack (nocx-dvql): it arrives on the
// session.integrationChanged notification, which is what lets it be revised
// later — a handshake that expires or a channel lost mid-session are both
// after the ack, and a one-shot field could never report either. An empty
// return still means "no refusal": a session that asked for integration and
// has not failed reports `starting`, which carries no reason at all.
func reachOpenSSH(t *testing.T, conn *websocket.Conn, enhanced bool) (sid string, reason string) {
	t.Helper()
	resp := reachJSONRPCCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
		"kind": "ssh", "profileId": "p1", "enhanced": enhanced,
	})
	var envelope struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode open response: %v\nraw: %s", err, string(resp))
	}
	if envelope.Error != nil {
		t.Fatalf("open failed: %d %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result.SessionID, reachIntegrationReason(t, conn, envelope.Result.SessionID)
}

// reachIntegrationReason waits for the session's first integration status and
// returns its reason. Read AFTER the ack, deliberately: AD-7 puts the ack
// before any of the session's own traffic, so a status frame that overtook it
// would address a session the renderer does not yet have.
func reachIntegrationReason(t *testing.T, conn *websocket.Conn, sid string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read integration status: %v", err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		var frame struct {
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Status    string `json:"status"`
				Reason    string `json:"reason"`
			} `json:"params"`
		}
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}
		if frame.Method != "session.integrationChanged" || frame.Params.SessionID != sid {
			continue
		}
		return frame.Params.Reason
	}
	t.Fatalf("no session.integrationChanged for %s: the refusal reason never reached the product", sid)
	return ""
}

// reachWriteAndReadEcho round-trips one write through the real data plane:
// binary frame in → SSH channel → test server echo loop → ring → binary frame
// out. This is the "usable session" proof at the transport boundary.
func reachWriteAndReadEcho(t *testing.T, conn *websocket.Conn, sid string) {
	t.Helper()
	idBytes, err := session.IDToBytes(session.ID(sid))
	if err != nil {
		t.Fatalf("session id to bytes: %v", err)
	}
	frame := []byte{0x01, 0x01} // FrameVersion, MsgTypeData
	frame = append(frame, idBytes[:]...)
	frame = append(frame, []byte("hello")...)
	// The write is RETRIED from a goroutine, and that is a statement about
	// the product rather than about flakiness: a session that has just been
	// opened is bootstrapping, and design §5.3 says a keystroke in that
	// interval is REFUSED, not buffered. This fixture server never speaks
	// the bootstrap protocol, so the interval closes on the writer's own
	// deadline; until then the frame is dropped exactly as a user's
	// keystroke would be, and the user types again. The read keeps ONE
	// deadline for the whole wait — a websocket connection does not survive
	// a read timeout, so the retry cannot live in the read loop.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if len(msg) < 18 || msg[0] != 0x01 || msg[1] != 0x01 {
			continue // control frame or non-data binary — skip
		}
		if got := string(msg[18:]); got == "echo:hello" {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// The behavioural proof: the launcher is reachable from the transport, and
// its start command is what the remote session runs.
// ---------------------------------------------------------------------------

// The production launcher through the adapter through the real transport:
// the open ack reports integration succeeded and the test server receives the
// CARRIER — the bounded loader, under 1 KiB, addressed with the session id —
// not a plain shell request. This is the exact chain that was dead before the
// wiring — grep RemoteLauncher in internal/app and internal/transport returned
// nothing.
//
// What it used to assert was the shape the carrier design retires: an
// `env … bash -c` wrapper around an rcfile, with NOCX_SHELL_INTEGRATION and
// NOCX_SESSION_ID as environment assignments IN THE COMMAND. The environment
// block travels with stage-1 now; only addressing does, so the session id is
// asserted here as the addressing argument it became.
func TestRemoteLauncher_ReachableThroughRealTransport(t *testing.T) {
	srv := startReachSSHServer(t)
	ws, conn := reachStack(t, srv, &remoteLauncherAdapter{inner: shellintegration.NewRemoteLauncher(), logger: log.NewSlogAdapter(nil)})
	_ = ws

	sid, reason := reachOpenSSH(t, conn, true)
	if reason != "" {
		t.Errorf("shellIntegrationReason = %q, want empty (launcher accepted)", reason)
	}

	cmd := srv.waitForExec(t, 10*time.Second)
	if !strings.Contains(cmd, "/usr/bin/env -u BASH_ENV /bin/sh -c") {
		t.Errorf("exec command %q does not look like the carrier", cmd)
	}
	if !strings.Contains(cmd, shellintegration.LoaderReadyToken) {
		t.Errorf("exec command %q never emits %q — it is not the loader",
			cmd, shellintegration.LoaderReadyToken)
	}
	if len(cmd) >= shellintegration.MaxCarrierLen {
		t.Errorf("exec command is %d bytes; the stated bound is %d",
			len(cmd), shellintegration.MaxCarrierLen)
	}
	if !strings.Contains(cmd, " nocx-loader '"+sid+"'") {
		t.Errorf("exec command %q does not carry session %q as its first addressing "+
			"argument — the launcher did not see the session id", cmd, sid)
	}
	if n := srv.shellRequestCount(); n != 0 {
		t.Errorf("%d plain-shell request(s) alongside the launcher command; want 0", n)
	}

	reachWriteAndReadEcho(t, conn, sid)
}

// A launcher decline through the real transport: the session still opens as
// an ordinary usable shell, and the refusal reason reaches the product over
// the wire instead of dying in a log.
func TestRemoteLauncher_DeclineLeavesUsableSessionWithReasonOnWire(t *testing.T) {
	srv := startReachSSHServer(t)
	declining := &remoteLauncherAdapter{inner: decliningSILauncher{reason: shellintegration.ReasonNoSecureTemp}, logger: log.NewSlogAdapter(nil)}
	_, conn := reachStack(t, srv, declining)

	sid, reason := reachOpenSSH(t, conn, false)
	if reason != "no-secure-temp" {
		t.Errorf("shellIntegrationReason = %q, want %q", reason, "no-secure-temp")
	}
	srv.waitForShell(t, 10*time.Second)
	if n := srv.execCount(); n != 0 {
		t.Errorf("%d exec(s) sent despite the decline; want a plain shell", n)
	}
	if n := srv.shellRequestCount(); n != 1 {
		t.Errorf("shell requests = %d, want 1 (plain-shell fallback)", n)
	}
	reachWriteAndReadEcho(t, conn, sid)
}

// No launcher wired at all: the transport must degrade to a plain shell with
// reason none — never fail the open.
func TestRemoteLauncher_NotWired_PlainShellNoReason(t *testing.T) {
	srv := startReachSSHServer(t)
	_, conn := reachStack(t, srv, nil)

	sid, reason := reachOpenSSH(t, conn, false)
	if reason != "" {
		t.Errorf("shellIntegrationReason = %q, want empty (no launcher wired)", reason)
	}
	srv.waitForShell(t, 10*time.Second)
	if n := srv.execCount(); n != 0 {
		t.Errorf("%d exec(s) sent with no launcher wired; want a plain shell", n)
	}
	reachWriteAndReadEcho(t, conn, sid)
}

// decliningSILauncher is a scripted shellintegration.RemoteLauncher that
// always refuses — the adapter's decline mapping is what the test observes.
type decliningSILauncher struct {
	reason shellintegration.RefusalReason
}

func (d decliningSILauncher) StartCommand(shellintegration.ShellKind, shellintegration.LaunchOptions) (string, shellintegration.RefusalReason, bool) {
	return "", d.reason, false
}

// A launcher reason the ssh vocabulary does not know: the session still opens
// as a usable plain shell, the wire carries the distinct "unknown" reason
// (never the ReasonNone that renders as "integration succeeded"), the backend
// does not panic, and the contradiction is shouted into the log — the
// fail-open half of the tripwire that used to be a crash (ADR-0004:60).
func TestRemoteLauncher_UnmappedReason_UnknownOnWire(t *testing.T) {
	srv := startReachSSHServer(t)
	logger, buf := captureAdapterLogs(t)
	declining := &remoteLauncherAdapter{inner: decliningSILauncher{reason: shellintegration.RefusalReason("brand-new-reason")}, logger: logger}
	_, conn := reachStack(t, srv, declining)

	sid, reason := reachOpenSSH(t, conn, false)
	if reason != "unknown" {
		t.Errorf("shellIntegrationReason = %q, want %q", reason, "unknown")
	}
	if !strings.Contains(buf.String(), "unmapped refusal reason") {
		t.Errorf("expected the loud unmapped-reason log, got:\n%s", buf.String())
	}
	srv.waitForShell(t, 10*time.Second)
	if n := srv.execCount(); n != 0 {
		t.Errorf("%d exec(s) sent despite the unmapped decline; want a plain shell", n)
	}
	reachWriteAndReadEcho(t, conn, sid)
}

// A launcher that accepts while naming a refusal: the adapter declines, the
// claimed reason reaches the wire, and the session is still a usable plain
// shell — the contradiction is shouted, never fatal (ADR-0004:60).
func TestRemoteLauncher_AcceptedWithReason_DeclinesOnWire(t *testing.T) {
	srv := startReachSSHServer(t)
	logger, buf := captureAdapterLogs(t)
	contradicting := &remoteLauncherAdapter{inner: contradictingSILauncher{}, logger: logger}
	_, conn := reachStack(t, srv, contradicting)

	sid, reason := reachOpenSSH(t, conn, false)
	if reason != "unsupported-shell" {
		t.Errorf("shellIntegrationReason = %q, want %q", reason, "unsupported-shell")
	}
	if !strings.Contains(buf.String(), "accepted while naming a refusal reason") {
		t.Errorf("expected the loud contradiction log, got:\n%s", buf.String())
	}
	srv.waitForShell(t, 10*time.Second)
	if n := srv.execCount(); n != 0 {
		t.Errorf("%d exec(s) sent despite the contradicting launcher; want a plain shell", n)
	}
	reachWriteAndReadEcho(t, conn, sid)
}

// contradictingSILauncher is a scripted shellintegration.RemoteLauncher that
// violates the StartCommand contract: ok=true while naming a refusal.
type contradictingSILauncher struct{}

func (contradictingSILauncher) StartCommand(shellintegration.ShellKind, shellintegration.LaunchOptions) (string, shellintegration.RefusalReason, bool) {
	return "exec bash -i", shellintegration.ReasonUnsupportedShell, true
}
