package shellintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func testLogger() log.Logger {
	return log.NewSlogAdapter(nil)
}

func TestValidateCwd_Localhost(t *testing.T) {
	s := New(testLogger())

	tests := []struct {
		name    string
		host    string
		path    string
		wantErr bool
	}{
		{"empty host, root path", "", "/", false},
		{"empty host, home path", "", "/home/user", false},
		{"localhost host", "localhost", "/tmp", false},
		{"local hostname", osHostname(t), "/var/log", false},
		{"tilde path (home)", "", "~", false},
		{"non-local host", "remote.example.com", "/tmp", true},
		{"empty path", "", "", true},
		{"relative path", "", "documents", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := s.ValidateCwd(tt.host, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got %+v", info)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if info.Host != tt.host {
				t.Errorf("host: want %q, got %q", tt.host, info.Host)
			}
			if info.Path != tt.path {
				t.Errorf("path: want %q, got %q", tt.path, info.Path)
			}
		})
	}
}

func TestValidateCwd_CustomHostFunc(t *testing.T) {
	s := &Impl{
		log:    testLogger(),
		isHost: func(host string) bool { return host == "custombox" },
	}

	_, err := s.ValidateCwd("custombox", "/etc")
	if err != nil {
		t.Errorf("custom host should pass: %v", err)
	}

	_, err = s.ValidateCwd("localhost", "/etc")
	if err == nil {
		t.Error("localhost should fail with custom host func")
	}
}

func TestActivationEnv(t *testing.T) {
	s := New(testLogger())
	env := s.ActivationEnv(false)

	if len(env) != 1 || env[0] != "NOCX_SHELL_INTEGRATION=1" {
		t.Fatalf("ActivationEnv(false) = %v, want [NOCX_SHELL_INTEGRATION=1]", env)
	}
}

func TestActivationEnvEnhanced(t *testing.T) {
	s := New(testLogger())
	enh := s.ActivationEnv(true)

	joined := strings.Join(enh, "\n")
	if !strings.Contains(joined, "NOCX_PROMPT_MODE=marker-only") {
		t.Errorf("enhanced env missing NOCX_PROMPT_MODE: %v", enh)
	}
	var sid string
	for _, e := range enh {
		if strings.HasPrefix(e, "NOCX_SESSION_ID=") {
			sid = strings.TrimPrefix(e, "NOCX_SESSION_ID=")
		}
	}
	if sid == "" {
		t.Errorf("enhanced env missing non-empty NOCX_SESSION_ID: %v", enh)
	}
}

func TestEnsureInstalled_WritesScriptsAndGates(t *testing.T) {
	home := t.TempDir()
	s := New(testLogger())

	if err := s.EnsureInstalled(home); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	dir := filepath.Join(home, dirName)

	// Check VERSION file.
	vf := filepath.Join(dir, versionFile)
	// #nosec G304 — test-only path built from t.TempDir + fixed constants.
	data, err := os.ReadFile(vf)
	if err != nil {
		t.Fatalf("VERSION file not found: %v", err)
	}
	if strings.TrimSpace(string(data)) != version {
		t.Errorf("VERSION: want %q, got %q", version, strings.TrimSpace(string(data)))
	}

	// Check scripts exist.
	for name := range scripts {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("script %s not found: %v", name, err)
		}
	}

	// Check gate lines in rc files.
	for rcFile, gate := range rcGate {
		rcPath := filepath.Join(home, rcFile)
		// #nosec G304 — test-only path built from t.TempDir + fixed rc filename constants.
		data, err := os.ReadFile(rcPath)
		if err != nil {
			t.Errorf("rc file %s not found: %v", rcFile, err)
			continue
		}
		if !strings.Contains(string(data), gate) {
			t.Errorf("rc file %s missing gate line", rcFile)
		}
	}
}

