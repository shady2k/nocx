package proto

// The five wire ops of the one-shot write path (nocx-6q1uh.5, design §6, §7.2):
// a consistent SNAPSHOT of a session's screen, a one-shot TARGET minted from
// it, the INTENT that spends a target, a non-mutating STATUS poll for the
// same, and the ACCESS-BUMP a revocation sends ahead of whatever intents are
// still queued.
//
// # Naming: bare verbs, like every other op on this service
//
// Every op this service already answers is a bare verb scoped by the
// envelope's own service name (`spawn`, `resize`, `screen`, `replay` —
// proto.ServiceSession is already "session", so a dispatch envelope of
// {service: "session", op: "session.snapshot"} would repeat the scope the
// envelope already carries, and no op here is spelled that way). The design
// plan that first named these five wrote them as `session.snapshot`,
// `session.target`, `session.intent`, `session.intent-status` and
// `session.access-bump` — written before this package existed to check the
// spelling against. The tree's own convention wins (AGENTS.md, "believe the
// binary/tree over the plan"): `snapshot`, `target`, `intent`,
// `intent-status`, `access-bump`, and contracts/helper's file names follow
// the same bare spelling (session.snapshot.schema.json etc — the LEADING
// "session." there is the CONTRACT DIRECTORY's own file-naming convention,
// shared by every op on this service, e.g. session.screen.schema.json for
// the bare `screen` op — not a second op-naming scheme).
const (
	// OpSnapshot answers a consistent read of a session's screen — the rows a
	// target's digest is computed from, the cursor, and the frame a
	// coordinator classifies with the agent rule — retained for a short
	// window so a mint against the SAME snapshotId describes the one frame
	// the coordinator actually classified (design §6.1).
	OpSnapshot = "snapshot"
	// OpTarget mints a one-shot, signed target from a snapshotId this
	// session's ring still holds: the structural digest of the named rows,
	// bound to the screen identity and access epoch the snapshot carried
	// (design §6.2).
	OpTarget = "target"
	// OpIntent spends a target: a key or text payload, validated against a
	// fresh read of the same screen the target named, encoded against the
	// terminal's own modes and written at most once (design §6.5).
	OpIntent = "intent"
	// OpIntentStatus answers what became of a token-bound intent without
	// presenting its payload again: unknown, in_progress, or the recorded
	// (compact, regionOmitted) result (design §6.2).
	OpIntentStatus = "intent-status"
	// OpAccessBump raises a session's access epoch and refuses every older
	// uncommitted intent still queued, acknowledging once they are all
	// terminal (design §7.2). It is how a revocation makes itself provable
	// without an answer: a session that never acknowledges is still bounded
	// by every queued intent's own commitBy.
	OpAccessBump = "access-bump"
)

// SnapshotParams names the session a consistent screen read is taken from.
type SnapshotParams struct {
	Session HostSessionID `json:"session"`
}

// SnapshotResult is one consistent read of a session's screen (design §6.1):
// the frame a coordinator classifies with the agent rule, the runtime facts
// that belong to it, and the access epoch and read-barrier facts a target
// minted from THIS snapshot inherits.
type SnapshotResult struct {
	// SnapshotID names this reading within the session's retained ring
	// (snapshotRing = 8, each held at most snapshotMaxAge). session.target
	// mints against it; an evicted or aged-out id is refused snapshot_gone.
	SnapshotID uint64      `json:"snapshotId"`
	Frame      ScreenFrame `json:"frame"`
	// Revision is the runtime's monotonic clock at the read, the same fact
	// session.screen already reports for the same reason (design §5).
	Revision uint64 `json:"revision"`
	// InputFence is the highest input fence whose write had completed the
	// moment this snapshot was taken (design §5.4): read BEFORE the screen,
	// so it never claims to be newer than what the rows actually reflect.
	InputFence uint64 `json:"inputFence"`
	// Completeness is what the runtime can claim about the stream it holds,
	// the same closed vocabulary session.screen already answers with.
	Completeness Completeness `json:"completeness"`
	// AccessEpoch is this session's current access epoch (design §7.2),
	// stored with every target minted from this snapshot and carried by
	// every intent that spends one.
	AccessEpoch uint64 `json:"accessEpoch"`
	// ReadBarrier reports whether this session's process offers the read
	// barrier a detached writer needs before an SSH session's writes may be
	// trusted (nocx-6q1uh.3, spec §5.7). A local session always reports
	// true; it is carried on every snapshot rather than being a separate
	// question so a caller never reads a barrier fact and a screen fact from
	// two different instants.
	ReadBarrier bool `json:"readBarrier"`
}

// TargetParams mints a one-shot target from a retained snapshot: which rows,
// and which of the four kinds the agent rule (design §6.3) names for them.
//
// It deliberately carries no `includeCursor`: [design §6.2] draws that
// property from the kind alone — `input` targets always carry the caret into
// their digest, no other kind ever does — and [tokenBook.Mint] (nocx-6q1uh.4)
// already derives it that way. A field a caller could set and this op would
// silently ignore is worse than no field: contracts/README's third check
// (AGENTS.md rule 5) is built on trusting that a field on the wire does
// something, and a param session.target does not read would be exactly the
// dead API surface that check exists to catch.
type TargetParams struct {
	Session    HostSessionID `json:"session"`
	SnapshotID uint64        `json:"snapshotId"`
	// Kind is one of the closed set design §6.3 names: menu, input, working,
	// region.
	Kind  string `json:"kind"`
	First int    `json:"first"`
	Last  int    `json:"last"`
}

