package shellintegration

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Stage-1 exercised AS SHELL, on a real terminal, driven exactly as the Go
// sender drives it: the emitted carrier is the command, frame 1 is stage-1
// itself, frame 2 is the secret. Nothing here waits on a duration — every
// step waits for a marker the far side emits — and nothing is faked between
// the loader and the shell the user would get.
//
// The harness is carrier_exec_test.go's (startLoader, loaderPath, loaderEnv):
// one topology, one set of controlled tools, one way to assert a usable
// native prompt.

const (
	// canaryCap and canaryFence are what a secret must never become. They
	// are hex — the shape the tiers accept — so the assertion is about
	// where the value went, never about it being rejected as malformed.
	canaryCap   = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	canaryFence = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// stageOpts is the LaunchOptions every stage-1 test addresses, with the id
// shapes the product actually mints.
func stageOpts() LaunchOptions {
	o := carrierOpts()
	o.Capability = canaryCap
	o.Recovery = canaryFence
	return o
}

// fakeLaunchScript stands in for the installed launch carrier. It reports
// every surface the assertions care about — its own argv, its environment,
// what the inherited descriptor holds, and the terminal state it was handed —
// and then becomes an interactive shell, so "the session is usable" is
// asserted the same way as on a refusal.
const fakeLaunchScript = `#!/bin/sh
printf 'LAUNCH sid=[%s] pin=[%s]\n' "$1" "$2"
printf 'LAUNCH_TERMIOS=%s\n' "$(stty -g)"
printf 'LAUNCH_ENV=[%s]\n' "$(env | tr '\n' ' ')"
printf 'LAUNCH_ARGV=[%s]\n' "$(ps -o args= -p $$ 2>/dev/null | tr '\n' ' ')"
printf 'LAUNCH_ALLARGV=[%s]\n' "$(ps -eo args= 2>/dev/null | tr '\n' ' ')"
printf 'LAUNCH_LIFECYCLE=[%s][%s][%s][%s]\n' "${NOCX_LIFECYCLE_LANE-}" "${NOCX_LIFECYCLE_DOMAIN-}" "${NOCX_LIFECYCLE_EPOCH-}" "${NOCX_LIFECYCLE_PORT-}"
printf 'LAUNCH_CAPFD=[%s]\n' "${NOCX_CAP_FD-}"
__c=; __f=
if [ -n "${NOCX_CAP_FD-}" ]; then
  { IFS= read -r __c <&7 && IFS= read -r __f <&7; } 2>/dev/null || :
  { eval "exec ${NOCX_CAP_FD}<&-"; } 2>/dev/null || :
fi
printf 'LAUNCH_CAP=[%s] LAUNCH_FENCE=[%s]\n' "$__c" "$__f"
if ls "${TMPDIR:-/tmp}"/nocx.* >/dev/null 2>&1; then printf 'LAUNCH_TEMP_PRESENT\n'; else printf 'LAUNCH_TEMP_GONE\n'; fi
printf 'LAUNCH_DONE\n'
exec /bin/sh -i
`

// installFakeLaunch writes the stand-in launch carrier into a fixture home.
func installFakeLaunch(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// #nosec G306 — test fixture, and the real carrier is 0700 too.
	if err := os.WriteFile(filepath.Join(dir, launchName), []byte(fakeLaunchScript), 0o700); err != nil {
		t.Fatalf("write fake launch: %v", err)
	}
}

// stageTools is what the loader, stage-1 and the fake launch need on PATH.
var stageTools = append(append([]string{}, loaderBaseTools...),
	"sha256sum",              // the loader's hasher
	"ps", "env", "tr", "cat", // what the fake launch reports its surfaces with
	"chmod", // what the filesystem-failure injections build their cases with
)

// stageSession starts one bootstrap: the real carrier, the real stage-1
// frame, and a terminal. The caller sends frame 2.
type stageSession struct {
	*loaderSession
	home string
	tmp  string
	pre  string // the terminal state before the loader touched it
}

// startStage runs the carrier for shell/opts with stage-1 as frame 1 and
// waits for STAGE_READY.
func startStage(t *testing.T, shell ShellKind, opts LaunchOptions, mutate func(home, tmp, path string)) *stageSession {
	t.Helper()
	return startStageOn(t, shell, opts, mutate, stdoutOnTerminal)
}

// startStageOn is startStage with the output topology named. stdoutOnPipe is
// what a test needs to watch an outcome emitted AFTER the terminal's master
// side has gone away — the only way to observe the EOF-mid-frame path.
func startStageOn(t *testing.T, shell ShellKind, opts LaunchOptions, mutate func(home, tmp, path string), mode loaderStdout) *stageSession {
	t.Helper()
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, stageTools...)
	installFakeLaunch(t, home)
	if mutate != nil {
		mutate(home, tmp, path)
	}

	stage, err := Stage1Frame(shell, opts)
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	o := opts
	o.StageDigest = StageDigest(stage)
	cmd, reason, ok := NewRemoteLauncher().StartCommand(shell, o)
	if !ok {
		t.Fatalf("carrier refused: %q", reason)
	}
	// The terminal state BEFORE the loader saved it, printed by the same
	// shell on the same terminal: assertion 17 compares the restored state
	// against exactly this.
	s := startLoader(t, `printf "TERMIOS_PRE=%s\n" "$(stty -g)"; exec `+cmd,
		loaderEnv(home, path, tmp), mode)
	ss := &stageSession{loaderSession: s, home: home, tmp: tmp}
	ss.waitFor("TERMIOS_PRE=")
	ss.pre = ss.capture("TERMIOS_PRE=")
	ss.waitFor(LoaderReadyToken)
	ss.sendFrame(FrameStageSeq, stage)
	ss.waitFor(StageReadyToken)
	return ss
}

