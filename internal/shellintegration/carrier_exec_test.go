package shellintegration

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The loader exercised AS SHELL, not as a string. This package already runs
// shell in tests (scripts_exec_test.go, launcher_publish_pty_test.go) and
// this file follows that: a real /bin/sh, a real terminal on its stdin, real
// external tools resolved through a PATH the test controls.
//
// Two differences from the older helpers, both deliberate:
//
//   - Nothing here waits for a DURATION. Every step waits for an output
//     marker the loader itself emits, so the suite cannot pass on a fast
//     machine and fail on a slow one (AGENTS.md, 2026-08-11).
//   - The child's stdout can be a PIPE while its stdin is the terminal.
//     The loader's terminal handling is all on stdin (`stty` reads fd 0),
//     and separating the two is what lets a test observe an outcome emitted
//     AFTER the input side has gone away — which is the only way to watch
//     the EOF-mid-frame path name its outcome.

// loaderSession drives one run of an emitted carrier command.
type loaderSession struct {
	t       *testing.T
	master  *os.File // the terminal's master side: what the backend writes to
	cmd     *exec.Cmd
	mu      sync.Mutex
	out     strings.Builder
	updated chan struct{}
	ended   chan struct{}
}

// loaderStdout selects the child's output topology.
type loaderStdout int

const (
	// stdoutOnTerminal is production's shape: stdout is the same terminal
	// as stdin.
	stdoutOnTerminal loaderStdout = iota
	// stdoutOnPipe keeps the child's output readable after the terminal's
	// master side has been closed.
	stdoutOnPipe
)

// startLoader runs cmd under /bin/sh with a real terminal on its stdin.
func startLoader(t *testing.T, cmd string, env []string, mode loaderStdout) *loaderSession {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })

	// #nosec G204 — cmd is a launcher string built from package constants
	// and env is test-owned; a terminal is the only way to observe the
	// loader's termios handling.
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = append(os.Environ(), env...)
	c.Stdin = slave
	var pipeR *os.File
	switch mode {
	case stdoutOnTerminal:
		c.Stdout, c.Stderr = slave, slave
	case stdoutOnPipe:
		r, w, perr := os.Pipe()
		if perr != nil {
			t.Fatalf("os.Pipe: %v", perr)
		}
		c.Stdout, c.Stderr = w, w
		pipeR = r
		t.Cleanup(func() { _ = w.Close() })
		defer func() { _ = w.Close() }()
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	if err := c.Start(); err != nil {
		_ = slave.Close()
		t.Fatalf("start loader: %v", err)
	}
	_ = slave.Close()

	s := &loaderSession{
		t:       t,
		master:  master,
		cmd:     c,
		updated: make(chan struct{}, 1),
		ended:   make(chan struct{}),
	}
	var src io.Reader = master
	if pipeR != nil {
		src = pipeR
	}
	go s.pump(src)
	go func() { _ = c.Wait(); close(s.ended) }()
	t.Cleanup(func() {
		if c.Process != nil {
			_ = c.Process.Kill()
		}
	})
	return s
}

// pump copies the child's output into the buffer, signalling every read so
// waitFor can re-check without polling on a timer.
func (s *loaderSession) pump(src io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.out.Write(buf[:n])
			s.mu.Unlock()
			select {
			case s.updated <- struct{}{}:
			default:
			}
		}
		if err != nil {
			close(s.updated)
			return
		}
	}
}

func (s *loaderSession) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// waitFor blocks until want appears in the child's output. The timeout is a
// failsafe against a hung test, never the thing being measured: a correct
// run returns as soon as the bytes arrive, however slow the machine.
func (s *loaderSession) waitFor(want string) {
	s.t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		if strings.Contains(s.output(), want) {
			return
		}
		select {
		case _, open := <-s.updated:
			if !open {
				if strings.Contains(s.output(), want) {
					return
				}
				s.t.Fatalf("the session ended without emitting %q; output:\n%s", want, s.output())
			}
		case <-deadline:
			s.t.Fatalf("timed out waiting for %q; output:\n%s", want, s.output())
		}
	}
}

func (s *loaderSession) write(b []byte) {
	s.t.Helper()
	if _, err := s.master.Write(b); err != nil {
		s.t.Fatalf("write to the terminal: %v", err)
	}
}

