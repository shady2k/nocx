// Package git is the session-aware git domain: the Repo and RepoFactory
// contracts, the domain types every operation returns, and the git-semantic
// errors the transport switches on. The binding registry that guards a bound
// Repo lives in internal/git/registry — it is the one part of the git plane
// that imports internal/session, and the helper binary links this package
// standalone (plan Task 3, D18).
//
// # Why the git binary, not a library (spec D5)
//
// The panel must say the same thing as git typed in the tab next to it. A
// library is by construction a second implementation of "what does git
// think", and the divergences are silent: it agrees about a modified file and
// disagrees about a sparse checkout, a submodule, or a core.excludesFile the
// user set five years ago. The decisive case is hooks: this repository's own
// pre-commit hook is the quality gate for every commit in it. go-git does not
// run hooks. A Commit button that silently produced commits nobody's gate had
// seen would be green everywhere you look and wrong where you did not.
//
// # The local/remote seam is the operation, not the process (spec D16)
//
// Repo and RepoFactory are the whole seam: local spawns git here, the relay
// (a later build target, nocx-if6) sends named operations to a helper there,
// and nothing above these interfaces knows which. The alternative — a
// process-shaped Runner.Run(argv, stdin, out) interface — would make the
// relay emulate a local process: process groups, pipes, an exit status, an
// INT→TERM→KILL escalation, all protocol obligations over a WebSocket that
// has none of those things. The seam is therefore a closed set of named
// operations, the same shape the file manager's Provider has.
//
// Everything about spawning a child — argv, process groups, signals, pipes,
// the resolved environment — is private to internal/git/local. The test to
// apply to every value these interfaces return: does it describe what the
// user is being shown, or how a local pipe was cut? The second kind stays in
// local. The domain states (Completeness, the diff states, the commit
// outcome) are declared here, machine-independently, so the relay can report
// them too; local maps its private execution record (a "cut" child) to those
// states before anything crosses Repo.
//
// # The structural guarantee (spec §5.1)
//
// The registry in internal/git/registry is where a bound Repo exists, and
// Registry.Acquire is the only route to one. Binding holds its repo in an
// unexported field, so "every handler must remember to check" is not a
// discipline anybody has to keep: a handler cannot forget a check it never
// performs. Acquire performs the one authorisation check — the caller must
// Own the binding's session (D15) — and takes the use-guard that keeps the
// binding alive for the call's duration.
package git

import (
	"context"
	"time"
)

// Repo is one repository on one machine. It is the whole local/remote seam:
// local spawns git here, relay sends operations to the helper there, and
// nothing above this interface knows which. ctx is the entire cancellation
// contract — each implementation honours it with the mechanism its channel
// has, and neither imposes the other's.
//
// There is deliberately no Capability method: git's presence, its version and
// the environment it runs in are all determined before a repository exists,
// so they live on the factory's OpenOutcome and nowhere else. One fact, one
// owner (spec §5.1).
type Repo interface {
	Status(ctx context.Context) (Status, error)
	// EnvState reports the environment git will run in right now — the
	// non-waiting read. Open reports the same fact once (D6); the status
	// poll repeats it because Open's answer is provisional (nocx-6pz0): it
	// reports whatever has settled by open, which in the pre-settle window
	// is degraded, and a fact the panel can never correct would warn about
	// a degradation that no longer exists (nocx-69ey). It never resolves:
	// only the commit path waits on resolution, and the panel must never be
	// shown a "resolved" the resolution has not earned.
	EnvState() (EnvState, string)
	Diff(ctx context.Context, path string, side Side, maxBytes int64) (Diff, error)
	Log(ctx context.Context, max int) (Log, error)
	Stage(ctx context.Context, paths []string) (Status, error)
	Unstage(ctx context.Context, paths []string) (Status, error)
	StageAll(ctx context.Context) (Status, error)
	UnstageAll(ctx context.Context) (Status, error)
	Commit(ctx context.Context, msg string, amend bool) (CommitOutcome, error)
	HeadMessage(ctx context.Context) (HeadMessage, error)
	// RemoteURL is the URL of the remote the current branch tracks — the
	// "open on its hosting" fact (brief, nocx-hc0m). It is derived with
	// git, never guessed: symbolic-ref names the branch, the upstream
	// atom names the remote it tracks, remote get-url reads that remote's
	// URL. The branch is the question, so it is answered from HEAD, not
	// from a client-supplied name. A detached HEAD, a branch with no
	// upstream and a remote that no longer exists all answer ErrNoRemote —
	// the ordinary "nothing to open" case, never an error.
	RemoteURL(ctx context.Context) (string, error)
	Close() error
}

