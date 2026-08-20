package app

// The epic's one end-to-end check (design §0, bead nocx-m8jwn.8).
//
// What a user can do that they could not before:
//
//	A host reached by typing `ssh` by hand comes up integrated, through a
//	connection nocx owns — and no session, typed or saved, puts the
//	integration bundle or a session secret into the SSH command.
//
// §0's own paragraph is the check, and every clause of it is an assertion
// here:
//
//	over a fixture sshd, a typed `ssh` and a saved connection both reach an
//	integrated prompt; the exec request recorded for each is under 1 KiB and
//	contains neither generation data nor a secret; and with the fixture's
//	MaxSessions at 1 both still reach a working prompt, un-integrated, with a
//	named reason and exactly one authentication.
//
// Plus the epic's sharper half, §11 assertion 7: a taint canary placed in the
// capability and the recovery fence appears in NONE of the emitted command,
// the far host's argv, the environment, any directory entry under any remote
// root written to, product logs, or the shell's history — asserted PER
// SURFACE, because "it is not in the combined output" hides which surface
// leaked.
//
// # Why the canary is a canary and not the real value
//
// The bearers are minted through the production RequestDomain from a reader
// that stamps a marker into every 32-byte read — which is exactly the
// capability and the fence, and nothing else the kernel reads. So the marker
// is INSIDE the secret rather than beside it, and a surface is searched for
// the marker in every encoding a leak could take it through (raw bytes, hex,
// base64) as well as for the two bearers verbatim. The real random value
// alone would be found only in the one encoding the test happened to guess.
//
// # What this file is NOT
//
// It is not a reading of the implementation. Each path is driven through the
// seam a person reaches — a domain_request from a shell that typed `ssh`, or
// a saved profile's Connect — against a REAL OpenSSH server, and what is
// asserted afterwards is what a person would see: a prompt that works, a
// command that runs there, and a named reason when it does not.
//
// It extends the fixtures that already exist (live_sshd_test.go,
// ssh_child_assembly_test.go) rather than standing up a second harness; the
// three seams it needed — a capturable product logger, an injectable kernel
// randomness, and a recordable launcher — are fields on those fixtures,
// default-off, so every existing caller composes exactly as it did.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	crand "crypto/rand"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// The taint canary.

// canaryTag is the marker minted INTO the capability and the recovery fence.
// It names nothing and nobody: a canary that carried a real name would put
// that name into every artifact this repository keeps.
const canaryTag = "NOCX-P8-TAINT-CANARY"

// canaryRand is the kernel's randomness with the tag stamped into every
// 32-byte read. internal/lifecycle reads 32 bytes in exactly two places —
// randomCapability and randomFence — and 8 bytes everywhere else (domain and
// attempt ids), so this taints both bearers and nothing else. Everything past
// the tag stays random, so two domains never mint the same capability.
type canaryRand struct{}

func (canaryRand) Read(p []byte) (int, error) {
	n, err := crand.Read(p)
	if err != nil {
		return n, err
	}
	if len(p) == 32 {
		copy(p, canaryTag)
	}
	return n, nil
}

// canaryProbe is the per-surface scanner. It holds every form a leak could
// take the marker through, plus the two bearers verbatim where the test could
// learn them.
type canaryProbe struct {
	needles []canaryNeedle
}

type canaryNeedle struct {
	what  string
	value string
}

// newCanaryProbe builds the needle set. capHex and fenceHex may be empty when
// a path cannot hand the test that value back; the tag needles still cover
// both bearers, because the tag is inside each of them.
func newCanaryProbe(t *testing.T, capHex, fenceHex string) canaryProbe {
	t.Helper()
	raw := []byte(canaryTag)
	needles := []canaryNeedle{
		{"the canary as raw bytes", canaryTag},
		{"the canary hex-encoded", hex.EncodeToString(raw)},
		{"the canary hex-encoded (upper case)", strings.ToUpper(hex.EncodeToString(raw))},
		{"the canary base64-encoded", base64.StdEncoding.EncodeToString(raw)},
	}
	for _, b := range []struct {
		what string
		hexv string
	}{{"the per-epoch capability", capHex}, {"the recovery fence", fenceHex}} {
		if b.hexv == "" {
			continue
		}
		needles = append(needles, canaryNeedle{b.what + " (hex)", b.hexv})
		if rawBytes, err := hex.DecodeString(b.hexv); err == nil {
			needles = append(needles,
				canaryNeedle{b.what + " (raw bytes)", string(rawBytes)},
				canaryNeedle{b.what + " (base64)", base64.StdEncoding.EncodeToString(rawBytes)})
		}
	}
	return canaryProbe{needles: needles}
}

