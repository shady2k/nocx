package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fixture builds a workspace, a runtime root (home+tmp) and an existing PATH
// dir, returning them for assertions.
func fixture(t *testing.T) (workspace, runtimeRoot, pathDir string) {
	t.Helper()
	base := t.TempDir()
	workspace = filepath.Join(base, "workspace")
	runtimeRoot = filepath.Join(base, "runtime")
	pathDir = filepath.Join(base, "pathdir")
	for _, d := range []string{workspace, runtimeRoot, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), pathDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return workspace, runtimeRoot, pathDir
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return canonical
}

func TestBuildPolicy_Roots(t *testing.T) {
	workspace, runtimeRoot, pathDir := fixture(t)
	shell := "/bin/sh"

	env := []string{"PATH=" + pathDir + string(os.PathListSeparator) +
		filepath.Join(runtimeRoot, "missing") + string(os.PathListSeparator) +
		"relative/dir" + string(os.PathListSeparator) + workspace}

	p, err := BuildPolicy(Request{Workspace: workspace}, shell, runtimeRoot, env)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	// Writable roots in the fixed tooltip order: workspace, home, tmp.
	home := canonicalPath(t, filepath.Join(runtimeRoot, "home"))
	tmp := canonicalPath(t, filepath.Join(runtimeRoot, "tmp"))
	wantRW := []string{canonicalPath(t, workspace), home, tmp}
	if len(p.WritableRoots) != len(wantRW) {
		t.Fatalf("WritableRoots = %v, want %v", p.WritableRoots, wantRW)
	}
	for i, w := range wantRW {
		if p.WritableRoots[i] != w {
			t.Fatalf("WritableRoots[%d] = %q, want %q (full: %v)", i, p.WritableRoots[i], w, p.WritableRoots)
		}
	}

	// Read-only roots: documented system set, canonical execution directories,
	// and existing absolute PATH dirs. The shell itself is a read-only file.
	roSet := make(map[string]bool, len(p.ReadOnlyRoots))
	for _, root := range p.ReadOnlyRoots {
		if roSet[root] {
			t.Errorf("duplicate read-only root %q", root)
		}
		roSet[root] = true
	}
	for _, root := range systemReadOnlyRoots() {
		if _, stErr := os.Stat(root); stErr != nil {
			continue
		}
		canonRoot, rErr := filepath.EvalSymlinks(root)
		if rErr != nil {
			t.Fatalf("EvalSymlinks(%s): %v", root, rErr)
		}
		if !roSet[canonRoot] {
			t.Errorf("read-only roots missing system root %q (canonical %q); got %v", root, canonRoot, p.ReadOnlyRoots)
		}
	}
	shellCanon, err := filepath.EvalSymlinks(shell)
	if err != nil {
		t.Fatalf("EvalSymlinks(shell): %v", err)
	}
	if len(p.ReadOnlyFiles) != 1 || p.ReadOnlyFiles[0] != shellCanon {
		t.Errorf("ReadOnlyFiles = %v, want canonical shell %q", p.ReadOnlyFiles, shellCanon)
	}
	pathCanon, err := filepath.EvalSymlinks(pathDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(pathDir): %v", err)
	}
	if !roSet[pathCanon] {
		t.Errorf("read-only roots missing PATH dir %q", pathCanon)
	}
	for _, forbidden := range []string{canonicalPath(t, workspace), filepath.Join(runtimeRoot, "missing")} {
		if roSet[forbidden] {
			t.Errorf("read-only roots must not contain %q (workspace is RW; missing skipped)", forbidden)
		}
	}
	if len(p.WritableFiles) == 0 {
		t.Error("policy must grant the interactive device-file allowlist")
	}

	// The policy must validate cleanly end to end.
	if err := ValidatePolicy(p); err != nil {
		t.Fatalf("ValidatePolicy: %v", err)
	}
}

func TestBuildPolicy_CanonicalizesWorkspace(t *testing.T) {
	workspace, runtimeRoot, _ := fixture(t)
	link := filepath.Join(filepath.Dir(workspace), "workspace-link")
	if err := os.Symlink(workspace, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	p, err := BuildPolicy(Request{Workspace: link}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy via symlink: %v", err)
	}
	if p.Workspace != canonicalPath(t, workspace) {
		t.Fatalf("Workspace = %q, want canonical %q", p.Workspace, canonicalPath(t, workspace))
	}
}

func TestBuildPolicy_CanonicalizesRuntimeDirectories(t *testing.T) {
	workspace, runtimeRoot, _ := fixture(t)
	link := filepath.Join(filepath.Dir(runtimeRoot), "runtime-link")
	if err := os.Symlink(runtimeRoot, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	p, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", link, nil)
	if err != nil {
		t.Fatalf("BuildPolicy via runtime symlink: %v", err)
	}
	wantHome := canonicalPath(t, filepath.Join(runtimeRoot, "home"))
	wantTmp := canonicalPath(t, filepath.Join(runtimeRoot, "tmp"))
	if p.Home != wantHome || p.Tmp != wantTmp {
		t.Fatalf("runtime dirs = (%q, %q), want (%q, %q)", p.Home, p.Tmp, wantHome, wantTmp)
	}
	if !slices.Contains(p.WritableRoots, wantHome) || !slices.Contains(p.WritableRoots, wantTmp) {
		t.Fatalf("writable roots omit canonical runtime dirs: %v", p.WritableRoots)
	}
}

func TestBuildPolicy_RejectsInvalidWorkspace(t *testing.T) {
	_, runtimeRoot, _ := fixture(t)
	shell := "/bin/sh"
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	brokenLink := filepath.Join(t.TempDir(), "broken")
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), brokenLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cases := []struct {
		name      string
		workspace string
	}{
		{"empty", ""},
		{"nul", "ws\x00dir"},
		{"existing relative directory", "."},
		{"nonexistent", filepath.Join(t.TempDir(), "nope")},
		{"file", notDir},
		{"broken symlink", brokenLink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildPolicy(Request{Workspace: tc.workspace}, shell, runtimeRoot, nil)
			if !errors.Is(err, ErrInvalidPermissions) {
				t.Fatalf("err = %v, want ErrInvalidPermissions", err)
			}
		})
	}
}

