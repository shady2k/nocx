package agenttools

// What a file tool can actually DO under the grant the product mints
// (nocx-cd6vp). Every capability test before this one built its own fixture
// grant over a t.TempDir(), so the whole suite was green while files.read,
// files.edit and files.create refused every file in the shipped app: the run
// fence is "/" (ADR-0028 decision 4, amended 2026-08-26) and the containment
// check could not be satisfied by it.
//
// The grants here are therefore minted through content.EffectPolicy.AsGrant
// with transport.runGrantFor's own five fence literals — the offer layer is
// not what these assert, execution is (AGENTS.md testing rule 1).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/hashline"
)

// runFencePolicy is the matrix with the two file-tool rows permitting, and
// the path selector a person expressed on both. No selector means the row
// applies to the whole fence, which is the product's default.
func runFencePolicy(pathScopes ...string) content.EffectPolicy {
	row := func() content.EffectRow {
		r := content.EffectRow{Decision: content.DecisionPermit}
		for _, p := range pathScopes {
			r.Scopes = append(r.Scopes, content.GrantScope{Kind: content.ResourcePath, ID: p})
		}
		return r
	}
	return content.EffectPolicy{Observe: row(), MutateReversible: row()}
}

// runFenceGrant mints the grant the shipped product mints: the five literal
// run-fence scopes of transport.runGrantFor, through the same AsGrant seam.
// The path member is "/" — the amendment's "every readable file on the
// machine" — and the row selectors are where narrowing lives.
func runFenceGrant(policy content.EffectPolicy) content.Grant {
	return policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: "/"},
		{Kind: content.ResourceContent, ID: "note"},
		{Kind: content.ResourceContent, ID: "snippet"},
		{Kind: content.ResourceDestination, ID: "*"},
	})
}

// pathScopeGrant is a grant with NO run fence whose file-tool rows select
// exactly these paths. It is the fixture shape the capability tests need now
// that the capability's roots come from the effect row rather than from the
// grant's derived scope union: a bare content.Grant{Scopes: …} expresses
// declaration coverage, never authority for an effect.
func pathScopeGrant(paths ...string) content.Grant {
	return runFencePolicy(paths...).AsGrant(nil)
}

// escapeFixture builds a directory a row is scoped to, a directory outside
// it holding a secret, a real file inside, and a symlink inside that
// resolves outside. It is the shape the trap in this fix is about: with
// containment made universally true, exact() alone would authorize the
// symlink, because exact() matches the canonicalization of the path the
// model SPELLED — which is the target.
type escapeFixture struct {
	root   string
	real   string
	link   string
	secret string
}

const secretBytes = "the-secret-bytes\n"

func newEscapeFixture(t *testing.T) escapeFixture {
	t.Helper()
	root := t.TempDir()
	outside := t.TempDir()
	f := escapeFixture{
		root:   root,
		real:   filepath.Join(root, "real.txt"),
		link:   filepath.Join(root, "link"),
		secret: filepath.Join(outside, "secret.txt"),
	}
	if err := os.WriteFile(f.real, []byte("real\n"), 0o600); err != nil {
		t.Fatalf("WriteFile real: %v", err)
	}
	if err := os.WriteFile(f.secret, []byte(secretBytes), 0o600); err != nil {
		t.Fatalf("WriteFile secret: %v", err)
	}
	if err := os.Symlink(f.secret, f.link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	return f
}

func pathRef(path string) []ResourceRef {
	return []ResourceRef{{Kind: content.ResourcePath, ID: path}}
}

// ── files.read ───────────────────────────────────────────────────────────

// TestNarrowFilesRead_ProductionFenceReadsARealFile is the probe that found
// nocx-cd6vp, kept as a test: under the grant runGrantFor mints, files.read
// returns the file's content rather than ErrOutOfScope.
func TestNarrowFilesRead_ProductionFenceReadsARealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real.txt")
	if err := os.WriteFile(path, []byte("real\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	capability, err := narrowFilesRead(runFenceGrant(runFencePolicy()), pathRef(path), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesRead: %v", err)
	}
	reader, ok := capability.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedReader", capability)
	}
	got, err := reader.Read(context.Background(), path, 64<<10)
	if err != nil {
		t.Fatalf("files.read under the production run fence: %v", err)
	}
	if got.Text != "real\n" {
		t.Fatalf("files.read returned %q, want the file's content", got.Text)
	}
}

// TestNarrowFilesRead_RowScopedFenceRefusesASymlinkEscape is the other half:
// with the observe row scoped to one directory, a symlink inside it that
// resolves outside is refused by canonical identity, and none of the
// target's bytes come back.
func TestNarrowFilesRead_RowScopedFenceRefusesASymlinkEscape(t *testing.T) {
	f := newEscapeFixture(t)
	grant := runFenceGrant(runFencePolicy(f.root))

	capability, err := narrowFilesRead(grant, pathRef(f.link), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesRead: %v", err)
	}
	reader, ok := capability.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedReader", capability)
	}
	got, err := reader.Read(context.Background(), f.link, 64<<10)
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("symlink-escape read error = %v, want ErrOutOfScope", err)
	}
	if strings.Contains(got.Text, strings.TrimSpace(secretBytes)) {
		t.Fatalf("symlink-escape read returned the target's bytes: %q", got.Text)
	}
}

