package apiimport

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apibind"
	"github.com/shady2k/nocx/internal/apicoll"
)

// ---- the injected seams ----

type fsOp struct{ Op, Name string }

// probeFS is a real filesystem with one lie in it. Delegating to the real
// one is the point: "after the failure the destination does not exist" is
// then a question asked of the disk, not of a model of the disk that could
// be wrong in the same direction as the code.
type probeFS struct {
	inner FS
	log   []fsOp
	fail  func(op, name string, seq int) error
}

func newProbeFS() *probeFS { return &probeFS{inner: NewOSFS()} }

func (p *probeFS) check(op, name string) error {
	seq := len(p.log)
	p.log = append(p.log, fsOp{op, name})
	if p.fail == nil {
		return nil
	}
	return p.fail(op, name, seq)
}

func (p *probeFS) MkdirTemp(dir, pattern string) (string, error) {
	if err := p.check("MkdirTemp", dir); err != nil {
		return "", err
	}
	return p.inner.MkdirTemp(dir, pattern)
}

func (p *probeFS) MkdirAll(path string, perm os.FileMode) error {
	if err := p.check("MkdirAll", path); err != nil {
		return err
	}
	return p.inner.MkdirAll(path, perm)
}

func (p *probeFS) Lstat(name string) (fs.FileInfo, error) {
	if err := p.check("Lstat", name); err != nil {
		return nil, err
	}
	return p.inner.Lstat(name)
}

func (p *probeFS) WriteFile(name string, b []byte, perm os.FileMode) error {
	if err := p.check("WriteFile", name); err != nil {
		return err
	}
	return p.inner.WriteFile(name, b, perm)
}

func (p *probeFS) Sync(name string) error {
	if err := p.check("Sync", name); err != nil {
		return err
	}
	return p.inner.Sync(name)
}

func (p *probeFS) Rename(old, new string) error {
	if err := p.check("Rename", old); err != nil {
		return err
	}
	return p.inner.Rename(old, new)
}

func (p *probeFS) RemoveAll(path string) error {
	if err := p.check("RemoveAll", path); err != nil {
		return err
	}
	return p.inner.RemoveAll(path)
}

func (p *probeFS) ops(op string) []fsOp {
	var out []fsOp
	for _, o := range p.log {
		if o.Op == op {
			out = append(out, o)
		}
	}
	return out
}

func (p *probeFS) indexOf(op string) int {
	for i, o := range p.log {
		if o.Op == op {
			return i
		}
	}
	return -1
}

// recordingBinder is the BindWriter. It holds what it was given so the test
// can assert the value went HERE and nowhere else.
type recordingBinder struct {
	keys   []apibind.Key
	values []string
	fail   error
}

func (b *recordingBinder) Bind(ctx context.Context, k apibind.Key, value []byte) error {
	if b.fail != nil {
		return b.fail
	}
	b.keys = append(b.keys, k)
	b.values = append(b.values, string(value))
	return nil
}

func (b *recordingBinder) valueFor(variable string) (string, bool) {
	for i, k := range b.keys {
		if k.Variable == variable {
			return b.values[i], true
		}
	}
	return "", false
}

// ---- helpers ----

func destUnder(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "collection")
}

// walkFiles returns every file under root and its contents.
func walkFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // walks a t.TempDir() this test just wrote
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists (err = %v) after a failed import", path, err)
	}
}

// assertNoStaging fails if anything is left beside the destination. A
// staging directory that survives is litter the next import trips over.
func assertNoStaging(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read %s: %v", parent, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".apiimport") {
			t.Fatalf("staging directory %q survived", e.Name())
		}
	}
}

// ---- the happy path ----

