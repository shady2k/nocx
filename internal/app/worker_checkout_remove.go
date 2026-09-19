package app

// The removal half of the checkouts service (nocx-xn63t.1.5): a coordinator
// removes one or more of nocx's own leftover checkouts of ITS repository,
// explicitly and by name.
//
// THE WALK IS THE HOLDINGS WALK, and that is the design rather than a
// convenience: session → pane → recorded directory → repository is the one
// derivation of "which repository is the caller's", the join against the
// record's held answer is the one separation of held from abandoned, and the
// location under the worktrees root is the one marker of "nocx's". A second
// walk here would be a second answer that agrees until the day it doesn't.
// What is added on top is only the refusals and the two writes: the git
// seam's own RemoveWorktree — which refuses uncommitted work itself and has
// no force — and the row drop that keeps the durable record from listing a
// checkout it no longer has.
//
// THE BRANCH ALWAYS STAYS. RemoveWorktree leaves it, and nothing here calls
// DeleteWorktreeBranch: that operation exists for the one compensation a
// spawn may take back, and a removal's branch may hold work the worker
// committed. A person who wants the branch gone has a shell.
//
// THE REFUSALS ARE CHECKED IN ORDER, the spawn path's rule: the answer names
// the FIRST thing in the way, and each is the small set's own name —
// uncommitted, held, not-ours, unresolved — so a caller can act on it
// without parsing prose.
//
// The small surface other code removes through (the automatic sweep of old
// checkouts, task 1.6, removes through exactly this path and under exactly
// these refusals) is the RemoveCheckouts method itself, reached as
// checkoutRemover below; the assistant's tool executes through the same
// method on the same service value.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/workers"
)

// checkoutRemover is the removal through the holdings walk and its
// refusals — the seam the sweep of task 1.6 will hold, and the one the
// composition root already satisfies with the value it hands the assistant.
// Named here rather than taken as *workerCheckouts so a caller cannot reach
// the service's reading half by accident and a test can stand a double in.
type checkoutRemover interface {
	RemoveCheckouts(ctx context.Context, coordinatorSession string, refs []workers.CheckoutRef) workers.CheckoutRemoval
}

// RemoveCheckouts removes the asked checkouts of the caller's repository and
// answers one row per ask, in the order asked. A checkout is removed only
// when nocx could establish all of: it is a working tree of the caller's
// repository, it is one of nocx's (under the worktrees root, not the main
// checkout), no live worker holds it, and its working state could be read.
// Anything short of that is refused by name and left exactly as it was.
//
// A read that FAILS never becomes one of the checkout refusals: while the
// record's held answer cannot be read, held and abandoned cannot be told
// apart, and offering a live worker's checkout up for removal is the one
// mistake this method must never make — so every row answers unresolved and
// nothing on disk moves. A coordinator standing nowhere answers not-ours on
// every row: nothing was established as nocx's, because no repository of
// theirs was ever resolved.
func (c *workerCheckouts) RemoveCheckouts(ctx context.Context, coordinatorSession string, refs []workers.CheckoutRef) workers.CheckoutRemoval {
	lg := log.From(ctx)
	removal := workers.CheckoutRemoval{Items: make([]workers.RemovedCheckout, 0, len(refs))}
	refuseAll := func(kind workers.CheckoutRefusal, detail string) workers.CheckoutRemoval {
		items := make([]workers.RemovedCheckout, 0, len(refs))
		for _, ref := range refs {
			items = append(items, workers.RemovedCheckout{
				Path: ref.Path, Branch: ref.Branch, Refusal: kind, Detail: detail,
			})
		}
		return workers.CheckoutRemoval{Items: items}
	}
	if len(refs) == 0 {
		return removal
	}
	// THE MISSING INPUTS, exactly the holdings walk's: without the seam, the
	// session walk, the record's held answer and the durable rows, held and
	// abandoned cannot be told apart — and a removal that cannot tell them
	// apart removes nothing at all.
	if c.repos == nil || c.sessions == nil || c.layout == nil || c.held == nil || c.rows == nil {
		return refuseAll(workers.CheckoutRefusalUnresolved,
			"this backend cannot survey checkouts (a read the removal needs is not wired), so nothing was removed")
	}

	paneID := coordinatorPaneFor(c.sessions, coordinatorSession, lg)
	if paneID == "" {
		return refuseAll(workers.CheckoutRefusalNotOurs,
			"the coordinator's session resolves to no pane of nocx's, so no repository of yours — and no checkout of it — could be established")
	}
	cwd := coordinatorCwdFor(ctx, c.layout, paneID, lg)
	if cwd == "" {
		return refuseAll(workers.CheckoutRefusalNotOurs,
			"the coordinator's pane has no recorded directory, so no repository of yours could be established")
	}

	repo, outcome, err := c.repos.Open(ctx, cwd)
	if err != nil {
		lg.Warn("worker checkouts: open the coordinator's repository for a removal", "error", err)
		return refuseAll(workers.CheckoutRefusalUnresolved,
			fmt.Sprintf("the repository at %q could not be opened, so nothing could be verified and nothing was removed", cwd))
	}
	if outcome.State != git.OpenOK {
		return refuseAll(workers.CheckoutRefusalNotOurs,
			fmt.Sprintf("the coordinator stands in %q, which is not a repository nocx can open (%s)", cwd, outcome.State))
	}
	defer func() { _ = repo.Close() }()

	ground, fail := readRemovalGround(ctx, lg, repo)
	if fail != nil {
		return refuseAll(fail.Refusal, fail.Detail)
	}
	repoKey := ground.repoKey
	trees := ground.trees

	held, err := c.held.HeldWorktrees(ctx)
	if err != nil {
		lg.Warn("worker checkouts: read the record's held checkouts for a removal", "error", err)
		return refuseAll(workers.CheckoutRefusalUnresolved,
			"the record's held checkouts could not be read, so a live worker's checkout could not be told from a left-over one; nothing was removed")
	}
	heldPaths := make(map[string]bool, len(held))
	for _, wt := range held {
		heldPaths[wt.Path] = true
	}

	treeByPath := make(map[string]git.Worktree, len(trees))
	treeByBranch := make(map[string]git.Worktree, len(trees))
	for _, tree := range trees {
		treeByPath[tree.Path] = tree
		if !tree.Main {
			treeByBranch[tree.Branch] = tree
		}
	}

	removal = c.removeResolved(ctx, lg, repoKey, repo, heldPaths, treeByPath, treeByBranch, refs)
	return removal
}

