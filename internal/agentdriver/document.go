package agentdriver

// A rule as a document: anchors bound from the frame's own chrome, then an
// ordered list of branches over the closed predicate set of predicate.go.
//
// # Why a document
//
// The classifier used to be Go, so the only way to fix an agent whose TUI
// changed was to ship a new nocx. Splitting it puts the PREDICATES in Go,
// where their bounds are enforced, and the COMPOSITION in a document, where a
// person can repair it. A person composes; a person cannot invent a predicate
// and cannot lift a bound one enforces.
//
// # Order is a safety property
//
// Branches are evaluated in document order and the first match wins. The
// dialog branches come before the free-text branch so a dialog can never be
// masked by an input box drawn beneath it. An evaluator that reordered for any
// reason — efficiency, tidiness, a map — would be this file's defect.
//
// # No memory
//
// Evaluation reads the frame it is given and nothing else, because Driver's
// contract says a rule that remembers is a rule that can be stuck.

import (
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/panegrid"
)

// Document is one agent's rule.
type Document struct {
	Agent string `json:"agent"`
	// Anchors bind in order; a later one may refer to an earlier one.
	Anchors []AnchorSpec `json:"anchors"`
	// Branches are evaluated in order. First match wins.
	Branches []Branch `json:"branches"`
	// Default is the answer when no branch matched. It is a field rather
	// than a constant because a rule the engine does not understand should
	// be able to end in unknown, and free_text is the expensive direction.
	Default State `json:"default"`
}

// AnchorSpec binds a name to a row of the frame. A binding that fails is not
// an error: the anchor is ABSENT, and a predicate may ask about that.
type AnchorSpec struct {
	Name string `json:"name"`
	// Kind is "searchUp", "offset" or "firstNonBlankBelow".
	Kind string `json:"kind"`
	// From names the anchor this one is computed from. Empty means the
	// frame's own bottom edge, which only searchUp uses.
	From string `json:"from,omitempty"`
	// FromOffset shifts the starting row before the search begins.
	FromOffset int `json:"fromOffset,omitempty"`
	// Floor is the lowest row a searchUp may reach, inclusive.
	Floor int `json:"floor,omitempty"`
	// Offset is the delta for kind "offset".
	Offset int `json:"offset,omitempty"`
	// RuleGlyph, when set, makes searchUp look for a full-width rule of it.
	RuleGlyph string `json:"ruleGlyph,omitempty"`
	// MinRow rejects a binding whose row is below it. Zero means no floor.
	MinRow int `json:"minRow,omitempty"`
	// RequireCell, when set, rejects the binding unless the bound row
	// carries this text at RequireCol exactly.
	RequireCell string `json:"requireCell,omitempty"`
	RequireCol  int    `json:"requireCol,omitempty"`
	// RequireBound names anchors that must have bound for this one to bind.
	// It exists because a compound piece of chrome binds or fails WHOLE: an
	// input box whose prompt marker is missing is not an input box, and
	// every anchor derived from it must go absent together. Without this a
	// later anchor computed from an earlier step of the same structure would
	// survive its own structure's rejection.
	RequireBound []string `json:"requireBound,omitempty"`
}

// Branch is one rule of the ordered list. A branch is either a conjunction of
// predicates answering one state, or a three-valued Below switch.
type Branch struct {
	State State  `json:"state,omitempty"`
	When  []Pred `json:"when,omitempty"`
	Below *Below `json:"below,omitempty"`
}

// Below is the three-valued predicate, and the three cases are why it is not
// in When. All rows recognised answers AllMatched; a counterexample answers
// Counterexample; nothing drawn there at all falls through to the next branch.
// Collapsing the middle into the last turns a refusal into free_text.
type Below struct {
	Anchor         string   `json:"anchor"`
	Glyphs         []string `json:"glyphs"`
	AllMatched     State    `json:"allMatched"`
	Counterexample State    `json:"counterexample"`
}

// Pred is one predicate invocation. Kind selects which; the rest are its
// arguments, and an argument a kind does not use is ignored.
type Pred struct {
	Kind   string `json:"kind"`
	Anchor string `json:"anchor,omitempty"`
	Glyph  string `json:"glyph,omitempty"`
	Text   string `json:"text,omitempty"`
	Suffix string `json:"suffix,omitempty"`

	// Region arguments, for kind "regionAny".
	Up          bool `json:"up,omitempty"`
	MaxRows     int  `json:"maxRows,omitempty"`
	Col0Only    bool `json:"col0Only,omitempty"`
	StopAtBlank bool `json:"stopAtBlank,omitempty"`
}

// documentDriver is a Driver whose Classify is an evaluation of a Document.
type documentDriver struct{ doc Document }

func (d documentDriver) Agent() string { return d.doc.Agent }

// bound is the anchor table for one frame. Absent names are simply missing,
// which is what makes "did this bind" askable.
type bound map[string]int

// Classify evaluates the document against one frame.
func (d documentDriver) Classify(f panegrid.Frame) State {
	// The degenerate frame is the engine's, not a branch: a document cannot
	// express "there is no grid to read", and should not have to.
	if f.Rows <= 0 || f.Cols <= 0 || len(f.Lines) == 0 {
		return StateUnknown
	}
	anchors := d.bindAnchors(f)
	for _, b := range d.doc.Branches {
		if b.Below != nil {
			switch belowAnchorOpensOnlyWith(f, anchors[b.Below.Anchor], b.Below.Glyphs) {
			case belowAllMatched:
				if _, ok := anchors[b.Below.Anchor]; ok {
					return b.Below.AllMatched
				}
			case belowCounterexample:
				if _, ok := anchors[b.Below.Anchor]; ok {
					return b.Below.Counterexample
				}
			}
			continue
		}
		if allHold(f, anchors, b.When) {
			return b.State
		}
	}
	return d.doc.Default
}