// scan asserts one surface, naming the surface and the form that leaked. It
// is deliberately one call per surface: "the combined output is clean" is a
// weaker statement than six separate ones and does not say which one leaked.
func (c canaryProbe) scan(t *testing.T, surface, blob string) {
	t.Helper()
	for _, n := range c.needles {
		if strings.Contains(blob, n.value) {
			t.Errorf("SURFACE %s carries %s (%d bytes of surface searched)",
				surface, n.what, len(blob))
		}
	}
}

// scanTree asserts a filesystem surface: every directory ENTRY NAME and every
// file's CONTENT under the root. Both halves are the assertion — a secret in
// a file name is as disclosed as one in a file body, and §11 assertion 8 is
// about exactly that.
func (c canaryProbe) scanTree(t *testing.T, surface, root string) {
	t.Helper()
	if root == "" {
		t.Fatalf("SURFACE %s: no root to walk; the fixture did not provide one", surface)
	}
	files := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		c.scan(t, surface+": the directory entry "+rel, rel)
		if d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p) // #nosec G304 — p comes from WalkDir over a fixture-owned root.
		if rerr != nil {
			return nil
		}
		files++
		c.scan(t, surface+": the contents of "+rel, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("SURFACE %s: walking %s: %v", surface, root, err)
	}
	t.Logf("surface %s: %d file(s) under %s searched for %d needles", surface, files, root, len(c.needles))
}

// ---------------------------------------------------------------------------
// Product logs, as a surface.

// logSink captures everything the product logs, so "product logs" is a
// surface a test can read rather than a stream that went to stderr.
type logSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureProductLogs routes both the logger the compositions are handed AND
// slog's default — which is what every `log.NewSlogAdapter(nil)` inside the
// product resolves to — into one buffer for the test's lifetime. Without the
// default, a component that took the nil logger would log to stderr and its
// output would not be part of the surface at all.
func captureProductLogs(t *testing.T) (*logSink, log.Logger) {
	t.Helper()
	sink := &logSink{}
	h := slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h)
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sink, log.NewSlogAdapter(logger)
}

// ---------------------------------------------------------------------------
// The emitted command, as a surface.

// recordingLauncher records every remote command the product emits on the
// saved-connection path. The product deliberately never logs it (it used to
// carry both bearers), so recording at the seam is the only way to hold the
// exact bytes the server received.
type recordingLauncher struct {
	inner ssh.RemoteLauncher
	mu    sync.Mutex
	cmds  []string
}

func (r *recordingLauncher) StartCommand(shell ssh.ShellKind, opts ssh.LaunchOptions) (string, ssh.RefusalReason, bool) {
	cmd, reason, ok := r.inner.StartCommand(shell, opts)
	r.mu.Lock()
	r.cmds = append(r.cmds, cmd)
	r.mu.Unlock()
	return cmd, reason, ok
}

func (r *recordingLauncher) Prepare(shell ssh.ShellKind, opts ssh.LaunchOptions) (string, ssh.BootstrapRun, ssh.BootstrapGate, bool) {
	return r.inner.Prepare(shell, opts)
}

// only returns the single command this session emitted, failing when there
// was not exactly one: §11 assertion 5 says the same carrier goes out
// whatever the publish did, and more than one emission would mean the
// session re-decided.
func (r *recordingLauncher) only(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cmds) != 1 {
		t.Fatalf("the session emitted %d remote commands, want exactly 1: %q", len(r.cmds), r.cmds)
	}
	return r.cmds[0]
}

// hex64 finds a 64-hex run — the shape of both bearers and of the stage-1
// digest. Every one found in an exec request has to be the digest.
var hex64 = regexp.MustCompile(`[0-9a-f]{64}`)