// removalGround is what every removal decision stands on: the repository's
// key, its HEAD as the base the listing counts from, and the worktree
// listing itself.
type removalGround struct {
	repoKey string
	base    string
	trees   []git.Worktree
}

// readRemovalGround reads HEAD and the worktree listing the removal asks
// against, and derives the repository key. A read that fails answers the
// refusal PROTOTYPE every caller copies into its own answer shape — the
// tool's refuseAll rows, the sweep's per-checkout notes — so the mapping
// from a failed read to a named refusal is spelled here, once.
func readRemovalGround(ctx context.Context, lg log.Logger, repo git.Repo) (removalGround, *workers.RemovedCheckout) {
	head, err := repo.Log(ctx, 1)
	if err != nil || len(head.Entries) == 0 {
		lg.Warn("worker checkouts: read the repository's HEAD", "error", err)
		return removalGround{}, &workers.RemovedCheckout{
			Refusal: workers.CheckoutRefusalUnresolved,
			Detail:  "the repository's HEAD could not be read, so nothing could be verified and nothing was removed",
		}
	}
	base := head.Entries[0].Hash
	trees, err := repo.Worktrees(ctx, base)
	if err != nil {
		lg.Warn("worker checkouts: list the repository's worktrees", "error", err)
		return removalGround{}, &workers.RemovedCheckout{
			Refusal: workers.CheckoutRefusalUnresolved,
			Detail:  "the repository's worktrees could not be listed, so nothing could be verified and nothing was removed",
		}
	}
	if len(trees) == 0 || !trees[0].Main {
		lg.Warn("worker checkouts: the repository reports no main checkout")
		return removalGround{}, &workers.RemovedCheckout{
			Refusal: workers.CheckoutRefusalUnresolved,
			Detail:  "the repository reports no main checkout, which git never does; refusing rather than guessing",
		}
	}
	return removalGround{
		repoKey: nocxCheckoutRepoKey(trees[0].Path),
		base:    base,
		trees:   trees,
	}, nil
}

// removeResolved asks the removal for each ref against a listing the caller
// already read, and drops the removed rows from the durable record — THE
// ROW GOES WITH THE CHECKOUT: the record must not go on naming a checkout
// it no longer has. A failed drop leaves an invisible row, never a wrong
// checkout — the same convenience the holdings read's drop-on-read provides.
func (c *workerCheckouts) removeResolved(
	ctx context.Context,
	lg log.Logger,
	repoKey string,
	repo git.Repo,
	heldPaths map[string]bool,
	treeByPath map[string]git.Worktree,
	treeByBranch map[string]git.Worktree,
	refs []workers.CheckoutRef,
) workers.CheckoutRemoval {
	removal := workers.CheckoutRemoval{Items: make([]workers.RemovedCheckout, 0, len(refs))}
	removed := make([]string, 0, len(refs))
	for _, ref := range refs {
		item := c.removeOne(ctx, lg, repo, heldPaths, treeByPath, treeByBranch, ref)
		if item.Removed {
			removed = append(removed, item.Path)
		}
		removal.Items = append(removal.Items, item)
	}
	if len(removed) > 0 {
		if err := c.rows.Delete(ctx, repoKey, removed); err != nil {
			lg.Warn("worker checkouts: drop the removed checkouts' rows", "error", err)
		}
	}
	return removal
}

