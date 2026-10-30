package serverbin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant/runtimebundle"
	"github.com/shady2k/nocx/internal/update/serverbin"
)

func installedFixture(t *testing.T, parent, body string) (string, serverbin.TreeInstall) {
	t.Helper()
	source, _ := treeFixture(t, body)
	installed, err := serverbin.NewTreeInstaller(serverbin.NewOSTreeFS()).Ensure(context.Background(), source, parent, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	retainInstall(t, installed)
	return source, installed
}

func TestTreePruneKeepsLiveAndCurrentVersionsThenRemovesUnused(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	oldSource, old := installedFixture(t, parent, "old")
	_, current := installedFixture(t, parent, "current")
	installer := serverbin.NewTreeInstaller(serverbin.NewOSTreeFS())
	lockPath := filepath.Join(parent, runtimebundle.LeaseDirectory, old.Hash+".lock")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = installer.Prune(context.Background(), parent, current.Hash, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(old.Path); err != nil {
		t.Fatalf("removed live runtime: %v", err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	if err = current.Close(); err != nil {
		t.Fatal(err)
	} // current is kept even without a live process
	if err = installer.Prune(context.Background(), parent, current.Hash, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unused version was not pruned: %v", err)
	}
	if _, err = os.Stat(current.Path); err != nil {
		t.Fatalf("removed expected current version: %v", err)
	}
	after, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("pruner changed lock inode: %v", err)
	}
	reinstalled, err := installer.Ensure(context.Background(), oldSource, parent, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	retainInstall(t, reinstalled)
	after, err = os.Stat(lockPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("reinstallation replaced lock inode: %v", err)
	}
}

func TestTreePruneRetainsCorruptForeignAndUncertainEntries(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	_, corrupt := installedFixture(t, parent, "corrupt-me")
	_, current := installedFixture(t, parent, "current")
	if err := corrupt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt.Path, runtimebundle.EntrypointPath), []byte("different bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(parent, "not-a-runtime")
	empty := filepath.Join(parent, strings.Repeat("e", 64))
	outside := t.TempDir()
	alias := filepath.Join(parent, strings.Repeat("d", 64))
	for _, name := range []string{foreign, empty} {
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	installer := serverbin.NewTreeInstaller(serverbin.NewOSTreeFS())
	if err := installer.Prune(context.Background(), parent, current.Hash, "linux/amd64"); err == nil {
		t.Fatal("corruption was not reported as uncertain")
	}
	for _, name := range []string{corrupt.Path, current.Path, foreign, empty, alias, outside} {
		if _, err := os.Lstat(name); err != nil {
			t.Fatalf("deleted unproven version %s: %v", name, err)
		}
	}
}

type foreignTreeInfo struct {
	fs.FileInfo
	stat syscall.Stat_t
}

func (i foreignTreeInfo) Sys() any { return &i.stat }

type foreignTreeFS struct {
	serverbin.TreeFS
	foreign string
}

func (f foreignTreeFS) Lstat(name string) (fs.FileInfo, error) {
	info, err := f.TreeFS.Lstat(name)
	if err != nil || name != f.foreign {
		return info, err
	}
	statPtr, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("fixture needs Unix stat ownership metadata")
	}
	stat := *statPtr
	stat.Uid++
	return foreignTreeInfo{info, stat}, nil
}

func TestTreePruneDoesNotDeleteAnotherOwnersCompleteTree(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	_, foreign := installedFixture(t, parent, "foreign")
	_, current := installedFixture(t, parent, "current")
	if err := foreign.Close(); err != nil {
		t.Fatal(err)
	}
	view := foreignTreeFS{TreeFS: serverbin.NewOSTreeFS(), foreign: foreign.Path}
	if err := serverbin.NewTreeInstaller(view).Prune(context.Background(), parent, current.Hash, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign.Path); err != nil {
		t.Fatalf("removed foreign owner tree: %v", err)
	}
}

type pruneGateFS struct {
	serverbin.TreeFS
	root    string
	entered chan struct{}
	release chan struct{}
	ctx     context.Context
}

func (f pruneGateFS) RemoveAll(name string) error {
	if name == f.root {
		close(f.entered)
		select {
		case <-f.release:
		case <-f.ctx.Done():
			return f.ctx.Err()
		}
	}
	return f.TreeFS.RemoveAll(name)
}

func TestTreePruneExclusiveLeaseCoversValidationThroughDeletion(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	_, old := installedFixture(t, parent, "old")
	_, current := installedFixture(t, parent, "current")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	view := pruneGateFS{TreeFS: serverbin.NewOSTreeFS(), root: old.Path, entered: entered, release: release, ctx: ctx}
	result := make(chan error, 1)
	go func() { result <- serverbin.NewTreeInstaller(view).Prune(ctx, parent, current.Hash, "linux/amd64") }()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("pruner never reached delete interval: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	wait, cancelWait := context.WithTimeout(ctx, 40*time.Millisecond)
	lease, err := runtimebundle.AcquireShared(wait, parent, old.Hash)
	cancelWait()
	if !errors.Is(err, context.DeadlineExceeded) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("new reader entered delete interval: %v", err)
	}
	unblock()
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	locator := runtimebundle.New(old.Path, old.Hash, "linux/amd64")
	defer func() {
		if closeErr := locator.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if _, err = locator.Locate(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("locator exposed a removed version: %v", err)
	}
}

func TestTreeInstallWaitsForExclusivePruneLeaseBeforePromotion(t *testing.T) {
	source, digest := treeFixture(t, "payload")
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	lease, err := runtimebundle.TryExclusive(parent, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err = serverbin.NewTreeInstaller(serverbin.NewOSTreeFS()).Ensure(ctx, source, parent, "linux/amd64"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("installer ignored prune lease: %v", err)
	}
	if _, err = os.Stat(filepath.Join(parent, digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("promoted during exclusive prune")
	}
	assertNoOwnStages(t, parent)
}

type refusedPruneFS struct {
	serverbin.TreeFS
	root string
}

func (f refusedPruneFS) RemoveAll(name string) error {
	if name == f.root {
		return errInjected
	}
	return f.TreeFS.RemoveAll(name)
}

func TestTreePruneDeleteFailureLeavesVersionAndReleasesLease(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	_, old := installedFixture(t, parent, "old")
	_, current := installedFixture(t, parent, "current")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	view := refusedPruneFS{TreeFS: serverbin.NewOSTreeFS(), root: old.Path}
	if err := serverbin.NewTreeInstaller(view).Prune(context.Background(), parent, current.Hash, "linux/amd64"); !errors.Is(err, errInjected) {
		t.Fatalf("lost delete error: %v", err)
	}
	if _, err := os.Stat(old.Path); err != nil {
		t.Fatalf("failed deletion lost version: %v", err)
	}
	lease, err := runtimebundle.TryExclusive(parent, old.Hash)
	if err != nil {
		t.Fatalf("failed prune leaked exclusive lock: %v", err)
	}
	_ = lease.Close()
}

func TestTreeInstallRejectsComposedSymlinkEscapeBeforeEffects(t *testing.T) {
	source, _ := treeFixture(t, "payload")
	data, err := os.ReadFile(filepath.Join(source, "manifest.json")) //nolint:gosec // fixed manifest path inside treeFixture's temporary directory
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(source, runtimebundle.InterpreterPath), filepath.Join(source, "python/bin/python3.13-real")); err != nil {
		t.Fatal(err)
	}
	files, ok := manifest["files"].([]any)
	if !ok || len(files) == 0 {
		t.Fatal("fixture manifest needs file records")
	}
	interpreter, ok := files[0].(map[string]any)
	if !ok {
		t.Fatal("fixture interpreter record must be an object")
	}
	interpreter["path"] = "python/bin/python3.13-real"
	for name, target := range map[string]string{"python/bin/root": "../..", runtimebundle.InterpreterPath: "root/../python3.13-real"} {
		if err = os.Symlink(target, filepath.Join(source, name)); err != nil {
			t.Fatal(err)
		}
		files = append(files, map[string]any{"path": name, "link": target})
	}
	manifest["files"] = files
	outside := filepath.Join(filepath.Dir(source), "python3.13-real")
	if err = os.WriteFile(outside, []byte("outside unverified interpreter"), 0o700); err != nil { //nolint:gosec // negative escape fixture deliberately represents an executable outside the bundle
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	resolved, err := filepath.EvalSymlinks(filepath.Join(source, runtimebundle.InterpreterPath))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(outside)
	if err != nil || resolved != want {
		t.Fatalf("fixture did not reproduce kernel escape: %q %q %v", resolved, want, err)
	}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "agent-runtime")
	if _, err = serverbin.NewTreeInstaller(serverbin.NewOSTreeFS()).Ensure(context.Background(), source, parent, "linux/amd64"); !errors.Is(err, runtimebundle.ErrInvalid) {
		t.Fatalf("accepted unverified outside interpreter: %v", err)
	}
	if _, err = os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created stage before rejecting escape: %v", err)
	}
}

func TestPrepareRuntimeStagesAllTargetsAndPrunesOnlyUnusedAtStartup(t *testing.T) {
	for _, target := range []string{"linux/amd64", "darwin/amd64", "darwin/arm64"} {
		t.Run(target, func(t *testing.T) {
			parts := strings.Split(target, "/")
			mount := t.TempDir()
			executable := filepath.Join(mount, "usr/bin/nocx-server")
			if parts[0] == "darwin" {
				executable = filepath.Join(mount, "App.app/Contents/MacOS/nocx-server")
			}
			packaged, err := runtimebundle.PackagedRoot(executable, parts[0], parts[1])
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(packaged), 0o700); err != nil {
				t.Fatal(err)
			}
			firstSource, _ := treeFixtureTarget(t, "first package", target)
			if err = os.Rename(firstSource, packaged); err != nil {
				t.Fatal(err)
			}
			data := t.TempDir()
			first, err := serverbin.PrepareRuntime(context.Background(), executable, data, parts[0], parts[1])
			if err != nil {
				t.Fatal(err)
			}
			retainInstall(t, first)
			secondSource, _ := treeFixtureTarget(t, "replacement package", target)
			if err = os.RemoveAll(packaged); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(secondSource, packaged); err != nil {
				t.Fatal(err)
			}
			second, err := serverbin.PrepareRuntime(context.Background(), executable, data, parts[0], parts[1])
			if err != nil {
				t.Fatal(err)
			}
			retainInstall(t, second)
			if first.Path == second.Path {
				t.Fatal("package replacement did not select new immutable identity")
			}
			if _, err = os.Stat(first.Path); err != nil {
				t.Fatalf("startup pruned live previous package: %v", err)
			}
			if err = first.Close(); err != nil {
				t.Fatal(err)
			}
			reused, err := serverbin.PrepareRuntime(context.Background(), executable, data, parts[0], parts[1])
			if err != nil {
				t.Fatal(err)
			}
			retainInstall(t, reused)
			if reused.Fresh || reused.Path != second.Path {
				t.Fatalf("startup did not reuse expected complete tree: %#v", reused)
			}
			if _, err = os.Stat(first.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("startup retained proven-unused old version: %v", err)
			}
			if err = os.RemoveAll(mount); err != nil {
				t.Fatal(err)
			}
			locator := runtimebundle.New(second.Path, second.Hash, target)
			if err = locator.Adopt(second.Lease); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if closeErr := locator.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			}()
			if _, err = locator.Locate(context.Background()); err != nil {
				t.Fatalf("staged runtime depends on replaced source: %v", err)
			}
		})
	}
}
