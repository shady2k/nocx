package app

// Live-sshd child-domain ASSEMBLY proofs (nocx-u7uh.29): the ssh child
// domain driven as one chain against a REAL sshd — a real ssh client reaches
// a real sshd over the composed grant line (ADR-0022), the -R reverse
// forward terminates at the lifecycle listener transport
// (internal/lifecyclechannel/listener.go), and the remote shell's own hello
// establishes the child (docs/lifecycle-protocol.md §9). The per-piece
// proofs already exist (composed-line quoting, listener transport, in-band
// installer, kernel grant flow); this file is the missing combination.
//
// The parent is harness-driven over the same publisher the production grant
// builder is wired to (internal/app/app.go wires WithGrantBuilder the same
// way), so the chain under test is exactly the production one from a
// validated domain_request to the child's establishment; the parent-shell
// hook behaviour (nested detect → request → suspend → exec → activate) is
// the shell scripts' own proven territory.
//
// Credential mechanism (the decision this bead carries, ADR-0025): OpenSSH
// resolves default identity paths AND known_hosts from the passwd home, not
// $HOME (measured on OpenSSH 10.4), so a fixture key cannot be dropped into
// a hermetic $HOME and the request shape does not need to grow an -i
// pass-through. The fixture's client key rides an in-process ssh agent
// (SSH_AUTH_SOCK), and a temp-dir `ssh` wrapper on PATH execs the real
// client with `-o UserKnownHostsFile=<fixture file>` — both test-scoped,
// neither touching the developer's home.

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	gosshagent "golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// assemblySID is the 32-hex session id the lane is registered with: the
// in-band dispatcher embeds it as NOCX_SESSION_ID (AD-7), and the nested
// shell is treated as the owning session.
const assemblySID = "aabbccddeeff00112233445566778899"

var (
	// childCapRe finds a per-epoch capability. It is no longer looked for in
	// the composed LINE — ADR-0035 took both bearers out of the command, and
	// requestChild now asserts their absence — but in FRAME 2, which the
	// delivery writes onto the parent's terminal after ownership of the
	// multiplex socket has been proven. That is where the harness learns the
	// child's capability, by watching exactly what the product delivers.
	childCapRe = regexp.MustCompile(`(?m)^([0-9a-f]{64})$`)
	// childForwardRe pulls the -R ports out of the composed line. The words
	// are shell-quoted one token at a time, so the port triple is its own
	// quoted argument.
	childForwardRe = regexp.MustCompile(`'127\.0\.0\.1:(\d+):127\.0\.0\.1:(\d+)'`)
)

// ---------------------------------------------------------------------------
// The parent's terminal, as the typed delivery sees it.
//
// In the product the frames travel on the pty of the session whose shell
// typed the `ssh`, and internal/session opens the window on it. This harness
// IS that shell — it runs the composed line under a pty of its own — so it
// provides the same window over that pty.
//
// The ordering is the interesting part, and it is the production ordering:
// the window is opened while the grant is being built, BEFORE the parent has
// the line, because once the parent has it `ssh` can start at once and a
// window opened afterwards could miss the loader's readiness token. The pty
// does not exist yet at that point, so the window waits for it.

type harnessWindow struct {
	mu       sync.Mutex
	buf      []byte
	sig      chan struct{}
	ptmx     *os.File
	attached chan struct{}
	written  []byte
	closed   bool
}

func newHarnessWindow() *harnessWindow {
	return &harnessWindow{sig: make(chan struct{}, 1), attached: make(chan struct{})}
}

// attach hands the window the terminal, once the composed line is running.
func (w *harnessWindow) attach(ptmx *os.File) {
	w.mu.Lock()
	w.ptmx = ptmx
	w.mu.Unlock()
	close(w.attached)
}

// feed is the tap: a COPY of the terminal's output, taking nothing from the
// reader that renders it.
func (w *harnessWindow) feed(p []byte) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	w.mu.Unlock()
	select {
	case w.sig <- struct{}{}:
	default:
	}
}

func (w *harnessWindow) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.After(timeout)
	for {
		w.mu.Lock()
		if i := bytes.IndexByte(w.buf, '\n'); i >= 0 {
			line := string(bytes.TrimRight(w.buf[:i], "\r"))
			w.buf = w.buf[i+1:]
			w.mu.Unlock()
			return line, nil
		}
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return "", io.EOF
		}
		select {
		case <-w.sig:
		case <-deadline:
			return "", errors.New("harness window: deadline")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (w *harnessWindow) Write(p []byte) (int, error) {
	<-w.attached
	w.mu.Lock()
	w.written = append(w.written, p...)
	ptmx := w.ptmx
	w.mu.Unlock()
	if ptmx == nil {
		return 0, errors.New("harness window: no terminal")
	}
	return ptmx.Write(p)
}

// QuarantineInput is a no-op here: this harness has no user typing into the
// terminal until it types itself, and it types only after the bootstrap has
// named its outcome.
func (w *harnessWindow) QuarantineInput() {}

func (w *harnessWindow) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	select {
	case w.sig <- struct{}{}:
	default:
	}
	return nil
}

