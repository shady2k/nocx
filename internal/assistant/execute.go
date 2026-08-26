package assistant

// The execution layer: one function per executable tool, each running the
// tool against ITS narrowed capability (design §6.6 — the only step that
// differs, and it differs by exactly the declaration row). The middleware
// sequences and enforces; this layer performs. An executor never re-checks
// the grant — it cannot: it holds only the capability, which is already
// scoped to the grant (ADR-0028 decision 4).
//
// The window contract (design §4.4): every tool that returns text returns a
// window — total, an explicit window, and a statement of which window was
// actually returned — so one files.read on a large log cannot consume the
// context the run needs. The window is the tool's own return contract
// (contracts/tools/files.read.schema.json states it), not a parameter.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/filesystem"
)

// filesReadWindowBytes is the window the files.read tool returns: the first
// this-many bytes of the file. It is a context budget, not a file limit —
// the window statement tells the model how much more the file holds.
const filesReadWindowBytes = 64 << 10

// executors maps tool name to the function that runs it against its narrowed
// capability. One entry per executable tool. The middleware consults it only
// after the declaration's Narrow produced a capability; a tool that executes
// InGo must have an entry here, enforced by TestExecutorsCoverTheRegistry
// (a new row with a Narrow but no executor is a registration that cannot
// run).
var executors = map[string]func(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error){
	"files.read":   executeFilesRead,
	"session.list": executeSessionListTool,
}

// toolSeams is the per-RUN infrastructure an executor may need and the
// capability must never hold: the capability is authority (ADR-0028 decision
// 4 — the dispatcher narrows, it does not check), while the session ledger is
// wiring, exactly as the renderer requester is for InRenderer tools.
type toolSeams struct {
	sessions SessionSource
}

func executeSessionListTool(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	reader, ok := cap.(*agenttools.SessionReader)
	if !ok {
		return "", fmt.Errorf("session.list: capability is %T, not *agenttools.SessionReader", cap)
	}
	return executeSessionList(ctx, reader, seams.sessions, args)
}

// filesReadResult is the tool's return: total (the file's size), the window
// that was ACTUALLY returned (which clamps to the file — a window past the
// end is answered honestly, never as an error), and the text. Binary content
// is reported as data, not pasted: Binary=true and no text.
type filesReadResult struct {
	Path     string          `json:"path"`
	Total    int64           `json:"total"`
	Window   filesReadWindow `json:"window"`
	Returned int64           `json:"returned"`
	Binary   bool            `json:"binary,omitempty"`
	Text     string          `json:"text,omitempty"`
}

type filesReadWindow struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// executeFilesRead runs the files.read tool: read the named path through the
// scoped capability (the grant's paths), return the window. The capability
// refuses an out-of-scope path structurally; the policy already refused or
// escalated it at the gate, and this refusal is the backstop that holds even
// if the policy is bypassed.
func executeFilesRead(ctx context.Context, cap agenttools.Capability, args json.RawMessage, _ toolSeams) (string, error) {
	scoped, ok := cap.(*filesystem.ScopedReader)
	if !ok {
		return "", fmt.Errorf("files.read: capability is %T, not *filesystem.ScopedReader", cap)
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		// Unreachable through the middleware (validation precedes policy,
		// let alone execution); the direct-call seam still answers honestly.
		return "", fmt.Errorf("files.read: args: %w", err)
	}
	c, err := scoped.Read(ctx, p.Path, filesReadWindowBytes)
	if err != nil {
		return "", err
	}
	out := filesReadResult{
		Path:  c.Path,
		Total: c.Total,
		Window: filesReadWindow{
			Start: 0,
			End:   c.Size,
		},
		Returned: c.Size,
		Binary:   c.Binary,
		Text:     c.Text,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("files.read: marshal result: %w", err)
	}
	return string(b), nil
}

// ── the frame vocabulary (design §4.1: a tool reads the screen through the
//    renderer, because the renderer owns the grid — AD-6) ──────────────────
//
// These types outlived the readScreen tool that introduced them. `session.read`
// took that tool's job (nocx-2ryxf.1) and `run` returns the same window shape,
// so the window, the cursor, the capture identity and the frame decode are
// shared here rather than restated per tool. The names still say `readScreen`
// because the frame wire vocabulary they mirror does; renaming them is a
// separate, mechanical change.

type readScreenWindow struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type readScreenCursor struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}

// readScreenIdent is the capture identity the frame carried (buffer
// instance, geometry, generation) — the same facts the push path records,
// consumed here in the minimal shape this tool's return needs.
type readScreenIdent struct {
	Buffer struct {
		Kind string `json:"kind"`
	} `json:"buffer"`
	Cols       int `json:"cols"`
	Rows       int `json:"rows"`
	Generation int `json:"generation"`
}

// frameBodyWire is this tool's consumer view of the validated frame body the
// requester returned (rows, cursor, identity). The frame wire vocabulary is
// owned by the transport's captureFrame validation; this decode reads the
// fields the window contract needs and never re-validates them.
type frameBodyWire struct {
	Rows []struct {
		Kind  string `json:"kind"`
		Cells []struct {
			Char string `json:"char"`
		} `json:"cells"`
		Text string `json:"text"`
	} `json:"rows"`
	Cursor *struct {
		Line int `json:"line"`
		Col  int `json:"col"`
	} `json:"cursor"`
	Identity *struct {
		Buffer struct {
			Kind string `json:"kind"`
		} `json:"buffer"`
		Cols       int `json:"cols"`
		Rows       int `json:"rows"`
		Generation int `json:"generation"`
	} `json:"identity"`
	Range *struct {
		Start int `json:"start"`
		End   int `json:"end"`
	} `json:"range"`
}

