package spawn

import (
	"fmt"
	"strings"
)

// ── worktrees (brief nocx-xn63t.1.1) ────────────────────────────────────
//
// The worktree operations and the reads that guard them. The argv tails live
// here with every other one (spec D16); the porcelain listing is parsed here
// because it is git's output format and the client — which never runs git —
// must not learn to read it.

// WorktreeListArgs is the whole-worktree listing: git worktree list
// --porcelain. One invocation answers path, HEAD, branch and detached for
// every worktree of the repository, main checkout included, main first.
//
// --porcelain is not a preference: the plain form pads paths to a column and
// QUOTES a path containing an escape (measured on git 2.55: a worktree at
// "/tmp/x/wt\nnl" prints as "/tmp/x/wt\nnl" with the backslash escaped),
// which would make a parser learn git's C-style quoting to recover a path we
// ought to have verbatim. The porcelain form prints the raw bytes.
//
// --no-optional-locks is the reader discipline StatusArgs documents in full:
// every read in this package takes no optional locks, so a reader can never
// be the process that rewrites what an agent beside it is working on.
func WorktreeListArgs() []string {
	return []string{"--no-optional-locks", "worktree", "list", "--porcelain"}
}

// WorktreeCheckoutArgs places an existing branch in a new worktree: git
// worktree add <path> <branch>. The branch must not be checked out anywhere —
// git refuses that itself, and the caller checks first so it can name the
// other path.
//
// The path rides in argv because git offers no other channel for it, and is
// placed after -- so a path that begins with '-' cannot be read as an option.
// This is the one place spec D8's argv rule does not apply: it exists because
// a pathspec LIST has no length bound, while a worktree path is one value
// already bounded by the filesystem (PATH_MAX).
func WorktreeCheckoutArgs(path, branch string) []string {
	return []string{"worktree", "add", "--", path, branch}
}

// WorktreeCreateArgs creates the branch at base and places it in a new
// worktree in one invocation: git worktree add -b <branch> <path> <base>.
//
// -b is what makes this one command rather than a branch followed by a
// checkout, and it matters for the interval the caller depends on: measured
// on git 2.55, git validates the base BEFORE it creates the branch (a bad
// base leaves nothing behind) but creates the branch BEFORE it prepares the
// worktree (an occupied path leaves the branch behind). The caller therefore
// resolves the base itself and refuses an occupied path itself, rather than
// letting git find either after it has already written a ref.
func WorktreeCreateArgs(path, branch, base string) []string {
	return []string{"worktree", "add", "-b", branch, "--", path, base}
}

// WorktreeRemoveArgs removes one linked worktree: git worktree remove <path>.
// There is no --force form here and no argument that adds one (brief
// nocx-xn63t.1.1): removing a worktree that holds work is refused by the
// caller and again by git, and a person who wants that work gone has a shell.
//
// The path is placed after -- for the reason WorktreeCheckoutArgs gives.
func WorktreeRemoveArgs(path string) []string {
	return []string{"worktree", "remove", "--", path}
}

// VerifyCommitArgs asks whether a revision names a commit, and prints its
// oid: git rev-parse --verify --quiet <rev>^{commit}. The ^{commit} peel is
// the point — an annotated tag's own oid is a tag object, and a base that
// names a tree or a blob is not a base a worktree can be created at.
//
// --quiet makes the two answers distinguishable BY EXIT STATUS rather than by
// prose (D11): 0 with an oid, 1 with no output, anything else an invocation
// that could not be made — the same machine-checked shape
// SymbolicRefArgs' non-zero exit has.
func VerifyCommitArgs(rev string) []string {
	return []string{"rev-parse", "--verify", "--quiet", rev + "^{commit}"}
}

// RefTipArgs asks for one ref's oid: git rev-parse --verify --quiet <ref>.
// Exit status carries the answer exactly as VerifyCommitArgs documents it, so
// "the branch does not exist" is a value the caller reads, never a message it
// parses. The oid it prints is also what makes a later delete atomic: it is
// the old value the ref must still hold.
func RefTipArgs(ref string) []string {
	return []string{"rev-parse", "--verify", "--quiet", ref}
}

// RevListCountArgs counts the commits reachable from `to` and not from
// `from`: git rev-list --count <from>..<to>. Both ends are passed as
// revisions git resolves, never as a name this package splits — "ahead of
// base" is git's own answer to a revision range, the same way the branch's
// upstream is git's own answer to %(upstream:remotename).
//
// --no-optional-locks is the reader discipline; rev-list reads objects and
// never the index, so here it is a fence rather than a fix (the reasons are
// in StatusArgs, in one place).
func RevListCountArgs(from, to string) []string {
	return []string{"--no-optional-locks", "rev-list", "--count", from + ".." + to}
}

