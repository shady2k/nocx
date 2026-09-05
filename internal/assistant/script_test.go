package assistant

// The script a command names, in the question (nocx-872jc.3).
//
// Rule 1 of AGENTS.md: these assert what a PERSON can now do — a proposal for
// `bash deploy.sh` reaches them carrying deploy.sh — through the seam a person
// actually reaches, which is the pipeline the policy gate escalates through
// and not ScriptReadingsFor called on its own. The unit tests underneath it
// are for the cases a driven escalation cannot conveniently produce: a source
// that fails, a file that is not text, a budget that is spent.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// fileScriptSource is a ScriptSource over a real directory — the smallest
// thing that behaves like the transport's: it resolves a relative path
// against the cwd it is given, refuses to guess when there is none, and reads
// the bytes that are actually on disk. It records what it was asked, because
// the seam is asserted BY TRYING and never by re-inspecting the caller.
type fileScriptSource struct {
	mu       sync.Mutex
	asked    []string
	err      error
	notText  bool
	tooLarge bool
}

func (f *fileScriptSource) ReadScript(_ context.Context, _, cwd, at string, _ int) (ScriptContent, error) {
	f.mu.Lock()
	f.asked = append(f.asked, at)
	f.mu.Unlock()
	if f.err != nil {
		return ScriptContent{}, f.err
	}
	if f.tooLarge {
		return ScriptContent{TooLarge: true}, nil
	}
	if f.notText {
		return ScriptContent{NotText: true}, nil
	}
	resolved := at
	if !filepath.IsAbs(resolved) {
		if cwd == "" {
			return ScriptContent{}, &ScriptUnreadableError{
				Reason: at + " is relative and nocx does not know which directory the command runs in",
			}
		}
		resolved = filepath.Join(cwd, at)
	}
	data, err := os.ReadFile(resolved) //nolint:gosec // a path the test itself wrote under t.TempDir()
	if err != nil {
		return ScriptContent{}, &ScriptUnreadableError{Reason: "there is no file at " + resolved}
	}
	return ScriptContent{Text: string(data)}, nil
}

func (f *fileScriptSource) reads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// runGrantIn is the grant a session.run proposal needs: the fixture session
// the run is bound to, plus a path row, on a matrix that asks every time.
func runGrantIn(dir string) content.Grant {
	return askEveryTimeMatrix().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: dir},
	})
}

// askAbout drives one command proposal through the WHOLE ask — a real
// provider proposing session.run, the real engine, the real gate — to the
// question a person is shown. Not the middleware on its own: the question is
// what this bead is about, and a person reaches it through Ask.
func askAbout(t *testing.T, source ScriptSource, dir, command string) *ApprovalRequest {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "session.run", args: string(args)}))
	t.Cleanup(srv.Close)
	cl, clErr := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	grant := runGrantIn(dir)
	p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
	p.Scripts = source
	p.Cwd = dir
	askErr := cl.Ask(context.Background(), p, func(AskEvent) error { return nil })
	var asked *ApprovalRequestedError
	if !errors.As(askErr, &asked) || asked.Request == nil {
		t.Fatalf("proposal %q: err = %v, want the approval-requested suspension", command, askErr)
	}
	return asked.Request
}

func readingFor(t *testing.T, req *ApprovalRequest, path string) ScriptReading {
	t.Helper()
	for _, reading := range req.Scripts {
		if reading.Path == path {
			return reading
		}
	}
	t.Fatalf("no reading for %q in %+v", path, req.Scripts)
	return ScriptReading{}
}

// ── what a person can now do ──────────────────────────────────────────────

// THE acceptance criterion: the whole of the file the command names is in the
// question, beside the command, labelled as what it is.
func TestApprovalQuestion_ACommandThatNamesAScriptCarriesTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	const body = "#!/bin/sh\nrm -rf /srv//*\necho done\n"
	writeFile(t, filepath.Join(dir, "deploy.sh"), body)
	source := &fileScriptSource{}

	req := askAbout(t, source, dir, "bash deploy.sh")

	if len(req.Scripts) != 1 {
		t.Fatalf("scripts = %+v, want exactly the one file the command names", req.Scripts)
	}
	reading := req.Scripts[0]
	if reading.Text != body {
		t.Fatalf("text = %q, want the WHOLE file %q", reading.Text, body)
	}
	if reading.Refusal != ScriptRefusalNone {
		t.Fatalf("refusal = %q, want none: the file was read", reading.Refusal)
	}
	// The path the COMMAND wrote, so the reading and the command line above
	// it are obviously about the same thing.
	if reading.Path != "deploy.sh" {
		t.Fatalf("path = %q, want the path the command wrote", reading.Path)
	}
	if reading.Verb != content.ResourceExecute {
		t.Fatalf("verb = %q, want execute", reading.Verb)
	}
}

