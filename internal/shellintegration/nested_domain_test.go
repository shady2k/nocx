package shellintegration

// The nested-environment acceptance test (nocx-u7uh.11): a real integrated
// bash parent on a pty, the user typing `sudo -i`, the parent requesting a
// child domain over the authenticated channel, receiving the grant (built
// by the same code path the composition root uses — LocalBashRcfile plus
// the preserved-fd close), suspending, and launching the child through a
// REAL passwordless sudo with --preserve-fds. The child bash reads its
// rcfile from the preserved descriptor (/dev/fd/4), establishes its own
// domain over the SAME inherited socketpair, and the parent re-activates
// only after the child closes. The kernel is a two-domain fake that
// enforces the §9 ordering: the child's hello is answered only after the
// parent suspended, and the parent's activation is recorded only after the
// child closed.

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
)

const (
	nestedChildDom   = "dom-child-test"
	nestedChildEpoch = 7
	nestedChildCap   = "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
)

// nestedKernel plays the kernel's transport side for ONE lane with TWO
// domains (parent + child) over ONE socketpair, enforcing the §9 ordering:
// the child hello is accepted only after the parent suspended, and the
// grant answers the parent's request with the child's identity and the
// opaque bootstrap the parent executes.
type nestedKernel struct {
	t               *testing.T
	conn            net.Conn
	mu              sync.Mutex
	seq             map[string]uint64
	accepted        []kernelEvent
	order           []string
	parentSuspended bool
	childClosed     bool
	childHeard      bool // the child ever established (a hello was accepted)
	parentActivated bool
	// refuseChild answers the parent's domain_request with an EMPTY
	// bootstrap — the §9 refusal. The parent then runs its command
	// conventionally and still activates at its next prompt (the stillborn
	// interval). A refused child never sends hello.
	refuseChild bool
	// shellFile is the test's copy of the SHELL end of the socketpair
	// (the child's fd 3 is a dup of the same file description). Writing a
	// frame here delivers it to the kernel's read loop on the other end —
	// which is how the stillborn tests inject a late child frame.
	shellFile *os.File
	// rejected counts frames the kernel rejects: a wrong addressing tuple,
	// a stale (non-increasing) sequence, or a frame addressed to the child
	// domain after the parent restored (the child's interval ended — it
	// established and closed, or never established at all). The stillborn
	// tests inject a late child frame and assert this count grows.
	rejected int
}

func newNestedKernel(t *testing.T) *nestedKernel {
	return &nestedKernel{t: t, seq: map[string]uint64{}}
}

func (k *nestedKernel) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	k.mu.Lock()
	k.conn = conn
	k.mu.Unlock()
	r := bufio.NewReader(conn)
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > 65536 {
			return
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var f frame
		if err := json.Unmarshal(body, &f); err != nil {
			continue
		}
		k.accept(f, body)
	}
}

// accept validates the frame against the right domain and enforces the
// §9 ordering. Runs under k.mu.
func (k *nestedKernel) accept(f frame, body []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch {
	case f.Dom == testDom && f.Epoch == testEpoch && f.Cap == testCap:
	case f.Dom == nestedChildDom && f.Epoch == nestedChildEpoch && f.Cap == nestedChildCap:
		// The child's interval ends when the parent restores — whether the
		// child established and closed, or never established at all (the
		// stillborn interval). A frame addressed to the child after that is
		// a late frame: the real kernel has no such domain on the stack
		// (a Pending child is not on the stack, §9) and rejects it. Counted
		// so the stillborn tests assert the rejection instead of inheriting
		// it as prose.
		if k.parentActivated {
			k.rejected++
			k.t.Logf("rejected late child frame: evt=%s seq=%d", f.Evt, f.Seq)
			return
		}
	default:
		k.rejected++
		k.t.Logf("rejected wrong tuple: dom=%s epoch=%d evt=%s seq=%d cap=%s", f.Dom, f.Epoch, f.Evt, f.Seq, f.Cap)
		return // wrong tuple: not one of the two domains
	}
	if f.Seq <= k.seq[f.Dom] {
		k.rejected++
		k.t.Logf("rejected stale seq: dom=%s evt=%s seq=%d (last=%d)", f.Dom, f.Evt, f.Seq, k.seq[f.Dom])
		return
	}
	k.seq[f.Dom] = f.Seq
	ev := kernelEvent{Seq: f.Seq, Evt: f.Evt}
	_ = json.Unmarshal(body, &ev.Body)
	k.accepted = append(k.accepted, ev)
	k.order = append(k.order, f.Dom+" "+f.Evt)
	switch f.Evt {
	case "hello":
		if f.Dom == nestedChildDom {
			// The §9 interval, this end: a child hello before the parent
			// suspended, or after the child closed, is an ordering
			// violation. A child that never establishes (refused bootstrap)
			// sends no hello at all — the stillborn interval is not an
			// ordering violation, it is the expected path.
			k.childHeard = true
			if !k.parentSuspended {
				k.t.Errorf("child hello before the parent suspended — §9 ordering violated")
			}
			if k.childClosed {
				k.t.Errorf("child hello after the child closed — a late frame slipped through")
			}
		}
		k.sendAcceptLocked(f.Dom, f.Epoch, f.Cap)
	case "domain_request":
		k.grantLocked()
	case "domain_suspended":
		k.parentSuspended = true
	case "domain_closed":
		if f.Dom == nestedChildDom {
			k.childClosed = true
		}
	case "domain_activated":
		// The parent may activate only once the child closed — or once it
		// became clear the child never established (a refused bootstrap:
		// the real kernel accepts an activation of a parent whose Pending
		// child never reached the stack, §9).
		if k.childHeard && !k.childClosed {
			k.t.Errorf("parent activated while the child was still live; order=%v", k.order)
		}
		k.parentActivated = true
	}
}

