package ssh

// Probe (nocx-m8jwn.11 / P4): the in-process half of §6.4's `exec`-refused
// measurement. The real-server half lives in
// internal/app/exec_refusal_probe_test.go and is the authority, because the
// row is a property of the server; this half measures what our OWN fixtures
// do, since they are what the product's tests will actually exercise.
//
// A real OpenSSH server cannot be configured to refuse an `exec` request at
// the request level (measured in the app half), so the exact sequence §6.4
// names — session, pty-req, REFUSED exec, then shell on the same channel —
// can only be produced by a server that chooses to refuse it. A
// golang.org/x/crypto/ssh server can, which is why both branches of the row
// are measured here: the refusing server that keeps the channel, and the
// refusing server that closes it.
//
// This file measures only. It wires no production code and changes none. It
// is deliberately self-contained (every identifier prefixed execProbe) so
// nothing in ssh_real_test.go or ssh_fsconn_test.go is restructured.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

const (
	execProbeBanner = "NOCXPROBE_PROMPT> "
	execProbeEcho   = "NOCXPROBE_ECHO:"
)

type execProbePTYReq struct {
	Term                         string
	Columns, Rows, Width, Height uint32
	Modelist                     string
}

type execProbeExecReq struct{ Command string }

// execProbeServer is a minimal in-process SSH server that REFUSES the `exec`
// request on a session channel. closeOnRefusal selects the branch: keep the
// channel open after refusing (the permissive server), or tear it down (the
// server that treats a refused start as fatal to the channel).
type execProbeServer struct {
	listener   net.Listener
	addr       string
	hostSigner gossh.Signer
	userSigner gossh.Signer

	closeOnRefusal bool

	mu          sync.Mutex
	auths       int
	conns       int
	sessions    int
	ptyGranted  int
	execRefused int
	shellOK     int
	ptyCols     uint32
}

func (s *execProbeServer) counts() (auths, conns, sessions, ptyGranted, execRefused, shellOK int, ptyCols uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auths, s.conns, s.sessions, s.ptyGranted, s.execRefused, s.shellOK, s.ptyCols
}

func execProbeSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("wrap signer: %v", err)
	}
	return signer
}

func startExecProbeServer(t *testing.T, closeOnRefusal bool) *execProbeServer {
	t.Helper()
	srv := &execProbeServer{
		hostSigner:     execProbeSigner(t),
		userSigner:     execProbeSigner(t),
		closeOnRefusal: closeOnRefusal,
	}
	cfg := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), srv.userSigner.PublicKey().Marshal()) {
				return nil, errors.New("unknown key")
			}
			srv.mu.Lock()
			srv.auths++
			srv.mu.Unlock()
			return nil, nil
		},
	}
	cfg.AddHostKey(srv.hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv.listener = ln
	srv.addr = ln.Addr().String()
	t.Cleanup(func() { _ = ln.Close() })

	// Server goroutines never touch t: they outlive the test body, and a
	// t.Logf from one of them is the -race failure ssh_real_test.go's logf
	// comment documents. Everything they observe lands in counters instead.
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go srv.serve(conn, cfg)
		}
	}()
	return srv
}

func (s *execProbeServer) serve(conn net.Conn, cfg *gossh.ServerConfig) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	s.mu.Lock()
	s.conns++
	s.mu.Unlock()
	go gossh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(gossh.UnknownChannelType, "only session channels here")
			continue
		}
		ch, chReqs, acceptErr := newChan.Accept()
		if acceptErr != nil {
			continue
		}
		s.mu.Lock()
		s.sessions++
		s.mu.Unlock()
		go s.handleSession(ch, chReqs)
	}
	_ = sshConn.Close()
}

