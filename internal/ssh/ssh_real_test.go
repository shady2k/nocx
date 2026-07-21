package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ---------------------------------------------------------------------------
// In-process SSH test server
// ---------------------------------------------------------------------------

type testSSHServer struct {
	t          *testing.T
	hostSigner gossh.Signer
	userSigner gossh.Signer
	listener   net.Listener
	addr       string

	mu         sync.Mutex
	ptyCols    uint16
	ptyRows    uint16
	ptyX       uint16
	ptyY       uint16
	shellCh    gossh.Channel
	shellReady chan struct{}
}

func startTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()

	hostKey := generateSigner(t)
	userKey := generateSigner(t)

	config := &gossh.ServerConfig{
		PublicKeyCallback: func(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), userKey.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("gossh: unknown public key for %q", meta.User())
		},
	}
	config.AddHostKey(hostKey)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test server listen: %v", err)
	}

	srv := &testSSHServer{
		t:          t,
		hostSigner: hostKey,
		userSigner: userKey,
		listener:   listener,
		addr:       listener.Addr().String(),
		shellReady: make(chan struct{}),
	}

	go srv.acceptLoop(config)
	return srv
}

func (s *testSSHServer) acceptLoop(config *gossh.ServerConfig) {
	conn, err := s.listener.Accept()
	if err != nil {
		s.t.Logf("test server accept: %v", err)
		return
	}

	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		s.t.Logf("test server handshake: %v", err)
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
			s.t.Logf("test server accept channel: %v", err)
			return
		}
		go s.handleSession(ch, reqs)
	}

	_ = sshConn.Close()
}

func (s *testSSHServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	// Process requests in a separate goroutine so the shell loop can run
	// concurrently and window-change requests are delivered after shell starts.
	go func() {
		for req := range reqs {
			switch req.Type {
			case "pty-req":
				cols, rows, xp, yp := parsePTYReq(req.Payload)
				s.mu.Lock()
				s.ptyCols = cols
				s.ptyRows = rows
				s.ptyX = xp
				s.ptyY = yp
				s.mu.Unlock()
				_ = req.Reply(true, nil)

			case "window-change":
				cols, rows, xp, yp := parseWindowChange(req.Payload)
				s.mu.Lock()
				s.ptyCols = cols
				s.ptyRows = rows
				s.ptyX = xp
				s.ptyY = yp
				s.mu.Unlock()
				_ = req.Reply(true, nil)

			case "shell":
				_ = req.Reply(true, nil)
				s.mu.Lock()
				s.shellCh = ch
				s.mu.Unlock()
				close(s.shellReady)

			default:
				_ = req.Reply(false, nil)
			}
		}
	}()

	// Echo loop: whatever the client writes comes back prefixed with "echo:".
	// Each chunk read is echoed as a separate message; the client must expect
	// that multiple writes may arrive in one read.
	buf := make([]byte, 4096)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			reply := append([]byte("echo:"), buf[:n]...)
			_, _ = ch.Write(reply)
		}
		if err != nil {
			return
		}
	}
}

func parsePTYReq(payload []byte) (cols, rows, xp, yp uint16) {
	r := bytes.NewReader(payload)
	termLen := readUint32(r)
	_, _ = r.Seek(int64(termLen), 1)
	// Wire format uses uint32; values are SSH protocol limits so fit uint16.
	cols = uint16(readUint32(r)) //nolint:gosec // SSH protocol values fit uint16
	rows = uint16(readUint32(r)) //nolint:gosec
	xp = uint16(readUint32(r))   //nolint:gosec
	yp = uint16(readUint32(r))   //nolint:gosec
	return
}

func parseWindowChange(payload []byte) (cols, rows, xp, yp uint16) {
	r := bytes.NewReader(payload)
	cols = uint16(readUint32(r)) //nolint:gosec
	rows = uint16(readUint32(r)) //nolint:gosec
	xp = uint16(readUint32(r))   //nolint:gosec
	yp = uint16(readUint32(r))   //nolint:gosec
	return
}

func readUint32(r *bytes.Reader) uint32 {
	var v uint32
	_ = binary.Read(r, binary.BigEndian, &v)
	return v
}

func (s *testSSHServer) getPTYSize() (cols, rows, xp, yp uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ptyCols, s.ptyRows, s.ptyX, s.ptyY
}

func (s *testSSHServer) close() {
	_ = s.listener.Close()
	s.mu.Lock()
	if s.shellCh != nil {
		_ = s.shellCh.Close()
	}
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func generateSigner(t *testing.T) gossh.Signer {
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

// writeKnownHosts writes the server's host key fingerprint into a
// known_hosts file for the given address, returns the file path.
func writeKnownHosts(t *testing.T, srv *testSSHServer, addr string) string {
	t.Helper()
	hostKey := srv.hostSigner.PublicKey()
	line := knownhosts.Line([]string{addr}, hostKey)
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// writeSSHConfig writes an ssh_config file and returns the path.
func writeSSHConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write ssh_config: %v", err)
	}
	return path
}

func hostPortOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRealClient_ImplementsSSH(t *testing.T) {
	var _ SSH = (*RealClient)(nil)
}

func TestRealChannel_ImplementsChannel(t *testing.T) {
	var _ Channel = (*RealChannel)(nil)
}

func TestConnect_KeyAuth_Success(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 640, 480),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = ch.Close() }()

	// Wait for the shell to be ready on the server side — no sleeps.
	<-srv.shellReady

	_, err = ch.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	buf := make([]byte, 32)
	n, err := ch.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "echo:hello" {
		t.Fatalf("expected echo:hello, got %q", string(buf[:n]))
	}
}

