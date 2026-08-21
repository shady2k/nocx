package shellintegration

// The authenticated lifecycle channel (ADR-0024) driven end to end: a REAL
// shell on a REAL pty, its hooks speaking the length-prefixed JSON protocol
// over a transport that is not the tty, against a fake kernel adapter that
// validates the capability and the sequence and answers hello with accept.
//
// These tests are the paired success paths the repo demands: every failure
// path in the hooks (bad capability, no transport, handshake timeout) has a
// sibling here that proves an ordinary machine with a live kernel gets a
// working channel, and the capability never appears in any environment.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

const (
	testCap   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testLane  = "lane-test"
	testDom   = "dom-test"
	testEpoch = 1
)

// kernelEvent is one accepted event, decoded just enough to assert on.
type kernelEvent struct {
	Seq  uint64
	Evt  string
	Body map[string]any
}

// frame is the wire shape of one envelope (docs/lifecycle-protocol.md §2).
type frame struct {
	V     uint8  `json:"v"`
	Lane  string `json:"lane"`
	Dom   string `json:"dom"`
	Epoch uint64 `json:"epoch"`
	Seq   uint64 `json:"seq"`
	Cap   string `json:"cap"`
	Evt   string `json:"evt"`
}

// fakeKernel plays the kernel's transport side: it accepts every connection
// (any local process can reach the loopback port — that is the transport's
// threat model), reads 4-byte big-endian length-prefixed JSON frames,
// validates the envelope (version, lane, domain, epoch, capability, then the
// monotonic sequence), answers the first hello with accept, and records
// accepted events. A frame with a wrong capability or a non-increasing
// sequence is rejected and counted — nothing about the domain state changes,
// exactly like the real kernel.
type fakeKernel struct {
	t             *testing.T
	cap           string
	mu            sync.Mutex
	accepted      []kernelEvent
	rejected      int
	lastSeq       uint64
	acceptedHello bool
	// conn is the shell's connection, captured on accept so a test can
	// push a refresh_request at it (the kernel-originated outbound the
	// adapter's Send would frame). Tests dial only one shell connection.
	conn net.Conn
	// readFrames, when false, accepts connections but never reads them:
	// the hello never ARRIVES (decision-9 fault variant 1).
	readFrames bool
	// answerHello, when false, reads and records the hello but never
	// answers it: ACCEPT never comes (decision-9 fault variant 2).
	answerHello bool
	// answerGrantEmpty makes every domain_request come back as a grant
	// carrying grantBootstrap — the empty string by default, which is the
	// protocol's refusal echo (nocx-tyyo).
	answerGrantEmpty bool
	grantBootstrap   string
}

func newFakeKernel(t *testing.T, cap string) *fakeKernel {
	return &fakeKernel{t: t, cap: cap, readFrames: true, answerHello: true}
}

// serveLoop accepts connections until the listener is closed and serves
// each one.
func (k *fakeKernel) serveLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if !k.readFrames {
			// Fault variant 1: the connection is accepted and held open,
			// never read — the shell's hello sits in the socket buffer and
			// the accept never comes. The shell's bounded handshake wait
			// expires with its native prompt visible.
			continue
		}
		go k.serve(conn)
	}
}

// serve reads frames until EOF.
func (k *fakeKernel) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	k.mu.Lock()
	if k.conn == nil {
		k.conn = conn
	}
	k.mu.Unlock()
	r := bufio.NewReader(conn)
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > 65536 {
			k.reject()
			return
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var f frame
		if err := json.Unmarshal(body, &f); err != nil {
			k.reject()
			return
		}
		if !k.acceptFrame(f) {
			continue // rejected as if it never arrived
		}
		k.mu.Lock()
		ev := kernelEvent{Seq: f.Seq, Evt: f.Evt}
		_ = json.Unmarshal(body, &ev.Body)
		k.accepted = append(k.accepted, ev)
		first := !k.acceptedHello
		k.acceptedHello = true
		k.mu.Unlock()
		if first && f.Evt == "hello" && k.answerHello {
			k.sendAccept(conn)
		}
		// A domain_request is answered the way the real publisher answers
		// one whose grant builder refused: the protocol's empty-bootstrap
		// echo (publisher.go leaves Bootstrap zero when the builder errors).
		// It is the shape the shell must survive, so the fixture can send
		// it (nocx-tyyo).
		if f.Evt == "domain_request" && k.answerGrantEmpty {
			rid, _ := ev.Body["request"].(string)
			env, _ := ev.Body["env"].(string)
			k.sendEmptyGrant(conn, rid, env)
		}
	}
}

// sendEmptyGrant pushes the refusal echo: a domain_grant for this request
// whose bootstrap is the empty string. The field order matches the codec's,
// because the shell parses `bootstrap` as the LAST field of the frame.
func (k *fakeKernel) sendEmptyGrant(conn net.Conn, rid, env string) {
	body := fmt.Sprintf(`{"v":1,"lane":%q,"dom":%q,"epoch":%d,"seq":0,"cap":%q,"evt":"domain_grant","request":%q,"env":%q,"domain":"","bootstrap":%q}`,
		testLane, testDom, testEpoch, k.cap, rid, env, k.grantBootstrap)
	var hdr [4]byte
	// #nosec G115 — a fixed-size test fixture, far under the protocol cap.
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	_, _ = conn.Write(append(hdr[:], body...))
}

func (k *fakeKernel) reject() {
	k.mu.Lock()
	k.rejected++
	k.mu.Unlock()
}

func (k *fakeKernel) rejectedCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rejected
}

// acceptFrame validates exactly what the real kernel validates before any
// state is consulted (protocol doc §4): version, lane, domain, epoch,
// capability, then the monotonic sequence.
func (k *fakeKernel) acceptFrame(f frame) bool {
	if f.V != 1 || f.Lane != testLane || f.Dom != testDom || f.Epoch != testEpoch {
		k.reject()
		return false
	}
	if f.Cap != k.cap {
		k.reject()
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if f.Seq <= k.lastSeq {
		k.rejected++
		return false
	}
	k.lastSeq = f.Seq
	return true
}

func (k *fakeKernel) sendAccept(conn net.Conn) {
	body := fmt.Sprintf(`{"v":1,"lane":%q,"dom":%q,"epoch":%d,"seq":0,"cap":%q,"evt":"accept"}`,
		testLane, testDom, testEpoch, k.cap)
	var hdr [4]byte
	// #nosec G115 — the accept frame is a fixed-size test fixture, far
	// under the 64 KiB protocol cap.
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	_, _ = conn.Write(append(hdr[:], body...))
}

// sendRefresh pushes a refresh_request envelope at the shell's connection,
// exactly what the adapter's Send would frame when the kernel desynchronizes
// the domain (protocol §10). seq 0 like every kernel-originated envelope.
func (k *fakeKernel) sendRefresh(rid string) {
	k.mu.Lock()
	conn := k.conn
	k.mu.Unlock()
	if conn == nil {
		k.t.Fatalf("no shell connection captured; the handshake never completed?")
	}
	body := fmt.Sprintf(`{"v":1,"lane":%q,"dom":%q,"epoch":%d,"seq":0,"cap":%q,"evt":"refresh_request","request":%q}`,
		testLane, testDom, testEpoch, k.cap, rid)
	var hdr [4]byte
	// #nosec G115 — the refresh frame is a fixed-size test fixture, far
	// under the 64 KiB protocol cap.
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	_, _ = conn.Write(append(hdr[:], body...))
}

func (k *fakeKernel) events() []kernelEvent {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]kernelEvent(nil), k.accepted...)
}

func (k *fakeKernel) count(evt string) int {
	n := 0
	for _, e := range k.events() {
		if e.Evt == evt {
			n++
		}
	}
	return n
}

// channelShell is a real interactive shell on a pty, its hooks sourced the
// way the launcher would source them: a bootstrap file that sets the
// capability as a non-exported variable (the @CAP@ substitution point) and
// then sources the embedded script, with the channel config in the
// environment (NOCX_LIFECYCLE_*).
// kernelHarness is the slice of the fake kernel the channelShell helper
// drives: count, events and the refresh-push the refresh tests use. Both
// the single-domain fakeKernel and the two-domain nestedKernel satisfy it.
type kernelHarness interface {
	count(evt string) int
	events() []kernelEvent
	sendRefresh(rid string)
	rejectedCount() int
}