// DeleteRefArgs removes one ref only while it still points at the commit the
// caller counted: git update-ref -d <ref> <old-oid>. The old value is what
// makes this narrow instead of a branch delete: measured on git 2.55, a
// mismatch fails the only way that matters — the ref is kept ("is at <sha>
// but expected <sha>") — while the ZERO oid is NOT a guard, because deleting
// with a zero old value succeeds and removes the ref. So the caller must pass
// the real oid it read, and the delete can then only happen to the commit
// whose commits-beyond-base count was zero.
func DeleteRefArgs(ref, oldOID string) []string {
	return []string{"update-ref", "-d", ref, oldOID}
}

// WorktreeRecord is one worktree of `git worktree list --porcelain`.
type WorktreeRecord struct {
	// Path is the worktree's path, verbatim (see ParseWorktreeList).
	Path string
	// Branch is the branch NAME the worktree has checked out — git's
	// "refs/heads/" prefix stripped, because the operations that compare it
	// compare it against a branch name a caller supplied. Empty when the
	// worktree is detached. A symbolic HEAD under some other namespace (a
	// state git does not produce for a worktree) is kept verbatim rather
	// than guessed at.
	Branch string
	// Detached is true when the worktree's HEAD is not on a branch.
	Detached bool
}

// worktreeAttributes are the porcelain lines that belong to a record rather
// than continuing its path. The parser recognizes them all — including the
// ones no operation here consults (HEAD, bare, locked, prunable) — because a
// line it failed to recognize would be appended to the path.
var worktreeAttributes = map[string]bool{
	"HEAD":     true,
	"branch":   true,
	"detached": true,
	"bare":     true,
	"locked":   true,
	"prunable": true,
}

// ParseWorktreeList parses the whole output of WorktreeListArgs: records
// separated by a blank line, each starting with "worktree <path>" and
// followed by its attribute lines.
//
// The path is read as EVERYTHING up to the next attribute line, not as the
// rest of its own line, and that is measured rather than defensive: git 2.55
// prints the path verbatim, unquoted, so a worktree whose path contains a
// newline renders as a "worktree <first line>" record with the rest of the
// path on the following line(s) (measured: "/tmp/wt/nl" with a newline
// between "wt" and "nl" comes back as exactly those two lines). A line that
// is not a known attribute can therefore only be a path continuation, and
// this is the one rule that recovers such a path instead of truncating it.
//
// The format has no -z form, so the ambiguity is git's and not ours: a path
// containing a blank line, or a line that begins like an attribute, cannot be
// recovered by any consumer. It is stated here rather than papered over.
func ParseWorktreeList(data []byte) ([]WorktreeRecord, error) {
	var records []WorktreeRecord
	var current *WorktreeRecord

	flush := func() {
		if current != nil {
			records = append(records, *current)
			current = nil
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "":
			// The blank line between records, and the one the listing ends
			// with: closing the record is what they mean either way.
			flush()
		case current == nil:
			path, ok := strings.CutPrefix(line, "worktree ")
			if !ok {
				return nil, fmt.Errorf("git: malformed worktree listing: expected %q, got %q", "worktree <path>", line)
			}
			current = &WorktreeRecord{Path: path}
		case isWorktreeAttribute(line):
			applyWorktreeAttribute(current, line)
		default:
			// A continuation of the path, which contained a newline.
			current.Path += "\n" + line
		}
	}
	flush()
	return records, nil
}

// isWorktreeAttribute reports whether a line opens a record attribute — its
// first field is a name the format defines — rather than continuing a path.
func isWorktreeAttribute(line string) bool {
	name, _, _ := strings.Cut(line, " ")
	return worktreeAttributes[name]
}

// applyWorktreeAttribute reads the one attribute this domain answers for. The
// others are consumed and dropped: they are facts about git's own bookkeeping
// (a lock, a prunable directory) that no operation on this seam takes a
// decision on, and the decisions that depend on them — removing a locked
// worktree, say — are git's to refuse when the operation reaches it.
func applyWorktreeAttribute(rec *WorktreeRecord, line string) {
	switch {
	case strings.HasPrefix(line, "branch "):
		ref := strings.TrimPrefix(line, "branch ")
		rec.Branch = strings.TrimPrefix(ref, "refs/heads/")
		rec.Detached = false
	case line == "detached":
		rec.Detached = true
	}
}
