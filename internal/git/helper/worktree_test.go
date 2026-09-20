package helper_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The linked-worktree operations are not served by the helper (brief
// nocx-xn63t.1.1): the coordinator creates the worktree of ITS repository on
// its own machine, and nothing of that crosses to a far host. What a caller
// must never get instead is a fabricated success, so this is the acceptance
// check for the honest refusal — against the real host and the real git
// service, the same harness the other tests in this package use.
func TestHelperRepoDoesNotServeTheWorktreeOperations(t *testing.T) {
	ctx := context.Background()
	dir := fixtureRepo(t)
	c := dialHelper(t, helperPeer(t))
	repo := openHelper(t, c, dir)
	wtPath := filepath.Join(t.TempDir(), "worker-1")

	cases := []struct {
		op   string
		call func() error
	}{
		{"worktree.add", func() error {
			_, err := repo.AddWorktree(ctx, "worker-1", "master", wtPath)
			return err
		}},
		{"worktree.list", func() error {
			_, err := repo.Worktrees(ctx, "master")
			return err
		}},
		{"worktree.remove", func() error {
			return repo.RemoveWorktree(ctx, wtPath)
		}},
		{"worktree.deleteBranch", func() error {
			return repo.DeleteWorktreeBranch(ctx, "worker-1", "master")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("the helper served a worktree operation it does not carry")
			}
			if !strings.Contains(err.Error(), tc.op) {
				t.Errorf("err = %v, want it to name %s: the caller has to be able to say what failed", err, tc.op)
			}
		})
	}

	if _, err := os.Stat(wtPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a worktree path was created by a refused call (stat err = %v)", err)
	}
	if branch, err := os.Stat(filepath.Join(dir, ".git", "refs", "heads", "worker-1")); err == nil {
		t.Errorf("a branch was created by a refused call: %s", branch.Name())
	}
}

// TestHelperRepoWorktreeRefusalIsLocal: the refusal is the CLIENT's answer,
// not the service's — nothing was asked over the wire. That is the difference
// between a caller hearing "the helper does not serve this yet" and hearing a
// protocol failure it would have to decode, and it is observable: the calls
// still answer the same way with the client closed, when no round trip is
// possible at all.
func TestHelperRepoWorktreeRefusalIsLocal(t *testing.T) {
	ctx := context.Background()
	dir := fixtureRepo(t)
	c := dialHelper(t, helperPeer(t))
	repo := openHelper(t, c, dir)
	if err := c.Close(); err != nil {
		t.Fatalf("closing the helper client: %v", err)
	}

	if _, err := repo.Worktrees(ctx, "master"); err == nil || !strings.Contains(err.Error(), "does not serve") {
		t.Errorf("Worktrees err = %v, want the local refusal even with the client closed", err)
	}
	// And a read the helper DOES serve is now a transport failure — the
	// control, so the two answers cannot be the same one by accident.
	if _, err := repo.Status(ctx); err == nil {
		t.Error("Status succeeded with the client closed; the control is not measuring what it says")
	}
}
