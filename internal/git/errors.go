package git

import (
	"fmt"
)

// Domain error markers for the git package. Transport switches on these to
// surface the right user-facing state; each wraps a distinguishable type the
// UI layer can map to an action, the way internal/ssh/errors.go does for
// connection failures. Invocation failures that carry no state of their own
// (a git command that exits non-zero) are ordinary fmt errors whose message
// includes git's own output — the transport re-polls on any of them.

// ErrNothingToCommit — Commit was refused before invocation because nothing
// is staged. Running git commit here would run the pre-commit hook and then
// fail confusingly; the refusal happens first (spec §5.1 "commit.go").
type ErrNothingToCommit struct{}

func (e *ErrNothingToCommit) Error() string { return "git: nothing is staged to commit" }

// ErrAmendUnborn — Commit with amend=true was refused before invocation
// because the branch is unborn: there is nothing to amend, and git's own
// answer ("You have nothing to amend") is a post-hoc refusal of an operation
// we already know is impossible.
type ErrAmendUnborn struct{}

func (e *ErrAmendUnborn) Error() string { return "git: cannot amend a commit on an unborn branch" }

// ErrConflicted — StageAll or UnstageAll was refused while any entry is
// conflicted (D19). Both operations are destructive in exactly that state:
// git add -A marks the conflict resolved using the marker-laden worktree
// file, and bare git reset deletes .git/MERGE_HEAD — silently aborting the
// merge. Measured on git 2.55, not reasoned.
type ErrConflicted struct {
	Path string
}

func (e *ErrConflicted) Error() string {
	return fmt.Sprintf("git: cannot stage or unstage all while %q is conflicted", e.Path)
}

// ErrNoRemote — RemoteURL found nothing to open: the branch is detached,
// has no upstream, or tracks a remote that does not exist. This is the
// ordinary "the panel draws no link" case (design D14), never an error: the
// transport maps it to the result state "none", the same way ErrConflicted
// maps to a renderable refusal rather than a wire error.
type ErrNoRemote struct{}

func (e *ErrNoRemote) Error() string { return "git: no remote to open" }

// ── the worktree refusals (brief nocx-xn63t.1.1) ───────────────────────
//
// Each names what was refused and the fact the caller needs to say something
// useful about it. They are declared here, with the git domain states and not
// in the local implementation, because the transport switches on them (spec
// D16: what crosses the seam is the domain's vocabulary, and the same
// refusals will have to be answerable by a helper implementation).

// ErrBranchCheckedOut — the branch is checked out in a worktree, so it cannot
// be placed in another one or deleted: a branch belongs to one working tree at
// a time, and both operations would either fail or leave that worktree's HEAD
// pointing at nothing. Path names the worktree that holds it, because "the
// branch is in use" without a location is a message the caller cannot act on.
type ErrBranchCheckedOut struct {
	Branch string
	Path   string
}

func (e *ErrBranchCheckedOut) Error() string {
	return fmt.Sprintf("git: branch %q is checked out at %q", e.Branch, e.Path)
}

// ErrBaseUnresolved — the base names no commit in this repository, so a
// branch cannot be created at it and "ahead of base" cannot be counted. It is
// checked before anything is written, which is what makes the refusal leave
// the repository exactly as it was.
type ErrBaseUnresolved struct {
	Base string
}

func (e *ErrBaseUnresolved) Error() string {
	return fmt.Sprintf("git: base %q does not name a commit", e.Base)
}

// ErrWorktreePathNotEmpty — the path a worktree was to be created at exists
// and is not an empty directory. An empty directory is allowed (git fills it,
// measured on 2.55); anything else would be overwritten, and this operation
// never removes what it found.
type ErrWorktreePathNotEmpty struct {
	Path string
}

func (e *ErrWorktreePathNotEmpty) Error() string {
	return fmt.Sprintf("git: %q already exists and is not an empty directory", e.Path)
}

// ErrWorktreeUncommitted — RemoveWorktree refused a worktree holding work
// that is in no commit: a tracked change or an untracked file. Changes is how
// many status records git reported, so the refusal can say how much without
// the caller re-reading.
type ErrWorktreeUncommitted struct {
	Path    string
	Changes int
}

func (e *ErrWorktreeUncommitted) Error() string {
	return fmt.Sprintf("git: worktree %q holds %d uncommitted change(s); nothing was removed", e.Path, e.Changes)
}

// ErrWorktreeUnreadable — the worktree's working state could not be read, so
// nothing about it is known: not that it is dirty, and NOT that it is clean.
// RemoveWorktree refuses it rather than treat the second as the answer, and
// Worktrees reports the same fact as the WorktreeUnreadable state, whose
// Reason is this error's account. The two presentations are one decision,
// made in one place.
//
// Cause is carried rather than flattened into a message: it is the
// invocation's own error when git could not be run, or the work ceiling when
// a bounded read was stopped, and errors.Is still reaches it. That also
// matters on the wire, where only the text crosses — the caller that can act
// on a specific failure knows it here, and the string is what the rest see.
type ErrWorktreeUnreadable struct {
	Path  string
	Cause error
}

func (e *ErrWorktreeUnreadable) Error() string {
	return fmt.Sprintf("git: the state of worktree %q could not be read: %v", e.Path, e.Cause)
}

// Unwrap exposes the cause, so a caller can tell "git could not be run" from
// "the read was stopped at the ceiling" without parsing the message.
func (e *ErrWorktreeUnreadable) Unwrap() error { return e.Cause }

// ErrMainWorktree — RemoveWorktree was asked for the repository's main
// checkout. The main working tree IS the repository to everybody looking at
// it, and removing it is a different operation with a different blast radius
// (the .git directory goes with it); this seam removes linked worktrees.
type ErrMainWorktree struct {
	Path string
}

func (e *ErrMainWorktree) Error() string {
	return fmt.Sprintf("git: %q is a main working tree, not a linked worktree", e.Path)
}

// ErrNotAWorktree — the path is not a working tree of this repository. It is
// the honest answer for a directory that belongs to somebody else, for a path
// that is not there, and for a worktree of ANOTHER repository: none of the
// three is something this repository may delete.
type ErrNotAWorktree struct {
	Path string
}

func (e *ErrNotAWorktree) Error() string {
	return fmt.Sprintf("git: %q is not a working tree of this repository", e.Path)
}

// ErrBranchHasCommits — DeleteWorktreeBranch refused: the branch reaches
// commits that base does not, which is exactly what the caller may not
// delete. Ahead is git's count over base..branch, so the refusal says how much
// work is being protected rather than only that something is.
type ErrBranchHasCommits struct {
	Branch string
	Base   string
	Ahead  int
}

func (e *ErrBranchHasCommits) Error() string {
	return fmt.Sprintf("git: branch %q has %d commit(s) that %q does not reach", e.Branch, e.Ahead, e.Base)
}
