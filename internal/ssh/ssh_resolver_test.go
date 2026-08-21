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
	// LastArgv records the most recent ResolveArgv call's argv verbatim,
	// so a test can prove the exact typed line reached the oracle.
	LastArgv []string
}

func NewStubConfigResolver() *StubConfigResolver {
	return &StubConfigResolver{Entries: make(map[string]HostConfig)}
}

// AddEntry adds a config entry for the given host.
func (s *StubConfigResolver) AddEntry(host string, cfg HostConfig) {
	s.Entries[host] = cfg
}

// ResolveArgv resolves the LAST argv element as the host (the oracle argv
// shape is ["ssh", "-G", ...options, destination]) and records the exact
// argv. The typed options themselves are ignored for the answer — the stub
// has no getopt semantics — but a caller that needs the options in the
// resolution can seed an entry for the exact destination with the resolved
// values.
func (s *StubConfigResolver) ResolveArgv(_ context.Context, argv []string) (*HostConfig, error) {
	s.LastArgv = append([]string(nil), argv...)
	if len(argv) == 0 {
		return &HostConfig{HostName: "", User: currentUser(), Port: 22}, errors.New("empty oracle argv")
	}
	host := argv[len(argv)-1]
	return s.ResolveConfig(context.Background(), host)
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
			HostName:      e.HostName,
			User:          e.User,
			Port:          e.Port,
			IdentityFile:  e.IdentityFile,
			RemoteCommand: e.RemoteCommand,
			RequestTTY:    e.RequestTTY,

			ControlMaster:  e.ControlMaster,
			ControlPath:    e.ControlPath,
			ControlPersist: e.ControlPersist,
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

func TestParseSSHGOutput_RemoteCommand(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		output := "remotecommand top -d 1\n"
		cfg, err := parseSSHGOutput(output, "myhost")
		if err != nil {
			t.Fatalf("parseSSHGOutput: %v", err)
		}
		if cfg.RemoteCommand != "top -d 1" {
			t.Errorf("RemoteCommand = %q, want %q", cfg.RemoteCommand, "top -d 1")
		}
	})

	t.Run("none_is_absent", func(t *testing.T) {
		// ssh -G prints "remotecommand none" when the directive is unset;
		// "none" is the oracle's sentinel for "no command", not a literal
		// command, so it must normalize to the empty representation.
		output := "remotecommand none\n"
		cfg, err := parseSSHGOutput(output, "myhost")
		if err != nil {
			t.Fatalf("parseSSHGOutput: %v", err)
		}
		if cfg.RemoteCommand != "" {
			t.Errorf("RemoteCommand = %q, want empty (unset)", cfg.RemoteCommand)
		}
	})
}