// signalGroup sends sig to the child's process GROUP, which is what a real
// session receives. Signalling the shell alone would prove nothing: a POSIX
// shell defers a trap until the command in the foreground finishes, and the
// command in the foreground here is the dd(1) reading the frame — so the
// shell would sit on the signal until bytes arrived that never will. The
// child has its own session (Setsid), so the group is exactly the bootstrap.
func (s *stageSession) signalGroup(sig syscall.Signal) {
	s.t.Helper()
	s.waitForFrameRead()
	if err := syscall.Kill(-s.cmd.Process.Pid, sig); err != nil {
		s.t.Fatalf("signal the bootstrap group with %v: %v", sig, err)
	}
}

// waitForFrameRead blocks until the bootstrap shell has forked the reader it
// waits in — an observable state, not a duration.
//
// It is necessary, and the reason is a MEASURED shell behaviour rather than a
// guess: a signal delivered in the window between the READY line and the
// fork is lost by /bin/sh here (bash 5.3) for HUP and QUIT — the trap never
// runs and the shell stays blocked in the read that follows. Delivered once
// the reader exists, every one of the four fires the trap. So the test waits
// for the state the assertion is actually about ("a signal arriving while the
// bootstrap waits for its frame") instead of racing it.
func (s *stageSession) waitForFrameRead() {
	s.t.Helper()
	pgrep, err := exec.LookPath("pgrep")
	if err != nil {
		s.t.Fatalf("pgrep is required by this test and missing from PATH (%v).\n"+
			"The signal cases must observe that the bootstrap is inside its frame read\n"+
			"before signalling it; without that they race a window the shell loses.", err)
	}
	deadline := time.After(30 * time.Second)
	for {
		out, _ := exec.Command(pgrep, "-P", strconv.Itoa(s.cmd.Process.Pid)).Output() // #nosec G204 — pid of our own child
		if len(strings.TrimSpace(string(out))) > 0 {
			return
		}
		select {
		case <-deadline:
			s.t.Fatal("the bootstrap never entered its frame read")
		default:
		}
	}
}

// assertStageNeverRan proves an unverified stage-1 was not executed.
//
// It is an ORDERING check, not a search, and deliberately: on the paths that
// refuse before the body is read, the frame's bytes reach the native login
// shell as ordinary input and are echoed back — so the stage-1 TEXT appears
// in the output, including the line that would announce it. What must not
// happen is that announcement arriving BEFORE the refusal, which is the only
// thing that could mean the shell actually ran it.
func (s *stageSession) assertStageNeverRan(outcome Outcome) {
	s.t.Helper()
	out := s.output()
	refusal := strings.Index(out, OutcomePrefix+OutcomeToken(outcome))
	announced := strings.Index(out, StageReadyToken)
	if refusal >= 0 && announced >= 0 && announced < refusal {
		s.t.Errorf("stage-1 announced itself before the refusal; output:\n%s", out)
	}
}

// capture returns the rest of the line following marker in the output.
func (s *stageSession) capture(marker string) string {
	s.t.Helper()
	out := s.output()
	i := strings.Index(out, marker)
	if i < 0 {
		s.t.Fatalf("output has no %q:\n%s", marker, out)
	}
	rest := out[i+len(marker):]
	if j := strings.IndexAny(rest, "\r\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// assertTermiosRestored proves the exact saved state came back: the value the
// native login shell (or the launch carrier) reports must byte-equal what the
// terminal held before the loader ran.
func (s *stageSession) assertTermiosRestored(marker string) {
	s.t.Helper()
	got := s.capture(marker)
	if got != s.pre {
		s.t.Errorf("restored termios %q, want the saved %q", got, s.pre)
	}
}

// assertNoSecretOnDisk walks the fixture home and the temp root: no directory
// entry may NAME a secret and no file may CONTAIN one (design §11 assertion 7
// and 8, the filesystem surfaces).
func (s *stageSession) assertNoSecretOnDisk() {
	s.t.Helper()
	for _, root := range []string{s.home, s.tmp} {
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			name := filepath.Base(p)
			if strings.Contains(name, canaryCap) || strings.Contains(name, canaryFence) {
				s.t.Errorf("a directory entry names a secret: %s", p)
			}
			if info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}
			b, rerr := os.ReadFile(p) // #nosec G304 — fixture tree
			if rerr != nil {
				return nil
			}
			if strings.Contains(string(b), canaryCap) || strings.Contains(string(b), canaryFence) {
				s.t.Errorf("a file under %s contains a secret: %s", root, p)
			}
			return nil
		})
		if err != nil {
			s.t.Fatalf("walk %s: %v", root, err)
		}
	}
}

// refuse drives the refusal shape: frame 2 arrives, stage-1 names outcome and
// the native login shell must be usable.
func (s *stageSession) assertRefused(outcome Outcome) {
	s.t.Helper()
	s.waitFor(OutcomePrefix + OutcomeToken(outcome))
	// The fixture ~/.profile prints the restored state, so waiting for it
	// is also how "the native login shell got that far" is observed.
	s.waitFor("TERMIOS_POST=")
	s.assertTermiosRestored("TERMIOS_POST=")
	s.assertNativeShellIsUsable()
	if strings.Contains(s.output(), "LAUNCH ") {
		s.t.Errorf("a refused bootstrap still exec'd the launcher; output:\n%s", s.output())
	}
	s.assertNoSecretOnDisk()
}

// ---------------------------------------------------------------------------
// The happy path
// ---------------------------------------------------------------------------

