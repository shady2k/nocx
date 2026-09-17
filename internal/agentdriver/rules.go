package agentdriver

// WHERE a rule comes from, as opposed to what one IS (document.go) or which
// ones this build carries (claude.go).
//
// The registry used to be compile-time: the rules were in the binary and a
// person who ran an agent nocx read wrongly could do nothing but wait for a
// release. What this file adds is the seam that makes a rule local
// (nocx-y6w66): the registry asks a source what the person's own documents say
// about an agent, on every lookup, and answers with what it finds.
//
// # Why the question is asked per lookup rather than once
//
// A rule arrives from a file a person edits while the pane it reads is open.
// Caching the answer at process start would make every edit a restart, and
// caching it at first use would make the pane that happened to be watched
// first the one that decides which rule the rest of the session runs under.
// The lookup is a lock and a map read against a classification that binds
// anchors and walks branches over a screenful of rows, and the source is
// read-only on the classify path, so the cost is the cheap half.
//
// # Shipped rules are NOT written to disk, and that is why the source is not
// # the whole answer
//
// The build's rules stay in the binary. The source answers about the person's
// documents only, and an agent they never touched falls through to the shipped
// driver this registry was built with — so an upgrade that ships a better
// claude rule improves an install nobody edited, and leaves one that was
// edited exactly where the person put it. If the source owned the shipped
// half, either the text would have to be copied to disk on first run (after
// which no upgrade reaches it) or the source would have to be rebuilt from the
// registry on every start (which is the same thing with more steps).

import (
	"encoding/json"
	"fmt"
	"sort"
)

// RuleState is what the person's own documents say about ONE agent, as the
// registry needs it: the rule they wrote if there is a usable one, and
// otherwise why there is not.
//
// It is three facts rather than a (Driver, bool) pair because the two ways of
// having no rule are not the same answer. "They wrote none" leaves the shipped
// rule reading the pane; "they switched detection off" and "what they wrote
// does not compile" both silence it, and a pane that is silent reports
// StateUnknown, which every caller treats as busy. A pair would collapse the
// first into the second, so a person's first edit could stop nocx reading an
// agent it reads correctly today.
type RuleState struct {
	// Driver is the rule the person wrote, already compiled. Nil when they
	// wrote none — and also when what they wrote does not compile, which is
	// what Broken says.
	Driver Driver
	// Off is true when they switched detection off for this agent.
	Off bool
	// Broken is true when a document of theirs is on disk and could not be
	// used: it does not parse, or it does not compile as a rule. A hand-edited
	// file is the expected way to reach this, so it is a state and not an
	// error a caller has to remember to handle — and it resolves to unknown
	// rather than to the shipped rule, because a document that silently
	// stands aside for the build's own is a document whose author is told
	// their edit took effect when it did not.
	Broken bool
}

// RuleSource is where a person's own rule documents are read from (AD-8). One
// implementation lives in internal/agentrule; a registry built without one
// answers from what it was constructed with, which is what every test wants.
//
// It is asked about agents the registry already knows, so a source may answer
// about exactly those and need not carry a set of its own.
type RuleSource interface {
	RuleState(agent string) RuleState
}

// ruleSourceBox gives the atomic pointer a concrete type to hold. The source
// is an interface and atomic.Pointer needs a pointer to a type, so one word of
// wrapper buys a field that can be attached from the composition root while
// panes are being classified — a race a plain field would have the moment
// anybody set it later than construction.
type ruleSourceBox struct{ src RuleSource }

// SetRuleSource attaches the person's own rules, and REPLACES any source
// already attached. nil detaches, which is the state a registry built in a
// test is in.
func (r *Registry) SetRuleSource(src RuleSource) {
	if src == nil {
		r.rules.Store(nil)
		return
	}
	r.rules.Store(&ruleSourceBox{src: src})
}

// ruleSource is the attached source, or nil.
func (r *Registry) ruleSource() RuleSource {
	if box := r.rules.Load(); box != nil {
		return box.src
	}
	return nil
}

// Documented is a driver whose rule is a document — which is every driver this
// package builds. It is how the shipped rule for an agent is read back OUT of
// the registry: a surface that shows a person what they are editing, and a
// delete that restores the shipped rule, both need the document and not merely
// the compiled driver.
type Documented interface {
	Driver
	// Rule returns the document this driver evaluates.
	Rule() Document
}

// Rule returns the document this driver evaluates.
func (d documentDriver) Rule() Document { return d.doc }

// Agents lists the agents this build ships a rule for, sorted. It is the set a
// person may write a rule for: a user rule REPLACES a shipped one, and there
// is nothing to replace for an agent this build never learned to read. So nocx
// does not take on new agents through this seam — that is what a driver is.
func (r *Registry) Agents() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byAgent))
	for agent := range r.byAgent {
		out = append(out, agent)
	}
	sort.Strings(out)
	return out
}

// ShippedRules returns the documents this build carries, sorted by agent. It
// is what a person starts editing from and what deleting their rule restores,
// and it is a copy: the registry's own drivers never change.
func (r *Registry) ShippedRules() []Document {
	if r == nil {
		return nil
	}
	out := make([]Document, 0, len(r.byAgent))
	for _, agent := range r.Agents() {
		d, ok := r.byAgent[agent]
		if !ok {
			continue
		}
		doc, ok := d.(Documented)
		if !ok {
			continue
		}
		out = append(out, doc.Rule())
	}
	return out
}

// Compile parses a rule document and compiles it into a driver. It is the
// write path's validator and the read path's builder in one, because they are
// one question: a document that Compile refuses is a document that could never
// answer, and every refusal here is reported to whoever supplied the bytes
// rather than swallowed until the first frame that finds it silent.
//
// It is the same construction the embedded rules go through at package init —
// there is one reader of this grammar, and a rule a person writes is held to
// exactly what the shipped ones are.
func Compile(raw []byte) (Driver, error) {
	return parseDocument(raw)
}

// parseDocument is Compile behind the type the package's own rules are stored
// as, so that the panic path and the error path cannot drift.
func parseDocument(raw []byte) (documentDriver, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return documentDriver{}, fmt.Errorf("agentdriver: rule document does not parse: %w", err)
	}
	d, err := newDocumentDriver(doc)
	if err != nil {
		return documentDriver{}, err
	}
	return d, nil
}
