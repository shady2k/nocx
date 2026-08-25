package sandbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func projectionDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir projection fixture: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonicalize projection fixture: %v", err)
	}
	return canonical
}

func projectionRuntime(t *testing.T, base string) string {
	t.Helper()
	runtimeRoot := filepath.Join(base, "runtime")
	for _, path := range []string{runtimeRoot, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp")} {
		projectionDir(t, path)
	}
	return runtimeRoot
}

func projectionPaths(projections []HomeProjection) []string {
	out := make([]string, 0, len(projections))
	for _, projection := range projections {
		out = append(out, projection.HostPath+"|"+projection.RelativePath)
	}
	return out
}

func TestBuildPolicy_PlansHomeProjectionsFromEffectiveExplicitGrants(t *testing.T) {
	base := t.TempDir()
	hostHome := projectionDir(t, filepath.Join(base, "host-home"))
	runtimeRoot := projectionRuntime(t, base)
	workspace := projectionDir(t, filepath.Join(hostHome, "work", "project"))
	globalWritable := projectionDir(t, filepath.Join(hostHome, ".local", "share", "tool"))
	removedWritable := projectionDir(t, filepath.Join(hostHome, "removed-rw"))
	addedWritable := projectionDir(t, filepath.Join(hostHome, ".local", "state", "tool"))
	globalReadOnly := projectionDir(t, filepath.Join(hostHome, ".config", "tool"))
	removedReadOnly := projectionDir(t, filepath.Join(hostHome, "removed-ro"))
	addedReadOnly := projectionDir(t, filepath.Join(hostHome, ".cache", "tool"))

	policy, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalWritable: []string{globalWritable, removedWritable},
		RemoveWritable: []string{removedWritable},
		AddWritable:    []string{addedWritable},
		GlobalReadOnly: []string{globalReadOnly, removedReadOnly},
		RemoveReadOnly: []string{removedReadOnly},
		AddReadOnly:    []string{addedReadOnly},
	}, "/bin/sh", runtimeRoot, []string{"HOME=/ignored", "HOME=" + hostHome})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	want := []string{
		workspace + "|work/project",
		globalWritable + "|.local/share/tool",
		addedWritable + "|.local/state/tool",
		globalReadOnly + "|.config/tool",
		addedReadOnly + "|.cache/tool",
	}
	if got := projectionPaths(policy.HomeProjections); !reflect.DeepEqual(got, want) {
		t.Fatalf("HomeProjections = %v, want %v", got, want)
	}
}

func TestBuildPolicy_HomeProjectionCanonicalAliasesDedupeFirstWins(t *testing.T) {
	base := t.TempDir()
	hostHome := projectionDir(t, filepath.Join(base, "host-home"))
	runtimeRoot := projectionRuntime(t, base)
	workspace := projectionDir(t, filepath.Join(base, "outside-workspace"))
	target := projectionDir(t, filepath.Join(hostHome, ".config", "tool"))
	alias := filepath.Join(hostHome, "config-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}

	policy, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalReadOnly: []string{alias, target},
		AddReadOnly:    []string{target},
	}, "/bin/sh", runtimeRoot, []string{"HOME=" + hostHome})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	want := []string{target + "|.config/tool"}
	if got := projectionPaths(policy.HomeProjections); !reflect.DeepEqual(got, want) {
		t.Fatalf("HomeProjections = %v, want %v", got, want)
	}
}

func TestBuildPolicy_HomeProjectionExcludesAbsoluteOnlyAndDerivedRoots(t *testing.T) {
	base := t.TempDir()
	hostHome := projectionDir(t, filepath.Join(base, "host-home"))
	runtimeRoot := projectionRuntime(t, filepath.Join(hostHome, ".cache", "nocx"))
	workspace := projectionDir(t, filepath.Join(hostHome, "workspace"))
	outside := projectionDir(t, filepath.Join(base, "outside"))
	pathDir := projectionDir(t, filepath.Join(hostHome, "derived-path"))
	runtimeAncestor := projectionDir(t, filepath.Join(hostHome, ".cache"))
	insideRuntime := projectionDir(t, filepath.Join(runtimeRoot, "nested"))
	gitCommon := projectionDir(t, filepath.Join(hostHome, "repo", ".git"))
	writeLinkedWorktreeFixture(t, workspace, gitCommon, "projected-worktree", false)

	policy, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalReadOnly: []string{"/", hostHome, filepath.Dir(hostHome), outside, runtimeAncestor, runtimeRoot, insideRuntime},
	}, "/bin/sh", runtimeRoot, []string{"HOME=" + hostHome, "PATH=" + pathDir})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	want := []string{workspace + "|workspace"}
	if got := projectionPaths(policy.HomeProjections); !reflect.DeepEqual(got, want) {
		t.Fatalf("HomeProjections = %v, want only explicit workspace projection %v", got, want)
	}
}