// TestStage1_HandsTheCapabilityOverAnUnlinkedDescriptor is the whole
// mechanism in one run: the secret reaches the shell, and it reaches it
// through a descriptor whose name was already gone before the first byte was
// written.
func TestStage1_HandsTheCapabilityOverAnUnlinkedDescriptor(t *testing.T) {
	opts := stageOpts()
	s := startStage(t, ShellAuto, opts, nil)

	secret, err := SecretFrame(opts)
	if err != nil {
		t.Fatalf("SecretFrame: %v", err)
	}
	s.sendFrame(FrameSecretSeq, secret)
	s.waitFor("LAUNCH_DONE")

	if got := s.capture("LAUNCH_CAP="); got != "["+canaryCap+"] LAUNCH_FENCE=["+canaryFence+"]" {
		t.Errorf("the launcher read %s from the descriptor, want the capability and the fence", got)
	}
	if got := s.capture("LAUNCH sid="); got != "["+opts.SessionID+"] pin=[]" {
		t.Errorf("launcher argv = %s, want the session id and the empty (auto) pin", got)
	}
	if got := s.capture("LAUNCH_LIFECYCLE="); got != fmt.Sprintf("[%s][%s][%d][%d]",
		opts.Lane, opts.Domain, opts.Epoch, opts.LifecyclePort) {
		t.Errorf("lifecycle addressing = %s, want the launch options", got)
	}
	// The number is stated once, from the constant stage-1 exports it under.
	capFD := strconv.Itoa(CapabilityFD)
	if got := s.capture("LAUNCH_CAPFD="); got != "["+capFD+"]" {
		t.Errorf("descriptor number = %s, want [%s]", got, capFD)
	}
	if !strings.Contains(s.output(), "LAUNCH_TEMP_GONE") {
		t.Errorf("the temp name survived into the launcher; output:\n%s", s.output())
	}

	// Per surface, not in aggregate (design §11 assertion 7). The far
	// side reports each surface verbatim and this side does the matching,
	// so the fixture never has to carry the canary in order to look for it.
	for _, surface := range []string{"LAUNCH_ENV=", "LAUNCH_ARGV=", "LAUNCH_ALLARGV="} {
		got := s.capture(surface)
		if strings.Contains(got, canaryCap) || strings.Contains(got, canaryFence) {
			t.Errorf("a secret reached %s %s", strings.TrimSuffix(surface, "="), got)
		}
	}
	s.assertNoSecretOnDisk()
	s.assertTermiosRestored("LAUNCH_TERMIOS=")

	// The shell the user is left with answers.
	s.write([]byte("echo PROMPT_ALIVE\n"))
	s.waitFor("PROMPT_ALIVE")
	// And the history of that shell holds neither bearer.
	s.assertNoSecretOnDisk()
}

// TestStage1_CarriesTheProfileShellPin: the pin cannot travel in the command
// (design §4.1 does not list it, and there is no room), so it travels in the
// frame — and the launch carrier prefers it over its own $SHELL dispatch.
func TestStage1_CarriesTheProfileShellPin(t *testing.T) {
	for _, tc := range []struct {
		kind ShellKind
		want string
	}{
		{ShellBash, "bash"},
		{ShellZsh, "zsh"},
		{ShellUnknown, "unknown"},
		{ShellAuto, ""},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			opts := stageOpts()
			s := startStage(t, tc.kind, opts, nil)
			secret, err := SecretFrame(opts)
			if err != nil {
				t.Fatalf("SecretFrame: %v", err)
			}
			s.sendFrame(FrameSecretSeq, secret)
			s.waitFor("LAUNCH_DONE")
			if got := s.capture("LAUNCH sid="); got != "["+opts.SessionID+"] pin=["+tc.want+"]" {
				t.Errorf("launcher argv = %s, want pin [%s]", got, tc.want)
			}
		})
	}
}

// TestStage1_RefusalFrameStartsTheShellWithNoCapability: the lifecycle
// channel could not be opened, so nothing was minted (design §6.1) and
// stage-1 is TOLD so rather than left to time out. The session still comes up
// — it simply has no channel.
func TestStage1_RefusalFrameStartsTheShellWithNoCapability(t *testing.T) {
	opts := stageOpts()
	s := startStage(t, ShellAuto, opts, nil)
	s.sendFrame(FrameSecretSeq, RefusalFrame(OutcomeChannelUnavailable))
	s.waitFor("LAUNCH_DONE")

	if got := s.capture("LAUNCH_CAPFD="); got != "[]" {
		t.Errorf("a refused bootstrap still handed over a descriptor: %s", got)
	}
	if got := s.capture("LAUNCH_CAP="); got != "[] LAUNCH_FENCE=[]" {
		t.Errorf("a refused bootstrap produced bearers: %s", got)
	}
	if got := s.capture("LAUNCH_LIFECYCLE="); got != "[][][][]" {
		t.Errorf("a refused bootstrap exported lifecycle addressing: %s", got)
	}
	if !strings.Contains(s.output(), "LAUNCH_TEMP_GONE") {
		t.Errorf("a refused bootstrap left a temp file; output:\n%s", s.output())
	}
}

// ---------------------------------------------------------------------------
// Validation (design §11 assertion 9)
// ---------------------------------------------------------------------------

func TestStage1_RefusesAFrameNamingAnotherSessionDomainOrEpoch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		twist func(*LaunchOptions)
	}{
		{"session", func(o *LaunchOptions) { o.SessionID = "0000000000000000000000000000dead" }},
		{"domain", func(o *LaunchOptions) { o.Domain = "dom-000000000000dead" }},
		{"epoch", func(o *LaunchOptions) { o.Epoch = o.Epoch - 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := stageOpts()
			s := startStage(t, ShellAuto, opts, nil)

			other := stageOpts()
			tc.twist(&other)
			secret, err := SecretFrame(other)
			if err != nil {
				t.Fatalf("SecretFrame: %v", err)
			}
			s.sendFrame(FrameSecretSeq, secret)
			s.assertRefused(OutcomeSecretNotForThisSession)
		})
	}
}

