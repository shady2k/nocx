package serverbin_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant/runtimebundle"
	"github.com/shady2k/nocx/internal/update/serverbin"
)

func treeFixture(t *testing.T, body string) (string, string) {
	return treeFixtureTarget(t, body, "linux/amd64")
}

func treeFixtureTarget(t *testing.T, body, target string) (string, string) {
	t.Helper()
	root := t.TempDir()
	files := []any{}
	for _, file := range []struct {
		name, body string
		executable bool
	}{
		{runtimebundle.InterpreterPath, "#!/bin/sh\nexit 99\n", true}, {runtimebundle.EntrypointPath, body, false}, {"python/lib/library.py", "stable library", false},
	} {
		name := filepath.Join(root, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if file.executable {
			mode = 0o700
		}
		if err := os.WriteFile(name, []byte(file.body), mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(file.body))
		files = append(files, map[string]any{"path": file.name, "sha256": hex.EncodeToString(sum[:]), "size": len(file.body), "executable": file.executable})
	}
	for name, target := range map[string]string{"python/bin/python": "python3.13", "python/bin/library": "../lib/library.py", "python/bin/library-chain": "library"} {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
		files = append(files, map[string]any{"path": name, "link": target})
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"formatVersion": 1, "metadata": map[string]any{"bridgeVersion": 1, "target": target, "interpreter": runtimebundle.InterpreterPath, "entrypoint": runtimebundle.EntrypointPath, "pythonVersion": "3.13.16", "lockSha256": strings.Repeat("a", 64), "packages": []any{map[string]any{"name": "pydantic-ai-slim", "version": "1.100.0"}}, "provenance": map[string]any{"source": "unit fixture"}}, "files": files}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return root, hex.EncodeToString(sum[:])
}

func retainInstall(t *testing.T, install serverbin.TreeInstall) {
	t.Helper()
	t.Cleanup(func() {
		if err := install.Close(); err != nil {
			t.Error(err)
		}
	})
}

func TestTreeInstallPreservesPrivateCompleteVersions(t *testing.T) {
	source, digest := treeFixture(t, "print('first')\n")
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	installer := serverbin.NewTreeInstaller(serverbin.NewOSTreeFS())
	first, err := installer.Ensure(context.Background(), source, parent, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	retainInstall(t, first)
	if !first.Fresh || first.Hash != digest || first.Path != filepath.Join(parent, digest) {
		t.Fatalf("wrong install: %#v", first)
	}
	if err = filepath.WalkDir(first.Path, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		want := os.FileMode(0o600)
		if entry.IsDir() || name == filepath.Join(first.Path, runtimebundle.InterpreterPath) {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s permissions=%o want %o", name, info.Mode().Perm(), want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"python/bin/python": "python3.13", "python/bin/library": "../lib/library.py", "python/bin/library-chain": "library"} {
		got, linkErr := os.Readlink(filepath.Join(first.Path, name))
		if linkErr != nil || got != target {
			t.Fatalf("link %s=%q %v", name, got, linkErr)
		}
	}
	if info, statErr := os.Stat(filepath.Join(first.Path, "empty")); statErr != nil || !info.IsDir() {
		t.Fatalf("empty directory lost: %v", statErr)
	}
	reused, err := installer.Ensure(context.Background(), source, parent, "linux/amd64")
	if err != nil || reused.Fresh || reused.Path != first.Path {
		t.Fatalf("not reused: %#v %v", reused, err)
	}
	retainInstall(t, reused)
	secondSource, _ := treeFixture(t, "print('second')\n")
	second, err := installer.Ensure(context.Background(), secondSource, parent, "linux/amd64")
	if err != nil || !second.Fresh || second.Path == first.Path {
		t.Fatalf("new version: %#v %v", second, err)
	}
	retainInstall(t, second)
	if err = os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(first.Path, runtimebundle.EntrypointPath))
	if err != nil || string(data) != "print('first')\n" {
		t.Fatalf("live older version was pruned: %q %v", data, err)
	}
}

func TestTreeInstallRefusesCorruptOrEmptyLiveTarget(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{true: "empty", false: "corrupt"}[empty], func(t *testing.T) {
			source, digest := treeFixture(t, "safe")
			parent := filepath.Join(t.TempDir(), "agent-runtime")
			target := filepath.Join(parent, digest)
			installer := serverbin.NewTreeInstaller(serverbin.NewOSTreeFS())
			if empty {
				if err := os.MkdirAll(target, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				install, err := installer.Ensure(context.Background(), source, parent, "linux/amd64")
				if err != nil {
					t.Fatal(err)
				}
				retainInstall(t, install)
				if err := os.WriteFile(filepath.Join(target, runtimebundle.EntrypointPath), []byte("evil"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := installer.Ensure(context.Background(), source, parent, "linux/amd64"); err == nil {
				t.Fatal("replaced invalid live tree")
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("removed live target: %v", err)
			}
			if !empty {
				data, err := os.ReadFile(filepath.Join(target, runtimebundle.EntrypointPath)) //nolint:gosec // target is this test's installed fixture; the entrypoint name is fixed
				if err != nil || string(data) != "evil" {
					t.Fatalf("rewrote corrupt live bytes: %q %v", data, err)
				}
			}
			assertNoOwnStages(t, parent)
		})
	}
}

type treeFaultFS struct {
	serverbin.TreeFS
	op, match   string
	onRename    func(string, string) error
	cancel      context.CancelFunc
	parent      string
	parentSyncs int
}

func (f *treeFaultFS) fails(op, name string) bool {
	return f.op == op && (f.match == "" || strings.Contains(name, f.match))
}

func (f *treeFaultFS) LockShared(ctx context.Context, parent, digest string) (*runtimebundle.Lease, error) {
	if f.fails("lease", parent) {
		return nil, errInjected
	}
	return f.TreeFS.LockShared(ctx, parent, digest)
}

func (f *treeFaultFS) Lstat(name string) (fs.FileInfo, error) {
	if f.fails("lstat", name) {
		return nil, errInjected
	}
	return f.TreeFS.Lstat(name)
}

func (f *treeFaultFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if f.fails("readdir", name) {
		return nil, errInjected
	}
	return f.TreeFS.ReadDir(name)
}

func (f *treeFaultFS) Readlink(name string) (string, error) {
	if f.fails("readlink", name) {
		return "", errInjected
	}
	return f.TreeFS.Readlink(name)
}

func (f *treeFaultFS) MkdirAll(name string, mode os.FileMode) error {
	if f.fails("mkdirall", name) {
		return errInjected
	}
	return f.TreeFS.MkdirAll(name, mode)
}

func (f *treeFaultFS) Mkdir(name string, mode os.FileMode) error {
	if f.fails("mkdir", name) {
		return errInjected
	}
	return f.TreeFS.Mkdir(name, mode)
}

func (f *treeFaultFS) Symlink(target, name string) error {
	if f.fails("symlink", name) {
		return errInjected
	}
	return f.TreeFS.Symlink(target, name)
}

func (f *treeFaultFS) Open(name string) (io.ReadCloser, error) {
	if f.fails("open", name) {
		return nil, errInjected
	}
	file, err := f.TreeFS.Open(name)
	if err != nil {
		return nil, err
	}
	if f.fails("read", name) || f.fails("read-close", name) {
		return treeFaultReader{ReadCloser: file, op: f.op}, nil
	}
	return file, nil
}

func (f *treeFaultFS) Create(name string, mode os.FileMode) (serverbin.File, error) {
	if f.fails("create", name) {
		return nil, errInjected
	}
	file, err := f.TreeFS.Create(name, mode)
	if err != nil {
		return nil, err
	}
	if f.match == "" || strings.Contains(name, f.match) {
		return &treeFaultFile{File: file, op: f.op, cancel: f.cancel}, nil
	}
	return file, nil
}

func (f *treeFaultFS) RenameNew(source, target string) error {
	if f.onRename != nil {
		if err := f.onRename(source, target); err != nil {
			return err
		}
	}
	if f.fails("rename", target) {
		return errInjected
	}
	return f.TreeFS.RenameNew(source, target)
}

func (f *treeFaultFS) SyncDir(name string) error {
	if f.op == "final-parent-sync" && name == f.parent {
		f.parentSyncs++
		if f.parentSyncs == 2 {
			return errInjected
		}
	}
	if f.fails("syncdir", name) {
		return errInjected
	}
	return f.TreeFS.SyncDir(name)
}

type treeFaultReader struct {
	io.ReadCloser
	op string
}

func (f treeFaultReader) Read(p []byte) (int, error) {
	if f.op == "read" {
		return 0, errInjected
	}
	return f.ReadCloser.Read(p)
}

func (f treeFaultReader) Close() error {
	err := f.ReadCloser.Close()
	if f.op == "read-close" {
		return errInjected
	}
	return err
}

type treeFaultFile struct {
	serverbin.File
	op     string
	cancel context.CancelFunc
}

func (f *treeFaultFile) Write(p []byte) (int, error) {
	if f.cancel != nil {
		f.cancel()
	}
	if f.op == "write" {
		return 0, errInjected
	}
	if f.op == "same-size-corruption" && len(p) > 0 {
		copyOf := append([]byte(nil), p...)
		copyOf[0] ^= 1
		return f.File.Write(copyOf)
	}
	if f.op == "short-copy" && len(p) > 0 {
		if _, err := f.File.Write(p[:len(p)-1]); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return f.File.Write(p)
}

func (f *treeFaultFile) Sync() error {
	if f.op == "sync" {
		return errInjected
	}
	return f.File.Sync()
}

func (f *treeFaultFile) Close() error {
	err := f.File.Close()
	if f.op == "close" {
		return errInjected
	}
	return err
}

func assertNoOwnStages(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".runtime-") && entry.Name() != ".runtime-foreign" {
			t.Errorf("left owned stage %s", entry.Name())
		}
	}
}

func TestTreeInstallFailuresNeverPromotePartialBytes(t *testing.T) {
	for _, op := range []string{"lstat", "readdir", "readlink", "open", "read", "read-close", "mkdirall", "lease", "mkdir", "create", "write", "sync", "close", "short-copy", "same-size-corruption", "symlink", "syncdir", "rename"} {
		t.Run(op, func(t *testing.T) {
			source, digest := treeFixture(t, "payload")
			parent := filepath.Join(t.TempDir(), "agent-runtime")
			foreign := filepath.Join(parent, ".runtime-foreign")
			if err := os.MkdirAll(foreign, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(foreign, "owner"), []byte("other installer"), 0o600); err != nil {
				t.Fatal(err)
			}
			view := &treeFaultFS{TreeFS: serverbin.NewOSTreeFS(), op: op}
			if op == "syncdir" {
				view.match = ".runtime-"
			}
			if _, err := serverbin.NewTreeInstaller(view).Ensure(context.Background(), source, parent, "linux/amd64"); err == nil {
				t.Fatal("injected failure was ignored")
			}
			if _, err := os.Lstat(filepath.Join(parent, digest)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("promoted failed stage: %v", err)
			}
			assertNoOwnStages(t, parent)
			data, err := os.ReadFile(filepath.Join(foreign, "owner")) //nolint:gosec // foreign names another test-owned staging fixture, not a user-selected path
			if err != nil || string(data) != "other installer" {
				t.Fatalf("removed another stage: %q %v", data, err)
			}
		})
	}
}

func TestTreeInstallVerifiesReadBackAndSourceBeforeEffects(t *testing.T) {
	for _, kind := range []string{"unlisted", "escaping-link", "readback-open", "readback-read"} {
		t.Run(kind, func(t *testing.T) {
			source, digest := treeFixture(t, "payload")
			parent := filepath.Join(t.TempDir(), "agent-runtime")
			view := &treeFaultFS{TreeFS: serverbin.NewOSTreeFS()}
			switch kind {
			case "unlisted":
				if err := os.WriteFile(filepath.Join(source, "app/unlisted.py"), []byte("unexpected"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "escaping-link":
				name := filepath.Join(source, "python/bin/python")
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/usr/bin/python3", name); err != nil {
					t.Fatal(err)
				}
			case "readback-open":
				view.op = "open"
				view.match = ".runtime-"
			case "readback-read":
				view.op = "read"
				view.match = ".runtime-"
			}
			if _, err := serverbin.NewTreeInstaller(view).Ensure(context.Background(), source, parent, "linux/amd64"); err == nil {
				t.Fatal("accepted invalid source/copy")
			}
			if kind == "unlisted" || kind == "escaping-link" {
				if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("mutated destination before source validation: %v", err)
				}
			}
			if _, err := os.Stat(filepath.Join(parent, digest)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("promoted invalid copy")
			}
			assertNoOwnStages(t, parent)
		})
	}
}

func TestTreeInstallCancellationAndPromotionRace(t *testing.T) {
	t.Run("cancel during copy", func(t *testing.T) {
		source, digest := treeFixture(t, "payload")
		parent := filepath.Join(t.TempDir(), "agent-runtime")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		view := &treeFaultFS{TreeFS: serverbin.NewOSTreeFS(), cancel: cancel}
		if _, err := serverbin.NewTreeInstaller(view).Ensure(ctx, source, parent, "linux/amd64"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
		if _, err := os.Stat(filepath.Join(parent, digest)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cancelled tree promoted")
		}
		assertNoOwnStages(t, parent)
	})
	t.Run("empty target appears at rename", func(t *testing.T) {
		source, digest := treeFixture(t, "payload")
		parent := filepath.Join(t.TempDir(), "agent-runtime")
		view := &treeFaultFS{TreeFS: serverbin.NewOSTreeFS(), onRename: func(_, target string) error { return os.Mkdir(target, 0o700) }}
		if _, err := serverbin.NewTreeInstaller(view).Ensure(context.Background(), source, parent, "linux/amd64"); err == nil {
			t.Fatal("overwrote existing empty target")
		}
		entries, err := os.ReadDir(filepath.Join(parent, digest))
		if err != nil || len(entries) != 0 {
			t.Fatalf("replaced invalid live directory: %v %v", entries, err)
		}
		assertNoOwnStages(t, parent)
	})
}

func TestTreeInstallFinalDirectorySyncFailureRetainsCompleteWinner(t *testing.T) {
	source, digest := treeFixture(t, "payload")
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	view := &treeFaultFS{TreeFS: serverbin.NewOSTreeFS(), op: "final-parent-sync", parent: parent}
	if _, err := serverbin.NewTreeInstaller(view).Ensure(context.Background(), source, parent, "linux/amd64"); !errors.Is(err, errInjected) {
		t.Fatalf("lost durability error: %v", err)
	}
	manifest, err := runtimebundle.ReadManifest(context.Background(), serverbin.NewOSTreeFS(), filepath.Join(parent, digest), digest, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtimebundle.VerifyTree(context.Background(), serverbin.NewOSTreeFS(), filepath.Join(parent, digest), manifest); err != nil {
		t.Fatalf("post-rename failure left partial tree: %v", err)
	}
	reused, err := serverbin.NewTreeInstaller(serverbin.NewOSTreeFS()).Ensure(context.Background(), source, parent, "linux/amd64")
	if err != nil || reused.Fresh {
		t.Fatalf("complete winner cannot be reused: %#v %v", reused, err)
	}
	retainInstall(t, reused)
	assertNoOwnStages(t, parent)
}

func TestTreeInstallConcurrentIdenticalTreesShareOneWinner(t *testing.T) {
	source, digest := treeFixture(t, "payload")
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const count = 6
	arrived := make(chan struct{}, count)
	release := make(chan struct{})
	view := &treeFaultFS{TreeFS: serverbin.NewOSTreeFS(), onRename: func(string, string) error {
		arrived <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	type outcome struct {
		install serverbin.TreeInstall
		err     error
	}
	results := make(chan outcome, count)
	var group sync.WaitGroup
	for n := 0; n < count; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			install, err := serverbin.NewTreeInstaller(view).Ensure(ctx, source, parent, "linux/amd64")
			results <- outcome{install, err}
		}()
	}
	for n := 0; n < count; n++ {
		select {
		case <-arrived:
		case <-ctx.Done():
			close(release)
			group.Wait()
			t.Fatal("installers did not reach concurrent promotion")
		}
	}
	close(release)
	group.Wait()
	close(results)
	fresh := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		retainInstall(t, result.install)
		if result.install.Path != filepath.Join(parent, digest) {
			t.Fatalf("different winner: %#v", result.install)
		}
		if result.install.Fresh {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh installs=%d", fresh)
	}
	assertNoOwnStages(t, parent)
}

func TestTreeInstallAcceptsParentDirectoryAliasWithoutEscapingTree(t *testing.T) {
	source, digest := treeFixture(t, "payload")
	base := t.TempDir()
	alias := filepath.Join(t.TempDir(), "profile-alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	install, err := serverbin.NewTreeInstaller(serverbin.NewOSTreeFS()).Ensure(context.Background(), source, filepath.Join(alias, "agent-runtime"), "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	retainInstall(t, install)
	actual, err := filepath.EvalSymlinks(install.Path)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(base, "agent-runtime", digest))
	if err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("directory alias changed the installed tree: %q != %q", actual, expected)
	}
}
