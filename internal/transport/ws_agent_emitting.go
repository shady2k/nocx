package transport

// WHAT AN ENROLLED PANE IS EMITTING, and what its rule reads on it
// (nocx-02uci; contracts/agent.emitting.schema.json).
//
// # Why this crossing exists
//
// The per-agent driver configuration design (2026-08-27) is explicit that the
// emitting view is not optional: "a rule the user must write blind is a dead
// rule." Half of that view is the screen; the other half — the one that is
// easy to forget — is the RULE's reading of it, because a view that shows only
// the screen is a screen and the person already has one of those.
//
// # It is a PULL, and that is the whole of its interval
//
// There is no subscription here and no sweep. The request IS the looking, so a
// surface that has closed asks nothing and nothing in the backend keeps reading
// a pane nobody is watching — the defect nocx-8hdia is an epic about. The
// interval opens at the first request a mounted surface makes for a pane and
// closes when it stops asking, and it needs no second end in the backend
// because there is no backend state to close.
//
// # Why it is not a third power
//
// The AD-6 amendment grants an enrolled pane's grid exactly two powers —
// whether nocx may write into the pane, and what its indicator shows — and says
// the list is exhaustive. This method exercises neither and adds no third,
// because a power is a power to DECIDE and this decides nothing: it creates no
// enrolment, moves no state, reaches no network destination, and is not shown
// to the user as their terminal or persisted as their history. It hands the
// pane's own operator a read-out of a screen they own and are already looking
// at, through the same window the grid already exists in. Enrolment stays an
// act performed on the authenticated channel; asking about a pane that was
// never enrolled produces nothing at all.

import (
	"context"
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/session"
)

// agentRules is the transport's half of the rule seam (AD-8). One method,
// because the transport may ask what a rule reads on a frame and may not
// classify, enrol, or edit a rule.
type agentRules interface {
	Explain(agent string, f panegrid.Frame) agentdriver.Explanation
}

// WithAgentRules attaches the driver registry the emitting view reads through.
//
// When it is not wired the method answers "not available" rather than an empty
// reading: a view that showed a person no anchors and no branches for a rule
// that exists would send them to repair a document that is fine.
func WithAgentRules(r agentRules) WSServerOption {
	return func(s *WSServer) { s.agentRules = r }
}

type agentEmittingParams struct {
	SessionID string `json:"sessionId"`
}

func validateAgentEmittingRaw(raw json.RawMessage) string {
	var p agentEmittingParams
	// Strict, so a caller that misspells the one field it may send is told
	// so rather than quietly answered with the pane list — which looks
	// exactly like "nocx is not watching that pane".
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	// Absent is legitimate: a surface asks for the pane list before a person
	// has picked one. Present and empty is not — it is a caller that thinks
	// it named a pane.
	if p.SessionID != "" && utf8.RuneCountInString(p.SessionID) > maxIDRunes {
		return "sessionId is bounded"
	}
	return ""
}

// agentEmittingResult is the answer. The pane list rides with every reading
// rather than living in a method of its own, so the poll that refreshes the
// view also refreshes the list: a pane whose observation closed leaves the
// list on the next answer, which is how the surface learns it has nothing left
// to show.
type agentEmittingResult struct {
	Panes   []agentEmittingPane `json:"panes"`
	Reading *agentEmittingRead  `json:"reading,omitempty"`
}

type agentEmittingPane struct {
	SessionID string `json:"sessionId"`
	Agent     string `json:"agent"`
}

type agentEmittingRead struct {
	SessionID     string                   `json:"sessionId"`
	InstanceID    string                   `json:"instanceId"`
	SessionEpoch  uint64                   `json:"sessionEpoch"`
	Agent         string                   `json:"agent"`
	HasRule       bool                     `json:"hasRule"`
	State         string                   `json:"state"`
	Fallback      string                   `json:"fallback"`
	MatchedBranch *int                     `json:"matchedBranch,omitempty"`
	Frame         agentEmittingFrame       `json:"frame"`
	Anchors       []agentEmittingAnchor    `json:"anchors"`
	Branches      []agentEmittingBranch    `json:"branches"`
	Extractors    []agentEmittingExtractor `json:"extractors"`
}

type agentEmittingFrame struct {
	Cols      int                 `json:"cols"`
	Rows      int                 `json:"rows"`
	CursorX   int                 `json:"cursorX"`
	CursorY   int                 `json:"cursorY"`
	AltScreen bool                `json:"altScreen"`
	Lines     []agentEmittingLine `json:"lines"`
}

