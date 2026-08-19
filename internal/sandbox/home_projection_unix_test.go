//go:build linux || darwin

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func projectionTestPolicy(runtimeRoot string, projections ...HomeProjection) *Policy {
	home := filepath.Join(runtimeRoot, "home")
	writable := make([]string, 0, len(projections)+2)
	writable = append(writable, home, filepath.Join(runtimeRoot, "tmp"))
	for _, projection := range projections {
		writable = append(writable, projection.HostPath)
	}
	return &Policy{
		Workspace:       projections[0].HostPath,
		WritableRoots:   writable,
		ReadOnlyRoots:   []string{},
		WritableFiles:   []string{},
		WritableDirs:    []string{},
		ReadOnlyFiles:   []string{},
		Shell:           "/bin/sh",
		Home:            home,
		Tmp:             filepath.Join(runtimeRoot, "tmp"),
		HomeProjections: append([]HomeProjection{}, projections...),
	}
}

func TestMaterializeHomeProjections_CreatesMinimalExactTargetForest(t *testing.T) {
	base := t.TempDir()
	runtimeRoot := projectionRuntime(t, base)
	config := projectionDir(t, filepath.Join(base, "host", ".config"))
	state := projectionDir(t, filepath.Join(config, "tool", "state"))
	share := projectionDir(t, filepath.Join(base, "host", ".local", "share", "tool"))
	policy := projectionTestPolicy(runtimeRoot,
		HomeProjection{HostPath: state, RelativePath: ".config/tool/state"},
		HomeProjection{HostPath: config, RelativePath: ".config"},
		HomeProjection{HostPath: share, RelativePath: ".local/share/tool"},
	)

	if err := materializeHomeProjections(runtimeRoot, policy); err != nil {
		t.Fatalf("materializeHomeProjections: %v", err)
	}
	configLink := filepath.Join(policy.Home, ".config")
	gotConfig, err := os.Readlink(configLink)
	if err != nil {
		t.Fatalf("read config link: %v", err)
	}
	if gotConfig != config {
		t.Fatalf("config link target = %q, want exact canonical %q", gotConfig, config)
	}
	if _, statErr := os.Lstat(filepath.Join(policy.Home, ".config", "tool", "state")); statErr != nil {
		t.Fatalf("nested logical projection not discoverable through topmost link: %v", statErr)
	}
	shareLink := filepath.Join(policy.Home, ".local", "share", "tool")
	gotShare, err := os.Readlink(shareLink)
	if err != nil {
		t.Fatalf("read sibling link: %v", err)
	}
	if gotShare != share {
		t.Fatalf("share link target = %q, want %q", gotShare, share)
	}
	for _, parent := range []string{filepath.Join(policy.Home, ".local"), filepath.Join(policy.Home, ".local", "share")} {
		info, err := os.Lstat(parent)
		if err != nil {
			t.Fatalf("lstat synthetic parent: %v", err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("synthetic parent mode = %v, want directory 0700", info.Mode())
		}
	}
}

func TestMaterializeHomeProjections_CreatesSiblingLinksUnderPrivateParent(t *testing.T) {
	base := t.TempDir()
	runtimeRoot := projectionRuntime(t, base)
	one := projectionDir(t, filepath.Join(base, "host", ".config", "one"))
	two := projectionDir(t, filepath.Join(base, "host", ".config", "two"))
	policy := projectionTestPolicy(runtimeRoot,
		HomeProjection{HostPath: one, RelativePath: ".config/one"},
		HomeProjection{HostPath: two, RelativePath: ".config/two"},
	)

	if err := materializeHomeProjections(runtimeRoot, policy); err != nil {
		t.Fatalf("materializeHomeProjections: %v", err)
	}
	for name, want := range map[string]string{"one": one, "two": two} {
		got, err := os.Readlink(filepath.Join(policy.Home, ".config", name))
		if err != nil {
			t.Fatalf("read sibling %s: %v", name, err)
		}
		if got != want {
			t.Fatalf("sibling %s target = %q, want %q", name, got, want)
		}
	}
}

func TestMaterializeHomeProjections_RejectsUnsafeParentsAndCollisionsPathFree(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, policy *Policy)
	}{
		{
			name: "symlink parent",
			setup: func(t *testing.T, policy *Policy) {
				t.Helper()
				outside := projectionDir(t, filepath.Join(filepath.Dir(filepath.Dir(policy.Home)), "outside-parent"))
				if err := os.Symlink(outside, filepath.Join(policy.Home, ".config")); err != nil {
					t.Fatalf("symlink parent: %v", err)
				}
			},
		},
		{
			name: "existing final entry",
			setup: func(t *testing.T, policy *Policy) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(policy.Home, ".config"), 0o700); err != nil {
					t.Fatalf("mkdir parent: %v", err)
				}
				if err := os.WriteFile(filepath.Join(policy.Home, ".config", "tool"), []byte("collision"), 0o600); err != nil {
					t.Fatalf("write collision: %v", err)
				}
			},
		},
		{
			name: "unsafe parent mode",
			setup: func(t *testing.T, policy *Policy) {
				t.Helper()
				if err := os.Chmod(policy.Home, 0o750); err != nil { //nolint:gosec // deliberately unsafe mode fixture.
					t.Fatalf("chmod home: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			runtimeRoot := projectionRuntime(t, base)
			target := projectionDir(t, filepath.Join(base, "host", ".config", "tool"))
			policy := projectionTestPolicy(runtimeRoot, HomeProjection{HostPath: target, RelativePath: ".config/tool"})
			tc.setup(t, policy)

			err := materializeHomeProjections(runtimeRoot, policy)
			var setupErr *SetupError
			if !errors.As(err, &setupErr) {
				t.Fatalf("error = %v, want SetupError", err)
			}
			if err.Error() != "sandbox: setup failed: runtime home projection failed" {
				t.Fatalf("error = %q, want stable path-free failure", err)
			}
			if strings.Contains(err.Error(), base) || strings.Contains(err.Error(), target) {
				t.Fatalf("error leaks fixture path: %v", err)
			}
		})
	}
}

