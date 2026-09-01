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
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/snippet"
)

type toolBoundContextKey struct{}

func withToolBound(ctx context.Context, bound agenttools.ResultBound) context.Context {
	return context.WithValue(ctx, toolBoundContextKey{}, bound)
}

func toolBound(ctx context.Context) (agenttools.ResultBound, error) {
	bound, ok := ctx.Value(toolBoundContextKey{}).(agenttools.ResultBound)
	if !ok || !bound.Valid() {
		return agenttools.ResultBound{}, errors.New("agent tool: missing result bound")
	}
	return bound, nil
}

// executors maps tool name to the function that runs it against its narrowed
// capability. One entry per executable tool. The middleware consults it only
// after the declaration's Narrow produced a capability; a tool that executes
// InGo must have an entry here, enforced by TestExecutorsCoverTheRegistry
// (a new row with a Narrow but no executor is a registration that cannot
// run).
var executors = map[string]func(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error){
	"files.read":       executeFilesRead,
	"files.edit":       executeFilesEdit,
	"files.create":     executeFilesCreate,
	"session.list":     executeSessionListTool,
	"notes.search":     executeNotesSearch,
	"notes.create":     executeNotesCreate,
	"notes.update":     executeNotesUpdate,
	"notes.delete":     executeNotesDelete,
	"snippets.list":    executeSnippetsList,
	"snippets.create":  executeSnippetsCreate,
	"snippets.update":  executeSnippetsUpdate,
	"snippets.delete":  executeSnippetsDelete,
	"snippets.reorder": executeSnippetsReorder,
	"skills.read":      executeSkillsRead,
}

// SkillSource is the assistant's seam onto the skill library. The index is
// what the prompt lists; Read is what the tool returns. The interface exists
// so the assistant depends on the abstraction and not on internal/skill.
type SkillSource interface {
	Index() []skill.Skill
	Read(name, relPath string) (skill.Content, error)
}

// toolSeams is the per-RUN infrastructure an executor may need and the
// capability must never hold: the capability is authority (ADR-0028 decision
// 4 — the dispatcher narrows, it does not check), while the session ledger is
// wiring, exactly as the renderer requester is for InRenderer tools.
type toolSeams struct {
	sessions         SessionSource
	noteOperation    capability.NoteOperation
	snippetOperation capability.SnippetOperation
	skills           SkillSource
}

type noteSearchRow struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Excerpt   string `json:"excerpt"`
	UpdatedAt int64  `json:"updatedAt"`
	Body      string `json:"body,omitempty"`
}

type notesSearchResult struct {
	Notes     []noteSearchRow `json:"notes"`
	Truncated bool            `json:"truncated"`
	Dropped   int             `json:"dropped"`
}

type noteMutationResult struct {
	Status string    `json:"status"`
	Note   note.Note `json:"note"`
}

type contentDeleteResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type snippetsListResult struct {
	Snippets  []snippet.Snippet `json:"snippets"`
	Truncated bool              `json:"truncated"`
	Dropped   int               `json:"dropped"`
}

type snippetMutationResult struct {
	Status  string          `json:"status"`
	Snippet snippet.Snippet `json:"snippet"`
}

type snippetsReorderResult struct {
	Snippets []snippet.Snippet `json:"snippets"`
}

func contentScope(cap agenttools.Capability, tool string) (*agenttools.ContentScope, error) {
	scope, ok := cap.(*agenttools.ContentScope)
	if !ok {
		return nil, fmt.Errorf("%s: capability is %T, not *agenttools.ContentScope", tool, cap)
	}
	return scope, nil
}

func requireContentRoot(scope *agenttools.ContentScope, tool string) error {
	if !scope.Allows("content") {
		return fmt.Errorf("%s: content library is outside the run's grant", tool)
	}
	return nil
}

func requireContentItem(scope *agenttools.ContentScope, tool, kind, id string) error {
	if !scope.Allows(kind + "/" + id) {
		return fmt.Errorf("%s: %s/%s is outside the run's grant", tool, kind, id)
	}
	return nil
}