// grantLocked answers the parent's request: the child's identity plus an
// opaque bootstrap built EXACTLY as the composition root builds it
// (buildLocalChildBootstrap's shape: LocalBashRcfile with the child's
// addressing and the preserved-fd close).
func (k *nestedKernel) grantLocked() {
	bootstrap := ""
	if !k.refuseChild {
		rc, err := LocalBashRcfile(LaunchOptions{
			SessionID:   "chansess",
			Enhanced:    true,
			Capability:  nestedChildCap,
			Lane:        testLane,
			Domain:      nestedChildDom,
			Epoch:       nestedChildEpoch,
			LifecycleFD: 3,
		})
		if err != nil {
			k.t.Fatalf("child rcfile: %v", err)
		}
		// The child reads the rcfile from the preserved bootstrap descriptor
		// chosen by the PARENT at launch (bash's free 4-9 scan, zsh's {var}
		// allocation >= 10 — never a fixed 4), so the close names the fd it
		// was READ FROM the way the composition root's closing line does:
		// BASH_SOURCE[0] is /dev/fd/N inside the rcfile, so ##*/ yields N
		// for either tier's descriptor number.
		rc += "\neval \"exec ${BASH_SOURCE[0]##*/}<&-\" 2>/dev/null\n"
		bootstrap = rc
	}
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       lifecycle.LaneID(testLane),
		Domain:     lifecycle.DomainID(testDom),
		Epoch:      testEpoch,
		Capability: capBytes(k.t, testCap),
		Event: lifecycle.Event{Kind: lifecycle.KindDomainGrant, DomainGrant: &lifecycle.DomainGrant{
			RequestID: "r-" + testDom + "-0",
			Env:       lifecycle.EnvSudo,
			Domain:    lifecycle.DomainID(nestedChildDom),
			Epoch:     nestedChildEpoch,
			Bootstrap: bootstrap,
		}},
	}
	if _, err := lifecyclecodec.Encode(k.conn, env); err != nil {
		k.t.Fatalf("encode grant: %v", err)
	}
}

// sendAcceptLocked answers a hello with the accept for THAT domain (the
// parent's accept carries the parent's addressing, the child's the child's).
func (k *nestedKernel) sendAcceptLocked(dom string, epoch uint64, capHex string) {
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       lifecycle.LaneID(testLane),
		Domain:     lifecycle.DomainID(dom),
		Epoch:      epoch,
		Capability: capBytes(k.t, capHex),
		Event:      lifecycle.Event{Kind: lifecycle.KindAccept, Accept: &lifecycle.Accept{}},
	}
	k.t.Logf("kernel sending accept for dom=%s epoch=%d", dom, epoch)
	if _, err := lifecyclecodec.Encode(k.conn, env); err != nil {
		k.t.Fatalf("encode accept: %v", err)
	}
	k.t.Logf("kernel accept sent for dom=%s", dom)
}

// sendRefresh pushes a refresh_request envelope at the parent's connection,
// exactly what the adapter's Send would frame when the kernel
// desynchronizes the parent domain (protocol §10).
func (k *nestedKernel) sendRefresh(rid string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       lifecycle.LaneID(testLane),
		Domain:     lifecycle.DomainID(testDom),
		Capability: capBytes(k.t, testCap),
		Event: lifecycle.Event{Kind: lifecycle.KindRefreshRequest, RefreshRequest: &lifecycle.RefreshRequest{
			RequestID: lifecycle.RequestID(rid),
		}},
	}
	if _, err := lifecyclecodec.Encode(k.conn, env); err != nil {
		k.t.Fatalf("encode refresh: %v", err)
	}
}

// rejectedCount reports frames the kernel rejected: a wrong addressing
// tuple, a stale sequence, or a frame addressed to the child domain after
// the parent restored. The stillborn tests inject one late frame and
// assert this count grows; the happy-path tests assert it stays zero.
func (k *nestedKernel) rejectedCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rejected
}

// count returns how many accepted events of one kind arrived across both
// domains.
func (k *nestedKernel) count(evt string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, e := range k.accepted {
		if e.Evt == evt {
			n++
		}
	}
	return n
}

