package content

// The durable layout chain (nocx-isoph.1), design
// .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §3, §4.5, §5, §7:
//
//	workspace   flat, never nested — which tabs are one piece of work
//	  └─ tab    the strip entry, and what the user decorates
//	       └─ pane   THE DURABLE IDENTITY: it outlives its shell, its tab and
//	                 the application
//
// This file is the public seam: ContentDB.Layout() returns a
// LayoutRepository, and it is the only writer of the three tables. It owns
// `workspaces` outright — CreateWorkspace used to hang off LedgerRepository,
// and leaving it there while the tab arrived would have given one table two
// repository owners, which is the defect this whole design spends its length
// avoiding. The ledger still READS through the foreign key
// (sessions.workspace_id) and still ensures a fallback default row for a
// session nobody has recorded; nothing else writes these tables.
//
// THE WINDOW IS NOT THE STORE (nocx-l21ib.4). A tab or a pane that leaves the
// window is MARKED CLOSED and keeps its row; only a workspace is ever
// deleted, once its last open tab has gone. Every read here answers with the
// WINDOW SET — the rows whose closed_at is null — so a caller cannot forget
// the filter, and the closed rows are reachable only through a reader written
// for them. The reason is the block's anchor: entries.pane_id is ON DELETE
// SET NULL and panes.tab_id is ON DELETE CASCADE, so deleting one tab
// unhooked every block its panes had ever printed, and an ordinary Cmd-W was
// a permanent loss of that work. ClearWindow is the one bulk form of leaving,
// and it exists for exactly one caller — the composition root's clean start.
//
// WHAT IS STORED AND WHAT IS ONLY COMPUTED is the reason the field list looks
// short (§4.5). The activity indicator, the attention indicator and the label
// are computed from the tab's panes and have NO column: attention arrives at
// a PANE — a command failed, a worker asked a question — so a copy on the tab
// would give one fact two owners, and they diverge the first time a pane is
// dragged elsewhere. What is genuinely the tab's own is "I have seen this",
// which duplicates nothing.
//
// PRODUCTION CALLERS, and they are real ones since nocx-isoph.2: the
// layout.read, workspaces.*, tabs.* and panes.* JSON-RPC methods
// (internal/transport/ws_layout_handlers.go, through capability.LayoutService)
// and the open ack's derived workspaceId, which reaches WorkspaceForPane from
// the session plane. The container lifecycle those calls drive is
// nocx-isoph.3's: creation-with-content, the dissolution of a container with
// its last member, the move and the replacement tab.
//
// `deadcode -whylive
// github.com/shady2k/nocx/internal/content.sqliteContent.MovePane` prints a
// path from main() through the control frame, the layout method spec and the
// operation's callback — and so, since nocx-isoph.4, do Snapshot and
// DeletePane. That is the only form of the answer worth anything: RTA counts
// a method a live interface value can hold as reachable, so a `deadcode` run
// that says nothing about this package says nothing either way. That is
// exactly the shape that shipped once before under a green "deadcode is
// empty" (nocx-rtg0), so what is reachable is written down next to the seam
// and kept current by hand.
//
// DELETEPANE'S CALLER IS panes.close, and the delay in giving it one was a
// NAME rather than an omission (nocx-isoph.4). The word the renderer wanted,
// pane.close, was already the capture-scoping notification — a different act
// under the same word. Both were named here: this one is panes.close, in the
// plural layout family beside panes.create and panes.move and taking the same
// close params as tabs.close and workspaces.close; the notification became
// secrets.paneClosed, in the domain that already owns a pending capture. The
// renderer mints ONE UUIDv7 per pane and sends it to both, so a pane has one
// identity and not one per seam.
//
// Snapshot is the other thing nocx-isoph.2 left out, and the larger one: with
// twelve writes and no read, the renderer had to remember what it had asked
// for, and what it remembers it owns — which is the invariant §4.1 moved into
// this process precisely to give an owner. Its wire method is layout.read.

import (
	"context"

	"github.com/shady2k/nocx/internal/sandbox"
)

// TabLayout is the direction a tab arranges its panes in. Direction is a
// property of the SET, size a property of the member (§5) — which is why the
// tab needed a row of its own and the display group did not.
//
// Two values, and the cost is stated rather than hidden: no asymmetric
// layouts, ever, until §5's decision is revisited deliberately. Panes do not
// nest, so "B on the left, C and D stacked on the right" is not expressible
// and is not meant to be.
type TabLayout string

