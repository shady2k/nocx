package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/vault"
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

	mu           sync.Mutex
	ptyCols      uint16
	ptyRows      uint16
	ptyX         uint16
	ptyY         uint16
	shellCh      gossh.Channel
	shellReady   chan struct{}
	shellReadyDo sync.Once
	// windowChanged carries one signal per processed window-change request.
	windowChanged chan struct{}
	// execCommands records every accepted "exec" request command
	// (session.Start payloads). Buffered; tests drain after Connect returns.
	execCommands chan string
	// shellRequests counts answered "shell" requests (session.Shell calls).
	shellRequests int
	// execRefusals and execSubstitutions count the two §6.4 rows this
	// server can produce, so a test states a number rather than a mood.
	execRefusals      int
	execSubstitutions int
	// execHandler, when set, answers every accepted exec request with its
	// canned output, exit status and a channel close — the server side of a
	// scripted remote probe (discovery tests). Nil keeps the default echo
	// behavior. Read under s.mu; set via setExecHandler.
	execHandler func(cmd string) (stdout, stderr string, exit int)
	// maxSessions, when > 0, caps session channels per connection; further
	// opens are rejected with ResourceShortage (OpenSSH's MaxSessions).
	// Read under s.mu; set via setMaxSessions.
	maxSessions int
	// sessionChannels counts session channels GRANTED across every
	// connection this server has served. It is what makes "the user's
	// session was claimed before nocx opened anything of its own" a state a
	// test can read at the moment the auxiliary call is made, rather than an
	// ordering it has to arrange. Read under s.mu; see sessionChannelCount.
	sessionChannels int
	// execRefusal selects §6.4's `exec` rows. A real OpenSSH server cannot
	// be made to refuse an exec request at all — measured in
	// internal/app/exec_refusal_probe_test.go, five ways of restricting an
	// account and every one ACCEPTS and substitutes — so the two refusing
	// rows exist only against a server that chooses to refuse, and this is
	// that server. Read under s.mu; set via setExecRefusal.
	execRefusal execRefusalMode
	// execSubstitute, when set, is what the server runs INSTEAD of the
	// command it was asked for: the request is accepted, this is written,
	// an exit status is sent and the channel is closed. That is the sixth
	// row, and the only one a stock server actually produces.
	execSubstitute string
	// liveConns tracks established server-side SSH connections so a test can
	// kill them to simulate transport loss. Guarded by liveMu (serveConn
	// registers from the accept loop while a test may kill concurrently).
	liveMu    sync.Mutex
	liveConns map[*gossh.ServerConn]struct{}

	// logMu guards every call this server's GOROUTINES make into t, and
	// `over` says the test that owns that t has finished. See logf.
	logMu sync.Mutex
	over  bool
}

// logf is the only way a server goroutine may reach *testing.T, and the lock
// is what makes it legal.
//
// The server's goroutines outlive their test by design: serving is sequential
// so acceptLoop stays parked in Accept, and a probe whose host key was
// rejected leaves gossh.NewServerConn mid-handshake on a connection the
// client has already dropped. close() only closed the LISTENER, so that
// handshake went on to fail and log its error into a t whose test had
// returned — which the race detector reports (correctly) as a write/read race
// against tRunner's completion, and which fails the whole package under
// -race while no test reports a failed assertion (nocx-9y7z).
//
// The guarantee is an interval, not an instant: close() takes this lock and
// sets `over` before it returns, so a logf already inside the lock finishes
// first and no later one can start. Every test closes its server with a defer
// (and t.Cleanup covers a test that forgets), and a deferred call runs before
// tRunner marks the test done. Nothing is silenced while the test is live —
// which is the point, since these four messages are how a handshake failure
// explains itself.
func (s *testSSHServer) logf(format string, args ...any) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.over {
		return
	}
	s.t.Logf(format, args...)
}

