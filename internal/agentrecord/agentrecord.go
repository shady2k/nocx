// Package agentrecord is the one description of an agent nocx runs: what
// launches it, what its conversation resumes under, and what a person may
// change about it (nocx-t5e7d, the launch half of the 2026-08-15 orchestration
// spec's §5).
//
// # What this package is the owner of, and what it is not
//
// internal/agentdriver owns what a RULE is and which rules this build ships;
// internal/agentrule owns where a person's own rule lives. Neither knows how
// to START an agent, and until now nothing did: the command an agent is
// launched with lived in whoever asked for the launch — the coordinator's own
// line for a worker (workers.spawn's `command`), the shell integration's one
// `claude` wrapper for a person typing at a pane. So one agent was described
// twice, in two vocabularies that had to be kept in step by hand, and an agent
// nocx does not ship could not be described at all.
//
// This package owns that description and nothing else. One record per agent:
// its id — the name the enrolment act carries, which is how every other
// package names the same agent — its display name, the command, its arguments,
// its environment lines, its icon and colour, whether it is one this build
// SHIPS (editable, never removable), whether the person switched it off, and
// the three resume shapes: mint a session id, continue a minted one, continue
// the launch directory's most recent one.
//
// Deliberately not here: the detection rule (that is a rule document, and
// nocx-y6w66 owns the file it lives in), calibration, placeholder expansion
// ({UUID} and its siblings are nocx-bag8j's decision, made at spawn over a
// tokenized argv), which of the resume shapes a given launch should use (that
// is nocx-2txuc's, decided by where the task lives rather than by what the
// agent prefers), and the argv a launch is built into (nocx-xn63t.5.2). This
// package holds what those questions are answered FROM, and answers none of
// them itself.
//
// # Shipped defaults are embedded and never written to disk
//
// Not on first run, not ever. That is the whole point of the split and it is
// the property nocx-dz9vj's falsifier names: a default written into the
// person's own file would make the file they never touched the thing that pins
// them to an old description, so an upgrade could never improve an agent
// nobody had edited. What is on disk is exactly what the person wrote, and a
// document that is not there means "the build's record, unchanged".
//
// # A person's document REPLACES the shipped record, and never merges
//
// The same decision internal/agentrule made for a rule, for the same reason: a
// field-by-field merge is two owners of one decision, and the one that wins is
// whichever the reader happens to apply last. A person editing an argument
// writes the whole record — which is what the shipped default already is, in
// the same shape, in the same place a surface shows it — and from then on the
// build's copy of that agent reaches nobody. It is also what makes the two
// halves of the acceptance criterion one behaviour rather than two: an upgrade
// improves an agent nobody touched (the shipped default is what is read), and
// leaves an edited one alone (their file is what is read).
//
// # The id is the file name
//
// `<config>/agents/<id>.json`, under the directory the BUILD chooses
// (internal/storage/appdir.go), so a dev stand keeps its own agents and the
// installed app keeps its own. A record document carries no `id` field: the
// file's own name is the agent, which is one fewer thing that can disagree
// with itself, and a name that could walk out of the app directory is refused
// where it enters (storage.ValidDocumentName).
package agentrecord

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/workers"
)

// DirName is the directory under the app's config directory that holds these
// documents. It is exported for the reason agentrule.DirName is: the surface
// that shows a person where their agents live has to say the same path, and a
// second spelling of it in the renderer is how the two come to disagree.
const DirName = "agents"

// documentVersion is this document's schema version. The storage package's
// protocol, one version per module (ADR-0011 §6): a document written by a
// NEWER nocx is refused rather than half-understood, and 0 means "no version
// on disk", which is what a person writing one by hand produces.
const documentVersion = 1

//go:embed agents/*.json
var shippedFS embed.FS

// Resume is HOW this agent's conversation is continued: the three argv shapes
// the 2026-08-15 spec's §5 named, in the record rather than in the code that
// launches. Each is a template — `--resume` and the id it continues are two
// elements of one argv, and a placeholder in either is expanded at spawn
// (nocx-bag8j) rather than here.
//
// The three are ARGS and not a list of mode names, and the difference is the
// whole reason this type has no `modes` field: a record that named its modes
// beside its arguments is a record that can say it resumes by id while
// carrying nothing to resume with, and the two would be one edit apart at all
// times. Supports derives the modes from what is actually there.
type Resume struct {
	// SessionIDArgs mints a session id when the agent starts, which is what
	// lets a launch be resumed BY ID afterwards. Empty means this agent
	// cannot be given an identity it is asked to use.
	SessionIDArgs []string `json:"sessionIdArgs,omitempty"`
	// ResumeIDArgs continues an explicitly named conversation. It is what a
	// SHARED checkout needs, where "the most recent session here" would pick
	// up a conversation belonging to another task (nocx-2txuc).
	ResumeIDArgs []string `json:"resumeIdArgs,omitempty"`
	// ResumeCwdArgs continues the agent's own most recent session for its
	// launch directory, which is correct in a worktree because each tree has
	// its own directory (nocx-2txuc).
	ResumeCwdArgs []string `json:"resumeCwdArgs,omitempty"`
}