// TestStage1_ReplayedFrameIsNeverActedOnTwice: after a terminal outcome a
// frame is never recognised again, because the reader that recognised it has
// exec'd. The replay must not produce a second capability, a second launcher
// or a second outcome.
func TestStage1_ReplayedFrameIsNeverActedOnTwice(t *testing.T) {
	opts := stageOpts()
	s := startStage(t, ShellAuto, opts, nil)
	secret, err := SecretFrame(opts)
	if err != nil {
		t.Fatalf("SecretFrame: %v", err)
	}
	s.sendFrame(FrameSecretSeq, secret)
	s.waitFor("LAUNCH_DONE")

	before := s.output()
	s.sendFrame(FrameSecretSeq, secret)
	s.write([]byte("echo AFTER_REPLAY\n"))
	s.waitFor("AFTER_REPLAY")
	after := s.output()

	if strings.Count(after, "LAUNCH_DONE") != strings.Count(before, "LAUNCH_DONE") {
		t.Errorf("the replayed frame started a second launcher; output:\n%s", after)
	}
	if strings.Count(after, "LAUNCH_CAP=") != strings.Count(before, "LAUNCH_CAP=") {
		t.Errorf("the replayed frame produced a second capability read; output:\n%s", after)
	}
	s.assertNoSecretOnDisk()
}

// TestStage1_MalformedSecretsAreRefused: a bearer that is not the shape a
// bearer has never reaches a descriptor.
func TestStage1_MalformedSecretsAreRefused(t *testing.T) {
	opts := stageOpts()
	s := startStage(t, ShellAuto, opts, nil)
	// Built by hand: SecretFrame refuses this at the sender, which is the
	// point — the far side must refuse it too, because a frame is not
	// trusted for having been well-formed when it left.
	body := fmt.Sprintf("%s %s %s %s %d\nnot-hex\n%s\n",
		FrameMagic, secretFrameSecret, opts.SessionID, opts.Domain, opts.Epoch, canaryFence)
	s.sendFrame(FrameSecretSeq, []byte(body))
	s.assertRefused(OutcomeSecretMalformed)
}

// ---------------------------------------------------------------------------
// Frame protocol failures (design §11 assertion 16)
// ---------------------------------------------------------------------------

func TestStage1_OverCapSecretFrameIsRefusedBeforeItIsRead(t *testing.T) {
	opts := stageOpts()
	s := startStage(t, ShellAuto, opts, nil)
	// FrameHeader refuses to build this, which is its job; the loader's own
	// refusal is what this test is about, so the header is hand-built.
	s.write([]byte(fmt.Sprintf("%s %d %8d\n", FrameMagic, FrameSecretSeq, MaxSecretFrameLen+1)))
	s.assertRefused(OutcomeSecretTooLarge)
}

func TestStage1_PartialSecretFrameNamesInterrupted(t *testing.T) {
	opts := stageOpts()
	// The outcome is emitted after the input side is gone, so the output
	// side must outlive it.
	s := startStageOn(t, ShellAuto, opts, nil, stdoutOnPipe)
	secret, err := SecretFrame(opts)
	if err != nil {
		t.Fatalf("SecretFrame: %v", err)
	}
	h, err := FrameHeader(FrameSecretSeq, len(secret))
	if err != nil {
		t.Fatalf("FrameHeader: %v", err)
	}
	// A header that promises more than the body delivers, then EOF: the
	// terminal's master side closes and the body read ends short.
	s.write([]byte(h))
	s.write(secret[:len(secret)-8])
	_ = s.master.Close()
	s.waitFor(OutcomePrefix + OutcomeToken(OutcomeBootstrapInterrupted))
	s.assertNoSecretOnDisk()
}

func TestStage1_MalformedSecretHeaderNamesProtocol(t *testing.T) {
	opts := stageOpts()
	s := startStage(t, ShellAuto, opts, nil)
	s.write([]byte("NOTAFRAME 1        7\n"))
	s.assertRefused(OutcomeBootstrapProtocol)
}

// TestStage1_BytesAfterACompleteFrameDoNotDisturbTheBootstrap: the reader
// consumes exactly the declared length and stops, so bytes that arrive in the
// SAME write as the frame are not taken as part of it. `dd bs=1 count=$L` is
// what makes that true, chosen over a shell `read` for the reason stage1.go
// gives: a `read` builtin may pull a whole buffer off a terminal and hand
// back one line, which is what makes a reader over-consume. Over-consumption
// is observable here — the frame would be short, its validation would fail,
// and there would be no LAUNCH_CAP to read — so this still catches the defect
// the check exists for.
//
// WHAT IT DELIBERATELY DOES NOT ASSERT, and why (nocx-wawqq). It used to
// finish by requiring that the trailing bytes never reach the user's shell,
// and it went red on a loaded CI runner with the trailing command actually
// executed. Nothing in stage-1 or in the launch carrier discards the tty
// input queue: `R` restores with `stty "$T"`, and that restores through
// TCSADRAIN, which drains OUTPUT and leaves input alone. What normally
// removes the trailer is the termios transition itself, raw back to
// canonical, and whether the bytes have reached the line discipline's read
// buffer by then is the kernel's business — one write to a master is not one
// delivery to the slave. So the old assertion was on an accident, and a test
// that guards an accident reports a machine's load rather than a defect.
//
// The guarantee it looked like it was making is real and lives one layer up:
// internal/session/bootstrap_window.go holds the input quarantine as an
// interval with both ends named, and while it is open the USER's keystrokes
// are refused in nocx and never reach the far tty at all. ADR-0024 states the
// same fact from the other side — "inside the window there is no shell, no
// user program and no user keystroke" — and puts a process that can already
// write this PTY outside the boundary, on the ground that it can read the
// same bytes anyway.
func TestStage1_BytesAfterACompleteFrameDoNotDisturbTheBootstrap(t *testing.T) {
	opts := stageOpts()
	s := startStage(t, ShellAuto, opts, nil)
	secret, err := SecretFrame(opts)
	if err != nil {
		t.Fatalf("SecretFrame: %v", err)
	}
	h, err := FrameHeader(FrameSecretSeq, len(secret))
	if err != nil {
		t.Fatalf("FrameHeader: %v", err)
	}
	// Frame and trailing bytes in ONE write, so the far side cannot have
	// exec'd in between: whatever consumes the frame sees the trailer in
	// the same buffer.
	s.write(append(append([]byte(h), secret...), []byte("echo AFTER_THE_FRAME\n")...))
	s.waitFor("LAUNCH_DONE")
	// The capability and the fence, read by the launcher out of the
	// descriptor stage-1 handed it. Getting these back is the proof that the
	// frame was parsed exactly, trailer and all notwithstanding.
	if got := s.capture("LAUNCH_CAP="); got != "["+canaryCap+"] LAUNCH_FENCE=["+canaryFence+"]" {
		t.Errorf("the launcher read %s from the descriptor, want the capability and the fence", got)
	}
}

