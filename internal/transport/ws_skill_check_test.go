package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/skill/builtin"
)

// checkCall issues skills.check for name over the harness's real socket,
// decodes the result into got, and turns a JSON-RPC error into a Go one —
// auditCall's twin for this method.
func checkCall(t *testing.T, h *auditHarness, name string, got *skillCheckResult) error {
	t.Helper()
	resp := jsonrpcCall(t, h.conn, "skills.check", map[string]any{"name": name})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		return errors.New(env.Error.Message)
	}
	return json.Unmarshal(env.Result, got)
}

// NOBODY HAS CHECKED THIS IS A RESULT, NOT AN ERROR. The default harness's
// "weather" skill exists but no audit has ever run against it.
func TestSkillsCheckIsAResultWhenNobodyHasChecked(t *testing.T) {
	repo := &recordingSkillChecks{}
	client := &auditingClient{report: "unused"}
	h := newAuditHarnessWithRoots(t, client, nil, WithSkillChecks(repo))

	var got skillCheckResult
	if err := checkCall(t, h, "weather", &got); err != nil {
		t.Fatalf("skills.check: %v", err)
	}
	if got.Checked {
		t.Fatalf("claimed a check nobody wrote: %+v", got)
	}
	if got.Name != "weather" {
		t.Fatalf("name = %q, want weather", got.Name)
	}
	if got.Check != nil || got.Current != nil {
		t.Fatalf("checked:false carried check/current: %+v", got)
	}
	if n := client.callCount(); n != 0 {
		t.Fatalf("skills.check spent %d model calls; it must spend none", n)
	}
}