// sendFrame writes one frame exactly as the Go sender will: the header the
// package declares, then the payload bytes and nothing else.
func (s *loaderSession) sendFrame(seq int, payload []byte) {
	s.t.Helper()
	h, err := FrameHeader(seq, len(payload))
	if err != nil {
		s.t.Fatalf("FrameHeader: %v", err)
	}
	s.write([]byte(h))
	s.write(payload)
}

// assertNativeShellIsUsable proves the refusal left a WORKING native login
// shell: the fixture ~/.profile ran, and the shell answers a typed command.
func (s *loaderSession) assertNativeShellIsUsable() {
	s.t.Helper()
	s.waitFor(nativeLoginMarker)
	// The command and its ANSWER are deliberately different strings. The
	// terminal is in cooked mode with echo back on by now, so a probe whose
	// echoed input contains the string being waited for would be satisfied
	// by the echo — proving the terminal, not the shell.
	s.write([]byte("printf 'PROMPT%s\\n' _ALIVE\n"))
	s.waitFor("PROMPT_ALIVE")
}

const nativeLoginMarker = "NATIVE_LOGIN_RAN"

// loaderHome is a disposable $HOME whose ~/.profile fingerprints the native
// login shell — the refusal outcome nothing may suppress.
func loaderHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	// #nosec G306 — test fixture file.
	//
	// It also fingerprints the TERMINAL STATE the refusal left behind:
	// design §11 assertion 17 wants the restored termios to byte-equal the
	// saved one on every catchable exit path, and the native login shell's
	// own startup file is where every one of those paths ends up.
	if err := os.WriteFile(filepath.Join(home, ".profile"),
		[]byte("echo "+nativeLoginMarker+"\nprintf 'TERMIOS_POST=%s\\n' \"$(stty -g)\"\n"), 0o600); err != nil {
		t.Fatalf("write fixture .profile: %v", err)
	}
	return home
}

// loaderPath builds a PATH containing exactly the named tools, so a test can
// state "this host has sha256sum but not shasum" and mean it. A missing tool
// FAILS rather than skips: a hasher-selection test that silently does not run
// is the green-on-nothing failure the testing rules exist to prevent.
func loaderPath(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s is required by this test and missing from PATH (%v).\n"+
				"The loader's hasher-selection and temp-file paths must not silently skip.",
				tool, err)
		}
		if err := os.Symlink(src, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("symlink %s: %v", tool, err)
		}
	}
	return dir
}

// loaderBaseTools is what the loader itself needs on PATH, hashers aside.
// `ls` is not the loader's — the fake stage-1 uses it to report whether the
// temp name survived the unlink.
var loaderBaseTools = []string{"stty", "mktemp", "dd", "wc", "rm", "ls"}

// loaderEnv assembles the child's environment: the fixture home, the
// controlled PATH, a private TMPDIR, and SHELL naming the shell the refusal
// path must exec.
func loaderEnv(home, path string, tmp string) []string {
	return []string{
		"HOME=" + home,
		"PATH=" + path,
		"SHELL=/bin/sh",
		"TMPDIR=" + tmp,
		"TERM=xterm",
		"ENV=",
		"BASH_ENV=",
	}
}

// fakeStageOne is the stage-1 stand-in. It is NOT stage-1 (that is the next
// package): it proves only what the loader promises — that a verified frame
// is what gets sourced, that it inherits the addressing arguments, and that
// the temp name is already gone by the time it runs.
const fakeStageOne = `printf "STAGE1_RAN sid=%s lane=%s dom=%s epoch=%s port=%s fd=%s dig=%s\n" "$1" "$2" "$3" "$4" "$5" "$6" "$7"
if ls "${TMPDIR:-/tmp}"/nocx.* >/dev/null 2>&1; then printf "TEMP_PRESENT\n"; else printf "TEMP_GONE\n"; fi
exec /bin/sh -i
`

