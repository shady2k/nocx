package content

// Schema v1 of the one authoritative ledger (nocx-rtg0.2), per ADR-0019,
// ADR-0020 and design §5.2. The types here are the public repository seam:
// ContentDB.Ledger() returns a LedgerRepository, the only writer of the v1
// tables. The interim command_history table and CommandHistoryRepository are
// untouched by this surface — they are the live path until nocx-rtg0.3 cuts
// the wire protocol over to ledger.* (design §6.2), and nothing may write
// both (ADR-0019 §4).
//
// Until that cutover the v1 write path has NO PRODUCTION CALLER — only
// tests. Stated loudly because the same shape shipped once before
// (nocx-rtg0: ContentDB.Add reachable only from its own tests while a
// reachable read path hid the unreachable write): the v1 tables are
// schema-complete and test-proven, and deliberately not wired into the
// transport until nocx-rtg0.3.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ── closed enums; each mirrors a CHECK constraint in schemaV1 ─────────────

type EntryKind string

const (
	EntryShell  EntryKind = "shell"
	EntryAgent  EntryKind = "agent"
	EntryAction EntryKind = "action"
)

// Phase is the entry lifecycle (design §3.7): open until execution is
// confirmed, bound while an execution runs, closed once the outcome is
// known. Owned by the driver; the store only records and sweeps.
type Phase string

const (
	PhaseOpen   Phase = "open"
	PhaseBound  Phase = "bound"
	PhaseClosed Phase = "closed"
)

type EntryStatus string

const (
	EntryPending     EntryStatus = "pending"
	EntryRunning     EntryStatus = "running"
	EntrySuccess     EntryStatus = "success"
	EntryFailure     EntryStatus = "failure"
	EntryInterrupted EntryStatus = "interrupted"
	EntryUnknown     EntryStatus = "unknown"
)

// Relation is the edge vocabulary (design §3.4).
type Relation string

const (
	RelRerunOf    Relation = "rerun-of"
	RelSupersedes Relation = "supersedes"
	RelCausedBy   Relation = "caused-by"
	RelCites      Relation = "cites"
	RelInSpan     Relation = "in-span"
	// RelReferences joins a question entry to the frame entry it points at
	// (design §5 — the edge carries the region in its payload).
	RelReferences Relation = "references"
)

type MediaType string

const (
	MediaVT       MediaType = "application/vt"
	MediaText     MediaType = "text/plain"
	MediaMarkdown MediaType = "text/markdown"
	MediaJSON     MediaType = "application/json"
)

type ArtifactState string

const (
	ArtifactOpen   ArtifactState = "open"
	ArtifactSealed ArtifactState = "sealed"
)

// Truncation is the primary reason an artifact does not hold the whole
// stream (design §3.5): a cap dropped the middle, a gap lost a range,
// suppression means capture was refused by policy.
type Truncation string

const (
	TruncCap        Truncation = "cap"
	TruncGap        Truncation = "gap"
	TruncSuppressed Truncation = "suppressed"
)

type Sensitivity string

const (
	SensitivityNormal    Sensitivity = "normal"
	SensitivitySensitive Sensitivity = "sensitive"
)

// Criticality gates behaviour (design §3.1) and is therefore contextual —
// it lives on the environment observation, never on the host.
type Criticality string

const (
	CriticalityRoutine   Criticality = "routine"
	CriticalitySensitive Criticality = "sensitive"
	CriticalityCritical  Criticality = "critical"
)

type EnvironmentKind string

const (
	EnvLocal     EnvironmentKind = "local"
	EnvSSH       EnvironmentKind = "ssh"
	EnvContainer EnvironmentKind = "container"
	EnvUnknown   EnvironmentKind = "unknown"
)

// Interactivity is the execution's input policy (ADR-0020 §2, §3);
// awaiting-takeover is the protocol transition where the human owns the
// lane and the agent is demoted, not evicted.
type Interactivity string

const (
	InteractivityNone          Interactivity = "none"
	InteractivityStdin         Interactivity = "stdin"
	InteractivityTTY           Interactivity = "tty"
	InteractivityAwaitTakeover Interactivity = "awaiting-takeover"
)

// TerminationReason distinguishes the five outcomes a single status plus
// exit code cannot (ADR-0020 §4): the command failed, the executor timed
// out, the transport vanished, the user killed it, the agent declined.
type TerminationReason string

