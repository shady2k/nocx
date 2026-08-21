package content

// Schema v1 of the one authoritative ledger (nocx-rtg0.2), per ADR-0019,
// ADR-0020 and design §5.2. The types here are the public repository seam:
// ContentDB.Ledger() returns a LedgerRepository, and since nocx-rtg0.19 it is
// the only writer of a command anywhere in nocx — command_history and
// CommandHistoryRepository are gone, so ADR-0019 §4's "nothing may write
// both" is now satisfied by there being nothing else to write.
//
// The entry lifecycle IS wired as of nocx-rtg0.3: internal/transport's
// ledger.open / ledger.bind / ledger.close (ws_ledger.go) drive Submit,
// StartExecution and FinishExecution through capability.LedgerService, and
// the ask transaction (agent.captureFrame / agent.ask) drives CaptureFrame,
// SubmitAgentAsk, TransitionRun, AppendChunk and FinishAgentRun. The READ
// path is wired as of nocx-rtg0.20: ledger.query drives QueryEntries and
// ledger.get drives Entry plus Edges (ws_ledger_query.go), and the query's
// `host` field is what finally asks a resolved environment row for its host —
// so Environment.Host has a renderer. history.record drives RecordCompleted
// (nocx-rtg0.19), which is where a finished command lands now, under the
// author the renderer minted (nocx-iadtt); ledger.capture drives
// CaptureOutput.
//
// WHAT IS STILL TEST-REACHABLE ONLY: CreateSession, DeleteSession,
// ListEntries, DeleteEntry, AppendArtifact, AddEdge and RunState.
//
// EvictEntries and Watermark are the third category, and it is written down
// rather than rounded to either of the other two: no production caller
// reaches the INTERFACE METHOD, and the behaviour behind it is nonetheless
// live on every submit — evictOnWrite calls the unexported evictEntries
// directly (retention.go), and the query path reads the unexported watermark
// inside its own transaction. So "no caller" here means the seam is untested
// in production, not that retention is asleep.
//
// RewriteRedaction stopped being the awkward case when command_history went
// (nocx-rtg0.19). It is wired and TAKEN: secrets.captureSave reaches it
// through capability.CaptureSaveService, the id router that used to choose a
// store by parsing an integer is gone with the second store, and
// history.record now mints the entry-keyed links that made the ledger arm
// unreachable in production before.
//
// Read that list rather than a deadcode run. `deadcode -filter
// 'nocx/internal/content'` prints nothing for this package and always has —
// RTA reports every method here "reachable only through reflection", so the
// tool cannot tell a wired write path from an unwired one. This is exactly
// the shape that shipped once before (nocx-rtg0: ContentDB.Add reachable
// only from its own tests while a reachable read path hid the unreachable
// write, under a green "deadcode is empty"), so the honest statement lives
// here, next to the seam, and is kept current by hand.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
// The lease (ADR-0020 decision 2) adds the two deadlines a single
// "timeout" cannot tell apart — silence is a different failure from
// slowness — and the output budget, so "which bound ended this run" is
// answerable from the record, never reconstructed.
type TerminationReason string

