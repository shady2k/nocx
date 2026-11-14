package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/shellenv"
)

// dumpEnvScript is the tail every fake shell in this package ends with: the
// marker, then the same `exec env -0` production runs. Fake shells that must
// resolve to a controlled environment run it under `env -i`.
const dumpEnvScript = "printf '" + shellenv.Marker + "'\nexec /usr/bin/env -0\n"

// TestResolveShellEnvSucceedsOnAnOrdinaryMachine is D6's paired success
// test: on an ordinary machine the resolver produces a PATH-carrying
// environment from the real shell.
func TestResolveShellEnvSucceedsOnAnOrdinaryMachine(t *testing.T) {
	shell := detectShell()
	env, err := resolveShellEnv(context.Background(), shell, 5*time.Second, 256<<10)
	if err != nil {
		t.Fatalf("resolveShellEnv(%s): %v", shell, err)
	}
	if _, ok := shellenv.Path(env); !ok {
		t.Fatalf("resolved environment has no PATH: %v", env)
	}
	for _, kv := range env {
		if !strings.Contains(kv, "=") {
			t.Fatalf("malformed entry %q", kv)
		}
	}
}

func TestResolveShellEnvShellMissing(t *testing.T) {
	_, err := resolveShellEnv(context.Background(), "/nonexistent/shell", time.Second, 64<<10)
	if err == nil {
		t.Fatal("resolution succeeded with a missing shell")
	}
}

// TestResolveShellEnvTimesOut: a shell whose rc file hangs is bounded by the
// deadline, and the failure is a timeout-shaped error.
func TestResolveShellEnvTimesOut(t *testing.T) {
	dir := t.TempDir()
	hang := filepath.Join(dir, "hang-shell")
	// #nosec G306 — the script must be executable: the resolver execs it.
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 1000\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := resolveShellEnv(context.Background(), hang, 200*time.Millisecond, 64<<10)
	if err == nil {
		t.Fatal("resolution succeeded against a hanging shell")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("resolution took %s, deadline not enforced", elapsed)
	}
}

func TestResolveShellEnvNoPathDegrades(t *testing.T) {
	// A shell that resolves an environment without PATH is not a usable
	// environment — the resolver says so rather than returning it.
	dir := t.TempDir()
	script := filepath.Join(dir, "empty-shell")
	// #nosec G306 — the script must be executable: the resolver execs it.
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+
		"printf '"+shellenv.Marker+"'\nexec /usr/bin/env -i FOO=bar /usr/bin/env -0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := resolveShellEnv(context.Background(), script, time.Second, 64<<10)
	if err == nil || !strings.Contains(err.Error(), "no PATH") {
		t.Fatalf("err = %v, want a no-PATH degradation", err)
	}
}

func TestEnvCacheResolveAndFail(t *testing.T) {
	// Success is cached; a failure is retried (not poisoned).
	dir := t.TempDir()
	shell := filepath.Join(dir, "ok-shell")
	// #nosec G306 — the script must be executable: the resolver execs it.
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"+
		"printf '"+shellenv.Marker+"'\nexec /usr/bin/env -i PATH=/usr/bin X=1 /usr/bin/env -0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newEnvCache(shell, time.Second, 64<<10)
	env, state, _ := c.resolve(context.Background())
	if _, ok := shellenv.Path(env); state != git.EnvResolved || !ok {
		t.Fatalf("first resolve: state=%s env=%v", state, env)
	}
	// Cached: the second resolve must not re-run the shell. Prove it by
	// deleting the shell.
	if err := os.Remove(shell); err != nil {
		t.Fatal(err)
	}
	env2, state2, _ := c.resolve(context.Background())
	if state2 != git.EnvResolved || len(env2) == 0 {
		t.Fatalf("cached resolve lost the environment: state=%s", state2)
	}
}

// TestEnvCacheFailureRememberedThenRetried: a failed resolution is remembered
// — not re-attempted on every call, which is what turned one 5 s timeout into
// a tax on every open (nocx-6pz0) — but only for the cooldown: once it has
// passed, the next call retries, so a transient failure recovers instead of
// poisoning the cache for the process lifetime.
func TestEnvCacheFailureRememberedThenRetried(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "flaky-shell")
	count := filepath.Join(dir, "count")
	// #nosec G306 — the script must be executable: the resolver execs it.
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nprintf x >> "+count+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newEnvCache(shell, time.Second, 64<<10)

	if _, state, _ := c.resolve(context.Background()); state != git.EnvDegraded {
		t.Fatalf("first resolve: state = %s, want degraded", state)
	}
	// Within the cooldown the remembered failure is served without a retry.
	if _, state, _ := c.resolve(context.Background()); state != git.EnvDegraded {
		t.Fatalf("second resolve: state = %s, want degraded (remembered)", state)
	}
	// #nosec G304 — the count path is a test TempDir.
	if b, _ := os.ReadFile(count); len(b) != 1 {
		t.Fatalf("the failing probe ran %d times within the cooldown, want 1", len(b))
	}
	// Past the cooldown the next resolve retries — and a success is cached.
	c.lastTry = time.Now().Add(-time.Hour)
	// #nosec G306 — the script must be executable: the resolver execs it.
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"+
		"printf '"+shellenv.Marker+"'\nexec /usr/bin/env -i PATH=/usr/bin /usr/bin/env -0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env, state, _ := c.resolve(context.Background())
	if _, ok := shellenv.Path(env); state != git.EnvResolved || !ok {
		t.Fatalf("retry resolve: state=%s env=%v, want resolved", state, env)
	}
}
