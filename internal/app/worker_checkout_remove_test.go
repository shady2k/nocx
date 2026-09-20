package app

// The explicit removal of nocx's own leftover checkouts (nocx-xn63t.1.5),
// end to end over the REAL git binary, the REAL local factory and a REAL
// content store — the same stand the holdings answer is proven on, because
// the removal walks the same walk and must refuse where that walk refuses.
//
// The brief's acceptance criteria, each with its test:
//
//   - a clean leftover checkout is removed, its branch still exists, and
//     holdings no longer lists it
//     (TestACleanLeftoverCheckoutIsRemovedAndItsBranchStays);
//   - the three named refusals — uncommitted work, a live worker's hold,
//     not one of nocx's checkouts — each leave the checkout exactly as it
//     was, and each sits beside the success above
//     (TestADirtyCheckoutIsRefusedByNameAndNothingIsRemoved,
//     TestAHeldCheckoutIsRefusedByNameAndNothingIsRemoved,
//     TestAPathThatIsNotNocxCheckoutIsRefusedAndNothingIsRemoved);
//   - several in one call, one of them dirty: the clean ones are removed,
//     the result names which was refused and why
//     (TestSeveralInOneCallRemoveTheCleanAndNameTheRefused);
//   - a read the decision needs that fails refuses everything as
//     unresolved, because held and abandoned cannot then be told apart
//     (TestAFailingHeldReadRefusesEverythingAsUnresolved).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/workers"
)

// Criterion 1, whole: a clean leftover checkout removed by path — the
// directory gone, the branch it held still in the repository, the durable
// row dropped with it, and a holdings ask afterwards listing nothing.
func TestACleanLeftoverCheckoutIsRemovedAndItsBranchStays(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "the sweep task")
	path := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")
	key := nocxCheckoutRepoKey(repoDir)

	// A NEW coordinator session in the same repository — the one that
	// inherited the leftover — does the removing.
	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	got := stand.checkouts.RemoveCheckouts(context.Background(), string(coordB),
		[]workers.CheckoutRef{{Path: path}})

	if len(got.Items) != 1 {
		t.Fatalf("items = %+v, want one answer", got.Items)
	}
	item := got.Items[0]
	if !item.Removed || item.Refusal != "" {
		t.Fatalf("item = %+v, want the checkout removed", item)
	}
	if item.Path != path || item.Branch != "feat/one" {
		t.Fatalf("item = %+v, want the resolved path and branch", item)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the checkout at %q is still on disk after the removal", path)
	}
	// THE BRANCH ALWAYS STAYS: it was the worker's committed work, and the
	// removal had no more claim on it than the close did.
	gitRun(t, repoDir, "rev-parse", "--verify", "refs/heads/feat/one")

	survey := stand.checkouts.Leftovers(context.Background(), string(coordB))
	if !survey.Complete || len(survey.Leftovers) != 0 {
		t.Fatalf("holdings after the removal = %+v, want nothing left over", survey)
	}
	rows, err := stand.rows.List(context.Background(), key)
	if err != nil || len(rows) != 0 {
		t.Fatalf("the record still holds %+v (%v) after the removal dropped it", rows, err)
	}
}

// Criterion 1, by branch: the ask names the branch, the answer names the
// path it resolved to — what is true on disk is in the answer.
func TestARemovalByBranchNamesThePathItRemoved(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/by/branch", "t")
	path := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/by/branch")

	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	got := stand.checkouts.RemoveCheckouts(context.Background(), string(coordB),
		[]workers.CheckoutRef{{Branch: "feat/by/branch"}})

	if len(got.Items) != 1 || !got.Items[0].Removed {
		t.Fatalf("items = %+v, want the checkout removed", got.Items)
	}
	if got.Items[0].Path != path {
		t.Fatalf("item = %+v, want the path the branch resolved to (%q)", got.Items[0], path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the checkout at %q is still on disk", path)
	}
}

// Criterion 2, uncommitted: a checkout holding work no commit keeps is
// refused by name, the file is exactly where it was, and holdings still
// lists the checkout — nothing moved.
func TestADirtyCheckoutIsRefusedByNameAndNothingIsRemoved(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/dirty", "t")
	path := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/dirty")
	note := filepath.Join(path, "uncommitted-notes.txt")
	if err := os.WriteFile(note, []byte("the worker's findings"), 0o600); err != nil {
		t.Fatalf("write the uncommitted work: %v", err)
	}

	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	got := stand.checkouts.RemoveCheckouts(context.Background(), string(coordB),
		[]workers.CheckoutRef{{Path: path}})

	if len(got.Items) != 1 {
		t.Fatalf("items = %+v, want one answer", got.Items)
	}
	item := got.Items[0]
	if item.Removed || item.Refusal != workers.CheckoutRefusalUncommitted {
		t.Fatalf("item = %+v, want the uncommitted refusal", item)
	}
	if item.Detail == "" {
		t.Fatal("the refusal says no why; a coordinator cannot act on a bare no")
	}
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("the uncommitted work did not survive the refusal: %v", err)
	}
	gitRun(t, repoDir, "rev-parse", "--verify", "refs/heads/feat/dirty")

	survey := stand.checkouts.Leftovers(context.Background(), string(coordB))
	if !survey.Complete || len(survey.Leftovers) != 1 {
		t.Fatalf("holdings after the refusal = %+v, want the checkout still listed", survey)
	}
}

