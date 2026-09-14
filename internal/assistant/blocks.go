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
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

const (
	defaultBlockListLimit = 10
	maxBlockListLimit     = 50
	defaultBlockLines     = 200
	maxBlockLines         = 2000
)

var ErrSessionItemNotFound = errors.New("no such item in this session")

// ── the descendant read path (design §6.1, §11 "kept, deliberately") ──────
//
// session.read naming one of the caller's DESCENDANTS is read through
// PaneReader, which internal/app builds over the real helper client and the
// agent rule (session_targets.go) — never through RendererRequester, which
// stays reserved for the run's OWN pane (spec §11). This package cannot
// name internal/app's concrete types (app depends on assistant, not the
// reverse — the same layering RunContext.PaneAccess already lives with), so
// PaneReader.Read's access parameter is `any`: the RunContext.PaneAccess
// value, forwarded here untouched and type-asserted back to its concrete
// type at app's own point of use inside the Read implementation.

// TargetView is one minted target (design §6.1–§6.3): the signed token, the
// rows and kind it was minted for, and — for a menu — the menu it was
// minted over.
type TargetView struct {
	Token     string
	TokenID   string
	Kind      sessionruntime.TargetKind
	Rows      sessionruntime.RowRange
	Region    string
	Menu      *agentdriver.Menu
	ExpiresAt time.Time
}

// MessageView is one queued session.message, as session.read's
// pendingMessages reports it (design §8). Task 8 declares the shape
// PaneRead needs before Task 10 builds the queue behind it: Phase is a
// plain string here rather than the closed MessagePhase set Task 10 adds in
// internal/app/pane_messages.go — replace this field's type there rather
// than adding a second one, once that set exists. Pending is always empty
// and DeliveryLost always nil until then (design §8.1).
type MessageView struct {
	ID           string
	Namespace    string
	Phase        string
	BytesWritten int
	BoxContents  string
}

// PaneRead is one session.read of a descendant's pane: the frame the
// helper's snapshot carried, what the agent rule classified it as, and — on
// request — the target minted from that same frame (design §6.1).
type PaneRead struct {
	Frame          paneview.Frame
	Classification agentdriver.State
	Target         *TargetView
	Pending        []MessageView
	DeliveryLost   *time.Time
	ReadBarrier    bool
}

// PaneReader is session.read's helper-backed read path for a sessionId
// naming one of the caller's descendants. internal/app's concrete
// implementation is the one production value; RunContext.SessionReads
// carries it as `any` for the reason explained above, and executeSessionRead
// asserts this interface at the point of use.
type PaneReader interface {
	Read(ctx context.Context, access any, sessionID string, want *sessionruntime.TargetKind, rows *sessionruntime.RowRange) (PaneRead, error)
}

// sessionRowRange is a target's rows on the wire — BOTH ends inclusive,
// exactly as sessionruntime.RowRange and agentdriver.RowSpan already state
// them internally. It is a distinct wire shape from blockSpan (a ledger or
// screen WINDOW, whose End is one PAST the last line): reusing blockSpan
// here would silently change what "end" means depending on which field of
// the same result you read.
type sessionRowRange struct {
	First int `json:"first"`
	Last  int `json:"last"`
}

type sessionMenuWire struct {
	Question string           `json:"question"`
	Options  []string         `json:"options"`
	Selected int              `json:"selected"`
	Rows     sessionRowRange  `json:"rows"`
	Body     *sessionRowRange `json:"body,omitempty"`
}

type sessionTargetWire struct {
	Token       string           `json:"token"`
	TokenID     string           `json:"tokenId"`
	Kind        string           `json:"kind"`
	Rows        sessionRowRange  `json:"rows"`
	Region      string           `json:"region,omitempty"`
	Menu        *sessionMenuWire `json:"menu,omitempty"`
	ExpiresAtMs int64            `json:"expiresAtMs"`
}