func TestMaterializeHomeProjections_RejectsSymlinkRuntimeRootAndSourceDrift(t *testing.T) {
	t.Run("runtime root alias", func(t *testing.T) {
		base := t.TempDir()
		runtimeRoot := projectionRuntime(t, base)
		runtimeAlias := filepath.Join(base, "runtime-alias")
		if err := os.Symlink(runtimeRoot, runtimeAlias); err != nil {
			t.Fatalf("symlink runtime root: %v", err)
		}
		target := projectionDir(t, filepath.Join(base, "host", "target"))
		policy := projectionTestPolicy(runtimeRoot, HomeProjection{HostPath: target, RelativePath: "target"})
		if err := materializeHomeProjections(runtimeAlias, policy); err == nil {
			t.Fatal("materializer accepted a symlink runtime root")
		}
	})

	t.Run("source no longer has exact canonical name", func(t *testing.T) {
		base := t.TempDir()
		runtimeRoot := projectionRuntime(t, base)
		target := projectionDir(t, filepath.Join(base, "host", "target"))
		alias := filepath.Join(base, "host", "target-alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatalf("symlink source alias: %v", err)
		}
		policy := projectionTestPolicy(runtimeRoot, HomeProjection{HostPath: alias, RelativePath: "target"})
		if err := materializeHomeProjections(runtimeRoot, policy); err == nil {
			t.Fatal("materializer accepted a non-canonical source")
		}
	})
}

func TestMaterializeHomeProjections_RejectsOversizedPlanBeforeTouchingDisk(t *testing.T) {
	base := t.TempDir()
	runtimeRoot := projectionRuntime(t, base)
	target := projectionDir(t, filepath.Join(base, "host", "target"))
	projections := make([]HomeProjection, maxHomeProjections+1)
	for i := range projections {
		projections[i] = HomeProjection{HostPath: target, RelativePath: filepath.Join("p", strings.Repeat("x", i+1))}
	}
	policy := projectionTestPolicy(runtimeRoot, projections...)
	if err := materializeHomeProjections(runtimeRoot, policy); err == nil {
		t.Fatal("materializer accepted an oversized projection plan")
	}
	entries, err := os.ReadDir(policy.Home)
	if err != nil {
		t.Fatalf("read runtime home: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("oversized plan touched runtime home: %v", entries)
	}
}

func TestRemoveRuntimeRoot_RemovesPartialProjectionForestWithoutFollowingTargets(t *testing.T) {
	base := t.TempDir()
	runtimeRoot := projectionRuntime(t, base)
	one := projectionDir(t, filepath.Join(base, "host", "one"))
	two := projectionDir(t, filepath.Join(base, "host", "two"))
	sentinel := filepath.Join(one, "sentinel")
	if err := os.WriteFile(sentinel, []byte("host survives"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	policy := projectionTestPolicy(runtimeRoot,
		HomeProjection{HostPath: one, RelativePath: ".config/one"},
		HomeProjection{HostPath: two, RelativePath: ".local/two"},
	)
	if err := os.Mkdir(filepath.Join(policy.Home, ".local"), 0o700); err != nil {
		t.Fatalf("mkdir collision parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(policy.Home, ".local", "two"), []byte("collision"), 0o600); err != nil {
		t.Fatalf("write collision: %v", err)
	}

	if err := materializeHomeProjections(runtimeRoot, policy); err == nil {
		t.Fatal("materializer unexpectedly succeeded through collision")
	}
	if got, err := os.Readlink(filepath.Join(policy.Home, ".config", "one")); err != nil || got != one {
		t.Fatalf("first link was not created before partial failure: target=%q err=%v", got, err)
	}
	RemoveRuntimeRoot(runtimeRoot)
	if _, err := os.Stat(runtimeRoot); !os.IsNotExist(err) {
		t.Fatalf("runtime root remains after cleanup: %v", err)
	}
	// #nosec G304 -- sentinel is a trusted path inside the test-owned host fixture.
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("host sentinel removed by cleanup: %v", err)
	}
	if string(data) != "host survives" {
		t.Fatalf("host sentinel changed: %q", data)
	}
	RemoveRuntimeRoot(runtimeRoot)
}