func TestImportIntoPostmanWritesTheFolder(t *testing.T) {
	dest := destUnder(t)
	p := newProbeFS()
	b := &recordingBinder{}

	unsup, err := ImportInto(t.Context(), p, b, dest, strings.NewReader(postmanFixture), apicoll.Route{})
	if err != nil {
		t.Fatalf("ImportInto: %v", err)
	}
	if len(unsup) == 0 {
		t.Fatal("this fixture has features we do not carry, and none were reported")
	}

	files := walkFiles(t, dest)
	// The manifest the READER looks for, under the name it looks for it
	// under: apicoll.ManifestName, not a name this package chose.
	if _, ok := files[apicoll.ManifestName]; !ok {
		t.Fatalf("no %s; have %v", apicoll.ManifestName, keysOf(files))
	}
	if _, ok := files["environments/default.json"]; !ok {
		t.Fatalf("no environments/default.json; have %v", keysOf(files))
	}
	// A folder became a directory and the request in it became a file.
	found := false
	for name := range files {
		if strings.HasPrefix(name, "Users/") && strings.HasSuffix(name, ".json") {
			found = true
			if fi, err := os.Stat(filepath.Join(dest, "Users")); err != nil || !fi.IsDir() {
				t.Fatalf("Users is not a directory: %v", err)
			}
		}
	}
	if !found {
		t.Fatalf("the Users folder produced no file; have %v", keysOf(files))
	}

	// {{baseUrl}} survives as {{baseUrl}}.
	if !strings.Contains(strings.Join(valuesOf(files), "\n"), "{{baseUrl}}") {
		t.Fatal("no file carries {{baseUrl}}")
	}

	// THE RULE. A walk of every written file finds neither the secret
	// value nor any identifier for it.
	for name, body := range files {
		if strings.Contains(body, pmSecretValue) {
			t.Fatalf("%s carries the secret VALUE", name)
		}
		if strings.Contains(body, pmSecretID) {
			t.Fatalf("%s carries Postman's IDENTIFIER for the secret", name)
		}
		if strings.Contains(body, "11112222-3333-4444-5555-666677778888") {
			t.Fatalf("%s carries the source document's id", name)
		}
	}
	// And the name IS there, because a file names a variable.
	if !strings.Contains(files["environments/default.json"], "apiToken") {
		t.Fatalf("the environment does not declare the variable: %s", files["environments/default.json"])
	}

	// The value went to the binder, keyed by (collection, environment,
	// variable) — the triple, so two collections do not share a value.
	got, ok := b.valueFor("apiToken")
	if !ok {
		t.Fatalf("apiToken was never bound; bound %+v", b.keys)
	}
	if got != pmSecretValue {
		t.Fatalf("bound value = %q", got)
	}
	for _, k := range b.keys {
		if k.Collection != dest {
			t.Fatalf("binding key collection = %q, want %q", k.Collection, dest)
		}
		if k.Environment != "default" {
			t.Fatalf("binding key environment = %q", k.Environment)
		}
	}
}

func keysOfAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// The other entrance, through the same writer: a curl line whose token
// reaches the binder and no file.
func TestImportIntoCurlLine(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlLXZhbHVl" //nolint:gosec // a synthetic token: the test exists to prove this exact string reaches no file
	dest := destUnder(t)
	p := newProbeFS()
	b := &recordingBinder{}

	line := `curl -X POST 'https://api.acme.test/users' -H 'Authorization: Bearer ` + token + `' -d '{"a":1}'`
	if _, err := ImportInto(t.Context(), p, b, dest, strings.NewReader(line), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}
	files := walkFiles(t, dest)
	if len(files) < 2 {
		t.Fatalf("files = %v", keysOf(files))
	}
	for name, body := range files {
		if strings.Contains(body, token) {
			t.Fatalf("%s carries the token", name)
		}
	}
	if len(b.keys) != 1 {
		t.Fatalf("bound %+v, want exactly the token", b.keys)
	}
	if b.values[0] != token {
		t.Fatalf("bound value = %q", b.values[0])
	}
	if b.keys[0].Collection != dest {
		t.Fatalf("key = %+v", b.keys[0])
	}
	// The environment declares the variable the request names, or the
	// send has nothing to resolve.
	env := files["environments/default.json"]
	if !strings.Contains(env, b.keys[0].Variable) {
		t.Fatalf("the environment does not declare %q: %s", b.keys[0].Variable, env)
	}
}

// ---- atomicity (§12.2), each with the injected FS ----

func TestImportIntoStagesInsideTheDestinationsParent(t *testing.T) {
	dest := destUnder(t)
	p := newProbeFS()
	if _, err := ImportInto(t.Context(), p, &recordingBinder{}, dest, strings.NewReader(postmanFixture), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}
	mk := p.ops("MkdirTemp")
	if len(mk) != 1 {
		t.Fatalf("MkdirTemp called %d times", len(mk))
	}
	// Across filesystems a rename is a copy and not atomic, so the staging
	// directory has to share a filesystem with the destination — which is
	// what "inside the destination's parent" buys.
	if mk[0].Name != filepath.Dir(dest) {
		t.Fatalf("staged in %q, want the destination's parent %q", mk[0].Name, filepath.Dir(dest))
	}
}

