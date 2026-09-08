package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// scriptedFiles answers per call and records what each one was asked, so a
// test can assert that ONE file went to ONE call rather than all of them to
// one.
type scriptedFiles struct {
	mu    sync.Mutex
	asked [][]*schema.Message
	// reply is chosen by the file's path so an out-of-order fan-out cannot
	// make a test pass by accident.
	reply  func(user string) (string, error)
	failed int
}

func (m *scriptedFiles) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.asked = append(m.asked, msgs)
	m.mu.Unlock()
	body, err := m.reply(msgs[1].Content)
	if err != nil {
		m.mu.Lock()
		m.failed++
		m.mu.Unlock()
		return nil, err
	}
	return &schema.Message{Content: body}, nil
}

func (*scriptedFiles) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream is not used by a skill audit")
}

func envelope(verdict, report string) string {
	body, err := json.Marshal(map[string]string{"verdict": verdict, "report": report})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func bundle(paths ...string) []SkillAuditFile {
	files := make([]SkillAuditFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, SkillAuditFile{Path: path, Text: "the bytes of " + path})
	}
	return files
}

// ONE CALL PER FILE, AND THE MODEL NEVER CHOOSES WHICH (nocx-fuymi).
//
// The reading used to be one call over the whole bundle concatenated, so a
// file past the composition budget was named and not read. Now the manifest is
// the loop, nocx walks it, and the bundle's text cannot steer the walk — which
// is what keeps design §7's inertness true while the reading gets deeper.
func TestReadingCallsOncePerFileAndOnceMore(t *testing.T) {
	m := &scriptedFiles{reply: func(user string) (string, error) {
		if strings.Contains(user, "What each file's reading said") {
			return envelope("clear", "three paragraphs about the whole skill"), nil
		}
		return envelope("clear", "a paragraph about one file"), nil
	}}

	got, err := auditBundle(context.Background(), m, SkillAuditParams{
		Name:     "weather",
		Purpose:  "Answer questions about the weather",
		Overview: "---\nname: weather\n---\nask the station",
		Files:    bundle("SKILL.md", "references/stations.md", "scripts/fetch.sh"),
	})
	if err != nil {
		t.Fatalf("auditBundle: %v", err)
	}
	if len(m.asked) != 4 {
		t.Fatalf("calls = %d, want three files and one conclusion", len(m.asked))
	}
	if len(got.Files) != 3 {
		t.Fatalf("notes = %d, want one per file", len(got.Files))
	}
	for i, path := range []string{"SKILL.md", "references/stations.md", "scripts/fetch.sh"} {
		if got.Files[i].Path != path {
			t.Fatalf("notes[%d] = %q, want %q — the manifest's order, whatever order the calls finished in", i, got.Files[i].Path, path)
		}
	}
	if got.Verdict != SkillClear || !strings.Contains(got.Report, "whole skill") {
		t.Fatalf("the reading is not the conclusion: %+v", got.SkillReading)
	}

	// EACH per-file call carried exactly one file. A call carrying two would
	// be the old single pass wearing the new shape.
	for _, msgs := range m.asked {
		user := msgs[1].Content
		if strings.Contains(user, "What each file's reading said") {
			continue
		}
		carried := 0
		for _, path := range []string{"SKILL.md", "references/stations.md", "scripts/fetch.sh"} {
			if strings.Contains(user, "the bytes of "+path) {
				carried++
			}
		}
		if carried != 1 {
			t.Fatalf("a per-file call carried %d files:\n%s", carried, user)
		}
	}
}

// THE CLAIM TRAVELS WITH EVERY FILE, because "does this do what the skill says
// it does" cannot be answered by a file that never saw the claim.
func TestEachFileIsToldWhatTheSkillClaims(t *testing.T) {
	m := &scriptedFiles{reply: func(string) (string, error) {
		return envelope("clear", "a paragraph"), nil
	}}
	if _, err := auditBundle(context.Background(), m, SkillAuditParams{
		Name:     "weather",
		Purpose:  "Answer questions about the weather",
		Overview: "---\nname: weather\n---\nask the station",
		Files:    bundle("SKILL.md", "references/stations.md"),
	}); err != nil {
		t.Fatalf("auditBundle: %v", err)
	}
	for _, msgs := range m.asked {
		user := msgs[1].Content
		if strings.Contains(user, "What each file's reading said") {
			continue
		}
		if !strings.Contains(user, "Answer questions about the weather") {
			t.Fatalf("a per-file call was not told what the skill claims:\n%s", user)
		}
	}
}

