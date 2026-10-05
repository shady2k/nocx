package agentrecord

// The agent record (nocx-t5e7d). What is proved here is the property the
// acceptance criterion is about rather than the shape of the code: that a
// shipped default never reaches the disk, that a person's own document is what
// is read, that removing it puts the shipped values back, and that the record
// is what answers whether an agent can be launched — including for a name nocx
// has never seen, which is the question the restart record could not answer
// about itself.
//
// Every state is reached through the PRODUCT's own reader — Store.entry and
// the probe the startup restore calls — and never by reaching into a field the
// product does not read, which would prove the field exists and nothing about
// what nocx does.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/workers"
)

// newStore opens the store over a fresh app directory and answers the
// directory it made as well, because half of these assertions are about what
// is NOT there.
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, filepath.Join(dir, DirName)
}

// writeDocument puts a record document on disk by hand, which is how a person
// writes one: the file IS the record, and nothing in the product wrote it.
func writeDocument(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s.json: %v", id, err)
	}
}

// entry reads one agent the way the product does.
func entry(t *testing.T, s *Store, id string) Entry {
	t.Helper()
	e, ok := s.entry(id)
	if !ok {
		t.Fatalf("entry(%q): nocx has no record of it", id)
	}
	return e
}

// THE FALSIFIER, first: a shipped default that appears on disk on first run is
// the defect this design exists to prevent, because the file a person never
// touched would then be the thing pinning them to an old record.
func TestTheFirstRunWritesNoDocument(t *testing.T) {
	s, dir := newStore(t)

	got, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the agent directory: %v", err)
	}
	if len(got) != 0 {
		names := make([]string, 0, len(got))
		for _, e := range got {
			names = append(names, e.Name())
		}
		t.Fatalf("the first run left %v on disk, and a shipped default on disk is the file an upgrade can never improve", names)
	}

	// Reading is not writing: the shipped record is answerable, and the
	// directory is still empty afterwards.
	e := entry(t, s, "claude")
	if e.State != StateShipped || e.Record.Command != "claude" {
		t.Fatalf("claude = %+v, want the build's own record", e)
	}
	if !e.Record.Builtin {
		t.Fatalf("claude is not builtin, so a surface would offer to remove an agent that is part of the build")
	}
	if again, _ := os.ReadDir(dir); len(again) != 0 {
		t.Fatalf("reading the shipped record wrote %d document(s) to disk", len(again))
	}
}

// An agent nobody edited reads the build's record, and the resume shapes the
// shipped default declares are the ones the record answers with.
func TestAnUntouchedAgentReadsTheBuildsRecord(t *testing.T) {
	s, _ := newStore(t)
	e := entry(t, s, "claude")

	modes := e.Record.Resume.Modes()
	if len(modes) != 2 || modes[0] != workers.ResumeByID || modes[1] != workers.ResumeByCwd {
		t.Fatalf("claude's resume modes = %v, want by-id and by-cwd", modes)
	}
	if e.Record.Resume.Supports(workers.ResumeNone) {
		t.Fatalf("a record reports that it resumes with mode %q, and that mode is the fact that it does not resume", workers.ResumeNone)
	}
	if len(e.Record.Resume.SessionIDArgs) == 0 {
		t.Fatalf("claude's record cannot mint the session id a by-id resume continues: %+v", e.Record.Resume)
	}
	if len(e.Record.Resume.ResumeCwdArgs) == 0 {
		t.Fatalf("claude's record declares no by-cwd resume, which is the mode a worktree launch needs: %+v", e.Record.Resume)
	}
}

// A person's document REPLACES the build's record, and the build's copy
// reaches nobody afterwards. The document's own bytes survive the read, which
// is what "an upgrade never silently rewrites an edited agent" means for the
// file: whatever a later build changes about its default, this file is read.
func TestAPersonsDocumentReplacesTheShippedRecordAndIsNotRewritten(t *testing.T) {
	s, dir := newStore(t)
	written := `{
  "version": 1,
  "command": "/opt/wrapped-claude",
  "args": ["--dangerously-skip-permissions"],
  "resume": {"resumeCwdArgs": ["--continue"]}
}
`
	writeDocument(t, dir, "claude", written)

	e := entry(t, s, "claude")
	if e.State != StateUser {
		t.Fatalf("claude = %q, want the person's own document in force", e.State)
	}
	if e.Record.Command != "/opt/wrapped-claude" {
		t.Fatalf("command = %q, want the person's own; the build's default reached an agent they edited", e.Record.Command)
	}
	if len(e.Record.Args) != 1 || e.Record.Args[0] != "--dangerously-skip-permissions" {
		t.Fatalf("args = %v, want the person's own", e.Record.Args)
	}
	// The build ships a by-id resume for claude and this document declares
	// none: what is read is their document ENTIRELY, because a field-by-field
	// merge would be two owners of one decision.
	if e.Record.Resume.Supports(workers.ResumeByID) {
		t.Fatalf("the build's by-id resume survived a document that declares none: %+v", e.Record.Resume)
	}
	if !e.Record.Builtin {
		t.Fatalf("claude stopped being builtin because a person edited it; builtin is the build's fact and not a document's")
	}

	after, err := os.ReadFile(filepath.Join(dir, "claude.json")) //nolint:gosec // a path this test just wrote under t.TempDir()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != written {
		t.Fatalf("the document came back changed:\n%s\nwant:\n%s", after, written)
	}
}

