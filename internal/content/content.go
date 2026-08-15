// Package content declares the ContentDB capability and the typed repository
// seams for AI agent conversations and command history.
//
// # SQLite implementation conditions
//
// The real implementation is deferred until the first feature needs it (agent-mode
// epic nocx-dw3 or command history nocx-4ff.6, whichever lands first). When it
// arrives, these rules apply:
//
//   - One database: content.db (not one per entity).
//   - WAL journal mode, because surviving a force-quit is the whole reason a
//     desktop application takes a database at all.
//   - foreign_keys=ON at connection open.
//   - Short transactions through a single controlled write path; no long-lived
//     or concurrent write transactions.
//   - Honesty constraint: ordinary DELETE leaves data in WAL pages, freelists
//     and FTS shadow tables. The UI says "removed from nocx", not "securely
//     erased", unless and until checkpointing and vacuum are implemented
//     deliberately.
//
// No generic Repository[T] — each entity declares its own typed repository
// interface (ADR-0011 §1).
package content

import (
	"context"
	"errors"
)

// ErrNotImplemented is the sentinel error returned by every Stub method.
var ErrNotImplemented = errors.New("content stub: not implemented")

// ErrClosed is returned by operations on a ContentDB that has been Closed.
var ErrClosed = errors.New("content: store is closed")

// ErrNotFound is returned when an operation addresses a row id no row
// carries — most often a history entry the retention sweep removed between
// the record and a later rewrite.
var ErrNotFound = errors.New("content: no such history row")

// ErrIDConflict is returned when a client-minted entry id is submitted a
// second time with different content. The id is an idempotency key bound to
// the client identity and a payload digest, so a replay that would alias a
// different intent is refused rather than silently stored twice.
var ErrIDConflict = errors.New("content: entry id already used by a different submission")

// ContentDB is the capability for unbounded, query-oriented private content
// (ADR-0011 §5). It owns a single SQLite database and exposes typed repository
// interfaces for each entity class.
type ContentDB interface {
	Conversations() ConversationRepository
	CommandHistory() CommandHistoryRepository
	// Backup writes a consistent, encrypted snapshot of the whole database
	// to destPath. The destination is created through the same keyed VFS as
	// the live database (ADR-0018 amendment — the plaintext-canary rule), so
	// a restore is: replace content.db with the snapshot and open with the
	// same key. This is the only supported way to copy the database: WAL
	// mode means the live store is content.db plus -wal plus -shm, and
	// copying the single file while running produces a torn backup.
	Backup(ctx context.Context, destPath string) error
	Close() error
	// RestorePrivate applies the given conversations and command history to
	// the store in ONE atomic operation: either every item is durable or
	// none is. It is the write-side counterpart of the portable export's
	// private content block (ADR-0011 §7) and the ONLY supported way to
	// restore such a block — a caller that loops Save/Add instead builds a
	// partial restore that cannot be unwound.
	//
	// The store owns the atomicity; the caller must not sequence the
	// writes. A store that cannot hold conversations (the SQLite backing
	// stubs them until agent mode, design §5.1) reports ErrNotImplemented
	// when conversations are non-empty, exactly as its
	// ConversationRepository does — the failure is honest, never a silent
	// drop. History rows keep their timestamps; row ids are assigned by
	// the store.
	RestorePrivate(ctx context.Context, conversations []Conversation, history []CommandRecord) error
	// Ledger returns the schema-v1 ledger repository (ADR-0019, ADR-0020,
	// design §5.2): entries, edges, executions, artifacts, environments
	// with their versioned observations, sessions and workspaces. The
	// ledger.* wire methods (nocx-rtg0.3) will drive this surface; until
	// that cutover its only callers are tests, and command_history remains
	// the live history path. Nothing may write both (ADR-0019 §4).
	Ledger() LedgerRepository
}

// CommandStatus is the execution status of a command. It mirrors the closed
// set in frontend/src/command-ledger.ts:10.
type CommandStatus string

const (
	StatusRunning     CommandStatus = "running"
	StatusSuccess     CommandStatus = "success"
	StatusFailure     CommandStatus = "failure"
	StatusInterrupted CommandStatus = "interrupted"
	StatusUnknown     CommandStatus = "unknown"
)

// CommandRecord mirrors frontend/src/command-ledger.ts:12-25.
// Nullable TS fields (exitCode, startedAt, endedAt) use pointers:
// nil means "not set," matching TypeScript null without a sentinel value.
// Output bytes are never retained here (ADR-0008); that is nocx-de7's job.
type CommandRecord struct {
	ID        int64
	Command   string
	Cwd       string
	Host      string
	Status    CommandStatus
	ExitCode  *int
	StartedAt *int64
	EndedAt   *int64
	Trusted   bool
	// MaskedCount and MaskedKinds record what was redacted from Command
	// before this row was written (secrets.Mask). A row with no secrets has
	// 0 and nil — the facts describe the durable text, and the durable text
	// is always the masked one. The kinds are the closed vocabulary of
	// internal/secrets, deduplicated in first-occurrence order.
	MaskedCount int
	MaskedKinds []string
	// Redactions are the structured segments the mask left on Command: one
	// per finding, kind and byte span into Command plus the head/tail the
	// mask shows (no secret material — prefix/suffix are exactly the text
	// already visible in the masked command). A row whose redaction was
	// saved to a vault reference has the segment replaced by the reference
	// and dropped from this list; a row with no secrets has nil. The
	// renderer draws an unresolved chip at each segment and refuses to run
	// the command as written.
	Redactions []Redaction
}