// capability returns the per-epoch capability out of the secret frame the
// delivery wrote, waiting for it to be written.
func (w *harnessWindow) capability(t *testing.T) (lifecycle.Capability, bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		m := childCapRe.FindStringSubmatch(string(w.written))
		w.mu.Unlock()
		if m != nil {
			raw, err := hex.DecodeString(m[1])
			if err != nil {
				t.Fatalf("frame 2 capability %q does not decode: %v", m[1], err)
			}
			var cap lifecycle.Capability
			if len(raw) != len(cap) {
				t.Fatalf("frame 2 capability is %d bytes, want %d", len(raw), len(cap))
			}
			copy(cap[:], raw)
			return cap, true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return lifecycle.Capability{}, false
}

// harnessTerminals is the typedSessions seam: one window, for the one session
// this harness has.
type harnessTerminals struct{ win *harnessWindow }

func (h harnessTerminals) OpenBootstrapWindow(session.ID) (session.BootstrapWindow, error) {
	return h.win, nil
}

// fixturePort extracts the sshd port from the fixture address.
func (fx *liveSshd) fixturePort() int {
	_, portStr, err := net.SplitHostPort(fx.addr)
	if err != nil {
		panic(fmt.Sprintf("fixture addr %q: %v", fx.addr, err))
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		panic(fmt.Sprintf("fixture port %q: %v", portStr, err))
	}
	return port
}

// startInProcessAgent serves the fixture client key over a unix socket using
// the standard ssh-agent protocol, so the REAL ssh client the composed line
// invokes can authenticate to the fixture sshd without any option. This is
// the test-scoped credential mechanism the option decision (ADR-0025)
// records: OpenSSH resolves default identity paths from the passwd home, not
// $HOME, so a fixture key cannot be placed where the client will find it via
// HOME; the agent is the hermetic alternative that keeps the test off the
// developer's real ~/.ssh.
func startInProcessAgent(t *testing.T, fx *liveSshd) string {
	t.Helper()
	// NOT t.TempDir(): a unix socket path is bounded by sun_path, which is
	// 104 bytes on darwin, and t.TempDir() spells the test's full name into
	// the directory. On macOS the base is already
	// /var/folders/<11>/<26>/T/ (~49 bytes), so these three
	// TestLiveSshd_SSHChildAssembly_* names put the socket past the limit and
	// bind failed with "invalid argument" — on the platform this product
	// ships to first, while Linux's 108 bytes and short /tmp hid it
	// (nocx-cn86). A short fixed prefix keeps the path inside the bound
	// whatever the test is called.
	dir, err := os.MkdirTemp("", "nocx-agt")
	if err != nil {
		t.Fatalf("agent socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	if len(sock) > 100 {
		t.Fatalf("agent socket path is %d bytes, past darwin's sun_path bound: %s", len(sock), sock)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("agent socket: %v", err)
	}
	keyring := gosshagent.NewKeyring()
	if err := keyring.Add(gosshagent.AddedKey{
		PrivateKey: fx.clientRaw,
		Comment:    "fixture client key",
	}); err != nil {
		t.Fatalf("add fixture key to agent: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return // listener closed on cleanup
			}
			go func() { _ = gosshagent.ServeAgent(keyring, c) }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return sock
}

// installSSHWrapper makes the REAL ssh client the composed line invokes
// trust the fixture sshd WITHOUT touching the developer's home: OpenSSH
// resolves known_hosts from the passwd home, not $HOME (measured on
// OpenSSH 10.4, nocx-u7uh.29), so a fixture known_hosts cannot be dropped
// into a hermetic $HOME. The wrapper is a test-scoped mechanism: a temp-dir
// `ssh` on PATH that execs the real ssh binary with `-o UserKnownHostsFile=
// <fixture file>` (the equivalent of a user's own config option); identity
// rides the agent (startInProcessAgent). The client, the server, the -R
// forward and the remote shell are all real.
func installSSHWrapper(t *testing.T, fx *liveSshd) string {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatalf("find the real ssh client: %v", err)
	}
	dir := t.TempDir()
	// The fixture host key, in the canonical bracketed form the client
	// looks up when connecting to a non-default port.
	knownHosts := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{fmt.Sprintf("[127.0.0.1]:%d", fx.fixturePort())}, fx.hostKey)
	if err := os.WriteFile(knownHosts, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture known_hosts: %v", err)
	}
	wrapper := "#!/bin/sh\nexec " + shellQuoteForSh(realSSH) +
		" -o UserKnownHostsFile=" + shellQuoteForSh(knownHosts) + " \"$@\"\n"
	wrapperPath := filepath.Join(dir, "ssh")
	// #nosec G306 — the stand-in for ssh must be executable to be found
	// through PATH; temp dir, no secret.
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write ssh wrapper: %v", err)
	}
	return dir
}