// Supports reports whether this agent's record declares the args a resume in
// this mode would be built from. An unknown mode is not supported, and neither
// is ResumeNone — see Modes for why the second of those is the honest answer
// rather than an omission.
func (r Resume) Supports(mode workers.ResumeMode) bool {
	switch mode {
	case workers.ResumeByID:
		return len(r.ResumeIDArgs) > 0
	case workers.ResumeByCwd:
		return len(r.ResumeCwdArgs) > 0
	default:
		// ResumeNone, and every mode a newer build might name: an identity
		// that asks for no conversation is not one there is an invocation
		// for, and a mode this build does not know is not its to interpret.
		return false
	}
}

// Modes lists the resume modes this record declares, in the order the type
// introduces them, so a refusal can name what WAS available rather than only
// what was not — the same reason WorkerCoordinator.Environments exists.
//
// ResumeNone is deliberately absent: it is a fact a person may record about an
// agent ("this one cannot resume") and not a way this record resumes one, so
// listing it here would advertise an invocation that does not exist.
func (r Resume) Modes() []workers.ResumeMode {
	out := make([]workers.ResumeMode, 0, 2)
	if r.Supports(workers.ResumeByID) {
		out = append(out, workers.ResumeByID)
	}
	if r.Supports(workers.ResumeByCwd) {
		out = append(out, workers.ResumeByCwd)
	}
	return out
}

// Document is a record as it is written: everything about an agent that a
// person or the build may state, and nothing the build derives.
//
// It carries no `id`, because the file's own name is the agent (see the
// package comment), and no `builtin`, because whether an agent is one this
// build ships is a fact about the build rather than a claim a document gets to
// make. Record is what a reader is handed and it adds both.
type Document struct {
	// Version is this document's schema version. Absent (0) is what a person
	// writing one by hand produces, and it is the oldest shape rather than an
	// error.
	Version int `json:"version,omitempty"`
	// DisplayName is what a surface calls this agent. Empty means the build
	// has nothing nicer to say than the id, which is a surface's problem and
	// not a validity one.
	DisplayName string `json:"displayName,omitempty"`
	// Command is what is exec'd — a program, not a shell line. It is a
	// separate field from the id on purpose and the difference is
	// load-bearing: the id is the name the ENROLMENT act carries (ADR-0024
	// decision 2) and the command is the binary, so a person whose claude is
	// a wrapper, a versioned path or an alias can be orchestrated at all.
	Command string `json:"command"`
	// Args is the record's own arguments for that command, before the
	// person's own or the caller's.
	Args []string `json:"args,omitempty"`
	// Icon and Colour are what a surface draws beside the agent. Held here
	// because they are the agent's rather than the surface's: a second copy
	// in the renderer would be the same agent wearing two identities.
	Icon   string `json:"icon,omitempty"`
	Colour string `json:"colour,omitempty"`
	// Disabled means the person switched this agent off. It is an OFFERING
	// fact and not a launch one: nothing in this package refuses a launch
	// over it, because a record that stopped describing a running worker's
	// agent would be a record whose own value changed what already happened.
	Disabled bool `json:"disabled,omitempty"`
	// Env is environment lines — "KEY=VALUE" — set for everything this agent
	// launches.
	Env []string `json:"env,omitempty"`
	// Resume is the three shapes above. Absent is legitimate and means this
	// agent does not resume at all.
	Resume Resume `json:"resume"`
}

// Record is one agent as a reader gets it: the document, plus the two facts
// only the build can answer.
type Record struct {
	// ID is the agent's name, which is also the document's file name and the
	// name the enrolment act carries. It is derived from the file rather than
	// read from it, so there is no way for a document to describe one agent
	// under another's name.
	ID string
	// Builtin is whether this build SHIPS this agent. A builtin agent is
	// editable and never removable: deleting the person's document puts the
	// shipped record back rather than taking the agent away, because the
	// agent is part of the build and the file was only ever their edit of it.
	Builtin bool
	Document
}