// TestNarrowFilesRead_RowScopedFenceReadsAFileInsideTheRow is the paired
// success for the refusal above (AGENTS.md testing rule 3): the same row
// scope, a real file inside it, and the read works.
func TestNarrowFilesRead_RowScopedFenceReadsAFileInsideTheRow(t *testing.T) {
	f := newEscapeFixture(t)
	capability, err := narrowFilesRead(runFenceGrant(runFencePolicy(f.root)), pathRef(f.real), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesRead: %v", err)
	}
	reader, ok := capability.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedReader", capability)
	}
	got, err := reader.Read(context.Background(), f.real, 64<<10)
	if err != nil {
		t.Fatalf("in-row read: %v", err)
	}
	if got.Text != "real\n" {
		t.Fatalf("in-row read = %q, want the file's content", got.Text)
	}
}

// ── files.edit ───────────────────────────────────────────────────────────

func TestNarrowFilesEdit_ProductionFenceEditsARealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	capability, err := narrowFilesEdit(runFenceGrant(runFencePolicy()), pathRef(path), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesEdit: %v", err)
	}
	editor, ok := capability.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", capability)
	}
	snapshot, err := hashline.Read(path, 64<<10)
	if err != nil {
		t.Fatalf("hashline.Read: %v", err)
	}
	if _, editErr := editor.Edit(context.Background(), path, snapshot.Revision, "PUT 1.=1:\n+after"); editErr != nil {
		t.Fatalf("files.edit under the production run fence: %v", editErr)
	}
	body, err := os.ReadFile(path) //nolint:gosec // a path this test just created under t.TempDir()
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), "after") {
		t.Fatalf("file after the edit = %q, want the patched line", string(body))
	}
}

func TestNarrowFilesEdit_RowScopedFenceRefusesASymlinkEscape(t *testing.T) {
	f := newEscapeFixture(t)
	capability, err := narrowFilesEdit(runFenceGrant(runFencePolicy(f.root)), pathRef(f.link), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesEdit: %v", err)
	}
	editor, ok := capability.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", capability)
	}
	// The revision is minted from the link so the refusal cannot be a stale
	// revision wearing a scope refusal's clothes.
	snapshot, err := hashline.Read(f.link, 64<<10)
	if err != nil {
		t.Fatalf("hashline.Read: %v", err)
	}
	if _, editErr := editor.Edit(context.Background(), f.link, snapshot.Revision, "PUT 1.=1:\n+overwritten"); !errors.Is(editErr, filesystem.ErrOutOfScope) {
		t.Fatalf("symlink-escape edit error = %v, want ErrOutOfScope", editErr)
	}
	body, err := os.ReadFile(f.secret)
	if err != nil {
		t.Fatalf("ReadFile secret: %v", err)
	}
	if string(body) != secretBytes {
		t.Fatalf("the file outside the row changed to %q", string(body))
	}
}

func TestNarrowFilesEdit_RowScopedFenceEditsAFileInsideTheRow(t *testing.T) {
	f := newEscapeFixture(t)
	capability, err := narrowFilesEdit(runFenceGrant(runFencePolicy(f.root)), pathRef(f.real), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesEdit: %v", err)
	}
	editor, ok := capability.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", capability)
	}
	snapshot, err := hashline.Read(f.real, 64<<10)
	if err != nil {
		t.Fatalf("hashline.Read: %v", err)
	}
	if _, err := editor.Edit(context.Background(), f.real, snapshot.Revision, "PUT 1.=1:\n+edited"); err != nil {
		t.Fatalf("in-row edit: %v", err)
	}
}

// ── files.create ─────────────────────────────────────────────────────────

func TestNarrowFilesCreate_ProductionFenceCreatesAFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "new.txt")
	capability, err := narrowFilesCreate(runFenceGrant(runFencePolicy()), pathRef(target), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesCreate: %v", err)
	}
	editor, ok := capability.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", capability)
	}
	if _, err := editor.Create(context.Background(), target, "created\n"); err != nil {
		t.Fatalf("files.create under the production run fence: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("files.create under the production run fence wrote nothing: %v", err)
	}
}

func TestNarrowFilesCreate_RowScopedFenceRefusesASymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	dirLink := filepath.Join(root, "dirlink")
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	target := filepath.Join(dirLink, "new.txt")

	capability, err := narrowFilesCreate(runFenceGrant(runFencePolicy(root)), pathRef(target), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesCreate: %v", err)
	}
	editor, ok := capability.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", capability)
	}
	if _, err := editor.Create(context.Background(), target, "escaped\n"); !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("symlink-escape create error = %v, want ErrOutOfScope", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink-escape create wrote outside the row: stat err = %v", err)
	}
}

func TestNarrowFilesCreate_RowScopedFenceCreatesInsideTheRow(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "new.txt")
	capability, err := narrowFilesCreate(runFenceGrant(runFencePolicy(root)), pathRef(target), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesCreate: %v", err)
	}
	editor, ok := capability.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", capability)
	}
	if _, err := editor.Create(context.Background(), target, "created\n"); err != nil {
		t.Fatalf("in-row create: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("in-row create wrote nothing: %v", err)
	}
}