// THE WHOLE POINT OF STORING: a check that was written comes back whole,
// with current:true when nothing on disk has moved since.
func TestSkillsCheckReturnsTheStoredCheckAndSaysItIsCurrent(t *testing.T) {
	repo := &recordingSkillChecks{}
	client := &auditingClient{report: "unused"}
	h := newAuditHarnessWithRoots(t, client, nil, WithSkillChecks(repo))

	material, err := h.store.Audit("weather")
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if err := repo.Put(context.Background(), content.SkillCheck{
		Name: "weather", Provenance: "installed", Verdict: "suspect",
		Report: "It pipes a URL into sh.", Role: "auditing", Endpoint: "local",
		Model: "m", Digest: material.Digest, CheckedAt: 1_757_000_000_000,
		Read: material.Read, Omitted: nil, Findings: nil, MaxBytes: 131072,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var got skillCheckResult
	if err := checkCall(t, h, "weather", &got); err != nil {
		t.Fatalf("skills.check: %v", err)
	}
	if !got.Checked {
		t.Fatal("did not find the check that was written")
	}
	if got.Check == nil {
		t.Fatal("checked:true with no check")
	}
	if got.Check.Verdict != "suspect" || got.Check.Report != "It pipes a URL into sh." {
		t.Fatalf("check came back wrong: %+v", got.Check)
	}
	if got.Current == nil || !*got.Current {
		t.Fatalf("current = %v, want true with nothing edited", got.Current)
	}
	if n := client.callCount(); n != 0 {
		t.Fatalf("skills.check spent %d model calls; it must spend none", n)
	}
}

// A STALE READING IS STILL THE READING. It is about earlier bytes and the
// surface says so; hiding it would throw away something the person paid
// for, and would make "no check" and "an old check" the same state.
func TestSkillsCheckSaysNotCurrentAfterAnEdit(t *testing.T) {
	repo := &recordingSkillChecks{}
	client := &auditingClient{report: "unused"}
	h := newAuditHarnessWithRoots(t, client, nil, WithSkillChecks(repo))

	material, err := h.store.Audit("weather")
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	putCheck := content.SkillCheck{
		Name: "weather", Provenance: "installed", Verdict: "clear", Report: "ok",
		Role: "auditing", Endpoint: "local", Model: "m", Digest: material.Digest,
		CheckedAt: 1, Read: material.Read, MaxBytes: 131072,
	}
	if putErr := repo.Put(context.Background(), putCheck); putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}

	// One byte, on disk, under the record.
	path := filepath.Join(h.root, "weather", "SKILL.md")
	// #nosec G304 -- path is this test's own harness directory.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	var got skillCheckResult
	if err := checkCall(t, h, "weather", &got); err != nil {
		t.Fatalf("skills.check: %v", err)
	}
	if !got.Checked {
		t.Fatal("the check disappeared because the bytes moved")
	}
	// THE WHOLE CHECK, not one field of it: a handler that trimmed report,
	// findings, read or checkedAt on the stale branch would still pass a
	// bare Verdict comparison, because both branches build the DTO through
	// the same toSkillCheckDTO — so the assertion has to be the same
	// equality the invariant promises, not a sample of it.
	want := toSkillCheckDTO(putCheck)
	if !reflect.DeepEqual(got.Check, want) {
		t.Fatalf("the stored check changed across the digest comparison:\ngot  %+v\nwant %+v", got.Check, want)
	}
	if got.Current == nil || *got.Current {
		t.Fatalf("current = %v, want false after an edit", got.Current)
	}
}

// A BUILTIN NEVER TOUCHES THE STORE. Its bytes came with nocx itself and
// skills.audit refuses to spend a model call on one, so a builtin can never
// have a row — asserted by the store's own call count staying at zero, not
// only by the response.
func TestSkillsCheckBuiltinAnswersUncheckedWithoutTouchingTheStore(t *testing.T) {
	repo := &recordingSkillChecks{}
	client := &auditingClient{report: "unused"}
	h := newAuditHarnessWithRoots(t, client, []skill.Root{{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin}}, WithSkillChecks(repo))

	var got skillCheckResult
	if err := checkCall(t, h, "skill-authoring", &got); err != nil {
		t.Fatalf("skills.check: %v", err)
	}
	if got.Checked {
		t.Fatalf("a builtin was reported checked: %+v", got)
	}
	if n := repo.gets(); n != 0 {
		t.Fatalf("the store was read %d times for a builtin", n)
	}
}

// THE STORE BEING A STUB ANSWERS checked:false WITH NO ERROR — the same
// stance content.Stub.SkillChecks() already documents: a store that is not
// there has no check for anybody.
func TestSkillsCheckStubStoreAnswersUncheckedWithNoError(t *testing.T) {
	client := &auditingClient{report: "unused"}
	stub := content.NewStub(log.NewSlogAdapter(nil)).SkillChecks()
	h := newAuditHarnessWithRoots(t, client, nil, WithSkillChecks(stub))

	var got skillCheckResult
	if err := checkCall(t, h, "weather", &got); err != nil {
		t.Fatalf("skills.check: %v", err)
	}
	if got.Checked {
		t.Fatalf("a stub store reported a check: %+v", got)
	}
}

// NO STORE WIRED AT ALL answers the same way as a stub: skills.check must
// never mistake "nothing to read from" for a failure.
func TestSkillsCheckNoStoreWiredAnswersUnchecked(t *testing.T) {
	client := &auditingClient{report: "unused"}
	h := newAuditHarnessWithRoots(t, client, nil) // no WithSkillChecks at all

	var got skillCheckResult
	if err := checkCall(t, h, "weather", &got); err != nil {
		t.Fatalf("skills.check: %v", err)
	}
	if got.Checked {
		t.Fatalf("no store was wired and skills.check still reported a check: %+v", got)
	}
}

// THE STORE ERRORS. A Get that fails is a real failure — unlike "no row" —
// and it must reach the caller as one, distinctly from "nobody checked this".
func TestSkillsCheckErrorsWhenTheStoreFails(t *testing.T) {
	repo := &recordingSkillChecks{getFailure: content.ErrNotImplemented}
	client := &auditingClient{report: "unused"}
	h := newAuditHarnessWithRoots(t, client, nil, WithSkillChecks(repo))

	var got skillCheckResult
	err := checkCall(t, h, "weather", &got)
	if err == nil {
		t.Fatalf("skills.check answered %+v with a failing store", got)
	}
}

// THE SKILL IS GONE. There is nothing to recompose a digest against, so it
// refuses rather than inventing a "checked: false" about a name that
// resolves to nothing — the same refusal skill.Audit itself gives, and the
// store is never asked about a name that does not exist.
func TestSkillsCheckErrorsWhenTheSkillIsGone(t *testing.T) {
	repo := &recordingSkillChecks{}
	client := &auditingClient{report: "unused"}
	h := newAuditHarnessWithRoots(t, client, nil, WithSkillChecks(repo))

	var got skillCheckResult
	err := checkCall(t, h, "absent", &got)
	if err == nil {
		t.Fatalf("skills.check answered %+v for a skill no root holds", got)
	}
	if n := repo.gets(); n != 0 {
		t.Fatalf("the store was read %d times for a skill that does not exist", n)
	}
}

// NO SKILL LIBRARY WIRED AT ALL: the method is unavailable, the same
// -32601 shape skills.audit answers with no engine.
func TestSkillsCheckRefusesWhenNoSkillLibraryIsWired(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "skills.check", map[string]any{"name": "deploy"})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil {
		t.Fatalf("skills.check answered %s with no skill library wired", env.Result)
	}
	if env.Error.Code != -32601 {
		t.Fatalf("code = %d, want -32601", env.Error.Code)
	}
}