func TestBuildPolicy_RuntimeRootErrorsAreSetupFailures(t *testing.T) {
	workspace, runtimeRoot, _ := fixture(t)
	var se *SetupError
	// Runtime root exists but home/ is missing.
	empty := filepath.Join(t.TempDir(), "empty-runtime")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", empty, nil)
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want SetupError", err)
	}

	// Shell path that cannot resolve.
	_, err = BuildPolicy(Request{Workspace: workspace}, filepath.Join(t.TempDir(), "no-shell"), runtimeRoot, nil)
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want SetupError for bad shell", err)
	}
}

func TestGitCommonDir(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "worktree")
	common := filepath.Join(base, "repo", ".git")
	for _, d := range []string{workspace, common} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	t.Run("relative gitdir line", func(t *testing.T) {
		writeLinkedWorktreeFixture(t, workspace, common, "relative", true)
		got, ok := gitCommonDir(workspace)
		if want := canonicalPath(t, common); !ok || got != want {
			t.Fatalf("gitCommonDir = %q, %v; want %q, true", got, ok, want)
		}
	})

	t.Run("absolute gitdir line", func(t *testing.T) {
		writeLinkedWorktreeFixture(t, workspace, common, "absolute", false)
		got, ok := gitCommonDir(workspace)
		if want := canonicalPath(t, common); !ok || got != want {
			t.Fatalf("gitCommonDir = %q, %v; want %q, true", got, ok, want)
		}
	})

	t.Run("malformed yields no root and no error", func(t *testing.T) {
		bad := filepath.Join(base, "bad-worktree")
		if err := os.MkdirAll(bad, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for name, content := range map[string]string{
			"not a gitdir line":  "gitdir-broken\n",
			"empty target":       "gitdir:   \n",
			"nonexistent target": "gitdir: ../nope\n",
			"target is a file":   "gitdir: ../file-target\n",
			"nul target":         "gitdir: a\x00b\n",
		} {
			if name == "target is a file" {
				if err := os.WriteFile(filepath.Join(base, "file-target"), []byte("x"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if err := os.WriteFile(filepath.Join(bad, ".git"), []byte(content), 0o600); err != nil {
				t.Fatalf("write .git: %v", err)
			}
			got, ok := gitCommonDir(bad)
			if ok {
				t.Errorf("case %q: got root %q, want none", name, got)
			}
		}
	})

	t.Run("untrusted gitdir cannot widen the sandbox", func(t *testing.T) {
		for name, arrange := range map[string]func(string){
			"target outside common worktrees": func(ws string) {
				target := filepath.Join(base, "outside")
				if err := os.MkdirAll(target, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "gitdir"), []byte(filepath.Join(ws, ".git")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: "+target+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			"missing reciprocal gitdir": func(ws string) {
				target := filepath.Join(common, "worktrees", "missing-backlink")
				if err := os.MkdirAll(target, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: "+target+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			"mismatched reciprocal gitdir": func(ws string) {
				target := filepath.Join(common, "worktrees", "wrong-backlink")
				if err := os.MkdirAll(target, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "gitdir"), []byte(filepath.Join(base, "someone-else", ".git")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: "+target+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(name, func(t *testing.T) {
				ws := filepath.Join(base, strings.ReplaceAll(name, " ", "-"))
				if err := os.MkdirAll(ws, 0o750); err != nil {
					t.Fatal(err)
				}
				arrange(ws)
				if got, ok := gitCommonDir(ws); ok {
					t.Errorf("untrusted .git widened sandbox to %q", got)
				}
			})
		}
	})

	t.Run("symlinked gitfile cannot widen the sandbox", func(t *testing.T) {
		ws := filepath.Join(base, "symlinked-gitfile")
		if err := os.MkdirAll(ws, 0o750); err != nil {
			t.Fatal(err)
		}
		targetFile := filepath.Join(base, "attacker-gitfile")
		if err := os.WriteFile(targetFile, []byte("gitdir: "+filepath.Join(common, "worktrees", "relative")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(targetFile, filepath.Join(ws, ".git")); err != nil {
			t.Fatal(err)
		}
		if got, ok := gitCommonDir(ws); ok {
			t.Errorf("symlinked .git widened sandbox to %q", got)
		}
	})

	t.Run("regular repository has no common dir", func(t *testing.T) {
		reg := filepath.Join(base, "regular")
		if err := os.MkdirAll(filepath.Join(reg, ".git"), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if got, ok := gitCommonDir(reg); ok {
			t.Errorf("regular repo: unexpected common dir %q", got)
		}
	})

	t.Run("missing .git", func(t *testing.T) {
		clean := filepath.Join(base, "clean-worktree")
		if err := os.MkdirAll(clean, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if got, ok := gitCommonDir(clean); ok {
			t.Errorf("missing .git file: unexpected common dir %q", got)
		}
	})
}

func TestBuildPolicy_GitCommonDirAppearsInWritableRoots(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "worktree")
	common := filepath.Join(base, "repo", ".git")
	for _, d := range []string{workspace, common, filepath.Join(base, "runtime", "home"), filepath.Join(base, "runtime", "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	writeLinkedWorktreeFixture(t, workspace, common, "policy", false)
	p, err := BuildPolicy(Request{Workspace: workspace}, "/bin/sh", filepath.Join(base, "runtime"), nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	want := []string{
		canonicalPath(t, workspace),
		canonicalPath(t, common),
		canonicalPath(t, filepath.Join(base, "runtime", "home")),
		canonicalPath(t, filepath.Join(base, "runtime", "tmp")),
	}
	if len(p.WritableRoots) != len(want) {
		t.Fatalf("WritableRoots = %v, want %v", p.WritableRoots, want)
	}
	for i, w := range want {
		if p.WritableRoots[i] != w {
			t.Fatalf("WritableRoots[%d] = %q, want %q", i, p.WritableRoots[i], w)
		}
	}
}

func writeLinkedWorktreeFixture(t *testing.T, workspace, common, name string, relative bool) {
	t.Helper()
	target := filepath.Join(common, "worktrees", name)
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("mkdir linked worktree metadata: %v", err)
	}
	dotGit := filepath.Join(workspace, ".git")
	targetRef := target
	if relative {
		var err error
		targetRef, err = filepath.Rel(workspace, target)
		if err != nil {
			t.Fatalf("relative gitdir: %v", err)
		}
	}
	if err := os.WriteFile(dotGit, []byte("gitdir: "+targetRef+"\n"), 0o600); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "gitdir"), []byte(dotGit+"\n"), 0o600); err != nil {
		t.Fatalf("write reciprocal gitdir: %v", err)
	}
}

func TestValidatePolicy_RejectsUnenforceableDocuments(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	rt := filepath.Join(base, "rt")
	for _, d := range []string{ws, filepath.Join(rt, "home"), filepath.Join(rt, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	p, err := BuildPolicy(Request{Workspace: ws}, "/bin/sh", rt, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	t.Run("valid document", func(t *testing.T) {
		if err := ValidatePolicy(p); err != nil {
			t.Fatalf("ValidatePolicy: %v", err)
		}
	})

	t.Run("conflicting duplicate permissions", func(t *testing.T) {
		dup := *p
		dup.ReadOnlyRoots = append(append([]string{}, p.ReadOnlyRoots...), p.WritableRoots[0])
		if err := ValidatePolicy(&dup); err == nil {
			t.Fatal("expected conflict rejection")
		}
	})

	t.Run("read-only root below writable root", func(t *testing.T) {
		q := *p
		q.ReadOnlyRoots = append(append([]string{}, p.ReadOnlyRoots...), filepath.Join(p.WritableRoots[0], "child"))
		if err := ValidatePolicy(&q); err == nil {
			t.Fatal("expected read-only-below-writable rejection")
		}
	})

	t.Run("nul, empty, relative, non-absolute", func(t *testing.T) {
		for _, bad := range []string{"", "rel/path", "a\x00b", "workspace\x00x"} {
			q := *p
			q.WritableRoots = append([]string{bad}, p.WritableRoots...)
			if err := ValidatePolicy(&q); err == nil {
				t.Errorf("expected rejection for %q", bad)
			}
		}
	})

	t.Run("root count bound", func(t *testing.T) {
		q := *p
		for i := 0; i <= maxRoots; i++ {
			q.ReadOnlyRoots = append(q.ReadOnlyRoots, filepath.Join(base, "r", string(rune('a'+i%26)), string(rune('0'+i%10))))
		}
		if err := ValidatePolicy(&q); err == nil {
			t.Fatal("expected root-count rejection")
		}
	})

	t.Run("size bound", func(t *testing.T) {
		q := *p
		long := filepath.Join(base, strings.Repeat("x", maxPolicyBytes))
		q.ReadOnlyRoots = append(q.ReadOnlyRoots, long)
		if _, err := q.Bytes(); err == nil {
			t.Fatal("expected size rejection")
		}
	})
}

func TestRuntimeRoot(t *testing.T) {
	cacheDir := t.TempDir()
	root, err := NewRuntimeRoot(cacheDir)
	if err != nil {
		t.Fatalf("NewRuntimeRoot: %v", err)
	}
	parent := filepath.Dir(root)
	if filepath.Base(parent) != "sandbox-sessions" {
		t.Fatalf("runtime root parent = %q, want sandbox-sessions", parent)
	}
	for _, d := range []string{root, filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("mode of %s = %o, want 0700", d, fi.Mode().Perm())
		}
	}
	RemoveRuntimeRoot(root)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("runtime root still exists after RemoveRuntimeRoot (err=%v)", err)
	}
	// Idempotent on a removed root.
	RemoveRuntimeRoot(root)
}

func TestPathEntries(t *testing.T) {
	env := []string{"FOO=1", "PATH=/a:/b", "PATH=/c"}
	got := pathEntries(env)
	want := []string{"/c"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("pathEntries = %v, want %v", got, want)
	}
	if pathEntries([]string{"NO_PATH=1"}) != nil {
		t.Fatal("expected nil for env without PATH")
	}
}

func TestSandboxEnv(t *testing.T) {
	home := "/rt/home"
	tmp := "/rt/tmp"
	env := []string{"HOME=/real/home", "PATH=/usr/bin:/bin", "TMPDIR=/real/tmp", "KEEP=1"}
	got := sandboxEnv(env, home, tmp)
	joined := strings.Join(got, "\n")
	for _, wantKV := range []string{
		"HOME=" + home,
		"XDG_DATA_HOME=" + home + "/.local/share",
		"XDG_CONFIG_HOME=" + home + "/.config",
		"XDG_CACHE_HOME=" + home + "/.cache",
		"XDG_STATE_HOME=" + home + "/.local/state",
		"TMPDIR=" + tmp,
		"TMP=" + tmp,
		"TEMP=" + tmp,
		"NOCX_SANDBOX=filesystem",
		"PATH=/usr/bin:/bin",
		"KEEP=1",
	} {
		if !strings.Contains(joined, wantKV) {
			t.Errorf("sandboxEnv missing %q (got %v)", wantKV, got)
		}
	}
	// No duplicate keys: HOME/TMPDIR appear exactly once.
	for _, key := range []string{"HOME", "TMPDIR", "NOCX_SANDBOX"} {
		count := 0
		for _, kv := range got {
			if strings.HasPrefix(kv, key+"=") {
				count++
			}
		}
		if count != 1 {
			t.Errorf("key %s appears %d times in %v", key, count, got)
		}
	}
}

func TestBuildPolicy_ComposesRootsInSharedOrder(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "worktree")
	common := filepath.Join(base, "repo", ".git")
	runtimeRoot := filepath.Join(base, "runtime")
	globalKeep := filepath.Join(base, "global-keep")
	globalDrop := filepath.Join(base, "global-drop")
	add := filepath.Join(base, "add")
	for _, d := range []string{workspace, common, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), globalKeep, globalDrop, add} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	writeLinkedWorktreeFixture(t, workspace, common, "order", false)

	p, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalWritable: []string{globalDrop, globalKeep},
		AddWritable:    []string{add},
		RemoveWritable: []string{globalDrop},
	}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	want := []string{
		canonicalPath(t, workspace),
		canonicalPath(t, common),
		canonicalPath(t, globalKeep),
		canonicalPath(t, add),
		canonicalPath(t, filepath.Join(runtimeRoot, "home")),
		canonicalPath(t, filepath.Join(runtimeRoot, "tmp")),
	}
	if len(p.WritableRoots) != len(want) {
		t.Fatalf("WritableRoots = %v, want %v", p.WritableRoots, want)
	}
	for i := range want {
		if p.WritableRoots[i] != want[i] {
			t.Fatalf("WritableRoots[%d] = %q, want %q (full %v)", i, p.WritableRoots[i], want[i], p.WritableRoots)
		}
	}
}

func TestBuildPolicy_UnmatchedRemovalFails(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	other := filepath.Join(base, "other")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), other} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	_, err := BuildPolicy(Request{Workspace: workspace, RemoveWritable: []string{other}}, "/bin/sh", runtimeRoot, nil)
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions for unmatched removal", err)
	}
}

func TestBuildPolicy_AddRemoveConflictFails(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	dir := filepath.Join(base, "dir")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), dir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	_, err := BuildPolicy(Request{Workspace: workspace, AddWritable: []string{dir}, RemoveWritable: []string{dir}}, "/bin/sh", runtimeRoot, nil)
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions for add/remove conflict", err)
	}
}

func TestBuildPolicy_RemoveMandatoryRootFails(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	_, err := BuildPolicy(Request{Workspace: workspace, RemoveWritable: []string{workspace}}, "/bin/sh", runtimeRoot, nil)
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions for removing workspace", err)
	}
}