type channelShell struct {
	t        *testing.T
	cmd      *exec.Cmd
	ptmx     *os.File
	kernel   kernelHarness
	listener net.Listener
	mu       sync.Mutex
	out      []byte
}

// startChannelShell boots the hooks against a fake kernel over loopback TCP
// (the remote / in-band transport shape). It returns only after the
// handshake completed: the kernel has seen hello and the shell has sent its
// first prompt_ready.
func startChannelShell(t *testing.T, shell, scriptName, script string) *channelShell {
	t.Helper()
	return startChannelShellCfg(t, shell, scriptName, script, newFakeKernel(t, testCap), "", true)
}

// startChannelShellCfg boots the hooks against a fake kernel over loopback
// TCP (the remote / in-band transport shape), with the decision-9 fault
// harness knobs: the kernel may be in a fault mode (never reading the
// connection, or never answering hello), the shell's native prompt may carry
// a sentinel text the assertions key on (a suppressed marker-only prompt has
// no native text at all), and the handshake wait is optional — the fault
// tests must observe the shell AFTER its bounded wait expires, not after a
// completed handshake.
func startChannelShellCfg(t *testing.T, shell, scriptName, script string, k *fakeKernel, sentinelPrompt string, waitHandshake bool) *channelShell {
	t.Helper()
	sh := requireShell(t, shell)

	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatalf("listen: %v", lnErr)
	}
	port := tcpPort(t, ln)

	home := t.TempDir()
	tmpDir := t.TempDir()
	bootstrap := filepath.Join(t.TempDir(), "bootstrap")
	// The launcher's substitution point: __nocx_cap='@CAP@' in the rcfile
	// text. The hooks drop the export attribute again at source time.
	boot := "export -n __nocx_cap 2>/dev/null\n__nocx_cap='" + testCap + "'\nexport -n __nocx_cap 2>/dev/null\n"
	if err := os.WriteFile(bootstrap, []byte(boot), 0o600); err != nil {
		t.Fatalf("write bootstrap: %v", err)
	}
	scriptPath := writeScriptFile(t, scriptName, script)

	// The shell FAMILY, which is what the rc layout and the prompt variable
	// depend on — never the binary name, which since the version matrix is a
	// variant: "bash32" is a bash and matches no `case "bash"`. When it did,
	// the rc file was silently not written at all, so the bash32 leg started
	// with no integration and "failed" for a reason that was not the product.
	// The script being driven is the exact answer: nocx.bash is bash's.
	family := "bash"
	if strings.HasSuffix(scriptName, ".zsh") {
		family = "zsh"
	}

	// #nosec G204 — sh is the requireShell-resolved path, not input; a
	// real interactive shell on a real pty is the only way to exercise the
	// channel hooks (same annotation as the in-band pty suite).
	cmd := exec.Command(sh, "-i")
	promptEnv := ""
	if sentinelPrompt != "" {
		// The sentinel rides the shell's own prompt variable, so the
		// assertion is on the fixture's prompt text — what a person sees —
		// not on marker bytes.
		if family == "zsh" {
			promptEnv = "PROMPT=" + sentinelPrompt
		} else {
			promptEnv = "PS1=" + sentinelPrompt
		}
	}
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+tmpDir, "TERM=xterm", "HISTFILE=/dev/null", promptEnv),
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"NOCX_SESSION_ID=chansess",
		"NOCX_LIFECYCLE_LANE="+testLane,
		"NOCX_LIFECYCLE_DOMAIN="+testDom,
		fmt.Sprintf("NOCX_LIFECYCLE_EPOCH=%d", testEpoch),
		fmt.Sprintf("NOCX_LIFECYCLE_PORT=%d", port),
		"NOCX_LIFECYCLE_TIMEOUT_MS=1000",
	)
	// The gate line: source the bootstrap (cap) then the hooks — the shape
	// of the launcher rcfile's install section.
	gate := filepath.Join(t.TempDir(), "gate")
	gateBody := ". " + ShellQuote(bootstrap) + "\n. " + ShellQuote(scriptPath) + "\n"
	if err := os.WriteFile(gate, []byte(gateBody), 0o600); err != nil {
		t.Fatalf("write gate: %v", err)
	}
	// The sentinel prompt must be set INSIDE the rc file, not only in the
	// environment: the developer machine's own bashrc may export PS1, and
	// bash takes the FIRST value from the environment, so an env-only
	// sentinel silently loses and the assertion checks the wrong prompt.
	promptLine := ""
	if sentinelPrompt != "" {
		if family == "zsh" {
			promptLine = "PROMPT='" + sentinelPrompt + "'\n"
		} else {
			promptLine = "PS1='" + sentinelPrompt + "'\n"
		}
	}
	switch family {
	case "bash":
		rc := filepath.Join(home, ".bashrc")
		if err := os.WriteFile(rc, []byte(promptLine+". "+ShellQuote(gate)+"\n"), 0o600); err != nil {
			t.Fatalf("write .bashrc: %v", err)
		}
	case "zsh":
		zdot := t.TempDir()
		if err := os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte(promptLine+". "+ShellQuote(gate)+"\n"), 0o600); err != nil {
			t.Fatalf("write .zshrc: %v", err)
		}
		cmd.Env = append(cmd.Env, "ZDOTDIR="+zdot)
	}

	go k.serveLoop(ln)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k, listener: ln}
	// The pump must run before the shell renders anything: the handshake
	// happens during rc sourcing, and the first prompt follows immediately.
	go s.readPump()
	if waitHandshake {
		s.waitForHandshake()
	}
	return s
}

// ptyPumpLag delays every pty pump in this package between the read that
// takes bytes out of the kernel and the append that makes them visible to an
// assertion — the exact window a descheduled pump occupies on a loaded
// machine. Off by default; set NOCX_TEST_PTY_PUMP_LAG_MS to open it.
//
// It exists because this package's fragile assertions are fragile ONE AT A
// TIME: they read the pty after synchronising on the lifecycle channel, which
// is a different transport, and whichever one the scheduler catches is the
// one that goes red. Two of them cost three red gates before anybody could
// name the class (nocx-yjen, nocx-j41jx). With the knob open the whole class
// fails deterministically in a single run on an idle machine, so a fix can be
// verified against a failure instead of against silence:
//
//	NOCX_TEST_PTY_PUMP_LAG_MS=150 go test -race ./internal/shellintegration/
//
// A test that passes with this open does not depend on the pump being
// prompt. That is the property, and it is cheap to re-check.
var ptyPumpLag = func() time.Duration {
	ms, err := strconv.Atoi(os.Getenv("NOCX_TEST_PTY_PUMP_LAG_MS"))
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}()

func (s *channelShell) readPump() {
	buf := make([]byte, 8192)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			if ptyPumpLag > 0 {
				time.Sleep(ptyPumpLag)
			}
			s.mu.Lock()
			s.out = append(s.out, buf[:n]...)
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *channelShell) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.out)
}

// waitForHandshake waits until the kernel has accepted hello and the first
// prompt_ready — the handshake completed and the shell is at its first
// ready prompt. Event-driven, so the shell's first prompt is never raced.
func (s *channelShell) waitForHandshake() {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.kernel.count("hello") > 0 && s.kernel.count("prompt_ready") > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.t.Fatalf("handshake never completed; accepted=%v output=%q", s.kernel.events(), s.output())
}