// TargetResult is a minted, one-shot target.
type TargetResult struct {
	// Token is the whole signed target, self-describing and opaque to the
	// caller: what session.intent presents back to spend it.
	Token string `json:"token"`
	// TokenID names the same target without exposing its signature — what
	// session.intent-status polls by, so a status caller need not hold (or
	// re-transmit) the full signed token to ask what became of it.
	TokenID     string `json:"tokenId"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// IntentParams spends one target: the token it was minted onto, the access
// epoch and commit deadline the coordinator read at the same moment, and the
// key or text payload design §6.5 defines.
type IntentParams struct {
	Session HostSessionID `json:"session"`
	Token   string        `json:"token"`
	// AccessEpoch is the epoch the coordinator believes is current (design
	// §7.2) — bound into the token's canonical intent at the commit point, so
	// a replay of the same token under a DIFFERENT epoch is refused
	// token_spent rather than silently re-admitted under the new one.
	AccessEpoch uint64 `json:"accessEpoch"`
	// CommitBy is an absolute deadline on the shared monotonic clock
	// (internal/monoclock), 5s after the coordinator sent this intent. The
	// owner refuses commit_deadline at receipt and at the commit point for
	// any intent past it.
	CommitBy int64 `json:"commitBy"`
	// Kind is "key" or "text" (design §6.5); sessionruntime.IntentKindKey/
	// IntentKindText is what it maps to.
	Kind string `json:"kind"`
	// Payload is the intent's argument: a key name (sessionruntime's own
	// name+modifier spelling, e.g. "ctrl+c") for kind "key", or raw text for
	// kind "text". Encoding it against the terminal's own modes happens at
	// the commit point, never here.
	Payload []byte `json:"payload"`
}

// IntentResult is what became of one session.intent (design §6.5). State is
// the closed set: executed, refused, failed_partial, delivery_unknown,
// cancelled, in_progress — never a bare "failed", because a caller acting on
// this answer needs to know whether SOME bytes reached the program.
type IntentResult struct {
	State        string `json:"state"`
	BytesWritten int    `json:"bytesWritten"`
	FenceAfter   uint64 `json:"fenceAfter"`
	// RetryAfterMs is set only alongside state "in_progress": the caller's
	// own write is still resolving (a replay of a token whose first attempt
	// has not settled), and this is how long before asking again is worth
	// it.
	RetryAfterMs int `json:"retryAfterMs,omitempty"`
	// Refusal is set only when State is "refused".
	Refusal *IntentRefusal `json:"refusal,omitempty"`
}

// IntentRefusal names why a session.intent was refused, from the closed set
// design §6.5 draws: stale_target, incomparable, expired, forged,
// token_spent, snapshot_gone, completeness_unknown, cannot_encode,
// would_submit, access_revoked, no_read_barrier, commit_deadline, capacity,
// busy, closing.
type IntentRefusal struct {
	Cause string `json:"cause"`
	// RegionNow is the region the target named, AS IT READS NOW — present
	// only on the response that produced this refusal, and bounded to
	// regionNowMax (16 KiB) with RegionTruncated set beyond it. A replay or a
	// session.intent-status poll never carries it (RegionOmitted instead):
	// design §6.2's "the caller re-reads" rather than trusting a stale echo.
	RegionNow       string `json:"regionNow,omitempty"`
	RegionTruncated bool   `json:"regionTruncated,omitempty"`
	RegionOmitted   bool   `json:"regionOmitted,omitempty"`
}

// IntentStatusParams polls what became of one token-bound intent, without
// presenting its payload again.
type IntentStatusParams struct {
	Session HostSessionID `json:"session"`
	TokenID string        `json:"tokenId"`
}

// IntentStatusResult mirrors the compact record session.intent itself would
// have answered, minus the one-time region — State is "unknown" (never
// minted, or its slot has since released), "in_progress" (bound, awaiting a
// result), or the terminal state the bound intent settled at (mirroring
// Result.State when Result is present).
type IntentStatusResult struct {
	State  string        `json:"state"`
	Result *IntentResult `json:"result,omitempty"`
}

// AccessBumpParams raises a session's access epoch to at least above+1.
type AccessBumpParams struct {
	Session HostSessionID `json:"session"`
	// Above names the epoch this bump supersedes. Idempotent on it: a repeat
	// bump naming the same value returns the epoch already in force rather
	// than raising it again or re-revoking anything.
	Above uint64 `json:"above"`
}

// AccessBumpResult is the epoch now in force. It carries no refusal: a bump
// always applies (design §7.2 names no case where it may not), so a caller
// that cannot reach the session at all sees a transport-level error rather
// than a typed refusal here.
type AccessBumpResult struct {
	Epoch uint64 `json:"epoch"`
}
