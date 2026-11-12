package shellintegration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

// TestBashChannel_PrimaryRepaintWithDirenvHook distinguishes shell lifecycle
// events from a foreground program repainting the primary screen (nocx-peg8e.1.1).
// NOCX_TEST_BASH53 selects an externally built Bash 5.3 and must not silently
// skip a missing or wrong-version binary when explicitly set.
func TestBashChannel_PrimaryRepaintWithDirenvHook(t *testing.T) {
	bash53, explicit := os.LookupEnv("NOCX_TEST_BASH53")
	for _, variant := range []struct {
		name, path, version string
		required            bool
	}{
		{"bash5.2", "/bin/bash", "5.2", false},
		{"bash5.3", bash53, "5.3", explicit},
	} {
		t.Run(variant.name, func(t *testing.T) {
			if variant.version == "5.3" && !explicit {
				t.Skip("set NOCX_TEST_BASH53 to run the external Bash 5.3 diagnostic")
			}
			resolved, version, ok := resolveBash(variant.path)
			if !ok || version != variant.version {
				if variant.required {
					t.Fatalf("explicit Bash fixture %q: need GNU bash %s, resolved=%q version=%q", variant.path, variant.version, resolved, version)
				}
				t.Skipf("diagnostic requires GNU bash %s at %q; set NOCX_TEST_BASH53 for the external 5.3 build", variant.version, variant.path)
			}
			// #nosec G204 — resolved is the validated diagnostic Bash fixture.
			versionOutput, err := exec.Command(resolved, "--version").Output()
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("shell=%s version=%s", resolved, strings.SplitN(string(versionOutput), "\n", 2)[0])
			t.Run("synthetic-direnv", func(t *testing.T) {
				testBashPrimaryRepaint(t, resolved, "")
			})
			if direnv := os.Getenv("NOCX_TEST_DIRENV"); direnv != "" {
				if _, err := os.Stat(direnv); err != nil {
					t.Fatalf("explicit direnv fixture: %v", err)
				}
				t.Run("real-direnv", func(t *testing.T) {
					testBashPrimaryRepaint(t, resolved, direnv)
				})
			}
		})
	}
}