// removeOne resolves one ask and removes what it names, refusing by name at
// the first thing in the way. Resolution is against the listing the call
// already read: a path must be one of this repository's working trees, a
// branch the one tree holding it. The answer's path and branch are the
// checkout as resolved — what is true on disk is in the answer.
func (c *workerCheckouts) removeOne(
	ctx context.Context,
	lg log.Logger,
	repo git.Repo,
	heldPaths map[string]bool,
	treeByPath map[string]git.Worktree,
	treeByBranch map[string]git.Worktree,
	ref workers.CheckoutRef,
) workers.RemovedCheckout {
	refuse := func(path, branch string, kind workers.CheckoutRefusal, format string, args ...any) workers.RemovedCheckout {
		return workers.RemovedCheckout{
			Path: path, Branch: branch,
			Refusal: kind,
			Detail:  fmt.Sprintf(format, args...),
		}
	}

	var tree git.Worktree
	switch {
	case ref.Path != "":
		asked := filepath.Clean(ref.Path)
		found, ok := treeByPath[asked]
		if !ok {
			return refuse(asked, ref.Branch, workers.CheckoutRefusalNotOurs,
				"no working tree of this repository sits at %q, so it is not one of nocx's checkouts", asked)
		}
		tree = found
		if ref.Branch != "" && ref.Branch != tree.Branch {
			return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalNotOurs,
				"the checkout at %q holds branch %q, not %q; the two names must agree", tree.Path, tree.Branch, ref.Branch)
		}
	case ref.Branch != "":
		found, ok := treeByBranch[ref.Branch]
		if !ok {
			return refuse("", ref.Branch, workers.CheckoutRefusalNotOurs,
				"no checkout of this repository holds branch %q, so there is nothing of nocx's to remove", ref.Branch)
		}
		tree = found
	default:
		return refuse("", "", workers.CheckoutRefusalNotOurs,
			"the ask names neither a path nor a branch, so no checkout could be resolved")
	}

	// THE LOCATION IS THE MARKER, before anything else about the checkout is
	// judged: the main checkout and a person's own linked worktrees are
	// working trees of this repository and are never nocx's to remove.
	if tree.Main {
		return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalNotOurs,
			"%q is the repository's main checkout, which is never one of nocx's checkouts", tree.Path)
	}
	if !c.underWorktreeRoot(tree.Path) {
		return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalNotOurs,
			"the checkout at %q is outside nocx's worktrees directory, so it is not one of nocx's checkouts", tree.Path)
	}
	if heldPaths[tree.Path] {
		return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalHeldByWorker,
			"a live worker holds the checkout at %q; close the worker first — its checkout is not left over", tree.Path)
	}
	if tree.State == git.WorktreeUnreadable {
		return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalUnresolved,
			"the checkout's working state could not be read (%s), and unreadable is never clean; nothing was removed", tree.Reason)
	}

	// The seam refuses uncommitted work itself, and has no force: the
	// answers below are the seam's own refusals, carried rather than
	// re-decided here.
	if err := repo.RemoveWorktree(ctx, tree.Path); err != nil {
		lg.Warn("worker checkouts: remove the checkout", "path", tree.Path, "error", err)
		switch {
		case errors.As(err, new(*git.ErrWorktreeUncommitted)):
			return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalUncommitted,
				"%s; nothing was removed", err)
		case errors.As(err, new(*git.ErrWorktreeUnreadable)):
			return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalUnresolved,
				"%s; what is on disk is not claimed", err)
		case errors.As(err, new(*git.ErrNotAWorktree)), errors.As(err, new(*git.ErrMainWorktree)):
			return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalNotOurs,
				"%s; the repository changed while the ask was being decided", err)
		default:
			return refuse(tree.Path, tree.Branch, workers.CheckoutRefusalUnresolved,
				"the removal failed: %s; what is on disk now is not claimed", err)
		}
	}
	lg.Info("worker checkouts: the checkout is removed", "path", tree.Path, "branch", tree.Branch)
	return workers.RemovedCheckout{Path: tree.Path, Branch: tree.Branch, Removed: true}
}
