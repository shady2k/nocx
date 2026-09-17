// Package agentrule stores a person's OWN rule for an agent: one document per
// agent, under the app directory, readable and editable by hand as well as
// from Settings (nocx-y6w66).
//
// # What this package is the owner of
//
// internal/agentdriver owns what a rule IS (the document grammar and the
// evaluator) and which ones this build SHIPS. This package owns the one thing
// that is neither: where a rule a person wrote lives, and what it means when
// there is one. It answers two readers with one derivation — the registry asks
// RuleState on every lookup, and the Settings surface asks Rules for the whole
// set — so a pane and the page describing it cannot disagree about which rule
// is in force.
//
// # The four things an agent can be, and why there are four
//
// SHIPPED: the build's rule reads the pane. Nothing of the person's is in the
// way, which is every agent on every install nobody has edited yet.
//
// USER: their rule reads the pane. It REPLACES the shipped one for that agent
// entirely — never a field-by-field merge, which would be two owners of one
// decision (AD-8).
//
// DISABLED: they switched detection off. The pane reports unknown, so nocx does
// not type into it — and this is NOT deleting: their document, if they wrote
// one, is still on disk, and switching detection back on restores exactly it.
//
// UNREADABLE: a document of theirs is on disk and cannot be used. Also unknown
// rather than the shipped rule: a file that silently stood aside for the
// build's own would tell its author their edit took effect when it did not.
//
// # Where the documents are, and why that directory
//
// `<config>/agent-rules/<agent>.json`, under the directory the BUILD chooses
// (internal/storage/appdir.go). A dev stand keeps its own rules and the
// installed app keeps its own, so nothing an e2e run or `make dev` writes can
// reach the rules a person depends on. The directory is named rather than
// derived from an environment variable for the reason that file gives: there
// is nothing to pass and nothing to forget.
//
// # Shipped rules are never written to disk
//
// Not on first run, not when a person switches detection off, not ever. That
// is the property that makes an upgrade useful: a newly shipped claude rule
// reaches an install nobody edited, and leaves one that was edited alone.
// Writing the shipped rule into the person's document would make the file they
// never touched the thing that pins them to an old rule.
package agentrule

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/shady2k/nocx/internal/agentdriver"
)

// DirName is the directory under the app's config directory that holds these
// documents. It is exported because the Settings surface says where they are:
// a person who can edit a rule by hand needs the path, and telling them a
// second copy of it from the renderer is how the two come to disagree.
const DirName = "agent-rules"

// documentVersion is this document's schema version. The storage package's
// protocol, one version per module (ADR-0011 §6): a document written by a
// NEWER nocx is refused rather than half-understood, and 0 means "no version
// on disk", which is what a person writing one by hand produces.
const documentVersion = 1

// document is one agent's file on disk. The person's rule is held as raw JSON
// so that what they wrote is what comes back — a rule re-marshalled from the
// parsed form would come back with its own field spelling, and this file is
// meant to be edited by hand.
type document struct {
	Version int `json:"version"`
	// Enabled is a POINTER, and the pointer is the whole of the trick: absent
	// must mean ON. A plain bool would make a hand-written document that
	// carries only a rule — the shortest correct file there is — switch
	// detection off by omission.
	Enabled *bool `json:"enabled,omitempty"`
	// Rule is the person's own rule. Absent means "use the shipped one",
	// which is what a document that only switches detection off carries.
	Rule json.RawMessage `json:"rule,omitempty"`
}

// enabled reports whether detection is on. Absent is on.
func (d document) enabled() bool { return d.Enabled == nil || *d.Enabled }

// agentName bounds what may become a FILE name. The names come from the
// drivers this build carries, and one that walks out of the app directory is a
// wiring mistake with a filesystem behind it — so it is refused where it
// enters, at construction, and never again. Leading dots are out for the same
// reason the directory is named rather than hidden: a rule nobody can see is a
// rule nobody can repair by hand, which is most of why it is a file.
var agentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// State is where the rule reading an agent's pane comes from, and whether
// there is one at all. The set is closed for the reason agentdriver.State's
// is: a value nobody wrote a branch for would land in whichever branch was
// written last, and one of these branches is "nocx may not read this pane".
type State string

const (
	// StateShipped means this build's rule reads the pane.
	StateShipped State = "shipped"
	// StateUser means the person's own rule reads the pane, in place of the
	// shipped one.
	StateUser State = "user"
	// StateDisabled means they switched detection off: the pane reports
	// unknown, and deleting the document returns the shipped rule.
	StateDisabled State = "disabled"
	// StateUnreadable means a document of theirs exists and could not be
	// used. The pane reports unknown, the same failing-closed direction.
	StateUnreadable State = "unreadable"
)

// States lists the closed set, in the order the package comment introduces it.
func States() []State {
	return []State{StateShipped, StateUser, StateDisabled, StateUnreadable}
}

// Valid reports whether s is in the closed set. What crosses a boundary is a
// string, and this is where a string stops being one.
func (s State) Valid() bool {
	for _, k := range States() {
		if s == k {
			return true
		}
	}
	return false
}

// Enabled reports whether detection is reading the pane in this state. It is a
// PREDICATE over the closed set and not a field beside it, so a switch cannot
// come to disagree with the state the page was drawn from.
func (s State) Enabled() bool { return s == StateShipped || s == StateUser }

// Entry is one agent's detection as a surface reads it: what is in force, the
// text of the rule as it stands, and — when there is a problem — why.
type Entry struct {
	Agent string
	State State
	// Document is the rule text as it stands: the person's own document when
	// they wrote one, and the SHIPPED rule's text when they did not, so that
	// the surface has something to edit from on an install nobody has touched.
	//
	// EMPTY in StateUnreadable, deliberately. The text on disk is not readable
	// and the shipped rule is not what is in force, so a surface filled with
	// either would invite a save that replaces a file the person cannot see.
	Document string
	// Problem is why their document could not be used, in a clause a person
	// can act on. Empty in every other state.
	Problem string
}

// state is one agent's half of the store, as loaded.
type state struct {
	// shipped is this build's rule for the agent, as text.
	shipped string
	// raw is the person's own document, nil when they wrote none.
	raw json.RawMessage
	// enabled is the document's switch, absent meaning on.
	enabled bool
	// driver is raw compiled, nil when there is no usable rule of theirs.
	driver agentdriver.Driver
	// problem is why raw could not be used. Empty when it could.
	problem string
	// where is the derived state both readers answer from.
	where State
}

// classify is the ONE place a state is decided. Both readers project this
// value, so the pane and the page cannot come to disagree about which rule is
// in force.
//
// The order is the design's: a document that cannot be read is unreadable
// whatever it says, because nothing else in it can be believed; off beats a
// rule, because switching detection off is not a way of editing one; and a
// rule of theirs beats the build's.
func classify(st state) State {
	switch {
	case st.problem != "":
		return StateUnreadable
	case !st.enabled:
		return StateDisabled
	case st.raw != nil:
		return StateUser
	default:
		return StateShipped
	}
}

// clause reduces a refusal to the half a person needs. The packages here write
// their refusals as clauses already ("document default \"x\" is not a state")
// and prefix them with the package they came from, which is our vocabulary and
// not the reader's.
func clause(err error) string {
	return strings.TrimPrefix(err.Error(), "agentdriver: ")
}
