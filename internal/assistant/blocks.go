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
)

var ErrSessionItemNotFound = errors.New("no such item in this session")

// boundBlockText applies the window's BYTE bound — the line count is what
// the model aims with, and this is the budget it cannot overrun with 2000
// very long lines. It cuts on a line boundary and returns the end the
// returned window must state, so the reply never claims lines it did not
// carry.
func boundBlockText(text string, start, end int, maxBytes int64) (string, int) {
	if maxBytes <= 0 || int64(len(text)) <= maxBytes {
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
		if int64(kept+width) > maxBytes {
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
		return text[:int(maxBytes)], start + 1
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
	Truncated bool              `json:"truncated,omitempty"`
	Dropped   int64             `json:"dropped,omitempty"`
	Remaining int64             `json:"remaining,omitempty"`
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
	Truncated bool              `json:"truncated,omitempty"`
	Dropped   int64             `json:"dropped,omitempty"`
	Remaining int64             `json:"remaining,omitempty"`
	Cursor    *readScreenCursor `json:"cursor,omitempty"`
	Identity  *readScreenIdent  `json:"identity,omitempty"`
	Note      string            `json:"note,omitempty"`
}

type blockSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

func executeSessionList(ctx context.Context, reader *agenttools.SessionReader, source SessionSource, args json.RawMessage) (string, error) {
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		Limit int `json:"limit"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("session.list: args: %w", unmarshalErr)
	}
	sessionID := reader.SessionID()
	if !reader.Allows(sessionID) {
		return "", fmt.Errorf("session.list: session %q is outside the run's grant", sessionID)
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
	items, err := source.ListSessionItems(ctx, sessionID, p.Limit)
	if err != nil {
		return "", fmt.Errorf("session.list: %w", err)
	}
	out := sessionListResult{
		SessionID: sessionID,
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
	originalBytes := len(b)
	for int64(len(b)) > bound.MaxBytes && len(out.Items) > 0 {
		out.Items = out.Items[:len(out.Items)-1]
		out.More = true
		b, err = json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("session.list: marshal truncated result: %w", err)
		}
	}
	if int64(len(b)) > bound.MaxBytes {
		return "", fmt.Errorf("session.list: result metadata exceeds declared result bound of %d bytes", bound.MaxBytes)
	}
	if len(b) < originalBytes {
		out.Truncated = true
		out.Dropped = int64(originalBytes - len(b))
		out.Remaining = out.Dropped
		b, err = json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("session.list: marshal bounded result: %w", err)
		}
		if int64(len(b)) > bound.MaxBytes {
			return "", fmt.Errorf("session.list: bounded result metadata exceeds declared result bound of %d bytes", bound.MaxBytes)
		}
	}
	return string(b), nil
}

func executeSessionRead(ctx context.Context, reader *agenttools.SessionReader, source SessionSource, requester RendererRequester, args json.RawMessage) (string, error) {
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	var p struct {
		ID    string `json:"id"`
		Start int    `json:"start"`
		Count int    `json:"count"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("session.read: args: %w", unmarshalErr)
	}
	sessionID := reader.SessionID()
	if p.Start < 0 || p.Count < 0 {
		return "", errors.New("session.read: start and count must be non-negative")
	}
	if !reader.Allows(sessionID) {
		return "", fmt.Errorf("session.read: session %q is outside the run's grant", sessionID)
	}
	if p.ID == "" {
		return executeSessionScreen(ctx, sessionID, requester, p.Start, p.Count, bound.MaxBytes)
	}
	if reader.IsAutomaticItem(p.ID) {
		return executeSessionItemScreen(ctx, sessionID, p.ID, requester, p.Start, p.Count, bound.MaxBytes)
	}
	if source == nil {
		return "", errors.New("session.read: no session source is wired for this run")
	}
	// A MARKED ITEM IS READ INSIDE ITS MARK (nocx-hp8p2.15). The person
	// selected those rows and asked about them, and the span travelled here
	// with the ask — so it is what the run knows, not a hint. A call that
	// names the item and leaves the window out used to fall through to the
	// default below and read to the end of the block: one marked line of
	// `df -h` came back as the whole of `df -h`, and the answer was about
	// the command. Two rounds of prompt wording did not stop it, because
	// asking the model to be careful is not a bound.
	//
	// A window the model DID ask for is honoured — it may legitimately want
	// context around the mark, and it can only ever reach rows the grant
	// already allows.
	if mark, ok := reader.MarkedWindow(p.ID); ok && p.Start == 0 && p.Count <= 0 {
		p.Start, p.Count = mark.Start, mark.Count
	} else if mark, ok := reader.MarkedWindow(p.ID); ok && p.Count <= 0 {
		p.Count = mark.Count
	}
	if p.Count <= 0 {
		p.Count = defaultBlockLines
	}
	if p.Count > maxBlockLines {
		p.Count = maxBlockLines
	}
	item, err := source.ReadSessionItem(ctx, sessionID, p.ID, p.Start, p.Count)
	if err != nil {
		return "", fmt.Errorf("session.read: %w", err)
	}
	if item.State == "running" {
		return executeSessionItemScreen(ctx, sessionID, p.ID, requester, p.Start, p.Count, bound.MaxBytes)
	}
	outText, returnedEnd := boundBlockText(item.Text, item.Start, item.End, bound.MaxBytes)
	out := sessionReadResult{
		SessionID: sessionID,
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
	if len(outText) < len(item.Text) {
		out.Truncated = true
		out.Dropped = int64(len(item.Text) - len(outText))
		out.Remaining = int64(len(item.Text) - len(outText))
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("session.read: marshal result: %w", err)
	}
	return string(b), nil
}

func executeSessionItemScreen(ctx context.Context, sessionID, itemID string, requester RendererRequester, start, count int, maxBytes int64) (string, error) {
	body, err := executeSessionScreen(ctx, sessionID, requester, start, count, maxBytes)
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

func executeSessionScreen(ctx context.Context, sessionID string, requester RendererRequester, start, count int, maxBytes int64) (string, error) {
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
	// A frame row is its text: the renderer joins the cells' characters in
	// column order before the row leaves it, blanks kept, so the row's width
	// is the screen's (nocx-u3vxd).
	lines := make([]string, 0, len(frame.Rows))
	for _, row := range frame.Rows {
		lines = append(lines, row.Text)
	}
	fullText := strings.Join(lines, "\n")
	text, returnedEnd := boundBlockText(fullText, returned.Start, returned.End, maxBytes)
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
	if len(text) < len(fullText) {
		out.Truncated = true
		out.Dropped = int64(len(fullText) - len(text))
		out.Remaining = int64(len(fullText) - len(text))
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