type sessionMessageWire struct {
	ID           string `json:"id"`
	Namespace    string `json:"namespace"`
	Phase        string `json:"phase"`
	BytesWritten int    `json:"bytesWritten,omitempty"`
	BoxContents  string `json:"boxContents,omitempty"`
}

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
	// Classification, Target, PendingMessages and ReadBarrier are set only
	// for a sessionId naming a descendant (design §6.1, §7.1, §8, Task 8) —
	// absent for the run's own pane, which keeps the renderer path above
	// unchanged and has no target, classification or read barrier to
	// report.
	Classification  string               `json:"classification,omitempty"`
	Target          *sessionTargetWire   `json:"target,omitempty"`
	PendingMessages []sessionMessageWire `json:"pendingMessages,omitempty"`
	ReadBarrier     *bool                `json:"readBarrier,omitempty"`
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

// applyMarkedWindow narrows a session.read to the row span a PERSON marked on
// this item, when they marked one and the call did not name a window of its
// own. It is the one owner of that rule, because the rule is the same
// wherever the rows come from: a block read from the ledger (nocx-hp8p2.15)
// and the frozen screen a summon attaches (nocx-hp8p2.7) are two surfaces
// with one meaning — the person asked about THESE rows, and the span is
// authority rather than a hint the model may improve on.
//
// A window the model asked for itself is honoured: it may legitimately want
// context around the mark, and it can only ever reach rows the grant already
// allows. A start with no count keeps the mark's count, so "read from here"
// stays as long as the mark rather than becoming the whole item.
func applyMarkedWindow(reader *agenttools.SessionReader, id string, start, count *int) {
	mark, ok := reader.MarkedWindow(id)
	if !ok || *count > 0 {
		return
	}
	if *start == 0 {
		*start = mark.Start
	}
	*count = mark.Count
}

func executeSessionRead(ctx context.Context, cap *agenttools.SessionDescendantCapability, source SessionSource, requester RendererRequester, args json.RawMessage) (string, error) {
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	if cap == nil || cap.SessionReader == nil {
		return "", errors.New("session.read: capability carries no session reader")
	}
	reader := cap.SessionReader
	var p struct {
		SessionID string `json:"sessionId"`
		ID        string `json:"id"`
		Start     int    `json:"start"`
		Count     int    `json:"count"`
		Target    string `json:"target"`
	}
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("session.read: args: %w", unmarshalErr)
	}
	ownSession := reader.SessionID()
	if p.Start < 0 || p.Count < 0 {
		return "", errors.New("session.read: start and count must be non-negative")
	}
	targetSessionID := p.SessionID
	if targetSessionID == "" {
		targetSessionID = ownSession
	}
	// A sessionId naming a DESCENDANT is read through PaneReader — never
	// through the renderer, which stays reserved for the run's own pane
	// (design §11 "kept, deliberately"). This branches on the sessionId
	// alone, before any grant check: reader.Allows only ever covers the
	// run's OWN session, and a descendant's authority is PaneAccess, a
	// completely different capability (§7.1).
	if targetSessionID != ownSession {
		return executeDescendantSessionRead(ctx, cap, targetSessionID, p.Target, bound.MaxBytes)
	}
	sessionID := ownSession
	if !reader.Allows(sessionID) {
		return "", fmt.Errorf("session.read: session %q is outside the run's grant", sessionID)
	}
	if p.ID == "" {
		return executeSessionScreen(ctx, sessionID, requester, p.Start, p.Count, bound.MaxBytes)
	}
	if reader.IsAutomaticItem(p.ID) {
		// A ROW MARK NARROWS THE FROZEN SCREEN, exactly as it narrows a block
		// below (nocx-hp8p2.7): a person who selected rows inside the pinned
		// screen marked THAT item, and the span travelled here with the ask.
		applyMarkedWindow(reader, p.ID, &p.Start, &p.Count)
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
	applyMarkedWindow(reader, p.ID, &p.Start, &p.Count)
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

// descendantTargetKinds is the closed set session.read's own `target`
// parameter accepts (design §6.3) — the same four kinds sessionruntime
// names, spelled out here because the schema validates the string and this
// is where it becomes the typed value PaneReader.Read wants.
var descendantTargetKinds = map[string]sessionruntime.TargetKind{
	string(sessionruntime.TargetMenu):    sessionruntime.TargetMenu,
	string(sessionruntime.TargetInput):   sessionruntime.TargetInput,
	string(sessionruntime.TargetWorking): sessionruntime.TargetWorking,
	string(sessionruntime.TargetRegion):  sessionruntime.TargetRegion,
}

// executeDescendantSessionRead is session.read's path for a sessionId
// naming one of the caller's descendants (design §6.1, §7.1, §11 "kept,
// deliberately"): PaneReader — never RendererRequester, which stays
// reserved for the run's own pane — asks the helper for a snapshot,
// classifies it with the agent rule, and, when target names a kind, mints
// one from that same frame.
func executeDescendantSessionRead(ctx context.Context, cap *agenttools.SessionDescendantCapability, sessionID, target string, maxBytes int64) (string, error) {
	if cap.PaneAccess == nil || cap.SessionReads == nil {
		return "", fmt.Errorf("session.read: %q is not this run's own session and no descendant pane authority is wired for this run", sessionID)
	}
	reader, ok := cap.SessionReads.(PaneReader)
	if !ok {
		return "", fmt.Errorf("session.read: session reads capability is %T, not a PaneReader", cap.SessionReads)
	}
	var want *sessionruntime.TargetKind
	if target != "" {
		kind, valid := descendantTargetKinds[target]
		if !valid {
			return "", fmt.Errorf("session.read: %q is not a target kind this tool knows", target)
		}
		want = &kind
	}
	read, err := reader.Read(ctx, cap.PaneAccess, sessionID, want, nil)
	if err != nil {
		return "", fmt.Errorf("session.read: %w", err)
	}
	out := descendantSessionReadResult(sessionID, read, maxBytes)
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("session.read: marshal descendant result: %w", err)
	}
	return string(b), nil
}