func TestBuildPolicy_CanonicalizesAddGlobalRemoveSymlinks(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	realGlobal := filepath.Join(base, "real-global")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), realGlobal} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	linkGlobal := filepath.Join(base, "link-global")
	if err := os.Symlink(realGlobal, linkGlobal); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// Global entered via symlink, removed via the canonical real path — an
	// exact canonical match must remove it.
	p, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalWritable: []string{linkGlobal},
		RemoveWritable: []string{realGlobal},
	}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	for _, w := range p.WritableRoots {
		if w == canonicalPath(t, realGlobal) {
			t.Fatalf("removed global %q still writable", realGlobal)
		}
	}
}

func TestBuildPolicy_RejectsProtectedAdd(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// "/" is an ancestor of every documented system root and always exists.
	_, err := BuildPolicy(Request{Workspace: workspace, AddWritable: []string{"/"}}, "/bin/sh", runtimeRoot, nil)
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions for protected add", err)
	}
}

func TestBuildPolicy_RejectsProtectedGlobal(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	_, err := BuildPolicy(Request{Workspace: workspace, GlobalWritable: []string{"/"}}, "/bin/sh", runtimeRoot, nil)
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want SetupError for protected global", err)
	}
}

func TestBuildPolicy_RejectsProtectedWorkspace(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	for _, d := range []string{filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// "/" is an ancestor of every documented system root. Treating it as the
	// workspace would turn the sandbox into an ordinary process with a marker.
	_, err := BuildPolicy(Request{Workspace: "/"}, "/bin/sh", runtimeRoot, nil)
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions for protected workspace", err)
	}
}

