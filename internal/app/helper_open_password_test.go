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
// half of it — WithoutPasswordPrompt is what takes the rung off a probe's dial
// — and this is the same rule at the seam the helper selection added.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"github.com/shady2k/nocx/internal/credential"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/remoteprobe"
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

	// rootDir is what an `sftp` subsystem serves, and EMPTY means this fixture
	// serves none (every test that predates nocx-50w7p.12 asks for no
	// subsystem, and a fixture that answered one would be answering a question
	// nobody put).
	rootDir string
	// home is what the `home` probe answers — the account's $HOME, which is
	// deliberately NOT rootDir in the tests that use it: the whole point of
	// nocx-50w7p.15 is that a session activates under the former and an sftp
	// subsystem starts in the latter. Empty means the fixture answers nothing,
	// which is the "the host could not say" refusal.
	home string
	// refuseExec answers every exec request false, which is what a host with
	// ForceCommand or a restricted shell does: the home probe cannot run, and
	// the publish must refuse rather than guess.
	refuseExec bool
	// closeOnSFTPWrite closes the whole connection the moment the sftp
	// subsystem's first packet arrives — a transport lost MID-PUBLISH, and
	// deterministic: the subsystem handshake has already succeeded, so the
	// failure lands on the bytes rather than on the open.
	closeOnSFTPWrite bool
	// refuseSubsystem answers the sftp request false, which is what a host
	// with no sftp-server does.
	refuseSubsystem bool
	// conns counts the connections that finished AUTHENTICATION, and
	// subsystems records the subsystem names asked for in order. They are the
	// server's own view — "how many times did somebody authenticate" is not a
	// fact a client can report about itself.
	conns      int
	subsystems []string
	// directTargets is the addresses this host was asked to reach on a
	// `direct-tcpip` channel, in order. It is what lets one fixture stand as a
	// BASTION: a jump hop's whole job is to connect to an address on its own
	// network, so a route through this host shows up here as the next hop's
	// address — which is the evidence that a dial went through it rather than
	// straight at the destination.
	directTargets []string

	mu        sync.Mutex
	passwords []string
	execs     int
	// acceptedKey, when set, is a public key this host authenticates: the
	// public-key half of the same fixture, for the credentials that prove a key
	// rather than present a password. keyFingerprints records every key that
	// was OFFERED, accepted or not, which is how a test tells "this key
	// authenticated" from "something did".
	acceptedKey     gossh.PublicKey
	keyFingerprints []string
	execSeen        []string

	liveMu sync.Mutex
	live   map[*gossh.ServerConn]struct{}
}

func startPasswordSSHServer(t *testing.T) *pwSSHServer {
	t.Helper()
	return startPasswordSFTPSSHServer(t, "")
}