func testBashPrimaryRepaint(t *testing.T, shell, direnv string) {
	t.Helper()
	bin := t.TempDir()
	pidFile := filepath.Join(bin, "top.pid")
	// This is the shape emitted by direnv 2.38.2's `hook bash`, including
	// both command substitutions, the global vars and SIGINT restoration.
	// Only the external export command is replaced by an empty export result:
	// the test needs the hook, not a developer's .envrc or credentials.
	// https://github.com/direnv/direnv/blob/v2.38.2/internal/cmd/shell_bash.go
	hook := `
_direnv_hook() {
  local previous_exit_status=$?;
  local previous_sigint_trap="$(trap -p SIGINT)";
  vars="$(printf '')";
  trap -- '' SIGINT;
  eval "$vars";
  eval "${previous_sigint_trap:-trap - SIGINT}";
  return $previous_exit_status;
}
PROMPT_COMMAND=_direnv_hook
`
	if direnv != "" {
		hook = "eval \"$(" + ShellQuote(direnv) + " hook bash)\"\n"
	}
	// Each r is an observable barrier: the foreground process has repainted
	// and is back waiting for input, without exiting or switching buffers.
	top := "#!/bin/sh\n" +
		"printf '%s' \"$$\" > " + ShellQuote(pidFile) + "\n" +
		"printf 'PRIMARY-TOP-READY\\n'\n" +
		"n=0\nwhile IFS= read -r key; do\n" +
		"case \"$key\" in\n" +
		"r) n=$((n + 1)); printf '\\033[H\\033[KPRIMARY-TOP-REPAINT-%s\\n' \"$n\" ;;\n" +
		"q) exit 0 ;;\nesac\ndone\n"
	// #nosec G306 — the fixture must be executable and lives in t.TempDir.
	if err := os.WriteFile(filepath.Join(bin, "top"), []byte(top), 0o755); err != nil {
		t.Fatal(err)
	}
	hook += "PATH=" + ShellQuote(bin) + ":$PATH\n"
	s := startChannelShell(t, shell, "nocx.bash", hook+bashScript)
	var childPID int
	defer func() {
		// Also release a parked child after a failed assertion, so cleanup
		// cannot accidentally feed `exit` to the foreground program.
		if childPID != 0 {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
		s.close()
		logPrimaryRepaint(t, "cleanup", s)
	}()
	// Anchor the initial prompt on both transports before sampling counts.
	waittest.WaitForTimeout(t, "initial rendered prompt", 10*time.Second, func() bool {
		return strings.Contains(s.output(), "\x1b]133;B")
	})
	startsBefore := s.kernel.count("start")
	completesBefore := s.kernel.count("complete")
	readyBefore := s.kernel.count("prompt_ready")
	outBefore := len(s.output())
	if _, err := s.ptmx.Write([]byte("top\n")); err != nil {
		t.Fatal(err)
	}
	waittest.WaitForTimeout(t, "top started and parked", 10*time.Second, func() bool {
		return strings.Contains(s.output()[outBefore:], "PRIMARY-TOP-READY") && s.kernel.count("start") > startsBefore
	})
	pidBytes, err := os.ReadFile(pidFile) // #nosec G304 -- pidFile is inside this test's private TempDir.
	if err != nil {
		t.Fatal(err)
	}
	childPID, err = strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	for repaint := 1; repaint <= 3; repaint++ {
		if _, err := s.ptmx.Write([]byte("r\n")); err != nil {
			t.Fatal(err)
		}
		marker := fmt.Sprintf("\x1b[H\x1b[KPRIMARY-TOP-REPAINT-%d", repaint)
		waittest.WaitForTimeout(t, "foreground repaint acknowledgement", 10*time.Second, func() bool {
			return strings.Contains(s.output()[outBefore:], marker)
		})
		logPrimaryRepaint(t, fmt.Sprintf("repaint-%d-parked", repaint), s)
		if err := syscall.Kill(childPID, 0); err != nil {
			t.Fatalf("top must still be alive after repaint %d: %v", repaint, err)
		}
		if got := s.kernel.count("complete") - completesBefore; got != 0 {
			t.Errorf("foreground top is alive but shell emitted %d completions", got)
		}
		if got := s.kernel.count("start") - startsBefore; got != 1 {
			t.Errorf("foreground top must have one start, got %d", got)
		}
		if got := s.kernel.count("prompt_ready") - readyBefore; got != 0 {
			t.Errorf("foreground top is alive but shell emitted %d ready prompts", got)
		}
	}
	var attempt any
	for _, event := range s.kernel.events() {
		if event.Evt == "start" {
			attempt = event.Body["attempt"]
			if command, _ := event.Body["command"].(string); command != "top" {
				t.Errorf("start must carry user's top command, got %q", command)
			}
		}
	}
	if _, err := s.ptmx.Write([]byte("q\n")); err != nil {
		t.Fatal(err)
	}
	waittest.WaitForTimeout(t, "top completion after quit", 10*time.Second, func() bool {
		return s.kernel.count("complete") > completesBefore
	})
	s.waitForPromptAfterComplete()
	s.waitForRenderedThroughPrompt()
	childPID = 0 // The foreground process has exited and the shell reaped it.
	logPrimaryRepaint(t, "after-quit", s)
	if got := s.kernel.count("start") - startsBefore; got != 1 {
		t.Errorf("one invocation of top must produce one start, got %d", got)
	}
	if got := s.kernel.count("complete") - completesBefore; got != 1 {
		t.Errorf("one invocation of top must complete only after quit, got %d completions", got)
	}
	for _, event := range s.kernel.events() {
		if event.Evt == "complete" && (event.Body["attempt"] != attempt || event.Body["exit_code"] != float64(0)) {
			t.Errorf("completion must close top's attempt with exit 0, got attempt=%v exit=%v", event.Body["attempt"], event.Body["exit_code"])
		}
	}
	if out := s.output()[outBefore:]; strings.Contains(out, "\x1b[?1049h") || strings.Contains(out, "\x1b[?47h") {
		t.Error("fixture unexpectedly entered the alternate screen")
	}
}

func logPrimaryRepaint(t *testing.T, phase string, s *channelShell) {
	t.Helper()
	// Log every envelope field except the bearer and render fence. Never dump
	// the PTY: OSC 636 carries a snapshot nonce unrelated to this diagnosis.
	for _, event := range s.kernel.events() {
		body := make(map[string]any, len(event.Body))
		for key, value := range event.Body {
			if key == "cap" || key == "fence" {
				body[key] = "[redacted]"
			} else {
				body[key] = value
			}
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s envelope=%s", phase, encoded)
	}
	out := s.output()
	markers := regexp.MustCompile(`\x1b\]133;[^\x07\x1b]*(?:\x07|\x1b\\)`).FindAllString(out, -1)
	t.Logf("%s OSC133 sequence=%q", phase, markers)
	t.Logf("%s OSC133 A=%d B=%d C=%d D=%d; rejected=%d", phase,
		strings.Count(out, "\x1b]133;A"), strings.Count(out, "\x1b]133;B"),
		strings.Count(out, "\x1b]133;C"), strings.Count(out, "\x1b]133;D"), s.kernel.rejectedCount())
}