// assertExecRequest is §0's middle clause, for one path: under 1 KiB, no
// secret, no generation data. The numbers are logged because §0 is a
// measurement and "it fits" is not one.
func assertExecRequest(t *testing.T, probe canaryProbe, path, cmd, bundleRoot string, forbiddenHex ...string) {
	t.Helper()
	if cmd == "" {
		t.Fatalf("%s: no exec request was recorded; there is nothing to measure", path)
	}
	t.Logf("MEASURED %s: the exec request is %d bytes (bound %d)", path, len(cmd), shellintegration.MaxCarrierLen)
	if len(cmd) > shellintegration.MaxCarrierLen {
		t.Errorf("%s: the exec request is %d bytes, past the stated %d-byte bound",
			path, len(cmd), shellintegration.MaxCarrierLen)
	}
	// A secret. The canary covers the two bearers whatever they were; the
	// explicit hex values cover the case where a path handed the test the
	// real one.
	probe.scan(t, path+": the emitted command", cmd)
	for _, h := range forbiddenHex {
		if h != "" && strings.Contains(cmd, h) {
			t.Errorf("%s: the emitted command carries a bearer verbatim", path)
		}
	}
	// Generation data. The bound above already excludes the ~90 KiB bundle
	// the epic removed, so the sharper question is whether ANY of the
	// published generation's bytes travel in the command. Every 64-byte
	// window of every file the publish committed is looked for.
	if bundleRoot != "" {
		assertNoGenerationBytes(t, path, cmd, bundleRoot)
	}
	// The only 64-hex value the design permits in the command is the
	// stage-1 digest, which names public bytes (§4.1).
	if runs := hex64.FindAllString(cmd, -1); len(runs) > 1 {
		t.Errorf("%s: the emitted command carries %d 64-hex values; only the stage-1 digest may be there: %v",
			path, len(runs), runs)
	}
}

// assertNoGenerationBytes fails if any 64-byte window of any file under the
// published bundle root appears in the command. 64 bytes is far past
// coincidence for shell text, and the window walk is what makes the claim
// "no generation data" rather than "no bundle-sized command".
func assertNoGenerationBytes(t *testing.T, path, cmd, bundleRoot string) {
	t.Helper()
	const window = 64
	scanned := 0
	_ = filepath.WalkDir(bundleRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p) // #nosec G304 — fixture-owned bundle root.
		if rerr != nil || len(b) < window {
			return nil
		}
		scanned++
		for i := 0; i+window <= len(b); i += 16 {
			if strings.Contains(cmd, string(b[i:i+window])) {
				rel, _ := filepath.Rel(bundleRoot, p)
				t.Errorf("%s: the emitted command carries generation data — %d bytes of %s appear in it",
					path, window, rel)
				return fs.SkipAll
			}
		}
		return nil
	})
	t.Logf("%s: %d published generation file(s) checked against the emitted command", path, scanned)
}

// ---------------------------------------------------------------------------
// The composed line's argv, for the typed path.

// argvOfComposedLine evaluates the grant's composed line under a real bash
// with a stand-in `ssh` that prints its argv, so the exec request measured is
// the argument the client would actually receive — not a token this test
// unquoted by hand. The same idiom the composer's own tests use.
func argvOfComposedLine(t *testing.T, line string) []string {
	t.Helper()
	binDir := t.TempDir()
	printer := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\000' \"$a\"; done\n"
	// #nosec G306 — a stand-in for ssh must be executable to be found through
	// PATH; temp dir, no secret.
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(printer), 0o755); err != nil {
		t.Fatalf("write argv-printing ssh: %v", err)
	}
	prog := "PATH=" + shellQuoteForSh(binDir) + ":$PATH\n" + line + "\n"
	// #nosec G204 — prog is the production-composed line under test plus a
	// PATH pointing at this test's temp dir; evaluating it under a real bash
	// is the measurement.
	out, err := exec.Command("bash", "-c", prog).Output()
	if err != nil {
		t.Fatalf("evaluating the composed line to read its argv: %v", err)
	}
	argv := strings.Split(string(out), "\x00")
	if n := len(argv); n > 0 && argv[n-1] == "" {
		argv = argv[:n-1]
	}
	if len(argv) == 0 {
		t.Fatalf("the composed line produced no argv at all: %s", line)
	}
	return argv
}

// ---------------------------------------------------------------------------
// The far side's own recordings.