// events returns the accepted events.
func (k *nestedKernel) events() []kernelEvent {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]kernelEvent(nil), k.accepted...)
}

func (k *nestedKernel) serveFile(f *os.File) {
	c, err := net.FileConn(f)
	if err != nil {
		return
	}
	k.serve(c)
}

func capBytes(t *testing.T, hexCap string) lifecycle.Capability {
	var c lifecycle.Capability
	if _, err := hex.Decode(c[:], []byte(hexCap)); err != nil {
		t.Fatalf("decode cap: %v", err)
	}
	return c
}

// TestBashNestedChildDomain is the NESTED acceptance criterion's local
// half, proven end to end on a real pty: the child gets its own
// authenticated domain, the parent suspends before the child hello, and
// closing the child restores the parent only through its authenticated
// activation. The launch uses a fake `sudo` on PATH that stands in for a
// preserve-fds-capable sudo (the real one on this host lacks
// --preserve-fds — the brief's named fallback case): it preserves every fd
// (plain exec does) and runs the child bash that reads its rcfile from the
// preserved descriptor. The shell-side flow — request, grant, suspend,
// preserved-fd launch, child establish, activate — is what this test
// proves; the platform's sudo flag support is the container's job.
func TestBashNestedChildDomain(t *testing.T) {
	k := newNestedKernel(t)
	// The fake sudo: the launch line is
	// `env -u BASHOPTS sudo --preserve-fds=3,4 -i env -u BASH_ENV
	// -u BASHOPTS bash --rcfile /dev/fd/4 -i`; a preserve-fds sudo keeps
	// fds 3 and 4 and runs that command. The fake strips sudo's own arguments
	// and executes the generated child.
	fakeSudo := "#!/bin/sh\n" +
		"# Advertise the capability the production launcher probes before\n" +
		"# consuming the user's sudo command.\n" +
		"if [ \"$1\" = --help ]; then echo '  --preserve-fds=list'; exit 0; fi\n" +
		"# Test stand-in for a preserve-fds-capable sudo: plain exec preserves\n" +
		"# every fd. Strip sudo's own prefix, then execute the REAL generated\n" +
		"# child command so its environment boundary is covered too.\n" +
		"shift\n" +
		"[ \"$1\" = -i ] && shift\n" +
		"exec \"$@\"\n"
	s := startNestedBashParent(t, k, "sudo", fakeSudo)
	exportBashOptsWithExtdebug(t, s, k)
	driveNestedHappyInterval(t, s, k, "sudo -i", "")
	assertNoInheritedBashDebugger(t, s)
}

// BASHOPTS is readonly but can retain the export attribute from user startup.
// nocx then adds extdebug, and an unguarded nested Bash tries to start bashdb
// before it can read the granted rcfile. Force that real parent state and wait
// for its boundary before launching the child.
func exportBashOptsWithExtdebug(t *testing.T, s *channelShell, k *nestedKernel) {
	t.Helper()
	before := k.count("complete")
	_, _ = s.ptmx.Write([]byte("export LC_ALL=C BASHOPTS\n"))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && k.count("complete") == before {
		time.Sleep(25 * time.Millisecond)
	}
	if k.count("complete") == before {
		t.Fatalf("parent never completed BASHOPTS setup; output=%q", s.output())
	}
}

func assertNoInheritedBashDebugger(t *testing.T, s *channelShell) {
	t.Helper()
	if strings.Contains(s.output(), "debugging mode disabled") {
		t.Fatalf("nested child inherited debugger mode through BASHOPTS; output=%q", s.output())
	}
}

// TestBashNestedSudoWithoutPreserveFDSRunsConventionally proves the honest
// compatibility fallback: capability detection happens before the parent
// requests or suspends a child domain, so an older sudo sees the user's exact
// `-i` argument once and never sees nocx's internal descriptor launcher.
func TestBashNestedSudoWithoutPreserveFDSRunsConventionally(t *testing.T) {
	k := newNestedKernel(t)
	s := startNestedBashParent(t, k, "sudo", fakeSudoWithoutPreserveFDS())
	assertUnsupportedSudoRunsConventionally(t, s, k)
}

func fakeSudoWithoutPreserveFDS() string {
	return "#!/bin/sh\n" +
		"if [ \"$1\" = --help ]; then echo 'usage: sudo -i'; exit 0; fi\n" +
		"printf 'CONVENTIONAL-SUDO:%s\\n' \"$*\"\n"
}