func TestEnsureInstalled_Idempotent(t *testing.T) {
	home := t.TempDir()
	s := New(testLogger())

	// First install.
	if err := s.EnsureInstalled(home); err != nil {
		t.Fatalf("first EnsureInstalled: %v", err)
	}

	// Read gate line count before second install.
	for rcFile := range rcGate {
		rcPath := filepath.Join(home, rcFile)
		// #nosec G304 — test-only path built from t.TempDir + fixed rc filename constants.
		data, err := os.ReadFile(rcPath)
		if err != nil {
			t.Fatalf("read rc %s: %v", rcFile, err)
		}
		firstCount := strings.Count(string(data), "# nocx terminal shell integration")

		// Second install should not duplicate.
		if installErr := s.EnsureInstalled(home); installErr != nil {
			t.Fatalf("second EnsureInstalled: %v", installErr)
		}

		// #nosec G304 — test-only path built from t.TempDir + fixed rc filename constants.
		data2, err := os.ReadFile(rcPath)
		if err != nil {
			t.Fatalf("read rc %s after second install: %v", rcFile, err)
		}
		secondCount := strings.Count(string(data2), "# nocx terminal shell integration")
		if secondCount != firstCount {
			t.Errorf("rc %s gate duplicated: first=%d, second=%d", rcFile, firstCount, secondCount)
		}
	}
}

func TestEnsureInstalled_EmptyHome(t *testing.T) {
	s := New(testLogger())
	if err := s.EnsureInstalled(""); err == nil {
		t.Error("expected error for empty home")
	}
}

func TestEnsureInstalled_PreservesExistingRcContent(t *testing.T) {
	home := t.TempDir()
	s := New(testLogger())

	// Write existing rc content.
	for rcFile := range rcGate {
		rcPath := filepath.Join(home, rcFile)
		// #nosec G306 — test fixture file, intentionally created with restricted permissions.
		if err := os.WriteFile(rcPath, []byte("# existing config\n"), 0o600); err != nil {
			t.Fatalf("write rc: %v", err)
		}
	}

	if err := s.EnsureInstalled(home); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	for rcFile := range rcGate {
		rcPath := filepath.Join(home, rcFile)
		// #nosec G304 — test-only path built from t.TempDir + fixed rc filename constants.
		data, err := os.ReadFile(rcPath)
		if err != nil {
			t.Fatalf("read rc: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "# existing config") {
			t.Errorf("rc %s lost existing content", rcFile)
		}
	}
}

// TestEnsureInstalled_RewritesOldVersion guards nocx-6b3x: an existing
// install whose VERSION marker is stale must be rewritten — including the
// scripts that the old version never shipped. Seeded with the real previous
// version ("9") and an install that lacks the posix script entirely, the
// way every installed ~/.nocx looked before nocx-518d.
func TestEnsureInstalled_RewritesOldVersion(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, dirName)
	// #nosec G306 — test fixture directory, intentionally created with restricted permissions.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// #nosec G306 — test fixture file, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(dir, versionFile), []byte("9\n"), 0o600); err != nil {
		t.Fatalf("write stale VERSION: %v", err)
	}
	// #nosec G306 — test fixture file, intentionally created with restricted permissions.
	if err := os.WriteFile(filepath.Join(dir, "shell-integration.bash"), []byte("stale bash script\n"), 0o600); err != nil {
		t.Fatalf("write stale script: %v", err)
	}

	s := New(testLogger())
	if err := s.EnsureInstalled(home); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}

	// #nosec G304 — test-only path built from t.TempDir + fixed constants.
	vf, err := os.ReadFile(filepath.Join(dir, versionFile))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	if strings.TrimSpace(string(vf)) != version {
		t.Errorf("VERSION = %q, want %q (stale install must be rewritten)", strings.TrimSpace(string(vf)), version)
	}

	for name := range scripts {
		// #nosec G304 — test-only path built from t.TempDir + fixed constants.
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("script %s missing after rewrite: %v", name, err)
			continue
		}
		if string(data) == "stale bash script\n" {
			t.Errorf("script %s was not rewritten", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "shell-integration.posix")); err != nil {
		t.Errorf("shell-integration.posix missing after rewrite: %v", err)
	}
}

