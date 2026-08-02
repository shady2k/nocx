package ssh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// TestSSHConfigResolver_Conformance runs ssh -G against a real ssh binary and
// a controlled config file, verifying that the parsing matches expectations.
//
// This test is skipped by default. To run it, set the environment variable
// NOCX_TEST_SSH_G=1. It requires ssh(1) on PATH.
func TestSSHConfigResolver_Conformance(t *testing.T) {
	if os.Getenv("NOCX_TEST_SSH_G") == "" {
		t.Skip("Skipping: set NOCX_TEST_SSH_G=1 to run the real-ssh conformance test")
	}

	// Verify ssh is on PATH.
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatalf("ssh not found on PATH: %v", err)
	}
	t.Logf("using ssh: %s", sshPath)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")

	// Write a config with host aliases, a Match directive (the bug we're
	// closing), and an Include to test cache-scope limitations.
	configContent := `
Host dev
    HostName dev.example.com
    User developer
    Port 2222

Host prod
    HostName 10.0.0.1
    User deploy

Match user developer
    ForwardAgent yes

Host nocx-test
    HostName 127.0.0.1
    Port 2222
    IdentityFile ~/.ssh/nocx_test_key
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	logger := log.NewSlogAdapter(nil)

	// Test 1: Baseline — ssh -G with -F <configPath> works.
	t.Run("sshG_binary_responds", func(t *testing.T) {
		// #nosec G204 — this is the conformance test against the real ssh oracle.
		cmd := exec.Command(sshPath, "-F", configPath, "-G", "dev")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("ssh -G dev: %v", err)
		}
		if !strings.Contains(string(out), "hostname dev.example.com") {
			t.Errorf("ssh -G dev output missing hostname dev.example.com:\n%s", string(out))
		}
		if !strings.Contains(string(out), "port 2222") {
			t.Errorf("ssh -G dev output missing port 2222:\n%s", string(out))
		}
	})

	// Test 2: The Match directive bug is fixed — ssh -G handles Match.
	t.Run("match_directive_does_not_break_resolution", func(t *testing.T) {
		resolver := NewSSHConfigResolver(logger, configPath, "")
		host, err := resolver.ResolveHost(context.Background(), "prod")
		if err != nil {
			t.Fatalf("ResolveHost prod: %v", err)
		}
		if host != "10.0.0.1" {
			t.Errorf("ResolveHost(prod) = %q, want 10.0.0.1", host)
		}
	})

	// Test 3: Full config resolution.
	t.Run("full_config_resolution", func(t *testing.T) {
		resolver := NewSSHConfigResolver(logger, configPath, "")
		cfg, err := resolver.ResolveConfig(context.Background(), "dev")
		if err != nil {
			t.Fatalf("ResolveConfig dev: %v", err)
		}
		if cfg.HostName != "dev.example.com" {
			t.Errorf("HostName = %q, want dev.example.com", cfg.HostName)
		}
		if cfg.User != "developer" {
			t.Errorf("User = %q, want developer", cfg.User)
		}
		if cfg.Port != 2222 {
			t.Errorf("Port = %d, want 2222", cfg.Port)
		}
	})

	// Test 4: Host with IdentityFile.
	t.Run("host_with_identity_file", func(t *testing.T) {
		resolver := NewSSHConfigResolver(logger, configPath, "")
		cfg, err := resolver.ResolveConfig(context.Background(), "nocx-test")
		if err != nil {
			t.Fatalf("ResolveConfig nocx-test: %v", err)
		}
		if cfg.HostName != "127.0.0.1" {
			t.Errorf("HostName = %q, want 127.0.0.1", cfg.HostName)
		}
		if cfg.Port != 2222 {
			t.Errorf("Port = %d, want 2222", cfg.Port)
		}
		if cfg.IdentityFile == "" {
			t.Error("IdentityFile should not be empty for nocx-test")
		}
		if !strings.Contains(cfg.IdentityFile, "nocx_test_key") {
			t.Errorf("IdentityFile = %q, want path containing nocx_test_key", cfg.IdentityFile)
		}
	})

	// Test 5: Cache invalidation with real ssh.
	t.Run("cache_invalidation", func(t *testing.T) {
		resolver := NewSSHConfigResolver(logger, configPath, "")

		// First resolution should succeed.
		host, err := resolver.ResolveHost(context.Background(), "prod")
		if err != nil {
			t.Fatalf("first ResolveHost prod: %v", err)
		}
		if host != "10.0.0.1" {
			t.Errorf("first ResolveHost(prod) = %q, want 10.0.0.1", host)
		}

		// Update the config to change prod's HostName.
		updatedConfig := "Host prod\n    HostName prod-new.example.com\n    User deploy\n"
		if wErr := os.WriteFile(configPath, []byte(updatedConfig), 0o600); wErr != nil {
			t.Fatalf("write updated config: %v", wErr)
		}

		// Resolution should see the new value after the cache is invalidated.
		host, err = resolver.ResolveHost(context.Background(), "prod")
		if err != nil {
			t.Fatalf("second ResolveHost prod: %v", err)
		}
		if host != "prod-new.example.com" {
			t.Errorf("after update ResolveHost(prod) = %q, want prod-new.example.com", host)
		}
	})

	// Test 6: Unknown host returns original hostname.
	t.Run("unknown_host_returns_original", func(t *testing.T) {
		resolver := NewSSHConfigResolver(logger, configPath, "")
		host, err := resolver.ResolveHost(context.Background(), "nonexistent")
		if err != nil {
			t.Fatalf("ResolveHost nonexistent: %v", err)
		}
		if host != "nonexistent" {
			t.Errorf("ResolveHost(nonexistent) = %q, want nonexistent", host)
		}
	})
}