func executeNotesSearch(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "notes.search")
	if err != nil {
		return "", err
	}
	var p struct {
		Query string `json:"query"`
		ID    string `json:"id"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("notes.search: args: %w", unmarshalErr)
	}
	if seams.noteOperation == nil {
		return "", errors.New("notes.search: notes operation is unavailable")
	}
	var result notesSearchResult
	err = seams.noteOperation.Run(ctx, func(callCtx context.Context, svc capability.NoteService) error {
		if p.ID != "" {
			if itemErr := requireContentItem(scope, "notes.search", "note", p.ID); itemErr != nil {
				return itemErr
			}
			n, getErr := svc.Get(callCtx, p.ID)
			if getErr != nil {
				return getErr
			}
			result.Notes = []noteSearchRow{{ID: n.ID, Title: n.Title, Body: n.Body, UpdatedAt: n.UpdatedAt}}
			return nil
		}
		if rootErr := requireContentRoot(scope, "notes.search"); rootErr != nil {
			return rootErr
		}
		rows, searchErr := svc.Search(callCtx, p.Query)
		if searchErr != nil {
			return searchErr
		}
		for _, row := range rows {
			if scope.Allows("note/" + row.ID) {
				result.Notes = append(result.Notes, noteSearchRow{
					ID: row.ID, Title: row.Title, Excerpt: row.Excerpt, UpdatedAt: row.UpdatedAt,
				})
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	return marshalBoundedNotes(result.Notes, bound.MaxBytes)
}

func executeNotesCreate(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "notes.create")
	if err != nil {
		return "", err
	}
	var p struct {
		Body string `json:"body"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("notes.create: args: %w", unmarshalErr)
	}
	if rootErr := requireContentRoot(scope, "notes.create"); rootErr != nil {
		return "", rootErr
	}
	if seams.noteOperation == nil {
		return "", errors.New("notes.create: notes operation is unavailable")
	}
	var out noteMutationResult
	err = seams.noteOperation.Run(ctx, func(callCtx context.Context, svc capability.NoteService) error {
		n, createErr := svc.Create(callCtx, p.Body)
		out = noteMutationResult{Status: "created", Note: n}
		return createErr
	})
	if err != nil {
		return "", err
	}
	return marshalResult(out)
}

func executeNotesUpdate(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "notes.update")
	if err != nil {
		return "", err
	}
	var p struct {
		ID   string `json:"id"`
		Body string `json:"body"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("notes.update: args: %w", unmarshalErr)
	}
	if itemErr := requireContentItem(scope, "notes.update", "note", p.ID); itemErr != nil {
		return "", itemErr
	}
	if seams.noteOperation == nil {
		return "", errors.New("notes.update: notes operation is unavailable")
	}
	var out noteMutationResult
	err = seams.noteOperation.Run(ctx, func(callCtx context.Context, svc capability.NoteService) error {
		n, updateErr := svc.Update(callCtx, p.ID, p.Body)
		out = noteMutationResult{Status: "updated", Note: n}
		return updateErr
	})
	if err != nil {
		return "", err
	}
	return marshalResult(out)
}

func executeNotesDelete(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "notes.delete")
	if err != nil {
		return "", err
	}
	var p struct {
		ID string `json:"id"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("notes.delete: args: %w", unmarshalErr)
	}
	if itemErr := requireContentItem(scope, "notes.delete", "note", p.ID); itemErr != nil {
		return "", itemErr
	}
	if seams.noteOperation == nil {
		return "", errors.New("notes.delete: notes operation is unavailable")
	}
	err = seams.noteOperation.Run(ctx, func(callCtx context.Context, svc capability.NoteService) error {
		return svc.Delete(callCtx, p.ID)
	})
	if err != nil {
		return "", err
	}
	return marshalResult(contentDeleteResult{ID: p.ID, Status: "deleted"})
}

func executeSnippetsList(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "snippets.list")
	if err != nil {
		return "", err
	}
	if rootErr := requireContentRoot(scope, "snippets.list"); rootErr != nil {
		return "", rootErr
	}
	if seams.snippetOperation == nil {
		return "", errors.New("snippets.list: snippets operation is unavailable")
	}
	var snippets []snippet.Snippet
	err = seams.snippetOperation.Run(ctx, func(_ context.Context, svc capability.SnippetService) error {
		var listErr error
		snippets, listErr = svc.List()
		return listErr
	})
	if err != nil {
		return "", err
	}
	filtered := snippets[:0]
	for _, item := range snippets {
		if scope.Allows("snippet/" + item.ID) {
			filtered = append(filtered, item)
		}
	}
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	return marshalBoundedSnippets(filtered, bound.MaxBytes)
}

