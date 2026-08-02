package ssh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ---------------------------------------------------------------------------
// StubConfigResolver — a test fake for ConfigResolver that returns static
// results. Used by binding tests and other tests that need deterministic SSH
// config resolution without shelling out to ssh.
// ---------------------------------------------------------------------------

// StubConfigResolver implements ConfigResolver from a static map.
// An empty HostConfig for a host means "return defaults" (no alias).
type StubConfigResolver struct {
	Entries map[string]HostConfig
}

func NewStubConfigResolver() *StubConfigResolver {
	return &StubConfigResolver{Entries: make(map[string]HostConfig)}
}

// AddEntry adds a config entry for the given host.
func (s *StubConfigResolver) AddEntry(host string, cfg HostConfig) {
	s.Entries[host] = cfg
}

func (s *StubConfigResolver) ResolveHost(_ context.Context, host string) (string, error) {
	if e, ok := s.Entries[host]; ok && e.HostName != "" {
		return e.HostName, nil
	}
	return host, nil
}

func (s *StubConfigResolver) ResolveConfig(_ context.Context, host string) (*HostConfig, error) {
	if e, ok := s.Entries[host]; ok {
		return &HostConfig{
			HostName:     e.HostName,
			User:         e.User,
			Port:         e.Port,
			IdentityFile: e.IdentityFile,
		}, nil
	}
	return &HostConfig{HostName: host, User: currentUser(), Port: 22}, nil
}

// compile-time interface check
var _ ConfigResolver = (*StubConfigResolver)(nil)

// ---------------------------------------------------------------------------
// parseSSHGOutput tests
// ---------------------------------------------------------------------------

func TestParseSSHGOutput_FullConfig(t *testing.T) {
	output := `user ubuntu
hostname 10.0.0.42
port 2222
identityfile ~/.ssh/special_id
forwardagent yes
stricthostkeychecking accept-new
`
	cfg, err := parseSSHGOutput(output, "myhost")
	if err != nil {
		t.Fatalf("parseSSHGOutput: %v", err)
	}
	if cfg.HostName != "10.0.0.42" {
		t.Errorf("HostName = %q, want 10.0.0.42", cfg.HostName)
	}
	if cfg.User != "ubuntu" {
		t.Errorf("User = %q, want ubuntu", cfg.User)
	}
	if cfg.Port != 2222 {
		t.Errorf("Port = %d, want 2222", cfg.Port)
	}
	if cfg.IdentityFile != expandPath("~/.ssh/special_id") {
		t.Errorf("IdentityFile = %q, want %q", cfg.IdentityFile, expandPath("~/.ssh/special_id"))
	}
}

func TestParseSSHGOutput_Minimal(t *testing.T) {
	// ssh -G for an unresolvable host still outputs some fields.
	output := `user vagrant
hostname unknown-host
port 22
identityfile ~/.ssh/id_rsa
identityfile ~/.ssh/id_ecdsa
`
	cfg, err := parseSSHGOutput(output, "unknown-host")
	if err != nil {
		t.Fatalf("parseSSHGOutput: %v", err)
	}
	if cfg.HostName != "unknown-host" {
		t.Errorf("HostName = %q, want unknown-host", cfg.HostName)
	}
	if cfg.User != "vagrant" {
		t.Errorf("User = %q, want vagrant", cfg.User)
	}
	if cfg.Port != 22 {
		t.Errorf("Port = %d, want 22", cfg.Port)
	}
	// Should take the first identityfile.
	if cfg.IdentityFile != expandPath("~/.ssh/id_rsa") {
		t.Errorf("IdentityFile = %q, want %q", cfg.IdentityFile, expandPath("~/.ssh/id_rsa"))
	}
}

func TestParseSSHGOutput_EmptyOutput(t *testing.T) {
	cfg, err := parseSSHGOutput("", "myhost")
	if err != nil {
		t.Fatalf("parseSSHGOutput: %v", err)
	}
	if cfg.HostName != "myhost" {
		t.Errorf("HostName = %q, want myhost (original host)", cfg.HostName)
	}
	if cfg.Port != 0 {
		t.Errorf("Port = %d, want 0 (unset — caller determines default)", cfg.Port)
	}
}

func TestParseSSHGOutput_NoHostName(t *testing.T) {
	output := `user test
port 2222
`
	cfg, err := parseSSHGOutput(output, "myhost")
	if err != nil {
		t.Fatalf("parseSSHGOutput: %v", err)
	}
	if cfg.HostName != "myhost" {
		t.Errorf("HostName = %q, want myhost (original host)", cfg.HostName)
	}
}

