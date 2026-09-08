package transport

// The script half of the approval question on the wire (nocx-872jc.3): the
// whole of the file the command NAMES, carried beside the verbatim command.
//
// Three checks, and the third is the point (contracts/README row 3): the DTO
// satisfies the schema, the REAL notification off the REAL socket satisfies
// it AND carries the bytes that are on disk, and the verbatim command it is
// beside is untouched. Underneath them, the source itself: what it reads,
// what it refuses, and a true sentence for every refusal — a modal with an
// empty box in it is the failure this bead exists to remove, and an empty box
// is what a refusal with no sentence renders as.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// scriptHarness is the ask harness with a real local filesystem provider
// factory and one open local session — the wiring an approval question about
// a command in that session actually has.
type scriptHarness struct {
	*askHarness
	sid string
	dir string
}

func newScriptHarness(t *testing.T, client *scriptedApprovalClient, opts ...WSServerOption) *scriptHarness {
	t.Helper()
	h := newAskHarnessWithOpts(t, client, append([]WSServerOption{
		WithFilesystemProviderFactory(filesLocalFactory),
	}, opts...)...)
	h.createEndpoint()
	return &scriptHarness{askHarness: h, sid: openLocalSession(t, h.conn), dir: t.TempDir()}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	at := filepath.Join(dir, name)
	if err := os.WriteFile(at, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", at, err)
	}
	return at
}

func executeInvocation(paths ...string) content.Invocation {
	inv := content.Invocation{Parsed: true}
	for _, at := range paths {
		inv.Resources.Resources = append(inv.Resources.Resources,
			content.Resource{Path: at, Verb: content.ResourceExecute})
	}
	return inv
}

// ── the source itself ─────────────────────────────────────────────────────

// A relative path resolves against the run's OWN cwd, which is the whole
// reason the cwd travels: `bash deploy.sh` names a path only in a directory.
func TestReadScript_ResolvesARelativePathAgainstTheRunsCwd(t *testing.T) {
	h := newScriptHarness(t, &scriptedApprovalClient{})
	const body = "#!/bin/sh\necho deploying\n"
	writeScript(t, h.dir, "deploy.sh", body)

	read, err := h.ws.ReadScript(t.Context(), h.sid, h.dir, "deploy.sh", assistant.MaxScriptBytes)
	if err != nil {
		t.Fatalf("ReadScript: %v", err)
	}
	if read.Text != body {
		t.Fatalf("text = %q, want the file %q", read.Text, body)
	}
}

// An absolute path needs no cwd at all.
func TestReadScript_ReadsAnAbsolutePathWithNoCwd(t *testing.T) {
	h := newScriptHarness(t, &scriptedApprovalClient{})
	at := writeScript(t, h.dir, "setup.sh", "echo setting up\n")

	read, err := h.ws.ReadScript(t.Context(), h.sid, "", at, assistant.MaxScriptBytes)
	if err != nil {
		t.Fatalf("ReadScript: %v", err)
	}
	if read.Text != "echo setting up\n" {
		t.Fatalf("text = %q", read.Text)
	}
}

// A relative path with NO directory to resolve it against is refused rather
// than guessed. This is the one failure on this surface a person cannot see:
// ~/deploy.sh and /srv/app/deploy.sh are both real files with the right name,
// and showing the wrong one would be worse than showing none.
func TestReadScript_RefusesToGuessADirectoryForARelativePath(t *testing.T) {
	h := newScriptHarness(t, &scriptedApprovalClient{})
	writeScript(t, h.dir, "deploy.sh", "echo hi\n")

	_, err := h.ws.ReadScript(t.Context(), h.sid, "", "deploy.sh", assistant.MaxScriptBytes)
	if err == nil {
		t.Fatal("a relative path with no cwd was resolved anyway")
	}
	if !strings.Contains(err.Error(), "deploy.sh") {
		t.Fatalf("refusal %q does not name the file it is about", err)
	}
}

