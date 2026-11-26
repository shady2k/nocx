package assistant

// files.list and files.search are the read-side pair of stage 10's last leaf
// (.11.3). These tests run the executors the model's calls reach — through
// the same narrowed capability the declaration builds and the same result
// bound the kernel arm's context — and then check two things the declaration
// alone cannot: that the answer conforms to the contract document the model
// was shown, and that the bound is a DROP-TAIL window rather than a silent
// cut.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// queryFixture builds a granted tree and the capability files.list and
// files.search receive for a call naming root.
type queryFixture struct {
	root    string
	outside string
	grant   content.Grant
}

func newQueryFixture(t *testing.T) queryFixture {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{root, filepath.Join(root, "sub"), filepath.Join(root, ".git"), outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(root, "a.txt"), "alpha\nneedle here\n")
	write(filepath.Join(root, "sub", "b.txt"), "beta\nneedle sub\n")
	write(filepath.Join(root, ".git", "config"), "needle in git\n")
	write(filepath.Join(outside, "secret.txt"), "needle secret\n")
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 'n', 'e', 'e', 'd', 'l', 'e'}, 0o600); err != nil {
		t.Fatalf("write bin.dat: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	return queryFixture{root: root, outside: outside, grant: autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: root}})}
}

func (f queryFixture) capability(t *testing.T, name, path string) agenttools.Capability {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("%s is not in the assembled registry", name)
	}
	capability, err := tool.Narrow(f.grant, []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: path}}, localRunContext(t))
	if err != nil {
		t.Fatalf("Narrow %s: %v", name, err)
	}
	return capability
}

// validateAgainstContract is the wire's other end: the result the executor
// returned must be what the declaration says it returns.
func validateAgainstContract(t *testing.T, tool, out string) {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	decl, ok := reg.Lookup(tool)
	if !ok {
		t.Fatalf("%s is not in the registry", tool)
	}
	sch, err := compileResultSchema(decl)
	if err != nil {
		t.Fatalf("%s result schema: %v", tool, err)
	}
	var doc any
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("%s result does not parse: %v", tool, err)
	}
	if err := sch.Validate(doc); err != nil {
		t.Fatalf("%s result does not conform to its contract: %v\n%s", tool, err, out)
	}
}

type filesListResultDoc struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	Entries   []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Kind string `json:"kind"`
		Size int64  `json:"size"`
	} `json:"entries"`
	Truncated bool `json:"truncated"`
	Skipped   struct {
		GitDirs  int `json:"gitDirs"`
		Symlinks int `json:"symlinks"`
	} `json:"skipped"`
}

func TestExecuteFilesListReturnsTheTreeWithoutGitOrSymlinkTargets(t *testing.T) {
	fx := newQueryFixture(t)
	capability := fx.capability(t, "files.list", fx.root)
	out, err := executeFilesList(toolTestContext(), capability, mustJSON(t, map[string]any{"path": fx.root, "recursive": true}), toolSeams{})
	if err != nil {
		t.Fatalf("executeFilesList: %v", err)
	}
	validateAgainstContract(t, "files.list", out)

	var res filesListResultDoc
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Recursive || res.Truncated {
		t.Fatalf("result = %+v, want a complete recursive listing", res)
	}
	names := map[string]string{}
	for _, entry := range res.Entries {
		names[entry.Path] = entry.Kind
	}
	for _, want := range []string{
		filepath.Join(fx.root, "a.txt"),
		filepath.Join(fx.root, "sub"),
		filepath.Join(fx.root, "sub", "b.txt"),
	} {
		if _, ok := names[want]; !ok {
			t.Errorf("listing has no %s: %+v", want, res.Entries)
		}
	}
	if kind, ok := names[filepath.Join(fx.root, "escape")]; !ok || kind != "symlink" {
		t.Errorf("listing = %+v, want the symlink reported as a symlink and not entered", res.Entries)
	}
	for path := range names {
		if strings.Contains(path, string(filepath.Separator)+".git") || strings.HasSuffix(path, ".git") {
			t.Errorf("listing reported %q: a .git directory is skipped", path)
		}
		if strings.HasSuffix(path, "secret.txt") {
			t.Errorf("listing reached %q through a symlink out of the granted tree", path)
		}
	}
	if res.Skipped.GitDirs != 1 || res.Skipped.Symlinks != 1 {
		t.Errorf("skipped = %+v, want one .git directory and one symlink", res.Skipped)
	}
}