func allHold(f panegrid.Frame, anchors bound, preds []Pred) bool {
	for _, p := range preds {
		if !holds(f, anchors, p) {
			return false
		}
	}
	return true
}

// holds evaluates one predicate. A predicate naming an anchor that did not
// bind answers false, EXCEPT aboveAnchorIfBound, whose whole purpose is the
// other answer.
func holds(f panegrid.Frame, anchors bound, p Pred) bool {
	switch p.Kind {
	case "cursorOn":
		return cursorOn(f, p.Glyph)
	case "cursorOpensItsRow":
		col, ok := firstNonBlankCol(f, f.CursorY)
		return ok && col == f.CursorX
	case "numberedOptionAfterCursor":
		return numberedOption(rowTextFrom(f, f.CursorY, f.CursorX+1))
	case "anchorUnbound":
		_, ok := anchors[p.Anchor]
		return !ok
	case "cursorAboveAnchorIfBound":
		row, ok := anchors[p.Anchor]
		if !ok {
			return true
		}
		return f.CursorY < row
	case "nearestNonBlankAboveCursorContains":
		text, ok := nearestNonBlankAbove(f, f.CursorY)
		return ok && strings.Contains(text, p.Text)
	case "rowContains":
		row, ok := anchors[p.Anchor]
		if !ok {
			return false
		}
		return rowContains(f, row, p.Text)
	case "regionAny":
		row, ok := anchors[p.Anchor]
		if !ok {
			return false
		}
		r := region{anchor: row, up: p.Up, maxRows: p.MaxRows, col0Only: p.Col0Only, stopAtBlank: p.StopAtBlank}
		return r.anyRow(f, func(text string) bool {
			if p.Text != "" && !strings.Contains(text, p.Text) {
				return false
			}
			if p.Suffix != "" && !strings.HasSuffix(text, p.Suffix) {
				return false
			}
			return true
		})
	}
	return false
}

func (d documentDriver) bindAnchors(f panegrid.Frame) bound {
	anchors := make(bound, len(d.doc.Anchors))
	for _, a := range d.doc.Anchors {
		row, ok := bindOne(f, anchors, a)
		if !ok {
			continue
		}
		anchors[a.Name] = row
	}
	return anchors
}

func bindOne(f panegrid.Frame, anchors bound, a AnchorSpec) (int, bool) {
	for _, need := range a.RequireBound {
		if _, ok := anchors[need]; !ok {
			return 0, false
		}
	}
	start := f.Rows - 1
	if a.From != "" {
		row, ok := anchors[a.From]
		if !ok {
			return 0, false
		}
		start = row
	}
	switch a.Kind {
	case "searchUp":
		found := -1
		for y := start + a.FromOffset; y >= a.Floor; y-- {
			if fullWidthRule(f, y, a.RuleGlyph) {
				found = y
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		return guard(f, found, a)
	case "offset":
		return guard(f, start+a.Offset, a)
	case "firstNonBlankBelow":
		row, ok := firstNonBlankRowBelow(f, start)
		if !ok {
			return 0, false
		}
		return guard(f, row, a)
	}
	return 0, false
}

func guard(f panegrid.Frame, row int, a AnchorSpec) (int, bool) {
	if row < 0 || row >= f.Rows {
		return 0, false
	}
	if row < a.MinRow {
		return 0, false
	}
	if a.RequireCell != "" && !cellAtCol(f, row, a.RequireCol, a.RequireCell) {
		return 0, false
	}
	return row, true
}

// validate refuses a document that could never answer, at construction. These
// are wiring mistakes and belong to process start, like NewRegistry's.
func (d Document) validate() error {
	if d.Agent == "" {
		return fmt.Errorf("agentdriver: document names no agent, so nothing could ever look it up")
	}
	if !d.Default.Valid() {
		return fmt.Errorf("agentdriver: document default %q is not a state", d.Default)
	}
	seen := make(map[string]bool, len(d.Anchors))
	for _, a := range d.Anchors {
		if a.Name == "" {
			return fmt.Errorf("agentdriver: an anchor has no name")
		}
		if a.From != "" && !seen[a.From] {
			return fmt.Errorf("agentdriver: anchor %q is computed from %q, which is not bound before it", a.Name, a.From)
		}
		for _, need := range a.RequireBound {
			if !seen[need] {
				return fmt.Errorf("agentdriver: anchor %q requires %q, which is not bound before it", a.Name, need)
			}
		}
		seen[a.Name] = true
	}
	for i, b := range d.Branches {
		if b.Below != nil {
			if !seen[b.Below.Anchor] {
				return fmt.Errorf("agentdriver: branch %d reads below %q, which no anchor binds", i, b.Below.Anchor)
			}
			if !b.Below.AllMatched.Valid() || !b.Below.Counterexample.Valid() {
				return fmt.Errorf("agentdriver: branch %d names a state that does not exist", i)
			}
			continue
		}
		if !b.State.Valid() {
			return fmt.Errorf("agentdriver: branch %d answers %q, which is not a state", i, b.State)
		}
		for _, p := range b.When {
			if p.Anchor != "" && !seen[p.Anchor] {
				return fmt.Errorf("agentdriver: branch %d names anchor %q, which no anchor binds", i, p.Anchor)
			}
		}
	}
	return nil
}