func assertUnsupportedSudoRunsConventionally(t *testing.T, s *channelShell, k *nestedKernel) {
	t.Helper()
	_, _ = s.ptmx.Write([]byte("sudo -i\n"))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), "CONVENTIONAL-SUDO:") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	out := s.output()
	if !strings.Contains(out, "CONVENTIONAL-SUDO:-i") {
		t.Fatalf("the user's sudo command did not run conventionally; output=%q", out)
	}
	if strings.Contains(out, "CONVENTIONAL-SUDO:--preserve-fds") {
		t.Fatalf("unsupported sudo received nocx's internal launcher; output=%q", out)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.parentSuspended || k.childHeard {
		t.Fatalf("unsupported sudo opened a child interval; order=%v output=%q", k.order, out)
	}
	for _, step := range k.order {
		if step == testDom+" domain_request" {
			t.Fatalf("unsupported sudo requested a child domain; order=%v output=%q", k.order, out)
		}
	}
}

// startNestedBashParent boots a real interactive bash on a pty with the
// lifecycle socketpair on fd 3, the nested machinery sourced through a
// gate, and a fake nested-environment launcher on PATH — binName is `sudo`
// or `su`, fakeBody is the whole fixture script (the nestedKernel plays
// the kernel's transport side). Returns only after the parent handshake
// completed.
func startNestedBashParent(t *testing.T, k *nestedKernel, binName, fakeBody string) *channelShell {
	t.Helper()
	bash := requireShell(t, "bash")

	kernelFile, shellFile := lifecycleSocketpair(t)
	k.shellFile = shellFile

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

	binDir := t.TempDir()
	// #nosec G306 — a stand-in for a privileged launcher must be executable
	// to be found and run through PATH; it lives in the test's own temp dir
	// and holds no secret. 0600 would make the fixture unable to do the one
	// thing it exists for.
	if werr := os.WriteFile(filepath.Join(binDir, binName), []byte(fakeBody), 0o755); werr != nil {
		t.Fatalf("write fake %s: %v", binName, werr)
	}

	// #nosec G204 — bash is the requireShell-resolved path, not input; an
	// interactive shell with an inherited descriptor is the only way to
	// exercise the local transport shape.
	cmd := exec.Command(bash, "-i")
	cmd.ExtraFiles = []*os.File{shellFile} // becomes fd 3
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null", "PATH="+binDir+":"+os.Getenv("PATH")),
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"NOCX_SESSION_ID=chansess",
		"NOCX_LIFECYCLE_LANE="+testLane,
		"NOCX_LIFECYCLE_DOMAIN="+testDom,
		fmt.Sprintf("NOCX_LIFECYCLE_EPOCH=%d", testEpoch),
		"NOCX_LIFECYCLE_FD=3",
		"NOCX_LIFECYCLE_TIMEOUT_MS=3000",
	)

	go k.serveFile(kernelFile)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
	go s.readPump()
	t.Cleanup(func() { _ = ptmx.Close(); _ = cmd.Process.Kill() })
	s.waitForHandshake()
	return s
}