const (
	TermCompleted     TerminationReason = "completed"
	TermFailed        TerminationReason = "failed"
	TermTimeout       TerminationReason = "timeout"
	TermTransportGone TerminationReason = "transport-gone"
	TermUserKilled    TerminationReason = "user-killed"
	TermAgentDeclined TerminationReason = "agent-declined"
	TermInterrupted   TerminationReason = "interrupted"
	// TermInactivity is the lease's silence bound: the execution produced
	// no output for the inactivity deadline and was terminalized for it.
	// Distinct from TermTimeout because a command can be slow AND alive;
	// silence is the failure that looks like life.
	TermInactivity TerminationReason = "inactivity"
	// TermOutputBudget is the lease's volume bound: the execution produced
	// more than its output budget allowed and was terminalized for it —
	// bounded visibly, never truncated silently.
	TermOutputBudget TerminationReason = "output-budget"
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

// Effect is the ADR-0020 effect lattice — what an execution may do to the
// world (docs/decisions/0020-the-agent-gets-a-lane-authority-is-granted-per-run.md
// decision 6): observe | mutate-reversible | mutate-destructive |
// privilege-change | disclose | cross-boundary | delegate. It lives here
// beside ResourceKind for the same reason ResourceKind does: the ledger owns
// the vocabulary the durable grant record stores, and a consumer (the agent
// tool registry, the policy) consumes it, never duplicates it. A grant names
// the effect classes it permits; authority_grants persists them
// (grant_effects), so "forgot to classify a tool" stops compiling in the
// registry and stops persisting here.
type Effect string

const (
	EffectObserve           Effect = "observe"
	EffectMutateReversible  Effect = "mutate-reversible"
	EffectMutateDestructive Effect = "mutate-destructive"
	EffectPrivilegeChange   Effect = "privilege-change"
	EffectDisclose          Effect = "disclose"
	EffectCrossBoundary     Effect = "cross-boundary"
	EffectDelegate          Effect = "delegate"
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

// Workspace — narrative and presentation scope (ADR-0020 §5): which sessions
// read as one story — is declared in layout.go, together with the tab and the
// pane. It moved there with nocx-isoph.1: the backend now owns the whole
// chain workspace → tab → pane, and a table with two repository owners is
// the defect the design spends most of its length avoiding. The ledger still
// reads it through sessions.workspace_id; it no longer writes it.

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

// Host is the environment's host as the ledger stored it: the endpoint for
// a remote environment, "" for the local machine — the same string
// command_history's host column held and the same one history.query's
// contract calls host.
//
// It is a READ of the facet environmentForSession wrote, never a second
// derivation of it: nothing here asks a session where it is and nothing
// re-hashes an id, so the one owner of "where is this session" (AD-8) is
// still the only thing that decides. When the endpoint facet is refined to
// the canonical user@host:port, THIS is where the host is taken out of it —
// a caller that splits an endpoint itself would be the second owner.
func (e Environment) Host() string {
	if e.Endpoint == nil {
		return ""
	}
	return *e.Endpoint
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
	ID            string // client-minted UUIDv7
	Client        string // client identity binding the idempotency key
	EnvironmentID string
	// PaneID is the block's DURABLE anchor (design §6.1): the pane it ran
	// in, frontend-minted and therefore UNTRUSTED — an id naming no pane is
	// refused with ErrUnknownPane rather than stored dangling. Nil means the
	// entry is attached to no recorded pane, which is what an agent run
	// outside a terminal and every submit before nocx-rtg0.28 look like.
	PaneID         *string
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
// reason is the execution's fact, the status is the entry's final one, and
// the remaining three are the ENTRY's terminal facts — what a close learns
// that only the close can know.
//
// Those three are here rather than on SubmitEntry because Submit is
// write-once by construction: the client-minted id is an idempotency key
// bound to a digest of the submitted content, so routing a later fact
// through Submit would change the digest and turn every outbox replay of the
// original open into ErrIDConflict (nocx-rtg0.23). A close is the second
// event on one row, never a second submission of one intent.
type FinishExecution struct {
	EndedAt           int64
	TerminationReason TerminationReason
	Status            EntryStatus
	// StartedAt fills entries.started_at when the close is what learns it: a
	// close whose open was lost carries the start the renderer measured. A
	// row that already knows its start keeps it — the close never overwrites
	// a fact the ledger already held.
	StartedAt *int64
	// DurationMs is the renderer's measured duration. Nil leaves whatever the
	// row already carries: a close with no measurement erases nothing.
	DurationMs *int64
	// Payload is the entry's kind payload (design §3.3) — for a shell entry
	// the exit code. Nil leaves the column untouched, which is what a close
	// on a kind with no terminal payload wants.
	Payload *string
}

// ShellPayload is the `shell` arm of the kind payload (design §3.3), the
// durable form of the facts that are shell-specific and therefore NOT
// top-level entry columns — hoisting them would make every other kind carry
// nulls.
//
// The arm carries exactly one field today. `trusted` and `markers` are in
// the design's version of this type and are deliberately absent: ADR-0024
// deleted the `trusted` boolean, its laundering rule and the anonymous
// marker cycle it was derived from, so neither has a source in the renderer
// any more. A field the wire could only fill with a guess is worse than one
// that is not there.
type ShellPayload struct {
	Kind     EntryKind `json:"kind"`
	V        int       `json:"v"`
	ExitCode *int      `json:"exitCode"`
}

// ShellPayloadJSON renders the shell arm for storage. The error json.Marshal
// declares cannot happen for three primitive fields, and a branch no test
// could reach is worse than none.
func ShellPayloadJSON(exitCode *int) string {
	b, _ := json.Marshal(ShellPayload{Kind: EntryShell, V: 1, ExitCode: exitCode})
	return string(b)
}

// ShellExitCodeOf reads the exit code back out of an entry payload — the
// counterpart of ShellPayloadJSON, and the ONE reader of that key, so a
// recall read never re-derives an exit code from anything else.
//
// nil means the row has no exit code, which is a real state and not a zero:
// a command that is still running has none, an interrupted one has none, and
// a non-shell entry never had one. The arm is only read when the payload
// says it is a shell arm, because the key belongs to that arm — the payload
// is a shared object whose other keys have their own owners (the redaction
// receipt in redaction.go).
func ShellExitCodeOf(payload string) (*int, error) {
	obj, err := decodePayloadObject(payload)
	if err != nil {
		return nil, err
	}
	raw, ok := obj["kind"]
	if !ok {
		return nil, nil
	}
	var kind EntryKind
	if err := json.Unmarshal(raw, &kind); err != nil {
		return nil, fmt.Errorf("content: entry payload kind is not a string: %w", err)
	}
	if kind != EntryShell {
		return nil, nil
	}
	code, ok := obj["exitCode"]
	if !ok {
		return nil, nil
	}
	var out *int
	if err := json.Unmarshal(code, &out); err != nil {
		return nil, fmt.Errorf("content: entry payload exitCode is not a number: %w", err)
	}
	return out, nil
}

// Grant is the authority recorded on a run (ADR-0020 §5): versioned,
// expiring, immutable once execution starts. It names BOTH dimensions of
// the authority — the effect classes permitted (what the run may do to the
// world, decision 6) and the resource scopes it may touch (what exists to
// be touched, decision 5). A grant over effect classes alone permits
// nothing in particular and everything in general: "may observe" reaches
// every path, session and credential unless the scopes say otherwise.
//
// Policy is the decision MATRIX of the amended §7 (policy.go): one row per
// effect class. Effects and Scopes are the matrix's derivations, materialized
// by EffectPolicy.AsGrant when the run's grant is minted — the matrix is the
// one source of what a run may do, and a grant built any other way is a
// hand-rolled authority the consumer cannot have reasoned about.
type Grant struct {
	Version   int
	ExpiresAt int64
	Policy    EffectPolicy
	Effects   []Effect
	Scopes    []GrantScope
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

// GrantScope is one resource the grant touches — what "this run held a grant
// for these environments and touched these three sessions" is a query over.
// The json tags are the wire form of a policy row's scope (the settings RPC);
// the durable record persists kind and id as columns, not JSON.
type GrantScope struct {
	Kind ResourceKind `json:"kind"`
	ID   string       `json:"id"`
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
	ErrNoSuchEntry          = errors.New("content: no such entry")
	// ErrArtifactTooLarge is the ceiling on ONE artifact, and it is input
	// validation rather than the user's retention preference: the per-command
	// cap decides how much of an output is worth keeping, this decides what a
	// caller may make the store hold whatever any setting says.
	ErrArtifactTooLarge = errors.New("content: artifact exceeds the per-artifact ceiling")
)

// MaxArtifactBytes is that ceiling. Four times the per-command cap's default,
// so raising the setting to its own maximum does not walk into it by
// accident.
const MaxArtifactBytes = 1 << 20

// CaptureOutput is one body of a frozen block arriving from the renderer
// (nocx-2f0f, design §4). It is the only write path for what a shell command
// printed, and it is deliberately not AppendArtifact followed by AppendChunk
// at the caller: the two have to land in one transaction, and the execution
// the artifact hangs on is resolved HERE — the renderer knows the entry it
// recorded and has never seen an execution id, which is a backend integer.
//
// EVERY ID IS UNTRUSTED. The artifact id is client-minted, so a capture whose
// ack was lost is retried: the same id and seq is a replay that writes
// nothing, and the same id asking for a different artifact is ErrIDConflict.
type CaptureOutput struct {
	// EntryID is the row the body belongs to — what history.record answered
	// with.
	EntryID string
	// ArtifactID is client-minted UUIDv7 and the idempotency key.
	ArtifactID string
	// MediaType is application/vt for the SGR body a restore draws, or
	// text/plain for the derived body search and copy read.
	MediaType MediaType
	// DerivedFrom names the artifact this one was produced from: the plain
	// body names the SGR body, and nothing else in this path sets it.
	DerivedFrom *string
	// Truncated says the body is not the whole of what was printed — 'cap'
	// when the middle was dropped at the per-command limit.
	Truncated *Truncation

	CaptureMethod  CaptureMethod
	CaptureVersion int
	TerminalCols   *int
	TerminalRows   *int

	// Seq is the chunk's position, minted by the CALLER so a retry is a
	// no-op. It starts at 1, which is where AppendChunk's own numbering
	// starts.
	Seq  int
	Body []byte
}

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
	// Environment is the row EnvironmentID names, resolved by the SAME
	// statement that read the entry — never a lookup per row. It is what
	// lets a row say which host it ran on (Environment.Host()); nil when no
	// environment row carries the id, which is "unknown", never "local".
	Environment *Environment
	Cwd         string
	Kind        EntryKind
	Intent      string
	Phase       Phase
	Status      EntryStatus
	SubmittedAt int64
	// The terminal facts, and the column the two sparse readers live in.
	// They are on the SUMMARY rather than only on LedgerEntry because a page
	// of recall renders all four — the relative time, the duration, the exit
	// code and the redaction receipt — and a page that had to fetch them per
	// row would be the N+1 the environment join exists to prevent.
	StartedAt  *int64
	EndedAt    *int64
	DurationMs *int64
	// Payload is the entry's kind payload column, raw. Two readers own its
	// keys and neither is the store: ShellExitCodeOf for the shell arm and
	// EntryMaskingOf for the redaction receipt (nocx-rtg0.24). The store
	// hands the column over rather than decoding it, so there is exactly one
	// decoder per key and it is the one that already exists.
	Payload string
}

// Summary is the timeline row of a recall-shaped entry — a projection, never
// a second read. It exists so the entry a detail read returns and the rows a
// page returns reach the wire through ONE mapping: two mappers of one shape
// disagree the first time either grows a field.
func (e LedgerEntry) Summary() LedgerEntrySummary {
	return LedgerEntrySummary{
		ID: e.ID, IngestSeq: e.IngestSeq, EnvironmentID: e.EnvironmentID,
		Environment: e.Environment, Cwd: e.Cwd, Kind: e.Kind, Intent: e.Intent,
		Phase: e.Phase, Status: e.Status, SubmittedAt: e.SubmittedAt,
		StartedAt: e.StartedAt, EndedAt: e.EndedAt, DurationMs: e.DurationMs,
		Payload: e.Payload,
	}
}

// MaxLedgerPageLimit is the ceiling on one page of recall. An unbounded
// limit is a denial of service the renderer can trigger by accident — one
// mistyped page size and the store reads years of history into memory.
const MaxLedgerPageLimit = 200

// LedgerQuery is one recall query over the ledger (design §6.2). Scope is
// the ladder's rung (§10.6) and EnvironmentID/Cwd are its coordinates: the
// server answers from the rung it was asked for and never silently widens,
// because a ladder whose rung you cannot see is a filter. Kind and Status
// are the closed enums the CHECK constraints name — a value they do not name
// is a refused request, never an empty result set.
//
// The two bounds read DIFFERENT columns, deliberately:
//
//   - Before is the paging cursor and reads ingest_seq, the design's total
//     order (§6.3). The page holds entries strictly earlier in that order.
//     The interim command_history path pages on its rowid; the two are never
//     mixed, because a rowid is not this design's order.
//   - Since is a wall-clock floor and reads submitted_at, the store's own
//     stamp. It is a question about time, and seq cannot answer one. It is
//     deliberately not ended_at, which is NULL while a command runs and
//     would silently drop every running entry from a time window.
//
// Text is the recall overlay's search box, and it is deliberately the SAME
// predicate the interim history path has answered since nocx-ms7v: a
// case-insensitive substring over the recorded intent, applied WITHIN the
// rung. Not a pattern and not full-text — a needle containing % or _ matches
// those characters literally, because the box is a search box and never a
// grammar the user has to learn. Empty is no filter, which is also what an
// absent one means on the wire. Full-text search is a different question with
// a different owner (ledger.search, nocx-rtg0.21); this field exists so the
// cutover off command_history is like for like rather than a feature.
type LedgerQuery struct {
	Scope         Scope
	EnvironmentID string
	Cwd           string
	Kind          EntryKind
	Status        EntryStatus
	// PaneID narrows the page to one pane's blocks — the read design §8's
	// restore is made of. It is a FILTER and not a rung: the rungs are the
	// recall ladder the user climbs (everywhere / host / directory) and a
	// pane is not a step on it, so this composes with whichever rung was
	// asked for instead of becoming a fourth one. Empty is no filter.
	PaneID string
	// BeforeID is the cursor expressed as the previous page's last entry id,
	// which is the only handle history.query has ever put on the wire
	// (nocx-rtg0.19). The store resolves it to that row's ingest_seq inside
	// the same read transaction and pages before it.
	//
	// NOTHING IS ORDERED BY IT. The order is ingest_seq and only ingest_seq —
	// a UUIDv7 sorts by the moment a CLIENT minted it, which is not the
	// moment the backend accepted it, so ordering by one would silently
	// reshuffle a user's history. This is a position resolved through a row,
	// never a comparison.
	//
	// An id naming no row is REFUSED rather than answered with the newest
	// page: a caller paging with a handle the store has evicted must learn
	// that instead of quietly starting again from the top.
	BeforeID string
	Text     string
	Since    *int64
	Before   *int64
	Limit    int
}

// LedgerPage is one page of recall, newest first, plus the three facts that
// keep it honest.
type LedgerPage struct {
	// Entries is the page. Never nil: no matches is an empty slice.
	Entries []LedgerEntrySummary
	// Exhausted is true when no further entries exist beyond this page.
	Exhausted bool
	// HasRows reports whether the ledger holds any entry at all, read in the
	// same transaction as the page. It is what separates "the store answered
	// and had nothing" from "the store has nothing to answer from": an empty
	// answer and an unanswerable question must not look alike, and a reader
	// that cannot tell them apart renders "no history" for "history is off".
	HasRows bool
	// Coverage is the store-wide horizon in Unix milliseconds: how far back
	// this store can still speak for, independent of the rung and of every
	// filter, because retention is store-wide so the horizon is too. Nil
	// when there is no horizon to state.
	//
	// It has two sources and the honest one depends on eviction. Once this
	// store has evicted anything the number comes from the retention
	// watermark — the horizon CANNOT be computed from the rows that remain,
	// because the rows that carried it are the ones that were deleted
	// (§5.4). Until then it is the oldest retained entry's ended_at, which
	// is exact: the survivors are the whole store. Nil while nothing has
	// completed and nothing has been evicted.
	Coverage *int64
}

// LedgerEntry is the recall-shaped read: the entry with every execution and
// each execution's pinned observation, grant and artifacts.
type LedgerEntry struct {
	ID            string
	IngestSeq     int64
	Client        string
	Digest        string
	EnvironmentID string
	// Environment is the resolved environment row (see
	// LedgerEntrySummary.Environment): the entry's host, kind and profile,
	// read back in the same statement as the entry. Nil when the
	// environment row is gone.
	Environment *Environment
	// PaneID is the anchor the restore path reads; SessionID beside it is
	// provenance, and it is nil from the first Open after the backend that
	// wrote it exited (design §6.1).
	PaneID         *string
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
// write both. The header above keeps the by-hand list of which methods have
// a production caller and which do not.
type LedgerRepository interface {
	// CreateSession records a restore key under a workspace. The workspace
	// itself belongs to LayoutRepository (layout.go).
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
	// RecordCompleted writes one command that has already finished — the
	// intent, its single execution and its outcome in ONE transaction, with
	// the entry id minted by the backend. It is what history.record lands
	// through since nocx-rtg0.19 replaced command_history, and it exists
	// beside Submit rather than instead of it because the two answer
	// different questions: Submit opens a lifecycle the renderer will drive
	// to a close, this records one that is already over.
	RecordCompleted(ctx context.Context, in CompletedCommand) (string, error)
	// Submit accepts an intent as an open entry and returns the
	// backend-assigned ingest_seq. Two entries in the same millisecond
	// still get distinct, ordered sequences — wall time is not a key.
	// Idempotent for (id, client, digest): a replay returns the original
	// row; the same id with different content is ErrIDConflict.
	Submit(ctx context.Context, in SubmitEntry) (SubmitResult, error)
	// Entry is the recall read: the entry, its resolved environment (which
	// is how the row says what host it ran on), its executions, each
	// execution's pinned observation and grant, and its artifacts
	// (metadata only — no chunk bodies). Nil when no row carries id.
	Entry(ctx context.Context, id string) (*LedgerEntry, error)
	// ListEntries returns the limit newest entries, newest first, ordered
	// by ingest_seq — commit order, never by wall clock. Each row carries
	// its resolved environment, joined in the one statement: the page costs
	// a single query however many rows and however many hosts it spans.
	ListEntries(ctx context.Context, limit int) ([]LedgerEntrySummary, error)
	// QueryEntries is the recall read and the ONE ordering implementation
	// (design §6.2): one page of the rung the caller asked for, newest first
	// by ingest_seq, with the page's exhaustion, whether the ledger holds
	// any row at all, and the store-wide retention horizon — all read in one
	// transaction, so the page and the facts about it cannot disagree about
	// the store's state. Every row carries its resolved environment from the
	// same statement. A request the closed enums cannot express, or a limit
	// outside [1, MaxLedgerPageLimit], is refused rather than answered with
	// an empty page that reads as "nothing ever matched".
	QueryEntries(ctx context.Context, q LedgerQuery) (LedgerPage, error)
	// RewriteRedaction replaces the redaction segment at span in the
	// entry's stored intent with reference (a vault reference), removing the
	// segment from the entry's receipt (EntryMasking, in entries.payload).
	// The row is addressed by its client-minted UUIDv7 — the ledger's own
	// key, which is what makes this method exist rather than widening
	// CommandHistoryRepository's rowid-keyed one.
	//
	// Idempotent: a span that is not among the row's CURRENT redactions is a
	// no-op, so a retried save after a lost response cannot replace text at
	// stale offsets. Returns ErrNotFound when no entry carries id, and an
	// error when the span no longer fits the stored intent — the row changed
	// shape underneath the caller, and refusing is the only answer that is
	// not corruption.
	RewriteRedaction(ctx context.Context, entryID string, span Redaction, reference string) error
	// DeleteEntry removes an entry; edges referencing it and its
	// executions (and their artifacts, chunks and grant) cascade. A pin
	// protects against background eviction, not against this.
	DeleteEntry(ctx context.Context, id string) error
	// EvictEntries removes the oldest completed entries retention no longer
	// covers — oldest-first by ingest_seq, the ledger's only total order —
	// and records what it removed in the retention watermark, in ONE
	// transaction. An entry holding a pinned artifact is exempt; an entry
	// that has not ended is unfinished rather than old and is never a
	// candidate. Max bounds the pass, because eviction shares the writer
	// goroutine with every other mutation.
	//
	// The deletion and the watermark commit together or not at all: a
	// deletion without its watermark would silently narrow what the store
	// can answer while it went on claiming full coverage.
	EvictEntries(ctx context.Context, req EvictionRequest) (EvictionResult, error)
	// Watermark reports what this store has ever lost to eviction: the
	// running count and the horizon its knowledge is complete after. Both
	// are read from the watermark alone — that they are underivable from
	// the surviving rows is the reason it exists.
	Watermark(ctx context.Context) (RetentionWatermark, error)
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
	// CaptureOutput records one body of a frozen block: the artifact if it
	// is not there yet and the chunk at its seq, in one transaction against
	// the entry's own execution. Idempotent on (artifact id, seq).
	//
	// REFUSING TO STORE IS NOT AN ERROR, and the answer says which happened.
	// Output retention off, or an entry marked sensitive, returns
	// (false, nil): the block keeps its row and keeps no body, the same shape
	// RecordCompleted uses for history.enabled. An error there would surface
	// in front of somebody who turned the setting off deliberately, and a
	// bare nil would leave the caller sending the rest of a body nobody is
	// storing.
	CaptureOutput(ctx context.Context, in CaptureOutput) (bool, error)
	// AppendChunk appends one chunk to an artifact and maintains its
	// byte_len (logical content bytes — the retention budget's unit).
	AppendChunk(ctx context.Context, artifactID string, seq int, body []byte) error
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
