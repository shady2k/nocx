package content

// Restart reconciliation (nocx-k6p18.5; the content-store restart
// reconciliation design, and level-1 D5 in the epic's voice).
//
// ── The premise this repeals ─────────────────────────────────────────────
//
// `Open` used to run two sweeps, and both were founded on a premise stated in
// the code beside them: "a session is server-authoritative (AD-7), lives
// inside one backend process and CANNOT OUTLIVE IT". Everything followed from
// it — at store-open every `sessions` row named a dead process, every
// recording named a pipe that could not still be producing, and every open
// entry belonged to a command nobody was running any more. Deleting all three
// was not a policy, it was a theorem.
//
// The helper owns the host now (nocx-k6p18.3): a session's PTY belongs to a
// process that survives the coordinator, so the axiom is repealed and the
// theorem with it. Left alone, a REPLACING coordinator would, at exactly the
// moment the promise exists to protect: delete the `sessions` row of a running
// session, delete its recording, null the `session_id` of every entry naming
// it, and close its still-open entry as `unknown` — declaring finished a
// command that is still running.
//
// ── A wrong argument, recorded so it is not made again ───────────────────
//
// That this needs only one `UPDATE`: stop forcing `phase='closed'` and keep
// both deletes, because "the pipe died". The pipe is the ATTACHMENT. The
// session and its output stream survived it. Deleting the recording because a
// READER went away throws out precisely what the promise exists to keep — a
// coordinator that recorded thirty minutes of a build, updated, and came back
// after the helper's window had rolled would have destroyed those thirty
// minutes, and "start a fresh recording" avoids the discontinuity error only
// by discarding them.
//
// ── Why the question cannot be asked where it used to be answered ────────
//
// `Open` runs before any carrier exists. Asking a host needs a connection, the
// connection may need the vault, and the vault needs this store. So `Open`
// opens and JUDGES NOTHING; carriers come up; an inventory is asked; each
// session is reconciled through the seam below, on the ordinary connection
// (ADR-0043 — reconciliation opens none of its own).
//
// The carried-over set is held in MEMORY rather than stamped on the rows. It
// is exact without a stamp: `Open` is the first thing this incarnation does,
// so every `sessions` row and every recording it finds belongs to a previous
// one, and anything created afterwards is this incarnation's and is not in the
// set. That is also what makes a stale verdict harmless — Apply refuses a
// session this incarnation did not carry over (ErrNotPending) — and what makes
// an interrupted pass resumable: the next `Open` recomputes the set from the
// tables as they then are, so a row is either judged or pending and never half
// of both.

import (
	"context"
	"errors"
	"time"
)

// ErrNotPending is returned by Apply for a session this incarnation did not
// carry over: one it opened itself, one already reconciled, or one that never
// existed. It is a REFUSAL and not a failure of the verdict — the alternative
// is a stale `absent` deleting the live work of a session whose id came round
// again.
var ErrNotPending = errors.New("content: session is not awaiting reconciliation")

// SessionVerdict is the closed vocabulary of the three answers, and the third
// is the whole problem. Closed for the reason every other vocabulary in this
// package is: a caller inventing a fourth value would be inventing policy the
// store then has to guess at.
type SessionVerdict string

const (
	// VerdictLive — the exact generation reports this host session. Keep
	// everything: the row, the recording, the provenance, and the OPEN ENTRY
	// STAYS OPEN, because the command is still running.
	VerdictLive SessionVerdict = "live"
	// VerdictAbsent — the exact reachable generation was ASKED and says it
	// does not hold this session. Exactly what `Open` used to do, for that
	// session alone, in one transaction.
	//
	// It requires an ANSWER. A refused connection, a timeout, a sealed vault
	// and an unreachable host are each VerdictUnknown: liveness is a fact
	// obtained, never an inference from a failure. Nothing in this package can
	// enforce that on the caller — what it can do is make the honest call as
	// easy as the dishonest one, which is why VerdictUnknown carries a cause
	// and costs nothing to apply.
	VerdictAbsent SessionVerdict = "absent"
	// VerdictUnknown — nobody could be asked. Change nothing; reconcile on a
	// later attempt. This is a state a person can sit in for hours — a laptop
	// opened away from the network, a host behind a jump box that is down, a
	// vault-gated credential waiting on somebody — which is why it has a cause
	// and a surface rather than a silence.
	VerdictUnknown SessionVerdict = "unknown"
)

