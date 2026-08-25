package assistant

// The block tools (nocx-5u3oz.6): list the finished blocks this run was
// granted, and read a WINDOW of one.
//
// WHY THEY EXIST. In nocx a finished command's rows LEAVE the xterm grid at
// the block freeze and the DOM owns them — the renderer's clearViewport says
// it in as many words: "the grid only ever holds the running command's rows,
// and the DOM owns the scrollback". So readScreen, which reads the grid,
// answers a screenful of empty lines for everything that has already
// finished, which is everything the person is looking at. A run asked "what
// command did I run?" over a screen full of `df` output read 33 empty rows
// and went guessing at ~/.bash_history.
//
// WHERE THE TEXT COMES FROM, and this is the decision the bead asked to be
// made explicitly: the LEDGER, not the renderer. ADR-0019 decision 1 is one
// authoritative ledger with disposable projections, and the DOM scrollback
// is a projection of it — the renderer already writes every frozen block
// there (history.record for the row, ledger.capture for the two bodies), so
// reading the record is reading what the renderer put there rather than
// asking it to re-derive it. It needs no renderer round trip, so it has no
// timeout and no "the tab is gone" hang; it survives a closed tab; and it
// reuses the query, the paging and the artifact read that already exist
// instead of growing a second enumeration of blocks beside them.
//
// What that costs is named on the return rather than hidden: a block whose
// body the store never kept (history off, output retention off, a sensitive
// command) is listed with bodyKept false and reads as a stated absence, and
// a body the capture truncated says so (truncated: "cap" — the middle went,
// the head and tail are what the store has).
//

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/agenttools"
)

const (
	defaultBlockListLimit = 10
	maxBlockListLimit     = 50
	defaultBlockLines     = 200
	maxBlockLines         = 2000
	maxBlockWindowBytes   = 64 << 10
)

var ErrSessionItemNotFound = errors.New("no such item in this session")

// boundBlockText applies the window's BYTE bound — the line count is what
// the model aims with, and this is the budget it cannot overrun with 2000
// very long lines. It cuts on a line boundary and returns the end the
// returned window must state, so the reply never claims lines it did not
// carry.
func boundBlockText(text string, start, end int) (string, int) {
	if len(text) <= maxBlockWindowBytes {
		return text, end
	}
	kept := 0
	lines := 0
	for kept < len(text) {
		nl := indexNewline(text[kept:])
		width := nl + 1
		if nl < 0 {
			width = len(text) - kept
		}
		if kept+width > maxBlockWindowBytes {
			break
		}
		kept += width
		lines++
	}
	out := text[:kept]
	// A single line longer than the whole budget would keep nothing at all;
	// answer with the head of it rather than with an empty window that reads
	// as "the block printed nothing".
	if lines == 0 {
		return text[:maxBlockWindowBytes], start + 1
	}
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, start + lines
}

func indexNewline(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return i
		}
	}
	return -1
}

// SessionSource is the shared read seam for session.list and session.read.
// The transport implements it from the ledger for item state and finished
// output. A running item is completed by the renderer half of session.read,
// so the backend never reconstructs live terminal text.
type SessionSource interface {
	ListSessionItems(ctx context.Context, sessionID string, limit int) (SessionItems, error)
	ReadSessionItem(ctx context.Context, sessionID, itemID string, start, count int) (SessionItemRead, error)
}

type SessionItem struct {
	ID       string
	Command  string
	State    string
	ExitCode *int
	Lines    int
}

type SessionItems struct {
	Items []SessionItem
	More  bool
}

type SessionItemRead struct {
	ID       string
	Command  string
	State    string
	ExitCode *int
	Total    int
	Start    int
	End      int
	Text     string
	Note     string
}