// Removing the person's document puts the shipped values back, and it is the
// only thing that does: nothing in this package writes a default over a file.
func TestRemovingTheDocumentRestoresTheShippedValues(t *testing.T) {
	s, dir := newStore(t)
	writeDocument(t, dir, "claude", `{"version": 1, "command": "/opt/wrapped-claude"}`)

	if got := entry(t, s, "claude").Record.Command; got != "/opt/wrapped-claude" {
		t.Fatalf("command = %q before the removal, want the person's own", got)
	}
	if err := os.Remove(filepath.Join(dir, "claude.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	e := entry(t, s, "claude")
	if e.State != StateShipped || e.Record.Command != "claude" {
		t.Fatalf("claude = %+v after removing the document, want the shipped record back", e)
	}
}

// An agent this build does not ship is the person's own: it is read, it is not
// builtin, and it did not have to exist in the binary to be launchable.
func TestAnAgentThisBuildDoesNotShipIsThePersonsOwn(t *testing.T) {
	s, dir := newStore(t)
	writeDocument(t, dir, "myagent", `{"version": 1, "command": "my-agent", "args": ["--fast"]}`)

	e := entry(t, s, "myagent")
	if e.State != StateUser || e.Record.Command != "my-agent" {
		t.Fatalf("myagent = %+v, want the person's own record in force", e)
	}
	if e.Record.Builtin {
		t.Fatalf("an agent the person added is builtin, so a surface would refuse to remove it")
	}
}

// A record may be written with NO resume at all, and that is a complete record
// rather than a broken one: an agent that cannot resume is a fact worth
// recording, and refusing it would make the honest answer unrepresentable.
func TestAnAgentThatDoesNotResumeIsACompleteRecord(t *testing.T) {
	s, dir := newStore(t)
	writeDocument(t, dir, "myagent", `{"version": 1, "command": "my-agent"}`)

	e := entry(t, s, "myagent")
	if e.State != StateUser {
		t.Fatalf("state = %q, want a usable record: an agent with no resume args is an agent that does not resume", e.State)
	}
	if modes := e.Record.Resume.Modes(); len(modes) != 0 {
		t.Fatalf("modes = %v, want none", modes)
	}
}

// Every way a document can be unusable lands in ONE state, and that state is
// never the shipped record: standing aside for the build's copy would tell an
// author their edit took effect when it did not, and for a launch that is
// worse than not launching.
func TestADocumentThatCannotBeUsedIsUnreadableAndNeverTheShippedOne(t *testing.T) {
	cases := map[string]string{
		"an emptied file":                 ``,
		"no command":                      `{"version": 1, "args": ["--fast"]}`,
		"a command that is only spaces":   `{"version": 1, "command": "   "}`,
		"an environment line with no key": `{"version": 1, "command": "claude", "env": ["=oops"]}`,
		"an environment line with no =":   `{"version": 1, "command": "claude", "env": ["NOPE"]}`,
		"an argument that is empty":       `{"version": 1, "command": "claude", "args": [""]}`,
		"an empty resume element":         `{"version": 1, "command": "claude", "resume": {"resumeCwdArgs": [""]}}`,
		"a newer version":                 `{"version": 99, "command": "claude"}`,
		"not JSON at all":                 `{`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, dir := newStore(t)
			writeDocument(t, dir, "claude", body)
			e, known := s.entry("claude")
			if !known {
				t.Fatalf("the agent disappeared entirely; nocx still ships a record for claude")
			}
			if e.State != StateUnreadable {
				t.Fatalf("state = %q, want unreadable", e.State)
			}
			if e.Problem == "" {
				t.Fatalf("no problem was reported, so nothing could tell the author why their edit is not in force")
			}
			if e.Record.Command == "claude" {
				t.Fatalf("the shipped record was handed back as if it were in force")
			}
		})
	}
}

// A command is a program and not a shell line, and a record that carries a
// line break in one is refused rather than handed to a launcher that would run
// the rest of it.
func TestACommandCarryingALineBreakIsRefused(t *testing.T) {
	s, dir := newStore(t)
	writeDocument(t, dir, "claude", `{"version": 1, "command": "claude\nrm -rf /"}`)

	e := entry(t, s, "claude")
	if e.State != StateUnreadable {
		t.Fatalf("state = %q, want a refused record", e.State)
	}
	if !strings.Contains(e.Problem, "line break") {
		t.Fatalf("problem = %q, want the line break named", e.Problem)
	}
}

// A name that could walk out of the app directory is not an agent: it is
// refused, and the rule is storage's because every per-agent document family
// derives a file name the same way.
func TestANameThatCouldLeaveTheAppDirectoryIsNotAnAgent(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"../escape", "/etc/passwd", ".hidden", "", "a/b", ".."} {
		if _, ok := s.entry(id); ok {
			t.Fatalf("%q was accepted as an agent name", id)
		}
		if storage.ValidDocumentName(id) {
			t.Fatalf("%q is accepted as a document name", id)
		}
	}
	// And the rule is not merely strict: every name this build ships passes it.
	if !storage.ValidDocumentName("claude") {
		t.Fatalf("the name this build ships is refused by the rule the store applies")
	}
}

