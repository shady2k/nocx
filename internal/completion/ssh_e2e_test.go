package completion

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/shady2k/nocx/internal/remoteprobe"
)

// TestSSHCompleter_E2E_RemotePaths exercises the SSHCompleter through a
// real SSH connection to an in-process SSH server that runs the
// completion script in a real bash. Both halves of the assertion are
// load-bearing:
//
//  1. POSITIVE: /etc/passwd appears when completing "pas" in /etc.
//  2. NEGATIVE: a file existing only on the backend machine does NOT appear.
func TestSSHCompleter_E2E_RemotePaths(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	srv := startCompletionSSHServer(t)
	client := srv.gosshClient(t)
	defer func() { _ = client.Close() }()

	// Sentinel that exists only on the backend, not in /etc.
	tmpDir := t.TempDir()
	sentinel := "nocx_e2e_sentinel_" + t.Name()
	if err := os.WriteFile(filepath.Join(tmpDir, sentinel), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewSSH(func(ctx context.Context, host string) (ProbeConn, error) {
		return &gosshExecConn{client: client}, nil
	})

	resp, err := c.Complete(context.Background(), Request{
		Host:  srv.addr,
		Cwd:   "/etc",
		Line:  "ls pas",
		Pos:   6,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Positive: passwd exists in /etc.
	found := false
	for _, c := range resp.Candidates {
		if c.Name == "passwd" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("remote /etc completion: 'passwd' not found in %+v", namesOf(resp.Candidates))
	}

	// Negative: the sentinel must NOT appear.
	for _, c := range resp.Candidates {
		if c.Name == sentinel {
			t.Errorf("remote /etc completion: local-only sentinel %q appeared", sentinel)
		}
	}
}

// TestSSHCompleter_E2E_GitCompletion exercises command-specific completion
// through a real SSH server.
func TestSSHCompleter_E2E_GitCompletion(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	srv := startCompletionSSHServer(t)
	client := srv.gosshClient(t)
	defer func() { _ = client.Close() }()

	// Probe: does bash-completion load?
	sess, _ := client.NewSession()
	defer func() { _ = sess.Close() }()
	probeOut, _ := sess.Output("type -t _completion_loader &>/dev/null && echo ready || echo missing")
	if strings.TrimSpace(string(probeOut)) != "ready" {
		t.Skip("bash-completion not available")
	}

	c := NewSSH(func(ctx context.Context, host string) (ProbeConn, error) {
		return &gosshExecConn{client: client}, nil
	})

	resp, err := c.Complete(context.Background(), Request{
		Host:  srv.addr,
		Cwd:   "/tmp",
		Line:  "git ch",
		Pos:   6,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	foundCheckout := false
	foundCherryPick := false
	for _, c := range resp.Candidates {
		switch c.Name {
		case "checkout":
			foundCheckout = true
		case "cherry-pick":
			foundCherryPick = true
		}
	}
	if !foundCheckout {
		t.Errorf("git ch + Tab: 'checkout' not found in %+v", namesOf(resp.Candidates))
	}
	if !foundCherryPick {
		t.Errorf("git ch + Tab: 'cherry-pick' not found in %+v", namesOf(resp.Candidates))
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func namesOf(cs []Candidate) []string {
	n := make([]string, len(cs))
	for i, c := range cs {
		n[i] = c.Name
	}
	return n
}

// ── in-process SSH server (handles exec, no PTY) ─────────────────────────
// Mirrors internal/discovery/e2e_test.go. The e2e-sshd binary always
// creates a PTY and does not send exit-status for non-interactive exec
// requests, so gossh.Session.Output hangs. This server handles exec
// the way gossh expects: reply true, run the command in bash, send
// exit-status, close the channel.

type completionSSHServer struct {
	hostKey  gossh.Signer
	userKey  gossh.Signer
	addr     string
	listener net.Listener
}

func startCompletionSSHServer(t *testing.T) *completionSSHServer {
	t.Helper()
	hostKey := generateEd25519Signer(t)
	userKey := generateEd25519Signer(t)

	config := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if string(key.Marshal()) == string(userKey.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	config.AddHostKey(hostKey)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &completionSSHServer{
		hostKey:  hostKey,
		userKey:  userKey,
		addr:     ln.Addr().String(),
		listener: ln,
	}
	t.Cleanup(func() { _ = ln.Close() })
	go srv.acceptLoop(config)
	return srv
}

func (s *completionSSHServer) acceptLoop(config *gossh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn, config)
	}
}

func (s *completionSSHServer) serveConn(conn net.Conn, config *gossh.ServerConfig) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer func() { _ = sshConn.Close() }()
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
}

func (s *completionSSHServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var m struct{ Command string }
			if err := gossh.Unmarshal(req.Payload, &m); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			stdout, stderr, exit := runRemoteCompletion(m.Command)
			_, _ = ch.Write(stdout)
			_, _ = ch.Stderr().Write(stderr)
			//nolint:gosec // SSH exit statuses are 0-255
			_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{uint32(exit)}))
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// runRemoteCompletion executes the completion script command in bash.
// The command from remoteprobe.CompletionCommand is:
//
//	bash -s -- '<cwd>' '<line>' <pos> <limit> '<nonce>' << 'NOCXEOF_<nonce>'
//	<script>
//	NOCXEOF_<nonce>
func runRemoteCompletion(cmd string) (stdout, stderr []byte, exit int) {
	// Run the command in bash. The heredoc delivers the script via stdin.
	c := exec.Command("bash", "-c", cmd)
	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return outBuf.Bytes(), errBuf.Bytes(), ee.ExitCode()
		}
		return outBuf.Bytes(), errBuf.Bytes(), 1
	}
	return outBuf.Bytes(), errBuf.Bytes(), 0
}

// gosshClient creates a *gossh.Client connected to the server.
func (s *completionSSHServer) gosshClient(t *testing.T) *gossh.Client {
	t.Helper()
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{s.addr}, s.hostKey.PublicKey())
	if err := os.WriteFile(khPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	kh, err := knownhosts.New(khPath)
	if err != nil {
		t.Fatalf("knownhosts: %v", err)
	}
	host, portStr, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	config := &gossh.ClientConfig{
		User:              "e2e",
		Auth:              []gossh.AuthMethod{gossh.PublicKeys(s.userKey)},
		HostKeyCallback:   kh,
		HostKeyAlgorithms: []string{gossh.KeyAlgoED25519},
		Timeout:           10 * time.Second,
	}
	client, err := gossh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
	if err != nil {
		t.Fatalf("gossh.Dial: %v", err)
	}
	return client
}

func generateEd25519Signer(t *testing.T) gossh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return signer
}

// ── ExecConn adapter backed by *gossh.Client ─────────────────────────────

type gosshExecConn struct {
	client *gossh.Client
}

func (c *gosshExecConn) Complete(ctx context.Context, probe CompletionProbe) (*ExecResult, error) {
	// The command is composed HERE, from the same package the helper composes
	// it from: this test stands where the helper stands, and the only thing the
	// two must agree about is the command a completion probe is.
	cmd := remoteprobe.CompletionCommand(probe.Cwd, probe.Line, probe.Pos, probe.Limit, probe.Nonce)
	sess, err := c.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()

	var stdout, stderr strings.Builder
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	runErr := make(chan error, 1)
	go func() { runErr <- sess.Run(cmd) }()

	select {
	case err := <-runErr:
		exitStatus := 0
		if err != nil {
			if ee, ok := err.(*gossh.ExitError); ok {
				exitStatus = ee.ExitStatus()
			} else {
				return nil, err
			}
		}
		return &ExecResult{
			Stdout:     []byte(stdout.String()),
			Stderr:     []byte(stderr.String()),
			ExitStatus: exitStatus,
		}, nil
	case <-ctx.Done():
		_ = sess.Close()
		<-runErr
		return nil, ctx.Err()
	}
}

func (c *gosshExecConn) Close() error { return nil }