// carrierFor renders the command under test with a digest over payload.
func carrierFor(t *testing.T, payload []byte) string {
	t.Helper()
	opts := carrierOpts()
	opts.Capability = canaryCapability
	opts.Recovery = canaryRecovery
	opts.StageDigest = StageDigest(payload)
	cmd, reason, ok := NewRemoteLauncher().StartCommand(ShellAuto, opts)
	if !ok {
		t.Fatalf("carrier refused: %q", reason)
	}
	return cmd
}

// ---------------------------------------------------------------------------
// Paired hasher cases (design §11 assertion 4)
// ---------------------------------------------------------------------------

func TestLoader_SourcesVerifiedStageOne_WithSha256sum(t *testing.T) {
	payload := []byte(fakeStageOne)
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, append(append([]string{}, loaderBaseTools...), "sha256sum")...)

	s := startLoader(t, carrierFor(t, payload), loaderEnv(home, path, tmp), stdoutOnTerminal)
	s.waitFor(LoaderReadyToken)
	s.sendFrame(FrameStageSeq, payload)
	s.waitFor("STAGE1_RAN")
	s.waitFor("TEMP_GONE")

	out := s.output()
	if strings.Contains(out, nativeLoginMarker) {
		t.Errorf("a verified stage-1 must not degrade to a native login shell; output:\n%s", out)
	}
	if strings.Contains(out, OutcomePrefix) {
		t.Errorf("a verified stage-1 named a refusal outcome; output:\n%s", out)
	}
	// The addressing arguments reached stage-1 as positional parameters,
	// and the secrets did not travel at all.
	if !strings.Contains(out, "sid="+carrierOpts().SessionID) {
		t.Errorf("stage-1 did not inherit the session id; output:\n%s", out)
	}
	if !strings.Contains(out, "fd="+fmt.Sprint(StageFD)) {
		t.Errorf("stage-1 did not inherit the stage descriptor number; output:\n%s", out)
	}
	if strings.Contains(out, "CANARY") {
		t.Errorf("a secret canary reached the far side; output:\n%s", out)
	}
}

func TestLoader_SourcesVerifiedStageOne_WithOnlyShasum(t *testing.T) {
	payload := []byte(fakeStageOne)
	home := loaderHome(t)
	tmp := t.TempDir()
	tools := append(append([]string{}, loaderBaseTools...), "shasum")
	if _, err := exec.LookPath("perl"); err == nil {
		tools = append(tools, "perl")
	}
	path := loaderPath(t, tools...)

	s := startLoader(t, carrierFor(t, payload), loaderEnv(home, path, tmp), stdoutOnTerminal)
	s.waitFor(LoaderReadyToken)
	s.sendFrame(FrameStageSeq, payload)
	s.waitFor("STAGE1_RAN")

	if out := s.output(); strings.Contains(out, nativeLoginMarker) {
		t.Errorf("shasum -a 256 must be a working second hasher; output:\n%s", out)
	}
}

func TestLoader_NoHasher_NamesOutcomeAndLeavesNativeLoginShell(t *testing.T) {
	payload := []byte(fakeStageOne)
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, loaderBaseTools...)

	s := startLoader(t, carrierFor(t, payload), loaderEnv(home, path, tmp), stdoutOnTerminal)
	s.waitFor(LoaderReadyToken)
	s.sendFrame(FrameStageSeq, payload)
	s.waitFor(OutcomePrefix + OutcomeToken(OutcomeStageDigestUnavailable))
	s.assertNativeShellIsUsable()

	if out := s.output(); strings.Contains(out, "STAGE1_RAN") {
		t.Errorf("unverified stage-1 was executed; output:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Frame refusals (design §11 assertions 4 and 16)
// ---------------------------------------------------------------------------

func TestLoader_OverCapFrameRefusedBeforeItIsRead(t *testing.T) {
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, append(append([]string{}, loaderBaseTools...), "sha256sum")...)

	s := startLoader(t, carrierFor(t, []byte(fakeStageOne)), loaderEnv(home, path, tmp), stdoutOnTerminal)
	s.waitFor(LoaderReadyToken)
	// The header is written by hand: FrameHeader refuses to build this one,
	// which is the writer half of the same cap.
	s.write([]byte(fmt.Sprintf("%s %d %8d\n", FrameMagic, FrameStageSeq, MaxStageFrameLen+1)))
	s.waitFor(OutcomePrefix + OutcomeToken(OutcomeStageTooLarge))
	s.assertNativeShellIsUsable()

	if out := s.output(); strings.Contains(out, "STAGE1_RAN") {
		t.Errorf("an over-cap frame was executed; output:\n%s", out)
	}
}

func TestLoader_DigestMismatchRefused(t *testing.T) {
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, append(append([]string{}, loaderBaseTools...), "sha256sum")...)

	// The command commits to the digest of the real payload; the frame
	// carries different bytes.
	cmd := carrierFor(t, []byte(fakeStageOne))
	s := startLoader(t, cmd, loaderEnv(home, path, tmp), stdoutOnTerminal)
	s.waitFor(LoaderReadyToken)
	s.sendFrame(FrameStageSeq, []byte("printf TAMPERED_STAGE_RAN\n"))
	s.waitFor(OutcomePrefix + OutcomeToken(OutcomeStageDigestMismatch))
	s.assertNativeShellIsUsable()

	if out := s.output(); strings.Contains(out, "TAMPERED_STAGE_RAN") {
		t.Errorf("a frame that failed its digest was executed; output:\n%s", out)
	}
}