// RepoFactory opens one. Resolution — is git here, is it new enough, and is
// this directory inside a repository — happens BEFORE a Repo can exist, so it
// cannot be a Repo method; and it must not be done with os/exec in the
// composition layer, which would put local mechanics back above the seam with
// no remote counterpart. So it is its own seam, with the same two
// implementations.
//
// local: probe capability, run rev-parse, build a Repo on the answer.
// relay: send one open operation to the helper and build a client Repo on its
// answer. One round trip, not three.
type RepoFactory interface {
	Open(ctx context.Context, cwd string) (Repo, OpenOutcome, error)
}

// OpenState is the outcome table of git.open (spec §5.1, remote-helper
// design §6). noCwd is produced by the composition layer from the caller's
// origin before the factory is invoked; the §6 refusal states
// (consentRequired, unsupportedPlatform, deployFailed, execForbidden,
// helperVersionMismatch) are produced by the helper selection and the
// helper dial — the composition layer again, never the factory; the
// factory itself answers ok, notARepository, gitUnavailable or gitTooOld.
type OpenState string

const (
	// OpenOK — a repository was resolved; a Repo accompanies this outcome.
	OpenOK OpenState = "ok"
	// OpenNotARepository — rev-parse said no, or answered malformed output.
	OpenNotARepository OpenState = "notARepository"
	// OpenNoCwd — the caller had no verified cwd to offer.
	OpenNoCwd OpenState = "noCwd"
	// OpenGitUnavailable — no git on the environment's PATH.
	OpenGitUnavailable OpenState = "gitUnavailable"
	// OpenGitTooOld — below the version floor; the result carries what it found.
	OpenGitTooOld OpenState = "gitTooOld"
	// OpenConsentRequired — the session is an SSH session whose machine has
	// no relay-tier answer (remote-helper design D8): the user has not yet
	// accepted the helper for this host. The panel offers the consent flow;
	// accepting raises the machine to the relay tier and the next git.open
	// proceeds. Produced by the composition layer from the consent
	// decision, before the factory is invoked — the producer of a state
	// owns declaring it, so the state lives here with its siblings.
	OpenConsentRequired OpenState = "consentRequired"
	// OpenUnsupportedPlatform — the session's host runs an OS/arch we build
	// no helper for (D20), or the helper artifact was not built (`make
	// helpers` has not run). Message names which, and what to do about it.
	OpenUnsupportedPlatform OpenState = "unsupportedPlatform"
	// OpenDeployFailed — uploading or installing the helper on the host
	// failed (D7). Message carries what failed.
	OpenDeployFailed OpenState = "deployFailed"
	// OpenExecForbidden — the server refused the exec that would run the
	// helper, or answered with something that is not our helper (D5).
	// Message carries what was seen.
	OpenExecForbidden OpenState = "execForbidden"
	// OpenHelperVersionMismatch — an incompatible helper answered (D6):
	// a protocol version or content hash that is not the one installed.
	// Non-retryable until the helper is reinstalled.
	OpenHelperVersionMismatch OpenState = "helperVersionMismatch"
)

// OpenOutcome carries the resolved repository's identity and the two facts
// that precede it: the git version (capability) and the environment state
// (D6). Toplevel and GitDir are both load-bearing — they are the binding's
// identity, because two linked worktrees of one repository are different
// working trees (spec §5.1) — and both are set only when State is OpenOK.
// Message is the refusal's account: what failed and what to do about it,
// set when State is one of the §6 refusal states (unsupportedPlatform,
// deployFailed, execForbidden, helperVersionMismatch). A state that
// renders a generic error is not done (brief, nocx-1xxa) — the panel says
// what the state means and names the recovery.
type OpenOutcome struct {
	State      OpenState
	Toplevel   string   // the worktree root; "" unless ok
	GitDir     string   // the absolute git directory; "" unless ok
	GitVersion string   // "2.55.0"; set when the probe ran
	EnvState   EnvState // D6: resolved, or degraded
	EnvReason  string   // why the environment is degraded; "" when resolved
	Message    string   // the refusal's account; "" unless a §6 refusal state
}