// scriptMarkers declares, per mapped script, the OSC 133 markers it must
// emit. bash and zsh emit all four from preexec hooks (PROMPT_COMMAND/
// DEBUG, precmd/preexec_functions). The posix script structurally cannot
// emit C: POSIX sh has no preexec hook, and the absence is deliberate —
// asserted at execution time by TestPosixIntegration_EmitsMarkersFromPS1.
// Its escapes are \033 rather than \e, so its expectation is its own. The
// expectation is per-script on purpose: adding a script to the map without
// deciding its markers fails the map test below, and a bash script that
// stops emitting C is a real defect, not a tolerated tier difference.
var scriptMarkers = map[string][]string{
	// bash/zsh emit A, C and D through the __nocx_marker helper (the literal
	// kind is chosen at the call site); B is the literal escape in PS1. The
	// call sites are the tripwire: a script that stops emitting C is a real
	// defect, not a tolerated tier difference.
	"shell-integration.bash":  {`__nocx_marker A`, `\e]133;B`, `__nocx_marker C`, `__nocx_marker D`},
	"shell-integration.zsh":   {`__nocx_marker A`, `\e]133;B`, `__nocx_marker C`, `__nocx_marker D`},
	"shell-integration.posix": {`\033]133;A`, `\033]133;B`, `\033]133;D`},
}

// missingScriptMarkers returns the declared markers for name that content
// lacks. A script with no declared expectation reports itself as missing
// its expectation — the tripwire that keeps every mapped script decided.
func missingScriptMarkers(name, content string) []string {
	want, ok := scriptMarkers[name]
	if !ok {
		return []string{"<no declared marker expectation — add one to scriptMarkers>"}
	}
	var missing []string
	for _, marker := range want {
		if !strings.Contains(content, marker) {
			missing = append(missing, marker)
		}
	}
	return missing
}

func TestScriptContent_ContainsMarkers(t *testing.T) {
	for name, content := range scripts {
		for _, marker := range missingScriptMarkers(name, content) {
			t.Errorf("script %s missing marker %q", name, marker)
		}
	}
}

func TestScriptMarkerExpectation_RejectsBashWithoutC(t *testing.T) {
	content := strings.ReplaceAll(bashScript, `__nocx_marker C`, "")
	found := false
	for _, m := range missingScriptMarkers("shell-integration.bash", content) {
		if m == `__nocx_marker C` {
			found = true
		}
	}
	if !found {
		t.Errorf("bash script without C not flagged as missing %q", `__nocx_marker C`)
	}
}

func TestScriptContent_GuardOnActivationEnv(t *testing.T) {
	for name, content := range scripts {
		if !strings.Contains(content, "NOCX_SHELL_INTEGRATION") {
			t.Errorf("script %s missing activation env guard", name)
		}
	}
}

func osHostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Skipf("cannot get hostname: %v", err)
	}
	return h
}

// errReader is an io.Reader that always returns an error.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, os.ErrInvalid }

// TestActivationEnvFailsClosedOnRandError verifies that when crypto/rand
// fails (e.g. on a low-entropy embedded system), ActivationEnv(true) omits
// the enhanced vars entirely instead of falling back to a predictable id
// (nocx-4ff.13).
func TestActivationEnvFailsClosedOnRandError(t *testing.T) {
	orig := randReader
	randReader = errReader{}
	defer func() { randReader = orig }()

	s := New(testLogger())
	enh := s.ActivationEnv(true)
	joined := strings.Join(enh, "\n")
	if strings.Contains(joined, "NOCX_PROMPT_MODE") || strings.Contains(joined, "NOCX_SESSION_ID") {
		t.Fatalf("enhanced env must be omitted when session id cannot be generated: %v", enh)
	}
	found := false
	for _, e := range enh {
		if strings.HasPrefix(e, "NOCX_SHELL_INTEGRATION=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("NOCX_SHELL_INTEGRATION must be present even on rand failure: %v", enh)
	}
}