// recording reads one of the fixture's far-side recordings, failing when the
// recorder never fired: a surface nothing wrote is a surface this check
// cannot claim to have asserted, and reporting it as clean would be the
// silent gap AGENTS.md rule 2 exists to close.
func (fx *liveSshd) recording(t *testing.T, name, what string) string {
	t.Helper()
	if fx.recDir == "" {
		t.Fatalf("%s: the fixture was not started with withFarSideRecording()", what)
	}
	b, err := os.ReadFile(filepath.Join(fx.recDir, name)) // #nosec G304 — fixture-owned recording dir.
	if err != nil || len(b) == 0 {
		t.Fatalf("%s: the far-side recorder produced nothing (%v).\n"+
			"The recorder is sourced from the fixture's ~/.bashrc, which bash reads for a\n"+
			"non-interactive `-c` shell when SSH_CLIENT is set. A far side whose login shell\n"+
			"is not bash cannot record its own argv, and this surface is then UNASSERTED\n"+
			"rather than clean — which is the one thing this check may not report as clean.", what, err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Kernels.

// newCanaryKernel is newRecordingKernel with the tainted randomness: the same
// kernel → publisher → acking-emitter composition production wires, minting
// bearers that carry the marker.
func newCanaryKernel() *recordingKernel {
	k := lifecycle.New(lifecycle.Options{Rand: canaryRand{}})
	pub := lifecyclepub.New(k)
	pub.SetEmitter(ackingEmitter{pub: pub})
	return &recordingKernel{Publisher: pub}
}

// ---------------------------------------------------------------------------
// Clause 1 and 2, saved connection: an integrated prompt, and neither bearer
// on any surface.

func TestEpicE2E_ASavedConnectionComesUpIntegratedAndLeaksNeitherBearer(t *testing.T) {
	logs, logger := captureProductLogs(t)
	fx := startLiveSshd(t, true, withFarSideRecording())
	fx.logger = logger
	rec := &recordingLauncher{inner: &remoteLauncherAdapter{
		inner: shellintegration.NewRemoteLauncher(), logger: logger,
	}}
	fx.launcher = rec

	kernel := newCanaryKernel()
	installer := shellintegration.New(logger)
	ch, out := fx.connect(t, kernel, ssh.ShellBash, installer)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("session terminal:\n%s", out.String())
			t.Logf("sshd log:\n%s", fx.logBuf.String())
		}
	})

	// §0 clause 1: an INTEGRATED prompt. The domain is established, and a
	// line typed at that prompt runs there and reports its own fence — which
	// is what makes it integrated rather than merely connected.
	waitFor(t, "the saved connection's domain established", 30*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	att := runLine(t, ch, kernel, "printf 'P8_SAVED_PROMPT\\n'; sleep 0.3", 0)
	fence := fmt.Sprintf("\x1b]1337;NOCX_FENCE;%x\x07", att.Fence)
	waitFor(t, "the typed line's output and its fence", 20*time.Second, func() bool {
		return strings.Contains(out.String(), "P8_SAVED_PROMPT") && strings.Contains(out.String(), fence)
	})

	capHex, fenceHex := kernel.capabilityHex(), kernel.recoveryHex()
	probe := newCanaryProbe(t, capHex, fenceHex)

	// The far shell's own environment, brought onto the terminal so the
	// ENVIRONMENT surface is a thing this test read rather than assumed.
	dumpFarEnvironment(t, ch, out, "P8_SAVED_ENV_DONE")

	// §0 clause 2, for this path.
	assertExecRequest(t, probe, "the saved connection", rec.only(t),
		filepath.Join(fx.home, ".nocx"), capHex, fenceHex)

	// End the session so the far side's history and temp cleanup have run.
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	waitFor(t, "session end after exit", 30*time.Second, func() bool {
		select {
		case <-ch.Done():
			return true
		default:
			return false
		}
	})

	assertEverySurface(t, fx, probe, "the saved connection", out.String(), logs)
}

// ---------------------------------------------------------------------------
// Clause 1 and 2, typed `ssh`: the whole point of the epic.

func TestEpicE2E_ATypedSSHComesUpIntegratedAndLeaksNeitherBearer(t *testing.T) {
	logs, logger := captureProductLogs(t)
	fx := startLiveSshd(t, true, withFarSideRecording())
	fx.logger = logger
	fx.rand = canaryRand{}

	h := newSSHChildHarness(t, fx)
	h.establishParent()
	// This is the seam a person reaches: the shell that owns the terminal
	// saw the user type `ssh`, asked for a child domain, and is handed one
	// opaque line to run.
	h.requestChild("127.0.0.1", fx.fixturePort(), fx.user)
	h.suspendParent()
	waitFor(t, "the parent suspended under the nested session", 15*time.Second, func() bool {
		return h.domainState(h.parent) == lifecycle.DomainSuspended
	})

	agentSock := startInProcessAgent(t, fx)
	wrapperDir := installSSHWrapper(t, fx)
	proc := h.runComposedLine(agentSock, wrapperDir)
	t.Cleanup(proc.kill)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("composed-line terminal:\n%s", proc.out.String())
			t.Logf("sshd log:\n%s", fx.logBuf.String())
			t.Logf("product log:\n%s", logs.String())
		}
	})

	// §0 clause 1: the typed line reaches an INTEGRATED prompt — the child
	// establishes over its own transport, owns the lane at a ready prompt,
	// and a command typed there runs on the far host and completes on the
	// child domain.
	waitFor(t, "the typed session's child domain established", 60*time.Second, func() bool {
		return h.domainState(h.child) == lifecycle.DomainEstablished
	})
	waitFor(t, "the lane owned by the child at a ready prompt", 20*time.Second, func() bool {
		ls := h.laneSnapshot()
		return ls.Domain == h.child && ls.Lifecycle == lifecycle.LifecyclePromptReady
	})
	const farLine = "printf P8_TYPED_PROMPT"
	if _, err := proc.ptmx.Write([]byte(farLine + "\n")); err != nil {
		t.Fatalf("type at the far prompt: %v", err)
	}
	waitFor(t, "the far command completed on the child domain", 30*time.Second, func() bool {
		id, ok := h.facts.attemptFor(h.child, farLine)
		if !ok {
			return false
		}
		a, ok := h.kernel.Attempt(id)
		return ok && a.State == lifecycle.AttemptCompleted &&
			a.ExitCode != nil && *a.ExitCode == 0
	})
	waitFor(t, "the far command's output on the terminal", 20*time.Second, func() bool {
		return strings.Contains(proc.out.String(), "P8_TYPED_PROMPT")
	})

	// The child's capability is learned from FRAME 2 — the bytes the product
	// actually delivered — which is the only place it exists outside the
	// backend now that the command carries neither bearer. The recovery
	// fence has no such seam on this path; the canary covers it, because the
	// canary is inside it.
	childCap, ok := h.win.capability(t)
	if !ok {
		t.Fatal("frame 2 never carried a capability: the typed session's secret was never delivered")
	}
	capHex := hex.EncodeToString(childCap[:])
	probe := newCanaryProbe(t, capHex, "")

	// The environment on the far host.
	if _, err := proc.ptmx.Write([]byte(
		"env; cat /proc/$$/environ | tr '\\000' '\\n'; printf 'P8_TYPED_ENV_DONE\\n'\n")); err != nil {
		t.Fatalf("dump the far environment: %v", err)
	}
	waitFor(t, "the far environment on the terminal", 30*time.Second, func() bool {
		return strings.Contains(proc.out.String(), "P8_TYPED_ENV_DONE")
	})

	// §0 clause 2, for this path. The exec request is the LAST argv word of
	// the line the parent shell runs, read out of a real bash rather than
	// unquoted by hand.
	argv := argvOfComposedLine(t, h.bootstrap)
	execRequest := argv[len(argv)-1]
	t.Logf("MEASURED the typed line: %d argv words, composed line %d bytes", len(argv), len(h.bootstrap))
	for _, want := range []string{"ControlMaster=auto", "ControlPersist=no"} {
		if !containsWordWith(argv, want) {
			t.Errorf("the typed line does not make the user's own process the master (%s missing): %v", want, argv)
		}
	}
	assertExecRequest(t, probe, "the typed ssh", execRequest,
		filepath.Join(fx.home, ".nocx"), capHex)
	// And no OTHER argv word carries a bearer either: the exec request is
	// the last word, but a secret smuggled into an option would be just as
	// present on the far host's command line.
	probe.scan(t, "the typed ssh: the whole composed line", h.bootstrap)

	// End the far session the way a person does, and let the far shell
	// finish before the local pty goes: it writes its history as it exits,
	// and closing the master first kills the client under it — which is how
	// the history surface came back unwritten the first time this ran. The
	// file appearing IS that event, so it is what is waited on.
	if _, err := proc.ptmx.Write([]byte("exit\n")); err != nil {
		t.Fatalf("type exit at the far prompt: %v", err)
	}
	waitFor(t, "the far shell to exit and flush its history", 30*time.Second, func() bool {
		_, statErr := os.Stat(fx.histFile)
		return statErr == nil
	})
	// Now the local end may go. It is killed rather than waited for: the
	// user's own `ssh` is the multiplex master and nocx still holds a control
	// connection to it, so waiting on the client to exit is waiting on an
	// interval this test does not own.
	proc.kill()
	waitFor(t, "the child left the stack", 30*time.Second, func() bool {
		st := h.domainState(h.child)
		return st == lifecycle.DomainClosed || st == lifecycle.DomainLost
	})

	assertEverySurface(t, fx, probe, "the typed ssh", proc.out.String(), logs)
}