// startPasswordSFTPSSHServer is the same fixture with an `sftp` subsystem over
// root (empty root = no subsystem), which is the destination shape the Files
// panel and the bundle publish need.
func startPasswordSFTPSSHServer(t *testing.T, root string) *pwSSHServer {
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
	s := &pwSSHServer{
		hostSigner: hostSigner, listener: listener, addr: listener.Addr().String(),
		rootDir: root, live: make(map[*gossh.ServerConn]struct{}),
	}

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
		// A host that accepts a KEY authenticates the offer before it asks for
		// a signature, so the accepted key is read here rather than captured at
		// construction: a test that arms the fixture after it starts is asking
		// about the same host.
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			s.mu.Lock()
			s.keyFingerprints = append(s.keyFingerprints, gossh.FingerprintSHA256(key))
			accepted := s.acceptedKey
			s.mu.Unlock()
			if accepted == nil {
				return nil, errors.New("this host takes no key")
			}
			if !bytes.Equal(accepted.Marshal(), key.Marshal()) {
				return nil, errors.New("not the key this host accepts")
			}
			return nil, nil
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
	// A connection that reaches here has AUTHENTICATED: NewServerConn returns
	// only after the handshake and the auth exchange are done. That is the
	// counter the Files acceptance reads.
	s.mu.Lock()
	s.conns++
	s.mu.Unlock()
	s.liveMu.Lock()
	s.live[sshConn] = struct{}{}
	s.liveMu.Unlock()
	defer func() {
		s.liveMu.Lock()
		delete(s.live, sshConn)
		s.liveMu.Unlock()
	}()
	go gossh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() == "direct-tcpip" {
			// What a jump host serves: the far side connects to a target on
			// ITS network and carries the bytes.
			go s.serveDirectTCPIP(newChan)
			continue
		}
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

// serveDirectTCPIP proxies one `direct-tcpip` channel to the address it names,
// which is the whole of a bastion's behaviour. The dial happens before the
// channel is accepted, so a target that refuses rejects the open itself — the
// refusal the dialing side classifies.
func (s *pwSSHServer) serveDirectTCPIP(newChan gossh.NewChannel) {
	var p struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := gossh.Unmarshal(newChan.ExtraData(), &p); err != nil {
		_ = newChan.Reject(gossh.ConnectionFailed, "malformed direct-tcpip payload")
		return
	}
	target := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	s.mu.Lock()
	s.directTargets = append(s.directTargets, target)
	s.mu.Unlock()

	conn, err := net.DialTimeout("tcp", target, 15*time.Second)
	if err != nil {
		_ = newChan.Reject(gossh.ConnectionFailed, err.Error())
		return
	}
	ch, chReqs, err := newChan.Accept()
	if err != nil {
		_ = conn.Close()
		return
	}
	go gossh.DiscardRequests(chReqs)
	go func() {
		defer func() { _ = ch.Close() }()
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(ch, conn)
	}()
	_, _ = io.Copy(conn, ch)
}

// handleSession answers what the callers of this fixture ask of a far side: the
// platform probe's pty-less exec, the session's own shell, and — since
// nocx-50w7p.12 — the `sftp` subsystem the Files panel's lease runs on.
//
// The requests are handled in THIS goroutine rather than beside the echo loop,
// because a session serves one of the three and the choice is only known once
// the request arrives: an echo loop reading the same channel as an sftp server
// is two readers on one byte stream, and the first sftp INIT would be echoed
// back as text.
func (s *pwSSHServer) handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			s.echoLoop(ch)
			return
		case "subsystem":
			var m struct{ Subsystem string }
			if err := gossh.Unmarshal(req.Payload, &m); err != nil || m.Subsystem != "sftp" || s.rootDir == "" || s.refuseSubsystem {
				_ = req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.subsystems = append(s.subsystems, m.Subsystem)
			s.mu.Unlock()
			_ = req.Reply(true, nil)
			s.serveSFTP(ch)
			return
		case "exec":
			var m struct{ Command string }
			_ = gossh.Unmarshal(req.Payload, &m)
			if s.refuseExec {
				_ = req.Reply(false, nil)
				return
			}
			s.mu.Lock()
			s.execs++
			s.execSeen = append(s.execSeen, m.Command)
			s.mu.Unlock()
			// The exec surface is COMMAND-AWARE, and it has to be: this
			// fixture now stands behind the helper's named probes, and the
			// home probe (`echo $HOME`) is a different question with a
			// different answer from the platform one. Answering every
			// command with a platform triple would make `ProbeLease.Home`
			// return "Linux x86_64" and the publisher write the bundle into
			// a directory nobody named.
			switch m.Command {
			case remoteprobe.UnameCommand:
				_ = req.Reply(true, nil)
				_, _ = ch.Write([]byte("Linux x86_64\n"))
			case remoteprobe.HomeCommand, remoteprobe.HomeFallbackCommand:
				_ = req.Reply(true, nil)
				// The trailing newline is the shell's, and the probe trims.
				_, _ = ch.Write([]byte(s.home + "\n"))
			default:
				// A command this fixture does not answer is refused rather
				// than satisfied with a plausible-looking line.
				_ = req.Reply(false, nil)
				return
			}
			_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{Status: 0}))
			_ = ch.Close()
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// echoLoop mirrors the interactive session: whatever is written comes back
// unchanged, which is all the pane-open tests ask of the far side.
func (s *pwSSHServer) echoLoop(ch gossh.Channel) {
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

// serveSFTP serves the root directory over the channel, which is the far side
// the Files panel's lease speaks to.
//
// `closeOnSFTPWrite` is the transport-lost-mid-publish knob: the subsystem
// handshake has already succeeded by the time this runs, so closing the
// connection here drops the transport UNDER the bytes rather than refusing the
// open. It is a separate fact from `refuseSubsystem` on purpose — "the host has
// no sftp-server" and "the sftp-server went away while you were writing" are
// answered by different code paths, and a test that conflated them would prove
// neither.
func (s *pwSSHServer) serveSFTP(ch gossh.Channel) {
	if s.closeOnSFTPWrite {
		buf := make([]byte, 4096)
		_, _ = ch.Read(buf)
		s.killConns()
		_ = ch.Close()
		return
	}
	server, err := pkgsftp.NewServer(ch, pkgsftp.WithServerWorkingDirectory(s.rootDir))
	if err != nil {
		_ = ch.Close()
		return
	}
	_ = server.Serve()
	_ = server.Close()
}

func (s *pwSSHServer) authAttempts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.passwords...)
}