func TestWritableRootIsProtected(t *testing.T) {
	systemRoots := []string{"/usr", "/usr/local/lib"}
	cases := []struct {
		candidate string
		want      bool
	}{
		{"/", true},
		{"/usr", true},
		{"/usr/local", true},
		{"/usr/local/lib", true},
		{"/usr/local/lib/pkg", false},
		{"/usr/share", false},
		{"/usrlocal", false},
		{"/home/usr", false},
	}
	for _, tc := range cases {
		if got := writableRootIsProtected(tc.candidate, systemRoots); got != tc.want {
			t.Errorf("writableRootIsProtected(%q) = %v, want %v", tc.candidate, got, tc.want)
		}
	}
}

func TestBuildPolicy_WritableRootsAreCopies(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	global := filepath.Join(base, "global")
	add := filepath.Join(base, "add")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), global, add} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	req := Request{Workspace: workspace, GlobalWritable: []string{global}, AddWritable: []string{add}}
	p, err := BuildPolicy(req, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	before := append([]string(nil), p.WritableRoots...)

	// Mutating the caller's slices must never change the installed policy.
	req.GlobalWritable[0] = "/elsewhere"
	req.AddWritable[0] = "/nowhere"
	req.RemoveWritable = append(req.RemoveWritable, "/x")

	if len(before) != len(p.WritableRoots) {
		t.Fatalf("policy changed length after input mutation")
	}
	for i := range before {
		if before[i] != p.WritableRoots[i] {
			t.Fatalf("policy WritableRoots[%d] changed from %q to %q after input mutation", i, before[i], p.WritableRoots[i])
		}
	}
}

func TestBuildPolicy_ComposedPolicyWithinBounds(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	global := filepath.Join(base, "global")
	add := filepath.Join(base, "add")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), global, add} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	p, err := BuildPolicy(Request{Workspace: workspace, GlobalWritable: []string{global}, AddWritable: []string{add}}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if validationErr := ValidatePolicy(p); validationErr != nil {
		t.Fatalf("composed policy fails validation: %v", validationErr)
	}
	b, err := p.Bytes()
	if err != nil {
		t.Fatalf("composed policy serialization: %v", err)
	}
	if len(b) > maxPolicyBytes {
		t.Fatalf("composed policy exceeds size bound: %d > %d", len(b), maxPolicyBytes)
	}
}

