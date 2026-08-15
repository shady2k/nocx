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