func executeSnippetsCreate(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "snippets.create")
	if err != nil {
		return "", err
	}
	var p struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("snippets.create: args: %w", unmarshalErr)
	}
	if rootErr := requireContentRoot(scope, "snippets.create"); rootErr != nil {
		return "", rootErr
	}
	if seams.snippetOperation == nil {
		return "", errors.New("snippets.create: snippets operation is unavailable")
	}
	var out snippetMutationResult
	err = seams.snippetOperation.Run(ctx, func(_ context.Context, svc capability.SnippetService) error {
		item, createErr := svc.Create(p.Title, p.Body)
		out = snippetMutationResult{Status: "created", Snippet: item}
		return createErr
	})
	if err != nil {
		return "", err
	}
	return marshalResult(out)
}

func executeSnippetsUpdate(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "snippets.update")
	if err != nil {
		return "", err
	}
	var p struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("snippets.update: args: %w", unmarshalErr)
	}
	if itemErr := requireContentItem(scope, "snippets.update", "snippet", p.ID); itemErr != nil {
		return "", itemErr
	}
	if seams.snippetOperation == nil {
		return "", errors.New("snippets.update: snippets operation is unavailable")
	}
	var out snippetMutationResult
	err = seams.snippetOperation.Run(ctx, func(_ context.Context, svc capability.SnippetService) error {
		item, updateErr := svc.Update(p.ID, p.Title, p.Body)
		out = snippetMutationResult{Status: "updated", Snippet: item}
		return updateErr
	})
	if err != nil {
		return "", err
	}
	return marshalResult(out)
}

func executeSnippetsDelete(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "snippets.delete")
	if err != nil {
		return "", err
	}
	var p struct {
		ID string `json:"id"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("snippets.delete: args: %w", unmarshalErr)
	}
	if itemErr := requireContentItem(scope, "snippets.delete", "snippet", p.ID); itemErr != nil {
		return "", itemErr
	}
	if seams.snippetOperation == nil {
		return "", errors.New("snippets.delete: snippets operation is unavailable")
	}
	err = seams.snippetOperation.Run(ctx, func(_ context.Context, svc capability.SnippetService) error {
		return svc.Delete(p.ID)
	})
	if err != nil {
		return "", err
	}
	return marshalResult(contentDeleteResult{ID: p.ID, Status: "deleted"})
}

func executeSnippetsReorder(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "snippets.reorder")
	if err != nil {
		return "", err
	}
	if rootErr := requireContentRoot(scope, "snippets.reorder"); rootErr != nil {
		return "", rootErr
	}
	var p struct {
		IDs []string `json:"ids"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("snippets.reorder: args: %w", unmarshalErr)
	}
	if seams.snippetOperation == nil {
		return "", errors.New("snippets.reorder: snippets operation is unavailable")
	}
	var out []snippet.Snippet
	err = seams.snippetOperation.Run(ctx, func(_ context.Context, svc capability.SnippetService) error {
		var reorderErr error
		out, reorderErr = svc.Reorder(p.IDs)
		return reorderErr
	})
	if err != nil {
		return "", err
	}
	return marshalResult(snippetsReorderResult{Snippets: out})
}

func marshalResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("agent tool: marshal result: %w", err)
	}
	return string(b), nil
}

type skillReadResult struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

func executeSkillsRead(ctx context.Context, cap agenttools.Capability, args json.RawMessage, seams toolSeams) (string, error) {
	scope, err := contentScope(cap, "skills.read")
	if err != nil {
		return "", err
	}
	var p struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("skills.read: args: %w", unmarshalErr)
	}
	if !scope.Allows("skill/" + p.Name) {
		return "", fmt.Errorf("skills.read: %q is outside this run's grant", p.Name)
	}
	if seams.skills == nil {
		return "", errors.New("skills.read: the skill library is unavailable")
	}
	got, err := seams.skills.Read(p.Name, p.Path)
	if err != nil {
		return "", fmt.Errorf("skills.read: %w", err)
	}
	return marshalResult(skillReadResult{Name: p.Name, Path: got.Path, Content: string(got.Bytes)})
}

