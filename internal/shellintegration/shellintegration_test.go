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

func TestRemoteStartCommand(t *testing.T) {
	s := New(testLogger())
	cmd := s.RemoteStartCommand()

	if !strings.Contains(cmd, "NOCX_SHELL_INTEGRATION=1") {
		t.Errorf("RemoteStartCommand missing activation env: %q", cmd)
	}
	if !strings.Contains(cmd, "exec") {
		t.Errorf("RemoteStartCommand missing exec: %q", cmd)
	}
	if !strings.Contains(cmd, `"${SHELL:-/bin/sh}"`) {
		t.Errorf("RemoteStartCommand should quote SHELL expansion: %q", cmd)
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

func TestScriptContent_ContainsMarkers(t *testing.T) {
	markers := []string{
		`\e]133;A`,
		`\e]133;B`,
		`\e]133;C`,
		`\e]133;D`,
	}

	for name, content := range scripts {
		for _, marker := range markers {
			if !strings.Contains(content, marker) {
				t.Errorf("script %s missing marker %q", name, marker)
			}
		}
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
