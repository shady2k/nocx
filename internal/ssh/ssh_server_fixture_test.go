//go:build nocx_local_ssh

package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
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
				s.mu.Unlock()
				s.shellReadyDo.Do(func() { close(s.shellReady) })

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

// memSecretStore is the in-memory credential.Resolver the auth-chain and
// prompt tests both stand in for a vault: Create mints an id, and Resolve
// answers the stored value through Secret.Use like a real one does. It is
// here, beside the client fixtures and for the same reason — the untagged
// password_prompt_test.go needs it, and the untagged pass is the build where
// a tagged file does not exist.
//
// It lives here rather than in the fixture file both builds compile, because
// every caller is tagged: the untagged pass would report it as unused.
type memSecretStore struct {
	mu   sync.Mutex
	m    map[credential.SecretID]credential.Secret
	next int
}

func (s *memSecretStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := credential.SecretID(fmt.Sprintf("mem-%d", s.next))
	if s.m == nil {
		s.m = make(map[credential.SecretID]credential.Secret)
	}
	s.m[id] = value
	return id, nil
}

func (s *memSecretStore) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return v, nil
}

func (s *memSecretStore) Resolve(ctx context.Context, id credential.SecretID, why credential.Stance) (credential.Secret, error) {
	return credential.NewResolver(s, nil, nil).Resolve(ctx, id, why)
}

func (s *memSecretStore) Delete(_ context.Context, id credential.SecretID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

func (s *memSecretStore) Exists(_ context.Context, id credential.SecretID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	return ok, nil
}

func newTestStore() *memSecretStore {
	return &memSecretStore{}
}

// newTestRealClient builds a RealClient with test-safe defaults.
func newTestRealClient(t *testing.T) *RealClient {
	t.Helper()
	dir := t.TempDir()
	rc, err := NewReal(
		log.NewSlogAdapter(nil), // nil handler → slog falls back
		WithKnownHostsFile(filepath.Join(dir, "known_hosts")),
		WithConfigResolver(NewStubConfigResolver()),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	return rc
}