// The external call fails: the file is not there. AGENTS.md testing rule 3,
// and the sentence names the path so a person can check it.
func TestReadScript_AMissingFileIsANamedRefusal(t *testing.T) {
	h := newScriptHarness(t, &scriptedApprovalClient{})

	_, err := h.ws.ReadScript(t.Context(), h.sid, h.dir, "deploy.sh", assistant.MaxScriptBytes)
	if err == nil {
		t.Fatal("a missing file read as though it were there")
	}
	if !strings.Contains(err.Error(), "deploy.sh") {
		t.Fatalf("refusal %q does not name the missing file", err)
	}
}

// A directory is not a script. The provider says so structurally and the
// sentence must not pretend the read merely failed.
func TestReadScript_ADirectoryIsNotAFileToRead(t *testing.T) {
	h := newScriptHarness(t, &scriptedApprovalClient{})
	if err := os.Mkdir(filepath.Join(h.dir, "scripts"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := h.ws.ReadScript(t.Context(), h.sid, h.dir, "scripts", assistant.MaxScriptBytes); err == nil {
		t.Fatal("a directory read as a script")
	}
}

// Bytes that are not text are a fact about the FILE, not a failed read.
func TestReadScript_BytesThatAreNotTextAreTheirOwnFact(t *testing.T) {
	h := newScriptHarness(t, &scriptedApprovalClient{})
	writeScript(t, h.dir, "blob.sh", "ELF\x00\x01\x02binary")

	read, err := h.ws.ReadScript(t.Context(), h.sid, h.dir, "blob.sh", assistant.MaxScriptBytes)
	if err != nil {
		t.Fatalf("ReadScript: %v", err)
	}
	if !read.NotText || read.Text != "" {
		t.Fatalf("read = %+v, want not-text with no bytes", read)
	}
}

// A file at exactly the budget comes back WHOLE, and one byte past it is
// too large. The boundary is asserted because "budget+1" is how the size is
// learned and an off-by-one there silently truncates or silently refuses.
func TestReadScript_TheBudgetBoundaryIsExact(t *testing.T) {
	h := newScriptHarness(t, &scriptedApprovalClient{})
	const budget = 64
	writeScript(t, h.dir, "exact.sh", strings.Repeat("a", budget))
	writeScript(t, h.dir, "over.sh", strings.Repeat("a", budget+1))

	exact, err := h.ws.ReadScript(t.Context(), h.sid, h.dir, "exact.sh", budget)
	if err != nil {
		t.Fatalf("ReadScript(exact): %v", err)
	}
	if exact.TooLarge || len(exact.Text) != budget {
		t.Fatalf("a file of exactly the budget came back %+v, want the whole of it", exact)
	}
	over, err := h.ws.ReadScript(t.Context(), h.sid, h.dir, "over.sh", budget)
	if err != nil {
		t.Fatalf("ReadScript(over): %v", err)
	}
	if !over.TooLarge {
		t.Fatal("a file one byte past the budget was not reported as too large")
	}
	// The head is NOT shown: a person who read the first N bytes of a script
	// would believe they had read the script.
	if over.Text != "" {
		t.Fatalf("text = %q, want nothing for an over-budget file", over.Text)
	}
}

// A session that is gone: the question still opens, the reading says why.
func TestReadScript_AnUnknownSessionIsARefusalNotAPanic(t *testing.T) {
	h := newScriptHarness(t, &scriptedApprovalClient{})
	writeScript(t, h.dir, "deploy.sh", "echo hi\n")

	if _, err := h.ws.ReadScript(t.Context(), "0123456789abcdef0123456789abcdef", h.dir, "deploy.sh", assistant.MaxScriptBytes); err == nil {
		t.Fatal("an unknown session read a file anyway")
	}
}

// No provider factory wired at all — a build that cannot reach any machine's
// files. Nothing is read, and it is said rather than crashed.
func TestReadScript_WithNoProviderFactorySaysSo(t *testing.T) {
	h := newAskHarness(t, &scriptedApprovalClient{})
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)
	dir := t.TempDir()
	writeScript(t, dir, "deploy.sh", "echo hi\n")

	_, err := h.ws.ReadScript(t.Context(), sid, dir, "deploy.sh", assistant.MaxScriptBytes)
	if err == nil {
		t.Fatal("a server with no filesystem provider factory read a file anyway")
	}
	if err.Error() == "" {
		t.Fatal("the refusal carries no sentence, which renders as an empty box")
	}
}

// ── the wire ──────────────────────────────────────────────────────────────

func scriptSuspension(readings *[]assistant.ScriptReading) func(runID string) error {
	return func(runID string) error {
		return &assistant.ApprovalRequestedError{Request: &assistant.ApprovalRequest{
			RunID: runID, Attempt: 1, Tool: "session.run", CallID: "call_1",
			Arguments: `{"command":"bash deploy.sh","sessionId":"s"}`,
			ArgHash:   "hash-a",
			Effect:    content.EffectDelegate,
			Scripts:   *readings,
		}}
	}
}

func TestAgentApprovalRequested_ScriptsDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")

	cases := map[string][]assistant.ScriptReading{
		"the file, read": {{
			Path: "deploy.sh", Verb: content.ResourceExecute,
			Text: "#!/bin/sh\nrm -rf /\n", MaxBytes: assistant.MaxScriptBytes,
		}},
		"two files": {
			{Path: "a.sh", Verb: content.ResourceExecute, Text: "echo a\n", MaxBytes: assistant.MaxScriptBytes},
			{Path: "b.sh", Verb: content.ResourceExecute, Text: "echo b\n", MaxBytes: assistant.MaxScriptBytes},
		},
		"sourced, not executed": {{
			Path: "env.sh", Verb: content.ResourceSource,
			Text: "export TOKEN=x\n", MaxBytes: assistant.MaxScriptBytes,
		}},
		"not text": {{
			Path: "x.sh", Verb: content.ResourceExecute,
			Refusal: assistant.ScriptRefusalNotText, MaxBytes: assistant.MaxScriptBytes,
		}},
		"over the budget": {{
			Path: "x.sh", Verb: content.ResourceExecute,
			Refusal: assistant.ScriptRefusalTooLarge, MaxBytes: assistant.MaxScriptBytes,
		}},
		"nothing to read": {{
			Path: "x.sh", Verb: content.ResourceExecute,
			Refusal: assistant.ScriptRefusalUnreadable, MaxBytes: assistant.MaxScriptBytes,
			Reason: "there is no file at /repo/x.sh on that machine, so nothing was read",
		}},
	}
	for name, readings := range cases {
		dto := agentApprovalRequested{
			RunID: "7", Attempt: 1, Tool: "session.run", CallID: "call_1",
			ArgHash: "hash-a", Arguments: `{"command":"bash deploy.sh"}`,
			Reason: "policy", Effect: "delegate",
			Standing: agentApprovalStanding{Available: true, Rule: "bash deploy.sh"},
			Scripts:  readings,
		}
		raw, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		validateJSON(t, schema, raw, "agent.approvalRequested DTO with scripts ("+name+")")
	}
}