// shellQuoteForSh single-quotes a path for the POSIX wrapper script.
func shellQuoteForSh(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// waitForResult polls cond until it is true or the timeout elapses,
// returning whether the condition was met (unlike waitFor, which fails the
// test). Used where the failure path must inspect the buffer that caused the
// timeout.
func waitForResult(t *testing.T, what string, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// sshChildHarness wires the PRODUCTION grant composition (the same
// kernel → publisher → grant-builder stack app.go wires) and drives the
// parent side of the protocol over a loopback listener transport, so the
// test controls exactly the frames the parent shell sends (request, suspend,
// activate) and observes the kernel read model in between.
type sshChildHarness struct {
	t           *testing.T
	kernel      *recordingKernel
	lane        lifecycle.LaneID
	parentLn    *lifecyclechannel.Listener
	conn        net.Conn
	dec         *lifecyclecodec.Decoder
	seq         uint64
	parent      lifecycle.DomainID
	parentEpoch uint64
	parentCap   lifecycle.Capability
	// The grant, once requestChild ran.
	child      lifecycle.DomainID
	childEpoch uint64
	bootstrap  string
	childCap   lifecycle.Capability
	// win is the parent's terminal as the typed delivery sees it: the
	// window the frames travel on, and where the harness learns the child's
	// capability from (ADR-0035 took it out of the composed line).
	win        *harnessWindow
	childLPort int // the listener transport's local port (the -R target)
	childRPort int // the remote bind the sshd opens (CPORT)
	// Every fact the publisher emitted, in order — the renderer's whole
	// input (nocx-mlyu). An attempt the kernel abandons as its domain
	// closes is never named by a later fact, so the sequence is the only
	// place its id survives.
	facts *factLog
}

// factLog records the published facts while acknowledging establishments
// exactly as ackingEmitter does — the renderer's two jobs, in the order it
// does them.
type factLog struct {
	pub *lifecyclepub.Publisher
	mu  sync.Mutex
	all []lifecyclepub.Fact
}

func (l *factLog) PublishLifecycle(f lifecyclepub.Fact) {
	l.mu.Lock()
	l.all = append(l.all, f)
	l.mu.Unlock()
	if f.Generation == "" || f.Domain == "" {
		return
	}
	_ = l.pub.AcknowledgeEstablishment(
		lifecycle.LaneID(f.Lane), lifecycle.DomainID(f.Domain), f.Epoch, f.Generation)
}

// attemptFor returns the id of the first attempt the given domain published
// under the given command, and whether one was ever published at all.
func (l *factLog) attemptFor(domain lifecycle.DomainID, command string) (lifecycle.AttemptID, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.all {
		if f.Domain != string(domain) || f.Attempt == nil {
			continue
		}
		if f.Attempt.Command == command {
			return lifecycle.AttemptID(f.Attempt.ID), true
		}
	}
	return "", false
}

// destinationOf returns the destination the published facts carry for the
// domain, and whether any fact named one.
func (l *factLog) destinationOf(domain lifecycle.DomainID) (lifecyclepub.Destination, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range l.all {
		if f.Domain == string(domain) && f.Destination != nil {
			return *f.Destination, true
		}
	}
	return lifecyclepub.Destination{}, false
}

func (l *factLog) commands(domain lifecycle.DomainID) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, f := range l.all {
		if f.Domain == string(domain) && f.Attempt != nil && f.Attempt.Command != "" {
			out = append(out, f.Attempt.Command)
		}
	}
	return out
}

func newSSHChildHarness(t *testing.T, fx *liveSshd) *sshChildHarness {
	t.Helper()
	logger := fx.log()
	// The kernel's randomness comes from the fixture, so a test that needs a
	// TAINTED capability and fence (the epic's canary) mints them through the
	// production RequestDomain rather than reaching past it.
	k := lifecycle.New(lifecycle.Options{Rand: fx.rand})
	sessions := newSessionRegistry()
	transports := newTransportRegistry()
	lane := lifecycle.LaneID("lane-ssh-child-assembly")
	sessions.register(lane, assemblySID)

	// The production grant wiring (app.go): the grant builder is the single
	// owner of "how do we reach a host"; the closure resolves the publisher
	// lazily, the way the composition root does.
	// The typed-ssh delivery, wired as the composition root wires it. The
	// control root is a short disposable directory: an over-long
	// ControlPath does not degrade to no-multiplexing, it kills the
	// connection, so the product refuses to build one and a test must not
	// hand it one either.
	//
	// SHORT is asked of the product, not assumed of $TMPDIR. This minted
	// the directory in os.TempDir(), which on macOS is a 48-character
	// per-user confinement directory — the expansion landed 4 bytes past
	// the bound, the wrapper refused with ReasonNoControlPath, and these
	// tests read that refusal as a broken grant on the macOS runner while
	// passing here. DefaultControlRoot now picks a base a socket fits in;
	// its directory is that base, so the disposable root inherits the one
	// answer instead of re-deriving it (and would inherit the next fix too).
	sockRoot, err := os.MkdirTemp(filepath.Dir(ssh.DefaultControlRoot()), "nx")
	if err != nil {
		t.Fatalf("socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockRoot) })
	win := newHarnessWindow()
	typed := &typedRunner{
		log: logger,
		wrapper: ssh.NewTypedWrapper(logger,
			ssh.NewSSHConfigResolver(logger, os.DevNull, ""), sockRoot),
		dial:     DialTypedMux,
		publish:  shellintegration.New(logger),
		sessions: harnessTerminals{win: win},
		probes:   defaultMasterProbes,
	}

	var pub *lifecyclepub.Publisher
	pub = lifecyclepub.New(k,
		lifecyclepub.WithGrantBuilder(newChildGrantBuilder(logger,
			func() *lifecyclepub.Publisher { return pub }, transports, sessions, typed)))
	facts := &factLog{pub: pub}
	pub.SetEmitter(facts)
	kernel := &recordingKernel{Publisher: pub}

	parentLn, lnErr := lifecyclechannel.NewListener(logger, pub)
	if lnErr != nil {
		t.Fatalf("parent listener: %v", lnErr)
	}
	t.Cleanup(func() { _ = parentLn.Close() })
	// The parent is a LOCAL adapter (the child's ssh runs on this machine),
	// which is the kind buildSSHChildBootstrap requires.
	transports.register(parentLn.TransportID(), transportKind{local: true})

	h, err := pub.RequestDomain(lane, nil, parentLn.TransportID())
	if err != nil {
		t.Fatalf("mint parent: %v", err)
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", parentLn.Port()))
	if err != nil {
		t.Fatalf("dial parent listener: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &sshChildHarness{
		t:           t,
		kernel:      kernel,
		lane:        lane,
		parentLn:    parentLn,
		conn:        conn,
		dec:         lifecyclecodec.NewDecoder(conn, lifecyclecodec.Config{}, nil),
		parent:      h.Domain,
		parentEpoch: h.Epoch,
		parentCap:   h.Capability,
		facts:       facts,
		win:         win,
	}
}

// send writes one authenticated parent frame with the next sequence number.
func (h *sshChildHarness) send(ev lifecycle.Event) {
	h.seq++
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       h.lane,
		Domain:     h.parent,
		Epoch:      h.parentEpoch,
		Sequence:   h.seq,
		Capability: h.parentCap,
		Event:      ev,
	}
	if _, err := lifecyclecodec.Encode(h.conn, env); err != nil {
		h.t.Fatalf("encode %s: %v", ev.Kind, err)
	}
}

// readFrame reads one kernel→parent envelope (accept, grant).
func (h *sshChildHarness) readFrame(what string) lifecycle.Envelope {
	h.t.Helper()
	env, err := h.dec.ReadFrame()
	if err != nil {
		h.t.Fatalf("read %s: %v", what, err)
	}
	return env
}

// establishParent performs the parent's hello/accept handshake.
func (h *sshChildHarness) establishParent() {
	h.send(lifecycle.Event{
		Kind:  lifecycle.KindHello,
		Hello: &lifecycle.Hello{Shell: "assembly-test"},
	})
	env := h.readFrame("parent accept")
	if env.Event.Kind != lifecycle.KindAccept {
		h.t.Fatalf("parent handshake answered with %s, want accept", env.Event.Kind)
	}
}

// requestChild sends the ssh domain_request and captures the grant: the
// child's identity and the opaque composed line, plus the child's capability
// and listener port parsed out of the line.
func (h *sshChildHarness) requestChild(host string, port int, user string) {
	h.send(lifecycle.Event{
		Kind: lifecycle.KindDomainRequest,
		DomainRequest: &lifecycle.DomainRequest{
			RequestID: "r-assembly-1",
			Env:       lifecycle.EnvSSH,
			Host:      host,
			User:      user,
			Port:      port,
		},
	})
	env := h.readFrame("domain grant")
	grant := env.Event.DomainGrant
	if grant == nil || grant.Domain == "" {
		h.t.Fatalf("domain_request answered without a child domain; empty bootstrap = the refusal")
	}
	if grant.Bootstrap == "" {
		h.t.Fatalf("grant carries no bootstrap: the child was refused")
	}
	h.child = grant.Domain
	h.childEpoch = grant.Epoch
	h.bootstrap = grant.Bootstrap

	// ADR-0035's whole subject: neither bearer travels in the command, and
	// neither does the bundle. The line's only 64-hex value is the stage-1
	// DIGEST, which names public bytes; a capability there would reach the
	// far host's process arguments and every recorder of the exec request,
	// and a bundle there is the 92,204-byte command this epic removed.
	if len(h.bootstrap) > shellintegration.MaxCarrierLen+4096 {
		h.t.Fatalf("composed line is %d bytes: the bundle is back in the command", len(h.bootstrap))
	}

	ports := childForwardRe.FindStringSubmatch(h.bootstrap)
	if ports == nil {
		h.t.Fatalf("composed line carries no -R forward: %s", h.bootstrap)
	}
	if _, err := fmt.Sscanf(ports[1], "%d", &h.childRPort); err != nil {
		h.t.Fatalf("remote -R port %q: %v", ports[1], err)
	}
	if _, err := fmt.Sscanf(ports[2], "%d", &h.childLPort); err != nil {
		h.t.Fatalf("local -R port %q: %v", ports[2], err)
	}
}

// promptReadyParent puts the parent at a ready prompt — the only lane state
// a shell-originated start is admitted from (kernel decision 5).
func (h *sshChildHarness) promptReadyParent() {
	h.send(lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}})
	waitFor(h.t, "parent at a ready prompt", 10*time.Second, func() bool {
		return h.laneSnapshot().Lifecycle == lifecycle.LifecyclePromptReady
	})
}

// startParentAttempt opens a shell-originated attempt on the parent, the way
// the parent's own start hook does for a hand-typed line.
func (h *sshChildHarness) startParentAttempt(id, command string) lifecycle.AttemptID {
	att := lifecycle.AttemptID(id)
	h.send(lifecycle.Event{
		Kind:  lifecycle.KindStart,
		Start: &lifecycle.Start{AttemptID: &att, Command: command},
	})
	waitFor(h.t, "the parent's attempt opened", 10*time.Second, func() bool {
		a, ok := h.kernel.Attempt(att)
		return ok && a.State == lifecycle.AttemptOpen
	})
	return att
}

// completeParentAttempt reports the parent's own exit status with a render
// fence, as the parent shell's completion hook does.
func (h *sshChildHarness) completeParentAttempt(att lifecycle.AttemptID, code int) {
	var f lifecycle.FenceNonce
	f[0] = 0xA1
	h.send(lifecycle.Event{
		Kind:     lifecycle.KindComplete,
		Complete: &lifecycle.Complete{AttemptID: &att, ExitCode: &code, Fence: f},
	})
}

func (h *sshChildHarness) suspendParent() {
	h.send(lifecycle.Event{
		Kind:            lifecycle.KindDomainSuspended,
		DomainSuspended: &lifecycle.DomainSuspendedEvent{},
	})
}

func (h *sshChildHarness) activateParent() {
	h.send(lifecycle.Event{
		Kind:            lifecycle.KindDomainActivated,
		DomainActivated: &lifecycle.DomainActivatedEvent{},
	})
}

func (h *sshChildHarness) domainState(d lifecycle.DomainID) lifecycle.DomainState {
	d2, ok := h.kernel.Domain(d)
	if !ok {
		h.t.Fatalf("domain %s vanished from the kernel", d)
	}
	return d2.State
}

func (h *sshChildHarness) laneSnapshot() lifecycle.LaneSnapshot {
	st, err := h.kernel.State(h.lane)
	if err != nil {
		h.t.Fatalf("lane state: %v", err)
	}
	return st
}

// composedLineProc is the local bash running the grant's composed line — the
// "parent executes the bootstrap" step, with the real ssh client on PATH and
// the fixture key in an agent. It runs on a REAL pty because the parent shell
// always does, and because the composed line no longer wraps the client
// (nocx-beib): the ssh client's stdin IS this terminal, which is what lets it
// prompt a human and what makes `-t` allocate the far pty. The test types to
// the far shell through the same terminal.
type composedLineProc struct {
	t    *testing.T
	cmd  *exec.Cmd
	ptmx *os.File
	out  *outputBuffer
	done bool
}

// runComposedLine executes the grant bootstrap in a real bash on a pty, the
// way the parent shell evals it. The terminal is load-bearing: the composed
// line hands the client the parent's own stdin, so a pipe here would make
// OpenSSH refuse the remote pty ("Pseudo-terminal will not be allocated
// because stdin is not a terminal") and the far shell would never come up
// interactive. PATH is prefixed with the ssh-wrapper dir so the composed
// line's `ssh` resolves to the wrapper (which execs the real client with the
// fixture known_hosts).
func (h *sshChildHarness) runComposedLine(agentSock, sshWrapperDir string) *composedLineProc {
	h.t.Helper()
	// #nosec G204 — the line is the production-composed bootstrap this test
	// proves; running it under a real bash is the assertion, not an accident.
	cmd := exec.Command("bash", "-c", h.bootstrap)
	path := sshWrapperDir + string(os.PathListSeparator) + os.Getenv("PATH")
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+agentSock, "PATH="+path)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		h.t.Fatalf("start composed line on a pty: %v", err)
	}
	out := &outputBuffer{}
	// One reader, two consumers: the window gets a COPY and takes nothing.
	// Every byte still reaches the buffer a failing test prints.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				_, _ = out.Write(buf[:n])
				h.win.feed(buf[:n])
			}
			if rerr != nil {
				_ = h.win.Close()
				return
			}
		}
	}()
	h.win.attach(ptmx)
	return &composedLineProc{t: h.t, cmd: cmd, ptmx: ptmx, out: out}
}