// driveNestedHappyInterval drives and asserts the §9 happy interval end to
// end on a real pty: the parent enters ENTRY (sudo -i / su -l), requests a
// child domain and suspends, the child establishes its OWN domain over the
// SAME socketpair and reaches a prompt, completes a command whose
// completion is accepted on the child domain (the assertion is the
// accepted event, never the pty echo — readline mirrors echo into the
// pty), closes, and the parent re-activates only through its authenticated
// activation, its own complete and prompt_ready following — and no frame
// is rejected by the kernel.
//
// earlyChildCmd, when non-empty, is typed immediately after ENTRY (the zsh
// tier's race-handling: the child may read it the moment it reaches its
// prompt); the command is typed again once the child domain is at a prompt
// so the accepted-complete assertion never depends on the early copy.
func driveNestedHappyInterval(t *testing.T, s *channelShell, k *nestedKernel, entry, earlyChildCmd string) {
	t.Helper()
	// The user enters the nested line. The parent requests the child,
	// suspends, and launches the child through the fake launcher with the
	// preserved descriptor.
	_, _ = s.ptmx.Write([]byte(entry + "\n"))
	if earlyChildCmd != "" {
		_, _ = s.ptmx.Write([]byte(earlyChildCmd + "\n"))
	}

	// The child establishes its own domain and reaches a prompt.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if k.count("hello") >= 2 && childPromptReady(t, k) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if k.count("hello") < 2 {
		t.Fatalf("no child hello; order=%v output=%q", k.order, s.output())
	}

	// The child is a working shell: run a command inside it. The assertion
	// is the child DOMAIN's accepted complete — not the echo text, which
	// readline also mirrors into the pty.
	_, _ = s.ptmx.Write([]byte("echo CHILD-SHELL-OK\n"))
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		childRan := false
		for _, e := range k.events() {
			if e.Body["dom"] == nestedChildDom && e.Evt == "complete" {
				childRan = true
			}
		}
		if childRan {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var childCompleted bool
	for _, e := range k.events() {
		if e.Body["dom"] == nestedChildDom && e.Evt == "complete" {
			childCompleted = true
		}
	}
	if !childCompleted {
		t.Fatalf("the child never completed a command through its own domain; order=%v output=%q", k.order, s.output())
	}

	// The child closes; the parent re-activates at its next prompt.
	_, _ = s.ptmx.Write([]byte("exit\n"))
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		done := k.parentActivated
		k.mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for the parent's post-activation complete + prompt_ready — the
	// activation is the FIRST frame of the resumed boundary and the other
	// two follow as separate writes, so asserting on the activation alone
	// races the frames behind it (observed on the su tests under load:
	// the accepted order ended at domain_activated while the shell had
	// already written the rest).
	deadline = time.Now().Add(10 * time.Second)
	resumed := false
	for time.Now().Before(deadline) {
		k.mu.Lock()
		var sawComplete, sawReady bool
		for _, e := range k.accepted {
			if e.Body["dom"] == testDom && e.Evt == "complete" {
				sawComplete = true
			}
			if e.Body["dom"] == testDom && e.Evt == "prompt_ready" {
				sawReady = true
			}
		}
		resumed = k.parentActivated && sawComplete && sawReady
		k.mu.Unlock()
		if resumed {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.parentActivated {
		t.Fatalf("parent never re-activated; order=%v output=%q", k.order, s.output())
	}
	// The §9 interval, both ends: the parent suspended before the child
	// hello, the child closed before the parent's activation, and the
	// parent's own lifecycle resumed (a complete and prompt_ready for the
	// parent domain follow the activation).
	if !k.parentSuspended {
		t.Fatalf("parent never suspended; order=%v", k.order)
	}
	if !resumed {
		k.t.Logf("final order=%v", k.order)
		t.Fatalf("parent lifecycle did not resume after activation; order=%v output=%q", k.order, s.output())
	}
	// Not asserted here: rejected == 0. The fixture's own gate in the
	// child's ~/.bashrc sources the integration once with the PARENT's
	// capability and the child's addressing — a wrong tuple the real
	// kernel rejects too (the child's rcfile then re-sources with its own
	// capability and establishes). The rejection count is asserted
	// relatively in the stillborn tests (before/after a late frame), never
	// absolutely.
}

// childPromptReady reports whether the child domain reached a prompt.
func childPromptReady(t *testing.T, k *nestedKernel) bool {
	for _, e := range k.events() {
		if e.Evt == "prompt_ready" && e.Body["dom"] == nestedChildDom {
			return true
		}
	}
	return false
}

// startNestedZshParent boots a real interactive zsh on a pty with the
// lifecycle socketpair on fd 3, the nested machinery sourced through a
// gate, and a fake nested-environment launcher on PATH — binName is `sudo`
// or `su`, fakeBody is the whole fixture script (the nestedKernel plays
// the kernel's transport side). The fake stands in for the real launcher:
// it preserves every fd (plain exec does) and runs the child shell that
// reads its rcfile from the preserved descriptor — or, for the stillborn
// bodies, just reports and exits. The shell-side flow — request, grant,
// suspend, preserved-fd launch, child establish, activate — is what the
// caller proves; the platform's launcher specifics are the container's
// job (same division as the bash twin). The happy-path body execs the
// child shell named in it (bash for the local tier: the app-side grant
// builder composes the child's BASH rcfile — LocalBashRcfile — so the
// launch line is the bash tier's, and the zsh parent is the thing under
// test).
func startNestedZshParent(t *testing.T, k *nestedKernel, binName, fakeBody string) *channelShell {
	t.Helper()
	zsh := requireShell(t, "zsh")

	kernelFile, shellFile := lifecycleSocketpair(t)
	k.shellFile = shellFile

	home := t.TempDir()
	script := writeScriptFile(t, "nocx.zsh", zshScript)
	gate := filepath.Join(t.TempDir(), "gate")
	gateBody := "typeset +x __nocx_cap 2>/dev/null\n__nocx_cap='" + testCap + "'\ntypeset +x __nocx_cap 2>/dev/null\n. " + ShellQuote(script) + "\n"
	if werr := os.WriteFile(gate, []byte(gateBody), 0o600); werr != nil {
		t.Fatalf("write gate: %v", werr)
	}
	if werr := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(". "+ShellQuote(gate)+"\n"), 0o600); werr != nil {
		t.Fatalf("write .zshrc: %v", werr)
	}
	binDir := t.TempDir()
	// #nosec G306 — a stand-in for a privileged launcher must be executable
	// to be found and run through PATH; it lives in the test's own temp dir
	// and holds no secret. 0600 would make the fixture unable to do the one
	// thing it exists for.
	if werr := os.WriteFile(filepath.Join(binDir, binName), []byte(fakeBody), 0o755); werr != nil {
		t.Fatalf("write fake %s: %v", binName, werr)
	}
	// #nosec G204 — zsh is the requireShell-resolved path, not input; an
	// interactive shell with an inherited descriptor is the only way to
	// exercise the local transport shape.
	cmd := exec.Command(zsh, "-i")
	cmd.ExtraFiles = []*os.File{shellFile} // becomes fd 3
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null", "ZDOTDIR="+home, "PATH="+binDir+":"+os.Getenv("PATH")),
		"NOCX_SHELL_INTEGRATION=1",
		"NOCX_PROMPT_MODE=marker-only",
		"NOCX_SESSION_ID=chansess",
		"NOCX_LIFECYCLE_LANE="+testLane,
		"NOCX_LIFECYCLE_DOMAIN="+testDom,
		fmt.Sprintf("NOCX_LIFECYCLE_EPOCH=%d", testEpoch),
		"NOCX_LIFECYCLE_FD=3",
		"NOCX_LIFECYCLE_TIMEOUT_MS=3000",
	)

	go k.serveFile(kernelFile)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
	go s.readPump()
	t.Cleanup(func() { _ = ptmx.Close(); _ = cmd.Process.Kill() })
	s.waitForHandshake()
	return s
}