// Redaction is one structured redaction segment on a history row. Offsets
// are BYTE offsets into the row's stored (masked) Command — the store
// slices bytes; the transport converts them to the UTF-16 code-unit
// positions the renderer decorates with, once, at the wire. The segment
// never carries secret material: Prefix/Suffix are the head-4/tail-4 the
// mask shows, exactly what is already visible in the masked command.
type Redaction struct {
	Kind   string `json:"kind"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

// Scope is the recall-ladder rung a history query is answered from (design
// §10.6). The server answers from the rung it was asked for and never
// silently widens: a ladder whose rung you cannot see is a filter.
type Scope string

const (
	ScopeDirectory  Scope = "directory"  // the exact working directory
	ScopeHost       Scope = "host"       // the exact host; "" is the local machine
	ScopeEverywhere Scope = "everywhere" // no rung filter
)

// HistoryPage is one page of command history, newest first.
type HistoryPage struct {
	// Entries is the page. Never nil: no matches is an empty slice
	// (contracts/history.query.schema.json: "Never null: no matches is []").
	Entries []CommandRecord
	// Exhausted is true when no further entries exist beyond this page.
	Exhausted bool
	// HasRows reports whether the store holds any rows at all, read in the
	// same transaction as the page. The transport uses it to tell "the
	// store answered and had nothing" (source=store, entries=[]) from "the
	// store has nothing to answer from" (source=session): an empty answer
	// and an unanswerable question must not look alike.
	HasRows bool
	// Coverage is the store-wide horizon the answer can see: the oldest
	// retained entry's ended_at, in Unix milliseconds, regardless of the
	// rung or the text filter (retention is store-wide, so the horizon is
	// too). The overlay renders it so a search under retention does not
	// present a partial history as the whole one. Nil when the store holds
	// no completed rows — nothing to state a horizon for.
	Coverage *int64
}

// Conversation is a conversation with an AI agent.
type Conversation struct {
	ID        string
	Title     string
	CreatedAt int64
	Messages  []Message
}

// Message is a single message within a conversation.
type Message struct {
	Role      string
	Content   string
	Timestamp int64
}

// ConversationRepository is the typed repository for AI agent conversations.
type ConversationRepository interface {
	Save(ctx context.Context, conv Conversation) error
	GetByID(ctx context.Context, id string) (*Conversation, error)
	List(ctx context.Context, limit int) ([]Conversation, error)
}

// CommandHistoryRepository is the typed repository for command history.
type CommandHistoryRepository interface {
	// Add stores one completed command's facts and returns the backend
	// assigned row id — the row's stable identity, which a later
	// RewriteRedaction addresses. When the live History policy is off, Add
	// succeeds and returns (0, nil): a command runs and no row appears,
	// never an error the caller has to swallow.
	Add(ctx context.Context, record CommandRecord) (int64, error)
	List(ctx context.Context, limit int) ([]CommandRecord, error)
	GetByID(ctx context.Context, id int64) (*CommandRecord, error)
	FindByPrefix(ctx context.Context, prefix string, limit int) ([]CommandRecord, error)
	// RewriteRedaction replaces the redaction segment at span in the row's
	// stored command with reference (a vault reference), removing the
	// segment from the row's redactions. The row is addressed by its stable
	// id. Idempotent: a segment already holding the same reference is
	// replaced by the same reference again. Returns ErrNotFound when no row
	// carries id, and an error when the span no longer fits the stored
	// command (the row changed shape underneath — refuse rather than
	// corrupt).
	RewriteRedaction(ctx context.Context, id int64, span Redaction, reference string) error
	// Query returns one page of command history for the given recall-ladder
	// rung, newest first. cwd is required for ScopeDirectory, host for
	// ScopeHost (both ignored for ScopeEverywhere). before, when non-nil, is
	// the opaque row id (the string form of a CommandRecord.ID) the previous
	// page ended at; the page contains only rows strictly older than it.
	// limit must be >= 1. text is the search filter (nocx-ms7v): a
	// case-insensitive substring over command, applied WITHIN the rung the
	// caller asked for — the server never silently widens. Empty or absent
	// means no filter. The page's Coverage is store-wide and independent of
	// both the rung and the filter.
	Query(ctx context.Context, scope Scope, cwd, host string, limit int, before *int64, text string) (HistoryPage, error)
}