// kill is the failure-path cleanup: never leave the ssh child running.
func (p *composedLineProc) kill() {
	p.t.Helper()
	_ = p.ptmx.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.wait()
}

// wait ends the composed line: the far session is already exiting, so close
// the bridge's stdin (cat's EOF) and reap the process.
func (p *composedLineProc) wait() {
	p.t.Helper()
	if p.done {
		return
	}
	p.done = true
	_ = p.ptmx.Close()
	_ = p.cmd.Wait()
}

// typeExit sends exit to the far shell through the keyboard bridge and waits
// for the composed line to return.
func (p *composedLineProc) typeExit() {
	p.t.Helper()
	if _, err := p.ptmx.Write([]byte("exit\n")); err != nil {
		p.t.Fatalf("type exit: %v", err)
	}
	p.wait()
}

// ---------------------------------------------------------------------------
// The happy path: the assembly, all links live.

// TestLiveSshd_SSHChildAssembly_ChildEstablishesOverComposedLine proves the
// chain the per-piece tests never ran together: a real ssh client reaches the
// real sshd over the composed grant line, the -R forward terminates at the
// listener transport, and the far shell's own hello establishes the child.
// The parent is Suspended for the whole interval and re-activates only
// through its authenticated activation (protocol doc §9).
func TestLiveSshd_SSHChildAssembly_ChildEstablishesOverComposedLine(t *testing.T) {
	fx := startLiveSshd(t, true)
	h := newSSHChildHarness(t, fx)
	h.establishParent()
	h.requestChild("127.0.0.1", fx.fixturePort(), fx.user)

	// The minted child is Pending: it must reach Established only through
	// the far shell's own hello on the reverse-forwarded transport, never
	// at mint time.
	if st := h.domainState(h.child); st != lifecycle.DomainPending {
		t.Fatalf("child state after grant = %d, want Pending", st)
	}
	if cd, ok := h.kernel.Domain(h.child); !ok || cd.Transport == h.parentLn.TransportID() {
		t.Fatalf("child minted on the parent transport; it must ride its own listener transport")
	}

	h.suspendParent()
	// The suspend frame is processed by the listener's pump; wait for it.
	waitFor(t, "parent Suspended", 10*time.Second, func() bool {
		return h.domainState(h.parent) == lifecycle.DomainSuspended
	})
	if ls := h.laneSnapshot(); ls.Domain != "" {
		t.Fatalf("lane has an active domain %q after the parent suspended, want none", ls.Domain)
	}

	agentSock := startInProcessAgent(t, fx)
	wrapperDir := installSSHWrapper(t, fx)
	proc := h.runComposedLine(agentSock, wrapperDir)
	t.Cleanup(proc.kill)
	// A failure here used to say only "timed out": the terminal is the only
	// place the far side reports why (a refused forward, a shell that died
	// on the launcher command, an authentication prompt nobody answered).
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("composed-line terminal:\n%s", proc.out.String())
		}
	})

	// The child establishes through the far shell's hello on the -R'd port.
	waitFor(t, "child domain Established via its own hello", 30*time.Second, func() bool {
		return h.domainState(h.child) == lifecycle.DomainEstablished
	})
	// The lane is owned by the child for the whole interval.
	waitFor(t, "lane owned by the child", 10*time.Second, func() bool {
		ls := h.laneSnapshot()
		return ls.Domain == h.child && ls.Lifecycle == lifecycle.LifecyclePromptReady
	})
	// The parent stays Suspended under the live child: no auto-activation.
	if st := h.domainState(h.parent); st != lifecycle.DomainSuspended {
		t.Fatalf("parent = %d while the child is live, want Suspended", st)
	}

	// The user finishes the nested session: exit at the far shell, through
	// the composed line's cat → ssh → far pty. The child's speaker leaves.
	proc.typeExit()
	waitFor(t, "child ended and left the stack", 30*time.Second, func() bool {
		st := h.domainState(h.child)
		return st == lifecycle.DomainClosed || st == lifecycle.DomainLost
	})
	// Still no auto-activation: the parent remains Suspended with the lane
	// empty until the authenticated activation arrives — the exact moment
	// a close alone must not restore it (§9).
	if st := h.domainState(h.parent); st != lifecycle.DomainSuspended {
		t.Fatalf("parent = %d after the child ended, want Suspended until activation", st)
	}
	if ls := h.laneSnapshot(); ls.Domain != "" {
		t.Fatalf("lane has an active domain %q after the child ended, want none", ls.Domain)
	}

	// Activation is the ONLY way back: the authenticated domain_activated
	// frame restores the parent to the lane.
	h.activateParent()
	waitFor(t, "parent re-established and owning the lane", 10*time.Second, func() bool {
		if st := h.domainState(h.parent); st != lifecycle.DomainEstablished {
			return false
		}
		ls := h.laneSnapshot()
		return ls.Domain == h.parent && ls.Lifecycle == lifecycle.LifecyclePromptReady
	})
}