func TestBuildPolicy_RejectsOversizedLists(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	many := make([]string, 0, maxUserPaths+1)
	for i := 0; i <= maxUserPaths; i++ {
		many = append(many, filepath.Join(base, "p", strconv.Itoa(i)))
	}

	build := func(mut func(*Request)) error {
		req := Request{Workspace: workspace}
		mut(&req)
		_, err := BuildPolicy(req, "/bin/sh", runtimeRoot, nil)
		return err
	}

	setupCases := map[string]func(*Request){
		"global writable":  func(r *Request) { r.GlobalWritable = many },
		"global read-only": func(r *Request) { r.GlobalReadOnly = many },
	}
	for name, mut := range setupCases {
		t.Run(name+" overflow is SetupError", func(t *testing.T) {
			var se *SetupError
			if err := build(mut); !errors.As(err, &se) {
				t.Fatalf("err = %v, want SetupError for oversized %s list", err, name)
			}
		})
	}

	requestCases := map[string]func(*Request){
		"writable addition":  func(r *Request) { r.AddWritable = many },
		"writable removal":   func(r *Request) { r.RemoveWritable = many },
		"read-only addition": func(r *Request) { r.AddReadOnly = many },
		"read-only removal":  func(r *Request) { r.RemoveReadOnly = many },
	}
	for name, mut := range requestCases {
		t.Run(name+" overflow is ValidationError", func(t *testing.T) {
			if err := build(mut); !errors.Is(err, ErrInvalidPermissions) {
				t.Fatalf("err = %v, want ErrInvalidPermissions for oversized %s list", err, name)
			}
		})
	}
}

func TestBuildPolicy_ComposesReadOnlyRoots(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	roDrop := filepath.Join(base, "ro-drop")
	roKeep := filepath.Join(base, "ro-keep")
	roAdd := filepath.Join(base, "ro-add")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), roDrop, roKeep, roAdd} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	p, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalReadOnly: []string{roDrop, roKeep},
		AddReadOnly:    []string{roAdd},
		RemoveReadOnly: []string{roDrop},
	}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	// User read-only roots join the full ReadOnlyRoots: baseline minus exact
	// removals first, then additions, in deterministic order.
	keep := canonicalPath(t, roKeep)
	add := canonicalPath(t, roAdd)
	drop := canonicalPath(t, roDrop)
	ki, ai := -1, -1
	for i, root := range p.ReadOnlyRoots {
		if root == keep {
			ki = i
		}
		if root == add {
			ai = i
		}
		if root == drop {
			t.Fatalf("removed read-only baseline %q still present in %v", drop, p.ReadOnlyRoots)
		}
	}
	if ki < 0 || ai < 0 {
		t.Fatalf("read-only roots missing keep=%d add=%d (got %v)", ki, ai, p.ReadOnlyRoots)
	}
	if ki > ai {
		t.Errorf("read-only order wrong: baseline %d after addition %d", ki, ai)
	}
}

func TestBuildPolicy_ReadOnlyRemovalMatchesOnlyReadOnlyBaseline(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	writable := filepath.Join(base, "writable")
	other := filepath.Join(base, "other")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), writable, other} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// A read-only removal must match a GlobalReadOnly entry, not a writable
	// baseline entry nor an arbitrary path.
	for name, req := range map[string]Request{
		"unmatched":            {Workspace: workspace, RemoveReadOnly: []string{other}},
		"writable-baseline":    {Workspace: workspace, GlobalWritable: []string{writable}, RemoveReadOnly: []string{writable}},
		"collides-with-add-ro": {Workspace: workspace, AddReadOnly: []string{other}, RemoveReadOnly: []string{other}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildPolicy(req, "/bin/sh", runtimeRoot, nil)
			if !errors.Is(err, ErrInvalidPermissions) {
				t.Fatalf("err = %v, want ErrInvalidPermissions", err)
			}
		})
	}
}

func TestBuildPolicy_ClassUpgradeDowngrade(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	dir := filepath.Join(base, "dir")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), dir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	canon := canonicalPath(t, dir)

	t.Run("writable-to-read-only", func(t *testing.T) {
		p, err := BuildPolicy(Request{
			Workspace:      workspace,
			GlobalWritable: []string{dir},
			RemoveWritable: []string{dir},
			AddReadOnly:    []string{dir},
		}, "/bin/sh", runtimeRoot, nil)
		if err != nil {
			t.Fatalf("BuildPolicy: %v", err)
		}
		if !slices.Contains(p.ReadOnlyRoots, canon) {
			t.Errorf("upgraded path %q missing from read-only roots", canon)
		}
		for _, root := range p.WritableRoots {
			if root == canon {
				t.Errorf("downgraded path %q still writable", canon)
			}
		}
	})

	t.Run("read-only-to-writable", func(t *testing.T) {
		p, err := BuildPolicy(Request{
			Workspace:      workspace,
			GlobalReadOnly: []string{dir},
			RemoveReadOnly: []string{dir},
			AddWritable:    []string{dir},
		}, "/bin/sh", runtimeRoot, nil)
		if err != nil {
			t.Fatalf("BuildPolicy: %v", err)
		}
		if !slices.Contains(p.WritableRoots, canon) {
			t.Errorf("upgraded path %q missing from writable roots", canon)
		}
		for _, root := range p.ReadOnlyRoots {
			if root == canon {
				t.Errorf("downgraded path %q still read-only", canon)
			}
		}
	})
}