// Criterion 2, held: a live worker's checkout is refused by name even
// though it is clean — a checkout somebody's worker stands in is not left
// over, and a removal that took it would take the worker's feet.
func TestAHeldCheckoutIsRefusedByNameAndNothingIsRemoved(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/held", "t")
	path := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/held")
	stand.holdCheckout(t, "worker-1", string(coordA), path, "feat/held")

	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	got := stand.checkouts.RemoveCheckouts(context.Background(), string(coordB),
		[]workers.CheckoutRef{{Path: path}})

	if len(got.Items) != 1 {
		t.Fatalf("items = %+v, want one answer", got.Items)
	}
	item := got.Items[0]
	if item.Removed || item.Refusal != workers.CheckoutRefusalHeldByWorker {
		t.Fatalf("item = %+v, want the held refusal", item)
	}
	if item.Detail == "" {
		t.Fatal("the refusal says no why")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the held checkout did not survive the refusal: %v", err)
	}
}

// Criterion 2, not ours: the main checkout, a person's own linked worktree,
// a path that is no working tree of the repository at all, and a branch no
// nocx checkout holds — all refused by name, none of them touched. The
// owner's own worktrees are never this act's to touch.
func TestAPathThatIsNotNocxCheckoutIsRefusedAndNothingIsRemoved(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/real", "t")
	realPath := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/real")

	// A linked worktree a person made by hand, OUTSIDE nocx's worktrees
	// directory — the shape the location marker exists to exclude.
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, repoDir, "worktree", "add", linked, "-b", "linked-branch")

	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	for _, tc := range []struct {
		name string
		ref  workers.CheckoutRef
	}{
		{"the main checkout", workers.CheckoutRef{Path: repoDir}},
		{"a person's own linked worktree, by path", workers.CheckoutRef{Path: linked}},
		{"a person's own linked worktree, by branch", workers.CheckoutRef{Branch: "linked-branch"}},
		{"a path that is no working tree", workers.CheckoutRef{Path: filepath.Join(t.TempDir(), "nowhere")}},
		{"a branch nothing holds", workers.CheckoutRef{Branch: "feat/nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stand.checkouts.RemoveCheckouts(context.Background(), string(coordB),
				[]workers.CheckoutRef{tc.ref})
			if len(got.Items) != 1 {
				t.Fatalf("items = %+v, want one answer", got.Items)
			}
			item := got.Items[0]
			if item.Removed || item.Refusal != workers.CheckoutRefusalNotOurs {
				t.Fatalf("item = %+v, want the not-ours refusal", item)
			}
			if item.Detail == "" {
				t.Fatal("the refusal says no why")
			}
		})
	}
	// AND NOTHING WAS TOUCHED: nocx's own checkout and the person's linked
	// worktree are both exactly where they were.
	for _, still := range []string{realPath, linked} {
		if _, err := os.Stat(still); err != nil {
			t.Fatalf("%q did not survive the refusals: %v", still, err)
		}
	}
	gitRun(t, repoDir, "rev-parse", "--verify", "refs/heads/linked-branch")
}