// ---------------------------------------------------------------------------
// The filesystem failure ordering (design §5.2, assertion 8)
// ---------------------------------------------------------------------------

// fakeMktemp installs a mktemp stand-in on the controlled PATH. Injecting the
// failures at the temp file is what makes each §5.2 case reachable: the real
// mktemp on a writable TMPDIR cannot fail in four different ways on demand.
func fakeMktemp(t *testing.T, path, tmp, body string) {
	fakeMktempFrom(t, path, tmp, 2, body)
}

// fakeMktempFrom injects from the nth call onwards. n=2 is stage-1's call;
// n=1 is the loader's, which happens first and with the same template.
func fakeMktempFrom(t *testing.T, path, tmp string, n int, body string) {
	t.Helper()
	real, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatalf("mktemp is required by this test and missing from PATH: %v", err)
	}
	// The symlink loaderPath made must go first: writing THROUGH it would
	// follow it to the real mktemp, which lives on a read-only store.
	dst := filepath.Join(path, "mktemp")
	if rerr := os.Remove(dst); rerr != nil && !os.IsNotExist(rerr) {
		t.Fatalf("remove the real mktemp from the controlled PATH: %v", rerr)
	}
	// ONLY THE SECOND CALL IS INJECTED, and the counter is the whole reason
	// this is not a one-line stub: the LOADER calls mktemp too, for frame 1,
	// with the same template. A stub that failed every call would inject the
	// failure one layer too early and prove nothing about stage-1 — measured,
	// not assumed: the first attempt named the loader's own no-temp outcome
	// and never reached the code under test.
	script := "#!/bin/sh\n" +
		"c=" + filepath.Join(tmp, ".mktemp-calls") + "\n" +
		"n=$(cat \"$c\" 2>/dev/null || echo 0)\n" +
		"n=$((n+1))\n" +
		"printf '%s' \"$n\" > \"$c\"\n" +
		"if [ \"$n\" -lt " + strconv.Itoa(n) + " ]; then exec " + real + " \"$@\"; fi\n" +
		body
	// #nosec G306 — test fixture on a PATH the test owns.
	if err := os.WriteFile(dst, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake mktemp: %v", err)
	}
}