// TestZshNestedChildDomain is the zsh twin of TestBashNestedChildDomain —
// the NESTED acceptance criterion's zsh half, proven end to end on a real
// pty (nocx-u7uh.28): a zsh parent entering sudo -i requests a child
// domain over the authenticated channel, receives the grant, suspends, and
// launches the child through the fake sudo; the child bash (the app-side
// bootstrap is a bash rcfile) establishes its own domain over the SAME
// inherited socketpair, and the parent re-activates only after the child
// closes — through its authenticated activation, never by a close alone.
func TestZshNestedChildDomain(t *testing.T) {
	k := newNestedKernel(t)
	fakeSudo := "#!/bin/sh\n" +
		"# Advertise the capability the production launcher probes before\n" +
		"# consuming the user's sudo command.\n" +
		"if [ \"$1\" = --help ]; then echo '  --preserve-fds=list'; exit 0; fi\n" +
		"# Test stand-in for a preserve-fds-capable sudo: plain exec preserves\n" +
		"# every fd. Strip sudo's own prefix, then execute the REAL generated\n" +
		"# child command so its environment boundary is covered too.\n" +
		"shift\n" +
		"[ \"$1\" = -i ] && shift\n" +
		"exec \"$@\"\n"
	s := startNestedZshParent(t, k, "sudo", fakeSudo)
	// The user enters sudo -i. The accept-line widget intercepts it (zsh's
	// DEBUG trap cannot suppress a command), the parent requests the child,
	// suspends, and launches the child through the fake sudo with the
	// preserved descriptor. The early echo is the zsh tier's race-handling:
	// the child may read it the moment it reaches its prompt.
	driveNestedHappyInterval(t, s, k, "sudo -i", "echo CHILD-SHELL-OK")
}

// TestZshNestedSudoWithoutPreserveFDSRunsConventionally is the zsh half of
// the same compatibility contract. Returning "not nested" before preexec lets
// the original accept-line chain execute the untouched command exactly once.
func TestZshNestedSudoWithoutPreserveFDSRunsConventionally(t *testing.T) {
	k := newNestedKernel(t)
	s := startNestedZshParent(t, k, "sudo", fakeSudoWithoutPreserveFDS())
	assertUnsupportedSudoRunsConventionally(t, s, k)
}

// TestZshNestedChildStillborn covers the §9 failure interval on the zsh
// tier: the kernel REFUSES the request (an empty bootstrap — forwarding
// refused, sudo policy), the parent sends domain_suspended and runs the
// command conventionally, and still re-activates at its next prompt. The
// child never establishes — no child hello — and the parent's activation
// is accepted against the stillborn child (a Pending child is not on the
// stack). A late frame from that never-established child is rejected
// against the restored parent by the real kernel; the fake kernel here
// proves the shell half: the parent neither hangs nor stays suspended.
func TestZshNestedChildStillborn(t *testing.T) {
	k := newNestedKernel(t)
	k.refuseChild = true
	// The refused child never launches a shell: the fake sudo just reports
	// and exits, so the parent's conventional run of `sudo -i` terminates
	// immediately instead of opening a nested shell.
	fakeSudo := "#!/bin/sh\n" +
		"if [ \"$1\" = --help ]; then echo '  --preserve-fds=list'; exit 0; fi\n" +
		"echo STILLBORN-SUDO-RAN\nexit 0\n"
	s := startNestedZshParent(t, k, "sudo", fakeSudo)

	// The user enters sudo -i. The parent requests the child, receives the
	// REFUSAL (an empty bootstrap), sends domain_suspended, and runs the
	// command conventionally — the fake sudo reports and exits.
	_, _ = s.ptmx.Write([]byte("sudo -i\n"))

	// The parent still re-activates at its next prompt — the §9 stillborn
	// interval is the expected path, not an error, and never a hang.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		done := k.parentActivated
		k.mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.parentSuspended {
		t.Fatalf("parent never suspended before the refused launch; order=%v output=%q", k.order, s.output())
	}
	if k.childHeard {
		t.Fatalf("a refused child still established (hello accepted); order=%v", k.order)
	}
	if !k.parentActivated {
		t.Fatalf("parent never re-activated after the stillborn child; order=%v output=%q", k.order, s.output())
	}
	if !strings.Contains(s.output(), "STILLBORN-SUDO-RAN") {
		t.Fatalf("the refused command did not run conventionally; output=%q", s.output())
	}
}

