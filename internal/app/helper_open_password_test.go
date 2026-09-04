package app

// The connection password reaches authentication exactly once per open.
//
// The helper-hosted PTY selection (OpenHosted) runs a platform probe BEFORE
// the ordinary open, and that probe dials the destination. A dial that
// carries the interactive rung asks the user; the probe then drops its pool
// lease, the pooled connection closes with it, and the session's own dial
// asks a SECOND time — a prompt nobody is waiting for, on an open that is
// already blocked. The user answered once and the answer never reached the
// authentication that matters.
//
// The invariant is an interval, not a moment: from the first dial of an open
// until the session exists, exactly one ask is raised and the password it
// returns is the one the server authenticates. internal/ssh already states
// half of it — TestPromptRung_ProbeNeverFiresTheAsk pins that a probe must
// not block on user input — and this is the same rule at the seam the helper
// selection added.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const openPasswordFixturePassword = "e2e-password-42"

// ---------------------------------------------------------------------------
// A password-only SSH server: the destination shape the connection-password
// ask exists for. It counts the password attempts it authenticates, so the
// test can say what the SERVER saw and not only what the client intended.
// ---------------------------------------------------------------------------

type pwSSHServer struct {
	hostSigner gossh.Signer
	listener   net.Listener
	addr       string

	mu        sync.Mutex
	passwords []string
	execs     int
}

func startPasswordSSHServer(t *testing.T) *pwSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &pwSSHServer{hostSigner: hostSigner, listener: listener, addr: listener.Addr().String()}

	config := &gossh.ServerConfig{
		PasswordCallback: func(_ gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			s.mu.Lock()
			s.passwords = append(s.passwords, string(password))
			s.mu.Unlock()
			if string(password) == openPasswordFixturePassword {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
	}
	config.AddHostKey(hostSigner)
	go s.acceptLoop(config)
	t.Cleanup(func() { _ = listener.Close() })
	return s
}

func (s *pwSSHServer) acceptLoop(config *gossh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn, config)
	}
}

func (s *pwSSHServer) serveConn(conn net.Conn, config *gossh.ServerConfig) {
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
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			return
		}
		go s.handleSession(ch, chReqs)
	}
	_ = sshConn.Close()
}

// handleSession answers what the two callers of this fixture ask of a far
// side: the platform probe's pty-less exec, and the session's own shell.
func (s *pwSSHServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	go func() {
		for req := range reqs {
			switch req.Type {
			case "pty-req", "shell":
				_ = req.Reply(true, nil)
			case "exec":
				_ = req.Reply(true, nil)
				s.mu.Lock()
				s.execs++
				s.mu.Unlock()
				// The probe reads one line and expects a platform triple;
				// anything else fails the probe, which is a legitimate
				// outcome for this test and never the thing it measures.
				_, _ = ch.Write([]byte("Linux x86_64\n"))
				_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{Status: 0}))
				_ = ch.Close()
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()
	buf := make([]byte, 4096)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			_, _ = ch.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *pwSSHServer) authAttempts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.passwords...)
}

// execCount is how many pty-less exec requests the far side served. The
// platform probe is the only thing in this test that issues one, so a
// non-zero count is the probe having authenticated and reached the host.
func (s *pwSSHServer) execCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.execs
}

// ---------------------------------------------------------------------------
// The user at the other end of the ask.
// ---------------------------------------------------------------------------

type countingPasswordRequester struct {
	mu       sync.Mutex
	answer   string
	requests []ssh.PasswordRequest
}

func (c *countingPasswordRequester) RequestConnectionPassword(_ context.Context, req ssh.PasswordRequest) (ssh.PasswordAnswer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	return ssh.PasswordAnswer{Password: c.answer}, nil
}

func (c *countingPasswordRequester) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// refusingArtifactSource ships no helper for any platform: the selection can
// never resolve to helper, so this test only ever exercises the fall-through
// to the ordinary open — which is the path every password profile takes.
type refusingArtifactSource struct{}

func (refusingArtifactSource) Artifact(deploy.Platform) ([]byte, string, error) {
	return nil, "", deploy.ErrUnsupportedPlatform
}

type openPasswordPTYFactory struct{ stub *pty.Stub }

func (f *openPasswordPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	return f.stub, nil
}