const (
	LayoutRow    TabLayout = "row"
	LayoutColumn TabLayout = "column"
)

// PaneKind is where a pane's pipe goes. Deliberately NOT EnvironmentKind,
// which carries `container` and `unknown` as well: those are honest answers
// about where a recorded command RAN, and a pane is a thing the user opens —
// §5 gives it exactly two, and a type that admitted four would make the
// schema's CHECK the only place the difference was written down.
type PaneKind string

const (
	PaneLocal PaneKind = "local"
	PaneSSH   PaneKind = "ssh"
)

// Workspace is which tabs are one piece of work. Flat, never nested (§3):
// there is no parent, and depth comes from lineage on the tab. It binds no
// host, owns no credentials and confers no authority.
type Workspace struct {
	// ID is client-minted UUIDv7 (§7) and therefore UNTRUSTED: the shape is
	// validated, never believed, and an insert on an existing id FAILS rather
	// than overwriting.
	ID string
	// Name is the user's. A workspace, unlike a tab, is always created
	// deliberately, so it always has one.
	Name string
	// Colour is the user's too, and it is what the strip reads sideways: a
	// name is read, a colour is recognised. One of the closed set in the
	// renderer's layout/tab-colours.ts — the same four every shipped theme
	// defines, so a workspace keeps its colour when the theme changes.
	//
	// NIL IS A REAL STATE AND IT IS THE DEFAULT WORKSPACE'S. That workspace
	// renders no chrome at all (§4.2) — no header, no name, no colour — so it
	// is never offered one and never stores one. It is also what a workspace
	// minted by the BACKEND has: the fallback row the ledger ensures for a
	// session nobody recorded was chosen by no user, so there is nobody whose
	// colour it could be.
	//
	// The store does not police the value. What is drawable is the renderer's
	// question and it already answers it for tabs — an unrecognised colour
	// draws as none rather than as a broken swatch — and a store that rejected
	// a colour a newer renderer understands would be the older half deciding
	// for the newer one.
	Colour *string
	// Position orders the switcher.
	Position int
}

// Tab is one slot in the strip and what the user decorates. It is the cheap
// wrapper: a tab is minted when a pane is dragged out and removed when its
// last pane leaves (§4.4), and the pane's identity, blocks, history and live
// pipe are untouched in both directions because only a reference moved.
type Tab struct {
	// ID is client-minted UUIDv7 (§7), UNTRUSTED, and never reused.
	ID string
	// WorkspaceID is never null: every tab is in a workspace, and there is
	// one owner of the default (nocx-fraus, moved here from the session by
	// §4.5 — the backend now owns the whole chain and resolves
	// pane → tab → workspace itself).
	WorkspaceID string
	// ParentID is the LINEAGE edge and nothing else (§4.2): who spawned whom,
	// provenance, immutable, never set by hand. It is admitted by
	// internal/lineage — the same rules a session's parent is admitted by —
	// and there is no method that changes it afterwards.
	//
	// The display grouping ("A, B and C are shown together") is the tab's
	// OTHER edge and must never become this column. It is symmetric, has no
	// host and therefore no row (§4.3), and it is set by dragging, which
	// arrives with nocx-8m2x6. Carrying both on one column is the failure
	// AGENTS.md names: the loser goes on advertising what it can no longer
	// deliver.
	ParentID *string
	// Name is nil when nobody named it, which is the normal case: a tab
	// created by a drag was never named by anybody, so demanding a name asks
	// for something the user did not give. The label is then derived from its
	// panes' titles — computed, never stored. A name the user DOES type is
	// stored here and wins.
	Name *string
	// Colour is nil when the tab was never decorated.
	Colour *string
	// Position orders the strip.
	Position int
	// Pinned keeps the tab at the head of the strip.
	Pinned bool
	// Layout is the direction this tab arranges its panes in.
	Layout TabLayout
	// SeenAt is the seen-mark: when the user last looked at this tab, in Unix
	// milliseconds, nil for a tab never seen. It is a TIMESTAMP rather than an
	// "unseen" flag on purpose — the flag is the computed indicator, and
	// storing it would be the very duplication §4.5 refuses. Whether an
	// unseen tab is still unseen after a restart is §12's second open
	// question, and a mark rather than a verdict leaves it open.
	SeenAt *int64
}