// run types one command line and returns when the command is finished on
// BOTH transports: its completion accepted on the lifecycle channel, and its
// whole render arrived on the pty.
//
// THE SECOND HALF IS THE POINT, and it is why this helper exists rather than
// a bare write. A channelShell carries two independent transports — a TCP
// socket to the fake kernel, and the pty — and a shell writes to both. Every
// wait this type used to offer was a wait on the SOCKET: waitForAccepted,
// waitForPromptAfterComplete, waitForHandshake, and the fake kernel's own
// flags. A test that waited on one of those and then read s.output() was
// reading a buffer fed by the other transport, with no wait on it at all —
// not a short deadline, no deadline: a plain read of whatever the pump
// goroutine happened to have appended by then.
//
// That is one defect with twenty faces. It surfaced as a missing fence
// (nocx-yjen's neighbour), a missing "STILLBORN-SUDO-RAN", a missing
// AFTERMARK — whichever assertion the scheduler caught that run — and each
// looked like its own bug. Set NOCX_TEST_PTY_PUMP_LAG_MS (see ptyPumpLag)
// and the whole class fails together, on an idle machine, every time.
//
// The anchor is the RENDER FENCE — 32 random bytes the shell mints for this
// command and writes to the pty AFTER the command's output, which is the
// rendezvous the protocol defines for exactly this question (doc §8, the
// decision 1 carve-out). Its arrival means every byte this command rendered
// is in the buffer, and because the fence is unique per command it needs no
// baseline count to be read against.
//
// A count of prompt markers was tried first and is wrong for a reason worth
// keeping: `promptsBefore := Count(output, A)` taken before the write reads
// the SAME lagging buffer, so under lag it misses the current prompt's own
// marker, and that marker then arrives and satisfies "one more than before"
// while the command has not started. A baseline sampled through the race
// cannot anchor the race.
func (s *channelShell) run(cmdline string) {
	s.t.Helper()
	if _, err := s.ptmx.Write([]byte(cmdline + "\n")); err != nil {
		s.t.Fatalf("write %q: %v", cmdline, err)
	}
	s.waitForAccepted("complete")
	// And then for the prompt the shell returns to. waitForAccepted stops at
	// the FIRST event of a kind, so stopping at "complete" leaves the
	// post-command prompt_ready still in flight — which the assertions below
	// require. On a fast host it usually arrived anyway; in the test container
	// it did not, and the test failed for a race rather than for a defect.
	s.waitForPromptAfterComplete()
	// The pty half, per the note above.
	s.waitForRenderedThroughPrompt()
}