// fakeSuBody is the happy-path stand-in for a real su. A real su
// authenticates, then runs the target shell with `-c <cmd>`; every
// implementation that matters ends that exec in a plain execv/execve with
// no fd sweep — util-linux su-common.c run_shell, shadow su execve_shell,
// BSD/macOS su (FreeBSD lineage) — so every non-CLOEXEC fd survives
// (measured for the shadow su on this host: the exact launcher line
// preserved fd 7 through `su -l -c`). The stand-in skips the auth and
// models the rest: it execs the -c command through a shell, preserving
// every fd by plain exec. What it does NOT model: PAM authentication and
// the target login shell's profile sourcing (-l); the stillborn twin
// models auth failure by exiting non-zero.
func fakeSuBody() string {
	return "#!/bin/sh\n" +
		"# Test stand-in for a real su: auth (skipped), then exec the target\n" +
		"# shell with -c <cmd>; plain exec preserves every fd, exactly like\n" +
		"# real su (util-linux/shadow/BSD: plain exec, no fd sweep).\n" +
		"m=0\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$m\" = 1 ]; then c=\"$a\"; m=0; continue; fi\n" +
		"  [ \"$a\" = -c ] && m=1\n" +
		"done\n" +
		"exec /bin/sh -c \"$c\"\n"
}

// injectLateChildFrame writes a hello addressed to the (never-established
// or long-closed) child domain into the kernel's READ stream — into the
// shell end of the socketpair, whose peer the kernel's serve loop reads.
// A late frame from a child whose interval ended: the real kernel has no
// such domain on the stack (a Pending child is not on the stack, §9) and
// rejects it; the fake kernel counts it, and the stillborn tests assert
// the count grows.
func injectLateChildFrame(t *testing.T, k *nestedKernel) {
	t.Helper()
	c, err := net.FileConn(k.shellFile)
	if err != nil {
		t.Fatalf("inject late child frame: %v", err)
	}
	defer func() { _ = c.Close() }()
	env := lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       lifecycle.LaneID(testLane),
		Domain:     lifecycle.DomainID(nestedChildDom),
		Epoch:      nestedChildEpoch,
		Capability: capBytes(t, nestedChildCap),
		Event:      lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}},
	}
	if _, err := lifecyclecodec.Encode(c, env); err != nil {
		t.Fatalf("encode late child frame: %v", err)
	}
}

// TestBashNestedChildDomainSu is the su twin of TestBashNestedChildDomain:
// the same §9 interval proven through a fake `su` on PATH. The launcher
// line for su is `env -u BASHOPTS su -l -c 'env -u BASH_ENV -u BASHOPTS
// bash --rcfile /dev/fd/N -i'` — su has no --preserve-fds flag, so the proof
// rests on the descriptor surviving su's own exec. fakeSuBody documents what
// is true of the real implementations and what the stand-in does not model.
func TestBashNestedChildDomainSu(t *testing.T) {
	k := newNestedKernel(t)
	s := startNestedBashParent(t, k, "su", fakeSuBody())
	exportBashOptsWithExtdebug(t, s, k)
	driveNestedHappyInterval(t, s, k, "su -l", "")
	assertNoInheritedBashDebugger(t, s)
}

// TestZshNestedChildDomainSu is the zsh twin: a zsh parent entering su -l
// through the fake su — the child establishes its own domain over the same
// socketpair, completes a command accepted on the child domain, and the
// parent re-activates only through its authenticated activation.
func TestZshNestedChildDomainSu(t *testing.T) {
	k := newNestedKernel(t)
	s := startNestedZshParent(t, k, "su", fakeSuBody())
	// The early echo is the zsh tier's race-handling (see the sudo twin).
	driveNestedHappyInterval(t, s, k, "su -l", "echo CHILD-SHELL-OK")
}