// Pane is the durable identity, and everything else about it follows from
// that: it outlives its shell, its tab and the application, and its blocks
// are found by its id after a restart. A pane and its session are two objects
// because D5 says so — the process dies with the backend and the pane does
// not.
type Pane struct {
	// ID is client-minted UUIDv7 (§7). It must survive a restart, so it
	// cannot come from a backend instance.
	ID string
	// TabID is the only edge a pane has. Panes do not nest (§5): there is no
	// parent pane, structurally, so asymmetric geometry is unrepresentable
	// rather than merely unused.
	TabID string
	// Cwd is where the pane's shell is, and what a restore reopens in.
	Cwd string
	// Kind decides restore behaviour, not a dialog (§8): a local pane starts
	// a fresh shell in the same cwd; an ssh pane attempts to reconnect.
	Kind PaneKind
	// Endpoint is the canonical user@host:port an ssh pane applies at; nil
	// for a local pane.
	Endpoint *string
	// SizeShare is this pane's share of its tab's extent. Size is a property
	// of the MEMBER, direction a property of the set (§5).
	SizeShare float64
}

// Replacement is the identity of the tab that appears when the last tab in
// the APPLICATION closes (nocx-isoph.3, §4.4 of the workspaces UX design). It
// is a parameter rather than something the backend invents for two reasons,
// and both are §7: a tab and a pane id are minted by the frontend, and they
// must survive a restart, so they cannot come from a backend instance.
//
// It is consulted ONLY when the close would otherwise leave no tab anywhere.
// Every other close ignores it, so a caller that always passes one is not
// asking for a tab it will not get.
type Replacement struct {
	// TabID and PaneID are client-minted UUIDv7, like every other id here,
	// and equally untrusted: an id already taken fails the whole close.
	TabID  string
	PaneID string
	// Cwd is where the replacement pane opens. Empty is legal and means the
	// backend process's own directory, which is what an unconfigured local
	// shell already inherits.
	Cwd string
}

// LayoutSnapshot is the whole chain in one answer (nocx-isoph.4): every
// workspace, every tab and every pane, each collection in its stored order.
//
// IT IS FLAT, and that is the same decision §4.3 makes about the display
// group: the edges are already on the rows — a tab names its workspace, a
// pane names its tab — so a nested tree would be a second encoding of what
// the rows already say, and the two would drift the first time one of them
// was built by hand. It also keeps the wire shape referencing the three
// object declarations rather than restating them.
//
// WHY A SNAPSHOT RATHER THAN THE THREE READERS. Workspaces, Tabs and Panes
// each answer one question and are kept; this composes them, so there is no
// second SQL statement asking anything they already ask. What it adds is that
// the composition happens on ONE side of the seam: a caller doing it itself
// would interleave its 1 + N + N·M calls with whatever else is writing, and
// draw a strip that never existed.
type LayoutSnapshot struct {
	// DefaultWorkspaceID is the workspace a tab belongs to until something
	// puts it somewhere else. It rides on the snapshot because the backend is
	// its single owner (AD-8): the default never renders and never acquires a
	// name, so a renderer cannot be expected to recognise it by sight, and a
	// copy of the constant on the other side of the wire would be a second
	// owner of the one id that exists to have exactly one.
	DefaultWorkspaceID string
	// Workspaces is every workspace in position order; Tabs is every tab in
	// the application grouped by workspace, each strip in its own order; Panes
	// is every pane grouped by tab. All three are non-nil, including when
	// empty — a fresh profile has no rows at all and that is an answer, not a
	// failure.
	Workspaces []Workspace
	Tabs       []Tab
	Panes      []Pane
}

// Created is what a create answers: the stored object, and whether this call
// found the work already done.
//
// Replayed is the visible half of §7's idempotency rule. A create whose
// answer was lost is retried — AD-9 exists because the socket drops — and the
// retry must return the FIRST object rather than mint a second one. Reporting
// which of the two happened is what lets a caller assert the property over
// the wire instead of inferring it from the absence of an error.
type Created[T any] struct {
	Object   T
	Replayed bool
}

// NewWorkspace is everything one creation-with-content made: there is no
// moment at which the workspace exists without the tab or the tab without the
// pane, so there is no answer that names only one of them either.
type NewWorkspace struct {
	Workspace Workspace
	FirstTab  Tab
	FirstPane Pane
}

// NewTab is the same one rung down.
type NewTab struct {
	Tab       Tab
	FirstPane Pane
}