// waitForRenderedThroughPrompt blocks until the pty carries the render fence
// of the LAST accepted complete, and then a prompt marker after it — the
// command's whole render, and the prompt that follows it.
//
// The fence pins a POSITION in the buffer, which is what lets the prompt
// marker be found without counting: "an A after this fence" is unambiguous
// where "one more A than before" is not.
func (s *channelShell) waitForRenderedThroughPrompt() {
	s.t.Helper()
	var fence string
	for _, e := range s.kernel.events() {
		if e.Evt == "complete" {
			if f, ok := e.Body["fence"].(string); ok && f != "" {
				fence = f
			}
		}
	}
	if fence == "" {
		s.t.Fatalf("no accepted complete carried a render fence; accepted=%v", s.kernel.events())
	}
	marker := "\x1b]1337;NOCX_FENCE;" + fence
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := s.output()
		if i := strings.Index(out, marker); i >= 0 {
			if strings.Contains(out[i:], "\x1b]133;A") {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.t.Fatalf("the command's render never reached the pty (fence %s and the prompt after it); accepted=%v output=%q",
		fence, s.kernel.events(), s.output())
}

// waitForPromptAfterComplete blocks until a prompt_ready is accepted with a
// sequence greater than the last complete's — the exact condition the caller
// goes on to assert, rather than a proxy for it.
func (s *channelShell) waitForPromptAfterComplete() {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var lastComplete uint64
		for _, e := range s.kernel.events() {
			if e.Evt == "complete" {
				lastComplete = e.Seq
			}
		}
		if lastComplete > 0 {
			for _, e := range s.kernel.events() {
				if e.Evt == "prompt_ready" && e.Seq > lastComplete {
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.t.Fatalf("no prompt_ready followed the complete; accepted=%v output=%q", s.kernel.events(), s.output())
}

func (s *channelShell) waitForAccepted(evt string) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range s.kernel.events() {
			if e.Evt == evt {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.t.Fatalf("kernel never accepted %q; accepted=%v output=%q", evt, s.kernel.events(), s.output())
}

func (s *channelShell) close() {
	_, _ = s.ptmx.Write([]byte("exit\n"))
	time.Sleep(300 * time.Millisecond)
	_ = s.ptmx.Close()
	_ = s.cmd.Process.Kill()
	_ = s.listener.Close()
}

// TestBashChannel_HandshakeAndLifecycle drives the REAL bash hooks through
// the whole lifecycle on the authenticated channel: hello accepted, then a
// command produces start → complete (with a fence) → prompt_ready, every
// frame carrying the capability and a strictly increasing sequence, and the
// fence bytes reaching the pty after the command's output.
//
// It runs against every bash on the machine (forEachBash): the hooks are one
// script, and the oldest bash it must work on is macOS's 3.2.
func TestBashChannel_HandshakeAndLifecycle(t *testing.T) {
	forEachBash(t, testBashChannelHandshakeAndLifecycle)
}

func testBashChannelHandshakeAndLifecycle(t *testing.T, shell string) {
	s := startChannelShell(t, shell, "nocx.bash", bashScript)
	defer s.close()

	// The handshake completed: hello accepted, then prompt_ready at the
	// first prompt.
	hello := s.kernel.events()
	if len(hello) < 2 {
		t.Fatalf("expected hello + prompt_ready, got %v", hello)
	}
	if hello[0].Evt != "hello" || hello[0].Seq != 1 {
		t.Errorf("first frame must be hello seq=1, got %v", hello[0])
	}
	if body, ok := hello[0].Body["shell"]; !ok || body != "bash" {
		t.Errorf("hello must carry the shell kind, got %v", hello[0].Body)
	}

	s.run("echo CHANNEL-HELLO")

	events := s.kernel.events()
	var start, complete, ready *kernelEvent
	prevSeq := uint64(0)
	for i := range events {
		e := &events[i]
		if e.Seq <= prevSeq {
			t.Errorf("sequence not strictly increasing at %v (after %d)", *e, prevSeq)
		}
		prevSeq = e.Seq
		switch e.Evt {
		case "start":
			start = e
		case "complete":
			complete = e
		case "prompt_ready":
			ready = e
		}
	}
	if start == nil {
		t.Fatalf("no start accepted for the command; events=%v", events)
	}
	if complete == nil {
		t.Fatalf("no complete accepted; events=%v", events)
	}
	if ready == nil || ready.Seq <= complete.Seq {
		t.Errorf("prompt_ready must follow complete (ready=%v complete=%v)", ready, complete)
	}

	// The start carries the command text the user typed.
	if cmd, ok := start.Body["command"].(string); !ok || !strings.Contains(cmd, "echo CHANNEL-HELLO") {
		t.Errorf("start must carry the command line, got %v", start.Body)
	}

	// The shell mints its own attempt id per command, and the id carries the
	// domain (nocx-u7uh.19): s-<dom>-<n>. PID spaces are not shared across
	// domains — a docker exec shell and its parent routinely share a low
	// PID — so an id minted from $$ alone collides across domains and the
	// kernel rejects the second domain's first command. The domain is the
	// disambiguator; the per-shell counter keeps ids unique within it.
	if id, ok := start.Body["attempt"].(string); !ok || !regexp.MustCompile(`^s-dom-test-\d+$`).MatchString(id) {
		t.Errorf("start must carry a domain-scoped attempt id, got %v", start.Body["attempt"])
	}

	// The complete carries the exit status and the fence; the fence bytes
	// reach the pty after the command's output (the render rendezvous).
	code, ok := complete.Body["exit_code"].(float64)
	if !ok || code != 0 {
		t.Errorf("complete must carry exit_code 0, got %v", complete.Body)
	}
	fence, ok := complete.Body["fence"].(string)
	if !ok || len(fence) != 64 {
		t.Fatalf("complete must carry a 64-hex fence, got %v", complete.Body)
	}
	out := s.output()
	cmdIdx := strings.Index(out, "CHANNEL-HELLO")
	fenceIdx := strings.Index(out, "NOCX_FENCE;"+fence)
	if cmdIdx < 0 {
		t.Fatalf("command output missing from pty: %q", out)
	}
	if fenceIdx < 0 {
		t.Fatalf("fence OSC missing from pty output: %q", out)
	}
	if fenceIdx < cmdIdx {
		t.Errorf("fence reached the pty BEFORE the command output (fence at %d, output at %d)", fenceIdx, cmdIdx)
	}
	// The fence must be exactly once.
	if strings.Count(out, "NOCX_FENCE;"+fence) != 1 {
		t.Errorf("fence appeared more than once in pty output")
	}
}

// TestBashChannel_AnswersRefreshWithSnapshot drives the shell half of the
// desync recovery (ADR-0024 decision 7, protocol §10): the kernel
// desynchronizes the domain (a framing gap) and sends refresh_request; at
// the next prompt the shell answers with an authenticated snapshot carrying
// the request id, its state (at_prompt), its next sequence, and — the point
// of this epic's shell work — last_completed: the attempt the shell just
// finished, under the shell's own id, with the REAL exit status, so a
// completion the gap swallowed reconciles to its real status rather than to
// unknown. It also restores a visible prompt (decision 9: a Desynchronized
// domain is not live, and the suppressed marker-only prompt would be
// invisible raw input).
func TestBashChannel_AnswersRefreshWithSnapshot(t *testing.T) {
	forEachBash(t, testBashChannel_AnswersRefreshWithSnapshot)
}

func testBashChannel_AnswersRefreshWithSnapshot(t *testing.T, shell string) {
	s := startChannelShell(t, shell, "nocx.bash", bashScript)
	defer s.close()

	// The kernel desynchronizes the domain and demands an authenticated
	// snapshot.
	rid := "req-" + strings.Repeat("ab", 8)
	s.kernel.sendRefresh(rid)

	// An idle shell in readline runs no traps (nocx-z9s9.16): the answer
	// lands at the next prompt boundary. Type a command with a
	// distinguishable exit status to reach it — the completion is then
	// swallowed by the refresh path (the snapshot preempts the complete),
	// and the snapshot must report what the shell actually knows: the
	// attempt it just finished, under the shell's own id, with the real
	// status. `false` exits 1 and, unlike a `( ... )` subshell command,
	// fires the DEBUG trap — the shell's start hook runs.
	if _, err := s.ptmx.Write([]byte("false\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.waitForAccepted("snapshot")

	events := s.kernel.events()
	var snap *kernelEvent
	for i := range events {
		if events[i].Evt == "snapshot" {
			snap = &events[i]
		}
	}
	if snap == nil {
		t.Fatalf("no snapshot accepted; events=%v", events)
	}
	if got, _ := snap.Body["request"].(string); got != rid {
		t.Errorf("snapshot must echo the refresh request id, got %v", snap.Body)
	}
	if st, ok := snap.Body["shell_state"].(string); !ok || st != "at_prompt" {
		t.Errorf("snapshot shell_state = %v, want at_prompt", snap.Body["shell_state"])
	}
	if ns, ok := snap.Body["next_seq"].(float64); !ok || ns <= float64(snap.Seq) {
		t.Errorf("snapshot next_seq = %v, want strictly greater than its own seq %d", snap.Body["next_seq"], snap.Seq)
	}
	// The shell names its own attempts (it mints an id per command at
	// start; the kernel learns it at attach and resolves it as a per-attempt
	// alias), so the snapshot carries last_completed — the attempt the shell
	// just finished, with the REAL exit status. active_attempt is never
	// reported: the shell answers only from a prompt, where nothing is
	// running.
	lc, ok := snap.Body["last_completed"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot must carry last_completed with the shell's own view, got %v", snap.Body)
	}
	if id, _ := lc["attempt"].(string); !regexp.MustCompile(`^s-dom-test-\d+$`).MatchString(id) {
		t.Errorf("last_completed must carry the shell-minted attempt id, got %v", lc)
	}
	if code, _ := lc["exit_code"].(float64); code != 1 {
		t.Errorf("last_completed must carry the real exit status (1), got %v", lc)
	}
	if _, ok := snap.Body["active_attempt"]; ok {
		t.Errorf("snapshot must not carry active_attempt at a prompt: %v", snap.Body)
	}

	// decision 9: the marker-only suppression ended. The desynced PS1 is
	// '\w \$ ' + B marker; probe the raw value at the next prompt.
	if _, err := s.ptmx.Write([]byte(`printf 'PS1=[%s]\n' "$PS1"` + "\n")); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	// The probe's answer is written to the PTY; the complete is accepted on
	// the CHANNEL. Waiting for the second says nothing about the first, so
	// wait for the answer itself (see run's note on the two transports).
	s.waitForAccepted("complete")
	waitForOutput(t, s, `PS1=[\w`, 15*time.Second)
	if !strings.Contains(s.output(), `PS1=[\w`) {
		t.Errorf("visible prompt not restored after desync; output=%q", s.output())
	}
}

// TestBashChannel_CapabilityNeverInAnyEnvironment asserts the capability's
// non-negotiable property: it appears in NO environment — not `env`, not
// /proc/<pid>/environ of the shell, and not of a child of the shell — and it
// lives in a non-exported shell variable a child cannot read.
func TestBashChannel_CapabilityNeverInAnyEnvironment(t *testing.T) {
	forEachBash(t, testBashChannel_CapabilityNeverInAnyEnvironment)
}

func testBashChannel_CapabilityNeverInAnyEnvironment(t *testing.T, shell string) {
	s := startChannelShell(t, shell, "nocx.bash", bashScript)
	defer s.close()

	if _, err := s.ptmx.Write([]byte("echo SHELL_ENV_HAS_CAP=$(env | grep -c " + testCap + ")\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := s.ptmx.Write([]byte("echo SHELL_VAR_HAS_CAP=${__nocx_cap:+yes}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A CHILD of the shell reports its own environment — the one that would
	// have inherited the capability had it ridden the environment at all.
	// It reports before it sleeps, and it sleeps so the test can still see
	// it in the process table while it reaps it.
	//
	// The child answers for itself because nothing else can ask. This used
	// to read the kernel's copy for both pids — /proc/<pid>/environ on
	// linux, sysctl kern.procargs2 on darwin — and the darwin half returns
	// argv and no environment for any pid but the caller's own, which macOS
	// has enforced since 10.15. So this check scanned an empty map on the
	// platform the product ships first, found no capability in it, and
	// passed (nocx-58gq). A security assertion that cannot fail is the one
	// shape worse than one that is missing.
	if _, err := s.ptmx.Write([]byte("bash -c 'echo CHILD_PID=$$; echo CHILD_ENV_HAS_CAP=$(env | grep -c " + testCap + "); sleep 30' &\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		// Wait for the RESULT lines, not the echoed command text (the typed
		// line itself contains the capability literal).
		o := s.output()
		if strings.Contains(o, "SHELL_ENV_HAS_CAP=0") &&
			strings.Contains(o, "SHELL_VAR_HAS_CAP=yes") &&
			childEnvAnswer.MatchString(o) &&
			strings.Contains(o, "CHILD_PID="+strconv.Itoa(parsePidOrZero(o))) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	out := s.output()
	childPID := parseLastPID(t, out, "CHILD_PID=")
	defer func() {
		// Reap the background child.
		_, _ = s.ptmx.Write([]byte("kill " + strconv.Itoa(childPID) + " 2>/dev/null\n"))
	}()
	if !strings.Contains(out, "SHELL_ENV_HAS_CAP=0") {
		t.Errorf("capability found in the shell's `env`:\n%s", out)
	}
	if !strings.Contains(out, "SHELL_VAR_HAS_CAP=yes") {
		t.Errorf("capability not held in the non-exported shell variable:\n%s", out)
	}
	// Present-and-zero, both halves asserted: a missing line would otherwise
	// read as an absent capability, which is the vacuous pass again.
	//
	// Matched by REGEXP and not by substring, because this is a pty and the
	// pty echoes what was typed: the literal text "CHILD_ENV_HAS_CAP=" is in
	// the buffer from the moment the command line is written, as part of
	// `echo CHILD_ENV_HAS_CAP=$(env | grep -c …)`. A substring check
	// therefore matched the QUESTION and read as the answer having arrived,
	// so the wait above returned immediately and the assertion below fired
	// against a child that had not spoken yet. The answer is the only form
	// with a digit after the `=`; the echo always has `$`.
	m := childEnvAnswer.FindStringSubmatch(out)
	if m == nil {
		t.Errorf("the child never reported its environment, so nothing was proven about it:\n%s", out)
	} else if m[1] != "0" {
		t.Errorf("capability present in the environment of the shell's child (count %s):\n%s", m[1], out)
	}
}

// childEnvAnswer matches the child's ANSWER and never the pty's echo of the
// question that produced it: the answer has a digit after the `=`, the echoed
// command has `$(`.
var childEnvAnswer = regexp.MustCompile(`CHILD_ENV_HAS_CAP=(\d+)`)

// parsePidOrZero returns the last CHILD_PID=<digits> in out, or 0 when the
// background child has not printed yet.
func parsePidOrZero(out string) int {
	last := 0
	for _, line := range strings.Split(out, "\n") {
		// The line may carry a preexec marker prefix (ESC ] 133 ; C), so
		// search within it rather than prefix-matching.
		if i := strings.Index(line, "CHILD_PID="); i >= 0 {
			if pid, err := strconv.Atoi(strings.TrimSpace(line[i+len("CHILD_PID="):])); err == nil {
				last = pid
			}
		}
	}
	return last
}

// parseLastPID finds the LAST occurrence of `name<digits>` in out — the
// innermost shell's own pid in the fixtures that print it.
func parseLastPID(t *testing.T, out, name string) int {
	t.Helper()
	last := -1
	for _, line := range strings.Split(out, "\n") {
		// The line may carry a preexec marker prefix (ESC ] 133 ; C), so
		// search within it rather than prefix-matching.
		if i := strings.Index(line, name); i >= 0 {
			if pid, err := strconv.Atoi(strings.TrimSpace(line[i+len(name):])); err == nil {
				last = pid
			}
		}
	}
	if last <= 0 {
		t.Fatalf("no %sNNNN found in output: %q", name, out)
	}
	return last
}

// TestBashChannel_ChildProcessCannotReadTheCapability proves the
// non-exported-variable property directly: a child of the shell cannot
// obtain the capability by any normal means — not from its environment and
// not from the parent's /proc/<pid>/environ.
func TestBashChannel_ChildProcessCannotReadTheCapability(t *testing.T) {
	forEachBash(t, testBashChannel_ChildProcessCannotReadTheCapability)
}

func testBashChannel_ChildProcessCannotReadTheCapability(t *testing.T, shell string) {
	s := startChannelShell(t, shell, "nocx.bash", bashScript)
	defer s.close()
	if _, err := s.ptmx.Write([]byte("echo CHILD_CAP_READ=$(bash -c 'env | grep -c " + testCap + "')\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		o := s.output()
		if strings.Contains(o, "CHILD_CAP_READ=0") || strings.Contains(o, "CHILD_CAP_READ=1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(s.output(), "CHILD_CAP_READ=0") {
		t.Errorf("a child of the shell could read the capability:\n%s", s.output())
	}
}

// TestBashChannel_ChildFrameWithoutCapabilityProducesNoAcceptedEvent: a
// process that inherited nothing but the transport's reachability — here the
// test itself, connecting to the loopback port like any local process on the
// remote host — writes a well-formed frame WITHOUT the capability. It must
// produce no accepted event and leave the live domain untouched: the shell's
// next command still completes.
func TestBashChannel_ChildFrameWithoutCapabilityProducesNoAcceptedEvent(t *testing.T) {
	forEachBash(t, testBashChannel_ChildFrameWithoutCapabilityProducesNoAcceptedEvent)
}

func testBashChannel_ChildFrameWithoutCapabilityProducesNoAcceptedEvent(t *testing.T, shell string) {
	s := startChannelShell(t, shell, "nocx.bash", bashScript)
	defer s.close()

	port := tcpPort(t, s.listener)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// A well-formed frame with a wrong capability (the "attacker" never
	// learned the real one — the non-exported variable cannot be read).
	bad := fmt.Sprintf(`{"v":1,"lane":%q,"dom":%q,"epoch":%d,"seq":50,"cap":%q,"evt":"start","command":"evil"}`,
		testLane, testDom, testEpoch, strings.Repeat("f", 64))
	if len(bad) > 65536 {
		t.Fatalf("test frame unexpectedly large: %d", len(bad))
	}
	var hdr [4]byte
	// #nosec G115 — the frame is built in this test, bounded above by the
	// protocol's 64 KiB cap, so the int→uint32 conversion cannot overflow.
	binary.BigEndian.PutUint32(hdr[:], uint32(len(bad)))
	if _, err := conn.Write(append(hdr[:], bad...)); err != nil {
		t.Fatalf("write bad frame: %v", err)
	}
	_ = conn.Close()
	time.Sleep(300 * time.Millisecond)

	if s.kernel.count("start") != 0 {
		t.Fatalf("the capability-less frame was accepted as a start: %v", s.kernel.events())
	}
	if s.kernel.rejectedCount() == 0 {
		t.Errorf("the capability-less frame was not counted as rejected")
	}

	// The live domain is untouched: the shell's next command completes.
	s.run("echo STILL-LIVE")
	if s.kernel.count("start") != 1 {
		t.Errorf("the shell's own next start was not accepted; events=%v", s.kernel.events())
	}
}

// assertNoTransportFailsOpen is acceptance criterion 3 asserted for real: a
// shell with a capability but no listener (refused transport —
// AllowTcpForwarding off) lands in a conventional terminal — the fixture's
// own prompt text stays VISIBLE (a suppressed marker-only prompt carries no
// native text), no lifecycle event is accepted, and the shell exits without
// hanging. The old version ended in a t.Logf that proved nothing.
func assertNoTransportFailsOpen(t *testing.T, shell, scriptName, script, sentinel string) {
	t.Helper()
	sh := requireShell(t, shell)
	home := t.TempDir()
	scriptFile := writeScriptFile(t, scriptName, script)
	gate := filepath.Join(t.TempDir(), "gate")
	gateBody := "export -n __nocx_cap 2>/dev/null\n__nocx_cap='" + testCap + "'\nexport -n __nocx_cap 2>/dev/null\n. " + ShellQuote(scriptFile) + "\n"
	if werr := os.WriteFile(gate, []byte(gateBody), 0o600); werr != nil {
		t.Fatalf("write gate: %v", werr)
	}
	if shell == "bash" {
		if werr := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("PS1='"+sentinel+"'\n. "+ShellQuote(gate)+"\n"), 0o600); werr != nil {
			t.Fatalf("write .bashrc: %v", werr)
		}
	}
	// A port with nothing listening.
	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatalf("listen: %v", lnErr)
	}
	deadPort := tcpPort(t, ln)
	_ = ln.Close()

	// #nosec G204 — sh is the requireShell-resolved path, not input; a
	// real interactive shell on a real pty is the only way to prove the
	// fail-open on a refused transport.
	cmd := exec.Command(sh, "-i")
	promptEnv := "PS1=" + sentinel
	if shell == "zsh" {
		promptEnv = "PROMPT=" + sentinel
		zdot := t.TempDir()
		if werr := os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte("PROMPT='"+sentinel+"'\n. "+ShellQuote(gate)+"\n"), 0o600); werr != nil {
			t.Fatalf("write .zshrc: %v", werr)
		}
		cmd.Env = append(cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null", promptEnv), "ZDOTDIR="+zdot)
	} else {
		cmd.Env = cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null", promptEnv)
	}
	cmd.Env = append(cmd.Env,
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"NOCX_SESSION_ID=chansess",
		"NOCX_LIFECYCLE_LANE="+testLane,
		"NOCX_LIFECYCLE_DOMAIN="+testDom,
		fmt.Sprintf("NOCX_LIFECYCLE_EPOCH=%d", testEpoch),
		fmt.Sprintf("NOCX_LIFECYCLE_PORT=%d", deadPort),
		"NOCX_LIFECYCLE_TIMEOUT_MS=1000",
	)
	// #nosec G204 — sh is the requireShell-resolved path, not input; a
	// real interactive shell on a real pty is the only way to prove the
	// fail-open on a refused transport.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	var mu sync.Mutex
	out := make([]byte, 0, 65536)
	done := make(chan bool, 1)
	go func() {
		buf := make([]byte, 8192)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				out = append(out, buf[:n]...)
				mu.Unlock()
			}
			if rerr != nil {
				done <- true
				return
			}
		}
	}()
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return string(out)
	}
	// The prompt must become visible on its own — no accept exists to gate
	// it on, and the shell must not wait for one.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(snapshot(), sentinel) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(snapshot(), sentinel) {
		_ = cmd.Process.Kill()
		t.Fatalf("native prompt %q never appeared without a transport; output=%q", sentinel, snapshot())
	}
	if _, err := ptmx.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("shell hung on a refused transport; fail-open requires an immediate conventional terminal")
	}
	_ = cmd.Wait()
	// No lifecycle event was accepted: the shell never became active.
	if strings.Contains(snapshot(), "prompt_ready") {
		t.Fatalf("shell emitted lifecycle events with no transport; output=%q", snapshot())
	}
}

// TestBashChannel_NoTransportFailsOpen: a shell with a capability but no
// listener (refused transport — AllowTcpForwarding off) must land in a
// conventional terminal: visible native prompt, no lifecycle events, no
// hang (acceptance criterion 3).
func TestBashChannel_NoTransportFailsOpen(t *testing.T) {
	assertNoTransportFailsOpen(t, "bash", "nocx.bash", bashScript, "BASH_PROMPT_SENTINEL> ")
}

// TestZshChannel_NoTransportFailsOpen is the zsh half of criterion 3: parity
// with bash — whatever is asserted for one is asserted for the other.
func TestZshChannel_NoTransportFailsOpen(t *testing.T) {
	assertNoTransportFailsOpen(t, "zsh", "nocx.zsh", zshScript, "ZSH_PROMPT_SENTINEL> ")
}

// TestBashChannel_LocalDescriptorTransport drives the LOCAL transport
// shape: the transport is an inherited descriptor (socketpair, handed over
// the way exec.Cmd.ExtraFiles does at spawn) instead of a loopback port.
func TestBashChannel_LocalDescriptorTransport(t *testing.T) {
	forEachBash(t, testBashChannelLocalDescriptorTransport)
}

func testBashChannelLocalDescriptorTransport(t *testing.T, shell string) {
	bash := requireShell(t, shell)

	// A connected unix socketpair: one end is the shell's inherited
	// descriptor, the other the kernel's.
	kernelFile, shellFile := lifecycleSocketpair(t)

	home := t.TempDir()
	script := writeScriptFile(t, "nocx.bash", bashScript)
	gate := filepath.Join(t.TempDir(), "gate")
	gateBody := "export -n __nocx_cap 2>/dev/null\n__nocx_cap='" + testCap + "'\nexport -n __nocx_cap 2>/dev/null\n. " + ShellQuote(script) + "\n"
	if werr := os.WriteFile(gate, []byte(gateBody), 0o600); werr != nil {
		t.Fatalf("write gate: %v", werr)
	}
	if werr := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(". "+ShellQuote(gate)+"\n"), 0o600); werr != nil {
		t.Fatalf("write .bashrc: %v", werr)
	}

	// #nosec G204 — bash is the requireShell-resolved path, not input; an
	// interactive shell with an inherited descriptor is the only way to
	// exercise the local transport shape.
	cmd := exec.Command(bash, "-i")
	cmd.ExtraFiles = []*os.File{shellFile} // becomes fd 3
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null"),
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"NOCX_SESSION_ID=chansess",
		"NOCX_LIFECYCLE_LANE="+testLane,
		"NOCX_LIFECYCLE_DOMAIN="+testDom,
		fmt.Sprintf("NOCX_LIFECYCLE_EPOCH=%d", testEpoch),
		"NOCX_LIFECYCLE_FD=3",
		"NOCX_LIFECYCLE_TIMEOUT_MS=3000",
	)

	k := newFakeKernel(t, testCap)
	go k.serveFile(kernelFile)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
	go s.readPump()
	defer func() { _ = ptmx.Close(); _ = cmd.Process.Kill() }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if k.count("hello") > 0 && k.count("prompt_ready") > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if k.count("hello") == 0 {
		t.Fatalf("no hello over the inherited descriptor; output=%q", s.output())
	}
	s.run("echo FD-TRANSPORT-OK")
	if k.count("complete") == 0 {
		t.Errorf("no complete over the inherited descriptor; events=%v", k.events())
	}
}

// lifecycleSocketpair mints the connected pair every fixture in this package
// uses as the lifecycle channel: the first end is the kernel's, the second is
// the one the spawn hands the shell as fd 3 through exec.Cmd.ExtraFiles. The
// dup ExtraFiles performs clears close-on-exec for that copy alone, so the
// shell gets fd 3 and nothing else of ours.
//
// Both ends are marked close-on-exec, and that is the fixture's PREMISE rather
// than hygiene (nocx-dsie). syscall.Socketpair leaves them inheritable, so
// every shell this package started inherited BOTH ends as well, at whatever
// numbers the test process happened to have free — which varies with what ran
// before, in the same process and on the same machine. When the kernel's end
// landed on fd 4, the number the bootstrap-progress tests announce as
// NOCX_BOOTSTRAP_FD and deliberately do NOT hand over, the rcfile's
// "startup-entered" was written into the kernel's end of the very socket it
// speaks the protocol on, came back out of the shell's own fd 3, and the
// handshake read "star" as a frame header and refused the session.
//
// The product's own socketpair does exactly this dance for exactly this reason
// (internal/lifecyclechannel/socketpair_other.go, nocx-1w69), as does
// internal/pty's descriptor fixture: create, then mark, with ForkLock held so
// no concurrent fork/exec slips through the window between the two.
func lifecycleSocketpair(t *testing.T) (kernelEnd, shellEnd *os.File) {
	t.Helper()
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	kernelEnd = os.NewFile(uintptr(fds[0]), "kernel-end")
	shellEnd = os.NewFile(uintptr(fds[1]), "shell-end")
	t.Cleanup(func() {
		_ = kernelEnd.Close()
		_ = shellEnd.Close()
	})
	return kernelEnd, shellEnd
}

// serveFile serves one connection wrapped around a socketpair end.
func (k *fakeKernel) serveFile(f *os.File) {
	c, err := net.FileConn(f)
	if err != nil {
		return
	}
	k.serve(c)
}

// TestZshChannel_HandshakeAndLifecycle is the zsh half of the channel test:
// hello accepted, then start → complete (with fence) → prompt_ready, with
// the capability and a monotonic sequence on every frame.
func TestZshChannel_HandshakeAndLifecycle(t *testing.T) {
	s := startChannelShell(t, "zsh", "nocx.zsh", zshScript)
	defer s.close()

	s.run("echo ZSH-CHANNEL-OK")

	events := s.kernel.events()
	var start, complete *kernelEvent
	prevSeq := uint64(0)
	for i := range events {
		e := &events[i]
		if e.Seq <= prevSeq {
			t.Errorf("sequence not strictly increasing at %v", *e)
		}
		prevSeq = e.Seq
		switch e.Evt {
		case "start":
			start = e
		case "complete":
			complete = e
		}
	}
	if start == nil {
		t.Fatalf("no start accepted; events=%v", events)
	}
	if complete == nil {
		t.Fatalf("no complete accepted; events=%v", events)
	}
	fence, ok := complete.Body["fence"].(string)
	if !ok || len(fence) != 64 {
		t.Fatalf("complete must carry a 64-hex fence, got %v", complete.Body)
	}
	if !strings.Contains(s.output(), "NOCX_FENCE;"+fence) {
		t.Errorf("fence OSC missing from zsh pty output: %q", s.output())
	}
	// The zsh start carries the full command line.
	if cmd, ok := start.Body["command"].(string); !ok || !strings.Contains(cmd, "echo ZSH-CHANNEL-OK") {
		t.Errorf("zsh start must carry the command line, got %v", start.Body)
	}
}

// TestZshChannel_AnswersRefreshWithSnapshot is the zsh half of the desync
// recovery (ADR-0024 decision 7, protocol §10): the kernel desynchronizes
// the domain and sends refresh_request; at the next prompt the shell answers
// with an authenticated snapshot carrying the request id, its state
// (at_prompt), its next sequence, and last_completed — the attempt it just
// finished, under the shell's own id, with the real exit status. The zsh
// tier now answers refresh the way the bash tier does (nocx-u7uh.19).
func TestZshChannel_AnswersRefreshWithSnapshot(t *testing.T) {
	s := startChannelShell(t, "zsh", "nocx.zsh", zshScript)
	defer s.close()

	rid := "req-" + strings.Repeat("cd", 8)
	s.kernel.sendRefresh(rid)

	// An idle zsh runs no precmd until the next prompt: type a command with
	// a distinguishable exit status to reach it. The completion is then
	// swallowed by the refresh path, and the snapshot reports what the
	// shell actually knows: the attempt it just finished, under its own id,
	// with the real status.
	if _, err := s.ptmx.Write([]byte("false\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.waitForAccepted("snapshot")

	events := s.kernel.events()
	var snap *kernelEvent
	for i := range events {
		if events[i].Evt == "snapshot" {
			snap = &events[i]
		}
	}
	if snap == nil {
		t.Fatalf("no snapshot accepted; events=%v", events)
	}
	if got, _ := snap.Body["request"].(string); got != rid {
		t.Errorf("snapshot must echo the refresh request id, got %v", snap.Body)
	}
	if st, ok := snap.Body["shell_state"].(string); !ok || st != "at_prompt" {
		t.Errorf("snapshot shell_state = %v, want at_prompt", snap.Body["shell_state"])
	}
	if ns, ok := snap.Body["next_seq"].(float64); !ok || ns <= float64(snap.Seq) {
		t.Errorf("snapshot next_seq = %v, want strictly greater than its own seq %d", snap.Body["next_seq"], snap.Seq)
	}
	lc, ok := snap.Body["last_completed"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot must carry last_completed with the shell's own view, got %v", snap.Body)
	}
	if id, _ := lc["attempt"].(string); !regexp.MustCompile(`^s-dom-test-\d+$`).MatchString(id) {
		t.Errorf("last_completed must carry the shell-minted attempt id, got %v", lc)
	}
	if code, _ := lc["exit_code"].(float64); code != 1 {
		t.Errorf("last_completed must carry the real exit status (1), got %v", lc)
	}
	if _, ok := snap.Body["active_attempt"]; ok {
		t.Errorf("snapshot must not carry active_attempt at a prompt: %v", snap.Body)
	}
}

// TestInBand_AuthenticatedChannelFromStreamedCapability drives the FULL
// in-band integration with an authenticated channel: the plan is built with
// the channel configuration (lane, domain, epoch, port), the wrapper is
// typed at the prompt, READY arrives, the backend writes the capability line
// then the payload+terminator into the raw-mode stream — the wrapper
// captures the capability into a non-exported variable BEFORE anything is
// staged, so the staged file stays capability-free — and the sourced hooks
// establish the channel with that streamed capability: hello accepted, then
// start → complete → prompt_ready.
func TestInBand_AuthenticatedChannelFromStreamedCapability(t *testing.T) {
	bash := requireShell(t, "bash")

	ln, lnErr := net.Listen("tcp", "127.0.0.1:0")
	if lnErr != nil {
		t.Fatalf("listen: %v", lnErr)
	}
	port := tcpPort(t, ln)
	k := newFakeKernel(t, testCap)
	go k.serveLoop(ln)

	plan, err := New(nil).InBandBootstrap("0123456789abcdef0123456789abcdef", &ChannelConfig{
		Lane: testLane, Domain: testDom, Epoch: testEpoch, Port: port,
	})
	if err != nil {
		t.Fatalf("InBandBootstrap: %v", err)
	}
	if strings.Contains(plan.Payload, testCap) {
		t.Fatal("the in-band payload must stay capability-free (it crosses the renderer)")
	}

	home := t.TempDir()
	// #nosec G204 — bash is the requireShell-resolved path, not input; a
	// real interactive shell on a real pty is the only way to exercise the
	// in-band wrapper (same annotation as the in-band pty suite).
	cmd := exec.Command(bash, "-i")
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null"),
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"NOCX_SESSION_ID=chansess",
	)
	if werr := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("PS1='IBPROMPT> '\n"), 0o600); werr != nil {
		t.Fatalf("write .bashrc: %v", werr)
	}
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k, listener: ln}
	go s.readPump()
	defer func() { _ = ptmx.Close(); _ = cmd.Process.Kill(); _ = ln.Close() }()

	// Wait for the native prompt, then type the wrapper (the way the
	// frontend would).
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), "IBPROMPT>") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := ptmx.Write([]byte(plan.Wrapper + "\r")); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	// READY means raw mode is on; the backend then writes the capability
	// line, the payload and the terminator.
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), "NOCX_IB_READY") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	stream := testCap + "\n" + plan.Payload + plan.Terminator + "\n"
	if _, err := ptmx.Write([]byte(stream)); err != nil {
		t.Fatalf("write cap+payload: %v", err)
	}

	// The sourced hooks must connect and handshake with the streamed
	// capability.
	s.waitForHandshake()
	if k.count("hello") != 1 {
		t.Fatalf("expected exactly one hello; events=%v", k.events())
	}
	s.run("echo INBAND-CHANNEL-OK")
	if k.count("complete") == 0 {
		t.Errorf("no complete over the in-band channel; events=%v", k.events())
	}
	if !strings.Contains(s.output(), "SHELL_CAP_NONEXPORTED_CHECK") {
		// The capability must be usable but non-exported: verify via the
		// variable and its absence from env.
		//
		// The marker is ASSEMBLED BY printf so it does not occur verbatim in
		// the command text. A pty echoes what is written to it, so `echo
		// IB_CAP_SET=...` put the string `IB_CAP_SET=` into the output buffer
		// the instant the line was typed — and the wait below, which breaks on
		// exactly that substring, was therefore satisfied by the ECHO rather
		// than by the shell's answer. The assertion then read a buffer whose
		// result had not arrived and reported a capability the shell was in
		// fact holding. It passed whenever the result happened to land in the
		// same read and failed under load, which is why a full Linux run was
		// red where the package alone was green (nocx-8b47).
		//
		// With the field names as printf arguments, `IB_CAP_SET=` exists only
		// in the output, so waiting for it means waiting for the answer.
		if _, err := ptmx.Write([]byte("printf 'IB_CAP_%s=%s IB_CAP_%s=%s\\n' SET \"${__nocx_cap:+yes}\" ENV \"$(env | grep -c " + testCap + ")\"\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), "IB_CAP_SET=") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	out := s.output()
	if !strings.Contains(out, "IB_CAP_SET=yes") {
		t.Errorf("streamed capability not held in the shell variable:\n%s", out)
	}
	if !strings.Contains(out, "IB_CAP_ENV=0") {
		t.Errorf("streamed capability leaked into the environment:\n%s", out)
	}
}

// tcpPort extracts the bound port of a test listener with the comma-ok
// assertion form errcheck demands.
func tcpPort(t *testing.T, ln net.Listener) int {
	t.Helper()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", ln.Addr())
	}
	return addr.Port
}

// --- decision 9 fault injection: every handshake boundary, prompt stays visible ---

// assertConventionalAfterHandshakeFault is the decision-9 fault assertion:
// after the shell's bounded handshake wait expires, the fixture's own
// prompt text (the sentinel) must be VISIBLE — a suppressed marker-only
// prompt has no native text at all — no lifecycle event beyond the
// swallowed hello is accepted, and the shell still exits cleanly. The
// assertion is on what a person sees, not on the absence of a hang.
func assertConventionalAfterHandshakeFault(t *testing.T, s *channelShell, sentinel string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), sentinel) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(s.output(), sentinel) {
		t.Fatalf("native prompt %q never became visible after the handshake fault; output=%q", sentinel, s.output())
	}
	for _, evt := range []string{"prompt_ready", "start", "complete"} {
		if n := s.kernel.count(evt); n != 0 {
			t.Fatalf("shell sent %d %q events with no accept in the picture: %v", n, evt, s.kernel.events())
		}
	}
	s.close()
}