// TestLiveSshd_SSHChildAssembly_ExitFreezesTheChildBlockAndCompletesTheParent
// watches the BLOCKS through the whole nested cycle, which the assembly proof
// above deliberately does not: it stops at the activation frame, and every
// symptom the owner reported (nocx-mlyu) is about what a block does after
// that frame.
//
// The interval this pins is the one no unit can reach: the parent opens the
// attempt for its own `ssh` line, suspends, a REAL far shell runs a command
// and then `exit` — destroying the very shell that would have sent the
// completion for it — and the parent comes back. Two blocks must end, and
// only one of them can end with a status the far side reported.
func TestLiveSshd_SSHChildAssembly_ExitFreezesTheChildBlockAndCompletesTheParent(t *testing.T) {
	fx := startLiveSshd(t, true)
	h := newSSHChildHarness(t, fx)
	h.establishParent()
	h.promptReadyParent()

	// The user typed `ssh …` at an integrated local prompt: the parent's own
	// block opens before anything nested exists.
	const parentLine = "ssh far-host"
	parentAtt := h.startParentAttempt("att-parent-ssh", parentLine)

	h.requestChild("127.0.0.1", fx.fixturePort(), fx.user)
	h.suspendParent()
	waitFor(t, "parent Suspended", 10*time.Second, func() bool {
		return h.domainState(h.parent) == lifecycle.DomainSuspended
	})
	// Suspension is not completion: the parent's block is still open, and it
	// must stay open for the whole nested interval.
	if a, _ := h.kernel.Attempt(parentAtt); a.State != lifecycle.AttemptOpen {
		t.Fatalf("parent attempt = %v while suspended, want still open", a.State)
	}

	agentSock := startInProcessAgent(t, fx)
	wrapperDir := installSSHWrapper(t, fx)
	proc := h.runComposedLine(agentSock, wrapperDir)
	t.Cleanup(proc.kill)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("composed-line terminal:\n%s", proc.out.String())
			t.Logf("child attempts published: %v", h.facts.commands(h.child))
		}
	})
	waitFor(t, "child domain Established via its own hello", 30*time.Second, func() bool {
		return h.domainState(h.child) == lifecycle.DomainEstablished
	})

	// The child says WHERE it is (nocx-ax79). Without this the pane shows a
	// cwd and nothing else, and a far /home/pi is indistinguishable from the
	// local one — the destination is the only authenticated answer to "which
	// machine will run the next command".
	dest, ok := h.facts.destinationOf(h.child)
	if !ok {
		t.Fatal("no published fact named the child's destination")
	}
	if dest.Host != "127.0.0.1" || dest.User != fx.user || dest.Port != fx.fixturePort() {
		t.Fatalf("destination = %+v, want %s@127.0.0.1:%d", dest, fx.user, fx.fixturePort())
	}

	// A command on the far host gets a block that closes normally — the
	// baseline the abandoned one is measured against.
	if _, err := proc.ptmx.Write([]byte("printf nocx-far\n")); err != nil {
		t.Fatalf("type far command: %v", err)
	}
	var farAtt lifecycle.AttemptID
	waitFor(t, "the far command completed on the child domain", 30*time.Second, func() bool {
		id, ok := h.facts.attemptFor(h.child, "printf nocx-far")
		if !ok {
			return false
		}
		farAtt = id
		a, ok := h.kernel.Attempt(id)
		return ok && a.State == lifecycle.AttemptCompleted
	})
	if a, _ := h.kernel.Attempt(farAtt); a.ExitCode == nil || *a.ExitCode != 0 {
		t.Fatalf("far command exit = %v, want 0", a.ExitCode)
	}

	// `exit` is the command that destroys the shell which would report its
	// own completion. Its block can never receive one — but it must still
	// END, with a stated unknown rather than a running dot that never stops.
	proc.typeExit()
	waitFor(t, "child ended and left the stack", 30*time.Second, func() bool {
		st := h.domainState(h.child)
		return st == lifecycle.DomainClosed || st == lifecycle.DomainLost
	})
	// Whether `exit` ever becomes an ATTEMPT is a race the product cannot
	// win and must not depend on: the shell emits its start frame and then
	// destroys itself, so the frame reaches the kernel or dies with the
	// transport. Measured here, it usually dies — the far terminal shows the
	// prompt's B mark, the echoed `exit`, and no C mark after it. So the
	// invariant is stated over the DOMAIN, which is authenticated either
	// way: nothing of a closed domain is left open. The renderer's two
	// answers to the two outcomes are pinned in
	// frontend/src/lifecycle/projections.test.ts — an attempt that arrived
	// goes unknown, and a submit that never got one is abandoned.
	if _, open := h.kernel.OpenAttempt(h.child); open {
		t.Fatal("the closed child domain still has an open attempt: a block that can never end")
	}
	if exitAtt, published := h.facts.attemptFor(h.child, "exit"); published {
		a, ok := h.kernel.Attempt(exitAtt)
		if !ok {
			t.Fatalf("attempt %s vanished from the kernel", exitAtt)
		}
		if a.State != lifecycle.AttemptUnknown {
			t.Fatalf("the `exit` attempt = %v after its domain closed, want unknown", a.State)
		}
		if a.ExitCode != nil {
			t.Fatalf("the `exit` attempt carries exit code %d; a status nobody reported "+
				"must never be invented", *a.ExitCode)
		}
	} else {
		t.Logf("no attempt was published for `exit` (the start frame died with the shell); "+
			"the child published: %v", h.facts.commands(h.child))
	}

	// The parent comes back and completes its own block with the status the
	// ssh client really exited with — the local D of the ssh line, which the
	// child's departure neither supplies nor invalidates.
	h.activateParent()
	waitFor(t, "parent re-established and owning the lane", 10*time.Second, func() bool {
		if st := h.domainState(h.parent); st != lifecycle.DomainEstablished {
			return false
		}
		return h.laneSnapshot().Domain == h.parent
	})
	h.completeParentAttempt(parentAtt, 0)
	waitFor(t, "the parent's ssh block froze with its real status", 10*time.Second, func() bool {
		a, ok := h.kernel.Attempt(parentAtt)
		return ok && a.State == lifecycle.AttemptCompleted && a.ExitCode != nil && *a.ExitCode == 0
	})
	// And the lane is the parent's again, structured, ready for the next
	// command — a second `ssh` starts from a clean block, not from whatever
	// the pane was left holding.
	h.send(lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}})
	waitFor(t, "lane back at the parent's ready prompt", 10*time.Second, func() bool {
		ls := h.laneSnapshot()
		return ls.Domain == h.parent && ls.Lifecycle == lifecycle.LifecyclePromptReady
	})
}