const (
	TermCompleted     TerminationReason = "completed"
	TermFailed        TerminationReason = "failed"
	TermTimeout       TerminationReason = "timeout"
	TermTransportGone TerminationReason = "transport-gone"
	TermUserKilled    TerminationReason = "user-killed"
	TermAgentDeclined TerminationReason = "agent-declined"
	TermInterrupted   TerminationReason = "interrupted"
)

// GrantPolicy is the autonomy preset the workspace mints (ADR-0020 §7).
type GrantPolicy string

const (
	GrantAskEveryTime GrantPolicy = "ask-every-time"
	GrantAskOnMutate  GrantPolicy = "ask-on-mutate"
	GrantAutonomous   GrantPolicy = "autonomous"
)

type ResourceKind string

const (
	ResourceEnvironment ResourceKind = "environment"
	ResourceSession     ResourceKind = "session"
	ResourcePath        ResourceKind = "path"
	ResourceCredential  ResourceKind = "credential"
	ResourceDestination ResourceKind = "destination"
	ResourceTool        ResourceKind = "tool"
)

// CaptureMethod records whether artifact text came from terminal cells, from
// raw output, from serialized block HTML, or was never captured (ADR-0019
// §6: derived text must be able to say how it was taken).
type CaptureMethod string

const (
	CaptureTerminalCells  CaptureMethod = "terminal-cells"
	CaptureRawOutput      CaptureMethod = "raw-output"
	CaptureSerializedHTML CaptureMethod = "serialized-html"
	CaptureNone           CaptureMethod = "none"
)

type Stream string

const (
	StreamStdout   Stream = "stdout"
	StreamStderr   Stream = "stderr"
	StreamCombined Stream = "combined"
)

// ── records ───────────────────────────────────────────────────────────────

// Workspace is narrative and presentation scope (ADR-0020 §5): which
// sessions read as one story. It mints default grants from its policy; it is
// never the enforcement object.
type Workspace struct {
	ID   string
	Name string
}

// Session is a restore key, never a recall filter (ADR-0019 §5): it names
// "that tab". An entry outlives its session (ON DELETE SET NULL).
type Session struct {
	ID          string // server-authoritative (AD-7)
	WorkspaceID string
}

// Environment is the durable identity of where work happens (design §3.1,
// amended): kind, endpoint and profile only. Mutable facts — branch,
// container id, privilege, criticality — live in Observations, so old
// entries are never reinterpreted with today's facts.
type Environment struct {
	ID        string
	Kind      EnvironmentKind
	Endpoint  *string // canonical user@host:port; nil for local
	ProfileID *string
	Payload   string // identity facets JSON (sparse extension only)
}