// TestBashChannel_HelloNeverArrivesKeepsVisiblePrompt is fault variant 1:
// the transport accepts the connection but the hello never ARRIVES (the
// kernel never reads). No accept can come; the bounded handshake wait
// expires and the native prompt stays visible (acceptance criterion 1).
func TestBashChannel_HelloNeverArrivesKeepsVisiblePrompt(t *testing.T) {
	forEachBash(t, testBashChannel_HelloNeverArrivesKeepsVisiblePrompt)
}

func testBashChannel_HelloNeverArrivesKeepsVisiblePrompt(t *testing.T, shell string) {
	k := newFakeKernel(t, testCap)
	k.readFrames = false
	s := startChannelShellCfg(t, shell, "nocx.bash", bashScript, k, "BASH_PROMPT_SENTINEL> ", false)
	assertConventionalAfterHandshakeFault(t, s, "BASH_PROMPT_SENTINEL> ")
}

// TestBashChannel_AcceptNeverComesKeepsVisiblePrompt is fault variant 2:
// the hello arrives but ACCEPT never comes (the kernel reads and records,
// and answers nothing). The shell's bounded wait expires and the native
// prompt stays visible (acceptance criterion 1).
func TestBashChannel_AcceptNeverComesKeepsVisiblePrompt(t *testing.T) {
	forEachBash(t, testBashChannel_AcceptNeverComesKeepsVisiblePrompt)
}

