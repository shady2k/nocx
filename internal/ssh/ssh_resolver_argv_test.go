package ssh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// scriptedSSH writes an executable fake "ssh" that records its exact argv
// to $SSH_ARGV_LOG and answers like `ssh -G` for a fixed destination whose
// port reflects a typed -p. The argv record is how a test proves the typed
// line reached the oracle (nocx-c5az) without a real ssh.
func scriptedSSH(t *testing.T) (path, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "argv.log")
	path = filepath.Join(dir, "ssh")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$SSH_ARGV_LOG"
echo "user testuser"
echo "hostname 10.0.0.1"
p=22
prev=
for a in "$@"; do
  [ "$prev" = "-p" ] && p=$a
  prev=$a
done
echo "port $p"
echo "remotecommand none"
echo "requesttty auto"
`
	// #nosec G306 — an executable stub the test invokes as ssh; 0600 would
	// make it unrunnable, which is the point of writing it.
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write scripted ssh: %v", err)
	}
	t.Setenv("SSH_ARGV_LOG", logPath)
	return path, logPath
}

// readArgvLog returns the recorded argv lines, or "" when ssh never ran.
func readArgvLog(t *testing.T, logPath string) string {
	t.Helper()
	b, err := os.ReadFile(logPath) // #nosec G304 — the scripted ssh's own log under the test's temp dir.
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read argv log: %v", err)
	}
	return string(b)
}

// spawnCount counts how many times the scripted ssh was invoked. The log
// holds the post-binary argv ("-G", options, destination — one line each),
// so the "-G" marker line appears exactly once per invocation.
func spawnCount(t *testing.T, logPath string) int {
	t.Helper()
	s := readArgvLog(t, logPath)
	return strings.Count(s, "-G\n")
}

func TestIdentityKey(t *testing.T) {
	cases := []struct {
		name string
		cfg  HostConfig
		want string
	}{
		{"plain", HostConfig{User: "pi", HostName: "192.168.0.93", Port: 22}, "pi@192.168.0.93:22"},
		{"unset port defaults to 22", HostConfig{User: "pi", HostName: "192.168.0.93", Port: 0}, "pi@192.168.0.93:22"},
		{"no user", HostConfig{HostName: "host", Port: 2222}, "host:2222"},
		{"ipv6 literal is bracketed", HostConfig{User: "u", HostName: "::1", Port: 22}, "u@[::1]:22"},
		{"hostname alias resolved away", HostConfig{User: "deploy", HostName: "10.0.0.1", Port: 2222}, "deploy@10.0.0.1:2222"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IdentityKey(&tc.cfg); got != tc.want {
				t.Errorf("IdentityKey(%+v) = %q, want %q", tc.cfg, got, tc.want)
			}
		})
	}
}

// TestResolveArgv_RunsTypedArgv: the typed argv reaches the ssh -G binary
// verbatim (a typed -p changes the answer), and the parsed config carries it.
func TestResolveArgv_RunsTypedArgv(t *testing.T) {
	sshPath, logPath := scriptedSSH(t)
	resolver := NewSSHConfigResolver(log.NewSlogAdapter(nil), "/nonexistent/config", sshPath)

	argv := []string{"ssh", "-G", "-p", "2222", "pi@192.168.0.93"}
	cfg, err := resolver.ResolveArgv(context.Background(), argv)
	if err != nil {
		t.Fatalf("ResolveArgv: %v", err)
	}
	if cfg.Port != 2222 {
		t.Errorf("Port = %d, want 2222 — the typed -p must reach the oracle", cfg.Port)
	}
	if cfg.HostName != "10.0.0.1" || cfg.User != "testuser" {
		t.Errorf("cfg = %+v, want the scripted ssh -G answer (hostname 10.0.0.1, user testuser)", cfg)
	}

	logged := readArgvLog(t, logPath)
	for _, want := range []string{"-G", "-p", "2222", "pi@192.168.0.93"} {
		if !strings.Contains(logged, want) {
			t.Errorf("oracle argv log missing %q; the typed line was not executed verbatim:\n%s", want, logged)
		}
	}
	if n := spawnCount(t, logPath); n != 1 {
		t.Errorf("ssh -G spawned %d times, want 1", n)
	}
}

// TestResolveArgv_CacheSkipsRepeatSpawn: a repeat of the same typed line
// must not spawn ssh -G again — the argvIndex fast path is what makes the
// second connection cheaper than the first.
func TestResolveArgv_CacheSkipsRepeatSpawn(t *testing.T) {
	sshPath, logPath := scriptedSSH(t)
	resolver := NewSSHConfigResolver(log.NewSlogAdapter(nil), "/nonexistent/config", sshPath)

	argv := []string{"ssh", "-G", "-p", "2222", "pi@host"}
	first, err := resolver.ResolveArgv(context.Background(), argv)
	if err != nil {
		t.Fatalf("first ResolveArgv: %v", err)
	}
	second, err := resolver.ResolveArgv(context.Background(), argv)
	if err != nil {
		t.Fatalf("second ResolveArgv: %v", err)
	}
	if first != second {
		t.Error("repeat resolution returned a different *HostConfig; the cache did not serve the second call")
	}
	if n := spawnCount(t, logPath); n != 1 {
		t.Errorf("ssh -G spawned %d times, want 1 (the cache must skip the repeat)", n)
	}
}

// TestResolveArgv_CacheKeyedByTheArgvNotTheDestination: two different typed
// lines that resolve to the same destination keep TWO cache entries, one per
// question. The typed host string is still not a key (the ADR-0015
// narrowing); neither is the destination they resolve to.
//
// THIS CASE USED TO ASSERT THE OPPOSITE — one entry, keyed by the resolved
// identity, with the argv only an index into it — and it is reversed
// deliberately rather than relaxed. ADR-0035 gave one destination two
// questions with two different right answers: the typed wrapper asks about
// the user's own line, and then about the same line plus our own
// ControlMaster/ControlPath/ControlPersist, because only ssh can expand the
// %C in the socket path. Sharing one entry gave the second question the
// first one's answer, and nocx refused to interpose on every typed ssh after
// the first (`no-control-path`; e2e/nocxify-journey.spec.ts, 2026-08-21).
//
// Nothing is paid for it. The spawn count below was TWO under the old key as
// well — it is right there in the assertion this replaces — so the shared
// entry never saved a subprocess. It only ever shared an answer, which is
// the whole of the defect.
func TestResolveArgv_CacheKeyedByTheArgvNotTheDestination(t *testing.T) {
	sshPath, logPath := scriptedSSH(t)
	r, ok := NewSSHConfigResolver(log.NewSlogAdapter(nil), "/nonexistent/config", sshPath).(*sshConfigResolver)
	if !ok {
		t.Fatal("resolver is not a *sshConfigResolver")
	}

	// The script answers identically for any destination, so both argv
	// sets resolve to testuser@10.0.0.1:22 — one identity, two lines.
	cfgA, err := r.ResolveArgv(context.Background(), []string{"ssh", "-G", "hostA"})
	if err != nil {
		t.Fatalf("ResolveArgv hostA: %v", err)
	}
	cfgB, err := r.ResolveArgv(context.Background(), []string{"ssh", "-G", "hostB"})
	if err != nil {
		t.Fatalf("ResolveArgv hostB: %v", err)
	}
	if IdentityKey(cfgA) != IdentityKey(cfgB) {
		t.Fatalf("IdentityKey differs: %q vs %q; the cache key would split one destination", IdentityKey(cfgA), IdentityKey(cfgB))
	}
	if len(r.argvCache) != 2 {
		t.Errorf("argvCache has %d entries, want 2 — one per question asked, so two argvs for one destination cannot answer for each other", len(r.argvCache))
	}
	if cfgA == cfgB {
		t.Error("both argvs were served the same *HostConfig; a second question about one destination must get its own answer")
	}
	if n := spawnCount(t, logPath); n != 2 {
		t.Errorf("ssh -G spawned %d times, want 2 (one per distinct argv)", n)
	}
}

// TestResolveArgv_PurgeClearsTheArgvFamily: the config-mtime purge clears
// the argv cache too, so a repeat after a purge spawns again rather than
// serving a stale answer.
func TestResolveArgv_PurgeClearsTheArgvFamily(t *testing.T) {
	sshPath, logPath := scriptedSSH(t)
	r, ok := NewSSHConfigResolver(log.NewSlogAdapter(nil), "/nonexistent/config", sshPath).(*sshConfigResolver)
	if !ok {
		t.Fatal("resolver is not a *sshConfigResolver")
	}

	argv := []string{"ssh", "-G", "host"}
	if _, err := r.ResolveArgv(context.Background(), argv); err != nil {
		t.Fatalf("ResolveArgv: %v", err)
	}
	r.purgeCache()
	if _, err := r.ResolveArgv(context.Background(), argv); err != nil {
		t.Fatalf("ResolveArgv after purge: %v", err)
	}
	if n := spawnCount(t, logPath); n != 2 {
		t.Errorf("ssh -G spawned %d times after purge, want 2 (a purge must force a re-spawn)", n)
	}
}

// TestResolveArgv_FailedOracleReturnsError: a failed or unavailable oracle
// returns the degraded config AND a distinguishable error — the planner
// refuses the rewrite on it (nocx-qwhp).
func TestResolveArgv_FailedOracleReturnsError(t *testing.T) {
	dir := t.TempDir()
	sshPath := filepath.Join(dir, "ssh")
	// #nosec G306 — an executable stub invoked as ssh; 0600 would make it unrunnable.
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write failing ssh: %v", err)
	}
	resolver := NewSSHConfigResolver(log.NewSlogAdapter(nil), "/nonexistent/config", sshPath)

	argv := []string{"ssh", "-G", "pi@host"}
	cfg, err := resolver.ResolveArgv(context.Background(), argv)
	if err == nil {
		t.Fatal("ResolveArgv returned nil error for a failing oracle")
	}
	if !errors.Is(err, ErrSSHConfigFailed) {
		t.Errorf("error %v does not wrap ErrSSHConfigFailed", err)
	}
	if cfg.HostName != "pi@host" {
		t.Errorf("degraded config HostName = %q, want the typed destination", cfg.HostName)
	}
}

// TestResolveArgv_RejectsMalformedArgv: the exec boundary validates the
// oracle shape; a violation is refused, never executed.
func TestResolveArgv_RejectsMalformedArgv(t *testing.T) {
	sshPath, logPath := scriptedSSH(t)
	resolver := NewSSHConfigResolver(log.NewSlogAdapter(nil), "/nonexistent/config", sshPath)

	for _, argv := range [][]string{
		{"ssh"},
		{"ssh", "-G"},
		{"scp", "-G", "host"},
		{"ssh", "-p", "2222", "host"},
	} {
		if _, err := resolver.ResolveArgv(context.Background(), argv); err == nil {
			t.Errorf("ResolveArgv(%v): expected an error for malformed argv", argv)
		}
	}
	if n := spawnCount(t, logPath); n != 0 {
		t.Errorf("malformed argv reached the binary %d times; the exec boundary must validate first", n)
	}
}

// TestStubConfigResolver_ResolveArgv: the stub records the exact argv and
// resolves the destination positional.
func TestStubConfigResolver_ResolveArgv(t *testing.T) {
	s := NewStubConfigResolver()
	s.AddEntry("prod", HostConfig{HostName: "10.0.0.1", User: "deploy", Port: 2222})

	argv := []string{"ssh", "-G", "-p", "9999", "prod"}
	cfg, err := s.ResolveArgv(context.Background(), argv)
	if err != nil {
		t.Fatalf("ResolveArgv: %v", err)
	}
	if cfg.HostName != "10.0.0.1" || cfg.User != "deploy" || cfg.Port != 2222 {
		t.Errorf("cfg = %+v, want the prod entry resolved", cfg)
	}
	if len(s.LastArgv) != len(argv) {
		t.Fatalf("LastArgv = %v, want the exact argv recorded", s.LastArgv)
	}
	for i := range argv {
		if s.LastArgv[i] != argv[i] {
			t.Errorf("LastArgv[%d] = %q, want %q — the typed argv must be recorded verbatim", i, s.LastArgv[i], argv[i])
		}
	}
}