func TestParseSSHGOutput_InvalidPort(t *testing.T) {
	output := "port not-a-number\n"
	cfg, err := parseSSHGOutput(output, "myhost")
	if err != nil {
		t.Fatalf("parseSSHGOutput: %v", err)
	}
	if cfg.Port != 0 {
		t.Errorf("Port = %d, want 0 (unset on unparseable port — caller determines default)", cfg.Port)
	}
}

// ---------------------------------------------------------------------------
// StubConfigResolver tests
// ---------------------------------------------------------------------------

func TestStubConfigResolver_ResolveHost(t *testing.T) {
	s := NewStubConfigResolver()
	s.AddEntry("dev", HostConfig{HostName: "dev.example.com"})

	host, err := s.ResolveHost(context.Background(), "dev")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "dev.example.com" {
		t.Errorf("ResolveHost = %q, want dev.example.com", host)
	}

	// Unknown host returns original.
	host, err = s.ResolveHost(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "unknown" {
		t.Errorf("ResolveHost = %q, want unknown", host)
	}
}

func TestStubConfigResolver_ResolveConfig(t *testing.T) {
	s := NewStubConfigResolver()
	s.AddEntry("prod", HostConfig{
		HostName: "10.0.0.1",
		User:     "deploy",
		Port:     2222,
	})

	cfg, err := s.ResolveConfig(context.Background(), "prod")
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.HostName != "10.0.0.1" {
		t.Errorf("HostName = %q, want 10.0.0.1", cfg.HostName)
	}
	if cfg.User != "deploy" {
		t.Errorf("User = %q, want deploy", cfg.User)
	}
	if cfg.Port != 2222 {
		t.Errorf("Port = %d, want 2222", cfg.Port)
	}

	// Unknown host returns defaults.
	cfg, err = s.ResolveConfig(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.HostName != "unknown" {
		t.Errorf("HostName = %q, want unknown", cfg.HostName)
	}
	if cfg.Port != 22 {
		t.Errorf("Port = %d, want 22", cfg.Port)
	}
}

// ---------------------------------------------------------------------------
// Real ssh -G resolver tests — deterministic via a fake ssh binary.
// The fake reads hostname-to-canonical mappings from a directory the test
// controls, and writes invocation counts to a counter file.
// ---------------------------------------------------------------------------

// fakeSSHClient creates a temp dir with a fake ssh binary, an empty config,
// and a hostnames/ directory. Returns (sshPath, configPath, hostnamesDir).
func fakeSSHClient(t *testing.T) (sshPath, configPath, hostnamesDir string) {
	t.Helper()
	dir := t.TempDir()

	configPath = filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	hostnamesDir = filepath.Join(dir, "hostnames")
	if err := os.MkdirAll(hostnamesDir, 0o750); err != nil {
		t.Fatalf("mkdir hostnames: %v", err)
	}

	counterPath := filepath.Join(dir, "counter")
	if err := os.WriteFile(counterPath, []byte("0"), 0o600); err != nil {
		t.Fatalf("write counter: %v", err)
	}

	sshPath = filepath.Join(dir, "ssh")
	script := fmt.Sprintf(`#!/bin/sh
# Fake ssh -G: args are "-F <config> -G <host>"
shift 2   # consume -F <config>
shift     # consume -G
host="$1"

# Increment invocation counter
counter="%s"
c=$(cat "$counter" 2>/dev/null || echo 0)
echo $((c + 1)) > "$counter"

# Read hostname override from hostnames/<host> file, fall back to host as-is.
hn_file="%s/${host}"
if [ -f "$hn_file" ]; then
    hn=$(cat "$hn_file")
else
    hn="$host"
fi

echo "user testuser"
echo "hostname $hn"
echo "port 22"
echo "identityfile ~/.ssh/id_rsa"
`, counterPath, hostnamesDir)
	// Write non-executable first (passes G306), then chmod to executable.
	if err := os.WriteFile(sshPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	// #nosec G302 — test fake ssh binary must be executable.
	if err := os.Chmod(sshPath, 0o700); err != nil {
		t.Fatalf("chmod fake ssh: %v", err)
	}
	return sshPath, configPath, hostnamesDir
}

// sshInvocationCount reads the counter file the fake ssh writes to.
func sshInvocationCount(t *testing.T, sshPath string) int {
	t.Helper()
	dir := filepath.Dir(sshPath)
	counterPath := filepath.Join(dir, "counter")
	// #nosec G304 — path is within the test's temp dir, not user-controlled.
	data, err := os.ReadFile(counterPath)
	if err != nil {
		return 0
	}
	n := 0
	_, _ = fmt.Sscanf(string(data), "%d", &n)
	return n
}

func TestSSHConfigResolver_BasicResolution(t *testing.T) {
	sshPath, configPath, hostnamesDir := fakeSSHClient(t)
	if err := os.WriteFile(filepath.Join(hostnamesDir, "myalias"), []byte("10.0.0.1"), 0o600); err != nil {
		t.Fatalf("write hostname: %v", err)
	}

	logger := log.NewSlogAdapter(nil)
	resolver := NewSSHConfigResolver(logger, configPath, sshPath)

	host, err := resolver.ResolveHost(context.Background(), "myalias")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "10.0.0.1" {
		t.Errorf("ResolveHost = %q, want 10.0.0.1", host)
	}

	cfg, err := resolver.ResolveConfig(context.Background(), "myalias")
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.HostName != "10.0.0.1" {
		t.Errorf("HostName = %q, want 10.0.0.1", cfg.HostName)
	}
	if cfg.User != "testuser" {
		t.Errorf("User = %q, want testuser", cfg.User)
	}
	if cfg.Port != 22 {
		t.Errorf("Port = %d, want 22", cfg.Port)
	}
}

func TestSSHConfigResolver_UnknownHost(t *testing.T) {
	sshPath, configPath, _ := fakeSSHClient(t)
	logger := log.NewSlogAdapter(nil)
	resolver := NewSSHConfigResolver(logger, configPath, sshPath)

	host, err := resolver.ResolveHost(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host != "unknown" {
		t.Errorf("ResolveHost = %q, want unknown", host)
	}

	cfg, err := resolver.ResolveConfig(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.HostName != "unknown" {
		t.Errorf("HostName = %q, want unknown", cfg.HostName)
	}
}

func TestSSHConfigResolver_CacheBehavior(t *testing.T) {
	sshPath, configPath, hostnamesDir := fakeSSHClient(t)
	if err := os.WriteFile(filepath.Join(hostnamesDir, "myalias"), []byte("10.0.0.1"), 0o600); err != nil {
		t.Fatalf("write hostname: %v", err)
	}

	logger := log.NewSlogAdapter(nil)
	resolver := NewSSHConfigResolver(logger, configPath, sshPath)

	// First call goes to fake ssh — invocations = 1.
	host, err := resolver.ResolveHost(context.Background(), "myalias")
	if err != nil {
		t.Fatalf("first ResolveHost: %v", err)
	}
	if host != "10.0.0.1" {
		t.Errorf("first ResolveHost = %q, want 10.0.0.1", host)
	}
	if n := sshInvocationCount(t, sshPath); n != 1 {
		t.Errorf("ssh invocations after first call = %d, want 1", n)
	}

	// Second call hits cache — no extra subprocess.
	host, err = resolver.ResolveHost(context.Background(), "myalias")
	if err != nil {
		t.Fatalf("second ResolveHost: %v", err)
	}
	if host != "10.0.0.1" {
		t.Errorf("second ResolveHost = %q, want 10.0.0.1", host)
	}
	if n := sshInvocationCount(t, sshPath); n != 1 {
		t.Errorf("ssh invocations after second call = %d, want 1 (cache hit)", n)
	}
}

func TestSSHConfigResolver_CacheInvalidationOnConfigChange(t *testing.T) {
	sshPath, configPath, hostnamesDir := fakeSSHClient(t)
	// Initial hostname mapping: myalias → 10.0.0.1
	if err := os.WriteFile(filepath.Join(hostnamesDir, "myalias"), []byte("10.0.0.1"), 0o600); err != nil {
		t.Fatalf("write hostname: %v", err)
	}

	logger := log.NewSlogAdapter(nil)
	resolver := NewSSHConfigResolver(logger, configPath, sshPath)

	// First resolution with current config.
	host, err := resolver.ResolveHost(context.Background(), "myalias")
	if err != nil {
		t.Fatalf("first ResolveHost: %v", err)
	}
	if host != "10.0.0.1" {
		t.Errorf("first ResolveHost = %q, want 10.0.0.1", host)
	}
	if n := sshInvocationCount(t, sshPath); n != 1 {
		t.Errorf("ssh invocations after first = %d, want 1", n)
	}

	// Second call hits cache.
	host, err = resolver.ResolveHost(context.Background(), "myalias")
	if err != nil {
		t.Fatalf("cached ResolveHost: %v", err)
	}
	if host != "10.0.0.1" {
		t.Errorf("cached ResolveHost = %q, want 10.0.0.1", host)
	}
	if n := sshInvocationCount(t, sshPath); n != 1 {
		t.Errorf("ssh invocations after cache hit = %d, want 1", n)
	}

	// Change the hostname mapping AND config mtime to trigger invalidation.
	if wErr := os.WriteFile(filepath.Join(hostnamesDir, "myalias"), []byte("other.example.com"), 0o600); wErr != nil {
		t.Fatalf("update hostname: %v", wErr)
	}
	time.Sleep(10 * time.Millisecond) // ensure mtime change
	if wErr := os.WriteFile(configPath, []byte("Host myalias\n  HostName other.example.com\n"), 0o600); wErr != nil {
		t.Fatalf("update config: %v", wErr)
	}

	// Resolution should re-invoke ssh -G and see the new hostname.
	host, err = resolver.ResolveHost(context.Background(), "myalias")
	if err != nil {
		t.Fatalf("after config change ResolveHost: %v", err)
	}
	if host != "other.example.com" {
		t.Errorf("after config change ResolveHost = %q, want other.example.com", host)
	}
	if n := sshInvocationCount(t, sshPath); n != 2 {
		t.Errorf("ssh invocations after invalidation = %d, want 2", n)
	}
}

func TestSSHConfigResolver_EmptySSHPath(t *testing.T) {
	// Passing a nonexistent sshPath should return ErrSSHBinaryNotFound,
	// with the host returned as-is for degradation.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	logger := log.NewSlogAdapter(nil)
	resolver := NewSSHConfigResolver(logger, configPath, "/nonexistent/ssh-binary")

	host, err := resolver.ResolveHost(context.Background(), "testhost")
	if !errors.Is(err, ErrSSHBinaryNotFound) {
		t.Fatalf("ResolveHost err = %v, want ErrSSHBinaryNotFound", err)
	}
	if host != "testhost" {
		t.Errorf("ResolveHost = %q, want testhost (original host fallback)", host)
	}

	cfg, err := resolver.ResolveConfig(context.Background(), "testhost")
	if !errors.Is(err, ErrSSHBinaryNotFound) {
		t.Fatalf("ResolveConfig err = %v, want ErrSSHBinaryNotFound", err)
	}
	if cfg.HostName != "testhost" {
		t.Errorf("HostName = %q, want testhost (fallback)", cfg.HostName)
	}
	if cfg.Port != 0 {
		t.Errorf("Port = %d, want 0 (unset fallback — caller determines default)", cfg.Port)
	}
}

// ---------------------------------------------------------------------------
// Sentinel error checks
// ---------------------------------------------------------------------------

func TestResolverSentinelErrors(t *testing.T) {
	// Verify the sentinel errors are distinguishable via errors.Is.
	if !errors.Is(ErrSSHBinaryNotFound, ErrSSHBinaryNotFound) {
		t.Error("ErrSSHBinaryNotFound should be self-identifying")
	}
	if !errors.Is(ErrSSHConfigTimeout, ErrSSHConfigTimeout) {
		t.Error("ErrSSHConfigTimeout should be self-identifying")
	}
	if !errors.Is(ErrSSHConfigFailed, ErrSSHConfigFailed) {
		t.Error("ErrSSHConfigFailed should be self-identifying")
	}

	// Verify they are distinct.
	if errors.Is(ErrSSHBinaryNotFound, ErrSSHConfigTimeout) {
		t.Error("ErrSSHBinaryNotFound should not match ErrSSHConfigTimeout")
	}
	if errors.Is(ErrSSHBinaryNotFound, ErrSSHConfigFailed) {
		t.Error("ErrSSHBinaryNotFound should not match ErrSSHConfigFailed")
	}
	if errors.Is(ErrSSHConfigTimeout, ErrSSHConfigFailed) {
		t.Error("ErrSSHConfigTimeout should not match ErrSSHConfigFailed")
	}
}

// ---------------------------------------------------------------------------
// parseSSHGOutput benchmark (sanity: ~10k hosts × single lookup)
// ---------------------------------------------------------------------------

func BenchmarkParseSSHGOutput(b *testing.B) {
	output := `user ubuntu
hostname 10.0.0.42
port 2222
identityfile ~/.ssh/special_id
forwardagent yes
stricthostkeychecking accept-new
`
	b.ResetTimer()
	for range b.N {
		_, _ = parseSSHGOutput(output, strconv.Itoa(b.N))
	}
}

func BenchmarkParseSSHGOutput_Minimal(b *testing.B) {
	output := `user vagrant
hostname
port 22
`
	b.ResetTimer()
	for range b.N {
		_, _ = parseSSHGOutput(output, strconv.Itoa(b.N))
	}
}