// A FILE NOBODY COULD READ IS A NOTE THAT SAYS SO, and the reduce is told. The
// alternative — dropping it — is a conclusion drawn over a subset the reader
// cannot name, which is the defect the omission list exists to prevent one
// level up.
func TestAFileThatFailsBecomesANoteAndTheConclusionIsToldSo(t *testing.T) {
	var reduceUser string
	m := &scriptedFiles{reply: func(user string) (string, error) {
		if strings.Contains(user, "What each file's reading said") {
			reduceUser = user
			return envelope("suspect", "one file could not be read"), nil
		}
		if strings.Contains(user, "the bytes of scripts/fetch.sh") {
			return "", errors.New("provider said no")
		}
		return envelope("clear", "a paragraph"), nil
	}}

	got, err := auditBundle(context.Background(), m, SkillAuditParams{
		Name:     "weather",
		Overview: "---\nname: weather\n---\nask the station",
		Files:    bundle("SKILL.md", "scripts/fetch.sh"),
	})
	if err != nil {
		t.Fatalf("one failed file failed the whole reading: %v", err)
	}
	if got.Files[1].Err == nil {
		t.Fatal("the failed file's note does not carry its failure")
	}
	if !strings.Contains(reduceUser, "scripts/fetch.sh") || !strings.Contains(reduceUser, "could not be read") {
		t.Fatalf("the conclusion was not told about the file nobody read:\n%s", reduceUser)
	}
	if got.Verdict != SkillSuspect {
		t.Fatalf("verdict = %q, want the conclusion's own answer", got.Verdict)
	}
}

// EVERY file failing is the READING failing. One or two are notes to weigh;
// all of them means there is nothing to weigh, and a verdict drawn from
// nothing is the empty report this feature refuses to give.
func TestAReadingWhereNoFileCouldBeReadFails(t *testing.T) {
	m := &scriptedFiles{reply: func(string) (string, error) {
		return "", errors.New("dial tcp: connection refused")
	}}
	got, err := auditBundle(context.Background(), m, SkillAuditParams{
		Name:     "weather",
		Overview: "x",
		Files:    bundle("SKILL.md", "scripts/fetch.sh"),
	})
	if err == nil {
		t.Fatalf("a reading in which nothing was read answered %+v", got.SkillReading)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the refusal drops what happened: %v", err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("the notes are not carried back: %+v", got.Files)
	}
}

// THE CONCLUSION READS THE NOTES AND SKILL.md, and not the bundle again. What
// it decides is whether the files do what SKILL.md claims, and the map pass is
// what read the files.
func TestTheConclusionIsGivenTheNotesAndTheSkillsOwnWords(t *testing.T) {
	var reduceUser string
	m := &scriptedFiles{reply: func(user string) (string, error) {
		if strings.Contains(user, "What each file's reading said") {
			reduceUser = user
			return envelope("clear", "the conclusion"), nil
		}
		return envelope("suspect", "this file curls an address the description never mentions"), nil
	}}
	if _, err := auditBundle(context.Background(), m, SkillAuditParams{
		Name:     "weather",
		Overview: "---\nname: weather\n---\nSKILL-MD-VERBATIM",
		Files:    bundle("SKILL.md", "scripts/fetch.sh"),
	}); err != nil {
		t.Fatalf("auditBundle: %v", err)
	}
	for _, want := range []string{
		"SKILL-MD-VERBATIM",
		"curls an address the description never mentions",
		"scripts/fetch.sh",
		"suspect",
	} {
		if !strings.Contains(reduceUser, want) {
			t.Fatalf("the conclusion was not given %q:\n%s", want, reduceUser)
		}
	}
	// And NOT the bytes: a conclusion re-reading the bundle would be the
	// single pass again, with the per-file calls paid for and wasted.
	if strings.Contains(reduceUser, "the bytes of scripts/fetch.sh") {
		t.Fatalf("the conclusion was given a file's bytes:\n%s", reduceUser)
	}
}
