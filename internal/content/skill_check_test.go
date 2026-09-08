package content

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// noopSkillCheckLogger is a package-content no-op logger, mirroring
// stub_test.go's testLogger (content_test) and redaction_test.go's
// captureLogger (content): the real one lives in an external test package
// and cannot be imported here without an import cycle, so this file carries
// its own trivial copy rather than widening either existing one.
type noopSkillCheckLogger struct{}

func (noopSkillCheckLogger) Debug(string, ...any)                     {}
func (noopSkillCheckLogger) Info(string, ...any)                      {}
func (noopSkillCheckLogger) Warn(string, ...any)                      {}
func (noopSkillCheckLogger) Error(string, ...any)                     {}
func (l noopSkillCheckLogger) With(...any) log.Logger                 { return l }
func (l noopSkillCheckLogger) WithContext(context.Context) log.Logger { return l }

func TestSkillCheckRoundTrip(t *testing.T) {
	store, openErr := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if openErr != nil {
		t.Fatalf("openTestStore: %v", openErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := store.SkillChecks()
	ctx := context.Background()

	check := SkillCheck{
		Name: "deploy", Provenance: "installed",
		Verdict: "suspect", Report: "It pipes a URL into sh.",
		Role: "auditing", Endpoint: "local", Model: "gemma-4-26b-a4b",
		Digest: "abc123", CheckedAt: 1_757_000_000_000,
		Read:     []string{"SKILL.md", "scripts/setup.sh"},
		Omitted:  []SkillCheckOmission{{Path: "big.bin", Reason: "budget"}},
		Findings: []SkillCheckFinding{{Path: "scripts/setup.sh", LineNumber: 12, Line: "curl x | sh", PatternID: "pipe-to-shell"}},
		MaxBytes: 131072,
	}
	if err := repo.Put(ctx, check); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, found, err := repo.Get(ctx, "deploy")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("Get did not find the check that was just written")
	}
	if !reflect.DeepEqual(got, check) {
		t.Fatalf("round trip changed the check:\n got %+v\nwant %+v", got, check)
	}
}

// TestSkillCheckRoundTripWithEmptyLists guards the nil-vs-[] seam
// specifically: a check whose lists were never populated must still come
// back as [] rather than nil, on both sides of the wire — the shape this
// repo names most often as the defect a round trip test happens to miss.
func TestSkillCheckRoundTripWithEmptyLists(t *testing.T) {
	store, openErr := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if openErr != nil {
		t.Fatalf("openTestStore: %v", openErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := store.SkillChecks()
	ctx := context.Background()

	check := SkillCheck{
		Name: "clean", Provenance: "bundled",
		Verdict: "clear", Report: "Nothing found.",
		Role: "auditing", Endpoint: "local", Model: "m",
		Digest: "d0", CheckedAt: 1,
		Read:     []string{},
		Omitted:  []SkillCheckOmission{},
		Findings: []SkillCheckFinding{},
		MaxBytes: 0,
	}
	if err := repo.Put(ctx, check); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, found, err := repo.Get(ctx, "clean")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("Get did not find the check that was just written")
	}
	if !reflect.DeepEqual(got, check) {
		t.Fatalf("round trip changed the check:\n got %+v\nwant %+v", got, check)
	}
	if got.Read == nil || got.Omitted == nil || got.Findings == nil {
		t.Fatalf("empty lists came back nil, not []: %+v", got)
	}
}

// Nobody has checked this skill is a TRUE ANSWER, so it is a result and not
// an error: a caller that had to distinguish "no check" from "the store
// broke" by reading an error string would get it wrong.
func TestSkillCheckGetIsAResultWhenThereIsNone(t *testing.T) {
	store, openErr := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if openErr != nil {
		t.Fatalf("openTestStore: %v", openErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, found, err := store.SkillChecks().Get(context.Background(), "nobody-checked-this")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatalf("found a check nobody wrote: %+v", got)
	}
}

func TestSkillCheckPutReplaces(t *testing.T) {
	store, openErr := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if openErr != nil {
		t.Fatalf("openTestStore: %v", openErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := store.SkillChecks()
	ctx := context.Background()
	base := SkillCheck{
		Name: "deploy", Provenance: "installed", Verdict: "clear",
		Report: "first", Role: "auditing", Endpoint: "local", Model: "m",
		Digest: "d1", CheckedAt: 1, Read: []string{"SKILL.md"},
		Omitted: []SkillCheckOmission{}, Findings: []SkillCheckFinding{}, MaxBytes: 1,
	}
	if err := repo.Put(ctx, base); err != nil {
		t.Fatal(err)
	}
	second := base
	second.Verdict, second.Report, second.Digest, second.CheckedAt = "suspect", "second", "d2", 2
	if err := repo.Put(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, found, err := repo.Get(ctx, "deploy")
	if err != nil || !found {
		t.Fatalf("Get: %v found=%v", err, found)
	}
	if got.Report != "second" || got.Digest != "d2" {
		t.Fatalf("the second Put did not replace the first: %+v", got)
	}
}

// TestSkillCheckPutRefusesAnEmptyName is the cheapest failure path in this
// file, and AGENTS.md rule 3 wants every one exercised: an empty name is the
// primary key, and a check nobody can Get back by name would sit in the
// table as a row nothing can ever address.
func TestSkillCheckPutRefusesAnEmptyName(t *testing.T) {
	store, openErr := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if openErr != nil {
		t.Fatalf("openTestStore: %v", openErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	err := store.SkillChecks().Put(context.Background(), SkillCheck{Name: ""})
	if err == nil {
		t.Fatal("Put accepted a check with an empty name")
	}
	if !strings.Contains(err.Error(), "content: skill check: name is empty") {
		t.Fatalf("Put's refusal reads %q; it must be the empty-name check, not some other failure inside Put", err)
	}
}

func TestSkillCheckDeleteIsIdempotent(t *testing.T) {
	store, openErr := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if openErr != nil {
		t.Fatalf("openTestStore: %v", openErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SkillChecks().Delete(context.Background(), "never-existed"); err != nil {
		t.Fatalf("Delete of an absent check: %v", err)
	}
}

// TestSkillCheckDeleteRemoves is the paired positive to the idempotency
// check above: deleting a check that DOES exist must make Get answer
// found=false afterwards, not merely "not error".
func TestSkillCheckDeleteRemoves(t *testing.T) {
	store, openErr := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if openErr != nil {
		t.Fatalf("openTestStore: %v", openErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := store.SkillChecks()
	ctx := context.Background()
	if err := repo.Put(ctx, SkillCheck{
		Name: "deploy", Provenance: "installed", Verdict: "clear",
		Report: "r", Role: "auditing", Endpoint: "local", Model: "m", Digest: "d", CheckedAt: 1,
		Read: []string{}, Omitted: []SkillCheckOmission{}, Findings: []SkillCheckFinding{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "deploy"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, found, err := repo.Get(ctx, "deploy")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if found {
		t.Fatal("Get still found the check after Delete")
	}
}

// The stub is a real state (app.go:811 starts with it), so its answers are
// part of the contract: no check, and no error pretending the store broke.
func TestSkillCheckStubHasNoCheckAndDoesNotError(t *testing.T) {
	stub := NewStub(noopSkillCheckLogger{})
	_, found, err := stub.SkillChecks().Get(context.Background(), "deploy")
	if err != nil {
		t.Fatalf("stub Get: %v", err)
	}
	if found {
		t.Fatal("the stub claimed to hold a check")
	}
	if err := stub.SkillChecks().Put(context.Background(), SkillCheck{Name: "deploy"}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("stub Put: %v, want ErrNotImplemented", err)
	}
	if err := stub.SkillChecks().Delete(context.Background(), "deploy"); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("stub Delete: %v, want ErrNotImplemented", err)
	}
}