// validate refuses a record that could never be launched. It runs on the
// SHIPPED defaults at construction and on every document a person wrote, so a
// broken record is reported while its author is looking at it rather than at
// the first launch that reads it.
//
// It is deliberately about what the record SAYS rather than about what a
// launch needs: a record with no resume args is a complete record for an agent
// that does not resume, and refusing it would make the honest answer
// unrepresentable.
func (d Document) validate() error {
	if strings.TrimSpace(d.Command) == "" {
		return fmt.Errorf("it names no command, so there is nothing to launch")
	}
	if strings.ContainsAny(d.Command, "\n\r") {
		return fmt.Errorf("its command carries a line break, and a command is a program rather than a shell script")
	}
	if err := noEmptyElements("argument", d.Args); err != nil {
		return err
	}
	for i, a := range d.Args {
		if strings.ContainsAny(a, "\n\r") {
			return fmt.Errorf("argument %d carries a line break", i+1)
		}
	}
	for i, e := range d.Env {
		if !isEnvLine(e) {
			return fmt.Errorf("environment line %d is %q, and every line is KEY=VALUE with a non-empty key", i+1, e)
		}
	}
	if err := d.Resume.validate(); err != nil {
		return err
	}
	return nil
}

// isEnvLine reports whether one entry is a KEY=VALUE line with a key in it. A
// line with no `=` sets nothing and a line with an empty key is a name no
// program can read, so both are refused rather than passed to a launcher that
// would drop them.
func isEnvLine(line string) bool {
	key, _, ok := strings.Cut(line, "=")
	return ok && key != ""
}

// validate refuses a resume template that could never be expanded into an
// argument list: an empty element is the one shape that cannot be anything but
// a typo, because argv has no way to express "nothing here".
func (r Resume) validate() error {
	for _, shape := range []struct {
		name string
		args []string
	}{
		{"sessionIdArgs", r.SessionIDArgs},
		{"resumeIdArgs", r.ResumeIDArgs},
		{"resumeCwdArgs", r.ResumeCwdArgs},
	} {
		if err := noEmptyElements(shape.name, shape.args); err != nil {
			return err
		}
	}
	return nil
}

// noEmptyElements refuses a template whose element is the empty string. An
// empty ARGUMENT is legal in argv and is nevertheless a typo in a record: argv
// expresses "nothing here" with no element at all, so an empty one is a
// document somebody mis-wrote, and a launch built from it would pass a
// different argument list than its author meant.
func noEmptyElements(name string, args []string) error {
	for i, a := range args {
		if a == "" {
			return fmt.Errorf("%s %d is empty, and argv carries an empty argument only when its author meant to", name, i+1)
		}
	}
	return nil
}

// shippedRecords is this build's own set, read out of the binary once.
//
// It is a package-level value rather than something New computes, because a
// shipped record that cannot be used is a WIRING mistake and a wiring mistake
// belongs to process start: loadShipped panics, so a build whose own defaults
// are broken fails at init rather than at the first person who launches that
// agent. It is the same shape and the same reason as agentdriver's own shipped
// rules.
var shippedRecords = loadShipped()

// loadShipped reads this build's own records out of the binary, keyed by id,
// and refuses — by PANIC, once, at init — anything it could not use.
func loadShipped() map[string]Record {
	entries, err := shippedFS.ReadDir("agents")
	if err != nil {
		panic(fmt.Sprintf("agentrecord: the shipped agent records are not in the binary: %v", err))
	}
	out := make(map[string]Record, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !storage.ValidDocumentName(id) {
			panic(fmt.Sprintf("agentrecord: %q is not a name this package can keep a document under", id))
		}
		raw, err := shippedFS.ReadFile(path.Join("agents", name))
		if err != nil {
			panic(fmt.Sprintf("agentrecord: the shipped record for %q could not be read: %v", id, err))
		}
		var doc Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			panic(fmt.Sprintf("agentrecord: the shipped record for %q does not parse: %v", id, err))
		}
		if err := doc.validate(); err != nil {
			panic(fmt.Sprintf("agentrecord: the shipped record for %q is not usable: %v", id, err))
		}
		out[id] = Record{ID: id, Builtin: true, Document: doc}
	}
	if len(out) == 0 {
		panic("agentrecord: no shipped agent records, and a build that describes no agent can launch none")
	}
	return out
}