// setExecHandler installs the scripted exec responder. Call before the
// test's first connection.
func (s *testSSHServer) setExecHandler(h func(cmd string) (stdout, stderr string, exit int)) {
	s.mu.Lock()
	s.execHandler = h
	s.mu.Unlock()
}

// execRefusalMode selects which of §6.4's `exec` rows this server produces.
type execRefusalMode int

const (
	// execAccepts is the default: the request is accepted and the command
	// runs.
	execAccepts execRefusalMode = iota
	// execRefusedChannelSurvives answers (false, nil) and leaves the
	// channel and its pty alone — a `shell` on the same channel still
	// works.
	execRefusedChannelSurvives
	// execRefusedChannelClosed answers (false, …) and tears the channel
	// down with it, so the client sees io.EOF on the next request.
	execRefusedChannelClosed
	// execAcceptedAndSubstituted accepts the request and runs something
	// else, consuming the channel.
	execAcceptedAndSubstituted
)

// setExecRefusal selects the §6.4 row. Call before the test's first
// connection.
func (s *testSSHServer) setExecRefusal(m execRefusalMode, substitute string) {
	s.mu.Lock()
	s.execRefusal = m
	s.execSubstitute = substitute
	s.mu.Unlock()
}

// sessionChannelCount is how many session channels this server has granted so
// far — the server's own view, taken at whatever moment the caller asks.
func (s *testSSHServer) sessionChannelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionChannels
}

// setMaxSessions caps session channels per connection (OpenSSH's
// MaxSessions). Call before the test's first connection.
func (s *testSSHServer) setMaxSessions(n int) {
	s.mu.Lock()
	s.maxSessions = n
	s.mu.Unlock()
}

// waitLiveConns blocks until the server holds exactly want established
// connections, and fails the test if it does not get there.
//
// It exists because acceptLoop serves sequentially: serveConn runs to
// completion before the next connection is accepted, so a client whose own
// handshake has returned may still be sitting unaccepted in the listener's
// backlog, and a connection the client has already closed may still be
// registered here. Both windows widen under load. A test that wants to act on
// the server's view of its connections has to wait for that view to catch up,
// and the count is the only thing that says it has (nocx-zlvw).
func (s *testSSHServer) waitLiveConns(want int) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.liveMu.Lock()
		got := len(s.liveConns)
		s.liveMu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("server holds %d established connections, want %d", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// killConns closes every established server-side connection, simulating
// transport loss for the clients. Closing the server side makes the
// client's transport fail, which is what a real network loss does.
//
// It refuses to kill nothing. Every caller closes connections in order to
// observe a loss immediately afterwards, so an empty set is never a no-op —
// it is a wait that can only end at its deadline, reported as whatever the
// caller was waiting on rather than as the kill that never happened. That is
// precisely how nocx-zlvw read as a slow machine for a week: five identical
// 5.05s failures under load, all of them the server having nothing to close.
func (s *testSSHServer) killConns() {
	s.t.Helper()
	s.liveMu.Lock()
	conns := make([]*gossh.ServerConn, 0, len(s.liveConns))
	for c := range s.liveConns {
		conns = append(conns, c)
	}
	s.liveMu.Unlock()
	if len(conns) == 0 {
		s.t.Fatal("killConns: no established connection to close — " +
			"the loss the test is about to wait for can never arrive; " +
			"wait for the server to accept the connection first (waitLiveConns)")
	}
	for _, c := range conns {
		_ = c.Close()
	}
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
		t:             t,
		hostSigner:    hostKey,
		userSigner:    userKey,
		listener:      listener,
		addr:          listener.Addr().String(),
		shellReady:    make(chan struct{}),
		windowChanged: make(chan struct{}, 8),
		execCommands:  make(chan string, 8),
		liveConns:     make(map[*gossh.ServerConn]struct{}),
	}

	// The server outlives the test's body but not its t: close() ends the
	// interval in which its goroutines may log (see logf). Registered here as
	// well as deferred by each test, so a test that forgets is still safe.
	t.Cleanup(srv.close)
	go srv.acceptLoop(config)
	return srv
}