// EnvironmentIDFor derives the environment id from its facets (design §3.1:
// "derived from facets, never from a session"). Deterministic: the same
// kind + endpoint always names the same environment, across restarts and
// across sessions to the same destination. The endpoint facet is the
// canonical user@host:port when known; this slice derives it from the
// session's own facts, so a refinement of the facet (e.g. the ssh
// resolver's canonical endpoint) changes the id — which is correct: a
// changed identity is a new id, never an UPDATE (EnsureEnvironment).
func EnvironmentIDFor(kind EnvironmentKind, endpoint string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(string(kind)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(endpoint))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Observation is one versioned snapshot of an environment's mutable facts.
// Append-only: version ascends per environment and an execution pins the
// observation current when it started.
type Observation struct {
	ID            int64 // filled on read; the row identity executions pin
	EnvironmentID string
	Version       int
	ObservedAt    int64  // backend wall clock, display only
	Confidence    string // JSON per-facet: asserted | derived | unknown
	Criticality   Criticality
	Payload       string // facet values JSON: branch, containerId, privilege, …
}

// SubmitEntry carries the client-minted intent. The client id is an
// UNTRUSTED idempotency key: the store binds it to Client and a digest of
// the submitted content, so a replay of the same id aliases the same intent
// and a replay with different content is refused (ErrIDConflict).
type SubmitEntry struct {
	ID             string // client-minted UUIDv7
	Client         string // client identity binding the idempotency key
	EnvironmentID  string
	SessionID      *string
	Cwd            string
	Kind           EntryKind
	Intent         string
	ConversationID *string
	StartedAt      *int64 // frontend monotonic clock — durations only
	EndedAt        *int64
	DurationMs     *int64
	Sensitivity    Sensitivity
	Payload        string // kind payload JSON (sparse extension only)
}

// SubmitResult is the store's answer to Submit: the backend-assigned
// ingest_seq (commit order, NOT causality — ADR-0019 §2), the store-stamped
// wall clock, and whether the id was a replay of the same submission.
type SubmitResult struct {
	ID          string
	IngestSeq   int64
	SubmittedAt int64
	Replayed    bool
}

// StartExecution begins one run of an entry (ADR-0020 §4): a rerun, a
// retry, a takeover and an infrastructure failure are executions of the same
// entry, never new intents. The store pins the environment observation
// current at this moment — a later observation does not move it. Grant, when
// non-nil, is the authority grant recorded on the run: versioned, expiring,
// immutable once execution starts (the workspace minted it; it is not the
// enforcement object).
type StartExecution struct {
	EntryID            string
	Lane               *string
	Attempt            int
	LeaseDeadline      *int64
	InactivityDeadline *int64
	Interactivity      Interactivity
	ProcessGroup       *string
	Executor           *string
	Grant              *Grant
}

// FinishExecution closes one run and the entry with it: the termination
// reason is the execution's fact, the status is the entry's final one.
type FinishExecution struct {
	EndedAt           int64
	TerminationReason TerminationReason
	Status            EntryStatus
}

// FinishAgentRun is the terminal close of an assistant run (the state
// machine this slice's driver persists: prepared → streaming → completed |
// failed | cancelled | interrupted). The run's terminal state and the
// entries close in ONE transaction (FinishAgentRun); the failure sentence —
// what agent.runState's error carries, a sentence a person reads, never a
// Go error string — is recorded on the run's payload.
type FinishAgentRun struct {
	State             RunState // RunCompleted or RunFailed (this slice)
	TerminationReason TerminationReason
	Error             string // the renderable reason; empty when completed
	EndedAt           int64
}

// Grant is the authority recorded on a run (ADR-0020 §5).
type Grant struct {
	Version   int
	ExpiresAt int64
	Policy    GrantPolicy
	Scopes    []GrantScope
}

// GrantScope is one resource the grant touches — what "this run held a grant
// for these environments and touched these three sessions" is a query over.
type GrantScope struct {
	Kind ResourceKind
	ID   string
}

// ── the assistant ask (design §5, §7; bead nocx-f4s5) ────────────────────

// RunState is the assistant run's state machine, ON THE WIRE because the
// renderer draws it (design §7): prepared → streaming → awaiting_approval →
// one of the terminal states. `interrupted` is what a run becomes when the
// backend restarts and finds it non-terminal (design §4.2). A reconnecting
// renderer reads the state; it never infers liveness from notifications
// having stopped.
type RunState string

const (
	RunPrepared         RunState = "prepared"
	RunStreaming        RunState = "streaming"
	RunAwaitingApproval RunState = "awaiting_approval"
	RunCompleted        RunState = "completed"
	RunCancelled        RunState = "cancelled"
	RunFailed           RunState = "failed"
	RunInterrupted      RunState = "interrupted"
)

// IsTerminal reports whether the state is in the closed terminal set — the
// only states a run may rest in forever.
func (s RunState) IsTerminal() bool {
	switch s {
	case RunCompleted, RunCancelled, RunFailed, RunInterrupted:
		return true
	}
	return false
}

// FrameIntent marks a frame entry (kind=agent): the capture is a fact,
// complete at ingest, closed at capture time. The ask's reference check
// recognises frames by kind+intent — an id that is not a frame is refused.
const FrameIntent = "frame-capture"

// FrameSource is which capture path a frame came from (design §2.2). The
// two sources are NOT the same path and are never silently substituted: a
// live frame is cells+attributes from the active xterm buffer; a frozen
// frame is already-serialized TEXT (the block's xterm cells are gone).
type FrameSource string

const (
	FrameLive   FrameSource = "live"
	FrameFrozen FrameSource = "frozen"
)

// FrameAttrs mirrors the renderer's CellAttrs
// (frontend/src/scrollback/serializer.ts): the per-cell attributes as
// xterm reports them, resolved against the theme snapshot taken at mint
// time. AD-8: the serializer owns the extraction; this is the wire/storage
// shape, never a re-derivation.
type FrameAttrs struct {
	Fg            *string `json:"fg"`
	Bg            *string `json:"bg"`
	Bold          bool    `json:"bold"`
	Italic        bool    `json:"italic"`
	Dim           bool    `json:"dim"`
	Underline     bool    `json:"underline"`
	Inverse       bool    `json:"inverse"`
	Blink         bool    `json:"blink"`
	Strikethrough bool    `json:"strikethrough"`
	Overline      bool    `json:"overline"`
}

// FrameCell is one cell of a live frame.
type FrameCell struct {
	Char  string     `json:"char"`
	Attrs FrameAttrs `json:"attrs"`
}

// FrameRow is one row of a frame. Live rows are cells+attributes; frozen
// rows are TEXT — a frozen block has no xterm cells left and its text has
// already been transformed by the serializer. The row kind records which;
// the two are never substituted for one another.
type FrameRow struct {
	Kind  string      `json:"kind"` // "cells" | "text"
	Cells []FrameCell `json:"cells,omitempty"`
	Text  string      `json:"text,omitempty"`
}

// FrameCursor is the absolute cursor position of a live frame. A frozen
// frame has no cursor — a serialized block has none (null on the wire).
type FrameCursor struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}