// Criterion 3, several in one call: the clean ones are removed, the dirty
// one is refused with its why, and the answer keeps the ask's order so a
// coordinator can line the rows up with what it asked for.
func TestSeveralInOneCallRemoveTheCleanAndNameTheRefused(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-a", "feat/clean-a", "t")
	stand.spawnCheckoutWorker(t, coordA, "worker-b", "feat/dirty", "t")
	stand.spawnCheckoutWorker(t, coordA, "worker-c", "feat/clean-b", "t")
	dirtyPath := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/dirty")
	if err := os.WriteFile(filepath.Join(dirtyPath, "findings.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write the dirty checkout's work: %v", err)
	}

	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	got := stand.checkouts.RemoveCheckouts(context.Background(), string(coordB), []workers.CheckoutRef{
		{Branch: "feat/clean-a"},
		{Path: dirtyPath},
		{Branch: "feat/clean-b"},
	})
	if len(got.Items) != 3 {
		t.Fatalf("items = %+v, want one answer per ask", got.Items)
	}
	want := []struct {
		removed bool
		refusal workers.CheckoutRefusal
		branch  string
	}{
		{removed: true, branch: "feat/clean-a"},
		{refusal: workers.CheckoutRefusalUncommitted, branch: "feat/dirty"},
		{removed: true, branch: "feat/clean-b"},
	}
	for i, w := range want {
		item := got.Items[i]
		if item.Removed != w.removed || item.Refusal != w.refusal {
			t.Fatalf("item %d = %+v, want removed=%v refusal=%q", i, item, w.removed, w.refusal)
		}
		if item.Branch != w.branch {
			t.Fatalf("item %d branch = %q, want %q", i, item.Branch, w.branch)
		}
		if !w.removed && item.Detail == "" {
			t.Fatalf("item %d was refused with no why", i)
		}
	}
	if _, err := os.Stat(filepath.Join(dirtyPath, "findings.txt")); err != nil {
		t.Fatalf("the refused checkout's work did not survive: %v", err)
	}

	survey := stand.checkouts.Leftovers(context.Background(), string(coordB))
	if !survey.Complete || len(survey.Leftovers) != 1 || survey.Leftovers[0].Branch != "feat/dirty" {
		t.Fatalf("holdings after the call = %+v, want exactly the refused checkout still there", survey)
	}
}

// A read the decision NEEDS failing refuses everything as unresolved: while
// the record's held answer cannot be read, held and abandoned cannot be
// told apart, and that is the one mistake a removal must never make.
func TestAFailingHeldReadRefusesEverythingAsUnresolved(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	path := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")

	broken := &workerCheckouts{
		repos:        stand.factory,
		worktreeRoot: stand.checkouts.worktreeRoot,
		rows:         stand.rows,
		sessions:     stand.reg,
		layout:       stand.tabs,
		held:         failingHeldSource{err: errHeldBroken},
	}
	coordB := stand.openCoordinator(t, "pane-b", repoDir)
	got := broken.RemoveCheckouts(context.Background(), string(coordB),
		[]workers.CheckoutRef{{Path: path}, {Branch: "feat/other"}})
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v, want one answer per ask", got.Items)
	}
	for i, item := range got.Items {
		if item.Removed || item.Refusal != workers.CheckoutRefusalUnresolved {
			t.Fatalf("item %d = %+v, want the unresolved refusal", i, item)
		}
		if item.Detail == "" {
			t.Fatalf("item %d was refused with no why", i)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the checkout did not survive the refusal: %v", err)
	}
}

// A coordinator session standing nowhere has established no repository, so
// nothing it names can be established as one of nocx's checkouts: refused,
// not-ours, nothing removed.
func TestARemovalForASessionStandingNowhereIsRefusedNotOurs(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	coordA := stand.openCoordinator(t, "pane-a", repoDir)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	path := expectedWorktreePath(stand.checkouts.worktreeRoot, repoDir, "feat/one")

	got := stand.checkouts.RemoveCheckouts(context.Background(), "no-such-session",
		[]workers.CheckoutRef{{Path: path}})
	if len(got.Items) != 1 {
		t.Fatalf("items = %+v, want one answer", got.Items)
	}
	item := got.Items[0]
	if item.Removed || item.Refusal != workers.CheckoutRefusalNotOurs {
		t.Fatalf("item = %+v, want the not-ours refusal", item)
	}
	if item.Detail == "" {
		t.Fatal("the refusal says no why")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the checkout did not survive the refusal: %v", err)
	}
}