func TestConnect_PTY_RequestedSize(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(100, 40, 800, 600),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	cols, rows, xp, yp := srv.getPTYSize()
	if cols != 100 {
		t.Errorf("expected cols=100, got %d", cols)
	}
	if rows != 40 {
		t.Errorf("expected rows=40, got %d", rows)
	}
	if xp != 800 {
		t.Errorf("expected xp=800, got %d", xp)
	}
	if yp != 600 {
		t.Errorf("expected yp=600, got %d", yp)
	}
}

func TestResize_ReachesServer(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = ch.Close() }()

	// Wait for the shell to be ready before resizing.
	<-srv.shellReady

	// Resize the channel. Pixel dimensions should reach the server via the
	// manual window-change wire message.
	err = ch.Resize(context.Background(), 132, 43, 1056, 860)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	// Wait for the window-change to be processed by the server's request
	// goroutine.
	time.Sleep(100 * time.Millisecond)

	cols, rows, xp, yp := srv.getPTYSize()
	if cols != 132 {
		t.Errorf("expected cols=132 after resize, got %d", cols)
	}
	if rows != 43 {
		t.Errorf("expected rows=43 after resize, got %d", rows)
	}
	if xp != 1056 {
		t.Errorf("expected xp=1056 after resize, got %d", xp)
	}
	if yp != 860 {
		t.Errorf("expected yp=860 after resize, got %d", yp)
	}
}

func TestDataFlow_Bidirectional(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = ch.Close() }()

	<-srv.shellReady

	// Write and read one message at a time so reads are predictable.
	// (Multiple unread writes may be batched into one read by the transport.)
	for _, msg := range []string{"first", "second", "third"} {
		_, err := ch.Write([]byte(msg))
		if err != nil {
			t.Fatalf("Write(%q): %v", msg, err)
		}

		buf := make([]byte, 128)
		n, err := ch.Read(buf)
		if err != nil {
			t.Fatalf("Read after %q: %v", msg, err)
		}
		expected := "echo:" + msg
		got := string(buf[:n])
		if got != expected {
			t.Fatalf("expected %q, got %q", expected, got)
		}
	}
}

func TestConnect_HostKeyMismatch_Rejected(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	dir := t.TempDir()
	khPath := filepath.Join(dir, "known_hosts")

	// Write a known_hosts with a DIFFERENT key so there's a mismatch.
	differentKey := generateSigner(t)
	line := knownhosts.Line([]string{srv.addr}, differentKey.PublicKey())
	if err := os.WriteFile(khPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err == nil {
		t.Fatal("expected error for mismatched host key, got nil")
	}

	var hostKeyErr *ErrHostKeyMismatch
	if !errors.As(err, &hostKeyErr) {
		t.Fatalf("expected ErrHostKeyMismatch, got %T: %v", err, err)
	}
}

func TestConnect_WrongKey_AuthFailed(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	wrongKey := generateSigner(t)

	_, err = client.Connect(
		context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(wrongKey),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}

	var authErr *ErrAuthFailed
	if !errors.As(err, &authErr) {
		t.Fatalf("expected ErrAuthFailed, got %T: %v", err, err)
	}
}

func TestSSHConfig_AliasResolution(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	// Extract the port from the server address so the config matches.
	_, portStr, _ := net.SplitHostPort(srv.addr)
	configContent := fmt.Sprintf(`Host myalias
    HostName %s
    User testuser
    Port %s
`, hostPortOnly(srv.addr), portStr)

	configPath := writeSSHConfig(t, configContent)

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
		WithSSHConfigPath(configPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), "myalias",
		WithUser("testuser"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err != nil {
		t.Fatalf("Connect via alias: %v", err)
	}
	defer func() { _ = ch.Close() }()

	<-srv.shellReady
	_, err = ch.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write via alias: %v", err)
	}
	buf := make([]byte, 32)
	n, err := ch.Read(buf)
	if err != nil {
		t.Fatalf("Read via alias: %v", err)
	}
	if string(buf[:n]) != "echo:hello" {
		t.Fatalf("expected echo:hello via alias, got %q", string(buf[:n]))
	}
}

func TestSSHConfig_ExplicitOptionBeatsConfig(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	configContent := fmt.Sprintf(`Host %s
    User configuser
`, hostPortOnly(srv.addr))

	configPath := writeSSHConfig(t, configContent)

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
		WithSSHConfigPath(configPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Connect(
		context.Background(), srv.addr,
		WithUser("explicituser"),
		WithAuthMethods([]gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		}),
		WithPTYSize(80, 24, 0, 0),
	)
	if err != nil {
		t.Fatalf("Connect with explicit user: %v", err)
	}
	defer func() { _ = ch.Close() }()

	<-srv.shellReady
	_, err = ch.Write([]byte("ping"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 32)
	n, err := ch.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "echo:ping" {
		t.Fatalf("expected echo:ping, got %q", string(buf[:n]))
	}
}