func TestExecuteFilesListTruncatesAtTheDeclaredBound(t *testing.T) {
	fx := newQueryFixture(t)
	// A bound far below this listing's size, so the drop-tail path is the one
	// under test. It still leaves room for the envelope (an absolute temp
	// path is long) and several rows, which is the case that matters: a
	// partial answer, not an empty one.
	const bound = 1200
	for i := 0; i < 40; i++ {
		name := filepath.Join(fx.root, "sub", strings.Repeat("f", 40)+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	capability := fx.capability(t, "files.list", fx.root)
	ctx := withToolBound(context.Background(), agenttools.ResultBound{MaxBytes: bound, Truncation: agenttools.TruncationDropTail})
	out, err := executeFilesList(ctx, capability, mustJSON(t, map[string]any{"path": fx.root, "recursive": true}), toolSeams{})
	if err != nil {
		t.Fatalf("executeFilesList: %v", err)
	}
	if int64(len(out)) > bound {
		t.Fatalf("result is %d bytes, over its %d-byte bound", len(out), bound)
	}
	validateAgainstContract(t, "files.list", out)
	var res filesListResultDoc
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Truncated {
		t.Fatal("a listing cut to fit its bound did not report truncated")
	}
	if len(res.Entries) == 0 || len(res.Entries) >= 43 {
		t.Fatalf("returned %d entries: want a partial listing, not nothing and not everything", len(res.Entries))
	}
	// What the traversal skipped before it filled is still reported: a
	// truncated answer says both that it stopped and what it refused.
	if res.Skipped.GitDirs != 1 || res.Skipped.Symlinks != 1 {
		t.Errorf("skipped = %+v, want the skips recorded before the bound filled", res.Skipped)
	}
}

func TestExecuteFilesListReportsAFailingList(t *testing.T) {
	fx := newQueryFixture(t)
	boom := errors.New("list boom")
	provider := &queryFailingProvider{Provider: local.New(), listPath: filepath.Join(fx.root, "sub"), err: boom}
	out, err := executeFilesList(toolTestContext(), capabilityOver(t, provider, fx, "files.list", fx.root), mustJSON(t, map[string]any{"path": fx.root, "recursive": true}), toolSeams{})
	if !errors.Is(err, boom) {
		t.Fatalf("executeFilesList = %q, %v — want the provider's list error", out, err)
	}
}

func TestExecuteFilesListRefusesAPathOutsideTheGrant(t *testing.T) {
	fx := newQueryFixture(t)
	// The capability is narrowed to the call's own path, which is outside the
	// grant's scopes: the request is expressible, the authority is not.
	capability := fx.capability(t, "files.list", fx.outside)
	_, err := executeFilesList(toolTestContext(), capability, mustJSON(t, map[string]any{"path": fx.outside}), toolSeams{})
	if !errors.Is(err, filesystem.ErrOutOfScope) {
		t.Fatalf("executeFilesList outside the grant = %v, want the capability refusal the kernel turns into our sentence", err)
	}
}

type filesSearchResultDoc struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Matches []struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Text string `json:"text"`
	} `json:"matches"`
	Files     int  `json:"files"`
	Truncated bool `json:"truncated"`
	Skipped   struct {
		GitDirs     int `json:"gitDirs"`
		Symlinks    int `json:"symlinks"`
		BinaryFiles int `json:"binaryFiles"`
	} `json:"skipped"`
}

func TestExecuteFilesSearchReturnsMatchingLines(t *testing.T) {
	fx := newQueryFixture(t)
	capability := fx.capability(t, "files.search", fx.root)
	out, err := executeFilesSearch(toolTestContext(), capability, mustJSON(t, map[string]any{"path": fx.root, "pattern": "needle"}), toolSeams{})
	if err != nil {
		t.Fatalf("executeFilesSearch: %v", err)
	}
	validateAgainstContract(t, "files.search", out)

	var res filesSearchResultDoc
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("matches = %+v, want the two lines of the granted tree", res.Matches)
	}
	want := map[string]int{filepath.Join(fx.root, "a.txt"): 2, filepath.Join(fx.root, "sub", "b.txt"): 2}
	for _, m := range res.Matches {
		if want[m.Path] != m.Line {
			t.Errorf("match %+v, want line %d of %s", m, want[m.Path], m.Path)
		}
		if !strings.Contains(m.Text, "needle") {
			t.Errorf("match text %q does not hold the pattern", m.Text)
		}
	}
	if res.Truncated {
		t.Error("a complete search reported truncated")
	}
	if res.Skipped.BinaryFiles != 1 || res.Skipped.GitDirs != 1 || res.Skipped.Symlinks != 1 {
		t.Errorf("skipped = %+v, want one binary file, one .git directory and one symlink", res.Skipped)
	}
	if res.Files != 3 {
		t.Errorf("files read = %d, want 3 (a.txt, bin.dat, sub/b.txt)", res.Files)
	}
}