// frameText joins a frame's rows into the same shape executeSessionScreen
// already produces from the renderer's own frame body: one line per row,
// blanks kept, cells joined in column order.
func frameText(f paneview.Frame) string {
	lines := make([]string, 0, len(f.Lines))
	for _, row := range f.Lines {
		var b strings.Builder
		for _, cell := range row {
			b.WriteString(cell.Text)
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
}

// descendantSessionReadResult renders one PaneRead into session.read's wire
// shape. classification is "none" for a non-agent pane, matching PaneRead's
// own documented meaning for the zero agentdriver.State rather than an
// empty string a reader might mistake for "not reported".
func descendantSessionReadResult(sessionID string, read PaneRead, maxBytes int64) sessionReadResult {
	fullText := frameText(read.Frame)
	text, _ := boundBlockText(fullText, 0, len(read.Frame.Lines), maxBytes)
	classification := string(read.Classification)
	if classification == "" {
		classification = "none"
	}
	readBarrier := read.ReadBarrier
	out := sessionReadResult{
		SessionID:      sessionID,
		State:          "screen",
		Source:         "helper",
		Text:           text,
		Classification: classification,
		ReadBarrier:    &readBarrier,
	}
	if len(text) < len(fullText) {
		out.Truncated = true
		out.Dropped = int64(len(fullText) - len(text))
		out.Remaining = out.Dropped
	}
	for _, m := range read.Pending {
		// MessageView and sessionMessageWire share the same fields, in the
		// same order — a plain conversion rather than a field-by-field
		// literal, so a field added to one is a compile error until the
		// other names it too.
		out.PendingMessages = append(out.PendingMessages, sessionMessageWire(m))
	}
	if read.Target != nil {
		out.Target = sessionTargetWireFrom(read.Target)
	}
	return out
}

func sessionTargetWireFrom(t *TargetView) *sessionTargetWire {
	wire := &sessionTargetWire{
		Token:       t.Token,
		TokenID:     t.TokenID,
		Kind:        string(t.Kind),
		Rows:        sessionRowRange{First: t.Rows.First, Last: t.Rows.Last},
		Region:      t.Region,
		ExpiresAtMs: t.ExpiresAt.UnixMilli(),
	}
	if t.Menu != nil {
		menu := &sessionMenuWire{
			Question: t.Menu.Question,
			Options:  t.Menu.Options,
			Selected: t.Menu.Selected,
			Rows:     sessionRowRange{First: t.Menu.Rows.First, Last: t.Menu.Rows.Last},
		}
		if t.Menu.Body.Last >= t.Menu.Body.First {
			menu.Body = &sessionRowRange{First: t.Menu.Body.First, Last: t.Menu.Body.Last}
		}
		wire.Menu = menu
	}
	return wire
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