// ---------------------------------------------------------------------------
// The failure paths: forwarding refused → stillborn child, parent still
// activates, late frame rejected.

// TestLiveSshd_SSHChildAssembly_ForwardingRefusedParentStillActivates proves
// the stillborn interval (protocol doc §9): a host whose sshd refuses the -R
// bind leaves the child Pending forever; the parent still activates at its
// next prompt boundary (a Pending child is not on the stack), and a late
// hello from the stillborn child is rejected against the restored parent —
// the reject mutates nothing.
func TestLiveSshd_SSHChildAssembly_ForwardingRefusedParentStillActivates(t *testing.T) {
	fx := startLiveSshd(t, false) // AllowTcpForwarding no
	h := newSSHChildHarness(t, fx)
	h.establishParent()
	h.requestChild("127.0.0.1", fx.fixturePort(), fx.user)
	if st := h.domainState(h.child); st != lifecycle.DomainPending {
		t.Fatalf("child state after grant = %d, want Pending", st)
	}
	h.suspendParent()

	agentSock := startInProcessAgent(t, fx)
	wrapperDir := installSSHWrapper(t, fx)
	proc := h.runComposedLine(agentSock, wrapperDir)
	t.Cleanup(proc.kill)

	// The refusal is observable: sshd rejects the tcpip-forward and the
	// client reports it. The client shares the parent's terminal now
	// (nocx-beib), so the report lands there — which is also what a user
	// would see, and the refusal-leak contract for a CONVENTIONAL session
	// is asserted separately by its own proof.
	refused := waitForResult(t, "ssh reporting the refused reverse forward", 30*time.Second, func() bool {
		return strings.Contains(proc.out.String(), "remote port forwarding failed")
	})
	if !refused {
		t.Fatalf("ssh never reported the refused -R; terminal:\n%s", proc.out.String())
	}
	// The stillborn child never establishes: give the far side time to have
	// tried the in-band connect to the refused port and failed open.
	time.Sleep(2 * time.Second)
	if st := h.domainState(h.child); st != lifecycle.DomainPending {
		t.Fatalf("stillborn child = %d, want Pending (never established)", st)
	}

	// The user gives up on the nested session; the far shell exits and the
	// composed line returns.
	proc.typeExit()

	// The parent still activates at its next prompt boundary.
	h.activateParent()
	waitFor(t, "parent re-established after the stillborn child", 10*time.Second, func() bool {
		if st := h.domainState(h.parent); st != lifecycle.DomainEstablished {
			return false
		}
		ls := h.laneSnapshot()
		return ls.Domain == h.parent && ls.Lifecycle == lifecycle.LifecyclePromptReady
	})

	// A late frame from the stillborn child is rejected against the
	// restored parent: its hello cannot establish a child over an active
	// parent, the listener closes the candidate, and nothing mutates.
	h.assertLateChildHelloRejected()
}

