//go:build unix

package hostsvc

// The owner half of a commit's mode step, on the platforms that have an owner
// to give a file to.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestChownToTargetTreatsEveryFailureAsBestEffort: giving a file away needs a
// privilege the helper does not have, and every kernel spells that refusal its
// own way — EPERM, EINVAL on an NFSv4 mount whose idmap disagrees, EACCES,
// ENOTSUP. The attempt is best effort because the account can write the file
// either way, so NO failure of it may refuse the commit; a test that only
// covered EPERM would let a helper that cannot chown at all fail every save on
// one kind of mount.
func TestChownToTargetTreatsEveryFailureAsBestEffort(t *testing.T) {
	dir := tempDir(t)
	holder := filepath.Join(dir, "holder.txt")
	mustWrite(t, holder, []byte("x\n"))
	info, err := os.Stat(holder)
	if err != nil {
		t.Fatal(err)
	}

	// The temporary vanished between the mode step and the owner step: the
	// real syscall fails with ENOENT, and that is a chown failure like any
	// other — best effort, not a reason to refuse the commit.
	vanished := filepath.Join(dir, "vanished")
	if err := chownToTarget(vanished, info); err != nil {
		t.Fatalf("chown of a vanished temporary = %v, want the best-effort nil", err)
	}

	// And the ordinary case still does what it says: the temporary keeps the
	// account's own identity, which is what the target already has.
	owned := filepath.Join(dir, "owned.txt")
	mustWrite(t, owned, []byte("x\n"))
	if err := chownToTarget(owned, info); err != nil {
		t.Fatalf("chown of an ordinary file = %v, want nil", err)
	}
}