func TestImportIntoRefusesAnExistingDestination(t *testing.T) {
	dest := destUnder(t)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	const marker = "do not replace me"
	if err := os.WriteFile(filepath.Join(dest, "existing.json"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}

	p := newProbeFS()
	b := &recordingBinder{}
	_, err := ImportInto(t.Context(), p, b, dest, strings.NewReader(postmanFixture), apicoll.Route{})
	if err == nil {
		t.Fatal("an existing destination was accepted")
	}
	// Refused BEFORE anything is staged. Leaning on the rename to fail is
	// not a refusal: whether rename(2) replaces an empty directory is a
	// property of the filesystem, so the check has to be ours, and the
	// evidence that it is ours is that no file was written at all.
	if n := len(p.ops("MkdirTemp")) + len(p.ops("WriteFile")); n != 0 {
		t.Fatalf("the import did %d filesystem operations before refusing: %+v", n, p.log)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want it to say the destination already exists", err)
	}
	// Refused, not replaced.
	got, readErr := os.ReadFile(filepath.Join(dest, "existing.json")) //nolint:gosec // a path this test minted under t.TempDir()
	if readErr != nil || string(got) != marker {
		t.Fatalf("the existing collection was disturbed: %q %v", got, readErr)
	}
	if len(walkFiles(t, dest)) != 1 {
		t.Fatalf("files under dest = %v", keysOf(walkFiles(t, dest)))
	}
	if len(b.keys) != 0 {
		t.Fatalf("a refused import still bound %+v", b.keys)
	}
	assertNoStaging(t, filepath.Dir(dest))

	// An EMPTY existing directory is the case the rename does not catch
	// for us: rename(2) onto an empty directory succeeds on Linux, so
	// without the check the import would silently take over a folder the
	// user had made and was about to fill. Refused, not replaced.
	empty := filepath.Join(t.TempDir(), "collection")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	pe := newProbeFS()
	if _, err := ImportInto(t.Context(), pe, &recordingBinder{}, empty, strings.NewReader(postmanFixture), apicoll.Route{}); err == nil {
		t.Fatal("an existing EMPTY destination was accepted")
	}
	if n := len(pe.ops("MkdirTemp")) + len(pe.ops("WriteFile")); n != 0 {
		t.Fatalf("an empty existing destination was staged into: %+v", pe.log)
	}
	if files := walkFiles(t, empty); len(files) != 0 {
		t.Fatalf("the empty directory was filled anyway: %v", keysOf(files))
	}
	assertNoStaging(t, filepath.Dir(empty))

	// A symlink at the destination is refused too: Lstat rather than Stat,
	// so a link pointing at somewhere else is not read through.
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "collection")
	target := filepath.Join(linkDir, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	pl := newProbeFS()
	if _, err := ImportInto(t.Context(), pl, &recordingBinder{}, link, strings.NewReader(postmanFixture), apicoll.Route{}); err == nil {
		t.Fatal("a symlink at the destination was accepted")
	}
	if n := len(pl.ops("MkdirTemp")) + len(pl.ops("WriteFile")); n != 0 {
		t.Fatalf("a symlinked destination was staged into: %+v", pl.log)
	}
	if files := walkFiles(t, target); len(files) != 0 {
		t.Fatalf("the symlink target was written through: %v", keysOf(files))
	}

	// And a file where the collection should go is refused too, not
	// silently treated as absent.
	dest2 := filepath.Join(t.TempDir(), "collection")
	if err := os.WriteFile(dest2, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportInto(t.Context(), newProbeFS(), &recordingBinder{}, dest2, strings.NewReader(postmanFixture), apicoll.Route{}); err == nil {
		t.Fatal("a file at the destination was accepted")
	}
}

// The crash window §12.2 names is "the rename landed and the contents did
// not". These are the syncs that close it.
func TestImportIntoSyncsFilesTheStagingDirectoryAndThenTheParent(t *testing.T) {
	dest := destUnder(t)
	p := newProbeFS()
	if _, err := ImportInto(t.Context(), p, &recordingBinder{}, dest, strings.NewReader(postmanFixture), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}

	renameAt := p.indexOf("Rename")
	if renameAt < 0 {
		t.Fatal("nothing was renamed")
	}
	staging := ""
	for _, o := range p.log {
		if o.Op == "Rename" {
			staging = o.Name
		}
	}

	// Every file is synced, and before the rename.
	written := map[string]bool{}
	for i, o := range p.log {
		if o.Op != "WriteFile" {
			continue
		}
		if i >= renameAt {
			t.Fatalf("%s was written after the rename", o.Name)
		}
		written[o.Name] = true
	}
	if len(written) < 3 {
		t.Fatalf("only %d files written", len(written))
	}
	for i, o := range p.log {
		if o.Op == "Sync" && written[o.Name] {
			delete(written, o.Name)
			if i >= renameAt {
				t.Fatalf("%s was synced after the rename", o.Name)
			}
		}
	}
	if len(written) != 0 {
		t.Fatalf("written but never synced: %v", keysOfBool(written))
	}

	// The staging directory itself is synced, and last before the rename.
	stagingSynced := -1
	for i, o := range p.log {
		if o.Op == "Sync" && o.Name == staging {
			stagingSynced = i
		}
	}
	if stagingSynced < 0 {
		t.Fatalf("the staging directory %q was never synced", staging)
	}
	if stagingSynced > renameAt {
		t.Fatal("the staging directory was synced after the rename")
	}

	// The parent is synced after the rename: without it the directory
	// entry for the collection is not durable.
	parentSynced := false
	for i, o := range p.log {
		if o.Op == "Sync" && o.Name == filepath.Dir(dest) && i > renameAt {
			parentSynced = true
		}
	}
	if !parentSynced {
		t.Fatalf("the parent was not synced after the rename; log = %+v", p.log)
	}
}

func keysOfBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The four injected failures §12.2 asks for, and one more: the bind. After
// every one of them the destination does not exist.
//
// Each fail function is built fresh per case, so "the third file" counts
// this run's writes and not the table's.
func TestImportIntoLeavesNoDestinationWhenAStepFails(t *testing.T) {
	injected := errors.New("injected")

	cases := []struct {
		name string
		// build returns the FS injector for this run; dest is known only
		// once the subtest has its own temp directory.
		build   func(dest string) func(op, name string, seq int) error
		bindErr error
	}{
		{
			name: "the third file",
			build: func(string) func(string, string, int) error {
				n := 0
				return func(op, name string, seq int) error {
					if op != "WriteFile" {
						return nil
					}
					n++
					if n == 3 {
						return injected
					}
					return nil
				}
			},
		},
		{
			name: "the first sync",
			build: func(string) func(string, string, int) error {
				return func(op, name string, seq int) error {
					if op == "Sync" {
						return injected
					}
					return nil
				}
			},
		},
		{
			name: "the sync of the staging directory",
			build: func(string) func(string, string, int) error {
				return func(op, name string, seq int) error {
					if op == "Sync" && strings.Contains(filepath.Base(name), ".apiimport") {
						return injected
					}
					return nil
				}
			},
		},
		{
			name: "the rename",
			build: func(string) func(string, string, int) error {
				return func(op, name string, seq int) error {
					if op == "Rename" {
						return injected
					}
					return nil
				}
			},
		},
		{
			// The parent sync is the only Sync after the rename, so failing
			// it is failing once the collection has ALREADY arrived.
			name: "the sync after the rename",
			build: func(dest string) func(string, string, int) error {
				return func(op, name string, seq int) error {
					if op == "Sync" && name == filepath.Dir(dest) {
						return injected
					}
					return nil
				}
			},
		},
		{
			name:    "the binding",
			bindErr: injected,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := destUnder(t)
			p := newProbeFS()
			if tc.build != nil {
				p.fail = tc.build(dest)
			}
			b := &recordingBinder{fail: tc.bindErr}

			_, err := ImportInto(t.Context(), p, b, dest, strings.NewReader(postmanFixture), apicoll.Route{})
			if err == nil {
				t.Fatal("the failure was swallowed")
			}
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v, want the injected one wrapped", err)
			}
			assertGone(t, dest)
			assertNoStaging(t, filepath.Dir(dest))
			if tc.bindErr == nil && len(b.keys) != 0 {
				t.Fatalf("a failed import bound %+v", b.keys)
			}
		})
	}
}