// TestOpenPath_PasswordAskFiresOncePerOpen drives the open path in the order
// the transport drives it — the helper selection first, then the registry's
// own open when the selection declines — against a destination whose only
// credential is a password the user must type.
//
// It asserts the whole interval: one ask raised, and the password that ask
// returned is the one the server authenticated. Two asks means the second
// prompt is standing in front of a user who has already answered, and the
// open blocks behind it.
func TestOpenPath_PasswordAskFiresOncePerOpen(t *testing.T) {
	srv := startPasswordSSHServer(t)
	reg, helperReg := openPasswordStack(t, srv)

	asker := &countingPasswordRequester{answer: openPasswordFixturePassword}
	cfg := openPasswordConfig(srv, &ssh.ConnectConfig{
		User:              "e2euser",
		AuthMode:          "password",
		ConnectionName:    "Password Proof",
		PasswordRequester: asker,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, selected, err := helperReg.OpenHosted(ctx, cfg); selected {
		t.Fatalf("the helper selected a destination it ships no artifact for")
	} else if err != nil {
		t.Fatalf("OpenHosted declined with an error: %v", err)
	}

	sess, err := reg.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("the session did not open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(sess.ID()) })

	if got := asker.count(); got != 1 {
		t.Errorf("the user was asked for the connection password %d times, want exactly 1", got)
	}
	attempts := srv.authAttempts()
	if len(attempts) == 0 {
		t.Fatal("the server authenticated no password at all: the answer never reached authentication")
	}
	for i, pw := range attempts {
		if pw != openPasswordFixturePassword {
			t.Errorf("auth attempt %d sent %q, want the password the user supplied", i, pw)
		}
	}
}

func writeKnownHostsFor(t *testing.T, path string, srv *pwSSHServer) {
	t.Helper()
	line := knownhosts.Line([]string{srv.addr}, srv.hostSigner.PublicKey())
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
}

// TestOpenPath_ProbeStillRunsOnARememberedPassword is the other end of the
// same rule, and the reason the suppression is narrow. A destination whose
// password has been remembered (ADR-0017: the profile references a vault
// secret) authenticates without interrupting anyone, so the platform probe
// must still dial it, still reach the far host and still be able to select
// the helper. Only the rung that would stop a person is off.
//
// Without this, "the probe must not prompt" could be satisfied by a probe
// that no longer probes.
func TestOpenPath_ProbeStillRunsOnARememberedPassword(t *testing.T) {
	srv := startPasswordSSHServer(t)
	reg, helperReg := openPasswordStack(t, srv)

	asker := &countingPasswordRequester{answer: "the ask must never fire"}
	cfg := openPasswordConfig(srv, &ssh.ConnectConfig{
		User:           "e2euser",
		AuthMode:       "password",
		ConnectionName: "Password Proof",
		Secrets:        rememberedPassword{value: openPasswordFixturePassword},
		SecretID:       "sec:remembered:1",
		// The credential may only be spent on the endpoint its profile
		// identifies; the resolver stamps this, so the test does too.
		AuthorizedEndpoint: srv.addr,
		PasswordRequester:  asker,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, selected, err := helperReg.OpenHosted(ctx, cfg); selected {
		t.Fatalf("the helper selected a destination it ships no artifact for")
	} else if err != nil {
		t.Fatalf("OpenHosted declined with an error: %v", err)
	}

	sess, err := reg.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("the session did not open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(sess.ID()) })

	if got := asker.count(); got != 0 {
		t.Errorf("a remembered password still raised %d ask(s); the open must be silent", got)
	}
	if got := srv.execCount(); got == 0 {
		t.Error("the platform probe never reached the host: the suppression stopped the probe, not just the prompt")
	}
	for i, pw := range srv.authAttempts() {
		if pw != openPasswordFixturePassword {
			t.Errorf("auth attempt %d sent %q, want the remembered password", i, pw)
		}
	}
}

// rememberedPassword is the stored-secret half of ADR-0017: the material a
// remembered connection password resolves to, with no person in the loop.
type rememberedPassword struct{ value string }

func (r rememberedPassword) Resolve(context.Context, credential.SecretID, credential.Stance) (credential.Secret, error) {
	return credential.NewSecret(r.value), nil
}

// openPasswordStack builds the real composition the open path runs through —
// the session registry over a real ssh.RealClient, and the helper registry
// whose selection runs before it. The only double is the artifact source.
func openPasswordStack(t *testing.T, srv *pwSSHServer) (*session.Reg, *helperRegistry) {
	t.Helper()
	logger := log.NewSlogAdapter(discardLogger())

	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHostsFor(t, knownHosts, srv)

	client, err := ssh.NewReal(logger,
		ssh.WithKnownHostsFile(knownHosts),
		ssh.WithConfigResolver(reachResolver{}),
	)
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	reg := session.New(logger, &openPasswordPTYFactory{stub: pty.NewStub(logger)}).
		WithSSHFactory(&sshFactoryAdapter{client: client})

	consentStore := consent.NewStore(logger, storage.NewDocumentStore(t.TempDir()), "consent.json")
	installStore := consent.NewInstallStore(logger, storage.NewDocumentStore(t.TempDir()), "installs.json")
	_, helperReg := helperGitFactory(client, refusingArtifactSource{}, consentStore, installStore, discardLogger())
	helperReg.registry = reg
	return reg, helperReg
}

func openPasswordConfig(srv *pwSSHServer, remote *ssh.ConnectConfig) session.Config {
	return session.Config{
		Kind:      session.KindRemote,
		Host:      srv.addr,
		ProfileID: "ssh:password-proof",
		Cols:      80, Rows: 24,
		Remote: remote,
	}
}
