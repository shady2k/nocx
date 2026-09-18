package git

import "time"

// The work ceilings and retention cap, shared as policy between the
// implementations (spec §5.1 "Bounding"). The values are the unmeasured
// constants of spec §9, recorded there as risks rather than defects; each
// would be settled by the measurement its comment names.

// MaxStatusEntries is the retention cap for the status lists: the parser
// retains the first MaxStatusEntries records and keeps counting the rest.
// 5,000 is large enough that a real change set is never capped and small
// enough that a stray un-ignored node_modules is caught (spec §9.1).
const MaxStatusEntries = 5000

// MaxStatusBytes is the byte half of the status work ceiling. Counting past
// the retention point is a NUL scan that costs nothing, but
// --untracked-files=all makes git traverse the filesystem and format a record
// for every file, so a generated tree with millions of untracked files would
// hold a subprocess open for as long as the traversal takes. At the byte
// ceiling the stream is cut and the result is Completeness: cut with a lower
// bound. 16 MiB is roughly 150k records at the 100-byte typical record — far
// above the retention cap, so a merely-capped status still completes exactly.
const MaxStatusBytes = 16 << 20

// MaxStatusWallClock is the wall-clock half of the status work ceiling. The
// byte ceiling bounds what we read; this bounds a traversal that produces no
// output — a stuck filesystem, a network share — so the child cannot be held
// open silently. Together they are what make the cut state reachable below
// the record cap (spec §9.1, D9).
const MaxStatusWallClock = 30 * time.Second

// MaxStderrBytes is the per-invocation stderr bound. Past it, output is
// discarded — never an error, because a stderr writer that errors stops the
// reader while the child is still writing and deadlocks the invocation — and
// the result reports that the bound was reached.
const MaxStderrBytes = 64 << 10

// MaxCommitOutputBytes bounds each of stdout and stderr captured for a failed
// commit's account. The commit surface shows git's own account of a failure
// (D11), and a silently clipped account is a worse lie than one that admits
// it, so the bound is reported rather than hidden.
const MaxCommitOutputBytes = 64 << 10

// MaxLogEntries is the retention cap for the log list: the parser retains
// the first MaxLogEntries commits and keeps counting the rest, and the
// invocation asks for one more than it will return — the extra record is
// how the caller knows more exist (D9). The Commits section is a
// confirmation surface — "the commit I just made is at the top" — so a
// recent window is the product: 50 is large enough that a real branch's
// recent history is never capped and small enough that the section stays a
// list (the unmeasured constants of spec §9, recorded there as risks).
const MaxLogEntries = 50

// MaxLogBytes is the byte half of the log work ceiling. Subjects are short
// and 50 records are small, so 1 MiB is far above any real answer; at the
// ceiling the stream is cut and the result is Completeness: cut with a
// lower bound, never a silently truncated list.
const MaxLogBytes = 1 << 20

// MaxLogWallClock is the wall-clock half of the log work ceiling. The byte
// ceiling bounds what is read; this bounds a read that produces no output
// — a stuck filesystem, a network share — so the child cannot be held open
// silently (spec §9.1).
const MaxLogWallClock = 30 * time.Second

// MaxWorktreeListBytes is the byte bound on the worktree listing — one
// `git worktree list --porcelain` read that answers every worktree's path,
// HEAD and branch (brief nocx-xn63t.1.1). A record is around 150 bytes and a
// repository's worktrees are few, so 1 MiB is far above any real answer while
// still bounding a listing something has gone wrong with.
//
// A listing that reaches the bound is REFUSED rather than reported: unlike a
// status list, whose prefix is a useful answer about a repository too large to
// traverse, a prefix of the worktree list is a list that silently omits the
// very worktree the caller is asking about — the one it means to remove.
const MaxWorktreeListBytes = 1 << 20

// MaxWorktreeAnswerBytes bounds a worktree operation's short scalar reads —
// one oid from rev-parse, one integer from rev-list --count. A git that
// answered more than this is not answering the question, and the read is
// refused rather than parsed.
const MaxWorktreeAnswerBytes = 4 << 10

// MaxWorktreeWallClock is the wall-clock bound on each invocation a worktree
// operation makes. The byte bounds bound what is read; this bounds a read
// that produces no output — a path on a stuck filesystem, a worktree
// directory on a network share — so no child is held open silently. It is the
// same 30 s the status and log traversals take (spec §9.1).
const MaxWorktreeWallClock = 30 * time.Second