// BufferIdentity names which buffer instance a frame belongs to: the normal
// buffer is one instance; EACH entry into the alternate screen is a new one
// (entering mints a new identity, leaving terminates it — design §2.3).
type BufferIdentity struct {
	Kind       string `json:"kind"` // "normal" | "alternate"
	AltSession *int   `json:"altSession,omitempty"`
}

// FrameIdentity is the live capture identity: buffer instance, geometry and
// a content generation that advances on write-parse plus the explicit
// state-changing operations — NEVER on repaint (ADR-0005 forces periodic
// repaints on Linux/WebKitGTK). The backend stores it; it never refuses on
// it: ADR-0029 — generation inequality is a trigger, never a verdict, and
// comparability is same | moved | notComparable, never a "stale" flag.
type FrameIdentity struct {
	Buffer     BufferIdentity `json:"buffer"`
	Cols       int            `json:"cols"`
	Rows       int            `json:"rows"`
	Generation int            `json:"generation"`
}

// FrameRange is a live frame's absolute buffer row range ([start, end)) —
// the provenance records what rows survive, and that the 10000-line
// scrollback cap may already have evicted rows above it.
type FrameRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// CaptureFrame carries one renderer-minted frame into the ledger
// (agent.captureFrame, design §7). The renderer ingests the frame FIRST;
// the backend mints the frame id and an ask references it.
type CaptureFrame struct {
	// CaptureID is the renderer's idempotency key — UNTRUSTED, like
	// Submit's client-minted id: bound to Client and a digest of the frame
	// content, so a replay returns the original frame id and the same key
	// with different content is ErrIDConflict. Without it a lost response
	// orphans a duplicate frame on every retry.
	CaptureID string
	// Client is the client identity binding the idempotency key.
	Client string
	// Env is the environment the frame was captured in, derived from the
	// session by the caller (the backend never trusts the renderer's idea
	// of where it is).
	Env Environment
	// SessionID is the tab the frame was captured from — the ownership key
	// an ask is checked against ("an ask naming a frame from another
	// session is rejected", design §5).
	SessionID *string
	// Cwd is where the capture happened (the renderer's OSC 7 fact).
	Cwd    string
	Source FrameSource
	Rows   []FrameRow
	// Cursor is the live cursor; null for a frozen frame.
	Cursor *FrameCursor
	// Identity is the live capture identity — required for source=live.
	Identity *FrameIdentity
	// Range is the live frame's absolute buffer row range — required for
	// source=live.
	Range *FrameRange
	// SerializerVersion is the frozen serializer version — required for
	// source=frozen (SERIALIZER_VERSION in frontend/src/scrollback).
	SerializerVersion *int
}

// CaptureFrameResult is the answer to a capture: the BACKEND-MINTED frame
// id (AD-7 — the renderer cannot invent one) and whether this call was a
// replay of an earlier capture.
type CaptureFrameResult struct {
	FrameID  string
	Replayed bool
}

// AgentReference is one "frame id + region" of an ask (design §2.2, §7).
type AgentReference struct {
	FrameID string
	Region  FrameRegion
}

// FrameRegion is a rectangle or row range INSIDE a frame (design §2.2):
// rows are relative to the frame's own rows. A frozen frame has no columns
// — its region is a full-width row range, so ColStart/ColEnd are absent
// there (rejected if present).
type FrameRegion struct {
	RowStart int
	RowEnd   int
	ColStart *int
	ColEnd   *int
}