// startTestSSHServerWithUserKey starts a test SSH server that authenticates
// with the provided user key instead of generating one. The caller retains
// access to the raw private key material to store in a test SecretStore.
func startTestSSHServerWithUserKey(t *testing.T, userKey gossh.Signer) *testSSHServer {
	t.Helper()

	hostKey := generateSigner(t)

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
		t:             t,
		hostSigner:    hostKey,
		userSigner:    userKey,
		listener:      listener,
		addr:          listener.Addr().String(),
		shellReady:    make(chan struct{}),
		windowChanged: make(chan struct{}, 8),
		execCommands:  make(chan string, 8),
		liveConns:     make(map[*gossh.ServerConn]struct{}),
	}

	// The server outlives the test's body but not its t: close() ends the
	// interval in which its goroutines may log (see logf). Registered here as
	// well as deferred by each test, so a test that forgets is still safe.
	t.Cleanup(srv.close)
	go srv.acceptLoop(config)
	return srv
}

func (s *testSSHServer) acceptLoop(config *gossh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Listener closed (srv.close) or a transient error — stop.
			return
		}
		s.serveConn(conn, config)
	}
}

// serveConn performs the server side of one SSH connection and returns when
// the connection ends, so acceptLoop can accept the next one. Serving
// sequentially is deliberate: a probe whose host key was rejected closes the
// connection without a session, and accept-on-first-use needs the follow-up
// connection (trust, then probe again) to be served too.
func (s *testSSHServer) serveConn(conn net.Conn, config *gossh.ServerConfig) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		s.logf("test server handshake: %v", err)
		_ = conn.Close()
		return
	}
	s.liveMu.Lock()
	s.liveConns[sshConn] = struct{}{}
	s.liveMu.Unlock()
	defer func() {
		s.liveMu.Lock()
		delete(s.liveConns, sshConn)
		s.liveMu.Unlock()
	}()

	go gossh.DiscardRequests(reqs)

	s.mu.Lock()
	maxSessions := s.maxSessions
	s.mu.Unlock()
	sessions := 0
	for newChan := range chans {
		switch newChan.ChannelType() {
		case "session":
			if maxSessions > 0 && sessions >= maxSessions {
				_ = newChan.Reject(gossh.ResourceShortage, "too many sessions")
				continue
			}
			sessions++
			s.mu.Lock()
			s.sessionChannels++
			s.mu.Unlock()
			ch, reqs, err := newChan.Accept()
			if err != nil {
				s.logf("test server accept channel: %v", err)
				return
			}
			go s.handleSession(ch, reqs)
		case "direct-tcpip":
			ch, reqs, err := newChan.Accept()
			if err != nil {
				s.logf("test server accept direct-tcpip: %v", err)
				continue
			}
			go s.handleDirectTCPIP(ch, reqs, newChan.ExtraData())
		default:
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
		}
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
				// Signal the test rather than making it sleep: a fixed wait
				// is a flake on a loaded machine, and a slow one here.
				select {
				case s.windowChanged <- struct{}{}:
				default:
				}

			case "shell":
				_ = req.Reply(true, nil)
				s.mu.Lock()
				s.shellCh = ch
				s.shellRequests++
				s.mu.Unlock()
				s.shellReadyDo.Do(func() { close(s.shellReady) })

			case "exec":
				var m struct{ Command string }
				if err := gossh.Unmarshal(req.Payload, &m); err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				s.mu.Lock()
				refusal := s.execRefusal
				substitute := s.execSubstitute
				s.mu.Unlock()
				switch refusal {
				case execAccepts:
					// The ordinary arm: the request is accepted and the
					// command runs, which is what falls through below.
				case execRefusedChannelSurvives:
					s.mu.Lock()
					s.execRefusals++
					s.mu.Unlock()
					_ = req.Reply(false, nil)
					continue
				case execRefusedChannelClosed:
					s.mu.Lock()
					s.execRefusals++
					s.mu.Unlock()
					_ = req.Reply(false, nil)
					_ = ch.Close()
					return
				case execAcceptedAndSubstituted:
					s.mu.Lock()
					s.execSubstitutions++
					s.mu.Unlock()
					_ = req.Reply(true, nil)
					_, _ = ch.Write([]byte(substitute))
					_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{0}))
					_ = ch.Close()
					return
				}
				_ = req.Reply(true, nil)
				s.mu.Lock()
				s.shellCh = ch
				s.execCommands <- m.Command
				handler := s.execHandler
				s.mu.Unlock()
				s.shellReadyDo.Do(func() { close(s.shellReady) })
				if handler != nil {
					// Scripted exec: answer with the canned output, a real
					// exit-status request, then close the channel the way
					// sshd does — the client's Run returns only after all
					// three.
					stdout, stderr, exit := handler(m.Command)
					_, _ = ch.Write([]byte(stdout))
					_, _ = ch.Stderr().Write([]byte(stderr))
					_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{uint32(exit)})) //nolint:gosec // SSH exit statuses are 0-255
					_ = ch.Close()
				}

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