// And the paired success (AGENTS.md testing rule 3): on an ordinary machine
// with nothing injected, all of it works.
func TestImportIntoSucceedsWithNothingInjected(t *testing.T) {
	dest := destUnder(t)
	p := newProbeFS()
	b := &recordingBinder{}
	if _, err := ImportInto(t.Context(), p, b, dest, strings.NewReader(postmanFixture), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}
	if fi, err := os.Stat(dest); err != nil || !fi.IsDir() {
		t.Fatalf("dest: %v", err)
	}
	if len(b.keys) == 0 {
		t.Fatal("nothing was bound")
	}
	assertNoStaging(t, filepath.Dir(dest))
}

// A destination whose parent does not exist: MkdirTemp fails, which is the
// external call this path makes first.
func TestImportIntoFailsWhenTheParentIsMissing(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "nope", "collection")
	_, err := ImportInto(t.Context(), newProbeFS(), &recordingBinder{}, dest, strings.NewReader(postmanFixture), apicoll.Route{})
	if err == nil {
		t.Fatal("a destination with no parent was accepted")
	}
	assertGone(t, dest)
}

func TestImportIntoRefusesAnUnusableDestination(t *testing.T) {
	for _, dest := range []string{"", ".", "/", string(filepath.Separator)} {
		if _, err := ImportInto(t.Context(), newProbeFS(), &recordingBinder{}, dest, strings.NewReader(postmanFixture), apicoll.Route{}); err == nil {
			t.Fatalf("ImportInto(dest=%q) succeeded", dest)
		}
	}
}

func TestImportIntoStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dest := destUnder(t)
	_, err := ImportInto(ctx, newProbeFS(), &recordingBinder{}, dest, strings.NewReader(postmanFixture), apicoll.Route{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	assertGone(t, dest)
	assertNoStaging(t, filepath.Dir(dest))
}

func TestImportIntoRejectsAnUnreadableDocument(t *testing.T) {
	dest := destUnder(t)
	if _, err := ImportInto(t.Context(), newProbeFS(), &recordingBinder{}, dest, strings.NewReader(`{"nonsense":true}`), apicoll.Route{}); err == nil {
		t.Fatal("an unrecognisable document was accepted")
	}
	assertGone(t, dest)
	assertNoStaging(t, filepath.Dir(dest))
}

// Files land private. A collection is shared by committing it, never by
// making it world-readable on the machine it was imported to.
func TestImportIntoWritesPrivateFiles(t *testing.T) {
	dest := destUnder(t)
	if _, err := ImportInto(t.Context(), NewOSFS(), &recordingBinder{}, dest, strings.NewReader(postmanFixture), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}
	err := filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			if fi.Mode().Perm()&0o077 != 0 {
				t.Fatalf("directory %s is %v", p, fi.Mode().Perm())
			}
			return nil
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("file %s is %v", p, fi.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The written JSON is what the model round-trips to: the file is the truth
// (§6.4), so what a reader parses back has to be what the importer meant.
func TestImportIntoWritesReadableJSON(t *testing.T) {
	dest := destUnder(t)
	if _, err := ImportInto(t.Context(), NewOSFS(), &recordingBinder{}, dest, strings.NewReader(postmanFixture), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}
	converted, err := parsePostman(strings.NewReader(postmanFixture), apicoll.Route{})
	if err != nil {
		t.Fatal(err)
	}
	coll, reqs, envs := converted.Collection, converted.Requests, converted.Environments
	files := walkFiles(t, dest)

	// The manifest carries the name and the version and nothing else: the
	// list of requests IS the folder (§6.2), so what the import wrote is
	// checked against the folder below rather than against a field.
	var gotManifest map[string]any
	if err := json.Unmarshal([]byte(files[apicoll.ManifestName]), &gotManifest); err != nil {
		t.Fatalf("%s: %v", apicoll.ManifestName, err)
	}
	if gotManifest["name"] != coll.Name {
		t.Fatalf("%s names %v, want %q", apicoll.ManifestName, gotManifest["name"], coll.Name)
	}
	if v, ok := gotManifest["schemaVersion"].(float64); !ok || int(v) != int(apicoll.Module.Current) {
		t.Fatalf("%s says schemaVersion %v, want %d", apicoll.ManifestName, gotManifest["schemaVersion"], apicoll.Module.Current)
	}
	if len(gotManifest) != 2 {
		t.Fatalf("%s carries %v; it carries the name and the version and nothing else", apicoll.ManifestName, keysOfAny(gotManifest))
	}
	for i, ref := range coll.Requests {
		body, ok := files[ref.RelPath]
		if !ok {
			t.Fatalf("the collection names %q and no such file was written", ref.RelPath)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("%s: %v", ref.RelPath, err)
		}
		if got["name"] != reqs[i].Name || got["method"] != reqs[i].Method {
			t.Fatalf("%s = %+v, want %q %q", ref.RelPath, got, reqs[i].Name, reqs[i].Method)
		}
	}
	if len(envs) > 0 {
		if _, ok := files["environments/"+"default.json"]; !ok {
			t.Fatalf("no environment file; have %v", keysOf(files))
		}
	}
}