// The REAL result off the REAL socket, validated against the schema, in
// all three shapes the wire declares: nobody has checked, a stored current
// check, and a stale one. A test validating a payload it built itself would
// prove only that the struct is well-formed, not that the server sends it
// (AGENTS.md rule 5).
func TestSkillsCheck_OverTheWireConformsToContract(t *testing.T) {
	repo := &recordingSkillChecks{}
	client := &auditingClient{report: "unused"}
	h := newAuditHarnessWithRoots(t, client, nil, WithSkillChecks(repo))
	schema := loadSchema(t, "skills.check.schema.json")

	uncheckedRaw := jsonrpcCall(t, h.conn, "skills.check", map[string]any{"name": "weather"})
	var uncheckedEnv rpcEnvelope
	if err := json.Unmarshal(uncheckedRaw, &uncheckedEnv); err != nil {
		t.Fatal(err)
	}
	if uncheckedEnv.Error != nil {
		t.Fatalf("skills.check: %+v", uncheckedEnv.Error)
	}
	validateJSON(t, schema, uncheckedEnv.Result, "skills.check wire (unchecked)")

	material, err := h.store.Audit("weather")
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	// Read: nil rather than material.Read — a fake store built directly
	// against a content.SkillCheck literal round-trips exactly what was Put,
	// so this is what exercises nonNilSkillCheckReads: a nil Read here would
	// marshal to `null` against a schema that requires an array, and nothing
	// else in this file's Puts ever leaves it unset.
	if putErr := repo.Put(context.Background(), content.SkillCheck{
		Name: "weather", Provenance: "installed", Verdict: "clear",
		Report: "ok", Role: "auditing", Endpoint: "local", Model: "m",
		Digest: material.Digest, CheckedAt: 1, Read: nil, MaxBytes: 131072,
	}); putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}

	checkedRaw := jsonrpcCall(t, h.conn, "skills.check", map[string]any{"name": "weather"})
	var checkedEnv rpcEnvelope
	if unmarshalErr := json.Unmarshal(checkedRaw, &checkedEnv); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if checkedEnv.Error != nil {
		t.Fatalf("skills.check: %+v", checkedEnv.Error)
	}
	validateJSON(t, schema, checkedEnv.Result, "skills.check wire (checked, current)")

	// One byte, on disk, under the record: the same check now disagrees with
	// the bytes, which is the third shape the schema declares — checked:true,
	// current:false — and until now it was validated only from a DTO the DTO
	// test built itself, never off this socket.
	path := filepath.Join(h.root, "weather", "SKILL.md")
	// #nosec G304 -- path is this test's own harness directory.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	staleRaw := jsonrpcCall(t, h.conn, "skills.check", map[string]any{"name": "weather"})
	var staleEnv rpcEnvelope
	if unmarshalErr := json.Unmarshal(staleRaw, &staleEnv); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if staleEnv.Error != nil {
		t.Fatalf("skills.check: %+v", staleEnv.Error)
	}
	validateJSON(t, schema, staleEnv.Result, "skills.check wire (checked, stale)")

	var stale skillCheckResult
	if err := json.Unmarshal(staleEnv.Result, &stale); err != nil {
		t.Fatal(err)
	}
	if stale.Current == nil || *stale.Current {
		t.Fatalf("current = %v after an edit, want false", stale.Current)
	}
}

// The DTO marshals to something the schema accepts, in both shapes a viewer
// has to draw: unchecked, and a stale check with empty omitted/findings.
func TestSkillsCheck_DTOConformsToContract(t *testing.T) {
	s := loadSchema(t, "skills.check.schema.json")
	current := false
	for _, tc := range []struct {
		name   string
		result skillCheckResult
	}{
		{name: "unchecked", result: skillCheckResult{Name: "weather", Checked: false}},
		{
			name: "checked, stale, no omissions or findings",
			result: skillCheckResult{
				Name: "weather", Checked: true, Current: &current,
				Check: &skillCheckDTO{
					Provenance: "installed", Verdict: "clear", Report: "ok",
					Role: "auditing", Endpoint: "Local", Model: "qwen3",
					Digest: "abc123", CheckedAt: "2025-09-04T15:33:20Z",
					Read: []string{"SKILL.md"}, Omitted: []skill.AuditOmission{},
					Findings: []skill.Finding{}, MaxBytes: 131072,
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatal(err)
			}
			validateJSON(t, s, raw, "skills.check DTO")
		})
	}
}