func testBashChannel_AcceptNeverComesKeepsVisiblePrompt(t *testing.T, shell string) {
	k := newFakeKernel(t, testCap)
	k.answerHello = false
	s := startChannelShellCfg(t, shell, "nocx.bash", bashScript, k, "BASH_PROMPT_SENTINEL> ", false)
	assertConventionalAfterHandshakeFault(t, s, "BASH_PROMPT_SENTINEL> ")
}

// TestZshChannel_HelloNeverArrivesKeepsVisiblePrompt is the zsh half of
// fault variant 1: parity with bash — whatever is asserted for one is
// asserted for the other.
func TestZshChannel_HelloNeverArrivesKeepsVisiblePrompt(t *testing.T) {
	k := newFakeKernel(t, testCap)
	k.readFrames = false
	s := startChannelShellCfg(t, "zsh", "nocx.zsh", zshScript, k, "ZSH_PROMPT_SENTINEL> ", false)
	assertConventionalAfterHandshakeFault(t, s, "ZSH_PROMPT_SENTINEL> ")
}

// TestZshChannel_AcceptNeverComesKeepsVisiblePrompt is the zsh half of
// fault variant 2: parity with bash.
func TestZshChannel_AcceptNeverComesKeepsVisiblePrompt(t *testing.T) {
	k := newFakeKernel(t, testCap)
	k.answerHello = false
	s := startChannelShellCfg(t, "zsh", "nocx.zsh", zshScript, k, "ZSH_PROMPT_SENTINEL> ", false)
	assertConventionalAfterHandshakeFault(t, s, "ZSH_PROMPT_SENTINEL> ")
}

