package workers

// What a close answers about the checkout it leaves (nocx-xn63t.1.3).
//
// THE OWNER'S DECISION, 2026-09-18: closing a worker never touches its
// checkout. A red merged-tree gate sends work back to the worker, and an
// agent's conversation is keyed to its directory — so workers.close leaves
// the checkout and its branch exactly as it found them, and answers what is
// in them instead: where the checkout is, which branch it holds, whether
// work sits in it that no commit keeps, and how many commits the branch has
// grown beyond its base.
//
// The READING is the closer's to make — the close is the moment the facts
// are wanted and the moment they are still true — and this package owns
// only the shape they arrive in and the two words it can say about them.
// The zero CloseResult is what every spawn without a worktree ask gets back,
// which is what keeps the field off the wire.

// CheckoutState is whether the close could read the checkout at all. Two
// answers and not one boolean, for the reason the git seam's own
// WorktreeState names: "could not read" and "clean" are the two answers a
// caller must not confuse, because one of them reads as safety over a
// worker's work.
type CheckoutState string

const (
	// CheckoutRead — Uncommitted and Ahead were read; they mean what they
	// say.
	CheckoutRead CheckoutState = "read"
	// CheckoutUnknown — the repository could not be asked: the checkout is
	// gone, or git failed. Uncommitted and Ahead carry NO meaning here, and
	// their zero values are the absence of an answer, never an answer of
	// zero.
	CheckoutUnknown CheckoutState = "unknown"
)

// Leftover is one checkout a close left on disk, and what the repository
// said it holds when the close asked.
type Leftover struct {
	// Path is where the checkout is. Still there: a close never removes it.
	Path string
	// Branch is the branch checked out in it, as git reports at the close —
	// it may have moved since the spawn. Empty when nothing could be read.
	Branch string
	// State says whether Uncommitted and Ahead mean what they say.
	State CheckoutState
	// Uncommitted is whether work sits in the checkout that no commit keeps
	// — a tracked change or an untracked file; ignored files do not count.
	// Meaningful only when State is CheckoutRead.
	Uncommitted bool
	// Ahead is how many commits the branch holds beyond the commit the
	// worktree started from — the worker's committed work, in count.
	// Meaningful only when State is CheckoutRead.
	Ahead int
}

// CloseResult is what ending a participant gives back to its caller. The
// close itself never fails on this answer's behalf: by the time the
// checkout is read, the session and the tab are already gone, so a failed
// reading is said (CheckoutUnknown) rather than returned as an error.
type CloseResult struct {
	// Worktree is the checkout that is still on disk, or zero when the
	// participant shared its coordinator's checkout.
	Worktree Leftover
}