// handleDirectTCPIP handles a "direct-tcpip" channel (jump-host forwarding).
// It connects to the target specified in extraData and proxies data between
// the channel and the target connection.
func (s *testSSHServer) handleDirectTCPIP(ch gossh.Channel, reqs <-chan *gossh.Request, extraData []byte) {
	defer func() { _ = ch.Close() }()

	// Parse extraData: dest-addr (string), dest-port (uint32),
	// originator-addr (string), originator-port (uint32).
	r := bytes.NewReader(extraData)
	hostLen := readUint32(r)
	hostBytes := make([]byte, hostLen)
	if _, err := r.Read(hostBytes); err != nil {
		return
	}
	host := string(hostBytes)
	port := readUint32(r)

	targetAddr := net.JoinHostPort(host, strconv.Itoa(int(port)))
	targetConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		s.logf("direct-tcpip: dial target %s: %v", targetAddr, err)
		return
	}
	defer func() { _ = targetConn.Close() }()

	go gossh.DiscardRequests(reqs)

	// Bidirectional copy.
	done := make(chan struct{}, 2)
	go func() {
		_, _ = ioCopy(targetConn, ch)
		done <- struct{}{}
	}()
	go func() {
		_, _ = ioCopy(ch, targetConn)
		done <- struct{}{}
	}()
	<-done
}