type sessionListResult struct {
	SessionID string            `json:"sessionId"`
	Items     []sessionListItem `json:"items"`
	More      bool              `json:"more,omitempty"`
}
type sessionListItem struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	State    string `json:"state"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Lines    int    `json:"lines"`
}

type sessionReadResult struct {
	SessionID string            `json:"sessionId"`
	ID        string            `json:"id,omitempty"`
	State     string            `json:"state"`
	Source    string            `json:"source"`
	ExitCode  *int              `json:"exitCode,omitempty"`
	Total     int               `json:"total,omitempty"`
	Window    blockSpan         `json:"window,omitempty"`
	Returned  blockSpan         `json:"returned,omitempty"`
	Text      string            `json:"text"`
	Cursor    *readScreenCursor `json:"cursor,omitempty"`
	Identity  *readScreenIdent  `json:"identity,omitempty"`
	Note      string            `json:"note,omitempty"`
}

type blockSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

func executeSessionList(ctx context.Context, reader *agenttools.SessionReader, source SessionSource, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"sessionId"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("session.list: args: %w", err)
	}
	if !reader.Allows(p.SessionID) {
		return "", fmt.Errorf("session.list: session %q is outside the run's grant", p.SessionID)
	}
	if source == nil {
		return "", errors.New("session.list: no session source is wired for this run")
	}
	if p.Limit <= 0 {
		p.Limit = defaultBlockListLimit
	}
	if p.Limit > maxBlockListLimit {
		p.Limit = maxBlockListLimit
	}
	items, err := source.ListSessionItems(ctx, p.SessionID, p.Limit)
	if err != nil {
		return "", fmt.Errorf("session.list: %w", err)
	}
	out := sessionListResult{
		SessionID: p.SessionID,
		Items:     make([]sessionListItem, 0, len(items.Items)),
		More:      items.More,
	}
	for _, item := range items.Items {
		out.Items = append(out.Items, sessionListItem(item))
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("session.list: marshal result: %w", err)
	}
	return string(b), nil
}

func executeSessionRead(ctx context.Context, reader *agenttools.SessionReader, source SessionSource, requester RendererRequester, args json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"sessionId"`
		ID        string `json:"id"`
		Start     int    `json:"start"`
		Count     int    `json:"count"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("session.read: args: %w", err)
	}
	if p.Start < 0 || p.Count < 0 {
		return "", errors.New("session.read: start and count must be non-negative")
	}
	if !reader.Allows(p.SessionID) {
		return "", fmt.Errorf("session.read: session %q is outside the run's grant", p.SessionID)
	}
	if p.ID == "" {
		return executeSessionScreen(ctx, p.SessionID, requester, p.Start, p.Count)
	}
	if source == nil {
		return "", errors.New("session.read: no session source is wired for this run")
	}
	if p.Count <= 0 {
		p.Count = defaultBlockLines
	}
	if p.Count > maxBlockLines {
		p.Count = maxBlockLines
	}
	item, err := source.ReadSessionItem(ctx, p.SessionID, p.ID, p.Start, p.Count)
	if err != nil {
		return "", fmt.Errorf("session.read: %w", err)
	}
	if item.State == "running" {
		return executeSessionItemScreen(ctx, p.SessionID, p.ID, requester, p.Start, p.Count)
	}
	outText, returnedEnd := boundBlockText(item.Text, item.Start, item.End)
	out := sessionReadResult{
		SessionID: p.SessionID,
		ID:        item.ID,
		State:     item.State,
		Source:    "ledger",
		ExitCode:  item.ExitCode,
		Total:     item.Total,
		Window:    blockSpan{Start: p.Start, End: p.Start + p.Count},
		Returned:  blockSpan{Start: item.Start, End: returnedEnd},
		Text:      outText,
		Note:      item.Note,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("session.read: marshal result: %w", err)
	}
	return string(b), nil
}

func executeSessionItemScreen(ctx context.Context, sessionID, itemID string, requester RendererRequester, start, count int) (string, error) {
	body, err := executeSessionScreen(ctx, sessionID, requester, start, count)
	if err != nil {
		return "", err
	}
	var screen sessionReadResult
	if unmarshalErr := json.Unmarshal([]byte(body), &screen); unmarshalErr != nil {
		return "", fmt.Errorf("session.read: screen result: %w", unmarshalErr)
	}
	screen.ID = itemID
	screen.State = "running"
	b, err := json.Marshal(screen)
	if err != nil {
		return "", fmt.Errorf("session.read: marshal running result: %w", err)
	}
	return string(b), nil
}

func executeSessionScreen(ctx context.Context, sessionID string, requester RendererRequester, start, count int) (string, error) {
	if requester == nil {
		return "", errors.New("session.read: no renderer requester is wired for this run")
	}
	var region *FrameRegion
	if count > 0 {
		region = &FrameRegion{Start: start, End: start + count}
	}
	body, err := requester.RequestScreen(ctx, sessionID, region)
	if err != nil {
		return "", err
	}
	var frame frameBodyWire
	if unmarshalErr := json.Unmarshal(body, &frame); unmarshalErr != nil {
		return "", fmt.Errorf("session.read: frame body: %w", unmarshalErr)
	}
	if frame.Identity == nil {
		return "", errors.New("session.read: the renderer's frame carried no capture identity")
	}
	asked := blockSpan{Start: 0, End: frame.Identity.Rows}
	if region != nil {
		asked = blockSpan{Start: region.Start, End: region.End}
	}
	returned := asked
	if frame.Range != nil {
		returned = blockSpan{Start: frame.Range.Start, End: frame.Range.End}
	}
	var lines []string
	for _, row := range frame.Rows {
		if row.Kind == "text" {
			lines = append(lines, row.Text)
			continue
		}
		var line strings.Builder
		for _, cell := range row.Cells {
			line.WriteString(cell.Char)
		}
		lines = append(lines, line.String())
	}
	text, returnedEnd := boundBlockText(strings.Join(lines, "\n"), returned.Start, returned.End)
	returned.End = returnedEnd
	out := sessionReadResult{
		SessionID: sessionID,
		State:     "screen",
		Source:    "renderer",
		Total:     frame.Identity.Rows,
		Window:    asked,
		Returned:  returned,
		Text:      text,
	}
	if frame.Cursor != nil {
		out.Cursor = &readScreenCursor{Line: frame.Cursor.Line, Col: frame.Cursor.Col}
	}
	out.Identity = &readScreenIdent{
		Buffer: struct {
			Kind string `json:"kind"`
		}{Kind: frame.Identity.Buffer.Kind},
		Cols: frame.Identity.Cols, Rows: frame.Identity.Rows, Generation: frame.Identity.Generation,
	}
	if frame.Identity.Buffer.Kind == "alternate" {
		out.Note = "the alternate buffer has no scrollback; this is the current screen, not accumulated output"
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("session.read: marshal screen result: %w", err)
	}
	return string(b), nil
}
