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
// SubmitAgentAsk, TransitionRun, OpenProse, SealProse, AppendChunk and
// FinishAgentRun. The READ
// path is wired as of nocx-rtg0.20: ledger.query drives QueryEntries and
// ledger.get drives Entry plus Edges plus Caused (ws_ledger_query.go), and the query's
// `host` field is what finally asks a resolved environment row for its host —
// so Environment.Host has a renderer. history.record drives RecordCompleted
// (nocx-rtg0.19), which is where a finished command lands now, under the
// author the renderer minted (nocx-iadtt); ledger.capture drives
// CaptureOutput.
//
// WHAT IS STILL TEST-REACHABLE ONLY: CreateSession, DeleteSession,
// ListEntries, DeleteEntry, AppendArtifact, AddEdge and RunState.
//
// AddCause and Caused were never on that list: they arrived wired
// (nocx-h1l4o). internal/assistant's policy middleware reaches AddCause for
// every entry a turn causes (policyMiddleware.noteCause) and ledger.get
// reaches Caused. Since ADR-0040 both work the tree — AddCause seats a child,
// Caused reads a block's children in pos order — rather than the retired
// `caused-by` edge. Both were checked with `deadcode -tags gtk3 -whylive`,
// which is the only form that answers this question — see the note below
// about -filter.
//
// EvictEntries, EvictBodies and Watermark are the third category, and it is
// written down rather than rounded to either of the other two: no production
// caller reaches the INTERFACE METHOD, and the behaviour behind it is
// nonetheless live on every submit — evictOnWrite calls the unexported
// evictEntries and evictBodies directly (retention.go), and the query path
// reads the unexported watermark inside its own transaction. So "no caller"
// here means the seam is untested in production, not that retention is
// asleep.
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
	"strings"
)

// ── closed enums; each mirrors a CHECK constraint in schemaV1 ─────────────

type EntryKind string

const (
	EntryShell  EntryKind = "shell"
	EntryAsk    EntryKind = "ask"
	EntryAction EntryKind = "action"
	// EntryText is one run of assistant prose (ADR-0040): a thing that was
	// PRINTED, not attempted. It has no intent, no execution and no outcome
	// to wait for, so the schema's CHECK pins its shape — inside a block
	// (parent and pos), empty intent, born closed and successful. Everything
	// drawn in the scrollback is an entry, and this is the kind that makes
	// that true of what the model writes between its calls.
	EntryText EntryKind = "text"
	// EntryFrame is a captured frame (design §2.2): a row the ledger owns —
	// it has no output and no outcome (a capture is a fact, complete at
	// ingest), but it is a row, and giving it a kind is what lets the ask's
	// reference check tell a frame from a turn by the discriminated column
	// rather than by comparing intent against a magic string. It is never
	// drawn as a block of its own — a frame is a reference into an ask.
	EntryFrame EntryKind = "frame"
)

// Source is the IMMEDIATE subject that submitted the content or the intent
// this entry represents (schemaV1's column, in the brief's words, verbatim
// for the boundary cases): initiation is NOT transitive — the assistant was
// set going by a person, and the command the assistant ran was submitted by
// the assistant, so if initiation chained every row would be `user` and the
// column would say nothing. Approval does not change it: a call the
// assistant proposed stays `assistant` after a person allows it, because the
// person authorised somebody else's intent, they did not submit it.
type Source string