// ── run (design §4.1: the agent runs a command through the same submit
//    path a person uses, executed by the renderer — the backend never
//    writes to the PTY, design §2.1) ──────────────────────────────────────

// runResult is the run tool's return contract (design §4.4 — every tool
// that returns text returns a window): the entry id the command was
// accepted under, the exit status of the completed block (null when it
// froze without one — an entered environment), total (the block's output
// line count), the window that was asked for (run asks for the whole
// output — [0, total)), the window that was actually returned (the
// renderer clamps to what it can carry; a longer output is answered
// honestly, never as an error), and the text of the returned window.
type runResult struct {
	SessionID string           `json:"sessionId"`
	EntryID   string           `json:"entryId"`
	ExitCode  *int             `json:"exitCode"`
	Status    string           `json:"status"`
	Total     int              `json:"total"`
	Window    readScreenWindow `json:"window"`
	Returned  readScreenWindow `json:"returned"`
	Text      string           `json:"text"`
}

// runBodyWire is this tool's consumer view of the resolved run body the
// requester returned: the entry id, the exit status, the block's frozen
// status vocabulary (success | failure | entered | unknown), the output's
// total line count, the span of the window actually returned and its text.
// The wire vocabulary is owned by the transport's run kind validation; this
// decode reads the fields the window contract needs and never re-validates
// them.
type runBodyWire struct {
	EntryID  string `json:"entryId"`
	ExitCode *int   `json:"exitCode"`
	Status   string `json:"status"`
	Total    int    `json:"total"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Text     string `json:"text"`
}

// executeRun runs the run tool: the narrowed session capability (the
// grant's sessions) gates the call, and the renderer submits the command
// through the ordinary path and resolves with the completed block's facts.
// The capability check happens BEFORE the request — naming a session
// outside the grant is refused here and no broker request ever leaves
// (criterion 4, asserted by trying).
//
// caused is called with the entry id the RESOLUTION carried, once, as soon
// as it is known (nocx-h1l4o). It exists so the join between the command and
// the turn that ran it is made by the backend, from the id the renderer
// already answers with — the renderer sends no arrangement of its own, the
// same rule ledger.open states for paneId. It is a parameter rather than a
// second decode of this function's marshalled return, because the entry id
// is decoded exactly once, here, where the wire shape is owned. Nil for a
// caller that is not recording causes.
func executeRun(ctx context.Context, runner *agenttools.Runner, requester RendererRequester, args json.RawMessage, caused func(entryID string)) (string, error) {
	var p struct {
		SessionID string `json:"sessionId"`
		Command   string `json:"command"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		// Unreachable through the middleware (validation precedes policy,
		// let alone execution); the direct-call seam still answers honestly.
		return "", fmt.Errorf("run: args: %w", err)
	}
	if p.Command == "" {
		return "", errors.New("run: an empty command is a bare newline, not an execution")
	}
	if !runner.Allows(p.SessionID) {
		return "", fmt.Errorf("run: session %q is outside the run's grant — the request never reached the renderer", p.SessionID)
	}
	if requester == nil {
		return "", errors.New("run: no renderer requester is wired for this run")
	}
	body, err := requester.RequestRun(ctx, p.SessionID, p.Command)
	if err != nil {
		return "", err
	}
	var b runBodyWire
	if decodeErr := json.Unmarshal(body, &b); decodeErr != nil {
		return "", fmt.Errorf("run: resolved body: %w", decodeErr)
	}
	// The command exists now and is joined now — before the window is
	// checked, because a window this function refuses is a corrupt
	// resolution about a command that really ran, and a block with no place
	// in its turn is exactly what this closes.
	//
	// AN EMPTY ENTRY ID IS A REAL ANSWER: the store wrote no row for this
	// command (History is off, or the record was dropped), so there is
	// nothing to name and nothing to join. It used to be refused here, which
	// read the store's honest "no row" as a corrupt resolution and failed a
	// command that had already run — over a relation that is an arrangement
	// (nocx-9sqii). The id the renderer answers with is the STORE's, which
	// is the only id this join can be written against: the ledger's foreign
	// key refuses anything else.
	if caused != nil && b.EntryID != "" {
		caused(b.EntryID)
	}

	// The window: run asks for the whole output — [0, total) — and the
	// renderer states the span it actually returned (it clamps a long
	// output rather than erroring, and the window says how much more the
	// block holds). The wire span is authoritative: a zero-length window
	// (an empty block) is a legitimate answer, not an absent one, so
	// returned is never invented when the span is empty — only a span that
	// contradicts the block (outside [0, total]) is refused as a corrupt
	// resolution.
	total := b.Total
	asked := readScreenWindow{Start: 0, End: total}
	if total < 0 || b.Start < 0 || b.End < b.Start || b.End > total {
		return "", fmt.Errorf("run: the renderer's window [%d,%d) is outside the block's [0,%d)", b.Start, b.End, total)
	}
	returned := readScreenWindow{Start: b.Start, End: b.End}

	out := runResult{
		SessionID: p.SessionID,
		EntryID:   b.EntryID,
		ExitCode:  b.ExitCode,
		Status:    b.Status,
		Total:     total,
		Window:    asked,
		Returned:  returned,
		Text:      b.Text,
	}
	res, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("run: marshal result: %w", err)
	}
	return string(res), nil
}