// agentEmittingLine carries one entry per COLUMN, so an index into Cells is a
// column index. That exactness is the point rather than a convenience: both of
// the amendment's powers are positional, ADR-0041 pins the emulator for its
// column geometry, and a double-width character occupies two columns — the
// grapheme at the first, an empty continuation cell at the second. Joining
// them into a string would lose the alignment the cursor position is stated in.
type agentEmittingLine struct {
	Cells   []string `json:"cells"`
	Rule    string   `json:"rule,omitempty"`
	OpensAt *int     `json:"opensAt,omitempty"`
}

type agentEmittingAnchor struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	From  string `json:"from,omitempty"`
	Bound bool   `json:"bound"`
	Row   *int   `json:"row,omitempty"`
}

type agentEmittingBranch struct {
	State      string                   `json:"state,omitempty"`
	Reached    bool                     `json:"reached"`
	Matched    bool                     `json:"matched"`
	Predicates []agentEmittingPredicate `json:"predicates"`
	Below      *agentEmittingBelow      `json:"below,omitempty"`
}

type agentEmittingPredicate struct {
	Kind      string             `json:"kind"`
	Anchor    string             `json:"anchor,omitempty"`
	Detail    string             `json:"detail,omitempty"`
	Evaluated bool               `json:"evaluated"`
	Held      bool               `json:"held"`
	Region    *agentEmittingSpan `json:"region,omitempty"`
}

type agentEmittingBelow struct {
	Anchor      string   `json:"anchor"`
	Glyphs      []string `json:"glyphs"`
	AnchorBound bool     `json:"anchorBound"`
	Verdict     string   `json:"verdict"`
}

type agentEmittingSpan struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type agentEmittingExtractor struct {
	Name   string                  `json:"name"`
	Anchor string                  `json:"anchor"`
	Region *agentEmittingSpan      `json:"region,omitempty"`
	Rows   []agentEmittingYieldRow `json:"rows"`
}

type agentEmittingYieldRow struct {
	Fields []agentEmittingField `json:"fields"`
}

type agentEmittingField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// handleAgentEmitting answers the list, and the named pane's reading when
// there is one to give.
func (s *WSServer) handleAgentEmitting(_ context.Context, req jsonrpcRequest, r Responder) {
	var p agentEmittingParams
	if msg := decodeParamsStrict(req.Params, &p); msg != "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
		return
	}
	watching := s.paneObserver.Watching()
	out := agentEmittingResult{Panes: make([]agentEmittingPane, 0, len(watching))}
	agent := ""
	found := false
	for _, w := range watching {
		out.Panes = append(out.Panes, agentEmittingPane{SessionID: w.PaneID, Agent: w.Agent})
		if p.SessionID != "" && w.PaneID == p.SessionID {
			agent, found = w.Agent, true
		}
	}
	if found {
		out.Reading = s.readEmittingPane(session.ID(p.SessionID), agent)
	}
	_ = r.TryResult(req.ID, mustMarshal(out))
}

// readEmittingPane builds the reading, and answers nil for every way the pane
// can stop being readable between the list and the frame. None of them is an
// error: an observation closes when its session ends, and a surface polling
// through that moment finds out one answer later rather than being told off
// for asking.
func (s *WSServer) readEmittingPane(sid session.ID, agent string) *agentEmittingRead {
	f, err := s.paneGrid.Frame(string(sid))
	if err != nil {
		return nil
	}
	sess, err := s.registry.Get(sid)
	if err != nil {
		return nil
	}
	ident := sess.Identity()
	e := s.agentRules.Explain(agent, f)
	read := &agentEmittingRead{
		SessionID:    string(sid),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
		Agent:        agent,
		HasRule:      e.HasRule,
		State:        string(e.State),
		Fallback:     string(e.Default),
		Frame:        emittingFrame(f, e.Rows),
		Anchors:      emittingAnchors(e.Anchors),
		Branches:     emittingBranches(e.Branches),
		Extractors:   emittingExtractors(e.Extractors),
	}
	if e.Matched >= 0 {
		matched := e.Matched
		read.MatchedBranch = &matched
	}
	return read
}

