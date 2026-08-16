package sandbox

import (
	"errors"
	"os"
	"path/filepath"
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
	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")
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
		filepath.Join(base, "runtime", "home"),
		filepath.Join(base, "runtime", "tmp"),
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
		Workspace: workspace,
		Global:    []string{globalDrop, globalKeep},
		Add:       []string{add},
		Remove:    []string{globalDrop},
	}, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}

	want := []string{
		canonicalPath(t, workspace),
		canonicalPath(t, common),
		canonicalPath(t, globalKeep),
		canonicalPath(t, add),
		filepath.Join(runtimeRoot, "home"),
		filepath.Join(runtimeRoot, "tmp"),
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
	_, err := BuildPolicy(Request{Workspace: workspace, Remove: []string{other}}, "/bin/sh", runtimeRoot, nil)
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
	_, err := BuildPolicy(Request{Workspace: workspace, Add: []string{dir}, Remove: []string{dir}}, "/bin/sh", runtimeRoot, nil)
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
	_, err := BuildPolicy(Request{Workspace: workspace, Remove: []string{workspace}}, "/bin/sh", runtimeRoot, nil)
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
		Workspace: workspace,
		Global:    []string{linkGlobal},
		Remove:    []string{realGlobal},
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
	_, err := BuildPolicy(Request{Workspace: workspace, Add: []string{"/"}}, "/bin/sh", runtimeRoot, nil)
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
	_, err := BuildPolicy(Request{Workspace: workspace, Global: []string{"/"}}, "/bin/sh", runtimeRoot, nil)
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
	req := Request{Workspace: workspace, Global: []string{global}, Add: []string{add}}
	p, err := BuildPolicy(req, "/bin/sh", runtimeRoot, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	before := append([]string(nil), p.WritableRoots...)

	// Mutating the caller's slices must never change the installed policy.
	req.Global[0] = "/elsewhere"
	req.Add[0] = "/nowhere"
	req.Remove = append(req.Remove, "/x")

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
	p, err := BuildPolicy(Request{Workspace: workspace, Global: []string{global}, Add: []string{add}}, "/bin/sh", runtimeRoot, nil)
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

	t.Run("global overflow is SetupError", func(t *testing.T) {
		_, err := BuildPolicy(Request{Workspace: workspace, Global: many}, "/bin/sh", runtimeRoot, nil)
		var se *SetupError
		if !errors.As(err, &se) {
			t.Fatalf("err = %v, want SetupError for oversized global list", err)
		}
	})

	t.Run("add overflow is ValidationError", func(t *testing.T) {
		_, err := BuildPolicy(Request{Workspace: workspace, Add: many}, "/bin/sh", runtimeRoot, nil)
		if !errors.Is(err, ErrInvalidPermissions) {
			t.Fatalf("err = %v, want ErrInvalidPermissions for oversized add list", err)
		}
	})

	t.Run("remove overflow is ValidationError", func(t *testing.T) {
		_, err := BuildPolicy(Request{Workspace: workspace, Remove: many}, "/bin/sh", runtimeRoot, nil)
		if !errors.Is(err, ErrInvalidPermissions) {
			t.Fatalf("err = %v, want ErrInvalidPermissions for oversized remove list", err)
		}
	})
}