// A refused nested SSH runs the user's command, exactly once (nocx-tyyo).
//
// The grant builder refuses `ssh` under a REMOTE parent — the -R forward
// would terminate on the far host and the multiplex socket would be created
// where this backend cannot reach it (internal/app/childdomain.go). The
// publisher answers a refusing builder with the protocol's empty-bootstrap
// echo, and the documented contract for that echo is the honest fallback:
// the parent runs the line conventionally.
//
// It did not. The ssh branch evaluated the bootstrap unconditionally, `eval
// ""` succeeded, __nocx_nested_launch returned 0, and the DEBUG wrapper
// returned 1 — which is how extdebug is TOLD to skip the original command.
// So a user typing `ssh host` at an enhanced remote prompt got no ssh, no
// error and their prompt straight back, while the backend logged a refusal
// nobody saw. The sudo/su branch guarded the same shape and was correct.
//
// This drives the real thing: a real bash on a real pty, the real DEBUG
// wrapper, a real domain_request over the real channel, and a stub `ssh` on
// PATH that says whether it ran. Skipped and duplicated are both failures,
// so the assertion is a count, not a presence.
func TestBashNested_ARefusedSSHGrantStillRunsTheUsersCommandExactlyOnce(t *testing.T) {
	forEachBash(t, testBashNestedRefusedSSHGrantRunsTheCommand)
}