// containsWordWith reports whether any argv word contains the substring.
func containsWordWith(argv []string, want string) bool {
	for _, a := range argv {
		if strings.Contains(a, want) {
			return true
		}
	}
	return false
}

// dumpFarEnvironment brings the far shell's environment onto the terminal so
// the environment surface is read rather than assumed, and waits for the
// marker that says the dump finished — an observable state change, never a
// duration.
func dumpFarEnvironment(t *testing.T, ch ssh.Channel, out *outputBuffer, marker string) {
	t.Helper()
	line := "env; cat /proc/$$/environ | tr '\\000' '\\n'; printf '" + marker + "\\n'\n"
	if _, err := ch.Write([]byte(line)); err != nil {
		t.Fatalf("dump the far environment: %v", err)
	}
	waitFor(t, "the far environment on the terminal", 30*time.Second, func() bool {
		return strings.Contains(out.String(), marker)
	})
}

// assertEverySurface is §11 assertion 7, per surface and named per surface.
func assertEverySurface(t *testing.T, fx *liveSshd, probe canaryProbe, path, terminal string, logs *logSink) {
	t.Helper()

	// 1. The far host's argv — recorded from inside the very process that
	//    had it, at the only moment it exists.
	argvRec := fx.recording(t, "argv", path+": the far host's argv")
	probe.scan(t, path+": the far host's argv", argvRec)
	t.Logf("surface %s: the far host's argv — %d bytes recorded across %d process(es)",
		path, len(argvRec), strings.Count(strings.TrimSpace(argvRec), "\n")+1)

	// 2. The environment — twice over: as the far process running our exec
	//    request had it, and as the interactive shell that came out of it
	//    has it (dumped onto the terminal above).
	envRec := fx.recording(t, "environ", path+": the far host's environment")
	if !strings.Contains(envRec, "HOME=") {
		t.Errorf("%s: the recorded far-side environment carries no HOME=; the recording is not an environment", path)
	}
	probe.scan(t, path+": the far host's environment at exec time", envRec)

	// 3. Every remote root written to, including the temp root.
	probe.scanTree(t, path+": the remote home", fx.home)
	probe.scanTree(t, path+": the remote temp root", fx.tmpRoot)

	// 4. Product logs.
	probe.scan(t, path+": the product's logs", logs.String())
	t.Logf("surface %s: product logs — %d bytes searched", path, len(logs.String()))

	// 5. The shell's history. The file has to have been WRITTEN, or this
	//    surface was never exercised and calling it clean would be a lie.
	hist, err := os.ReadFile(fx.histFile) // #nosec G304 — fixture-owned history path.
	if err != nil {
		t.Fatalf("%s: the far shell wrote no history at %s (%v); the history surface is "+
			"unasserted rather than clean", path, fx.histFile, err)
	}
	probe.scan(t, path+": the far shell's history", string(hist))
	t.Logf("surface %s: shell history — %d bytes searched", path, len(hist))

	// 6. And the session's own terminal, which is not on §11's list and is
	//    where a leak would be visible to the user first.
	probe.scan(t, path+": the session's terminal output", terminal)
}