// EnvState reports whether git will run in the resolved environment or in the
// degraded fallback (spec D6). The panel renders degraded before the first
// commit: a hook that silently could not find its tools is the exact failure
// the decision exists to prevent. degraded is also reported for the brief
// window before the background resolution settles (nocx-6pz0) — the panel
// must never be shown a "resolved" the resolution has not earned.
type EnvState string

const (
	// EnvResolved — git runs with the environment resolved from the user's shell.
	EnvResolved EnvState = "resolved"
	// EnvDegraded — resolution failed; git runs with os.Environ() and the
	// outcome says so.
	EnvDegraded EnvState = "degraded"
)

// Status is the answer to "what changed in this repository", from one
// git status --porcelain=v2 -z --branch --untracked-files=all invocation
// (D7). Staged, Unstaged and Conflicted are never nil — an empty set must
// marshal as [], not null; that exact bug was found by the first contract
// schema this repository ever ran.
type Status struct {
	Branch   string // "" when detached
	Detached bool
	Unborn   bool
	Head     string // short hash; "" when unborn
	Upstream string // "" when the branch has none
	Ahead    int
	Behind   int

	Staged     []Entry
	Unstaged   []Entry
	Conflicted []Entry

	// Total is the number of status records observed. Its meaning is fixed
	// by Completeness: exact when complete or capped, a lower bound when cut.
	Total        int
	Completeness Completeness
}

// Completeness is ONE discriminator describing how much of the repository's
// status the lists hold, and the panel switches on it first. A traversal
// stopped by the work ceiling after 100 records — below the retention cap —
// must not look complete, and two booleans got exactly that wrong (spec D9,
// revision 5).
type Completeness string

const (
	// CompletenessComplete — every record was observed and every one is in
	// the lists. Total is exact.
	CompletenessComplete Completeness = "complete"
	// CompletenessCapped — every record was observed; more existed than
	// MaxStatusEntries, and only the first are in the lists. Total is exact.
	CompletenessCapped Completeness = "capped"
	// CompletenessCut — a bounded read was stopped at the work ceiling
	// before its end. When the STATUS traversal was stopped, the lists hold
	// a prefix and Total is a lower bound. When only the line-count read
	// was stopped or failed, the lists are complete and Total is exact, but
	// no entry carries counts — counts are all-or-nothing, because a
	// partial count set makes the rows past the cut look like rows with
	// nothing to count (brief nocx-i4ki).
	CompletenessCut Completeness = "cut"
)

// Entry is one row of a list. X and Y are the porcelain v2 status columns:
// the index-side status and the worktree-side status ('.' when that side is
// clean, '?' for an untracked file, 'U' for a conflicted one). A file can be
// in both lists — XY with both columns non-'.' — which is why the panel's row
// key is {side, path}, not path.
type Entry struct {
	Path string
	X    byte
	Y    byte

	// Added and Deleted are the numstat line counts for this entry on its
	// side, nil when no count exists — an untracked file, a binary file, a
	// conflicted entry, or a count read that was bounded out (design D9,
	// brief nocx-i4ki). They are always set or unset as a pair.
	Added   *int
	Deleted *int
}

// Side names which side of a file a diff asks about. It is a closed set.
type Side string

const (
	SideStaged    Side = "staged"
	SideUnstaged  Side = "unstaged"
	SideUntracked Side = "untracked"
)

// DiffState is the state table of one file's unified diff (spec §5.1
// "diff.go"). empty and gone exist because the panel is polling: a row can be
// clicked in the same second an agent reverts the file.
type DiffState string