// assertLateChildHelloRejected sends the stillborn child's authenticated
// hello over its own listener transport after the parent re-activated: the
// kernel must reject it (the child's parent is not Suspended), the listener
// must close the candidate without any accept, and both domains must keep
// their states.
func (h *sshChildHarness) assertLateChildHelloRejected() {
	h.t.Helper()
	cap, ok := h.win.capability(h.t)
	if !ok {
		h.t.Fatal("no capability was ever delivered to the far side; the late-hello proof needs one")
	}
	h.childCap = cap
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", h.childLPort))
	if err != nil {
		h.t.Fatalf("dial child listener: %v", err)
	}
	defer func() { _ = conn.Close() }()
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       h.lane,
		Domain:     h.child,
		Epoch:      h.childEpoch,
		Sequence:   1,
		Capability: h.childCap,
		Event:      lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "late"}},
	}
	if _, encErr := lifecyclecodec.Encode(conn, env); encErr != nil {
		h.t.Fatalf("encode late hello: %v", encErr)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err == nil {
		h.t.Fatalf("late child hello was answered with %d bytes, want a rejected-and-closed candidate", n)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		h.t.Fatalf("late child hello left the candidate open: the listener did not close it")
	}
	// The reject mutated nothing.
	if st := h.domainState(h.child); st != lifecycle.DomainPending {
		h.t.Fatalf("child state after the late hello = %d, want Pending", st)
	}
	if st := h.domainState(h.parent); st != lifecycle.DomainEstablished {
		h.t.Fatalf("parent state after the late hello = %d, want Established", st)
	}
}
