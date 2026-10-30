package runtimebundle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocatorAdoptionPreservesOwnershipOnFailure(t *testing.T) {
	root, m := bundleFixture(t)
	digest := writeManifest(t, root, m)
	root = stagedFixture(t, root, digest)
	lease, err := AcquireShared(context.Background(), filepath.Dir(root), digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	otherRoot, other := bundleFixture(t)
	fixtureObject(t, other["metadata"])["pythonVersion"] = "3.13.17"
	otherDigest := writeManifest(t, otherRoot, other)
	otherRoot = stagedFixture(t, otherRoot, otherDigest)
	wrong := New(otherRoot, otherDigest, "linux/amd64")
	if err = wrong.Adopt(lease); !errors.Is(err, ErrInvalid) {
		t.Fatalf("adopted mismatched lease: %v", err)
	}
	if lease.File() == nil {
		t.Fatal("failed adoption consumed caller ownership")
	}
	if exclusive, lockErr := TryExclusive(filepath.Dir(root), digest); !errors.Is(lockErr, ErrLeaseBusy) {
		if exclusive != nil {
			_ = exclusive.Close()
		}
		t.Fatalf("failed adoption dropped shared lease: %v", lockErr)
	}
	locator := New(root, digest, "linux/amd64")
	if err = locator.Adopt(lease); err != nil {
		t.Fatal(err)
	}
	resolved, err := locator.Locate(context.Background())
	if err != nil || resolved.Lease != lease.File() {
		t.Fatalf("locator did not retain its bound descriptor: %#v %v", resolved, err)
	}
	if err = locator.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = locator.Locate(context.Background()); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("closed locator reacquired runtime: %v", err)
	}
	exclusive, err := TryExclusive(filepath.Dir(root), digest)
	if err != nil {
		t.Fatalf("closed locator retained lock: %v", err)
	}
	_ = exclusive.Close()
}

func TestInheritedLeaseRefusesUnrelatedDescriptorWithoutClosingIt(t *testing.T) {
	root, m := bundleFixture(t)
	digest := writeManifest(t, root, m)
	root = stagedFixture(t, root, digest)
	held, err := AcquireShared(context.Background(), filepath.Dir(root), digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := held.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	file, err := os.Open(filepath.Join(root, EntrypointPath)) //nolint:gosec // fixed entrypoint inside a test-created private bundle
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if _, err = inheritedLease(int(file.Fd()), root, digest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted unrelated FD: %v", err)
	}
	if _, err = file.Stat(); err != nil {
		t.Fatalf("invalid handoff closed an unrelated file: %v", err)
	}
}

func TestSharedLeaseWaitIsCancellableAndLockInodePersists(t *testing.T) {
	root, m := bundleFixture(t)
	digest := writeManifest(t, root, m)
	root = stagedFixture(t, root, digest)
	parent := filepath.Dir(root)
	exclusive, err := TryExclusive(parent, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := exclusive.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	name := filepath.Join(parent, LeaseDirectory, digest+".lock")
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if shared, lockErr := AcquireShared(ctx, parent, digest); !errors.Is(lockErr, context.DeadlineExceeded) {
		if shared != nil {
			_ = shared.Close()
		}
		t.Fatalf("shared lease bypassed exclusive owner: %v", lockErr)
	}
	if err = exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	shared, err := AcquireShared(context.Background(), parent, digest)
	if err != nil {
		t.Fatal(err)
	}
	_ = shared.Close()
	after, err := os.Stat(name)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lease close replaced persistent inode: %v", err)
	}
}

func TestLeaseFilesRejectSymlinksAndForeignShapes(t *testing.T) {
	for _, kind := range []string{"symlink directory", "symlink file", "nonempty file", "public file"} {
		t.Run(kind, func(t *testing.T) {
			root, m := bundleFixture(t)
			digest := writeManifest(t, root, m)
			root = stagedFixture(t, root, digest)
			parent := filepath.Dir(root)
			dir := filepath.Join(parent, LeaseDirectory)
			if kind == "symlink directory" {
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				name := filepath.Join(dir, digest+".lock")
				switch kind {
				case "symlink file":
					target := filepath.Join(t.TempDir(), "target")
					if err := os.WriteFile(target, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, name); err != nil {
						t.Fatal(err)
					}
				case "nonempty file":
					if err := os.WriteFile(name, []byte("foreign owner"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "public file":
					if err := os.WriteFile(name, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(name, 0o644); err != nil { //nolint:gosec // negative fixture: a public-readable lease file must be rejected
						t.Fatal(err)
					}
				}
			}
			if lease, err := AcquireShared(context.Background(), parent, digest); err == nil {
				_ = lease.Close()
				t.Fatal("accepted unsafe lease namespace")
			}
		})
	}
}