func (s *execProbeServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var m execProbePTYReq
			if gossh.Unmarshal(req.Payload, &m) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.ptyGranted++
			s.ptyCols = m.Columns
			s.mu.Unlock()
			_ = req.Reply(true, nil)

		case "exec":
			// The refusal under measurement.
			s.mu.Lock()
			s.execRefused++
			s.mu.Unlock()
			_ = req.Reply(false, nil)
			if s.closeOnRefusal {
				_ = ch.Close()
				return
			}

		case "shell":
			s.mu.Lock()
			s.shellOK++
			s.mu.Unlock()
			_ = req.Reply(true, nil)
			// A stand-in for a prompt: the fixture cannot produce a real
			// shell, so what it proves is that the channel carries bytes
			// both ways after the refusal. The real prompt is the app half.
			_, _ = ch.Write([]byte(execProbeBanner))
			go func() {
				buf := make([]byte, 4096)
				for {
					n, readErr := ch.Read(buf)
					if n > 0 {
						_, _ = ch.Write(append([]byte(execProbeEcho), buf[:n]...))
					}
					if readErr != nil {
						return
					}
				}
			}()

		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *execProbeServer) dial(t *testing.T) *gossh.Client {
	t.Helper()
	client, err := gossh.Dial("tcp", s.addr, &gossh.ClientConfig{
		User:            "probe",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(s.userSigner)},
		HostKeyCallback: gossh.FixedHostKey(s.hostSigner.PublicKey()),
		Timeout:         20 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial probe server: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// execProbeOpenSession opens a session channel and discards its inbound
// requests.
func execProbeOpenSession(t *testing.T, client *gossh.Client) gossh.Channel {
	t.Helper()
	ch, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("open session channel: %v", err)
	}
	go gossh.DiscardRequests(reqs)
	return ch
}

func execProbeRequestPTY(t *testing.T, ch gossh.Channel) bool {
	t.Helper()
	ok, err := ch.SendRequest("pty-req", true, gossh.Marshal(&execProbePTYReq{
		Term: "xterm-256color", Columns: 80, Rows: 24, Width: 640, Height: 480,
		Modelist: string([]byte{0}),
	}))
	if err != nil {
		t.Fatalf("pty-req: %v", err)
	}
	return ok
}

// execProbeReadInto pumps a channel into a buffer until it ends.
func execProbeReadInto(ch gossh.Channel) *execProbeBuf {
	out := &execProbeBuf{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				_, _ = out.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return out
}

type execProbeBuf struct {
	mu sync.Mutex
	b  []byte
}

func (e *execProbeBuf) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.b = append(e.b, p...)
	return len(p), nil
}

func (e *execProbeBuf) String() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return string(e.b)
}

// execProbeWaitFor blocks until the buffer contains want. It waits on an
// observable state change; the deadline exists only so a hang reports.
func execProbeWaitFor(t *testing.T, out *execProbeBuf, want, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: never saw %q; channel carried:\n%s", what, want, out.String())
}

// ---------------------------------------------------------------------------
// The sequence §6.4 names, against a server that refuses `exec` and keeps
// the channel: session, pty-req, refused exec, shell on the SAME channel.
func TestExecRefusalProbe_InProcess_RefusedExecLeavesTheSameChannelUsable(t *testing.T) {
	srv := startExecProbeServer(t, false)
	client := srv.dial(t)

	ch := execProbeOpenSession(t, client)
	defer func() { _ = ch.Close() }()

	if !execProbeRequestPTY(t, ch) {
		t.Fatalf("pty-req refused; the sequence cannot reach the exec step")
	}
	ok, err := ch.SendRequest("exec", true, gossh.Marshal(&execProbeExecReq{Command: "nocx-probe-launch"}))
	if err != nil {
		t.Fatalf("exec request errored: %v", err)
	}
	if ok {
		t.Fatalf("exec was accepted; this fixture exists to refuse it")
	}

	out := execProbeReadInto(ch)
	ok, err = ch.SendRequest("shell", true, nil)
	if err != nil {
		t.Fatalf("after the refused exec the channel was gone: shell errored with %v", err)
	}
	if !ok {
		t.Fatalf("after the refused exec the server refused shell on the same channel")
	}
	execProbeWaitFor(t, out, execProbeBanner, "the shell that followed the refused exec produced output")
	if _, err := ch.Write([]byte("typed\n")); err != nil {
		t.Fatalf("write to the shell: %v", err)
	}
	execProbeWaitFor(t, out, execProbeEcho+"typed", "the shell that followed the refused exec reads what is typed")

	auths, conns, sessions, ptys, refused, shells, cols := srv.counts()
	if auths != 1 || conns != 1 || sessions != 1 {
		t.Fatalf("auths=%d conns=%d sessions=%d, want 1/1/1 — the refusal must open no second connection and cost no second authentication", auths, conns, sessions)
	}
	if ptys != 1 || refused != 1 || shells != 1 {
		t.Fatalf("pty-req granted=%d exec refused=%d shell accepted=%d, want 1/1/1", ptys, refused, shells)
	}
	if cols != 80 {
		t.Fatalf("the pty granted before the refusal reported %d columns, want 80", cols)
	}
}