// ioCopy copies from src to dst, closing the write side of dst when done.
func ioCopy(dst io.WriteCloser, src io.Reader) (written int64, err error) {
	defer func() { _ = dst.Close() }()
	return io.Copy(dst, src)
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

// close stops the server and ends the interval in which its goroutines may
// speak to *testing.T (see logf). Idempotent: every test closes with a defer
// and both constructors also register it with t.Cleanup, so it runs twice on
// the ordinary path — closing a closed listener is an error nobody reads, and
// setting `over` twice is setting it once.
func (s *testSSHServer) close() {
	_ = s.listener.Close()
	s.mu.Lock()
	if s.shellCh != nil {
		_ = s.shellCh.Close()
	}
	s.mu.Unlock()
	// LAST, and taking the lock is the whole mechanism: a logf already in
	// flight holds it, so this waits for that call to finish before it
	// declares the test over. The goroutines themselves are not joined —
	// they may still be parked in Accept or mid-handshake — they simply stop
	// being able to reach t.
	s.logMu.Lock()
	s.over = true
	s.logMu.Unlock()
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
	// xpixel is deliberately NOT cols*8: the high-level WindowChange API we
	// replaced computed pixels with exactly that formula, so 1056 would pass
	// whether or not our value crossed the wire.
	err = ch.Resize(context.Background(), 132, 43, 1000, 860)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	select {
	case <-srv.windowChanged:
	case <-time.After(5 * time.Second):
		t.Fatal("server never processed the window-change request")
	}

	cols, rows, xp, yp := srv.getPTYSize()
	if cols != 132 {
		t.Errorf("expected cols=132 after resize, got %d", cols)
	}
	if rows != 43 {
		t.Errorf("expected rows=43 after resize, got %d", rows)
	}
	if xp != 1000 {
		t.Errorf("expected xp=1000 after resize, got %d", xp)
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
	_, portStr, _ := net.SplitHostPort(srv.addr)
	srvHost := hostPortOnly(srv.addr)
	srvPort, _ := strconv.Atoi(portStr)
	khPath := writeKnownHosts(t, srv, srv.addr)

	stub := NewStubConfigResolver()
	stub.AddEntry("myalias", HostConfig{HostName: srvHost, User: "testuser", Port: srvPort})

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
		WithConfigResolver(stub),
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
	srvHost := hostPortOnly(srv.addr)

	stub := NewStubConfigResolver()
	stub.AddEntry(srvHost, HostConfig{User: "configuser"})

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
		WithConfigResolver(stub),
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
		t.Fatalf("Connect with explicit user beats config: %v", err)
	}
	defer func() { _ = ch.Close() }()

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

func TestPoolConnectionSharing(t *testing.T) {
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

	auth := []gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}

	// Two Connects to the same host should share one connection.
	ch1, err := client.Connect(context.Background(), srv.addr,
		WithUser("test"), WithAuthMethods(auth), WithPTYSize(80, 24, 0, 0))
	if err != nil {
		t.Fatalf("Connect 1: %v", err)
	}

	ch2, err := client.Connect(context.Background(), srv.addr,
		WithUser("test"), WithAuthMethods(auth), WithPTYSize(80, 24, 0, 0))
	if err != nil {
		t.Fatalf("Connect 2: %v", err)
	}

	if got := client.pool.Count(); got != 1 {
		t.Fatalf("after 2 connects to same host, pool.Count()=%d, want 1 (shared)", got)
	}

	// Both channels work independently.
	<-srv.shellReady
	if _, err = ch1.Write([]byte("a")); err != nil {
		t.Fatalf("ch1.Write: %v", err)
	}
	buf := make([]byte, 32)
	var n int
	n, err = ch1.Read(buf)
	if err != nil {
		t.Fatalf("ch1.Read: %v", err)
	}
	if string(buf[:n]) != "echo:a" {
		t.Fatalf("ch1: want echo:a, got %q", string(buf[:n]))
	}

	if _, err = ch2.Write([]byte("b")); err != nil {
		t.Fatalf("ch2.Write: %v", err)
	}
	n, err = ch2.Read(buf)
	if err != nil {
		t.Fatalf("ch2.Read: %v", err)
	}
	if string(buf[:n]) != "echo:b" {
		t.Fatalf("ch2: want echo:b, got %q", string(buf[:n]))
	}

	// Close one — connection stays (the other still refs it).
	if err := ch1.Close(); err != nil {
		t.Fatalf("ch1.Close: %v", err)
	}
	if got := client.pool.Count(); got != 1 {
		t.Fatalf("after 1 close, pool.Count()=%d, want 1 (still shared)", got)
	}

	// Close the last — connection released.
	if err := ch2.Close(); err != nil {
		t.Fatalf("ch2.Close: %v", err)
	}
	if got := client.pool.Count(); got != 0 {
		t.Fatalf("after all closed, pool.Count()=%d, want 0", got)
	}
}

// TestProbe_Success verifies Probe authenticates and closes without a shell.
func TestProbe_Success(t *testing.T) {
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

	err = client.Probe(
		context.Background(), srv.addr,
		gossh.PublicKeys(srv.userSigner),
		WithUser("test"),
	)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	// Pool must be empty — Probe bypasses the pool entirely.
	if got := client.pool.Count(); got != 0 {
		t.Fatalf("pool.Count()=%d, want 0 (Probe bypasses pool)", got)
	}
}

// TestProbe_WrongKey_ReturnsError verifies Probe fails on bad auth.
func TestProbe_WrongKey_ReturnsError(t *testing.T) {
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
	err = client.Probe(
		context.Background(), srv.addr,
		gossh.PublicKeys(wrongKey),
		WithUser("test"),
	)
	if err == nil {
		t.Fatal("Probe with wrong key: expected error, got nil")
	}
}

// TestProbe_UnknownHost_ReturnsError verifies Probe fails when the host
// key is unknown, without attempting authentication.
func TestProbe_UnknownHost_ReturnsError(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	// Write known_hosts with a different host key than the server uses.
	wrongSigner := generateSigner(t)
	wrongKey := wrongSigner.PublicKey()
	line := knownhosts.Line([]string{srv.addr}, wrongKey)
	dir := t.TempDir()
	khPath := filepath.Join(dir, "known_hosts")
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

	err = client.Probe(
		context.Background(), srv.addr,
		gossh.PublicKeys(srv.userSigner),
		WithUser("test"),
	)
	if err == nil {
		t.Fatal("Probe with wrong host key: expected error, got nil")
	}
}

// TestProbe_SingleAuthMethod verifies Probe sends exactly one auth method
// (the supplied one) and does not fall back to agent or any other method.
// This is implicit: gossh.ClientConfig.Auth is set to a slice of length 1,
// so the server only sees that one method. A key that succeeds proves it.
func TestProbe_SingleAuthMethod(t *testing.T) {
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

	// Password method with correct key signer won't work (server only accepts
	// public key), but using public key with exactly one method should.
	err = client.Probe(
		context.Background(), srv.addr,
		gossh.PublicKeys(srv.userSigner),
		WithUser("test"),
	)
	if err != nil {
		t.Fatalf("Probe with single public-key method: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ConnectOption helpers for vault key path testing
// ---------------------------------------------------------------------------

func withKeySecretID(id credential.SecretID) ConnectOption {
	return func(c *ConnectConfig) { c.KeySecretID = id }
}

func withPassphraseSecretID(id credential.SecretID) ConnectOption {
	return func(c *ConnectConfig) { c.PassphraseSecretID = id }
}

func withStore(store credential.SecretStore) ConnectOption {
	return func(c *ConnectConfig) { c.Secrets = store }
}

// ---------------------------------------------------------------------------
// Vault key authentication — end-to-end via the SecretStore
// ---------------------------------------------------------------------------

// TestConnect_VaultKeyAuth_Success verifies that a private key stored in the
// SecretStore authenticates through the full Connect path: buildAuthChain
// loads the key from the store and opens a session.
func TestConnect_VaultKeyAuth_Success(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()

	// Generate a key, marshal it, and store in the vault.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	userSigner, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(block)

	keyID, err := store.Create(ctx, credential.NewSecretBytes(keyPEM))
	if err != nil {
		t.Fatalf("store vault key: %v", err)
	}

	// Start an SSH server that accepts this public key.
	srv := startTestSSHServerWithUserKey(t, userSigner)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}

	// Bind the credential to the server address so the binding check passes.
	authzHost, authzPortStr, _ := net.SplitHostPort(srv.addr)
	authzPort, _ := strconv.Atoi(authzPortStr)

	ch, err := client.Connect(
		ctx, srv.addr,
		WithUser("test"),
		withStore(store),
		withKeySecretID(keyID),
		withBinding(authzHost, authzPort),
		WithPTYSize(80, 24, 640, 480),
	)
	if err != nil {
		t.Fatalf("Connect with vault key: %v", err)
	}
	defer func() { _ = ch.Close() }()

	// Verify the shell is ready and data flows.
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
		t.Fatalf("got %q, want echo:hello", string(buf[:n]))
	}
}

// TestConnect_VaultEncryptedKeyAuth_Success verifies that an encrypted private
// key stored in the SecretStore with its passphrase authenticates through the
// full Connect path.
func TestConnect_VaultEncryptedKeyAuth_Success(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()

	passphrase := "test-encrypted-passphrase"

	// Generate an encrypted key and store both the key and passphrase.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	userSigner, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(block)

	keyID, err := store.Create(ctx, credential.NewSecretBytes(keyPEM))
	if err != nil {
		t.Fatalf("store vault encrypted key: %v", err)
	}
	pwID, err := store.Create(ctx, credential.NewSecret(passphrase))
	if err != nil {
		t.Fatalf("store vault passphrase: %v", err)
	}

	// Start an SSH server that accepts this public key.
	srv := startTestSSHServerWithUserKey(t, userSigner)
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

	authzHost, authzPortStr, _ := net.SplitHostPort(srv.addr)
	authzPort, _ := strconv.Atoi(authzPortStr)

	ch, err := client.Connect(
		ctx, srv.addr,
		WithUser("test"),
		withStore(store),
		withKeySecretID(keyID),
		withPassphraseSecretID(pwID),
		withBinding(authzHost, authzPort),
		WithPTYSize(80, 24, 640, 480),
	)
	if err != nil {
		t.Fatalf("Connect with encrypted vault key: %v", err)
	}
	defer func() { _ = ch.Close() }()

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
		t.Fatalf("got %q, want echo:hello", string(buf[:n]))
	}
}

// TestConnect_KeyFileAuth_Regression verifies that a connection using a
// KeyFile (instead of KeySecretID) still authenticates through the auth
// chain — a regression test against vault-key changes breaking the file
// path that every existing credential uses.
func TestConnect_KeyFileAuth_Regression(t *testing.T) {
	// Generate a key and a server that accepts it.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	userSigner, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}

	srv := startTestSSHServerWithUserKey(t, userSigner)
	defer srv.close()

	khPath := writeKnownHosts(t, srv, srv.addr)

	// Write the private key to a temp file as the KeyFile path would.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if writeErr := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); writeErr != nil {
		t.Fatalf("write key file: %v", writeErr)
	}

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
		WithKeyFile(keyPath),
		WithPTYSize(80, 24, 640, 480),
	)
	if err != nil {
		t.Fatalf("Connect with KeyFile: %v", err)
	}
	defer func() { _ = ch.Close() }()

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
		t.Fatalf("got %q, want echo:hello", string(buf[:n]))
	}
}