// The record answers the restart restore's resume question, and the answers
// are the agent record's own rather than the record's completeness alone.
func TestTheRecordAnswersWhetherAnAgentCanBeResumed(t *testing.T) {
	s, _ := newStore(t)
	p := s.Probe()
	ctx := context.Background()

	t.Run("an agent this build ships, by id", func(t *testing.T) {
		if err := p.Resume(ctx, "claude", workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-1"}); err != nil {
			t.Fatalf("Resume: %v, want claude's own by-id resume accepted", err)
		}
	})
	t.Run("an agent this build ships, by cwd", func(t *testing.T) {
		if err := p.Resume(ctx, "claude", workers.ResumeIdentity{Mode: workers.ResumeByCwd}); err != nil {
			t.Fatalf("Resume: %v, want claude's own by-cwd resume accepted", err)
		}
	})
	t.Run("a name nocx has never seen", func(t *testing.T) {
		// THIS is the answer the shipped DiskProbe could not give: it said
		// "an agent nocx never saw" in its own comment and could not tell one
		// from a name somebody invented.
		err := p.Resume(ctx, "definitely-not-an-agent", workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-1"})
		if err == nil {
			t.Fatalf("a name nocx has no record of was accepted as a launchable agent")
		}
		if !strings.Contains(err.Error(), "definitely-not-an-agent") {
			t.Fatalf("refusal = %q, want the agent named", err)
		}
	})
	t.Run("no agent at all", func(t *testing.T) {
		if err := p.Resume(ctx, "", workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-1"}); err == nil {
			t.Fatalf("an empty agent name was accepted")
		}
	})
	t.Run("a mode the agent's record declares no args for", func(t *testing.T) {
		s2, dir2 := newStore(t)
		writeDocument(t, dir2, "claude", `{"version": 1, "command": "claude", "resume": {"resumeCwdArgs": ["--continue"]}}`)
		err := s2.Probe().Resume(ctx, "claude", workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-1"})
		if err == nil {
			t.Fatalf("a by-id resume was accepted for a record that declares only a by-cwd one")
		}
		if !strings.Contains(err.Error(), "by-cwd") {
			t.Fatalf("refusal = %q, want what the record DOES resume named", err)
		}
	})
	t.Run("a record that declares no resume at all", func(t *testing.T) {
		s3, dir3 := newStore(t)
		writeDocument(t, dir3, "myagent", `{"version": 1, "command": "my-agent"}`)
		err := s3.Probe().Resume(ctx, "myagent", workers.ResumeIdentity{Mode: workers.ResumeByCwd})
		if err == nil {
			t.Fatalf("a resume was accepted for an agent whose record declares none")
		}
		if !strings.Contains(err.Error(), "no way to resume") {
			t.Fatalf("refusal = %q, want the absence named", err)
		}
	})
	t.Run("a document that cannot be used", func(t *testing.T) {
		s4, dir4 := newStore(t)
		writeDocument(t, dir4, "claude", `{`)
		err := s4.Probe().Resume(ctx, "claude", workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "conv-1"})
		if err == nil {
			t.Fatalf("a resume was accepted over a record that cannot be read")
		}
		if !strings.Contains(err.Error(), "cannot read") {
			t.Fatalf("refusal = %q, want the unreadable record named", err)
		}
	})
	t.Run("a store nobody built", func(t *testing.T) {
		// Fail closed, the same direction a nil probe is refused in
		// workers.Restore: a restore that could not look must not claim the
		// pane is fine.
		if err := (Probe{}).Resume(ctx, "claude", workers.ResumeIdentity{Mode: workers.ResumeByID, ID: "c"}); err == nil {
			t.Fatalf("a probe with no store accepted a resume")
		}
		if err := (Probe{}).Checkout(ctx, workers.Worktree{}, ""); err == nil {
			t.Fatalf("a probe with no store accepted a launch directory of nothing")
		}
	})
}

// The checkout question is not the record's, and it is delegated rather than
// restated: what a probe says about a directory is what the probe that owns
// that question says.
func TestTheCheckoutQuestionIsTheShippedProbesOwn(t *testing.T) {
	s, _ := newStore(t)
	p := s.Probe()
	ctx := context.Background()

	dir := t.TempDir()
	if err := p.Checkout(ctx, workers.Worktree{}, dir); err != nil {
		t.Fatalf("Checkout(%q) = %v, want a directory that is there accepted", dir, err)
	}
	if want := (workers.DiskProbe{}).Checkout(ctx, workers.Worktree{}, dir); want != nil {
		t.Fatalf("the shipped probe disagrees about %q: %v", dir, want)
	}
	gone := filepath.Join(dir, "gone")
	if err := p.Checkout(ctx, workers.Worktree{}, gone); err == nil {
		t.Fatalf("Checkout(%q) accepted a directory that is not there", gone)
	}
}