// AnswerIntent marks an answer entry (kind=agent): the model's streamed
// reply to a question, joined to it by a caused-by edge (design §5 — the
// answer is an entry, never a string held in a map that dies with the
// process).
const AnswerIntent = "answer"

// RunFacts is the run's configuration as it was at the time (design §5:
// "run mode, endpoint and model as they were at the time") — pinned into
// the execution's payload at ask time, so a later endpoint change never
// reinterprets what the run used. The credential is never part of this:
// the key is resolved at stream time, never persisted.
type RunFacts struct {
	Mode       string `json:"mode"` // "explain" — the only mode this slice knows
	EndpointID string `json:"endpointId,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
	Model      string `json:"model,omitempty"`
}

// AgentAsk is one ask transaction (agent.ask, design §5, §7): the question
// text plus references to already-captured frames. The backend records the
// frame references, the question, the answer entry and a PENDING RUN in ONE
// atomic create, before the model would be called — the identities and the
// recovery state exist first, or a frame lands with no question, a question
// with no run, a run with no answer entry, or a retry duplicates both.
type AgentAsk struct {
	// ID is the renderer-minted ask id — the UNTRUSTED idempotency key of
	// the question entry (the schema's own "client-minted UUIDv7" rule):
	// bound to Client and a digest of the ask content, so a replay returns
	// the original run id and the same id with different content is
	// ErrIDConflict.
	ID         string
	Client     string
	Env        Environment
	SessionID  *string
	Cwd        string
	Question   string
	References []AgentReference
	// Facts is the endpoint+model the run will use, resolved before the
	// transaction (with none configured the ask never reaches the
	// transaction — it is refused at the wire).
	Facts RunFacts
}

// AgentAskResult is the answer to an ask: the BACKEND-MINTED run id (the
// execution row — what agent.cancel/approve/status will address), the
// question entry id, the ANSWER entry id (where the streamed deltas land —
// agent.runDelta's entryId), the answer artifact id, the question's
// ingest_seq and whether this was a replay. The run is in state prepared:
// recorded, never executed.
type AgentAskResult struct {
	RunID            int64
	QuestionID       string
	AnswerEntryID    string
	AnswerArtifactID string
	IngestSeq        int64
	Replayed         bool
}

// The ask's reference-validation failures — reachable from the renderer (an
// unknown id, a frame from another tab, an out-of-bounds rectangle) and
// refused, never truncated or silently re-scoped. They are invalid params,
// not server faults: the transport maps them to -32602.
var (
	ErrFrameNotFound        = errors.New("content: no such frame")
	ErrNotAFrame            = errors.New("content: id is not a frame")
	ErrFrameSessionMismatch = errors.New("content: frame belongs to another session")
	ErrRegionOutOfBounds    = errors.New("content: region is out of bounds")
	ErrNoSuchRun            = errors.New("content: no such run")
)

// AppendArtifact creates one artifact of an execution, with its capture
// provenance (ADR-0019 §6). Content arrives via AppendChunk; an artifact is
// never one BLOB.
type AppendArtifact struct {
	ExecutionID    int64
	ID             string // client-minted UUIDv7
	MediaType      MediaType
	DerivedFrom    *string
	Pinned         bool
	Truncated      *Truncation
	CaptureMethod  CaptureMethod
	CaptureVersion int
	TerminalCols   *int
	TerminalRows   *int
	Stream         *Stream
	ByteOffset     *int64
	ByteEnd        *int64
	Encoding       string
	Gaps           []Gap
	Payload        string
}

// Gap is one dropped byte range in a captured stream.
type Gap struct {
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Reason string `json:"reason"`
}

// Edge is one relation between entries (design §3.4): the difference
// between a log and a memory. Cheap, one narrow table.
type Edge struct {
	From string
	To   string
	Rel  Relation
	// Payload is the edge's sparse extension — for a `references` edge it
	// is the region JSON (FrameRegion). Default '{}'; the store never
	// interprets it.
	Payload string
}

// LedgerEntrySummary is one row of the timeline: enough to page and render
// the recall flow without hauling executions.
type LedgerEntrySummary struct {
	ID            string
	IngestSeq     int64
	EnvironmentID string
	Cwd           string
	Kind          EntryKind
	Intent        string
	Phase         Phase
	Status        EntryStatus
	SubmittedAt   int64
}

// LedgerEntry is the recall-shaped read: the entry with every execution and
// each execution's pinned observation, grant and artifacts.
type LedgerEntry struct {
	ID             string
	IngestSeq      int64
	Client         string
	Digest         string
	EnvironmentID  string
	SessionID      *string
	Cwd            string
	Kind           EntryKind
	Intent         string
	Phase          Phase
	Status         EntryStatus
	ConversationID *string
	SubmittedAt    int64
	StartedAt      *int64
	EndedAt        *int64
	DurationMs     *int64
	Sensitivity    Sensitivity
	ReviewedAt     *int64
	Payload        string
	Executions     []Execution
}

// Execution is one run: lease bounds, interactivity policy, process group,
// start/end, termination reason, executor identity and — for an agent run —
// the run state the renderer draws. Artifacts attach to the execution, not
// to the intent.
type Execution struct {
	ID                 int64
	EntryID            string
	Lane               *string
	Attempt            int
	Observation        Observation
	LeaseDeadline      *int64
	InactivityDeadline *int64
	Interactivity      Interactivity
	ProcessGroup       *string
	StartedAt          *int64
	EndedAt            *int64
	TerminationReason  *TerminationReason
	// State is the assistant run state (prepared | streaming |
	// awaiting_approval | completed | cancelled | failed | interrupted).
	// NULL on executions that are not agent runs — a frame capture, a
	// future shell execution — so the startup sweep (every non-terminal
	// run becomes interrupted) never touches them.
	State    *RunState
	Executor *string
	Grant    *Grant
	// Payload is the execution's sparse extension (the run's pinned facts:
	// endpoint+model at ask time; the failure sentence after a failed
	// close).
	Payload   string
	Artifacts []Artifact
}

// Artifact is one capture of an execution, with provenance. Chunks carries
// the bodies in seq order; it is nil on artifacts embedded in LedgerEntry
// (the recall read must not haul bytes — Artifact fetches them).
type Artifact struct {
	ID             string
	ExecutionID    int64
	MediaType      MediaType
	DerivedFrom    *string
	State          ArtifactState
	ByteLen        int64
	ChunkCount     int
	Pinned         bool
	Truncated      *Truncation
	CaptureMethod  CaptureMethod
	CaptureVersion int
	TerminalCols   *int
	TerminalRows   *int
	Stream         *Stream
	ByteOffset     *int64
	ByteEnd        *int64
	Encoding       string
	Gaps           []Gap
	Payload        string
	Chunks         [][]byte
}

// LedgerRepository is the typed repository for schema v1 (ADR-0019,
// ADR-0020, design §5.2). It is the ONLY writer of the v1 tables; the
// interim CommandHistoryRepository writes command_history, and nothing may
// write both. The write path has no production caller until nocx-rtg0.3.
type LedgerRepository interface {
	// CreateWorkspace records a narrative scope.
	CreateWorkspace(ctx context.Context, ws Workspace) error
	// CreateSession records a restore key under a workspace.
	CreateSession(ctx context.Context, sess Session) error
	// DeleteSession removes a restore key; entries keep their rows and
	// lose the reference (ON DELETE SET NULL — an entry outlives its
	// session, ADR-0019 §5).
	DeleteSession(ctx context.Context, id string) error
	// EnsureEnvironment records durable identity; the first write wins
	// (identity is derived from the facets, so a changed identity is a
	// new id, not an UPDATE).
	EnsureEnvironment(ctx context.Context, env Environment) error
	// RecordObservation appends one versioned observation and returns its
	// row identity — what an execution pins. Append-only: a later
	// observation never rewrites an earlier one.
	RecordObservation(ctx context.Context, obs Observation) (int64, error)
	// Submit accepts an intent as an open entry and returns the
	// backend-assigned ingest_seq. Two entries in the same millisecond
	// still get distinct, ordered sequences — wall time is not a key.
	// Idempotent for (id, client, digest): a replay returns the original
	// row; the same id with different content is ErrIDConflict.
	Submit(ctx context.Context, in SubmitEntry) (SubmitResult, error)
	// Entry is the recall read: the entry, its executions, each
	// execution's pinned observation and grant, and its artifacts
	// (metadata only — no chunk bodies). Nil when no row carries id.
	Entry(ctx context.Context, id string) (*LedgerEntry, error)
	// ListEntries returns the limit newest entries, newest first, ordered
	// by ingest_seq — commit order, never by wall clock.
	ListEntries(ctx context.Context, limit int) ([]LedgerEntrySummary, error)
	// DeleteEntry removes an entry; edges referencing it and its
	// executions (and their artifacts, chunks and grant) cascade. A pin
	// protects against background eviction, not against this.
	DeleteEntry(ctx context.Context, id string) error
	// StartExecution begins one run, pinning the environment observation
	// current at this moment, and returns the execution's row identity.
	// Fails when the entry's environment has no observation yet — there
	// is nothing to pin, and an unpinned execution would be
	// reinterpreted later with today's facts.
	StartExecution(ctx context.Context, in StartExecution) (int64, error)
	// FinishExecution closes the run with its termination reason and
	// closes the entry with its final status.
	FinishExecution(ctx context.Context, executionID int64, end FinishExecution) error
	// AppendArtifact creates one artifact of an execution (never a BLOB:
	// content arrives chunked).
	AppendArtifact(ctx context.Context, in AppendArtifact) (string, error)
	// AppendChunk appends one chunk to an artifact and maintains its
	// byte_len (logical content bytes — the retention budget's unit).
	AppendChunk(ctx context.Context, artifactID string, body []byte) error
	// Artifact returns one artifact with its chunk bodies, or nil when no
	// artifact carries id.
	Artifact(ctx context.Context, id string) (*Artifact, error)
	// CaptureFrame ingests one renderer-minted frame (agent.captureFrame)
	// and returns the BACKEND-MINTED frame id — the frame lands as its own
	// closed entry (kind=agent, intent=frame-capture) whose execution owns
	// the frame artifact: cells-derived text with capture provenance, in
	// bounded ordered chunks, sealed at ingest. Idempotent on
	// (CaptureID, Client, digest): a replay returns the original id; the
	// same capture id with different content is ErrIDConflict. A frame that
	// is never referenced is an orphan and is swept by retention (a later
	// bead — byte accounting does not exist yet).
	CaptureFrame(ctx context.Context, in CaptureFrame) (CaptureFrameResult, error)
	// SubmitAgentAsk records ONE ask transaction atomically (agent.ask):
	// the question entry (kind=agent, open/pending), its pending run (the
	// backend-minted execution row, state prepared — the model is never
	// called by this method) and the references edges to the captured
	// frames, each carrying its region. A reference to an unknown id, a
	// non-frame id, a frame from another session or an out-of-bounds
	// region refuses the whole transaction — nothing is left behind.
	// Idempotent on (ID, Client, digest): a replay returns the original
	// run id; the same ask id with different content is ErrIDConflict.
	SubmitAgentAsk(ctx context.Context, in AgentAsk) (AgentAskResult, error)
	// RunState returns the assistant run state of one execution: the
	// durable state a reconnecting renderer reads (design §7 — it never
	// infers liveness from notifications having stopped). Nil when the
	// execution is not an agent run. ErrNoSuchRun when no execution carries
	// id.
	RunState(ctx context.Context, executionID int64) (*RunState, error)
	// TransitionRun moves the assistant run to a NON-TERMINAL state (this
	// slice: prepared → streaming). Terminal states go through
	// FinishAgentRun. The move is refused when the run is already terminal,
	// when the move is not on the machine, and when the execution is not an
	// agent run. The run is non-terminal from the moment the ask transaction
	// commits until a terminal state is persisted — deltas may arrive only
	// inside that span (after the streaming transition commits, before the
	// terminal close).
	TransitionRun(ctx context.Context, runID int64, to RunState) error
	// FinishAgentRun closes the run AND its entries in ONE transaction —
	// the terminal state this slice's driver persists: the run's state, end
	// and termination reason, the question entry, the answer entry (found
	// via its caused-by edge) and the answer artifact (sealed). A run is
	// never reported terminal in the run vocabulary while its entries still
	// say otherwise — both lifecycles close together, or neither does.
	FinishAgentRun(ctx context.Context, runID int64, in FinishAgentRun) error
	// AddEdge records one relation between two entries.
	AddEdge(ctx context.Context, e Edge) error
	// Edges returns every edge touching entryID, in either direction.
	Edges(ctx context.Context, entryID string) ([]Edge, error)
}
