//go:build nocx_local_ssh

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
	"strings"
	"sync"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ---------------------------------------------------------------------------
// A password-only SSH server: the destination shape the connection-password
// ask exists for. It counts the password attempts it authenticates, so the
// test can say what the SERVER saw and not only what the client intended.
// ---------------------------------------------------------------------------

type pwSSHServer struct {
	// counted is signalled each time a connection is counted, so a test can
	// wait for the count it expects instead of reading it before the
	// fixture's own goroutine has caught up (awaitConns, nocx-2v80t.10).
	counted chan struct{}

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
	// refuseShell answers the `shell` request false, which is what an account
	// with no login shell does: AUTHENTICATION SUCCEEDS and no session can be
	// started on it. It is the one knob under which a dial is expensive and the
	// OPEN is refused, which is the arm an open's cleanup is judged on
	// (nocx-k6p18.35): the probe authenticated, the pane's spawn did not, and
	// the reference the probe held has to be given back — a held lease that
	// outlives a failed open is a connection nobody will ever close.
	refuseShell bool
	// closeOnSFTPWrite closes the whole connection the moment the sftp
	// subsystem's first packet arrives — a transport lost MID-PUBLISH, and
	// deterministic: the subsystem handshake has already succeeded, so the
	// failure lands on the bytes rather than on the open.
	closeOnSFTPWrite bool
	// refuseSubsystem answers the sftp request false, which is what a host
	// with no sftp-server does.
	refuseSubsystem bool
	// acceptLaunchExec makes this host RUN a command that is not one of the
	// named probes — which on this fixture is the pane's own launch carrier,
	// the string a helper-hosted ssh pane starts its far shell with
	// (nocx-50w7p.21). It is a knob rather than the default because every
	// fixture that predates it is asking about a probe or a subsystem, and a
	// host that silently accepted arbitrary commands would answer a question
	// nobody put.
	//
	// What it buys is the far half of the publish's ordering claim: the command
	// is recorded, and `events` keeps it in the order the host saw it, so a
	// test can say the bundle was written BEFORE the launch that looks for it
	// was started.
	acceptLaunchExec bool
	// launchExecs is every launch command this host was asked to run, in order.
	launchExecs []string
	// events is what this host was asked to do, in the order it was asked:
	// `exec:<command>` for a probe, `subsystem:<name>` for a subsystem, and
	// `launch:<command>` for a launch. One list rather than three, because the
	// fact worth asserting across them is their ORDER.
	events []string
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
		counted: make(chan struct{}, 1),
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
	select {
	case s.counted <- struct{}{}:
	default:
	}
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
			if s.refuseShell {
				// Refused, not answered: the account has no shell to run, and
				// the connection stays up — which is the state that separates
				// "this host cannot carry a pane" from "this host cannot be
				// reached".
				_ = req.Reply(false, nil)
				continue
			}
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
			s.events = append(s.events, "subsystem:"+m.Subsystem)
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
			// A command this host does not recognise is the pane's own launch
			// carrier, which the fixture runs only when a test asked it to
			// (acceptLaunchExec). Everything the launch then does with its
			// stdin is drained and kept, so the channel stays open and a reader
			// on the other side is never blocked by a host that stopped
			// reading.
			if m.Command != remoteprobe.UnameCommand &&
				m.Command != remoteprobe.HomeCommand && m.Command != remoteprobe.HomeFallbackCommand {
				if !s.acceptLaunchExec {
					_ = req.Reply(false, nil)
					return
				}
				s.mu.Lock()
				s.launchExecs = append(s.launchExecs, m.Command)
				s.events = append(s.events, "launch:"+m.Command)
				s.mu.Unlock()
				_ = req.Reply(true, nil)
				s.drainCh(ch)
				return
			}
			s.mu.Lock()
			s.execs++
			s.execSeen = append(s.execSeen, m.Command)
			s.events = append(s.events, "exec:"+m.Command)
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

// drainCh reads and discards whatever a launched command's stdin carries, so
// the host keeps the channel open and never blocks the side that is writing.
// It is the launch counterpart of echoLoop: echoing would put the pane's own
// frame back on the stream to be read as a line, which is a conversation this
// fixture is not simulating.
func (s *pwSSHServer) drainCh(ch gossh.Channel) {
	buf := make([]byte, 4096)
	for {
		if _, err := ch.Read(buf); err != nil {
			return
		}
	}
}

// launchCommands is every launch this host was asked to run, in order.
func (s *pwSSHServer) launchCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.launchExecs...)
}

// eventLog is what this host was asked to do, in the order it was asked — the
// observable for "was the bundle written before the launch that looks for it
// was started".
func (s *pwSSHServer) eventLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
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

// keyOffers is every public key this host was asked to consider, in order —
// the far side's own count of the authentication rounds a pane's ONE gesture
// cost (nocx-1ebrs.18). It counts OFFERS rather than successes: a refused
// credential never completes a handshake, so connCount cannot see it, and a
// refused offer is exactly the event that must not repeat.
func (s *pwSSHServer) keyOffers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keyFingerprints...)
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

func writeKnownHostsFor(t *testing.T, path string, srvs ...*pwSSHServer) {
	t.Helper()
	var b strings.Builder
	for _, srv := range srvs {
		b.WriteString(knownhosts.Line([]string{srv.addr}, srv.hostSigner.PublicKey()) + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
}

// awaitConns answers the authenticated-connection count once it has reached
// want, or what it is when the bound runs out. The fixture counts a
// connection in its own goroutine after the handshake returns there, and the
// client can finish its whole probe first: a count read straight after the
// probe raced that goroutine and read 0, 1 run in a full gate
// (nocx-2v80t.10). The caller still asserts the exact number, so a count
// that overshoots fails exactly as before.
func (s *pwSSHServer) awaitConns(want int) int {
	deadline := time.After(10 * time.Second)
	for {
		if got := s.connCount(); got >= want {
			return got
		}
		select {
		case <-s.counted:
		case <-deadline:
			return s.connCount()
		}
	}
}

// homeForKnownHosts gives this test its own $HOME and answers the path the
// coordinator's ssh client reads its host keys from: $HOME/.ssh/known_hosts.
//
// That is the only seam there is. RealClient resolves its own home and
// production has no need to name a path, so a test that wants its own store
// gives the client its own home, exactly as a person has one (nocx-oalo1.3.2).
// The directory is created; the file is the caller's to write.
func homeForKnownHosts(t *testing.T) (home, khPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home, knownHostsIn(t, home)
}

// knownHostsIn answers the product's known_hosts path under a home the test
// already owns, and makes sure its directory exists. The file itself is the
// caller's to write.
func knownHostsIn(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("prepare %s: %v", dir, err)
	}
	return filepath.Join(dir, "known_hosts")
}