func TestStage1_FilesystemFailureOrdering(t *testing.T) {
	// Each case is one row of design §5.2, and every one of them must
	// restore the terminal, leave no secret anywhere, and leave a working
	// native prompt.
	cases := []struct {
		name    string
		outcome Outcome
		// mktemp body: prints the path it "created".
		mktemp func(dir string) string
		// after runs once the outcome has been named, for the assertions
		// that are about the filesystem rather than the terminal.
		after func(t *testing.T, dir string)
	}{
		{
			name:    "mktemp fails",
			outcome: OutcomeNoSecureTemp,
			mktemp:  func(string) string { return "exit 1\n" },
		},
		{
			name:    "the read descriptor cannot be opened",
			outcome: OutcomeCapabilityFDUnavailable,
			// A name in a directory that does not exist: nothing is
			// created, so nothing can be opened.
			mktemp: func(dir string) string {
				return "printf '%s\\n' " + dir + "/absent/nocx.XXXXXX\n"
			},
		},
		{
			name:    "the write descriptor cannot be opened",
			outcome: OutcomeCapabilityFDUnavailable,
			// Readable, not writable: the read opens, the write does
			// not, and the name must be gone afterwards.
			mktemp: func(dir string) string {
				return "f=" + dir + "/ro.tmp\n: >\"$f\"\nchmod 0400 \"$f\"\nprintf '%s\\n' \"$f\"\n"
			},
			after: func(t *testing.T, dir string) {
				if _, err := os.Stat(filepath.Join(dir, "ro.tmp")); !os.IsNotExist(err) {
					t.Errorf("the temp name survived a failed write-open: %v", err)
				}
			},
		},
		{
			name:    "the unlink fails, so nothing is written at all",
			outcome: OutcomeCapabilityUnlinkFailed,
			// A file inside a directory the session may not write:
			// both descriptors open, and the name cannot be removed.
			mktemp: func(dir string) string {
				return "d=" + dir + "/sticky\nf=$d/keep.tmp\nprintf '%s\\n' \"$f\"\n"
			},
			after: func(t *testing.T, dir string) {
				p := filepath.Join(dir, "sticky", "keep.tmp")
				b, err := os.ReadFile(p) // #nosec G304 — fixture
				if err != nil {
					t.Fatalf("the file the unlink could not remove is gone: %v", err)
				}
				if len(b) != 0 {
					t.Errorf("a failed unlink still wrote %d bytes; §5.2 case 4 says nothing is written at all", len(b))
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := stageOpts()
			var dir string
			s := startStage(t, ShellAuto, opts, func(home, tmp, path string) {
				dir = tmp
				if tc.name == "the unlink fails, so nothing is written at all" {
					sticky := filepath.Join(tmp, "sticky")
					if err := os.Mkdir(sticky, 0o700); err != nil {
						t.Fatalf("mkdir sticky: %v", err)
					}
					// #nosec G306 — fixture
					if err := os.WriteFile(filepath.Join(sticky, "keep.tmp"), nil, 0o600); err != nil {
						t.Fatalf("seed keep.tmp: %v", err)
					}
					// #nosec G302 — a DIRECTORY, and the mode is the
					// case: 0500 is readable and searchable but not
					// writable, which is what makes the unlink inside
					// it fail. G302 reasons about file bits.
					if err := os.Chmod(sticky, 0o500); err != nil {
						t.Fatalf("chmod sticky: %v", err)
					}
					// #nosec G302 — the same directory, made writable
					// again so the test's own cleanup can remove it.
					t.Cleanup(func() { _ = os.Chmod(sticky, 0o700) })
				}
				fakeMktemp(t, path, tmp, tc.mktemp(tmp))
			})
			secret, err := SecretFrame(opts)
			if err != nil {
				t.Fatalf("SecretFrame: %v", err)
			}
			s.sendFrame(FrameSecretSeq, secret)
			s.assertRefused(tc.outcome)
			if tc.after != nil {
				tc.after(t, dir)
			}
		})
	}
}

// TestStage1_NoLaunchCarrierOnTheFarSideNamesItAndLeavesAPrompt: there is
// nothing to exec, so the bootstrap says so instead of exec'ing something it
// has not proved.
func TestStage1_NoLaunchCarrierOnTheFarSideNamesItAndLeavesAPrompt(t *testing.T) {
	opts := stageOpts()
	s := startStage(t, ShellAuto, opts, func(home, tmp, path string) {
		if err := os.Remove(filepath.Join(home, dirName, launchName)); err != nil {
			t.Fatalf("remove fake launch: %v", err)
		}
	})
	secret, err := SecretFrame(opts)
	if err != nil {
		t.Fatalf("SecretFrame: %v", err)
	}
	s.sendFrame(FrameSecretSeq, secret)
	s.assertRefused(OutcomeGenerationUnavailable)
}

// ---------------------------------------------------------------------------
// Termios and cleanup on every catchable exit path (design §11 assertion 17)
// ---------------------------------------------------------------------------

// TestStage1_EachCatchableSignalRestoresTheExactTerminal: a signal arriving
// while stage-1 waits for its frame reaches the trap, which releases stage-1's
// own resources and then the loader's R — the sole owner of the restore.
func TestStage1_EachCatchableSignalRestoresTheExactTerminal(t *testing.T) {
	for name, sig := range map[string]syscall.Signal{
		"HUP":  syscall.SIGHUP,
		"INT":  syscall.SIGINT,
		"QUIT": syscall.SIGQUIT,
		"TERM": syscall.SIGTERM,
	} {
		t.Run(name, func(t *testing.T) {
			s := startStage(t, ShellAuto, stageOpts(), nil)
			s.signalGroup(sig)
			s.waitFor(OutcomePrefix + OutcomeToken(OutcomeBootstrapInterrupted))
			s.waitFor("TERMIOS_POST=")
			s.assertTermiosRestored("TERMIOS_POST=")
			s.assertNativeShellIsUsable()
			s.assertNoSecretOnDisk()
		})
	}
}

// TestLoader_EachOwnFailurePathRestoresTheExactTerminal covers the loader's
// half of the same assertion — the paths that end before control ever reaches
// stage-1.
func TestLoader_EachOwnFailurePathRestoresTheExactTerminal(t *testing.T) {
	stage, err := Stage1Frame(ShellAuto, stageOpts())
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}

	cases := []struct {
		name    string
		outcome Outcome
		// tools is the controlled PATH for this case.
		tools []string
		// mutate runs before the loader starts.
		mutate func(t *testing.T, path, tmp string)
		// drive sends whatever the case needs after LOADER_READY. Nil means
		// the refusal happens BEFORE the loader announces itself, and the
		// case asserts that it never announced itself at all.
		drive func(s *stageSession)
	}{
		{
			name:    "no hasher on the far side",
			outcome: OutcomeStageDigestUnavailable,
			tools:   loaderBaseTools,
			drive:   func(s *stageSession) { s.sendFrame(FrameStageSeq, stage) },
		},
		{
			name:    "a digest that is not the one the command carried",
			outcome: OutcomeStageDigestMismatch,
			tools:   stageTools,
			drive: func(s *stageSession) {
				s.sendFrame(FrameStageSeq, append(append([]byte(nil), stage...), []byte("\n:\n")...))
			},
		},
		{
			// The refusal comes BEFORE READY, and that ordering is the
			// fix rather than a detail of it. The loader used to
			// announce itself and then discover it had nowhere to put
			// the frame — by which time the writer had already sent
			// stage-1, and the native login shell the refusal execs
			// read those 1.6 KiB as TYPED COMMANDS. Measured under
			// dash: the shell announced STAGE_READY on the user's
			// terminal, ran stage-1's own dd and swallowed the user's
			// next typed line. Creating the temp file first means the
			// writer is still waiting for permission to send, so it
			// sends nothing at all.
			name:    "no secure temp, refused before it says it is ready",
			outcome: OutcomeNoSecureTemp,
			tools:   stageTools,
			mutate:  func(t *testing.T, path, tmp string) { fakeMktempFrom(t, path, tmp, 1, "exit 1\n") },
			drive:   nil,
		},
		{
			name:    "a signal before control reaches stage-1",
			outcome: OutcomeBootstrapInterrupted,
			tools:   stageTools,
			drive:   func(s *stageSession) { s.signalGroup(syscall.SIGINT) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := loaderHome(t)
			tmp := t.TempDir()
			path := loaderPath(t, tc.tools...)
			installFakeLaunch(t, home)
			if tc.mutate != nil {
				tc.mutate(t, path, tmp)
			}
			opts := stageOpts()
			opts.StageDigest = StageDigest(stage)
			cmd, reason, ok := NewRemoteLauncher().StartCommand(ShellAuto, opts)
			if !ok {
				t.Fatalf("carrier refused: %q", reason)
			}
			ls := startLoader(t, `printf "TERMIOS_PRE=%s\n" "$(stty -g)"; exec `+cmd,
				loaderEnv(home, path, tmp), stdoutOnTerminal)
			s := &stageSession{loaderSession: ls, home: home, tmp: tmp}
			s.waitFor("TERMIOS_PRE=")
			s.pre = s.capture("TERMIOS_PRE=")
			if tc.drive != nil {
				s.waitFor(LoaderReadyToken)
				tc.drive(s)
			}

			s.waitFor(OutcomePrefix + OutcomeToken(tc.outcome))
			if tc.drive == nil && strings.Contains(s.output(), LoaderReadyToken) {
				t.Errorf("the loader said it was ready and only then refused; a writer "+
					"that believed it would have put a frame into the native shell's "+
					"input. Output:\n%s", s.output())
			}
			s.waitFor("TERMIOS_POST=")
			s.assertTermiosRestored("TERMIOS_POST=")
			s.assertNativeShellIsUsable()
			s.assertStageNeverRan(tc.outcome)
		})
	}
}