// ---------------------------------------------------------------------------
// Clause 3: MaxSessions 1.

// TestEpicE2E_MaxSessions1LeavesAWorkingUnintegratedPrompt is §0's last
// clause: with the fixture's MaxSessions at 1 both paths still reach a
// working prompt, un-integrated, with a named reason and exactly one
// authentication.
//
// One session slot means the interactive session takes it and every auxiliary
// channel is refused. D3 is what makes that survivable: the adapter is
// mux-only with no fallback, so a refusal refuses the DELIVERY and never
// opens a connection — which is the difference between losing integration and
// costing the user a second password or a second 2FA prompt.
func TestEpicE2E_MaxSessions1LeavesAWorkingUnintegratedPrompt(t *testing.T) {
	t.Run("a typed ssh", func(t *testing.T) {
		logs, logger := captureProductLogs(t)
		fx := startLiveSshd(t, true, withSshdConfig("MaxSessions 1"))
		fx.logger = logger

		h := newSSHChildHarness(t, fx)
		h.establishParent()
		h.requestChild("127.0.0.1", fx.fixturePort(), fx.user)
		h.suspendParent()
		waitFor(t, "the parent suspended", 15*time.Second, func() bool {
			return h.domainState(h.parent) == lifecycle.DomainSuspended
		})

		agentSock := startInProcessAgent(t, fx)
		wrapperDir := installSSHWrapper(t, fx)
		proc := h.runComposedLine(agentSock, wrapperDir)
		t.Cleanup(proc.kill)
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("composed-line terminal:\n%s", proc.out.String())
				t.Logf("product log:\n%s", logs.String())
				t.Logf("sshd log:\n%s", fx.logBuf.String())
			}
		})

		// UN-INTEGRATED, with a NAMED reason. The loader names its terminal
		// outcome on the wire before it execs the native shell, and the
		// bootstrap window is a COPY and never a diversion — "every byte
		// still reaches the renderer, in order, unchanged" — so on this path
		// the named reason is on the user's own terminal, which is where a
		// degrade has to be visible. Waiting for it is also the observable
		// state change that says the bootstrap finished; a native prompt
		// string is not, because the loader execs a LOGIN shell and a login
		// shell does not read the fixture's .bashrc.
		waitFor(t, "the far side to name its terminal outcome", 90*time.Second, func() bool {
			return strings.Contains(proc.out.String(), shellintegration.OutcomePrefix)
		})
		assertNamedRefusal(t, proc.out.String())

		// A WORKING prompt: a line typed at it runs on the far host and its
		// output comes back. The marker is written split so the pty's echo
		// of the typed line cannot be mistaken for the shell's answer.
		if _, err := proc.ptmx.Write([]byte("printf P8_MAXSESS\"\"_TYPED_OK\\n\n")); err != nil {
			t.Fatalf("type at the far prompt: %v", err)
		}
		waitFor(t, "the far prompt running a typed line", 60*time.Second, func() bool {
			return strings.Contains(proc.out.String(), "P8_MAXSESS_TYPED_OK")
		})
		if st := h.domainState(h.child); st == lifecycle.DomainEstablished {
			t.Errorf("the child domain established under MaxSessions 1: the session integrated "+
				"where §0 says it must not (state %d)", st)
		}

		// EXACTLY ONE AUTHENTICATION. This is the whole of D3: a refused
		// session must never buy a second credential use.
		if n := fx.authCount(); n != 1 {
			t.Errorf("the server accepted %d authentications, want exactly 1", n)
		}
		t.Logf("MEASURED a typed ssh under MaxSessions 1: %d authentication(s)", fx.authCount())
	})

	t.Run("a saved connection", func(t *testing.T) {
		logs, logger := captureProductLogs(t)
		fx := startLiveSshd(t, true, withSshdConfig("MaxSessions 1"))
		fx.logger = logger

		// The interactive session holds the one slot, and the PRODUCT is what
		// makes that true: the session channel the user's shell runs on is
		// claimed before any auxiliary channel exists, so under one slot the
		// publish is the one that is refused, every time.
		//
		// This used to be arranged here, by a gated installer that held the
		// publish's first far-side call until the shell was open. That pin was
		// honest while the two raced — the ordering could not be asserted
		// because the product did not fix it — and it is exactly the thing to
		// remove now that it does. Nothing here arranges the order; the
		// installer only RECORDS what happened to the auxiliary call, so the
		// clause is asserted rather than staged.
		installer := &recordingInstaller{inner: shellintegration.New(logger)}

		// The named reason on THIS path is not on the terminal and must not
		// be looked for there: the bootstrap conversation happens on the SSH
		// channel, not on the user's pty, so the loader's outcome token never
		// reaches it. The product's own seam for it is the launcher adapter's
		// reportBootstrapOutcome, which is what feeds the session integration
		// axis the renderer reads — so that is what this asserts.
		outcomes := &reasonRecorder{}
		fx.launcher = &recordingLauncher{inner: &remoteLauncherAdapter{
			inner:                  shellintegration.NewRemoteLauncher(),
			logger:                 logger,
			reportBootstrapOutcome: outcomes.record,
		}}

		kernel := newCanaryKernel()
		ch, out := fx.connect(t, kernel, ssh.ShellBash, installer)
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("session terminal:\n%s", out.String())
				t.Logf("product log:\n%s", logs.String())
			}
		})

		// A WORKING prompt. The first write may be refused while the
		// bootstrap still owns the input (the quarantine), so the write is
		// retried until it is ACCEPTED once — and then the answer is waited
		// for, never re-typed.
		waitFor(t, "the session's input to leave the bootstrap quarantine", 90*time.Second, func() bool {
			_, err := ch.Write([]byte("printf 'P8_MAXSESS%s\\n' _SAVED_OK\n"))
			return err == nil
		})
		waitFor(t, "a working prompt over the saved connection", 60*time.Second, func() bool {
			return strings.Contains(out.String(), "P8_MAXSESS_SAVED_OK")
		})

		// UN-INTEGRATED with a NAMED reason, and no domain established.
		reason, named := outcomes.await(t)
		if !named {
			t.Fatal("the bootstrap never reported an outcome to the session integration axis; " +
				"§0 requires a NAMED reason, and a reason only the log carries is the " +
				"log-only degrade AGENTS.md forbids")
		}
		if reason == ssh.ReasonNone {
			t.Fatal("the bootstrap reported no refusal: the session integrated where §0 says it must not")
		}
		t.Logf("MEASURED the named reason: %s", reason)
		kernel.mu.Lock()
		domain := kernel.domain
		kernel.mu.Unlock()
		if domain != "" {
			if d, ok := kernel.Domain(domain); ok && d.State == lifecycle.DomainEstablished {
				t.Error("the domain established under MaxSessions 1: the session integrated " +
					"where §0 says it must not")
			}
		}
		// And the publish was genuinely refused rather than skipped — which
		// is §0's clause stated as an ORDERING and not as a hope: the one
		// session slot belonged to the user's shell before the publish
		// existed, so the far-side call the publish makes is the one the
		// server has nothing left for.
		attempted, auxErr := installer.firstCall()
		if !attempted {
			t.Error("the publish never ran, so this fixture did not exercise the refusal it exists for")
		}
		if attempted && auxErr == nil {
			t.Error("the publish's first far-side call SUCCEEDED under MaxSessions 1: it took the one " +
				"session slot, which is the slot §0 promises to the user's own shell")
		}
		t.Logf("MEASURED the auxiliary channel under one slot: %v", auxErr)

		if n := fx.authCount(); n != 1 {
			t.Errorf("the server accepted %d authentications, want exactly 1", n)
		}
		t.Logf("MEASURED a saved connection under MaxSessions 1: %d authentication(s)", fx.authCount())

		if _, err := ch.Write([]byte("exit\n")); err != nil {
			t.Fatalf("write exit: %v", err)
		}
	})
}