// The real notification off the real socket, carrying bytes that are really
// on disk, read through the server's OWN ScriptSource. A payload the test
// itself built proves the struct is well-formed, not that the server sends it.
func TestAgentApprovalRequested_ScriptsOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "agent.approvalRequested.schema.json")
	var readings []assistant.ScriptReading
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: scriptSuspension(&readings)},
	}}
	h := newScriptHarness(t, client)
	const body = "#!/bin/sh\nrm -rf /srv/app\n"
	writeScript(t, h.dir, "deploy.sh", body)
	// The REAL seam, on the REAL server, against a file really on disk: the
	// readings the notification carries are the ones the product builds.
	readings = assistant.ScriptReadingsFor(t.Context(), h.ws, h.sid, h.dir, executeInvocation("deploy.sh"))

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": h.sid, "question": "deploy it", "cwd": h.dir,
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	validateJSON(t, schema, raw, "agent.approvalRequested params with scripts (real socket)")

	var got struct {
		Arguments string `json:"arguments"`
		Scripts   []struct {
			Path     string `json:"path"`
			Verb     string `json:"verb"`
			Text     string `json:"text"`
			Refusal  string `json:"refusal"`
			MaxBytes int    `json:"maxBytes"`
			Reason   string `json:"reason"`
		} `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if len(got.Scripts) != 1 {
		t.Fatalf("scripts = %+v, want the one file the command names", got.Scripts)
	}
	if got.Scripts[0].Text != body {
		t.Fatalf("text = %q, want the whole file %q off the wire", got.Scripts[0].Text, body)
	}
	if got.Scripts[0].Path != "deploy.sh" || got.Scripts[0].Verb != "execute" {
		t.Fatalf("script = %+v, want the path the command wrote and its verb", got.Scripts[0])
	}
	if got.Scripts[0].Refusal != "" {
		t.Fatalf("refusal = %q, want none: the file was read", got.Scripts[0].Refusal)
	}
	// BESIDE, NEVER INSTEAD: what the person answers about still carries the
	// model's own verbatim command, untouched by anything read.
	if got.Arguments != `{"command":"bash deploy.sh","sessionId":"s"}` {
		t.Fatalf("arguments = %q, want the model's own proposal untouched", got.Arguments)
	}
}

// A proposal that names no file carries NO scripts field at all — absent, not
// an empty array. An empty array is an affordance, and an affordance beside a
// command that names no file reads as "we looked and found nothing".
func TestAgentApprovalRequested_NoScriptIsNoFieldOnTheWire(t *testing.T) {
	var none []assistant.ScriptReading
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: scriptSuspension(&none)},
	}}
	h := newScriptHarness(t, client)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": h.sid, "question": "list it", "cwd": h.dir,
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if _, present := got["scripts"]; present {
		t.Fatalf("scripts is on the wire for a proposal that names no file: %s", raw)
	}
}

// An unreadable file reaches the surface with a SENTENCE. Without one the
// window draws its refusal state with nothing in it, which is the empty
// affordance this bead forbids.
func TestAgentApprovalRequested_AnUnreadableScriptCarriesItsSentence(t *testing.T) {
	var readings []assistant.ScriptReading
	client := &scriptedApprovalClient{script: []approvalScriptStep{
		{suspend: scriptSuspension(&readings)},
	}}
	h := newScriptHarness(t, client)
	// Nothing is written: the command names a file that is not there.
	readings = assistant.ScriptReadingsFor(t.Context(), h.ws, h.sid, h.dir, executeInvocation("deploy.sh"))
	if len(readings) != 1 || readings[0].Refusal != assistant.ScriptRefusalUnreadable {
		t.Fatalf("readings = %+v, want one unreadable", readings)
	}

	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "ask-1", "sessionId": h.sid, "question": "deploy it", "cwd": h.dir,
	}, 1); errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	raw := readNotification(t, h.conn, "agent.approvalRequested", 5*time.Second)
	validateJSON(t, loadSchema(t, "agent.approvalRequested.schema.json"), raw,
		"agent.approvalRequested params with an unreadable script (real socket)")

	var got struct {
		Scripts []struct {
			Refusal string `json:"refusal"`
			Reason  string `json:"reason"`
			Text    string `json:"text"`
		} `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if len(got.Scripts) != 1 || got.Scripts[0].Refusal != "unreadable" {
		t.Fatalf("scripts = %+v, want one unreadable reading", got.Scripts)
	}
	if got.Scripts[0].Reason == "" {
		t.Fatal("an unreadable file reached the surface with no sentence: an empty affordance")
	}
	if got.Scripts[0].Text != "" {
		t.Fatalf("text = %q, want nothing: no bytes were read", got.Scripts[0].Text)
	}
}