func TestBuildPolicy_CrossClassConflictProvenance(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	home := filepath.Join(runtimeRoot, "home")
	parent := filepath.Join(base, "parent")
	child := filepath.Join(parent, "child")
	for _, d := range []string{workspace, home, filepath.Join(runtimeRoot, "tmp"), parent, child, filepath.Join(workspace, "sub"), filepath.Join(home, "sub")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// Request-caused conflicts are ValidationError; baseline-only conflicts
	// are SetupError.
	requestCases := map[string]Request{
		"add-ro-equals-workspace":   {Workspace: workspace, AddReadOnly: []string{workspace}},
		"add-ro-below-workspace":    {Workspace: workspace, AddReadOnly: []string{filepath.Join(workspace, "sub")}},
		"add-ro-below-global-rw":    {Workspace: workspace, GlobalWritable: []string{parent}, AddReadOnly: []string{child}},
		"add-ro-below-runtime-home": {Workspace: workspace, AddReadOnly: []string{filepath.Join(home, "sub")}},
		"add-ro-equals-add-rw":      {Workspace: workspace, AddWritable: []string{parent}, AddReadOnly: []string{parent}},
		"baseline-ro-vs-add-rw":     {Workspace: workspace, GlobalReadOnly: []string{parent}, AddWritable: []string{parent}},
	}
	for name, req := range requestCases {
		t.Run(name, func(t *testing.T) {
			_, err := BuildPolicy(req, "/bin/sh", runtimeRoot, nil)
			if !errors.Is(err, ErrInvalidPermissions) {
				t.Fatalf("err = %v, want ErrInvalidPermissions", err)
			}
		})
	}

	baselineCases := map[string]Request{
		"ro-equals-workspace":   {Workspace: workspace, GlobalReadOnly: []string{workspace}},
		"ro-below-workspace":    {Workspace: workspace, GlobalReadOnly: []string{filepath.Join(workspace, "sub")}},
		"ro-equals-global-rw":   {Workspace: workspace, GlobalWritable: []string{parent}, GlobalReadOnly: []string{parent}},
		"ro-below-global-rw":    {Workspace: workspace, GlobalWritable: []string{parent}, GlobalReadOnly: []string{child}},
		"ro-below-runtime-home": {Workspace: workspace, GlobalReadOnly: []string{filepath.Join(home, "sub")}},
	}
	for name, req := range baselineCases {
		t.Run(name, func(t *testing.T) {
			_, err := BuildPolicy(req, "/bin/sh", runtimeRoot, nil)
			var se *SetupError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want SetupError", err)
			}
		})
	}
}

func TestBuildPolicy_AllowsWritableBelowReadOnly(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	ancestor := filepath.Join(base, "ro-ancestor")
	child := filepath.Join(ancestor, "rw-child")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), ancestor, child} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	p, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalReadOnly: []string{ancestor},
		AddWritable:    []string{child},
	}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	if !slices.Contains(p.ReadOnlyRoots, canonicalPath(t, ancestor)) {
		t.Errorf("read-only ancestor %q missing from read-only roots", ancestor)
	}
	if !slices.Contains(p.WritableRoots, canonicalPath(t, child)) {
		t.Errorf("writable child %q missing from writable roots", child)
	}
	if err := ValidatePolicy(p); err != nil {
		t.Fatalf("ValidatePolicy on RW-below-RO policy: %v", err)
	}
}

func TestBuildPolicy_ReadOnlyAddRemoveCollisionFails(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	dir := filepath.Join(base, "dir")
	for _, d := range []string{workspace, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "tmp"), dir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	_, err := BuildPolicy(Request{Workspace: workspace, AddReadOnly: []string{dir}, RemoveReadOnly: []string{dir}}, "/bin/sh", runtimeRoot, nil)
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions for read-only add/remove conflict", err)
	}
}

// TestPathWithin_LexicalFastPath verifies that the lexical fast path still
// resolves the standard cases correctly.
func TestPathWithin_LexicalFastPath(t *testing.T) {
	cases := []struct {
		root, path string
		want       bool
	}{
		{"/usr", "/usr", true},
		{"/usr", "/usr/bin", true},
		{"/usr", "/usr/bin/gcc", true},
		{"/usr", "/usrlocal", false},
		{"/usr", "/home", false},
		{"/usr", "/", false},
		{"/", "/usr", true},
		{"/", "/", true},
	}
	for _, tc := range cases {
		if got := pathWithin(tc.root, tc.path); got != tc.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
		}
	}
}

// TestPathWithin_FilesystemIdentity verifies that pathWithin detects
// filesystem aliases (symlinks) that lexical comparison misses.
func TestPathWithin_FilesystemIdentity(t *testing.T) {
	base := t.TempDir()

	// Create a real directory and a symlink alias.
	realDir := filepath.Join(base, "real")
	aliasDir := filepath.Join(base, "alias")
	subDir := filepath.Join(aliasDir, "sub")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	// Lexical: /base/real does NOT contain /base/alias/sub (different prefix).
	// Filesystem: /base/alias → /base/real, so /base/alias/sub IS within /base/real.
	if !pathWithin(realDir, subDir) {
		t.Errorf("pathWithin(%q, %q): filesystem identity should detect alias, got false", realDir, subDir)
	}

	// Self-check: the symlink itself is the same file.
	if !pathWithin(realDir, aliasDir) {
		t.Errorf("pathWithin(%q, %q): symlink target should be detected as same file", realDir, aliasDir)
	}

	// Unrelated path should still be false.
	other := filepath.Join(base, "other")
	if err := os.MkdirAll(other, 0o750); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}
	if pathWithin(realDir, other) {
		t.Errorf("pathWithin(%q, %q): unrelated path should be false", realDir, other)
	}
}

// TestPathWithin_CaseInsensitiveAlias detects case-insensitive filesystems
// (macOS APFS default) and verifies that pathWithin catches case aliases.
// On case-sensitive filesystems the test is skipped loudly.
func TestPathWithin_CaseInsensitiveAlias(t *testing.T) {
	dir := t.TempDir()
	lower := filepath.Join(dir, "casedir")
	upper := filepath.Join(dir, "CASEDIR")
	if err := os.MkdirAll(lower, 0o750); err != nil {
		t.Fatalf("mkdir lower: %v", err)
	}
	li, errL := os.Stat(lower)
	ui, errU := os.Stat(upper)
	if errL != nil || errU != nil || !os.SameFile(li, ui) {
		t.Skip("skipping case-insensitive alias test: filesystem is case-sensitive")
	}

	sub := filepath.Join(upper, "sub")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	// Lexical: /tmp/.../casedir does NOT contain /tmp/.../CASEDIR/sub.
	// Filesystem identity: CASEDIR is the same file as casedir.
	if !pathWithin(lower, sub) {
		t.Errorf("pathWithin(%q, %q): case-insensitive alias should be detected, got false", lower, sub)
	}
}