// The reading changes NOTHING about what is sent. Same proposal, with the
// seam and without it: the arguments are byte-identical and so is the hash
// the answer is bound by.
func TestApprovalQuestion_TheScriptChangesNothingAboutTheBinding(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "deploy.sh"), "echo hi\n")
	const command = "bash deploy.sh"

	with := askAbout(t, &fileScriptSource{}, dir, command)
	without := askAbout(t, nil, dir, command)

	if with.Arguments != without.Arguments {
		t.Fatalf("arguments moved when a file was read: %q vs %q", with.Arguments, without.Arguments)
	}
	if !strings.Contains(with.Arguments, command) {
		t.Fatalf("arguments = %q, want the model's own verbatim command", with.Arguments)
	}
	if with.ArgHash != without.ArgHash {
		t.Fatalf("argHash moved when a file was read: %q vs %q", with.ArgHash, without.ArgHash)
	}
	if with.CallID != without.CallID || with.Attempt != without.Attempt {
		t.Fatal("the call id or attempt moved when a file was read")
	}
}

// Two scripts in one command: BOTH arrive. A window showing the first of two
// looks complete while being half the act.
func TestApprovalQuestion_EveryScriptTheCommandNamesArrives(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.sh"), "echo a\n")
	writeFile(t, filepath.Join(dir, "b.sh"), "echo b\n")
	source := &fileScriptSource{}

	req := askAbout(t, source, dir, "bash a.sh && bash b.sh")

	if len(req.Scripts) != 2 {
		t.Fatalf("scripts = %+v, want both files the command names", req.Scripts)
	}
	if got := readingFor(t, req, "a.sh").Text; got != "echo a\n" {
		t.Fatalf("a.sh = %q", got)
	}
	if got := readingFor(t, req, "b.sh").Text; got != "echo b\n" {
		t.Fatalf("b.sh = %q", got)
	}
}

// A command that names no file draws NO affordance: the field is absent, not
// an empty array, so the surface has nothing to render and says nothing.
func TestApprovalQuestion_ACommandThatNamesNoFileCarriesNoReading(t *testing.T) {
	dir := t.TempDir()
	source := &fileScriptSource{}

	req := askAbout(t, source, dir, "ls -la /etc")

	if req.Scripts != nil {
		t.Fatalf("scripts = %+v, want none: the command names no file", req.Scripts)
	}
	if reads := source.reads(); len(reads) != 0 {
		t.Fatalf("the source was asked for %v although the command names no file", reads)
	}
}

// A file that cannot be read is a TRUE SENTENCE and never an empty box. The
// external call fails; the question still appears, and it says which file and
// why rather than implying the file was harmless.
func TestApprovalQuestion_AFileThatCannotBeReadSaysSo(t *testing.T) {
	dir := t.TempDir() // deploy.sh deliberately not written
	source := &fileScriptSource{}

	req := askAbout(t, source, dir, "bash deploy.sh")

	reading := readingFor(t, req, "deploy.sh")
	if reading.Refusal != ScriptRefusalUnreadable {
		t.Fatalf("refusal = %q, want unreadable", reading.Refusal)
	}
	if reading.Text != "" {
		t.Fatalf("text = %q, want nothing: no bytes were read", reading.Text)
	}
	if reading.Reason == "" {
		t.Fatal("an unreadable file reached the surface with no sentence to draw — an empty affordance")
	}
	if !strings.Contains(reading.Reason, "deploy.sh") {
		t.Fatalf("reason = %q, want it to name the file it is about", reading.Reason)
	}
}

// `source x.sh` is not `bash x.sh`, and the wire keeps them apart: one runs a
// subprocess, the other changes the shell the person is sitting in.
func TestApprovalQuestion_SourcingIsItsOwnVerb(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "env.sh"), "export TOKEN=x\n")

	req := askAbout(t, &fileScriptSource{}, dir, "source env.sh")

	if got := readingFor(t, req, "env.sh").Verb; got != content.ResourceSource {
		t.Fatalf("verb = %q, want source", got)
	}
}

// ── the readings themselves ───────────────────────────────────────────────

// The external call fails with a bare error — AGENTS.md testing rule 3. The
// question is thinner, never refused, and there is always a sentence.
func TestScriptReadingsFor_ABareFailureStillCarriesASentence(t *testing.T) {
	source := &fileScriptSource{err: errors.New("the connection dropped")}
	readings := ScriptReadingsFor(context.Background(), source, "session-a", "/repo", executeOf("deploy.sh"))

	if len(readings) != 1 {
		t.Fatalf("readings = %+v, want one", readings)
	}
	if readings[0].Refusal != ScriptRefusalUnreadable {
		t.Fatalf("refusal = %q, want unreadable", readings[0].Refusal)
	}
	if !strings.Contains(readings[0].Reason, "the connection dropped") {
		t.Fatalf("reason = %q, want it to carry what went wrong", readings[0].Reason)
	}
}