// emittingFrame renders the grid column by column, and the rule's own reading
// of each row beside it. The two are one object per row because they are one
// reading of one row and a caller drawing them from two arrays would have to
// keep the indices in step.
func emittingFrame(f panegrid.Frame, rows []agentdriver.RowReading) agentEmittingFrame {
	out := agentEmittingFrame{
		Cols: f.Cols, Rows: f.Rows,
		CursorX: f.CursorX, CursorY: f.CursorY, AltScreen: f.AltScreen,
		Lines: make([]agentEmittingLine, 0, len(f.Lines)),
	}
	for y, line := range f.Lines {
		cells := make([]string, 0, len(line))
		for _, c := range line {
			cells = append(cells, c.Text)
		}
		row := agentEmittingLine{Cells: cells}
		if y < len(rows) {
			row.Rule = rows[y].RuleGlyph
			if rows[y].OpensAt >= 0 {
				at := rows[y].OpensAt
				row.OpensAt = &at
			}
		}
		out.Lines = append(out.Lines, row)
	}
	return out
}

func emittingAnchors(in []agentdriver.AnchorReading) []agentEmittingAnchor {
	out := make([]agentEmittingAnchor, 0, len(in))
	for _, a := range in {
		row := agentEmittingAnchor{Name: a.Name, Kind: a.Kind, From: a.From, Bound: a.Bound}
		if a.Bound {
			at := a.Row
			row.Row = &at
		}
		out = append(out, row)
	}
	return out
}

func emittingBranches(in []agentdriver.BranchReading) []agentEmittingBranch {
	out := make([]agentEmittingBranch, 0, len(in))
	for _, b := range in {
		row := agentEmittingBranch{
			State: string(b.State), Reached: b.Reached, Matched: b.Matched,
			Predicates: make([]agentEmittingPredicate, 0, len(b.Predicates)),
		}
		for _, p := range b.Predicates {
			row.Predicates = append(row.Predicates, agentEmittingPredicate{
				Kind: p.Kind, Anchor: p.Anchor, Detail: p.Detail,
				Evaluated: p.Evaluated, Held: p.Held, Region: emittingSpan(p.Region),
			})
		}
		if b.Below != nil {
			row.Below = &agentEmittingBelow{
				Anchor: b.Below.Anchor, Glyphs: b.Below.Glyphs,
				AnchorBound: b.Below.AnchorBound, Verdict: b.Below.Verdict,
			}
			if row.Below.Glyphs == nil {
				row.Below.Glyphs = []string{}
			}
		}
		out = append(out, row)
	}
	return out
}

func emittingExtractors(in []agentdriver.ExtractorReading) []agentEmittingExtractor {
	out := make([]agentEmittingExtractor, 0, len(in))
	for _, e := range in {
		row := agentEmittingExtractor{
			Name: e.Name, Anchor: e.Anchor, Region: emittingSpan(e.Region),
			Rows: make([]agentEmittingYieldRow, 0, len(e.Rows)),
		}
		for _, captured := range e.Rows {
			// Sorted, because a map's order is random and this is redrawn on
			// every poll: an unsorted list would reshuffle under the person
			// reading it.
			names := make([]string, 0, len(captured))
			for name := range captured {
				names = append(names, name)
			}
			sort.Strings(names)
			fields := make([]agentEmittingField, 0, len(names))
			for _, name := range names {
				fields = append(fields, agentEmittingField{Name: name, Value: captured[name]})
			}
			row.Rows = append(row.Rows, agentEmittingYieldRow{Fields: fields})
		}
		out = append(out, row)
	}
	return out
}

func emittingSpan(s *agentdriver.RowSpan) *agentEmittingSpan {
	if s == nil {
		return nil
	}
	return &agentEmittingSpan{From: s.From, To: s.To}
}

// emittingAvailable gates the method on the whole chain being wired. All three
// or none: a grid with no rules answers a screen and no reading, which is the
// half of the view the design says may not be missing.
func (s *WSServer) emittingAvailable() bool {
	return s.paneGrid != nil && s.paneObserver != nil && s.agentRules != nil
}

// agentEmittingSpecs registers the method. On the ORDINARY lane, not on a
// queue of its own: the handler copies one grid and evaluates one document, it
// touches no store and blocks on nothing, and a person watching a live view
// gets a stale screen the moment it is made to queue behind the domain that
// happens to be busy.
func (s *WSServer) agentEmittingSpecs() []methodSpec {
	return []methodSpec{
		whenAvailable(
			regResponder(s.lane, "agent.emitting", params(validateAgentEmittingRaw), func(r Responder) handlerFunc {
				return func(ctx context.Context, req jsonrpcRequest) { s.handleAgentEmitting(ctx, req, r) }
			}),
			s.emittingAvailable,
			"method not found: pane observation not wired"),
	}
}