// UnreconciledCause is why nobody could be asked, in a closed vocabulary the
// renderer picks its sentence from — the shape HistoryDegradeReason has, and
// for the same reason: a backend rewording must never change what a user
// reads. Some of these a person can act on (a sealed vault), which is exactly
// why `unknown` is shown rather than swallowed.
type UnreconciledCause string

const (
	// CauseNotYetAsked is where every carried-over session starts: this
	// incarnation has not reached a carrier for it yet. It is the honest
	// answer during startup and it is not a degrade.
	CauseNotYetAsked UnreconciledCause = "notYetAsked"
	// CauseNoInventory — there is nothing that could answer for this session:
	// no generation is recorded against it, so no inventory owns its id. It is
	// the ordinary answer for a session recorded before the helper owned the
	// host, and it is the reason those recordings need the age bound below.
	CauseNoInventory UnreconciledCause = "noInventory"
	// CauseAmbiguousInventory means more than one inventory claimed the same
	// id space. That can only be a duplicate registration bug; refusing to
	// choose makes the bug loud instead of letting first-wins hide it.
	CauseAmbiguousInventory UnreconciledCause = "ambiguousInventory"
	// CauseConnectionRefused — a carrier answered, refusing.
	CauseConnectionRefused UnreconciledCause = "connectionRefused"
	// CauseTimedOut — the ask did not finish in time.
	CauseTimedOut UnreconciledCause = "timedOut"
	// CauseHostUnreachable — the host could not be reached at all.
	CauseHostUnreachable UnreconciledCause = "hostUnreachable"
	// CauseVaultSealed — the credential this host needs is behind a sealed
	// vault, so the ask cannot even be attempted. The one cause on this list
	// that a person clears in one gesture.
	CauseVaultSealed UnreconciledCause = "vaultSealed"
)

// PendingSession is one session carried over from a previous incarnation and
// not yet judged, as the product must be able to describe it: which session,
// how long it has been waiting, why nobody could be asked, and how much of a
// person's work is hanging on the answer.
type PendingSession struct {
	// SessionID is the row's id — the same id `entries.session_id` names and
	// the recording is keyed by.
	SessionID string
	// SessionExists is the first reconciliation fact: the exact helper
	// generation reports this host session. It does not say that any open
	// attempt has been recovered, so it never clears Unreconciled.
	SessionExists bool
	// Host and Account identify the execution target whose helper may judge
	// this session. They are durable binding facts, not values inferred from
	// the helper generation or session id.
	Host    string
	Account string
	// Generation is the helper generation that owns SessionID's id space.
	// Empty means no inventory may judge it.
	Generation string
	// PaneID, ProfileID and HelperCommand are the route BACK to this
	// session — the pane it was the pipe of, the saved connection that
	// reaches its host, and the helper binary on that host the bridge
	// execs (nocx-k6p18.30). A verdict never reads them: they are what a
	// coordinator needs to RE-ADOPT the session rather than merely judge
	// it. Any of them empty means no route was recorded, and a route is
	// never inferred from the other fields — that inference is exactly what
	// the Host/Account/Generation ordering above exists to forbid.
	PaneID        string
	ProfileID     string
	HelperCommand string
	// Fingerprint is the execution machine's host public-key fingerprint,
	// the consent key. Re-adoption re-asks the consent decision with it
	// rather than assuming a decision made in a previous run still stands.
	Fingerprint string
	// Since is when this incarnation marked the session unreconciled at Open,
	// not when the remote command started. It is what the age bound reads and
	// what the product shows: "not reachable since" is a fact with a date.
	Since time.Time
	// Cause is why this is still unjudged. Never a verdict.
	Cause UnreconciledCause
	// Detail is the underlying error's own words, for a bug report. The
	// SENTENCE a user reads comes from Cause; this is what a support thread
	// needs and is never the sentence itself.
	Detail string
	// OpenEntries is how many of this session's blocks are still open — the
	// blocks that render as neither running nor finished until this is
	// answered.
	OpenEntries int
	// RecordedBytes is how much of its output is being kept while nobody can
	// say whether the session still exists. It is the cost of waiting, and
	// naming it is what makes the age bound below arguable rather than
	// arbitrary.
	RecordedBytes uint64
}