// ── the roots themselves ─────────────────────────────────────────────────

// TestFilesystemRoots_AreTheEffectRowsPathScopes asserts where the roots come
// from, per effect. The observe row here selects a directory and the
// mutate-reversible row selects nothing, so the two rows must disagree — a
// capability reading the grant's derived scope union would give both "/".
func TestFilesystemRoots_AreTheEffectRowsPathScopes(t *testing.T) {
	dir := t.TempDir()
	observeOnly := content.EffectPolicy{
		Observe:          content.EffectRow{Decision: content.DecisionPermit, Scopes: []content.GrantScope{{Kind: content.ResourcePath, ID: dir}}},
		MutateReversible: content.EffectRow{Decision: content.DecisionPermit},
	}
	grant := runFenceGrant(observeOnly)

	if got := filesystemRoots(grant, filesReadRowEffect); len(got) != 1 || got[0] != dir {
		t.Fatalf("observe roots = %v, want the row's own path scope %v", got, []string{dir})
	}
	if got := filesystemRoots(grant, filesWriteRowEffect); len(got) != 1 || got[0] != "/" {
		t.Fatalf("mutate-reversible roots = %v, want the unnarrowed run fence [/]", got)
	}
	// And the union the grant derives for declaration coverage is NOT what a
	// capability may use: it carries "/" whatever the observe row selected.
	fenceRoots := make([]string, 0, len(grant.Scopes))
	for _, scope := range grant.Scopes {
		if scope.Kind == content.ResourcePath {
			fenceRoots = append(fenceRoots, scope.ID)
		}
	}
	if len(fenceRoots) != 1 || fenceRoots[0] != "/" {
		t.Fatalf("grant.Scopes paths = %v, want the run fence [/] — the premise this test rests on", fenceRoots)
	}
}

// TestNarrowedCapabilityRootsAreCanonicalized is the roots' other property,
// asserted on the constructed capabilities rather than on the slice: a row
// scoped to a SYMLINK to a directory authorizes the directory's canonical
// identity, so a file spelled through the real directory is inside the
// scope. A lexical root would refuse it.
func TestNarrowedCapabilityRootsAreCanonicalized(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	aliasRoot := filepath.Join(base, "alias-root")
	if err := os.MkdirAll(realRoot, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	inside := filepath.Join(realRoot, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("WriteFile outside: %v", err)
	}
	grant := runFenceGrant(runFencePolicy(aliasRoot))

	readCap, err := narrowFilesRead(grant, pathRef(inside), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesRead: %v", err)
	}
	reader, ok := readCap.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedReader", readCap)
	}
	if _, readErr := reader.Read(context.Background(), inside, 64<<10); readErr != nil {
		t.Fatalf("read through the canonical spelling of a symlinked row scope: %v", readErr)
	}

	outsideCap, err := narrowFilesRead(grant, pathRef(outside), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesRead outside: %v", err)
	}
	outsideReader, ok := outsideCap.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedReader", outsideCap)
	}
	if _, readErr := outsideReader.Read(context.Background(), outside, 64<<10); !errors.Is(readErr, filesystem.ErrOutOfScope) {
		t.Fatalf("read outside the row scope error = %v, want ErrOutOfScope", readErr)
	}

	editCap, err := narrowFilesEdit(grant, pathRef(inside), RunContext{})
	if err != nil {
		t.Fatalf("narrowFilesEdit: %v", err)
	}
	editor, ok := editCap.(*filesystem.ScopedEditor)
	if !ok {
		t.Fatalf("capability = %T, want *filesystem.ScopedEditor", editCap)
	}
	snapshot, err := hashline.Read(inside, 64<<10)
	if err != nil {
		t.Fatalf("hashline.Read: %v", err)
	}
	if _, err := editor.Edit(context.Background(), inside, snapshot.Revision, "PUT 1.=1:\n+edited"); err != nil {
		t.Fatalf("edit through the canonical spelling of a symlinked row scope: %v", err)
	}
}

// TestFileToolNarrowEffectsMatchDeclarations keeps the effect constants in
// narrow.go from drifting away from the declaration rows they name. The
// constructors cannot read the table back — the table's initializer names
// them — so this is the check that stands in for that.
func TestFileToolNarrowEffectsMatchDeclarations(t *testing.T) {
	want := map[string]content.Effect{
		"files.read":   filesReadRowEffect,
		"files.edit":   filesWriteRowEffect,
		"files.create": filesWriteRowEffect,
	}
	seen := make(map[string]bool, len(want))
	for _, d := range declarations {
		expected, ok := want[d.Name]
		if !ok {
			continue
		}
		seen[d.Name] = true
		if got := content.WorstEffect(d.Effect); got != expected {
			t.Fatalf("%s declares effect %q; its capability narrows within row %q", d.Name, got, expected)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("declaration %q is absent from the table; its narrow constructor names an effect row for a tool that no longer exists", name)
		}
	}
}