// assertNamedRefusal fails unless the far side named a terminal outcome from
// the closed vocabulary, and that outcome is not acceptance. The token is on
// the terminal because the loader puts it there before it execs the native
// shell — a degrade the user cannot see is the log-only degrade AGENTS.md
// forbids.
func assertNamedRefusal(t *testing.T, terminal string) {
	t.Helper()
	i := strings.Index(terminal, shellintegration.OutcomePrefix)
	if i < 0 {
		t.Fatalf("the far side named no terminal outcome at all; §0 requires a NAMED reason.\nterminal:\n%s", terminal)
	}
	rest := terminal[i+len(shellintegration.OutcomePrefix):]
	token := strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	token = strings.TrimSpace(strings.TrimSuffix(token, "\r"))
	outcome, known := shellintegration.OutcomeForToken(token)
	if !known {
		t.Fatalf("the far side named %q, which is not in the outcome vocabulary", token)
	}
	if outcome == shellintegration.OutcomeBootstrapAccepted {
		t.Fatalf("the far side named %s: the session integrated where §0 says it must not", outcome)
	}
	t.Logf("MEASURED the named reason: %s", outcome)
}

// reasonRecorder captures what the launcher adapter reports to the session
// integration axis — the product's own seam for "why did this session not
// integrate", and the one a renderer reads.
type reasonRecorder struct {
	mu     sync.Mutex
	got    bool
	reason ssh.RefusalReason
}