// TestWritableRootIsProtected_FilesystemIdentity verifies that the protected
// writable check catches filesystem aliases that would bypass a lexical check.
func TestWritableRootIsProtected_FilesystemIdentity(t *testing.T) {
	base := t.TempDir()

	// System root: /base/sys
	sysRoot := filepath.Join(base, "sys")
	if err := os.MkdirAll(sysRoot, 0o750); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	systemRoots := []string{sysRoot}

	// Alias candidate: symlink to sysRoot.
	alias := filepath.Join(base, "sys-alias")
	if err := os.Symlink(sysRoot, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Lexical: /base/sys is not an ancestor of /base/sys-alias.
	// Filesystem: /base/sys-alias IS the same file as /base/sys.
	if !writableRootIsProtected(alias, systemRoots) {
		t.Errorf("writableRootIsProtected(%q, %v): alias should be protected", alias, systemRoots)
	}

	// Non-alias should not be protected.
	other := filepath.Join(base, "other")
	if err := os.MkdirAll(other, 0o750); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}
	if writableRootIsProtected(other, systemRoots) {
		t.Errorf("writableRootIsProtected(%q, %v): unrelated path should not be protected", other, systemRoots)
	}
}

// TestBuildPolicy_FinalErrorsAreSetupError verifies that normalize/validate
// failures from the tail of BuildPolicy are typed as SetupError.
func TestBuildPolicy_FinalErrorsAreSetupError(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")
	for _, d := range []string{workspace, home, tmp} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// A policy that triggers the root-count bound inside normalize.
	t.Run("root count bound", func(t *testing.T) {
		// Create enough directories so that canonicalPaths succeeds but
		// normalize/ValidatePolicy hits the maxRoots bound.
		bloatDir := filepath.Join(base, "bloat")
		var manyPaths []string
		for i := 0; i <= maxRoots; i++ {
			p := filepath.Join(bloatDir, "r", string(rune('a'+i%26)), string(rune('0'+i%10)))
			if err := os.MkdirAll(p, 0o750); err != nil {
				t.Fatalf("mkdir %s: %v", p, err)
			}
			manyPaths = append(manyPaths, p)
		}
		req := Request{Workspace: workspace, GlobalReadOnly: manyPaths}
		_, err := BuildPolicy(req, "/bin/sh", runtimeRoot, nil)
		var se *SetupError
		if !errors.As(err, &se) {
			t.Fatalf("err = %v, want SetupError for root-count overflow", err)
		}
	})

	// A policy that triggers a size-bound error inside normalize.
	t.Run("size bound", func(t *testing.T) {
		req := Request{Workspace: workspace}
		long := filepath.Join(base, strings.Repeat("x", maxPolicyBytes))
		req.GlobalReadOnly = []string{long}
		_, err := BuildPolicy(req, "/bin/sh", runtimeRoot, nil)
		var se *SetupError
		if !errors.As(err, &se) {
			t.Fatalf("err = %v, want SetupError for size-bound failure", err)
		}
	})
}

// TestValidatePolicy_ErrorsArePathFree verifies that ValidatePolicy error
// messages do not disclose filesystem paths.
func TestValidatePolicy_ErrorsArePathFree(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	rt := filepath.Join(base, "rt")
	for _, d := range []string{ws, filepath.Join(rt, "home"), filepath.Join(rt, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	p, err := BuildPolicy(Request{Workspace: ws}, "/bin/sh", rt, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	// Conflicting permissions: error must not contain the path.
	t.Run("conflicting permissions", func(t *testing.T) {
		dup := *p
		dup.ReadOnlyRoots = append(append([]string{}, p.ReadOnlyRoots...), p.WritableRoots[0])
		err := ValidatePolicy(&dup)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), p.WritableRoots[0]) {
			t.Errorf("error message contains path: %v", err)
		}
	})

	// Non-absolute path: error must not contain the path.
	t.Run("non-absolute path", func(t *testing.T) {
		dup := *p
		dup.WritableRoots = append([]string{"rel/path"}, p.WritableRoots...)
		err := ValidatePolicy(&dup)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "rel/path") {
			t.Errorf("error message contains path: %v", err)
		}
	})
}

// TestBuildPolicy_RejectsIdentityAliasCrossClass verifies that a
// filesystem-identity alias of a writable root cannot smuggle a read-only
// grant through the lexical cross-class check.
func TestBuildPolicy_RejectsIdentityAliasCrossClass(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")

	// Real writable root and a symlink alias.
	rwReal := filepath.Join(base, "rw-real")
	rwAlias := filepath.Join(base, "rw-alias")
	for _, d := range []string{workspace, home, tmp, rwReal} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.Symlink(rwReal, rwAlias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// A global read-only path that is lexically within the alias but
	// filesystem-identical to the writable root.
	roChild := filepath.Join(rwAlias, "child")
	if err := os.MkdirAll(roChild, 0o750); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}

	// The canonical path is within rwReal (the symlink target). Since
	// GlobalWritable includes rwReal, the canonical RO path ends up
	// filesystem-within the writable root. The cross-class check should
	// reject this — either lexically (the canonical path starts with
	// rwReal) or via the new filesystem-identity pathWithin.
	_, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalWritable: []string{rwReal},
		GlobalReadOnly: []string{roChild},
	}, "/bin/sh", runtimeRoot, nil)
	if err == nil {
		t.Fatal("expected rejection of RO-below-RW via identity alias")
	}
	// Baseline-only conflict → SetupError.
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want SetupError for identity-alias cross-class conflict", err)
	}
}