func TestBuildPolicy_HomeProjectionRetainsNestedReadOnlyAndWritableMetadata(t *testing.T) {
	base := t.TempDir()
	hostHome := projectionDir(t, filepath.Join(base, "host-home"))
	runtimeRoot := projectionRuntime(t, base)
	workspace := projectionDir(t, filepath.Join(base, "outside-workspace"))
	readOnlyAncestor := projectionDir(t, filepath.Join(hostHome, ".config"))
	writableChild := projectionDir(t, filepath.Join(readOnlyAncestor, "tool", "state"))

	policy, err := BuildPolicy(Request{
		Workspace:   workspace,
		AddWritable: []string{writableChild},
		AddReadOnly: []string{readOnlyAncestor},
	}, "/bin/sh", runtimeRoot, []string{"HOME=" + hostHome})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	want := []string{
		writableChild + "|.config/tool/state",
		readOnlyAncestor + "|.config",
	}
	if got := projectionPaths(policy.HomeProjections); !reflect.DeepEqual(got, want) {
		t.Fatalf("HomeProjections = %v, want %v", got, want)
	}
}

func TestBuildPolicy_HostHomeFallbackAndFailure(t *testing.T) {
	base := t.TempDir()
	hostHome := projectionDir(t, filepath.Join(base, "host-home"))
	runtimeRoot := projectionRuntime(t, base)
	workspace := projectionDir(t, filepath.Join(hostHome, "workspace"))
	t.Setenv("HOME", hostHome)

	policy, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy fallback: %v", err)
	}
	if got := projectionPaths(policy.HomeProjections); !reflect.DeepEqual(got, []string{workspace + "|workspace"}) {
		t.Fatalf("fallback HomeProjections = %v", got)
	}

	_, err = BuildPolicy(Request{Workspace: workspace}, "/bin/sh", runtimeRoot, []string{"HOME=relative-secret-home"})
	var setupErr *SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("invalid present HOME error = %v, want SetupError", err)
	}
	if strings.Contains(err.Error(), "relative-secret-home") || strings.Contains(err.Error(), workspace) {
		t.Fatalf("setup error leaks a path: %v", err)
	}
}

func TestBuildPolicy_EmptyHomeProjectionsMarshalAsArray(t *testing.T) {
	base := t.TempDir()
	hostHome := projectionDir(t, filepath.Join(base, "host-home"))
	runtimeRoot := projectionRuntime(t, base)
	workspace := projectionDir(t, filepath.Join(base, "outside-workspace"))

	policy, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", runtimeRoot, []string{"HOME=" + hostHome})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if policy.HomeProjections == nil || len(policy.HomeProjections) != 0 {
		t.Fatalf("HomeProjections = %#v, want non-nil empty slice", policy.HomeProjections)
	}
	data, err := policy.Bytes()
	if err != nil {
		t.Fatalf("Policy.Bytes: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if got := string(document["homeProjections"]); got != "[]" {
		t.Fatalf("homeProjections JSON = %s, want []", got)
	}
}

func TestValidatePolicy_RejectsInvalidHomeProjections(t *testing.T) {
	base := t.TempDir()
	hostHome := projectionDir(t, filepath.Join(base, "host-home"))
	runtimeRoot := projectionRuntime(t, base)
	workspace := projectionDir(t, filepath.Join(hostHome, "workspace"))
	other := projectionDir(t, filepath.Join(hostHome, "other"))
	policy, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", runtimeRoot, []string{"HOME=" + hostHome})
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	valid := HomeProjection{HostPath: workspace, RelativePath: "workspace"}
	cases := []struct {
		name        string
		projections []HomeProjection
	}{
		{name: "nil", projections: nil},
		{name: "duplicate relative", projections: []HomeProjection{valid, valid}},
		{name: "absolute relative path", projections: []HomeProjection{{HostPath: workspace, RelativePath: "/workspace"}}},
		{name: "dot", projections: []HomeProjection{{HostPath: workspace, RelativePath: "."}}},
		{name: "parent traversal", projections: []HomeProjection{{HostPath: workspace, RelativePath: "a/../workspace"}}},
		{name: "unrepresented host root", projections: []HomeProjection{{HostPath: other, RelativePath: "other"}}},
	}
	oversized := make([]HomeProjection, 1+4*maxUserPaths+1)
	for i := range oversized {
		oversized[i] = HomeProjection{HostPath: workspace, RelativePath: filepath.Join("p", string(rune('a'+i%26)), strings.Repeat("x", i+1))}
	}
	cases = append(cases, struct {
		name        string
		projections []HomeProjection
	}{name: "oversized", projections: oversized})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *policy
			candidate.HomeProjections = tc.projections
			if err := ValidatePolicy(&candidate); err == nil {
				t.Fatal("ValidatePolicy accepted invalid home projections")
			}
		})
	}
}

func TestPlanHomeProjectionForest_SelectsMinimalTopmostLinks(t *testing.T) {
	projections := []HomeProjection{
		{HostPath: "/host/.config/tool/state", RelativePath: ".config/tool/state"},
		{HostPath: "/host/.local/share/tool", RelativePath: ".local/share/tool"},
		{HostPath: "/host/.config", RelativePath: ".config"},
		{HostPath: "/host/.local/state/tool", RelativePath: ".local/state/tool"},
	}
	links, err := planHomeProjectionForest(projections)
	if err != nil {
		t.Fatalf("planHomeProjectionForest: %v", err)
	}
	want := []homeProjectionLink{
		{HostPath: "/host/.config", RelativePath: ".config"},
		{HostPath: "/host/.local/share/tool", RelativePath: ".local/share/tool"},
		{HostPath: "/host/.local/state/tool", RelativePath: ".local/state/tool"},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("links = %#v, want %#v", links, want)
	}
}
