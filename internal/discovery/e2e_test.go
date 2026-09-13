package discovery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/remoteprobe"
)

// TestDetector_OverWire_NormalHost is the composition check the fake tests
// cannot give: the REAL Detector, its real ladder, and the real command each
// rung is, against an in-process SSH server that answers exec requests the way
// a normal Linux host's shell would. The server returns the REAL measured ss
// fixture; the detector must select ss once, parse 9 listeners, and classify
// the mixed evidence correctly.
//
// # What changed when the transport moved, and what is proven where
//
// It used to dial with the coordinator's own ssh client and its DiscoveryConn
// lease. Nothing does that any more — the owner's invariant is that every ssh
// connection is made by this machine's helper, and the probes are named ops the
// helper answers — so the transport half of this test now lives with the helper
// (internal/helper/sshsvc's own tests drive the real `ssh` service against a
// real server) and what is driven HERE is the half that is still this package's:
// the ladder, the framing, the parsers and the five result states, over a real
// connection whose server answers real commands.
//
// The commands are composed from internal/remoteprobe — the same package the
// helper composes from — so a rename or a re-shaped rung that broke the ladder
// would fail here and not only in the helper's own tests.
func TestDetector_OverWire_NormalHost(t *testing.T) {
	var mu sync.Mutex
	var ran []string
	srv := startDiscoveryServer(t, func(cmd string) (stdout, stderr string, exit int) {
		mu.Lock()
		ran = append(ran, cmd)
		mu.Unlock()
		want, ok := remoteprobe.PortCommand(remoteprobe.PortSS)
		if !ok {
			t.Errorf("the ss probe has no command in remoteprobe")
		}
		if cmd != want {
			t.Errorf("exec command = %q, want the ss probe command %q", cmd, want)
		}
		return "NOCX-PD/1\n" + ssMixedFixture + "\nNOCX-PD/1\n", "", 0
	})

	lease := dialProbeLease(t, srv)
	d := NewDetector(lease, log.NewSlogAdapter(nil), WithSampleTimeout(5*time.Second))
	defer func() { _ = d.Close() }()

	s := d.Sample(context.Background())
	if s.Canceled {
		t.Fatal("sample canceled unexpectedly")
	}
	if s.State != StateAvailable {
		t.Fatalf("state = %v, want available; classification=%q probes=%v", s.State, s.Classification, s.ProbesTried)
	}
	if s.Probe != string(remoteprobe.PortSS) {
		t.Fatalf("probe = %q, want ss (ladder selected once)", s.Probe)
	}
	if len(s.Listeners) != 9 {
		t.Fatalf("listeners = %d, want 9", len(s.Listeners))
	}
	known, denied := 0, 0
	for _, l := range s.Listeners {
		switch l.Process.Evidence {
		case EvidenceKnown:
			known++
		case EvidencePermissionDenied:
			denied++
		}
	}
	if known != 3 || denied != 6 {
		t.Errorf("known = %d, denied = %d, want 3/6", known, denied)
	}

	// A second sample reuses the selected probe — no re-selection execs.
	s2 := d.Sample(context.Background())
	if s2.State != StateAvailable {
		t.Fatalf("second state = %v, want available", s2.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 2 {
		t.Errorf("ran %d commands, want 2 (selection is once per connection, then the selected probe only): %v", len(ran), ran)
	}
}

// dialLease is the probe lease this test drives the detector through: a real
// ssh connection and a real exec per probe, composed from remoteprobe.
//
// It stands where the helper stands in production, and it is deliberately the
// SMALLEST thing that can: no pool, no credentials beyond a generated key, no
// classification. What it shares with the helper's own probe runner is the only
// thing that has to agree — the command each named probe is.
type dialLease struct {
	client *gossh.Client
	done   chan struct{}
}

func dialProbeLease(t *testing.T, srv *discoveryServer) *dialLease {
	t.Helper()
	client, err := gossh.Dial("tcp", srv.addr, &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(srv.userKey)},
		HostKeyCallback: gossh.FixedHostKey(srv.hostKey.PublicKey()),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	l := &dialLease{client: client, done: make(chan struct{})}
	t.Cleanup(func() {
		_ = client.Close()
		close(l.done)
	})
	return l
}

func (l *dialLease) Sample(ctx context.Context, probe ProbeName) (*ExecResult, error) {
	command, ok := remoteprobe.PortCommand(probe)
	if !ok {
		return nil, &ExecError{Kind: ExecErrCommandTooLong}
	}
	sess, err := l.client.NewSession()
	if err != nil {
		return nil, &ExecError{Kind: ExecErrSessionRefused, Err: err}
	}
	defer func() { _ = sess.Close() }()

	out, err := sess.Output(command)
	result := &ExecResult{Stdout: out}
	var exitErr *gossh.ExitError
	switch {
	case err == nil:
		return result, nil
	case errors.As(err, &exitErr):
		result.ExitStatus = exitErr.ExitStatus()
		return result, nil
	}
	return nil, &ExecError{Kind: ExecErrConnectionLost, Err: err}
}

func (l *dialLease) Done() <-chan struct{} { return l.done }
func (l *dialLease) LostErr() error        { return nil }
func (l *dialLease) Close() error          { return l.client.Close() }

// ---------------------------------------------------------------------------
// Minimal in-process SSH server with scripted exec. The internal/ssh test
// server is not importable across packages; this is a trimmed version for
// the composition check only.
// ---------------------------------------------------------------------------

type discoveryServer struct {
	t        *testing.T
	hostKey  gossh.Signer
	userKey  gossh.Signer
	addr     string
	listener net.Listener
	handler  func(cmd string) (stdout, stderr string, exit int)
}

func startDiscoveryServer(t *testing.T, handler func(cmd string) (stdout, stderr string, exit int)) *discoveryServer {
	t.Helper()
	hostKey := generateSigner(t)
	userKey := generateSigner(t)

	config := &gossh.ServerConfig{
		PublicKeyCallback: func(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if string(key.Marshal()) == string(userKey.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	config.AddHostKey(hostKey)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &discoveryServer{
		t:        t,
		hostKey:  hostKey,
		userKey:  userKey,
		addr:     listener.Addr().String(),
		listener: listener,
		handler:  handler,
	}
	t.Cleanup(func() { _ = listener.Close() })
	go srv.acceptLoop(config)
	return srv
}

func (s *discoveryServer) acceptLoop(config *gossh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn, config)
	}
}

func (s *discoveryServer) serveConn(conn net.Conn, config *gossh.ServerConfig) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer func() { _ = sshConn.Close() }()
	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "session":
			ch, reqs, err := newChan.Accept()
			if err != nil {
				return
			}
			go s.handleSession(ch, reqs)
		default:
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
		}
	}
}

func (s *discoveryServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "exec":
			var m struct{ Command string }
			if err := gossh.Unmarshal(req.Payload, &m); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			stdout, stderr, exit := s.handler(m.Command)
			_, _ = ch.Write([]byte(stdout))
			_, _ = ch.Stderr().Write([]byte(stderr))
			_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{uint32(exit)})) //nolint:gosec // SSH exit statuses are 0-255
			_ = ch.Close()
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

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