func TestExecuteFilesSearchTruncatesAtTheDeclaredBound(t *testing.T) {
	fx := newQueryFixture(t)
	for i := 0; i < 20; i++ {
		body := strings.Repeat("needle line\n", 20)
		if err := os.WriteFile(filepath.Join(fx.root, "sub", "many"+strings.Repeat("x", i+1)+".txt"), []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	capability := fx.capability(t, "files.search", fx.root)
	const bound = 800
	ctx := withToolBound(context.Background(), agenttools.ResultBound{MaxBytes: bound, Truncation: agenttools.TruncationDropTail})
	out, err := executeFilesSearch(ctx, capability, mustJSON(t, map[string]any{"path": fx.root, "pattern": "needle"}), toolSeams{})
	if err != nil {
		t.Fatalf("executeFilesSearch: %v", err)
	}
	if int64(len(out)) > bound {
		t.Fatalf("result is %d bytes, over its %d-byte bound", len(out), bound)
	}
	validateAgainstContract(t, "files.search", out)
	var res filesSearchResultDoc
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Truncated {
		t.Fatal("a search cut to fit its bound did not report truncated")
	}
	if len(res.Matches) == 0 || len(res.Matches) >= 402 {
		t.Fatalf("returned %d matches: want a partial result", len(res.Matches))
	}
}

func TestExecuteFilesSearchReportsAnInvalidPattern(t *testing.T) {
	fx := newQueryFixture(t)
	capability := fx.capability(t, "files.search", fx.root)
	out, err := executeFilesSearch(toolTestContext(), capability, mustJSON(t, map[string]any{"path": fx.root, "pattern": "needle("}), toolSeams{})
	if err == nil {
		t.Fatalf("executeFilesSearch accepted a pattern RE2 cannot compile: %q", out)
	}
}

func TestExecuteFilesSearchReportsAFailingRead(t *testing.T) {
	fx := newQueryFixture(t)
	boom := errors.New("read boom")
	provider := &queryFailingProvider{Provider: local.New(), readPath: filepath.Join(fx.root, "a.txt"), err: boom}
	out, err := executeFilesSearch(toolTestContext(), capabilityOver(t, provider, fx, "files.search", fx.root), mustJSON(t, map[string]any{"path": fx.root, "pattern": "needle"}), toolSeams{})
	if !errors.Is(err, boom) {
		t.Fatalf("executeFilesSearch = %q, %v — want the provider's read error", out, err)
	}
}

// queryFailingProvider wraps the real provider and fails ONE call, so the
// failure test runs the real traversal around the failing step.
type queryFailingProvider struct {
	filesystem.Provider
	listPath string
	readPath string
	err      error
}

func (p *queryFailingProvider) List(ctx context.Context, path string, page filesystem.Page) (filesystem.Listing, error) {
	if p.listPath != "" && path == p.listPath {
		return filesystem.Listing{}, p.err
	}
	return p.Provider.List(ctx, path, page)
}

func (p *queryFailingProvider) Read(ctx context.Context, path string, maxBytes int64) (filesystem.Content, error) {
	if p.readPath != "" && path == p.readPath {
		return filesystem.Content{}, p.err
	}
	return p.Provider.Read(ctx, path, maxBytes)
}

// capabilityOver narrows the given tool over a source built on the wrapped
// provider, which is how a failure is injected without changing the fixture.
func capabilityOver(t *testing.T, provider filesystem.Provider, fx queryFixture, name, path string) agenttools.Capability {
	t.Helper()
	reg := filesystem.New()
	id, err := reg.Register(provider, testFilesSession, "", filesystem.Capabilities{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(id) })
	source := &localFileSource{reg: reg, id: id}
	tools, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := tools.Lookup(name)
	if !ok {
		t.Fatalf("%s is not in the registry", name)
	}
	capability, err := tool.Narrow(fx.grant, []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: path}}, agenttools.RunContext{
		Environment: localPaneEnvironment(),
		Files:       source,
	})
	if err != nil {
		t.Fatalf("Narrow %s: %v", name, err)
	}
	return capability
}