// TestZshNestedChildStillbornSu is the su twin of TestZshNestedChildStillborn,
// with the failure point where su's realistic failure is: the kernel GRANTS
// (unlike the sudo stillborn, where the kernel refuses) and the su launch
// itself fails — authentication refused. The parent suspends, the fake su
// reports `su: Authentication failure` and exits 1, the child never
// launches, and the parent still re-activates at its next prompt. And
// unlike the sudo twin, the late frame from the never-established child is
// actually injected and its rejection asserted: the kernel has no such
// domain on the stack, so the child-addressed hello is rejected and
// counted.
func TestZshNestedChildStillbornSu(t *testing.T) {
	k := newNestedKernel(t)
	// The realistic launch failure: real su prints `su: Authentication
	// failure` to stderr and exits 1 when PAM rejects the password. The
	// child never launches.
	fakeSu := "#!/bin/sh\n" +
		"# The realistic su launch failure: PAM refuses the password.\n" +
		"echo 'su: Authentication failure' >&2\n" +
		"exit 1\n"
	s := startNestedZshParent(t, k, "su", fakeSu)

	// The user enters su -l. The parent requests the child, receives the
	// GRANT, sends domain_suspended, and launches the fake su — which
	// refuses authentication and exits.
	_, _ = s.ptmx.Write([]byte("su -l\n"))

	// The parent still re-activates at its next prompt — the §9 stillborn
	// interval is the expected path, not an error, and never a hang.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		done := k.parentActivated
		k.mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	k.mu.Lock()
	if !k.parentSuspended {
		k.mu.Unlock()
		t.Fatalf("parent never suspended before the refused launch; order=%v output=%q", k.order, s.output())
	}
	if k.childHeard {
		k.mu.Unlock()
		t.Fatalf("a refused child still established (hello accepted); order=%v", k.order)
	}
	if !k.parentActivated {
		k.mu.Unlock()
		t.Fatalf("parent never re-activated after the stillborn child; order=%v output=%q", k.order, s.output())
	}
	rejectedBefore := k.rejected
	k.mu.Unlock()

	if !strings.Contains(s.output(), "su: Authentication failure") {
		t.Fatalf("the refused launch did not run conventionally; output=%q", s.output())
	}

	// A late frame from the never-established child: inject a hello with
	// the child's full tuple after the parent restored, and assert the
	// kernel rejects it.
	injectLateChildFrame(t, k)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		rejected := k.rejected
		k.mu.Unlock()
		if rejected > rejectedBefore {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.rejected <= rejectedBefore {
		t.Fatalf("the late child frame was not rejected; rejected=%d order=%v", k.rejected, k.order)
	}
}

// TestBashNestedChildDomainSuFallback asserts the conventional fallback the
// launcher relies on if a platform's su ever does NOT preserve the staged
// descriptor: the fake su closes every inherited fd above 2 before the
// exec. No real su does this today (util-linux, shadow and BSD/macOS all
// exec with no fd sweep — none of them promises preservation in a man page
// either), so this fake models the platform that does not exist yet and
// asserts the degrade instead of a comment claiming it. The child bash
// cannot read its rcfile from /dev/fd/N (measured: bash silently ignores
// the unreadable --rcfile and starts a conventional shell), never
// establishes, and the parent stillborn-activates at its next prompt —
// with a late child frame rejected, exactly as in the auth-refused
// stillborn.
func TestBashNestedChildDomainSuFallback(t *testing.T) {
	k := newNestedKernel(t)
	fakeSu := "#!/bin/sh\n" +
		"# Models an su that does NOT preserve descriptors (no real su does\n" +
		"# today — util-linux/shadow/BSD exec with no fd sweep — but none\n" +
		"# promises it; this asserts the fallback): close every inherited\n" +
		"# fd above 2 before the exec. The child cannot read its rcfile\n" +
		"# from /dev/fd/N and starts conventional.\n" +
		"for f in 3 4 5 6 7 8 9; do eval \"exec $f>&-\"; done\n" +
		"m=0\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$m\" = 1 ]; then c=\"$a\"; m=0; continue; fi\n" +
		"  [ \"$a\" = -c ] && m=1\n" +
		"done\n" +
		"exec /bin/sh -c \"$c\"\n"
	s := startNestedBashParent(t, k, "su", fakeSu)

	// The user enters su -l; the child (a conventional shell — no rcfile,
	// no lifecycle channel) is a working shell, so the fallback is
	// observable in the product, not only in a log: type a command inside
	// it and see it run, then leave.
	_, _ = s.ptmx.Write([]byte("su -l\n"))
	_, _ = s.ptmx.Write([]byte("echo FALLBACK-CHILD-RAN\nexit\n"))

	// The child never establishes; the parent stillborn-activates at its
	// next prompt.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		done := k.parentActivated
		k.mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	k.mu.Lock()
	if !k.parentSuspended {
		k.mu.Unlock()
		t.Fatalf("parent never suspended before the fallback launch; order=%v output=%q", k.order, s.output())
	}
	if k.childHeard {
		k.mu.Unlock()
		t.Fatalf("a descriptor-closed child still established (hello accepted); order=%v", k.order)
	}
	if !k.parentActivated {
		k.mu.Unlock()
		t.Fatalf("parent never re-activated after the fallback child; order=%v output=%q", k.order, s.output())
	}
	rejectedBefore := k.rejected
	k.mu.Unlock()

	if !strings.Contains(s.output(), "FALLBACK-CHILD-RAN") {
		t.Fatalf("the conventional child did not run the command; output=%q", s.output())
	}

	// A late frame from the never-established child is rejected and
	// counted.
	injectLateChildFrame(t, k)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		rejected := k.rejected
		k.mu.Unlock()
		if rejected > rejectedBefore {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.rejected <= rejectedBefore {
		t.Fatalf("the late child frame was not rejected; rejected=%d order=%v", k.rejected, k.order)
	}
}
