package app

// The worktree half of a worker spawn (nocx-xn63t.1.2): when a coordinator
// asks workers.spawn for a worktree, the participant's pane opens in a fresh
// linked checkout of ITS OWN repository, created through the git seam
// (internal/git — AddWorktree, and RemoveWorktree/DeleteWorktreeBranch for
// the compensation). Nothing here shells out to git and nothing here derives
// a second answer to a question the seam already owns: which checkout is the
// repository's MAIN one is the seam's Worktrees answer (git reports it first,
// precisely because the Repo this call opened may itself be bound to a linked
// worktree), and whether the branch may be placed is AddWorktree's.
//
// THE LOCATION IS THE MARKER. A nocx-made checkout lives under nocx's own
// application directory — internal/storage's build-tagged profile, so a dev
// stand and a shipped build never share one — at
//
//	<datadir>/worktrees/<repo key>/<branch slug>
//
// No other marker is written: the location is what marks it. <repo key> is
// the main checkout's basename plus "-" and the first 8 hex chars of
// sha256 over the absolute path of the repository's common git dir (the main
// checkout's .git), so two repositories that happen to share a basename do
// not collide. <branch slug> is the branch with "/" replaced by "-".
//
// THE INTERVAL. The checkout exists from before the participant's pane opens
// until either the participant goes live — the record accepts it at MarkLive
// (internal/workers) — or the compensation has removed it. Everything here
// before AddWorktree refuses without having written anything (the seam
// guarantees the same for its own internals), and once AddWorktree succeeds
// the worktreeUndo below EXISTS and its run belongs to every failure path
// until live.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/workers"
)

// The named refusals a worktree spawn answers before anything is minted.
// Each is a distinct fact a coordinator can act on, which is why they are
// separate names and not one "cannot" — and why each is checked in the order
// planWorktree asks them, so the answer names the FIRST thing in the way.
var (
	// errSpawnWorktreeUnwired — this backend was built without the two
	// facts a checkout needs: a git factory and a place to put checkouts.
	// Production always wires both; a nil is a composition-root gap, and a
	// refusal that says so beats a spawn that silently ignores the ask.
	errSpawnWorktreeUnwired = errors.New("worker spawn: this backend cannot create worktrees (no git seam wired)")
	// errSpawnWorktreeNoCwd — the coordinator's pane never reported a
	// directory, so there is no repository to branch from. The plain spawn
	// falls back to $HOME here; a checkout has no honest fallback.
	errSpawnWorktreeNoCwd = errors.New("worker spawn: the coordinator's pane has no recorded directory, so there is no repository to create a worktree from")
	// errSpawnWorktreeNoPaneRow — the coordinator's pane is in no tab this
	// window knows, so its row (and with it the local/remote kind) cannot
	// be read.
	errSpawnWorktreeNoPaneRow = errors.New("worker spawn: the coordinator's pane row could not be read, so nocx cannot establish the pane is on this machine")
	// errSpawnWorktreeNotLocal — the pane is an ssh pane. Its cwd names a
	// path on another machine, and a worktree created "there" would be
	// created here from a name that happens to match. Local only, by
	// decision.
	errSpawnWorktreeNotLocal = errors.New("worker spawn: the coordinator's pane is on a remote host, and a worktree can only be created on the machine nocx itself runs on")
	// errSpawnWorktreePathExists — the deterministic path is already taken.
	// The location is the marker, so anything there — an earlier spawn's
	// checkout, or a directory a person made — owns the name. There is no
	// retry with a different name and nothing here removes what it found.
	errSpawnWorktreePathExists = errors.New("worker spawn: the worktree path already exists")
)

// worktreeUndo is the checkout a spawn created, held together with
// everything removing it needs. It is the PLAN and the COMPENSATION in one
// value on purpose: from the moment AddWorktree returns until the record
// goes live, the checkout's only remaining legitimate outcome is "kept,
// because the participant went live", and every other outcome reaches this
// value's run. It is carried by value inside the pointer so it survives past
// the Repo the plan used; the undo re-opens the repository on its own,
// because it can be driven long after Spawn returned — a launcher whose
// enrolment never arrives is compensated by internal/workers' own retry,
// minutes later, on a context that is not Spawn's.
//
// base is the RESOLVED commit the branch starts from — HEAD's own hash for
// the default, the caller's revision as named otherwise — because it is what
// DeleteWorktreeBranch deletes the branch against, and a compensation that
// deleted against a movable name would delete the wrong amount of branch.
type worktreeUndo struct {
	repos   git.RepoFactory
	cwd     string
	repoKey string
	path    string
	branch  string
	base    string
	created bool
}

