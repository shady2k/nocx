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
    IdentitiesOnly yes

Host tty-test
    HostName 192.0.2.20
    RemoteCommand top -d 1
    RequestTTY yes
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
		// dev sets neither RemoteCommand nor RequestTTY; ssh -G renders
		// the unset directives as "remotecommand none" (or omits the line)
		// and "requesttty auto", which must resolve to the empty
		// representation.
		if cfg.RemoteCommand != "" {
			t.Errorf("RemoteCommand = %q, want empty (unset)", cfg.RemoteCommand)
		}
		if cfg.RequestTTY != "" {
			t.Errorf("RequestTTY = %q, want empty (unset default)", cfg.RequestTTY)
		}
	})

	// Test 4: Host with IdentityFile, and IdentitiesOnly read beside it.
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
		// EXACTLY the configured file: a config that names its own keys
		// suppresses ssh's default list, so a resolver that appended the
		// defaults would offer keys this host's configuration never named.
		want := []string{expandPath("~/.ssh/nocx_test_key")}
		if len(cfg.IdentityFiles) != 1 || cfg.IdentityFiles[0] != want[0] {
			t.Errorf("IdentityFiles = %q, want %q", cfg.IdentityFiles, want)
		}
		if !cfg.IdentitiesOnly {
			t.Error("IdentitiesOnly = false, want true: the directive is in the config")
		}
	})

	// Test 4b: a host whose config names no key gets ssh's OWN default list,
	// in ssh's own order — which is the whole reason the list is read from the
	// oracle rather than hard-coded here.
	t.Run("default_identity_files_come_from_ssh", func(t *testing.T) {
		resolver := NewSSHConfigResolver(logger, configPath, "")
		cfg, err := resolver.ResolveConfig(context.Background(), "dev")
		if err != nil {
			t.Fatalf("ResolveConfig dev: %v", err)
		}
		if len(cfg.IdentityFiles) == 0 {
			t.Fatal("IdentityFiles is empty for a host that names no key: ssh always answers with its defaults")
		}
		if cfg.IdentitiesOnly {
			t.Error("IdentitiesOnly = true, want false: no config names it for this host")
		}
		// Which defaults, and in which order, is the ssh version's business —
		// that is the point of asking it. What is checked is that they ARE
		// ssh's own default key names and not something this package made up.
		for _, path := range cfg.IdentityFiles {
			base := filepath.Base(path)
			if !strings.HasPrefix(base, "id_") {
				t.Errorf("IdentityFiles carries %q, which is not one of ssh's default identity files", path)
			}
			if !strings.Contains(path, string(filepath.Separator)+".ssh"+string(filepath.Separator)) {
				t.Errorf("IdentityFiles carries %q, want a path under ~/.ssh", path)
			}
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

	// Test 7: Host with RemoteCommand and RequestTTY set in ssh_config.
	t.Run("host_with_remote_command_and_requesttty", func(t *testing.T) {
		// cache_invalidation (test 5) overwrites the config file with a
		// minimal one; restore the full config so tty-test still resolves.
		if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
			t.Fatalf("restore config: %v", err)
		}
		resolver := NewSSHConfigResolver(logger, configPath, "")
		cfg, err := resolver.ResolveConfig(context.Background(), "tty-test")
		if err != nil {
			t.Fatalf("ResolveConfig tty-test: %v", err)
		}
		if cfg.RemoteCommand != "top -d 1" {
			t.Errorf("RemoteCommand = %q, want %q", cfg.RemoteCommand, "top -d 1")
		}
		if cfg.RequestTTY != "yes" {
			t.Errorf("RequestTTY = %q, want yes", cfg.RequestTTY)
		}
	})
}