const (
	// DiffOK — unified diff text, not truncated.
	DiffOK DiffState = "ok"
	// DiffBinary — git said "Binary files differ"; there is nothing to render.
	DiffBinary DiffState = "binary"
	// DiffTooLarge — the byte bound was reached; the retained text is a
	// prefix, and says so. This is the ONLY diff state local's cut maps to;
	// the cut itself stays private to local.
	DiffTooLarge DiffState = "tooLarge"
	// DiffEmpty — no differences: the file changed back, or the poll raced.
	DiffEmpty DiffState = "empty"
	// DiffGone — the path no longer exists on that side.
	DiffGone DiffState = "gone"
)

// Diff is the bounded result of diffing one file. Truncated is the observable
// half of the byte bound — the text is a prefix — and is set together with
// DiffTooLarge. It describes what the user is shown, never how the child was
// stopped.
type Diff struct {
	State     DiffState
	Text      string
	Truncated bool
}

// CommitState is the commit outcome (spec D11): zero → ok with the new head;
// non-zero → failed with git's own account. We do not classify why — git
// exposes no machine-readable discriminator between a hook, a signing failure
// and an index lock, and parsing prose would be a second git-error classifier.
type CommitState string

const (
	CommitOK     CommitState = "ok"
	CommitFailed CommitState = "failed"
	// CommitIndeterminate — the transport died between a mutation's
	// request and its response (remote-helper D12): the commit may have
	// happened, hooks and all, and the caller must say so — never a
	// failure (which would invite a retry that commits twice), never a
	// retry. Produced by the helper-backed repo on transport loss; the
	// local implementation never produces it, because a local child
	// either ran or was killed and the process can tell which.
	CommitIndeterminate CommitState = "indeterminate"
)

// CommitOutcome is the result of one commit. Output is git's stdout and
// stderr as far as the bound allowed, set when failed; OutputTruncated says
// the bound was reached, because a silently clipped account is a worse lie
// than one that admits it. Status is the fresh post-commit status (D12);
// StatusStale means the commit happened but the status read failed, and the
// panel must say so rather than render a stale list as fresh.
type CommitOutcome struct {
	State           CommitState
	Head            string // short hash of the new head; "" when failed or unknown
	Output          string
	OutputTruncated bool
	Status          Status
	StatusStale     bool
}

// HeadMessageState is the Amend prefill (spec §5.2, git.headMessage).
type HeadMessageState string

const (
	// HeadMessageOK — HEAD has a message; Message carries it.
	HeadMessageOK HeadMessageState = "ok"
	// HeadMessageNone — no HEAD message to amend: the branch is unborn.
	HeadMessageNone HeadMessageState = "none"
)

// HeadMessage is the prefill for the Amend box: the full HEAD message
// (subject and body), fetched once when the box is ticked.
type HeadMessage struct {
	State   HeadMessageState
	Message string
}

// LogEntry is one commit of the branch's recent history (brief, git.log).
// Refs are the decorations git attaches to the commit — branch names, tags,
// and HEAD when the commit is HEAD — in git's own order, with git's
// decoration prefixes ("HEAD -> ", "tag: ") stripped: the wire carries what
// the panel shows, and the decoration grammar is git vocabulary the relay
// would otherwise have to reproduce. Refs is never nil — an empty set
// marshals as [], not null.
type LogEntry struct {
	Hash       string    // the full object id
	ShortHash  string    // git's own abbreviation (%h)
	Subject    string    // the first line of the message (%s)
	AuthorName string    // the author's name (%an)
	AuthoredAt time.Time // the author date (%aI)
	Refs       []string  // decorations; empty when the commit carries none
}

// Log is the bounded answer to "what has happened on this branch": the
// first max commits of HEAD, newest first. A log is unbounded by nature, so
// it is bounded by contract (D9): the caller names max, the implementation
// asks git for max+1, and Completeness says which of the two answers it is.
// Total is exact when complete — the branch has at most max commits, all of
// them in Entries. When capped, Total is max+1 — the extra record is how
// "more than max exist" is known (D9) — and Entries holds the first max.
// When cut, Total is the records observed before the work ceiling stopped
// the stream, a lower bound.
type Log struct {
	Entries      []LogEntry // never nil
	Total        int        // commits observed; exact unless cut
	Completeness Completeness
}