func TestLoader_ProtocolViolationRefused(t *testing.T) {
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, append(append([]string{}, loaderBaseTools...), "sha256sum")...)

	s := startLoader(t, carrierFor(t, []byte(fakeStageOne)), loaderEnv(home, path, tmp), stdoutOnTerminal)
	s.waitFor(LoaderReadyToken)
	s.write([]byte(fmt.Sprintf("%-*s", FrameHeaderLen-1, "GARBAGE") + "\n"))
	s.waitFor(OutcomePrefix + OutcomeToken(OutcomeBootstrapProtocol))
	s.assertNativeShellIsUsable()
}

// TestLoader_EOFMidFrameNamesBootstrapInterrupted is the one case whose
// outcome is emitted after the input side is gone, so the child's output
// goes to a pipe (see the file comment). The loader has read a valid header
// and part of the body when the terminal's master side closes.
func TestLoader_EOFMidFrameNamesBootstrapInterrupted(t *testing.T) {
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, append(append([]string{}, loaderBaseTools...), "sha256sum")...)

	s := startLoader(t, carrierFor(t, []byte(fakeStageOne)), loaderEnv(home, path, tmp), stdoutOnPipe)
	s.waitFor(LoaderReadyToken)
	h, err := FrameHeader(FrameStageSeq, 4096)
	if err != nil {
		t.Fatal(err)
	}
	s.write([]byte(h))
	s.write([]byte("only-a-few-bytes"))
	_ = s.master.Close()

	s.waitFor(OutcomePrefix + OutcomeToken(OutcomeBootstrapInterrupted))
	s.waitFor(nativeLoginMarker)

	if out := s.output(); strings.Contains(out, "STAGE1_RAN") {
		t.Errorf("a truncated frame was executed; output:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Assertion 3 — the loader runs unconditionally
// ---------------------------------------------------------------------------

// TestLoader_RunsWithNothingInstalledOnTheFarSide is the defect this design
// replaces, made observable: with no ~/.nocx at all — the first-contact
// state, where the concurrent publish is still in flight — the command must
// still reach the frame read rather than short-circuit to a native shell.
func TestLoader_RunsWithNothingInstalledOnTheFarSide(t *testing.T) {
	payload := []byte(fakeStageOne)
	home := loaderHome(t)
	tmp := t.TempDir()
	path := loaderPath(t, append(append([]string{}, loaderBaseTools...), "sha256sum")...)

	if _, err := os.Stat(filepath.Join(home, dirName)); !os.IsNotExist(err) {
		t.Fatalf("the fixture home must have no %s: %v", dirName, err)
	}

	s := startLoader(t, carrierFor(t, payload), loaderEnv(home, path, tmp), stdoutOnTerminal)
	s.waitFor(LoaderReadyToken)
	s.sendFrame(FrameStageSeq, payload)
	s.waitFor("STAGE1_RAN")

	if out := s.output(); strings.Contains(out, nativeLoginMarker) {
		t.Errorf("an uninstalled far side degraded instead of bootstrapping; output:\n%s", out)
	}
}
