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
	// Backup writes a consistent, encrypted snapshot of the whole database
	// to destPath. The destination is created through the same keyed VFS as
	// the live database (ADR-0018 amendment — the plaintext-canary rule), so
	// a restore is: replace content.db with the snapshot and open with the
	// same key. This is the only supported way to copy the database: WAL
	// mode means the live store is content.db plus -wal plus -shm, and
	// copying the single file while running produces a torn backup.
	Backup(ctx context.Context, destPath string) error
	Close() error
	// Ledger returns the schema-v1 ledger repository (ADR-0019, ADR-0020,
	// design §5.2): entries, edges, executions, artifacts, environments
	// with their versioned observations, sessions and workspaces. The
	// ledger.* wire methods (nocx-rtg0.3) drive this surface's entry
	// lifecycle, and the ask transaction drives its agent half.
	// command_history remains the live history path until nocx-rtg0.19
	// removes it. Nothing may write both (ADR-0019 §4).
	Ledger() LedgerRepository
	// Layout returns the durable layout chain (nocx-isoph.1, design §3):
	// workspace → tab → pane, the three objects the backend owns and the
	// frontend asks it to create, move and destroy. It is the only writer of
	// those tables — the workspace included, which is why CreateWorkspace is
	// not on LedgerRepository: one table, one repository owner.
	Layout() LayoutRepository
	// APIRuns returns the durable API exchange repository. Its identity is
	// the collection path plus request relative path; the renderer's
	// session-only collection handle is never persisted.
	APIRuns() APIRunRepository
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