// Criterion, the macOS shape (nocx-xn63t.1.5 evidence): the worktrees root
// sits behind a symlinked ancestor, so git answers the resolved spelling of
// the checkout while nocx's root is the link spelling — and the
// coordinator's pane records the repository through its own symlinked
// ancestor. The removal must still recognise the checkout as nocx's (the
// location marker compares the two spellings canonically), remove it, leave
// the branch, and drop the row, with the ask spelled the way nocx hands
// paths out.
func TestASymlinkedWorktreesRootCheckoutIsStillNocxToRemove(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	root := symlinkedWorktreeRoot(t)
	stand.checkouts.worktreeRoot = root
	stand.spawner.worktreeRoot = root

	repoLink := symlinkedDir(t, repoDir)
	coordA := stand.openCoordinator(t, "pane-a", repoLink)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "the sweep task")
	path := expectedWorktreePath(root, repoLink, "feat/one")

	// A NEW coordinator session in the same repository — the one that
	// inherited the leftover — does the removing.
	coordB := stand.openCoordinator(t, "pane-b", repoLink)
	got := stand.checkouts.RemoveCheckouts(context.Background(), string(coordB),
		[]workers.CheckoutRef{{Path: path}})

	if len(got.Items) != 1 {
		t.Fatalf("items = %+v, want one answer", got.Items)
	}
	item := got.Items[0]
	if !item.Removed || item.Refusal != "" {
		t.Fatalf("item = %+v, want the checkout removed", item)
	}
	if item.Path != path || item.Branch != "feat/one" {
		t.Fatalf("item = %+v, want the resolved path and branch", item)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the checkout at %q is still on disk after the removal", path)
	}
	// THE BRANCH ALWAYS STAYS.
	gitRun(t, repoDir, "rev-parse", "--verify", "refs/heads/feat/one")

	rows, err := stand.rows.List(context.Background(), nocxCheckoutRepoKey(repoLink))
	if err != nil || len(rows) != 0 {
		t.Fatalf("the record still holds %+v (%v) after the removal dropped it", rows, err)
	}
}

// Criterion, the producer-mix shape on the removal: a hold the record
// spells through the symlinked ancestor must still be told from a
// leftover — the removal answers held-by-worker and touches nothing, never
// removes a checkout a live worker holds because two producers spelled the
// path differently.
func TestARemovalOfAHoldRecordedByASymlinkedSpellingIsRefusedHeld(t *testing.T) {
	repoDir, _ := initRealRepo(t)
	stand := newCheckoutStand(t)
	root := symlinkedWorktreeRoot(t)
	stand.checkouts.worktreeRoot = root
	stand.spawner.worktreeRoot = root
	repoLink := symlinkedDir(t, repoDir)
	coordA := stand.openCoordinator(t, "pane-a", repoLink)
	stand.spawnCheckoutWorker(t, coordA, "worker-1", "feat/one", "t")
	checkout := expectedWorktreePath(root, repoLink, "feat/one")
	stand.holdCheckout(t, "worker-1", string(coordA), symlinkedDir(t, checkout), "feat/one")

	coordB := stand.openCoordinator(t, "pane-b", repoLink)
	got := stand.checkouts.RemoveCheckouts(context.Background(), string(coordB),
		[]workers.CheckoutRef{{Path: checkout}})

	if len(got.Items) != 1 {
		t.Fatalf("items = %+v, want one answer", got.Items)
	}
	item := got.Items[0]
	if item.Removed || item.Refusal != workers.CheckoutRefusalHeldByWorker {
		t.Fatalf("item = %+v, want the held-by-worker refusal and nothing removed", item)
	}
	if _, err := os.Stat(checkout); err != nil {
		t.Fatalf("the checkout did not survive the refusal: %v", err)
	}
}

// The service keeps satisfying the small surface other code removes through
// (task 1.6's sweep): one method, the walk's own refusals.
var _ checkoutRemover = (*workerCheckouts)(nil)

var errHeldBroken = errBroken{}

// failingHeldSource is the record with a held answer that cannot be read —
// what a store that failed to open but was wired anyway would be.
type failingHeldSource struct{ err error }

func (f failingHeldSource) HeldWorktrees(context.Context) ([]workers.Worktree, error) {
	return nil, f.err
}