const (
	SourceUser      Source = "user"
	SourceAssistant Source = "assistant"
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

// `caused-by` was here and is retired (ADR-0040): containment is
// entries.parent_id now, which is one parent the database enforces rather
// than however many rows anybody inserted. What is left is what is genuinely
// not a tree.
const (
	RelRerunOf    Relation = "rerun-of"
	RelSupersedes Relation = "supersedes"
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
	PaneID    *string
	SessionID *string
	// ParentID and Pos place the entry IN THE TREE (ADR-0040): the block it
	// sits inside and where among that block's children it sits. Both nil is
	// a top-level block, whose order stays ingest_seq.
	//
	// They travel together — a parent with no seat cannot be drawn and a seat
	// with no parent is not a place — and the pair is the caller's, because
	// only the caller knows where in what it is writing this belongs. A
	// second child at a position already taken is refused by the database,
	// never silently reordered.
	ParentID *string
	Pos      *int
	Cwd      string
	Kind     EntryKind
	// Source is the IMMEDIATE subject that submitted the content or the
	// intent this entry represents (schemaV1's column, in the brief's own
	// words): initiation is NOT transitive — the assistant was set going by
	// a person, and the command the assistant ran was submitted by the
	// assistant, so if initiation chained every row would be `user` and the
	// column would say nothing. Approval does not change it: a call the
	// assistant proposed stays `assistant` after a person allows it,
	// because the person authorised somebody else's intent, they did not
	// submit it. Empty defaults to SourceUser at the writer, never here —
	// the stores that cannot mean it refuse it (RecordCompleted) or the
	// caller names it (Submit's assistant callers).
	Source      Source
	Intent      string
	StartedAt   *int64 // frontend monotonic clock — durations only
	EndedAt     *int64
	DurationMs  *int64
	Sensitivity Sensitivity
	Payload     string // kind payload JSON (sparse extension only)
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
//
// EndedAt closes the turn's interval and is what its duration is measured
// against: unlike a shell command, nobody but this process times a turn, so
// the close is the only place the number can come from (nocx-hoeq3).
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

// FrameIntent is the intent value a frame row carries (kind=frame): the
// capture is a fact, complete at ingest, closed at capture time. The ask's
// reference check recognises a frame by its KIND — the discriminated column,
// never this string (the magic-string comparison is what the kind exists to
// retire).
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
	Cwd string
	// Source is the CAPTURE PATH (design §2.2): live cells versus
	// serialized text. Subject is WHO asked for the capture — the
	// immediate subject, in the entries.source vocabulary — and the two
	// are deliberately different fields: a live frame is a person's
	// capture today and can equally be the readScreen tool's pull
	// tomorrow, so the path must never be read as authorship (the
	// brief's transitivity trap in miniature).
	Source  FrameSource
	Subject Source
	Rows    []FrameRow
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
// frame references, the TURN and a PENDING RUN with the body it will stream
// into, in ONE atomic create, before the model would be called — the
// identities and the recovery state exist first, or a frame lands with no
// question, a question with no run, a run with nowhere to write, or a retry
// duplicates both.
type AgentAsk struct {
	// ID is the renderer-minted ask id — the UNTRUSTED idempotency key of
	// the question entry (the schema's own "client-minted UUIDv7" rule):
	// bound to Client and a digest of the ask content, so a replay returns
	// the original run id and the same id with different content is
	// ErrIDConflict.
	ID     string
	Client string
	Env    Environment
	// PaneID is the turn's DURABLE anchor, and it is the same one a command
	// carries (design §6.1, nocx-4em1z): a turn IS a block, so it hangs on
	// the thing that outlives the backend. SessionID beside it is
	// provenance — which pipe the question was asked in — and is null from
	// the first Open after that backend exited. Anchoring a turn to the
	// session alone is what lost every dialogue on restore: the read is by
	// pane, and by then there is no session to match.
	//
	// The TRANSPORT fills it from the live session, exactly as ledger.open
	// does, and the renderer never sends one: the backend already resolved
	// which pane a session is the pipe of, and a second copy on the wire
	// would be one input under two owners.
	PaneID     *string
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
// execution row — what agent.cancel/approve/status will address), the TURN's
// entry id, the artifact the answer is written into, the entry's ingest_seq
// and whether this was a replay. The run is in state prepared: recorded,
// never executed.
//
// ONE entry id and not two (nocx-4em1z). A turn is a block: the question is
// the entry's intent, and its BODY IS ITS CHILDREN (ADR-0040, amending
// ADR-0039). The answer used to be an entry of its own joined by a caused-by
// edge (assistant design §5) and nothing needed it to be — its id was a
// routing ADDRESS for deltas, reasoning, tool calls and copy, and the
// turn's own id addresses all four.
//
// NO ANSWER ARTIFACT IS OPENED HERE, and the absence is the change. The ask
// used to open one text/plain artifact on the run and every delta appended to
// it, which is precisely the arrangement ADR-0040 retires: the unit that is
// DRAWN (a run of prose) and the unit that was STORED (the whole answer) were
// different things, and something had to translate between them. The run
// opens a `text` child per run of prose instead (OpenProse), so the turn
// itself now carries no body at all.
type AgentAskResult struct {
	RunID int64
	// EntryID is the turn: what the deltas are routed by, what the flow
	// renders as a block, and what a restore reads back. It is the PARENT the
	// prose blocks are opened under, never the thing prose appends to.
	EntryID   string
	IngestSeq int64
	Replayed  bool
}

// ProseBlock is one run of assistant prose as the store holds it (ADR-0040):
// the `text` child of the turn, and the artifact that child's deltas append
// to. Two ids because they are two rows doing two jobs — the block is what is
// DRAWN and what the wire addresses, the artifact is the body it grows.
type ProseBlock struct {
	// EntryID is the `text` entry: a child of the turn, at its own seat,
	// born closed and successful because prose was printed, not attempted.
	EntryID string
	// ArtifactID is that block's body, open until the block is sealed.
	ArtifactID string
}

// ProseFacts is the part of a TEXT entry's payload the ledger reads back:
// WHICH RUN printed this piece of prose (ADR-0040's "the conversation is
// assembled from the children, in pos order, per run").
//
// It is a payload key rather than a column because it is what ONE kind has —
// the distinction ADR-0040 drew when it rejected an attributes table: "a
// column is what every kind has and the database must check, and payload is
// what one kind has". And it is deliberately not artifacts.execution_id: that
// column says which ATTEMPT produced a body, and a run of prose was printed
// rather than attempted — the store writes it NULL for prose and a test
// asserts it (TestAnAskIsOneEntryWhoseBodyIsItsProseChildren).
//
// What it buys is the retry case and only the retry case. A turn with one
// agent run needs nothing recorded: every `text` child of that turn is that
// run's prose, because there is no other run it could belong to. A turn with
// SEVERAL agent-lane executions cannot be read that way — sorting its
// children by pos alone splices two attempts into one incoherent message —
// and this is the fact that separates them.
type ProseFacts struct {
	// RunID is the agent execution that opened this block. Zero — an absent
	// key — means the block records no run, which is what a `text` row
	// written by anything other than OpenProse looks like.
	RunID int64 `json:"runId"`
}

// ProseFactsOf decodes a TEXT entry's payload. It is the one reader of that
// key, beside ShellExitCodeOf and EntryMaskingOf: one decoder per key, and it
// is the one that already exists.
//
// A payload that is not JSON, or carries no runId, is NOT an error: it is a
// block that records no run, and saying so is the honest answer. The error
// return is kept for the malformed-JSON case a caller may want to log.
func ProseFactsOf(payload string) (ProseFacts, error) {
	var f ProseFacts
	if strings.TrimSpace(payload) == "" {
		return f, nil
	}
	if err := json.Unmarshal([]byte(payload), &f); err != nil {
		return ProseFacts{}, fmt.Errorf("content: decode prose facts: %w", err)
	}
	return f, nil
}

// TurnProse is one turn's answer as a reader must be told it: the prose of
// ONE run, in seat order, or the honest statement that it is gone.
//
// WHICH RUN, stated on the value rather than left to the caller to work out
// (the brief's trap 2: "be explicit about which run's prose the assembled
// message is"). It is the turn's LATEST agent-lane execution — the attempt
// whose text is the one a person just read, and therefore the one a follow-up
// question is asked about. An earlier attempt's prose is not in Text and is
// not counted in Blocks; it is not a hole either, because an attempt that was
// superseded is not part of the answer that stands.
//
// Today a turn has exactly one agent run: SubmitAgentAsk writes attempt 1 and
// the approval resume drives that SAME execution to completion (the resume is
// a real checkpoint resume — internal/transport askRunContext.nextSeq). So
// the selection is a no-op on every path the product has, and it exists
// because `executions` permits a second agent-lane row per entry by design
// (ADR-0020 decision 4) and a reader that ignored the possibility would
// interleave two attempts the day one arrived.
type TurnProse struct {
	// RunID and Attempt are the execution this prose is of — the answer to
	// "which run", carried so a caller never has to re-derive it.
	RunID   int64
	Attempt int
	// State is that run's own state, and it is what decides whether Text is
	// a finished answer or an unfinished attempt (the brief's trap 3). The
	// presence of `text` children cannot answer that: a run interrupted
	// halfway leaves exactly the same rows as one that finished.
	State RunState
	// Blocks is how many runs of prose Text was joined from — zero when the
	// run printed nothing, and zero when retention took what it printed.
	Blocks int
	// Text is those blocks' bodies, in pos order, concatenated. The order is
	// the whole meaning: a sentence written before a call explains why the
	// call was made, and a sentence written after it is a conclusion drawn
	// from its output.
	Text string
	// Evicted says retention took the bodies of this run's prose (ADR-0040's
	// retention rule: the prose of one run is evicted as a unit). It is read
	// off the SAME receipt LedgerEntry.ProseEvicted reads — the sweep's mark
	// on the body — narrowed to this run's blocks, so there is one stored
	// fact and one reading of it.
	//
	// It is the reason Text being empty is not ambiguous. Evicted with empty
	// Text is "there was an answer and it is no longer kept"; not evicted
	// with empty Text is "this run printed nothing". A reader that could not
	// tell them apart would have to either invent text or leave a hole, and
	// both are worse than the sentence that says which it is.
	Evicted bool
}

// PriorTurn is the turn that came before another one in the same pane: the
// question that was asked and the answer that stands to it.
//
// The pane is the thread. A turn is anchored to a pane and not to a session
// (nocx-4em1z: a session is gone by the time a restore runs), so "what did we
// just say" is a pane-scoped question, and it is the same scope the restore
// reads and the block tools list.
type PriorTurn struct {
	// EntryID is that turn's block — the id a caller can go on to read.
	EntryID string
	// Question is its intent: what was asked, exactly as it was recorded.
	Question string
	// Prose is what the run answered, already arranged (see TurnProse).
	Prose TurnProse
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

// AppendArtifact creates one artifact of a BLOCK, with its capture
// provenance (ADR-0019 §6). Content arrives via AppendChunk; an artifact is
// never one BLOB.
type AppendArtifact struct {
	// EntryID is the OWNER: the block this is a body of (ADR-0040). Required.
	EntryID string
	// ExecutionID is PROVENANCE: which attempt produced this body. Nil when
	// there was no attempt — a `text` block was printed, not run.
	ExecutionID    *int64
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
	// interprets it. It carried a `caused-by` edge's causal position until
	// ADR-0040 made containment a column, and no relation left here has a
	// field the store reads.
	Payload string
}

// ActionFacts is the part of an ACTION entry's payload the ledger reads
// back: what the call was, what the gate classed it as, and what it named.
//
// The row is written by internal/assistant (policy.go openAttempt and
// recordProposal), whose payload carries more than this — the run id, the
// approval binding, the classifier's verdict — none of which a restored turn
// draws. This declares the part with a READER, so the contract between the
// two sides is a type rather than three string literals in two packages.
type ActionFacts struct {
	Tool   string `json:"tool"`
	Effect Effect `json:"effect"`
	// Args is what the model asked for, as the tool's schema validated it —
	// written with the attempt and read back here so a RESTORED call can be
	// named the same way the live one was.
	//
	// It has a reader now, and that is the change (ADR-0040). Two calls of
	// one session-scoped tool have the same tool name and the same derived
	// resource; what separates them is the arguments, so a restore without
	// them would say strictly less than the live announcement did — the
	// defect this ADR was written against, arriving one restart later.
	Args map[string]any `json:"args,omitempty"`
	// OpensBlock is the tool declaration's own fact
	// (agenttools.Declaration.OpensBlock), written with the attempt: the
	// call's work became a BLOCK of its own, so that block — its command, its
	// output, its exit status — is the account of the call, and a second child
	// beside it would restate the same command twice (ADR-0040).
	//
	// Stored rather than matched on Tool by the reader, for the reason
	// Effect is stored: a reader holding its own list of which tools open
	// blocks is a second copy of the tool table.
	OpensBlock bool `json:"opensBlock,omitempty"`
	// Resource is what the call named, derived ONCE at the moment the gate
	// decided about the call and stored with it. Absent when the tool names
	// no resource in its parameters at all.
	Resource *GrantScope `json:"resource,omitempty"`
}

// CausedEntry is one CHILD of a block, resolved: its seat and the facts a
// reader needs to draw it, off its own row (ADR-0040).
//
// It used to be a `caused-by` edge's other end and it is a child row now.
// What it carried and no longer does is `at` — how far the turn's prose had
// got when this happened — because that offset existed only while the unit
// that is DRAWN (a run of prose) and the unit that is STORED (one whole
// answer) were different things. They are the same thing now: prose is a
// `text` child with a seat of its own, so the sequence is the children in
// pos order and there is nothing left to cut.
//
// The JOIN happens HERE, in the ledger, and that is the point (AD-8). A
// reader handed raw ids would have to resolve each one, order the result and
// decide what a dangling one means — a second owner of the arrangement, in
// the surface that has the least idea what it means. The ledger owns the
// arrangement; a reader draws what it is told.
type CausedEntry struct {
	// EntryID is the child — a run of prose the turn wrote, a shell command
	// it ran, or the action entry of a tool call it made.
	EntryID string
	// Position is the child's seat among its siblings (entries.pos).
	Position int
	Kind     EntryKind
	// Source is who submitted the child's content or intent — the same
	// entries.source fact a page row carries, so a restored turn's badge
	// never guesses it from the child's kind.
	Source Source
	// Intent is the child row's own intent: the command line for a shell
	// entry, the tool name for an action, and EMPTY for a `text` child —
	// prose has no intent, which is a clause of its CHECK rather than a
	// convention a reader has to know.
	Intent string
	// Effect is the effect class the gate decided for an ACTION entry, read
	// back off that row's payload. Empty on every other kind — a command a
	// turn ran is not a tool call and has no effect class.
	Effect Effect
	// Args is what the call asked for, off the same row's payload
	// (ActionFacts.Args). Nil on every other kind — a command a turn ran
	// asked for nothing — and nil for an action whose row predates the
	// field, which reads as a call named by its tool alone rather than as an
	// error.
	Args map[string]any
	// Resource is what the call named, as the backend derived it at the
	// moment it decided about the call (internal/assistant namedResource is
	// the ONE derivation). Nil when the tool names no resource in its
	// parameters at all, and nil for a non-action entry. It is STORED
	// rather than re-derived because re-deriving it in a reader would be a
	// second answer to a question that already has an owner.
	Resource *GrantScope
	// OpensBlock says this call's work became a block of its own
	// (ActionFacts.OpensBlock). False on every non-action kind: a command a
	// turn ran IS a block and does not also say it opened one.
	OpensBlock bool
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
	// Source is who submitted the content or intent this entry represents
	// (entries.source) — the fact the restore badge is painted from.
	Source Source
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
		Source:  e.Source,
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
	PaneID    *string
	SessionID *string
	// ParentID and Pos are the entry's place in the tree (ADR-0040): the
	// block it sits inside and its seat among that block's children. Both nil
	// on a top-level block.
	ParentID    *string
	Pos         *int
	Cwd         string
	Kind        EntryKind
	Intent      string
	Phase       Phase
	Status      EntryStatus
	SubmittedAt int64
	StartedAt   *int64
	EndedAt     *int64
	DurationMs  *int64
	// Source is who submitted the content or intent this entry represents
	// (entries.source) — carried on the detail read for the same reason it
	// is on the summary: the restore badge is painted from it.
	Source      Source
	Sensitivity Sensitivity
	Payload     string
	// Artifacts are the entry's OWN bodies — the ones no execution produced
	// (ADR-0040 decision 3: an artifact belongs to its block, and which
	// attempt made it is a second, weaker fact that a `text` block does not
	// have). A run of prose is exactly that case, and without this it could
	// be written and never enumerated: the per-execution lists below reach a
	// body through its attempt, and there is no attempt to reach through.
	//
	// It is deliberately NOT every artifact of the entry. A command's output
	// hangs on the attempt that produced it and is listed there; repeating it
	// here would be one body in two lists, which is two owners of one fact
	// the moment either list is filtered. Metadata only, like the others —
	// the recall read never hauls bytes.
	Artifacts []Artifact
	// ProseEvicted says the prose of THIS RUN is no longer kept: retention
	// took the bodies of its `text` children (ADR-0040's retention rule,
	// ADR-0019 §7). It is the ONE place a reader asks that question, and it
	// is a fact about the RUN because the run is the unit — the prose of one
	// run is retained or evicted together, so a turn cut into seven pieces
	// and a turn written in one report the same single answer, and a reader
	// drawing the turn has one sentence to say rather than one per hole.
	//
	// Derived, never stored: it is EXISTS over the receipts the sweep leaves
	// on the bodies themselves (Artifact.Evicted), so there is one stored
	// fact and one reading of it. False on every kind that has no prose,
	// which includes a command whose own terminal body was evicted — that
	// block says its own sentence, and a turn does not say it for it.
	ProseEvicted bool
	Executions   []Execution
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
	ID string
	// EntryID is the block this body belongs to; ExecutionID is which
	// attempt produced it, nil when there was none (ADR-0040).
	EntryID        string
	ExecutionID    *int64
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
	// Evicted says the store HAD this body and retention took it, as
	// distinct from a capture that never held anything: both are zero bytes
	// over zero chunks, and "this command printed nothing" and "this output
	// is no longer kept" are different sentences that must stay different.
	// The row survives the eviction precisely to carry this — §7 evicts
	// bodies and leaves everything that says what was there.
	Evicted bool
	Payload string
	Chunks  [][]byte
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
	// EvictBodies is the SIZE-driven sweep: it frees the oldest bodies the
	// retention budget no longer covers and leaves every entry, every
	// artifact row and every chunk of accounting behind it (ADR-0019 §7).
	// Oldest-first by the owning block's ingest_seq, the same total order
	// EvictEntries walks, and a pinned body is exempt for the same reason.
	//
	// IT EVICTS UNITS, NOT ARTIFACTS. The prose of one assistant run is one
	// unit (ADR-0040): a pass that takes one `text` body of a run takes all
	// of them, and a pin on any piece exempts the whole run. Everything else
	// is its own unit, so a command's terminal body still evicts
	// independently of the prose around it. Max bounds the pass in bodies
	// and is measured BETWEEN units — a pass overruns it to finish a run
	// rather than tear one in half.
	//
	// The retention watermark does not move. It answers what ROWS this store
	// has lost and how far back it can speak for them, and a stripped body
	// leaves every row in place; the loss is reported where a reader meets
	// it, on the block itself (Artifact.Evicted, LedgerEntry.ProseEvicted).
	EvictBodies(ctx context.Context, req BodyEvictionRequest) (BodyEvictionResult, error)
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
	// AppendArtifact creates one artifact of a BLOCK (never a BLOB: content
	// arrives chunked). The entry owns it; the execution, when there was
	// one, is the provenance of which attempt produced it.
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
	// OpenProse opens ONE run of assistant prose under a turn (ADR-0040): a
	// `text` child at the turn's next free seat, with an artifact of its own
	// for the deltas that follow. Both rows land in one transaction — a block
	// with nowhere to put its text is not a block anyone can draw.
	//
	// THE SEAT IS THE STORE'S, taken as MAX(pos)+1 under the parent inside
	// that transaction, exactly as AddCause takes it: two writers of one
	// column, one rule for what free means, so a command a turn ran and a run
	// of prose it wrote can never claim the same place.
	//
	// The BOUNDARY is the caller's, and that is the whole point of the method
	// existing: the backend decides where one run of prose ends and the next
	// begins (the first delta after a call opens one, the next call seals
	// it), so the renderer never has to. ErrNoSuchEntry when nothing carries
	// turnID — a body seated under a parent that is not there is the one
	// answer worse than an error.
	//
	// runID is the agent-lane execution that is printing this prose, and it
	// is recorded on the block (ProseFacts). The store REFUSES a run that is
	// not an agent-lane execution of turnID: prose attributed to another
	// turn's run is worse than prose attributed to none, because it would be
	// assembled into that turn's message.
	OpenProse(ctx context.Context, turnID string, runID int64) (ProseBlock, error)
	// SealProse seals one prose block's body: nothing may be appended to it
	// again. Called when the boundary arrives — the tool call that ends this
	// run of prose — and by the run's terminal close for whatever was still
	// open. Idempotent, and a block that carries no body is a no-op rather
	// than an error, because the caller's fact is "this block is finished",
	// which is true either way.
	SealProse(ctx context.Context, entryID string) error
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
	// FinishAgentRun closes the run AND its turn in ONE transaction — the
	// terminal state this slice's driver persists: the run's state, end and
	// termination reason, the turn's entry and the interval it took, and
	// every prose block it wrote (sealed).
	// A run is never reported terminal in the run vocabulary while its
	// entry still says otherwise — both lifecycles close together, or
	// neither does.
	FinishAgentRun(ctx context.Context, runID int64, in FinishAgentRun) error
	// AddEdge records one relation between two entries.
	AddEdge(ctx context.Context, e Edge) error
	// Edges returns every edge touching entryID, in either direction.
	Edges(ctx context.Context, entryID string) ([]Edge, error)
	// AddCause seats an EXISTING entry as the next child of turnID and
	// returns the seat it took (nocx-h1l4o, ADR-0039's closing sentence, as
	// amended by ADR-0040). It is the write path for a block the turn caused
	// but did not create — a command a `run` call opened, the action entry
	// of a tool call — whose row was submitted before anyone knew where in
	// the turn it belonged. An entry that knows its place when it is written
	// carries it on Submit instead (SubmitEntry.ParentID/Pos), which is what
	// a `text` block does.
	//
	// THE SEAT IS THE STORE'S, and the caller may not supply one. It is read
	// and written inside one transaction, so two children recorded at once
	// cannot take the same index and a process that restarted mid-turn
	// continues from what is stored rather than from zero — an in-memory
	// counter would restart on every approval resume, which re-runs the
	// pipeline over a turn that already has children.
	//
	// IDEMPOTENT ON THE PAIR: seating a child that is already under this
	// parent returns its ORIGINAL position and moves nothing. The approval
	// resume passes the same call through the pipeline a second time, and a
	// counter that advanced on the replay would move the resumed call to
	// after everything that followed it.
	//
	// ONE PARENT, which is the whole reason this is a column and not an
	// edge: an entry already seated under a DIFFERENT block is refused, not
	// re-parented, because moving a block out of the turn that drew it is
	// not something a second caller may do by accident. Either id naming an
	// entry that is not there is refused too, so nothing is left seated
	// under a parent the store does not hold.
	AddCause(ctx context.Context, turnID, causedID string) (int, error)
	// Caused returns entryID's CHILDREN in pos order — everything drawn
	// inside that block, prose included — each resolved into what a reader
	// draws it with. It read `caused-by` edges until ADR-0040 made
	// containment a column; the order it returns is the order on screen, and
	// the sequence is the whole meaning (a sentence before a command is why
	// the command was run). Empty — never an error — for a block with no
	// children and for an id no row carries: "what is inside this" has an
	// honest answer either way.
	Caused(ctx context.Context, entryID string) ([]CausedEntry, error)
	// PriorTurn returns the agent turn that precedes beforeEntryID in paneID
	// — the question that was asked and the prose of the run that answered
	// it, already arranged (PriorTurn, TurnProse). Nil, and no error, when
	// nothing precedes it: "there is no earlier turn in this pane" is an
	// honest answer, and the caller's next question ("so send no history")
	// is answered by it.
	//
	// THE JOIN IS HERE, and that is the point (AD-8). It is three questions —
	// which turn came before this one, which of its runs is the one that
	// stands, and what that run printed in what order — and each of them has
	// exactly one right answer. A caller that stitched them from a children
	// read would be a second owner of the arrangement, in the surface with
	// the least idea what it means, which is the defect ADR-0040 exists to
	// remove.
	//
	// beforeEntryID names the turn to look BEFORE, and it is resolved to that
	// row's ingest_seq inside the read — the same rule LedgerQuery.BeforeID
	// states, and for the same reason: a UUIDv7 sorts by the moment a client
	// minted it, which is not the moment the backend accepted it. An id no
	// row carries is refused with ErrNoSuchEntry rather than answered with
	// the newest turn in the pane, which would be a different turn's answer
	// presented as this one's context.
	//
	// An empty paneID answers nil: a session that is the pipe of no recorded
	// pane has no thread to read, and inventing one out of every pane's turns
	// would put another tab's conversation into this one.
	PriorTurn(ctx context.Context, paneID, beforeEntryID string) (*PriorTurn, error)
}