func (r *reasonRecorder) record(_ string, reason ssh.RefusalReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = true
	r.reason = reason
}

// await blocks until an outcome has been reported. The bootstrap always
// reaches a terminal outcome (design §7, assertion 26), so this waits on that
// event rather than on a duration; the deadline exists only so a session left
// in `starting` reports rather than hangs.
func (r *reasonRecorder) await(t *testing.T) (ssh.RefusalReason, bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got, reason := r.got, r.reason
		r.mu.Unlock()
		if got {
			return reason, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "", false
}

// recordingInstaller is the production installer with its first far-side call
// RECORDED. It arranges nothing — no gate, no ordering, no substitute
// behaviour — so what a test reads off it is what the product did.
//
// Its predecessor held that first call until the test released it, which fixed
// which of two concurrent openers reached the single session slot. That was
// honest while the product left the order open; it is a pin on the very clause
// §0 now guarantees, so it is gone rather than kept beside the guarantee.
type recordingInstaller struct {
	inner ssh.RemoteInstaller
	mu    sync.Mutex
	tried bool
	// homeErr is what the far side answered the FIRST auxiliary call with.
	// Under one session slot it is the refusal, and that refusal is the
	// observable proof the interactive session already held the slot.
	homeErr error
}

func (g *recordingInstaller) GetRemoteHome(client *gossh.Client) (string, error) {
	home, err := g.inner.GetRemoteHome(client)
	g.mu.Lock()
	if !g.tried {
		g.tried = true
		g.homeErr = err
	}
	g.mu.Unlock()
	return home, err
}

func (g *recordingInstaller) EnsureInstalledRemote(ctx context.Context, client *gossh.Client, home string) error {
	return g.inner.EnsureInstalledRemote(ctx, client, home)
}

func (g *recordingInstaller) UninstallRemote(ctx context.Context, client *gossh.Client, home string) (removed, conflicts []string, err error) {
	return g.inner.UninstallRemote(ctx, client, home)
}

// firstCall reports whether the publish reached the far side at all, and what
// that first call answered.
func (g *recordingInstaller) firstCall() (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tried, g.homeErr
}