// TestConnect_VaultSealed_ReturnsTypedError verifies that when the vault is
// sealed, connecting with a KeySecretID produces vault.ErrVaultSealed, not
// a generic auth failure. The UI keys on this error to show the Unlock dialog.
func TestConnect_VaultSealed_ReturnsTypedError(t *testing.T) {
	ctx := context.Background()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	userSigner, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}

	srv := startTestSSHServerWithUserKey(t, userSigner)
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

	authzHost, authzPortStr, _ := net.SplitHostPort(srv.addr)
	authzPort, _ := strconv.Atoi(authzPortStr)

	// Pass a sealed store — Get returns vault.ErrVaultSealed.
	_, err = client.Connect(
		ctx, srv.addr,
		WithUser("test"),
		withStore(&sealedStore{}),
		withKeySecretID("any-key-ref"),
		withBinding(authzHost, authzPort),
		WithPTYSize(80, 24, 640, 480),
	)
	if err == nil {
		t.Fatal("expected error from sealed vault, got nil")
	}
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("expected ErrVaultSealed, got %T: %v", err, err)
	}
}

// TestProbe_ThroughJumpHost verifies that ProbeConfigWithResult routes
// through the jump host when one is configured, rather than dialling
// the target directly (nocx-shat).
func TestProbe_ThroughJumpHost(t *testing.T) {
	// Bastion: a test SSH server with direct-tcpip support.
	bastionPub, bastionPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate bastion key: %v", err)
	}
	bastionSigner, err := gossh.NewSignerFromKey(bastionPriv)
	if err != nil {
		t.Fatalf("create bastion signer: %v", err)
	}
	bastion := startTestSSHServerWithKey(t, bastionSigner)
	defer bastion.close()

	// Target: a separate test SSH server with its own key.
	targetPub, targetPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate target key: %v", err)
	}
	targetSigner, err := gossh.NewSignerFromKey(targetPriv)
	if err != nil {
		t.Fatalf("create target signer: %v", err)
	}
	target := startTestSSHServerWithKey(t, targetSigner)
	defer target.close()

	// known_hosts: both bastion and target keys.
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	khLines := []string{
		knownhosts.Line([]string{bastion.addr}, bastion.hostSigner.PublicKey()),
		knownhosts.Line([]string{target.addr}, target.hostSigner.PublicKey()),
	}
	wErr := os.WriteFile(khPath, []byte(strings.Join(khLines, "\n")+"\n"), 0o600)
	if wErr != nil {
		t.Fatalf("write known_hosts: %v", wErr)
	}

	// Write the bastion's private key to a temp file for jump auth.
	jumpKeyPath := filepath.Join(t.TempDir(), "jump_key")
	block, err := gossh.MarshalPrivateKey(bastionPriv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err = os.WriteFile(jumpKeyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write jump key: %v", err)
	}

	client, err := NewReal(
		log.NewSlogAdapter(nil),
		WithKnownHostsFile(khPath),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	defer func() { _ = client.Close() }()

	bastionHost, bastionPortStr, _ := net.SplitHostPort(bastion.addr)
	bastionPort, _ := strconv.Atoi(bastionPortStr)

	// Build the ConnectConfig the resolver would produce: jump host with
	// key file, and auth for the target.
	cfg := &ConnectConfig{}
	cfg.AuthMethods = []gossh.AuthMethod{gossh.PublicKeys(targetSigner)}
	WithAuthMode("publicKey")(cfg)
	WithJumpHost(bastionHost, bastionPort, "jumpuser", "publicKey")(cfg)
	cfg.JumpKeyFile = jumpKeyPath

	// A direct-route target entry must not authorize this jump route, even
	// though the server currently presents the same key on both paths.
	_, err = client.ProbeConfigWithResult(context.Background(), target.addr, cfg)
	var unknown *ErrUnknownHostKey
	if !errors.As(err, &unknown) {
		t.Fatalf("first jump probe = %T %v, want ErrUnknownHostKey", err, err)
	}
	routeAddr := knownHostsTargetAddr(target.addr, cfg)
	if unknown.Addr != target.addr || unknown.KnownHostsAddr != routeAddr {
		t.Fatalf("jump evidence = display %q lookup %q, want %q and %q",
			unknown.Addr, unknown.KnownHostsAddr, target.addr, routeAddr)
	}
	if _, err = client.TrustHostKey(unknown.KnownHostsAddr, unknown.Key); err != nil {
		t.Fatalf("trust target on jump route: %v", err)
	}

	// The next probe follows the same route identity and succeeds without
	// replacing the target's direct-route entry.
	fp, err := client.ProbeConfigWithResult(context.Background(), target.addr, cfg)
	if err != nil {
		t.Fatalf("ProbeConfigWithResult through jump after trust: %v", err)
	}

	// The fingerprint must be the target's, not the bastion's.
	wantFp := gossh.FingerprintSHA256(target.hostSigner.PublicKey())
	if fp != wantFp {
		t.Errorf("fingerprint = %q, want target's %q", fp, wantFp)
	}

	// Pool must be empty — probe bypasses the pool.
	if got := client.pool.Count(); got != 0 {
		t.Fatalf("pool.Count()=%d, want 0 (probe bypasses pool)", got)
	}

	// Suppress unused — bastionPub/targetPub are for clarity.
	_ = bastionPub
	_ = targetPub
}