func marshalBoundedNotes(rows []noteSearchRow, max int64) (string, error) {
	for count := len(rows); count >= 0; count-- {
		out := notesSearchResult{Notes: rows[:count], Truncated: count != len(rows), Dropped: len(rows) - count}
		b, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("notes.search: marshal result: %w", err)
		}
		if int64(len(b)) <= max {
			return string(b), nil
		}
	}
	return "", errors.New("notes.search: result bound is too small for its contract")
}

func marshalBoundedSnippets(rows []snippet.Snippet, max int64) (string, error) {
	for count := len(rows); count >= 0; count-- {
		out := snippetsListResult{Snippets: rows[:count], Truncated: count != len(rows), Dropped: len(rows) - count}
		b, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("snippets.list: marshal result: %w", err)
		}
		if int64(len(b)) <= max {
			return string(b), nil
		}
	}
	return "", errors.New("snippets.list: result bound is too small for its contract")
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
	Path      string          `json:"path"`
	Total     int64           `json:"total"`
	Revision  string          `json:"revision"`
	Window    filesReadWindow `json:"window"`
	Seen      filesReadSeen   `json:"seen"`
	Returned  int64           `json:"returned"`
	Truncated bool            `json:"truncated,omitempty"`
	Dropped   int64           `json:"dropped,omitempty"`
	Remaining int64           `json:"remaining,omitempty"`
	Binary    bool            `json:"binary,omitempty"`
	Text      string          `json:"text,omitempty"`
}

type filesReadWindow struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type filesReadSeen struct {
	Start int `json:"start"`
	End   int `json:"end"`
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
		return "", fmt.Errorf("files.read: args: %w", err)
	}
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	snapshot, err := scoped.ReadSnapshot(ctx, p.Path, bound.MaxBytes)
	if err != nil {
		return "", err
	}
	truncated := snapshot.WindowEnd < snapshot.Total
	out := filesReadResult{
		Path:      p.Path,
		Total:     snapshot.Total,
		Revision:  snapshot.Revision,
		Window:    filesReadWindow{Start: 0, End: snapshot.WindowEnd},
		Seen:      filesReadSeen{Start: snapshot.SeenStart, End: snapshot.SeenEnd},
		Returned:  snapshot.WindowEnd,
		Truncated: truncated,
		Binary:    snapshot.Binary,
		Text:      snapshot.Text,
	}
	if truncated {
		out.Dropped = snapshot.Total - snapshot.WindowEnd
		out.Remaining = out.Dropped
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("files.read: marshal result: %w", err)
	}
	return string(b), nil
}