func TestParseSSHGOutput_RequestTTY(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"yes", "requesttty yes\n", "yes"},
		// OpenSSH >= 10 serializes booleans as true/false; older versions
		// print yes/no. Both render the same directive value and normalize
		// to the canonical yes/no so callers are version-independent.
		{"true_normalized", "requesttty true\n", "yes"},
		{"force", "requesttty force\n", "force"},
		// "auto" is the RequestTTY default; ssh -G prints it when the
		// directive is unset. It means "ssh decides", and for a command
		// execution ssh's decision is no TTY — indistinguishable from
		// unset, so it collapses to the empty representation.
		{"auto_is_default", "requesttty auto\n", ""},
		{"false_normalized", "requesttty false\n", "no"},
		{"no", "requesttty no\n", "no"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseSSHGOutput(tt.line, "myhost")
			if err != nil {
				t.Fatalf("parseSSHGOutput: %v", err)
			}
			if cfg.RequestTTY != tt.want {
				t.Errorf("RequestTTY = %q, want %q", cfg.RequestTTY, tt.want)
			}
		})
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
		HostName:      "10.0.0.1",
		User:          "deploy",
		Port:          2222,
		RemoteCommand: "top -d 1",
		RequestTTY:    "yes",
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
	if cfg.RemoteCommand != "top -d 1" {
		t.Errorf("RemoteCommand = %q, want %q", cfg.RemoteCommand, "top -d 1")
	}
	if cfg.RequestTTY != "yes" {
		t.Errorf("RequestTTY = %q, want yes", cfg.RequestTTY)
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
// and hostnames/, remotecommands/ and requestttys/ override directories.
// Returns (sshPath, configPath, hostnamesDir).
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
	remotecommandsDir := filepath.Join(dir, "remotecommands")
	if err := os.MkdirAll(remotecommandsDir, 0o750); err != nil {
		t.Fatalf("mkdir remotecommands: %v", err)
	}

	requestttysDir := filepath.Join(dir, "requestttys")
	if err := os.MkdirAll(requestttysDir, 0o750); err != nil {
		t.Fatalf("mkdir requestttys: %v", err)
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

# Read RemoteCommand override from remotecommands/<host>, fall back to the
# oracle's rendering of unset: "remotecommand none".
rc_file="%s/${host}"
if [ -f "$rc_file" ]; then
    rc=$(cat "$rc_file")
else
    rc="none"
fi

# Read RequestTTY override from requestttys/<host>, fall back to the
# oracle's rendering of unset: "requesttty auto".
tty_file="%s/${host}"
if [ -f "$tty_file" ]; then
    tty=$(cat "$tty_file")
else
    tty="auto"
fi

echo "user testuser"
echo "hostname $hn"
echo "port 22"
echo "identityfile ~/.ssh/id_rsa"
echo "remotecommand $rc"
echo "requesttty $tty"
`, counterPath, hostnamesDir, remotecommandsDir, requestttysDir)
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

func TestSSHConfigResolver_RemoteCommandAndRequestTTY(t *testing.T) {
	sshPath, configPath, _ := fakeSSHClient(t)
	dir := filepath.Dir(sshPath)

	// Host with a RemoteCommand containing spaces and RequestTTY set.
	if err := os.WriteFile(filepath.Join(dir, "remotecommands", "rc-host"), []byte("top -d 1"), 0o600); err != nil {
		t.Fatalf("write remotecommand: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requestttys", "rc-host"), []byte("yes"), 0o600); err != nil {
		t.Fatalf("write requesttty: %v", err)
	}

	logger := log.NewSlogAdapter(nil)
	resolver := NewSSHConfigResolver(logger, configPath, sshPath)

	cfg, err := resolver.ResolveConfig(context.Background(), "rc-host")
	if err != nil {
		t.Fatalf("ResolveConfig rc-host: %v", err)
	}
	// The parser must preserve the full command, not just its first token.
	if cfg.RemoteCommand != "top -d 1" {
		t.Errorf("RemoteCommand = %q, want %q", cfg.RemoteCommand, "top -d 1")
	}
	if cfg.RequestTTY != "yes" {
		t.Errorf("RequestTTY = %q, want yes", cfg.RequestTTY)
	}

	// A host without overrides: the fake ssh prints "remotecommand none" and
	// "requesttty auto" — the oracle's rendering of "unset" — which must
	// resolve to the empty representation.
	cfg, err = resolver.ResolveConfig(context.Background(), "plain-host")
	if err != nil {
		t.Fatalf("ResolveConfig plain-host: %v", err)
	}
	if cfg.RemoteCommand != "" {
		t.Errorf("RemoteCommand = %q, want empty (unset)", cfg.RemoteCommand)
	}
	if cfg.RequestTTY != "" {
		t.Errorf("RequestTTY = %q, want empty (unset default)", cfg.RequestTTY)
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

// Two argvs naming ONE destination are two questions, and each gets its own
// answer — before and after the other has been asked.
//
// The oracle cache was keyed by the resolved identity (user@host:port) with
// the argv only an index into it, on the assumption that every argv for one
// destination resolves the same. ADR-0035 made that false on purpose: the
// typed wrapper asks about the user's own line, and then about the same line
// plus our own ControlMaster/ControlPath/ControlPersist, and the second
// question exists BECAUSE it answers differently — only ssh can expand the
// %C in the socket path.
//
// Sharing one entry meant whichever question ran last owned the destination.
// The FIRST typed ssh to a host worked, because neither argv was cached yet;
// every one after it got the other question's answer, found no control path
// for the wrapped line, and refused to interpose with `no-control-path`. The
// user's second connection to the same host came up unintegrated
// (e2e/nocxify-journey.spec.ts, 2026-08-21).
func TestResolveArgv_TwoArgvsForOneDestinationKeepTheirOwnAnswers(t *testing.T) {
	sshPath, configPath := fakeSSHClientWithControlPath(t)
	resolver := NewSSHConfigResolver(log.NewSlogAdapter(nil), configPath, sshPath)

	plain := []string{"ssh", "-G", "-p", "2222", "e2e@127.0.0.1"}
	wrapped := []string{
		"ssh", "-G",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/nocx-mux-0/m-deadbeef",
		"-o", "ControlPersist=no",
		"-p", "2222", "e2e@127.0.0.1",
	}

	ask := func(what string, argv []string) *HostConfig {
		t.Helper()
		cfg, err := resolver.ResolveArgv(context.Background(), argv)
		if err != nil {
			t.Fatalf("ResolveArgv(%s): %v", what, err)
		}
		return cfg
	}

	// Both are asked once, in the order the typed wrapper asks them.
	if cp := ask("plain, first", plain).ControlPath; cp != "" {
		t.Fatalf("the user's own line reports ControlPath %q, want none", cp)
	}
	if cp := ask("wrapped, first", wrapped).ControlPath; cp == "" {
		t.Fatal("the wrapped line reports no ControlPath; the fake oracle is not answering the question this test asks")
	}

	// And again — the second connection to the same host.
	if cp := ask("plain, second", plain).ControlPath; cp != "" {
		t.Errorf("the user's own line reports ControlPath %q on the second ask, want none — "+
			"nocx would refuse the rewrite as the user's own multiplex policy", cp)
	}
	if cp := ask("wrapped, second", wrapped).ControlPath; cp == "" {
		t.Error("the wrapped line reports no ControlPath on the second ask — " +
			"nocx refuses to interpose with no-control-path and the session comes up unintegrated")
	}

	// The paired half: a REPEAT of one argv is still served from the cache,
	// so the fix did not buy correctness with a subprocess per question.
	before := sshInvocationCount(t, sshPath)
	_ = ask("wrapped, third", wrapped)
	if after := sshInvocationCount(t, sshPath); after != before {
		t.Errorf("ssh invocations %d → %d for a repeated argv, want no new spawn", before, after)
	}
}

// fakeSSHClientWithControlPath is an ssh -G stand-in that answers about the
// OPTIONS as well as the host: it echoes `controlpath` when one was asked
// for, which is exactly the difference the test above is about. The fake in
// fakeSSHClient consumes a fixed `-F <config> -G <host>` and cannot.
func fakeSSHClientWithControlPath(t *testing.T) (sshPath, configPath string) {
	t.Helper()
	dir := t.TempDir()
	configPath = filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	counterPath := filepath.Join(dir, "counter")
	if err := os.WriteFile(counterPath, []byte("0"), 0o600); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	sshPath = filepath.Join(dir, "ssh")
	script := fmt.Sprintf(`#!/bin/sh
counter="%s"
c=$(cat "$counter" 2>/dev/null || echo 0)
echo $((c + 1)) > "$counter"

cp=""
host=""
for a in "$@"; do
    case "$a" in
        ControlPath=*) cp="${a#ControlPath=}" ;;
        -*) ;;
        *) host="$a" ;;
    esac
done

echo "user e2e"
echo "hostname 127.0.0.1"
echo "port 2222"
echo "remotecommand none"
echo "requesttty auto"
[ -n "$cp" ] && echo "controlpath $cp"
[ -n "$host" ] || exit 1
exit 0
`, counterPath)
	if err := os.WriteFile(sshPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	// #nosec G302 — a stand-in ssh must be executable to be run.
	if err := os.Chmod(sshPath, 0o700); err != nil {
		t.Fatalf("chmod fake ssh: %v", err)
	}
	return sshPath, configPath
}
