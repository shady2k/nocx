// Package spawn is everything about asking git a question that is shared by
// every implementation that actually runs git: argv construction and the
// porcelain v2 status parser.
//
// It is linked ONLY by code that runs git — local here, and the copy of
// local compiled into the helper build (AD-2). The helper CLIENT never
// imports it: a client that built argv or parsed porcelain would either put
// process vocabulary on the wire or compute an answer nobody uses (spec
// D16). The program name is not part of these argv tails for the same
// reason — the path to the binary is a local fact; the arguments are not.
package spawn

import (
	"fmt"
	"strconv"

	"github.com/shady2k/nocx/internal/git"
)

// StatusArgs is the one invocation that answers the header and both lists
// (D7): git status --porcelain=v2 -z --branch --untracked-files=all. The
// branch, upstream and ahead/behind ride in the # branch.* records, and the
// ABSENCE of # branch.upstream / # branch.ab is what "no upstream" looks
// like — never a zero.
//
// --no-optional-locks is what makes this safe to POLL. Measured on git 2.55:
// a plain `git status` opportunistically refreshes the index and rewrites
// .git/index — the file's mtime moves on every run — while the same command
// with this flag leaves it untouched. The panel asks this question every few
// seconds, in a repository where an agent is running git in the terminal
// beside it, so a reader that mutates the index twelve times a minute is
// interference, not observation. This is the flag git added for exactly this
// caller.
func StatusArgs() []string {
	return []string{
		"--no-optional-locks",
		"status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all",
	}
}

// RevParseArgs asks git for the two values that are the binding's identity:
// the worktree root and the absolute git directory. git prints exactly two
// lines (verified on git 2.55); the caller validates that rather than
// trusting it.
func RevParseArgs() []string {
	return []string{"rev-parse", "--show-toplevel", "--absolute-git-dir"}
}

// AddArgs stages the pathspecs on stdin, NUL-separated (D8): paths never
// ride in argv for a mutation, because argv has an OS length cap and a path
// beginning with '-' would be read as an option.
func AddArgs() []string {
	return []string{"add", "--pathspec-from-file=-", "--pathspec-file-nul"}
}

// AddAllArgs is git add -A (D19). It is refused by the caller while any
// entry is conflicted: measured, git add -A marks the conflict resolved
// using a worktree file that still contains conflict markers.
func AddAllArgs() []string {
	return []string{"add", "-A"}
}

// ResetArgs unstages the pathspecs on stdin. Bare git reset is what makes
// unstage-all work on an unborn branch, where git restore --staged fails on
// an unresolvable HEAD — measured, no special unborn path is needed.
func ResetArgs() []string {
	return []string{"reset", "--pathspec-from-file=-", "--pathspec-file-nul"}
}

// ResetAllArgs is bare git reset — no HEAD, no pathspec (D19). It too is
// refused by the caller while any entry is conflicted: bare git reset during
// a conflicted merge deletes .git/MERGE_HEAD, silently aborting the merge.
func ResetAllArgs() []string {
	return []string{"reset"}
}

// CommitArgs is git commit with the message on stdin (-F -), never argv — a
// message with newlines and quotes is the normal case (D8). There is no
// --no-verify in this design and no setting that adds one: hooks always run.
func CommitArgs(amend bool) []string {
	if amend {
		return []string{"commit", "-F", "-", "--amend"}
	}
	return []string{"commit", "-F", "-"}
}

// HeadArgs reads the short hash of HEAD after a commit (the post-commit head
// read). "short" is git's own abbreviation.
func HeadArgs() []string {
	return []string{"rev-parse", "--short", "HEAD"}
}

// HeadMessageArgs reads the full HEAD message (subject and body) for the
// Amend prefill.
func HeadMessageArgs() []string {
	return []string{"log", "-1", "--format=%B"}
}

// SymbolicRefArgs names the current branch (git symbolic-ref --short HEAD).
// A non-zero exit is data: on a detached HEAD git refuses to name a branch,
// which is exactly the "nothing to open" answer RemoteURL needs.
func SymbolicRefArgs() []string {
	return []string{"symbolic-ref", "--short", "HEAD"}
}

// UpstreamRemoteArgs names the remote the branch tracks, via the
// %(upstream:remotename) atom: git's own answer to "which remote is this
// branch's upstream on" — never a client parse of the upstream ref, which
// would mis-split a remote whose name contains a slash. The atom prints
// empty for a branch with no upstream, and "." for a LOCAL upstream (a
// branch set to track another local branch) — both are "no remote to open".
func UpstreamRemoteArgs(branch string) []string {
	return []string{"for-each-ref", "--format=%(upstream:remotename)", "refs/heads/" + branch}
}

// RemoteUrlArgs reads one remote's fetch URL (git remote get-url). A
// non-zero exit is data: the tracked remote was deleted, and "no remote to
// open" is the honest answer.
func RemoteUrlArgs(remote string) []string {
	return []string{"remote", "get-url", remote}
}

// LogArgs asks for the first max+1 commits of HEAD, newest first (brief,
// git.log; D9's "ask for one more than you will return" — the extra record
// is how the caller knows it was capped). -z and %x00 keep every field
// NUL-delimited because a subject may contain a newline or a tab, and a
// line-based parser is a defect waiting for a commit message to find it
// (measured: the tab survives into the stream verbatim). The format is six
// values per commit — %H, %h, %s, %an, %aI, %D — and the -z flag emits the
// record terminator, so the parser counts seven fields per record.
//
// --no-optional-locks, the same flag StatusArgs documents: this is a read
// the panel makes while an agent runs git in the terminal beside it, and a
// reader that rewrites .git/index as a side effect is interference, not
// observation.
func LogArgs(max int) []string {
	return []string{
		"--no-optional-locks",
		"log", "-z",
		"--format=%H%x00%h%x00%s%x00%an%x00%aI%x00%D",
		"-n", strconv.Itoa(max + 1),
	}
}

// DiffArgs builds the invocation for one side of one file (spec §5.1
// "diff.go"). The path rides in argv, protected by --, because diff is
// read-only; only mutations keep paths out of argv.
//
// --no-ext-diff on every form, because the panel renders the output AS a
// unified diff and a user's diff.external driver replaces it wholesale.
// Measured: with diff.external set to a script that echoes one line, plain
// `git diff` returns that line and nothing else; --no-ext-diff returns the
// real unified diff. Developers who use difftastic or delta as a diff driver
// have exactly this configured, and D6 runs git under the user's resolved
// environment, so this is the ordinary case rather than the exotic one.
func DiffArgs(side git.Side, path string) ([]string, error) {
	switch side {
	case git.SideStaged:
		return []string{"diff", "--no-ext-diff", "--cached", "--no-color", "--", path}, nil
	case git.SideUnstaged:
		return []string{"diff", "--no-ext-diff", "--no-color", "--", path}, nil
	case git.SideUntracked:
		// An untracked file has nothing to diff against; --no-index against
		// /dev/null is git's own answer, a real all-additions diff. It exits
		// 1 when there are differences, which is why the caller treats a
		// non-zero exit as data.
		return []string{
			"diff", "--no-ext-diff", "--no-index", "--no-color", "--", "/dev/null", path,
		}, nil
	default:
		return nil, fmt.Errorf("git: unknown diff side %q", side)
	}
}

// LiteralPathspec prefixes each path with :(literal) so that a path git
// reports verbatim — one that happens to contain glob metacharacters like
// `*` or `[` — is staged as itself and not as a pattern. The panel stages
// the row the user clicked, never a glob.
func LiteralPathspec(path string) string {
	return ":(literal)" + path
}