// LayoutRepository is the typed repository for the layout chain (ADR-0011 §1:
// each entity declares its own typed repository, no generic Repository[T]).
//
// THE LIFECYCLE IS THE SIGNATURE, not a sweep (nocx-isoph.3, design §4.1 and
// §4.4). A workspace exists only while it holds at least one tab; a tab only
// while it holds at least one pane. Neither rule needs lifecycle code because
// neither empty state is reachable: creation is always creation-with-content
// — which is why CreateWorkspace takes a first tab and CreateTab a first pane
// — and the container's row goes in the SAME TRANSACTION that removes its
// last member. There is no reaper, no periodic check and no "empty" flag,
// because there is no interval in which an empty container exists to find.
//
// There is no method that changes a tab's parent, and that absence is load
// bearing: the lineage edge is verified at ADMISSION and never revisited,
// which is what makes it immutable rather than merely unwritten-again. It is
// also what makes a cycle unreachable through this seam — a parent must
// already exist, and a new id cannot already be an ancestor — so the walk
// internal/lineage runs at CreateTab is there for the depth bound and for the
// day a mover is added. MovePane moves a pane between TABS and is not that
// mover: it touches panes.tab_id and nothing on tabs.
type LayoutRepository interface {
	// WorkspaceProfileStore is implemented here so workspace payload has one writer.
	sandbox.WorkspaceProfileStore
	// CreateWorkspace records a workspace with its first tab and pane atomically.
	CreateWorkspace(ctx context.Context, ws Workspace, firstTab Tab, firstPane Pane) (Created[NewWorkspace], error)
	// Snapshot returns the whole workspace, tab and pane chain.
	Snapshot(ctx context.Context) (LayoutSnapshot, error)
	// Workspaces returns every workspace in position order.
	Workspaces(ctx context.Context) ([]Workspace, error)
	// RenameWorkspace updates the name of an existing workspace.
	RenameWorkspace(ctx context.Context, id, name string) (Workspace, error)
	// RecolourWorkspace sets or clears a workspace colour.
	RecolourWorkspace(ctx context.Context, id string, colour *string) (Workspace, error)
	// ReorderWorkspaces applies the user's order to all non-default workspaces.
	ReorderWorkspaces(ctx context.Context, ids []string) ([]Workspace, error)
	// DeleteWorkspace closes its open tabs and removes the workspace atomically.
	DeleteWorkspace(ctx context.Context, id string, next Replacement) error
	// CreateTab records a tab together with its first pane.
	CreateTab(ctx context.Context, tab Tab, firstPane Pane) (Created[NewTab], error)
	// CreateTabAfter creates a tab and seats it after an open sibling.
	CreateTabAfter(ctx context.Context, tab Tab, firstPane Pane, after string) (Created[NewTab], error)
	// Tabs returns one workspace's tabs in position order.
	Tabs(ctx context.Context, workspaceID string) ([]Tab, error)
	// RenameTab sets or clears a tab name.
	RenameTab(ctx context.Context, id string, name *string) (Tab, error)
	// RecolourTab sets or clears a tab colour.
	RecolourTab(ctx context.Context, id string, colour *string) (Tab, error)
	// PinTab controls whether a tab is kept at the head of its strip.
	PinTab(ctx context.Context, id string, pinned bool) (Tab, error)
	// ReorderTabs applies a complete order to one workspace's tabs.
	ReorderTabs(ctx context.Context, workspaceID string, ids []string) ([]Tab, error)
	// DeleteTab closes a tab and dissolves containers left empty.
	DeleteTab(ctx context.Context, id string, next Replacement) error
	// CreatePane adds a pane to an existing tab.
	CreatePane(ctx context.Context, pane Pane) (Created[Pane], error)
	// SetPaneCwd records a verified current directory for a pane.
	SetPaneCwd(ctx context.Context, paneID, cwd string) (Pane, error)
	// MovePane moves a pane to another tab, dissolving the old empty tab.
	MovePane(ctx context.Context, paneID, tabID string) (Pane, error)
	// PaneCwd returns the last verified cwd for a pane.
	PaneCwd(ctx context.Context, paneID string) (string, error)
	// TabForPane resolves the pane's current tab.
	TabForPane(ctx context.Context, paneID string) (string, error)
	// WorkspaceForPane resolves the pane's current workspace from the layout chain.
	WorkspaceForPane(ctx context.Context, paneID string) (string, error)
	// Panes returns one tab's panes in id order.
	Panes(ctx context.Context, tabID string) ([]Pane, error)
	// DeletePane closes a pane and dissolves containers left empty.
	DeletePane(ctx context.Context, id string, next Replacement) error
	// ClearWindow closes all open tabs and panes in one transaction.
	ClearWindow(ctx context.Context) error
}
