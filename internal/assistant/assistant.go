// Package assistant owns the tool authority kernel used by agent harnesses.
package assistant

import (
	"errors"
	"fmt"
	"io/fs"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// Client owns process-lifetime tool state.
type Client interface{ Discard(runID string) }

// ToolCall announces a validated tool invocation without its result.
type ToolCall struct {
	// Tool is the declared tool name, e.g. "files.read".
	Tool string
	// CallID is the model's own id for this call. The renderer keys on it,
	// so a call that is announced twice renders once.
	CallID string
	// Args is what the model asked for, AS VALIDATED against the tool's own
	// schema — the object the call actually ran on, never the raw string the
	// model emitted. It is what NAMES the call on screen, and it is the same
	// object the attempt's payload already stores (openAttempt writes the raw
	// form of it), so announcing it publishes no fact the ledger did not
	// already record. Never nil: a tool that takes no arguments validates to
	// an empty object, and absent and empty are the same sentence about a
	// call.
	Args map[string]any
	// EntryID is the ledger action entry the attempt was recorded under
	// (openAttempt): the thread joining question, run, attempt and answer.
	EntryID string
	// Effect is the class the gate decided on — the ledger's vocabulary,
	// never derived from the tool name by the renderer (ADR-0028 decision
	// 4).
	Effect content.Effect
	// Resource is what the call named, from namedResource — the ONE
	// derivation of "which argument names the resource this call touches",
	// shared with the scope check and the approval ask. Nil when the tool
	// names no resource in its parameters (git.status's repository IS the
	// grant's path scope).
	Resource *content.GrantScope
	// OpensBlock is the declaration's own fact
	// (agenttools.Declaration.OpensBlock): this call's work becomes a
	// TOP-LEVEL BLOCK, so the block is the account of the call and the flow
	// draws no line beside it (nocx-9sqii). True for `run` alone today.
	//
	// It rides the announcement because the renderer has to decide, at the
	// moment the call arrives, whether to seal the answer fragment it is
	// writing and let the block take that position. Deriving it from Tool in
	// the renderer would be a second copy of the tool table — the reason
	// Effect rides here too (ADR-0028 decision 4).
	OpensBlock bool
}

// AskParams binds one turn's authority and tool seams.
type AskParams struct {
	// Grant is the run's authority (ADR-0020 decision 5) — the ledger's
	// type (content.Grant), the one grant model: effect classes the run may
	// produce, resource scopes it may touch, and the policy preset that
	// decides act/ask/refuse. The kernel checks this authority at tool
	// admission and execution; the harness owns the model's tool declarations.
	// Nil means no authority and BindRunTools refuses to bind the turn.
	Grant *content.Grant
	// AttemptLedger is the ledger seam the tool-call pipeline records its
	// attempts with (design §6.4 — the attempt is durable, before the call,
	// so an interrupted run can never be told "this may already have
	// happened" when it cannot). Nil means no tool may execute: the run
	// fails closed rather than acting without a record. The transport wires
	// the real ledger when it mints grants; today no caller does.
	AttemptLedger AttemptLedger
	// Requester is the seam for renderer-orchestrated command submission.
	// It is separate from ScreenCapture because a screen read is answered by
	// the owning runtime, not by asking a renderer to reconstruct it.
	Requester RendererRequester
	// ScreenCapture reads the run's own pane from its owning backend runtime.
	// Nil means an own-screen read fails closed; descendant reads still use
	// the helper-backed PaneReader carried by their capability.
	ScreenCapture ScreenCaptureSource
	// SessionSource reads the run's existing session items from the ledger.
	// Nil means item reads and session.list fail closed.
	SessionSource SessionSource
	// Expansions asks ONE live shell what the safe expansions in a proposed
	// command currently read as, so the approval question can show the
	// expanded form beside the verbatim one (nocx-4h0m7.5). The query never
	// carries anything that would execute — the classification is syntactic
	// and refuses `$(…)`, backticks, `<(…)`, `${VAR:=x}` and `${VAR:?msg}`
	// before the query is built.
	//
	// Nil is the honest shape wherever our integration is not deployed —
	// every remote host without it, and every session whose shell cannot be
	// reached: nothing is expanded, every expansion is marked NOT ASKED with
	// its reason, and the run is otherwise unaffected.
	Expansions ExpansionSource
	// Workers is the worker record this run may start workers in and ask about
	// (nocx-dkawo.8). Nil is the honest shape for a caller with no record —
	// the tools refuse and say so, rather than starting a worker into
	// something that would not hold it.
	Workers WorkerRecord
	// Environment is where the pane this run was asked in is — the identity
	// the transport derives from the session's own facts
	// (environmentForSession, the one owner of "where is this session") and
	// hands over with the question. The kernel puts it on the run's
	// agenttools.RunContext, where a tool that can only act on this machine
	// reads it and refuses a pane elsewhere (ADR-anntnxdj §5, decision 3).
	// The zero value names no pane, which those tools refuse rather than
	// assume this machine.
	Environment content.Environment
	// WorkerEnvironment names the environment a spawn would reach. The
	// composition root owns it, for the reason the tool's resolver is a
	// constant: the spawner opens a local session, so a value the model
	// supplied could name an environment nothing can deliver.
	WorkerEnvironment string
	// PaneAccessBinder mints this run's DescendantPaneAccess/SessionReads/
	// SessionKeys/SessionMessages (design §7.1, §7.3, §6.4-§6.5, §8, Tasks
	// 8-10) — the kernel's side of the same binding the tool endpoint's
	// Admit does. Nil is the honest shape for a build with no
	// composition-root wiring yet: every sessionId naming a descendant is
	// then refused rather than guessed at.
	PaneAccessBinder PaneAccessBinder
	// Scripts reads the whole of a file a proposed command NAMES, so an
	// approval question about `bash deploy.sh` carries deploy.sh itself
	// (nocx-872jc.3). It READS and never executes; which file to read comes
	// from the command parser's own resource report, never from a second
	// tokenizing of the command line.
	//
	// Nil is the honest shape wherever no provider can reach the machine the
	// command would run on: nothing is read, the question says so in words,
	// and the run is otherwise unaffected.
	Scripts ScriptSource
	// Cwd is where this run was asked from. It is the run's own cwd — the
	// one the question carried and the ledger recorded with it — and it is
	// carried for exactly one purpose: resolving the relative path in
	// `bash deploy.sh` before that file is read for the approval question.
	// Empty means a relative path cannot be resolved, and the question says
	// that rather than guessing a directory.
	Cwd string
	// NoteOperation and SnippetOperation are the existing guard-bound domain
	// operations used by the Notes and Snippets panels. The assistant carries
	// them as seams; it never owns a service or a second store implementation.
	NoteOperation    capability.NoteOperation
	SnippetOperation capability.SnippetOperation
	// Fetcher is the guarded direct-network seam for fetch.url. It is wired
	// by the composition root; the tool cannot select a pane's connection
	// route or construct another HTTP client.
	Fetcher apifetch.TextFetcher
	// Approvals is the process-lifetime approval store (design §7.2): the
	// human's yes to one exact proposal, bound to run, attempt, tool, call
	// id and a hash of the canonical arguments. Nil disables the store: an
	// ask is still put to the person, and the guards that read the answer
	// find nothing recorded.
	Approvals *ApprovalStore
	// Questioner puts a prepared question to a person and returns their
	// answer (ADR-anntnxdj §3, Questioner in kernel.go). The transport hands
	// its approval service here, beside the store, and the call then BLOCKS
	// on the answer: the run rests in awaiting_approval while it waits, and
	// the answer reaches this very call. Nil is a caller with no person to
	// ask, where every ask is refused.
	Questioner Questioner
	// FreshGrant must return promptly: it reads a local policy snapshot, not
	// external work or approval. Admission cannot interrupt this synchronous mint.
	// FreshGrant mints the run's authority again from the policy as it stands
	// NOW (ADR-anntnxdj §3, "refreshed after an answer"). The pipeline calls
	// it after a person answers, and the guards and the capability of that
	// call read what it returns. Nil is a caller with no mint: a call that
	// waited runs on the grant minted at admission, and an expired one is
	// refused with the sentence that says so.
	FreshGrant func() *content.Grant
	// KnownMaterial is the egress vault comparison (design §7.1): "the
	// vault knows the real values, and a comparison beats any pattern." It
	// is legitimate here precisely because it happens in the backend and
	// nothing leaves — ADR-0011 §2 survives intact. The transport adapts
	// the vault to this seam when it mints grants. A grant-carrying ask
	// whose tools may execute FAILS without it: the gate must see short
	// vault values, or a result would leave for the provider unscreened.
	KnownMaterial KnownMaterial
	// RunID and SessionID are the run's backend-owned identities. The
	// transport passes both from the ask context; SessionID is the pane the
	// run is executing in, not a value recovered from grant scopes.
	RunID     string
	SessionID string
	Attempt   int
	// TurnEntryID is the TURN's own ledger entry — the one entry a turn is
	// since ADR-0039, and what everything this run causes is joined to by a
	// `caused-by` edge (nocx-h1l4o).
	//
	// It is passed rather than looked up from RunID, even though the two
	// name the same thing from either end of `executions.entry_id`: the
	// transport holds both facts off ONE SubmitAgentAsk result, so a lookup
	// here would recover what the caller already had and could only ever
	// disagree with it.
	//
	// Empty is the un-bound shape, and it means what it says: the entries
	// this run causes belong to no turn and are recorded with no relation
	// rather than with a guessed one.
	TurnEntryID string
	// AutomaticSessionItems are renderer-owned frozen-screen item ids. They
	// are scoped to this run and let session.read resolve those ids through the
	// renderer even when no durable ledger row exists.
	AutomaticSessionItems []string
	// MarkedSessionWindows are the row spans the person marked and sent with
	// this question. They bound a session.read of those items (nocx-hp8p2.15).
	MarkedSessionWindows []MarkedSessionWindow
}

// MarkedSessionWindow is one person-marked row span: which item, and which
// rows of it the question is about.
type MarkedSessionWindow struct {
	ItemID string
	Start  int
	Count  int
}

// Attachments is what came WITH the question, as the run carries it: the
// renderer-owned frozen-screen ids, and the row spans the person marked.
// One parameter rather than two because they travel together everywhere and
// answer one question — what is this ask about (nocx-hp8p2.15).
type Attachments struct {
	AutomaticItems []string
	MarkedWindows  []MarkedSessionWindow
}

// NewClientAndRegistry constructs the tool owner and its canonical registry.
func NewClientAndRegistry(logger log.Logger, floor content.Floor) (Client, agenttools.Registry, error) {
	reg, err := assembleToolRegistry(tools.Schemas)
	if err != nil {
		return nil, agenttools.Registry{}, err
	}
	return newClientWithRegistry(logger, reg, floor), reg, nil
}

func assembleToolRegistry(toolsFS fs.FS) (agenttools.Registry, error) {
	reg, err := agenttools.Assemble(toolsFS)
	if err != nil {
		return agenttools.Registry{}, fmt.Errorf("assistant: tool registry: %w", err)
	}
	if len(reg.All()) == 0 {
		return agenttools.Registry{}, errors.New("assistant: tool registry assembled EMPTY — the tool schemas did not reach the binary; a model would be offered no tools")
	}
	return reg, nil
}

func newClientWithRegistry(logger log.Logger, reg agenttools.Registry, floor content.Floor) Client {
	return &client{log: logger, tools: reg, floor: floor, approvals: NewApprovalStore(), snapshots: newRunSnapshots()}
}

type client struct {
	log       log.Logger
	tools     agenttools.Registry
	floor     content.Floor
	approvals *ApprovalStore
	snapshots *runSnapshots
}

func (c *client) Discard(runID string) {
	if c.snapshots != nil {
		c.snapshots.Discard(runID)
	}
}