// location is the record's view of the same facts: what
// workers.WorktreeSource carries to MarkLive.
func (u *worktreeUndo) location() workers.Worktree {
	if u == nil {
		return workers.Worktree{}
	}
	return workers.Worktree{Path: u.path, Branch: u.branch, Base: u.base}
}

// run removes the checkout and — only when THIS spawn created the branch —
// the branch with it. A branch the spawn merely checked out stays: it is
// somebody else's, and DeleteWorktreeBranch exists precisely for the one
// branch a spawn may take back. Both halves are the seam's own operations;
// a removal that fails (a worktree holding work, a branch that grew commits)
// fails the undo rather than being forced — the seam has no force, by
// design, because the thing it would force past is a worker's work.
func (u *worktreeUndo) run(ctx context.Context) error {
	if u == nil {
		return nil
	}
	repo, outcome, err := u.repos.Open(ctx, u.cwd)
	if err != nil {
		return fmt.Errorf("worker spawn: open the repository to remove the worktree: %w", err)
	}
	if outcome.State != git.OpenOK {
		return fmt.Errorf("worker spawn: the coordinator's checkout answers %q now, so the worktree at %q could not be removed", outcome.State, u.path)
	}
	defer func() { _ = repo.Close() }()
	if err := repo.RemoveWorktree(ctx, u.path); err != nil {
		return fmt.Errorf("worker spawn: remove the worktree at %q: %w", u.path, err)
	}
	if u.created {
		if err := repo.DeleteWorktreeBranch(ctx, u.branch, u.base); err != nil {
			return fmt.Errorf("worker spawn: delete the branch %q this spawn created: %w", u.branch, err)
		}
	}
	return nil
}