// TestStage1_CleanupRunTwiceBehavesAsCleanupRunOnce drives stage-1's own
// cleanup function directly, twice, with the loader's R replaced by a
// recorder.
//
// It has to be driven rather than observed, because in a real session cleanup
// CANNOT run twice: R's last act is an exec, so the process that would run it
// again no longer exists. That is the strongest form of the property and the
// reason the shell is written that way — but "cannot happen" is not the same
// as "is harmless if it does", and this asserts the second.
func TestStage1_CleanupRunTwiceBehavesAsCleanupRunOnce(t *testing.T) {
	stage, err := Stage1Frame(ShellAuto, stageOpts())
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	body := string(stage)
	start := strings.Index(body, "Q(){")
	if start < 0 {
		t.Fatal("stage-1 has no Q definition to extract")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("stage-1's Q definition is not the shape this test extracts")
	}
	qdef := body[start : start+end+3]

	tmp := t.TempDir()
	script := "R(){ printf 'R:%s\\n' \"$1\"; }\n" +
		qdef + "\n" +
		"G=$(mktemp " + tmp + "/nocx.XXXXXX)\n" +
		"exec " + strconv.Itoa(CapabilityFD) + "<\"$G\"\n" +
		"exec " + strconv.Itoa(capabilityWriteFD) + ">\"$G\"\n" +
		"C=1\nW=1\n" +
		"Q one\n" +
		"Q two\n" +
		"[ -e \"$G\" ] && printf 'LEFTOVER\\n'\n" +
		"printf 'FILES=[%s]\\n' \"$(ls " + tmp + ")\"\n" +
		"printf 'DONE\\n'\n"

	out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput() // #nosec G204 — text built from package constants
	if err != nil {
		t.Fatalf("run cleanup twice: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "R:one") || !strings.Contains(got, "R:two") {
		t.Errorf("cleanup did not reach the loader's exit both times:\n%s", got)
	}
	if strings.Contains(got, "LEFTOVER") {
		t.Errorf("the temp file survived cleanup:\n%s", got)
	}
	if !strings.Contains(got, "FILES=[]") {
		t.Errorf("cleanup left something in the temp root:\n%s", got)
	}
	// The second run must be as quiet as the first: a "bad file descriptor"
	// on a re-closed descriptor is exactly the noise a user would see.
	if strings.Contains(strings.ToLower(got), "bad file descriptor") ||
		strings.Contains(strings.ToLower(got), "no such file") {
		t.Errorf("the second cleanup complained:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// The writer and the loader, meeting
// ---------------------------------------------------------------------------

// loaderStream is a BootstrapStream over the terminal a loaderSession drives,
// so the REAL writer can be pointed at the REAL loader with nothing faked
// between them. Reads are served from the session's own capture buffer — the
// harness's pump already owns the master — and writes are counted, which is
// what lets a test assert that the writer sent NOTHING.
type loaderStream struct {
	s      *loaderSession
	off    int
	writes int
	sent   []byte
}

func (l *loaderStream) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.After(30 * time.Second) // failsafe, never the measurement
	for {
		out := l.s.output()
		if l.off <= len(out) {
			if i := strings.IndexByte(out[l.off:], '\n'); i >= 0 {
				line := out[l.off : l.off+i]
				l.off += i + 1
				return strings.TrimRight(line, "\r"), nil
			}
		}
		select {
		case _, open := <-l.s.updated:
			if !open {
				return "", io.EOF
			}
		case <-deadline:
			return "", ErrBootstrapDeadline
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (l *loaderStream) Write(p []byte) (int, error) {
	l.writes++
	l.sent = append(l.sent, p...)
	l.s.write(p)
	return len(p), nil
}

// TestBootstrap_ARefusalBeforeReadyPutsNoFrameIntoTheNativeShell is the
// regression test for the defect the containerized (dash) run found, and it is
// deliberately written at the seam where the defect lived: the real writer
// driving the real loader over a real terminal.
//
// The far host cannot make a secure temp file. What must NOT happen is what
// used to: the loader announces READY, the writer sends 1.6 KiB of stage-1 in
// one write, the loader then discovers it has nowhere to put it and execs a
// native login shell — which reads the frame as typed commands, runs stage-1's
// own dd, and swallows the user's next line. The user is left with a shell
// that ate their keystroke and printed three parse errors.
func TestBootstrap_ARefusalBeforeReadyPutsNoFrameIntoTheNativeShell(t *testing.T) {
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, stageTools...)
	installFakeLaunch(t, home)
	fakeMktempFrom(t, path, tmp, 1, "exit 1\n")

	stage, err := Stage1Frame(ShellAuto, stageOpts())
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	opts := stageOpts()
	opts.StageDigest = StageDigest(stage)
	cmd, reason, ok := NewRemoteLauncher().StartCommand(ShellAuto, opts)
	if !ok {
		t.Fatalf("carrier refused: %q", reason)
	}

	ls := startLoader(t, cmd, loaderEnv(home, path, tmp), stdoutOnTerminal)
	stream := &loaderStream{s: ls}
	lg, _ := captureLog()
	minted := 0
	outcome := DeliverBootstrap(context.Background(), lg, stream,
		testPlan(t, opts, &minted))

	if outcome != OutcomeNoSecureTemp {
		t.Errorf("outcome %q, want %q", outcome, OutcomeNoSecureTemp)
	}
	// The whole point: not one byte was written, so there is nothing queued
	// for the shell the refusal execs.
	if stream.writes != 0 {
		t.Errorf("the writer sent %d write(s) (%d bytes) to a loader that had already "+
			"refused; every one of them becomes typed input to the native shell",
			stream.writes, len(stream.sent))
	}
	if minted != 0 {
		t.Errorf("a capability was minted for a refused bootstrap")
	}

	// And the shell the user is left with is genuinely usable: it answers a
	// typed command, which is the assertion that failed under dash.
	s := &stageSession{loaderSession: ls, home: home, tmp: tmp}
	s.assertNativeShellIsUsable()
	s.assertNoSecretOnDisk()
}

// acceptingLaunchScript is the launch stand-in for the full-loop test below.
// It mirrors the ONE thing the real launch carrier does that the plain
// fakeLaunchScript does not: emit the terminal outcome, gated on the bootstrap
// marker exactly as __nocx_outcome is (launch.go). Without that the writer
// would sit in awaitOutcome until its deadline, and a test that waits out a
// duration is a test that measures the machine.
const acceptingLaunchScript = `#!/bin/sh
if [ -n "${NOCX_BOOTSTRAP:-}" ]; then printf '@OUTPFX@@ACCEPTED@\n'; fi
unset NOCX_BOOTSTRAP
printf 'LAUNCH_DONE\n'
exec /bin/sh -i
`

// installAcceptingLaunch writes that stand-in into a fixture home.
func installAcceptingLaunch(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := strings.NewReplacer(
		"@OUTPFX@", OutcomePrefix,
		"@ACCEPTED@", OutcomeToken(OutcomeBootstrapAccepted),
	).Replace(acceptingLaunchScript)
	// #nosec G306 — test fixture, and the real carrier is 0700 too.
	if err := os.WriteFile(filepath.Join(dir, launchName), []byte(body), 0o700); err != nil {
		t.Fatalf("write accepting launch: %v", err)
	}
}

// ddNoise is what dd(1) writes to stderr after every invocation. The three
// lines are matched by their stable fragments rather than in full, because the
// byte count and the rate differ per read and per host.
var ddNoise = []string{"records in", "records out", "bytes copied"}

// TestBootstrap_NoDDBookkeepingReachesTheUsersTerminal is the regression test
// for the defect this file's own reads had: stage-1 reads its two frames with
// `dd bs=1 count=N`, and dd(1) writes a three-line summary to stderr on every
// invocation. On the far side stderr is the user's terminal, so a bootstrap
// that worked perfectly still put six lines of bookkeeping over the user's
// prompt — and this side logged every one of them as an unexpected line.
//
// It is written at the seam where both halves are observable at once: the real
// writer driving the real loader over a real terminal, so the assertion is
// about what the USER sees and what the PRODUCT logs, not about the text of a
// template. The loader was already silent here (carrier.go's `exec
// 2>/dev/null`); this asserts stage-1 is too.
func TestBootstrap_NoDDBookkeepingReachesTheUsersTerminal(t *testing.T) {
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, stageTools...)
	installAcceptingLaunch(t, home)

	opts := stageOpts()
	stage, err := Stage1Frame(ShellAuto, opts)
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	opts.StageDigest = StageDigest(stage)
	cmd, reason, ok := NewRemoteLauncher().StartCommand(ShellAuto, opts)
	if !ok {
		t.Fatalf("carrier refused: %q", reason)
	}

	ls := startLoader(t, cmd, loaderEnv(home, path, tmp), stdoutOnTerminal)
	stream := &loaderStream{s: ls}
	lg, logs := captureLog()
	minted := 0
	outcome := DeliverBootstrap(context.Background(), lg, stream, testPlan(t, opts, &minted))
	if outcome != OutcomeBootstrapAccepted {
		t.Fatalf("outcome %q, want %q; terminal:\n%s", outcome, OutcomeBootstrapAccepted, ls.output())
	}

	// What the user sees. The frame text itself is echoed nowhere — the
	// loader turns echo off — so any dd summary here came from a dd the far
	// side ran with stderr still on the terminal.
	term := ls.output()
	for _, frag := range ddNoise {
		if strings.Contains(term, frag) {
			t.Errorf("dd(1) bookkeeping (%q) reached the user's terminal; terminal:\n%s", frag, term)
		}
	}
	// And what the product logs. This is the same fact from the other side:
	// the backend catches those lines while it waits for the outcome and
	// records each one, which is the shape the defect was found by.
	if got := logs.String(); strings.Contains(got, "unexpected line while awaiting the outcome") {
		t.Errorf("the backend logged unexpected lines during a clean bootstrap; log:\n%s", got)
	}
	// Logged rather than only asserted, so `go test -v -run` on this one test
	// is the evidence itself: a clean bootstrap is four lines end to end.
	t.Logf("the user's terminal, whole:\n%s", term)
	t.Logf("the product's log, whole:\n%s", logs.String())
}