// TestBuildPolicy_RejectsWritableAliasOfProtectedRoot verifies that a
// candidate writable alias of a protected read-only system root is rejected.
// On Linux this exercises the symlink-to-system-root case; on case-insensitive
// Darwin the filesystem-identity pathWithin catches case aliases.
func TestBuildPolicy_RejectsWritableAliasOfProtectedRoot(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")
	for _, d := range []string{workspace, home, tmp} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// Pick a system root that exists on this host.
	sysRoot := "/usr"
	if fi, err := os.Stat(sysRoot); err != nil || !fi.IsDir() {
		t.Skipf("skipping: %s is not an existing directory", sysRoot)
	}

	// Create a symlink alias to the system root.
	alias := filepath.Join(base, "usr-alias")
	if err := os.Symlink(sysRoot, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// canonicalPaths resolves alias → sysRoot via EvalSymlinks.
	// writableRootIsProtected(sysRoot, systemRoots) must detect the conflict.
	_, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalWritable: []string{alias},
	}, "/bin/sh", runtimeRoot, nil)
	if err == nil {
		t.Fatal("expected rejection of writable alias of a system root")
	}
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want SetupError for writable alias of system root", err)
	}
}

// TestDedupeKeepOrder_FilesystemIdentity verifies that dedupeKeepOrder
// collapses same-class filesystem-equivalent aliases (case-insensitive
// filesystems) first-wins. On case-sensitive filesystems the test is skipped.
func TestDedupeKeepOrder_FilesystemIdentity(t *testing.T) {
	dir := t.TempDir()
	lower := filepath.Join(dir, "dedupe")
	upper := filepath.Join(dir, "DEDUPE")
	if err := os.MkdirAll(lower, 0o750); err != nil {
		t.Fatalf("mkdir lower: %v", err)
	}
	li, errL := os.Stat(lower)
	ui, errU := os.Stat(upper)
	if errL != nil || errU != nil || !os.SameFile(li, ui) {
		t.Skip("skipping case-insensitive alias test: filesystem is case-sensitive")
	}

	// Resolve both to canonical paths.
	canonLower, err := filepath.EvalSymlinks(lower)
	if err != nil {
		t.Fatalf("EvalSymlinks lower: %v", err)
	}
	canonUpper, err := filepath.EvalSymlinks(upper)
	if err != nil {
		t.Fatalf("EvalSymlinks upper: %v", err)
	}

	got := dedupeKeepOrder([]string{canonLower, canonUpper})
	if len(got) != 1 {
		t.Fatalf("dedupeKeepOrder: got %d entries, want 1 (first-wins): %v", len(got), got)
	}
}

// TestBuildPolicy_ExactRemovalAcceptsAlias verifies that a removal spelled
// with a filesystem-equivalent case alias of a baseline entry is accepted as
// exact removal (not descendant removal). On case-sensitive filesystems the
// test is skipped.
func TestBuildPolicy_ExactRemovalAcceptsAlias(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")

	lower := filepath.Join(base, "removable")
	upper := filepath.Join(base, "REMOVABLE")
	for _, d := range []string{workspace, home, tmp, lower} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	li, errL := os.Stat(lower)
	ui, errU := os.Stat(upper)
	if errL != nil || errU != nil || !os.SameFile(li, ui) {
		t.Skip("skipping case-insensitive alias test: filesystem is case-sensitive")
	}

	// Baseline uses one case; removal uses the other.
	p, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalWritable: []string{lower},
		RemoveWritable: []string{upper},
	}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	// The writable root should have been removed (exact filesystem match).
	canonLower, _ := filepath.EvalSymlinks(lower)
	for _, r := range p.WritableRoots {
		if sameDir(r, canonLower) {
			t.Errorf("removed writable root %q still present via filesystem identity", r)
		}
	}
}

// TestBuildPolicy_AddRemoveAliasCollisionRejects verifies that an add and
// remove with filesystem-equivalent case aliases are rejected as a collision.
// On case-sensitive filesystems the test is skipped.
func TestBuildPolicy_AddRemoveAliasCollisionRejects(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")

	lower := filepath.Join(base, "collide")
	upper := filepath.Join(base, "COLLIDE")
	for _, d := range []string{workspace, home, tmp, lower} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	li, errL := os.Stat(lower)
	ui, errU := os.Stat(upper)
	if errL != nil || errU != nil || !os.SameFile(li, ui) {
		t.Skip("skipping case-insensitive alias test: filesystem is case-sensitive")
	}

	_, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalWritable: []string{lower},
		AddWritable:    []string{upper},
		RemoveWritable: []string{lower},
	}, "/bin/sh", runtimeRoot, nil)
	// The add and remove alias the same directory — should be a collision.
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions for add/remove alias collision", err)
	}
}

// TestBuildPolicy_CrossClassAliasPreservesProvenance verifies that a
// cross-class conflict detected via filesystem-equivalent alias preserves
// correct provenance (delta side → ValidationError, baseline-only →
// SetupError). On case-sensitive filesystems the test is skipped.
func TestBuildPolicy_CrossClassAliasPreservesProvenance(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	runtimeRoot := filepath.Join(base, "runtime")
	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")

	lower := filepath.Join(base, "shared")
	upper := filepath.Join(base, "SHARED")
	for _, d := range []string{workspace, home, tmp, lower} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	li, errL := os.Stat(lower)
	ui, errU := os.Stat(upper)
	if errL != nil || errU != nil || !os.SameFile(li, ui) {
		t.Skip("skipping case-insensitive alias test: filesystem is case-sensitive")
	}

	// persisted RO vs requested RW (delta-side) → ValidationError
	_, err := BuildPolicy(Request{
		Workspace:      workspace,
		GlobalReadOnly: []string{lower},
		AddWritable:    []string{upper},
	}, "/bin/sh", runtimeRoot, nil)
	if !errors.Is(err, ErrInvalidPermissions) {
		t.Fatalf("err = %v, want ErrInvalidPermissions for delta-side cross-class alias conflict", err)
	}
}