// The same measurement through gossh's *Session API, which is what an
// implementer will actually hold: a Start whose exec request is refused
// leaves the Session unstarted, so Shell on that same Session — and so on
// that same channel — still works.
func TestExecRefusalProbe_InProcess_GosshSessionIsReusableAfterARefusedStart(t *testing.T) {
	srv := startExecProbeServer(t, false)
	client := srv.dial(t)

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer func() { _ = session.Close() }()
	if ptyErr := session.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{}); ptyErr != nil {
		t.Fatalf("request pty: %v", ptyErr)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	startErr := session.Start("nocx-probe-launch")
	if startErr == nil {
		t.Fatalf("Start succeeded; this fixture refuses exec")
	}
	t.Logf("what the client observes when exec is refused: Start returned %v", startErr)

	if err := session.Shell(); err != nil {
		t.Fatalf("Shell on the same Session after the refused Start: %v", err)
	}
	out := &execProbeBuf{}
	go func() { _, _ = io.Copy(out, stdout) }()
	execProbeWaitFor(t, out, execProbeBanner, "the shell started on the Session whose Start was refused")
	if _, err := stdin.Write([]byte("typed\n")); err != nil {
		t.Fatalf("write to the shell: %v", err)
	}
	execProbeWaitFor(t, out, execProbeEcho+"typed", "that shell reads what is typed")

	if auths, conns, _, _, _, _, _ := srv.counts(); auths != 1 || conns != 1 {
		t.Fatalf("auths=%d conns=%d, want 1/1", auths, conns)
	}
}

// The other branch of the row: a server that refuses `exec` and tears the
// channel down with it. The prompt does NOT survive on that channel — but
// the connection does, and a fresh session channel on it needs no second
// authentication, which is what decides between D7's `conventional(reason)`
// and `session-failed(reason)`.
func TestExecRefusalProbe_InProcess_WhenTheServerClosesTheChannelOnRefusal(t *testing.T) {
	srv := startExecProbeServer(t, true)
	client := srv.dial(t)

	ch := execProbeOpenSession(t, client)
	if !execProbeRequestPTY(t, ch) {
		t.Fatalf("pty-req refused")
	}
	ok, err := ch.SendRequest("exec", true, gossh.Marshal(&execProbeExecReq{Command: "nocx-probe-launch"}))
	if err != nil {
		t.Fatalf("exec request errored: %v", err)
	}
	if ok {
		t.Fatalf("exec was accepted; this fixture exists to refuse it")
	}

	// What the client observes on the dead channel: not a refusal, an error.
	// The two are distinguishable without guessing, which is the point.
	shellOK, shellErr := ch.SendRequest("shell", true, nil)
	t.Logf("shell on the channel the server closed after refusing exec: ok=%v err=%v", shellOK, shellErr)
	if shellErr == nil && shellOK {
		t.Fatalf("shell succeeded on a channel the server had closed")
	}
	if shellErr != nil && !errors.Is(shellErr, io.EOF) {
		t.Fatalf("shell on the closed channel reported %v, want io.EOF", shellErr)
	}
	_ = ch.Close()

	// The connection outlives the channel, and costs no second credential use.
	ch2 := execProbeOpenSession(t, client)
	defer func() { _ = ch2.Close() }()
	if !execProbeRequestPTY(t, ch2) {
		t.Fatalf("pty-req refused on the replacement channel")
	}
	out := execProbeReadInto(ch2)
	ok, err = ch2.SendRequest("shell", true, nil)
	if err != nil || !ok {
		t.Fatalf("shell on the replacement channel: ok=%v err=%v, want accepted", ok, err)
	}
	execProbeWaitFor(t, out, execProbeBanner, "a replacement channel on the same connection reaches a shell")

	auths, conns, sessions, _, refused, shells, _ := srv.counts()
	if auths != 1 || conns != 1 {
		t.Fatalf("auths=%d conns=%d, want 1/1 — recovering from the refusal must not re-authenticate", auths, conns)
	}
	if sessions != 2 || refused != 1 || shells != 1 {
		t.Fatalf("sessions=%d exec refused=%d shell accepted=%d, want 2/1/1", sessions, refused, shells)
	}
}