type filesMutationResult struct {
	Path     string `json:"path"`
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func executeFilesEdit(ctx context.Context, cap agenttools.Capability, args json.RawMessage, _ toolSeams) (string, error) {
	if _, err := toolBound(ctx); err != nil {
		return "", err
	}
	editor, ok := cap.(*filesystem.ScopedEditor)
	if !ok {
		return "", fmt.Errorf("files.edit: capability is %T, not *filesystem.ScopedEditor", cap)
	}
	var p struct {
		Path     string `json:"path"`
		Revision string `json:"revision"`
		Patch    string `json:"patch"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("files.edit: args: %w", err)
	}
	result, err := editor.Edit(ctx, p.Path, p.Revision, p.Patch)
	if err != nil {
		return marshalFilesMutation(filesMutationResult{Path: p.Path, Status: "refused", Reason: err.Error()})
	}
	return marshalFilesMutation(filesMutationResult{Path: p.Path, Status: "applied", Revision: result.Revision})
}

func executeFilesCreate(ctx context.Context, cap agenttools.Capability, args json.RawMessage, _ toolSeams) (string, error) {
	if _, err := toolBound(ctx); err != nil {
		return "", err
	}
	editor, ok := cap.(*filesystem.ScopedEditor)
	if !ok {
		return "", fmt.Errorf("files.create: capability is %T, not *filesystem.ScopedEditor", cap)
	}
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("files.create: args: %w", err)
	}
	result, err := editor.Create(ctx, p.Path, p.Content)
	if err != nil {
		return marshalFilesMutation(filesMutationResult{Path: p.Path, Status: "refused", Reason: err.Error()})
	}
	return marshalFilesMutation(filesMutationResult{Path: p.Path, Status: "created", Revision: result.Revision})
}

func marshalFilesMutation(result filesMutationResult) (string, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("files mutation: marshal result: %w", err)
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
//
//	path a person uses, executed by the renderer — the backend never
//	writes to the PTY, design §2.1) ──────────────────────────────────────
const runStoppedMessage = "The person stopped this command. Do not retry it."

type runResult struct {
	SessionID string           `json:"sessionId"`
	EntryID   string           `json:"entryId"`
	ExitCode  *int             `json:"exitCode"`
	Status    string           `json:"status"`
	Stopped   bool             `json:"stopped"`
	Message   string           `json:"message,omitempty"`
	Total     int              `json:"total"`
	Window    readScreenWindow `json:"window"`
	Returned  readScreenWindow `json:"returned"`
	Text      string           `json:"text"`
	Truncated bool             `json:"truncated,omitempty"`
	Dropped   int64            `json:"dropped,omitempty"`
	Remaining int64            `json:"remaining,omitempty"`
}

// runResultStopped is the one interpretation of the renderer's explicit
// stop fact. It deliberately does not inspect exitCode: a command may exit
// 130 without anybody pressing Stop.
func runResultStopped(tool, out string) bool {
	if tool != "session.run" {
		return false
	}
	var result struct {
		Stopped bool `json:"stopped"`
	}
	return json.Unmarshal([]byte(out), &result) == nil && result.Stopped
}

// runBodyWire is this tool's consumer view of the resolved run body the
// requester returned: the entry id, the exit status, the block's frozen
// status vocabulary (success | failure | entered | unknown), the explicit
// person-stop fact, the output's total line count, the span of the window
// actually returned and its text.
// The wire vocabulary is owned by the transport's run kind validation; this
// decode reads the fields the window contract needs and never re-validates
// them.
type runBodyWire struct {
	EntryID  string `json:"entryId"`
	ExitCode *int   `json:"exitCode"`
	Status   string `json:"status"`
	Stopped  bool   `json:"stopped"`
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
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		Command string `json:"command"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		// Unreachable through the middleware (validation precedes policy,
		// let alone execution); the direct-call seam still answers honestly.
		return "", fmt.Errorf("run: args: %w", unmarshalErr)
	}
	if p.Command == "" {
		return "", errors.New("run: an empty command is a bare newline, not an execution")
	}
	sessionID := runner.SessionID()
	if !runner.Allows(sessionID) {
		return "", fmt.Errorf("run: session %q is outside the run's grant — the request never reached the renderer", sessionID)
	}
	if requester == nil {
		return "", errors.New("run: no renderer requester is wired for this run")
	}
	body, err := requester.RequestRun(ctx, sessionID, p.Command)
	if err != nil {
		var leaseErr *RunLeaseError
		if errors.As(err, &leaseErr) && caused != nil && leaseErr.EntryID != "" {
			caused(leaseErr.EntryID)
		}
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

	total := b.Total
	asked := readScreenWindow{Start: 0, End: total}
	if total < 0 || b.Start < 0 || b.End < b.Start || b.End > total {
		return "", fmt.Errorf("run: the renderer's window [%d,%d) is outside the block's [0,%d)", b.Start, b.End, total)
	}
	returned := readScreenWindow{Start: b.Start, End: b.End}
	text, returnedEnd := boundBlockText(b.Text, b.Start, b.End, bound.MaxBytes)
	returned.End = returnedEnd
	truncated := len(text) < len(b.Text)

	out := runResult{
		SessionID: sessionID,
		EntryID:   b.EntryID,
		ExitCode:  b.ExitCode,
		Status:    b.Status,
		Stopped:   b.Stopped,
		Message:   "",
		Total:     total,
		Window:    asked,
		Returned:  returned,
		Text:      text,
		Truncated: truncated,
	}
	if out.Stopped {
		out.Message = runStoppedMessage
	}
	if truncated {
		out.Dropped = int64(len(b.Text) - len(text))
		out.Remaining = out.Dropped
	}
	res, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("run: marshal result: %w", err)
	}
	return string(res), nil
}