// The shared deadline is spent: the reading says the window would not wait,
// which is a different sentence from "there is no such file". Asserted by
// handing the source the outcome rather than by making a test slow — a test
// that depended on a duration would be broken on a fast machine too.
func TestScriptReadingsFor_ASpentDeadlineIsItsOwnSentence(t *testing.T) {
	source := &fileScriptSource{err: context.DeadlineExceeded}
	readings := ScriptReadingsFor(context.Background(), source, "session-a", "/repo", executeOf("deploy.sh"))

	if readings[0].Refusal != ScriptRefusalUnreadable {
		t.Fatalf("refusal = %q, want unreadable", readings[0].Refusal)
	}
	if !strings.Contains(readings[0].Reason, "longer than the window would wait") {
		t.Fatalf("reason = %q, want the words for a deadline rather than for a missing file", readings[0].Reason)
	}
}

// No source at all — every caller that is not the transport, and every
// machine nothing can reach. Nothing is read, and the window says so.
func TestScriptReadingsFor_WithNoSourceSaysNothingWasRead(t *testing.T) {
	readings := ScriptReadingsFor(context.Background(), nil, "session-a", "/repo", executeOf("deploy.sh"))

	if len(readings) != 1 || readings[0].Refusal != ScriptRefusalUnreadable {
		t.Fatalf("readings = %+v, want one unreadable", readings)
	}
	if readings[0].Reason == "" {
		t.Fatal("no source and no sentence: an empty affordance")
	}
}

// Two facts about a file that IS there, kept apart from "we could not read
// it" — FileReadout draws them in a different tone for that reason.
func TestScriptReadingsFor_NotTextAndTooLargeAreFactsAboutTheFile(t *testing.T) {
	notText := ScriptReadingsFor(context.Background(), &fileScriptSource{notText: true}, "s", "/repo", executeOf("x.sh"))
	if notText[0].Refusal != ScriptRefusalNotText || notText[0].Text != "" {
		t.Fatalf("reading = %+v, want not-text with no bytes", notText[0])
	}
	tooLarge := ScriptReadingsFor(context.Background(), &fileScriptSource{tooLarge: true}, "s", "/repo", executeOf("x.sh"))
	if tooLarge[0].Refusal != ScriptRefusalTooLarge {
		t.Fatalf("reading = %+v, want too-large", tooLarge[0])
	}
	if tooLarge[0].MaxBytes != MaxScriptBytes {
		t.Fatalf("maxBytes = %d, want the budget it was measured against", tooLarge[0].MaxBytes)
	}
	// The head of an over-budget file is deliberately NOT shown: a person who
	// read the first 64 KiB of a script would believe they had read it.
	if tooLarge[0].Text != "" {
		t.Fatalf("text = %q, want nothing for an over-budget file", tooLarge[0].Text)
	}
}

// One path named twice is one reading. Two rows of the same bytes under the
// same name read as two files.
func TestScriptReadingsFor_APathNamedTwiceIsReadOnce(t *testing.T) {
	source := &fileScriptSource{}
	inv := content.Invocation{Parsed: true, Resources: content.ResourceReport{Resources: []content.Resource{
		{Path: "deploy.sh", Verb: content.ResourceExecute},
		{Path: "deploy.sh", Verb: content.ResourceExecute},
	}}}
	readings := ScriptReadingsFor(context.Background(), source, "s", "/repo", inv)
	if len(readings) != 1 {
		t.Fatalf("readings = %+v, want one", readings)
	}
	if reads := source.reads(); len(reads) != 1 {
		t.Fatalf("the source was asked %d times for one file", len(reads))
	}
}

// An unparsed command has no resource report to trust, so nothing is read.
func TestScriptReadingsFor_AnUnparsedCommandReadsNothing(t *testing.T) {
	source := &fileScriptSource{}
	inv := content.Invocation{Parsed: false, Resources: content.ResourceReport{Resources: []content.Resource{
		{Path: "deploy.sh", Verb: content.ResourceExecute},
	}}}
	if readings := ScriptReadingsFor(context.Background(), source, "s", "/repo", inv); readings != nil {
		t.Fatalf("readings = %+v, want none from a command nothing parsed", readings)
	}
	if reads := source.reads(); len(reads) != 0 {
		t.Fatalf("the source was asked for %v from an unparsed command", reads)
	}
}

// A verb that is not execute or source is not a script. `rm x.sh` names the
// same file and the window must not pretend the command is about to run it.
func TestScriptReadingsFor_OnlyExecuteAndSourceAreScripts(t *testing.T) {
	source := &fileScriptSource{}
	inv := content.Invocation{Parsed: true, Resources: content.ResourceReport{Resources: []content.Resource{
		{Path: "notes.txt", Verb: content.ResourceRead},
		{Path: "out.log", Verb: content.ResourceWrite},
		{Path: "gone.sh", Verb: content.ResourceDelete},
	}}}
	if readings := ScriptReadingsFor(context.Background(), source, "s", "/repo", inv); readings != nil {
		t.Fatalf("readings = %+v, want none: none of those verbs runs a file", readings)
	}
}

func executeOf(path string) content.Invocation {
	return content.Invocation{Parsed: true, Resources: content.ResourceReport{Resources: []content.Resource{
		{Path: path, Verb: content.ResourceExecute},
	}}}
}