// killConns closes every established server-side connection, which is the
// fixture's way of losing the transport under a lease that is mid-conversation.
//
// It lives here rather than in the suite that first needed it (the Files
// acceptance) because it is the fixture's own instrument: the bundle publish's
// mid-write loss needs exactly this, in a build where the Files acceptance's
// file is not compiled.
func (s *pwSSHServer) killConns() {
	s.liveMu.Lock()
	conns := make([]*gossh.ServerConn, 0, len(s.live))
	for c := range s.live {
		conns = append(conns, c)
	}
	s.liveMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
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
	reg, helperReg := openPasswordStack(t, srv, noLocalHelperProbes{t: t})

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
// secret) resolves without interrupting anyone, so the platform probe must
// still be ENTERED and still be handed the destination — only the rung that
// would stop a person is off.
//
// Without this, "the probe must not prompt" could be satisfied by a probe that
// no longer probes. What the probe then DOES with that credential — authenticating
// against the far side and running the uname command — is the helper's half, and
// it is asserted where a helper exists: internal/helper/sshsvc's probe tests
// drive a real ssh server with a password only the scripted coordinator has.
func TestOpenPath_ProbeStillRunsOnARememberedPassword(t *testing.T) {
	srv := startPasswordSSHServer(t)
	probes := &recordingProbeSource{}
	reg, helperReg := openPasswordStack(t, srv, probes)

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
	asked := probes.asked()
	if len(asked) != 1 {
		t.Fatalf("the platform probe was entered %d time(s), want 1: the suppression stopped the probe, not just the prompt", len(asked))
	}
	if asked[0].User != "e2euser" || net.JoinHostPort(asked[0].Host, strconv.Itoa(asked[0].Port)) != srv.addr {
		t.Errorf("the probe was handed %+v, want the destination the profile resolved (%s)", asked[0], srv.addr)
	}
	if asked[0].Identity.Credential.Ref == "" {
		t.Error("the probe was handed no credential reference: a probe that cannot authenticate is not a probe")
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
func openPasswordStack(t *testing.T, srv *pwSSHServer, probes probeHelperSource) (*session.Reg, *helperRegistry) {
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
	// Every lease this dispatch serves is this machine's helper's, and this
	// stack has no local daemon — so the lease half is the failing double
	// above. Nothing in this test's path opens one (it drives the AUTH ladder
	// of a pane open), which is what makes the substitution unreachable code
	// with a name rather than a weakened assertion.
	lanes := installLeaseRoutes{
		viaLocal: noLocalHelperLease{t: t},
		// The platform probe is this machine's helper's now (nocx-50w7p.9).
		// This stack has no daemon, so the route is the caller's: a test that
		// asserts the probe RUNS passes a recording source, and one that only
		// drives the pane's auth ladder passes the failing default.
		probes: &helperProbes{local: probes, resolve: client},
	}
	_, helperReg := helperGitFactory(lanes, refusingArtifactSource{}, consentStore, installStore, discardLogger())
	helperReg.registry = reg
	return reg, helperReg
}

// noLocalHelperLease is this machine's helper for a stack that has none: the
// install lease, the file panel's and the git factory's exec lane. It FAILS the
// test if it is ever reached, which is the honest shape — a stand-in that
// answered would hide the day the pane-open path starts needing a helper lease,
// and this test's subject is the auth ladder, not the install, the files or the
// lane.
type noLocalHelperLease struct{ t *testing.T }

func (n noLocalHelperLease) HelperInstallConn(context.Context, string, ...ssh.ConnectOption) (ssh.HelperInstallConn, error) {
	n.t.Error("this stack has no local helper, so no install lease can be acquired")
	return nil, errors.New("no local helper in this stack")
}

func (n noLocalHelperLease) FSConn(context.Context, string, ...ssh.ConnectOption) (ssh.FSConn, error) {
	n.t.Error("this stack has no local helper, so no sftp lease can be acquired")
	return nil, errors.New("no local helper in this stack")
}

func (n noLocalHelperLease) LaneConn(context.Context, string, proto.Machine, proto.GenerationID, ...ssh.ConnectOption) (helperclient.HelperConn, error) {
	n.t.Error("this stack has no local helper, so no lane can be opened")
	return nil, errors.New("no local helper in this stack")
}

// noLocalHelperProbes is the probe half of a stack that has no daemon: no
// helper, so no lease. It fails the test if it is reached, which is the honest
// shape for a test whose subject is the pane's auth ladder — a stand-in that
// answered would hide the day that path starts needing a probe.
type noLocalHelperProbes struct{ t *testing.T }

func (n noLocalHelperProbes) probeHelper(context.Context) (probeHelper, error) {
	n.t.Error("this stack has no local helper, so no probe lease can be acquired")
	return nil, errors.New("no local helper in this stack")
}

// recordingProbeSource stands where this machine's helper stands, and records
// what it was asked.
//
// The platform probe answers a credential question the COORDINATOR resolves and
// the helper presents (the suppression travels with the resolution), and the
// assertion this source serves is about that pair: the probe is ENTERED with a
// remembered password and no ask. Whether the far side then authenticates is the
// helper's half of the same criterion, and it is asserted where a helper exists
// — internal/helper/sshsvc's probe tests drive a real server with a password
// only the scripted coordinator has.
type recordingProbeSource struct {
	mu      sync.Mutex
	targets []proto.SSHDestination
}

func (r *recordingProbeSource) probeHelper(context.Context) (probeHelper, error) { return r, nil }

func (r *recordingProbeSource) AcquireProbeLease(_ context.Context, params proto.LeaseParams) (probeCommands, error) {
	r.mu.Lock()
	r.targets = append(r.targets, params.Destination)
	r.mu.Unlock()
	return &fakeProbeCommands{}, nil
}

func (r *recordingProbeSource) asked() []proto.SSHDestination {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proto.SSHDestination(nil), r.targets...)
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