// SessionJudgement is one verdict about one session.
type SessionJudgement struct {
	SessionID string
	Verdict   SessionVerdict
	// Cause and Detail apply to VerdictUnknown and are ignored otherwise: a
	// verdict that was REACHED needs no excuse.
	Cause  UnreconciledCause
	Detail string
}

// SessionReconciler is the seam that replaces the startup sweep. It is a
// repository like the others (AD-8: one owner per behaviour) because the
// carried-over set and the rows it names are the same fact, and a second
// holder of it would drift from the tables the first time either changed.
type SessionReconciler interface {
	// Pending is every session carried over from a previous incarnation and
	// still unjudged, oldest first. An empty answer means there is nothing to
	// reconcile — which is the ordinary state of a store a moment after it
	// opens with nothing left behind.
	Pending(ctx context.Context) ([]PendingSession, error)
	// Apply applies ONE verdict to ONE session.
	//
	//   live    — clears the mark and writes nothing.
	//   absent  — deletes the row (nulling session_id through the existing
	//             foreign key), deletes the recording, and closes the
	//             session's open entries as `unknown` (`interrupted` for an
	//             ask), in ONE transaction. That is `closeOpenEntries`'s own
	//             reason for being one transaction, kept: the interval it
	//             guards has one closing event, and a reader must never see
	//             one half of it.
	//   unknown — writes nothing and records the cause, so the product can
	//             say why and since when.
	//
	// `pane_id` is untouched in every branch. It is the anchor: a block whose
	// session is gone is still a command that ran, in the pane it ran in.
	//
	// A session this incarnation did not carry over is ErrNotPending.
	Apply(ctx context.Context, j SessionJudgement) error
	// SweepStale is the REPLACEMENT BOUND, and it is not optional.
	//
	// `dropDeadSessions` was the only bound on session recordings: they are
	// deliberately outside the budget sweep because its unit is
	// `artifacts.byte_len` ordered by `ingest_seq`, and a recording has
	// neither. The `absent` path restores that bound for the ordinary case —
	// a host that comes back and reports a session gone leaves no recording
	// behind. What it cannot bound is the `unknown` case: a host that never
	// comes back would accumulate recordings forever.
	//
	// So a session still unreconciled `age` after it was marked has its
	// recording and its row removed, WITHOUT being judged absent — the product
	// says the host was never reachable again, never that the session ended.
	// The same transaction closes open entries as unknown (or interrupted for
	// an ask), preserving each entry's pane anchor so forgetting the pending
	// marker cannot make the block render as running.
	//
	// It returns how many sessions were bounded.
	SweepStale(ctx context.Context, age time.Duration) (int, error)
}

// DefaultUnreconciledRetention is how long a recording nobody could judge is
// kept: a week, which is long enough to cover a laptop shut for a holiday
// weekend and short enough that a host which never returns cannot accumulate
// indefinitely. Each recording is separately bounded by the per-command cap,
// so the exposure is that cap times the sessions of one week.
//
// It is a CONSTANT and not yet a setting, deliberately: what this bead owes is
// that the bound exists at all, and choosing the number in Settings is
// ordinary product work with its own surface.
const DefaultUnreconciledRetention = 7 * 24 * time.Hour