func testBashNestedRefusedSSHGrantRunsTheCommand(t *testing.T, shell string) {
	k := newFakeKernel(t, testCap)
	k.answerGrantEmpty = true
	s := startChannelShellCfg(t, shell, "nocx.bash", bashScript, k, "", true)
	defer s.close()

	// A stub `ssh` that reports it ran. It is on PATH rather than a shell
	// function so the line the user types is dispatched exactly as it would
	// be on their machine.
	binDir := t.TempDir()
	stub := filepath.Join(binDir, "ssh")
	// #nosec G306 — a stub that a shell must FIND and RUN on PATH has to
	// carry the exec bit; 0o700 is already owner-only, and it lives in a
	// t.TempDir that goes away with the test.
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'STUB-SSH-RAN\\n'\n"), 0o700); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	s.run("export PATH=" + binDir + ":$PATH")

	// The ssh line is written directly and waited on by an OBSERVABLE state
	// change rather than by s.run: s.run stops at the FIRST accepted
	// "complete", which this shell already has from the PATH export, so it
	// would return before the line it just wrote ran at all. The prompt the
	// shell returns to is the state change that means this line is finished
	// — and it arrives whether the line ran or was swallowed, so the wait
	// cannot itself decide the assertion.
	promptsBefore := k.count("prompt_ready")
	if _, err := s.ptmx.Write([]byte("ssh refused.example.com\n")); err != nil {
		t.Fatalf("write the ssh line: %v", err)
	}
	waitForCount(t, func() int { return k.count("prompt_ready") },
		promptsBefore+1, "the prompt after the refused nested ssh", s, 15*time.Second)

	out := s.output()
	if n := strings.Count(out, "STUB-SSH-RAN"); n != 1 {
		t.Errorf("the user's ssh ran %d times, want exactly 1.\n"+
			"0 means the refusal swallowed the command (the bug); 2 means the fallback ran it and extdebug ran it again.\npty:\n%s", n, out)
	}
	if n := k.count("domain_suspended"); n != 0 {
		t.Errorf("the parent suspended itself %d time(s) for a child that was refused; "+
			"a refused grant claims no domain and suspends nothing", n)
	}
}

// And the paired success: a grant that DOES carry a bootstrap is evaluated,
// and the original line is not also run — the refusal fix must not turn the
// accepted path into a double execution.
func TestBashNested_AnAcceptedSSHGrantRunsTheBootstrapInsteadOfTheLine(t *testing.T) {
	forEachBash(t, testBashNestedAcceptedSSHGrantRunsBootstrap)
}

func testBashNestedAcceptedSSHGrantRunsBootstrap(t *testing.T, shell string) {
	k := newFakeKernel(t, testCap)
	k.answerGrantEmpty = true
	k.grantBootstrap = "printf 'BOOTSTRAP-RAN\\n'"
	s := startChannelShellCfg(t, shell, "nocx.bash", bashScript, k, "", true)
	defer s.close()

	binDir := t.TempDir()
	stub := filepath.Join(binDir, "ssh")
	// #nosec G306 — a stub that a shell must FIND and RUN on PATH has to
	// carry the exec bit; 0o700 is already owner-only, and it lives in a
	// t.TempDir that goes away with the test.
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'STUB-SSH-RAN\\n'\n"), 0o700); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	s.run("export PATH=" + binDir + ":$PATH")

	promptsBefore := k.count("prompt_ready")
	if _, err := s.ptmx.Write([]byte("ssh granted.example.com\n")); err != nil {
		t.Fatalf("write the ssh line: %v", err)
	}
	waitForCount(t, func() int { return k.count("prompt_ready") },
		promptsBefore+1, "the prompt after the granted nested ssh", s, 15*time.Second)

	out := s.output()
	if n := strings.Count(out, "BOOTSTRAP-RAN"); n != 1 {
		t.Errorf("the granted bootstrap ran %d times, want 1.\npty:\n%s", n, out)
	}
	if strings.Contains(out, "STUB-SSH-RAN") {
		t.Errorf("the original line ran as well as the grant: the child would be launched twice.\npty:\n%s", out)
	}
}

// The same invariant on the zsh tier (nocx-tyyo).
//
// zsh reaches the nested launch through an accept-line widget rather than a
// DEBUG trap, and its refusal echo had the identical shape: the ssh branch
// evaluated the bootstrap unconditionally, and an empty one made the widget
// report "the launch consumed the line" for a launch that never happened.
// Same defect, different mechanism — which is why it is asserted separately
// rather than assumed from the bash result.
func TestZshNested_ARefusedSSHGrantStillRunsTheUsersCommandExactlyOnce(t *testing.T) {
	k := newFakeKernel(t, testCap)
	k.answerGrantEmpty = true
	s := startChannelShellCfg(t, "zsh", "nocx.zsh", zshScript, k, "", true)
	defer s.close()

	binDir := t.TempDir()
	stub := filepath.Join(binDir, "ssh")
	// #nosec G306 — a stub that a shell must FIND and RUN on PATH has to
	// carry the exec bit; 0o700 is already owner-only, and it lives in a
	// t.TempDir that goes away with the test.
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'STUB-SSH-RAN\\n'\n"), 0o700); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	s.run("export PATH=" + binDir + ":$PATH")

	promptsBefore := k.count("prompt_ready")
	if _, err := s.ptmx.Write([]byte("ssh refused.example.com\n")); err != nil {
		t.Fatalf("write the ssh line: %v", err)
	}
	waitForCount(t, func() int { return k.count("prompt_ready") },
		promptsBefore+1, "the prompt after the refused nested ssh", s, 15*time.Second)

	out := s.output()
	if n := strings.Count(out, "STUB-SSH-RAN"); n != 1 {
		t.Errorf("the user's ssh ran %d times, want exactly 1.\npty:\n%s", n, out)
	}
	if n := k.count("domain_suspended"); n != 0 {
		t.Errorf("the parent suspended itself %d time(s) for a child that was refused", n)
	}
}