// planWorktree creates the checkout a worktree ask names and answers the
// undo that owns it. cwd is the coordinator pane's directory — already read
// by Spawn, and empty means the refusal, because a checkout from nowhere is
// not a thing. Every rung below the first refusal leaves nothing behind; the
// first line that writes anything is AddWorktree, and after it succeeds the
// returned undo EXISTS and every later failure in the spawn owes it a run.
//
// The base is resolved HERE, at spawn time, and not by the record: "" means
// the coordinator checkout's HEAD commit, read as a hash — a hash is the one
// spelling of "now" that cannot drift before a later compensation runs. A
// base the caller named is carried as named and validated by the seam.
func (s *workerSpawner) planWorktree(ctx context.Context, lg log.Logger, coordPane, cwd string, ask *workers.WorktreeAsk) (*worktreeUndo, error) {
	if s.repos == nil || s.worktreeRoot == "" {
		return nil, errSpawnWorktreeUnwired
	}
	if cwd == "" {
		return nil, errSpawnWorktreeNoCwd
	}
	local, err := s.coordinatorPaneIsLocal(ctx, coordPane, lg)
	if err != nil {
		return nil, err
	}
	if !local {
		return nil, errSpawnWorktreeNotLocal
	}

	repo, outcome, err := s.repos.Open(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("worker spawn: open the coordinator's repository at %q: %w", cwd, err)
	}
	if outcome.State != git.OpenOK {
		return nil, fmt.Errorf("worker spawn: %q is not a repository nocx can open (%s), so there is nothing to create a worktree of", cwd, outcome.State)
	}
	defer func() { _ = repo.Close() }()

	// THE BASE, resolved before anything is asked of the seam that needs
	// one: Worktrees counts ahead of it and AddWorktree creates at it.
	base := ask.Base
	if base == "" {
		head, readErr := repo.Log(ctx, 1)
		if readErr != nil {
			return nil, fmt.Errorf("worker spawn: read the coordinator's HEAD: %w", readErr)
		}
		if len(head.Entries) == 0 {
			return nil, fmt.Errorf("worker spawn: the repository at %q has no commits, so there is nothing to branch from", cwd)
		}
		base = head.Entries[0].Hash
	}

	// THE MAIN CHECKOUT, from the seam's own listing and never from this
	// Repo's toplevel: the coordinator may be standing in a linked worktree,
	// and the key must not then name the worktree. The common git dir is the
	// main checkout's .git — the repository the factory opened is never
	// bare (a bare checkout answers notARepository, having no toplevel), so
	// the layout fact is git's own.
	trees, err := repo.Worktrees(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("worker spawn: list the coordinator's repository's worktrees: %w", err)
	}
	if len(trees) == 0 || !trees[0].Main {
		return nil, fmt.Errorf("worker spawn: the repository at %q reports no main checkout, which git never does; refusing rather than guessing the key", cwd)
	}
	repoKey := nocxCheckoutRepoKey(trees[0].Path)

	// The root goes in canonical (nocxCanonicalPath): the checkout, the
	// record row and the spawn result are born with the one spelling every
	// later comparison — the removal guard, the sweep, git's own answers —
	// uses, so a symlinked ancestor (macOS /var → /private/var) cannot
	// split the location from the people who look for it (nocx-xn63t.1.2).
	path := filepath.Join(nocxCanonicalPath(s.worktreeRoot), repoKey, strings.ReplaceAll(ask.Branch, "/", "-"))
	if _, statErr := os.Lstat(path); statErr == nil {
		return nil, fmt.Errorf("%w: %q (a checkout nocx or somebody else put there is not replaced; pick another branch or remove it)", errSpawnWorktreePathExists, path)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("worker spawn: check whether %q is free: %w", path, statErr)
	}

	added, err := repo.AddWorktree(ctx, ask.Branch, base, path)
	if err != nil {
		// The seam's own refusals ARE the answer — ErrBranchCheckedOut
		// carries where the branch is held, ErrWorktreePathNotEmpty the
		// path that filled between the check above and this call — so they
		// reach the coordinator wrapped, not re-worded.
		return nil, fmt.Errorf("worker spawn: create the worktree for branch %q: %w", ask.Branch, err)
	}
	lg.Info("worker spawn: the participant's checkout exists",
		"path", path, "branch", ask.Branch, "base", base, "created_branch", added.Created)
	return &worktreeUndo{
		repos: s.repos, cwd: cwd, repoKey: repoKey,
		path: path, branch: ask.Branch, base: base, created: added.Created,
	}, nil
}

// coordinatorPaneIsLocal answers whether the coordinator's pane runs on this
// machine, from the pane row's own kind — the layout row is the one owner of
// what a pane is (the same row Spawn reads the coordinator's cwd from), and
// a spawn that must not create a checkout on a far machine reads the row
// rather than guessing from the cwd's shape.
func (s *workerSpawner) coordinatorPaneIsLocal(ctx context.Context, coordPane string, lg log.Logger) (bool, error) {
	if coordPane == "" || s.layout == nil {
		return false, errSpawnWorktreeNoPaneRow
	}
	tab, err := s.layout.TabForPane(ctx, coordPane)
	if err != nil || tab == "" {
		lg.Debug("worker spawn: the coordinator's pane is in no tab, so its kind is unknown",
			"pane_id", coordPane, "error", err)
		return false, errSpawnWorktreeNoPaneRow
	}
	panes, err := s.layout.Panes(ctx, tab)
	if err != nil {
		lg.Debug("worker spawn: the coordinator's tab's panes could not be read",
			"pane_id", coordPane, "tab_id", tab, "error", err)
		return false, errSpawnWorktreeNoPaneRow
	}
	for _, pane := range panes {
		if pane.ID != coordPane {
			continue
		}
		if pane.Kind != content.PaneLocal {
			lg.Debug("worker spawn: the coordinator's pane is not local",
				"pane_id", coordPane, "kind", pane.Kind)
			return false, nil
		}
		return true, nil
	}
	lg.Debug("worker spawn: the coordinator's pane is not among its tab's panes", "pane_id", coordPane, "tab_id", tab)
	return false, errSpawnWorktreeNoPaneRow
}
