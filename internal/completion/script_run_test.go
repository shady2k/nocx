package completion

// The static checks in script_portability_test.go read the script's TEXT.
// They cannot report what it EMITS, and the defect that bought this file was
// exactly that: the script printed compgen's whole replacement word in the
// `name` column, so `cd repos/t` completed to `cd repos/repos/tabby/` — the
// typed prefix inserted twice (nocx-yqoy5). Every string check passed.
//
// So this file runs the real script against a real fixture tree, through the
// real parser, and asserts the contract `Candidate.Name` states and the
// renderer depends on: name is the LAST PATH SEGMENT, path is absolute.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runScript executes the embedded script under bash with the given line and
// caret and parses the framed output the way the SSH completer does.
func runScript(t *testing.T, cwd, line string, pos int) []Candidate {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH; the remote script cannot be exercised here")
	}
	const nonce = "testnonce"
	// #nosec G204 -- the interpreter is resolved from PATH and every argument
	// is a test literal; this is the point of the test, which is to run the
	// real script rather than a paraphrase of it.
	cmd := exec.Command(bash, "-s", "--", cwd, line, strconv.Itoa(pos), "50", nonce)
	cmd.Stdin = strings.NewReader(completionScript)
	cmd.Env = append(os.Environ(), "HOME="+cwd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script failed: %v (stderr: %s)", err, stderr.String())
	}
	return parseCompletionOutput(out, nonce, 50).Candidates
}

// fixtureTree builds ~/repos/{tabby,termic} plus a top-level Downloads and
// returns its root.
func fixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"repos/tabby", "repos/termic", "Downloads"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func candidatesByName(cs []Candidate) map[string]Candidate {
	m := make(map[string]Candidate, len(cs))
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

// TestScriptNamesAreLastSegment_NestedToken is the regression: the token
// already carries `repos/`, and the renderer re-adds it, so the name must not.
func TestScriptNamesAreLastSegment_NestedToken(t *testing.T) {
	root := fixtureTree(t)
	got := runScript(t, root, "cd repos/t", len("cd repos/t"))
	by := candidatesByName(got)
	for _, want := range []string{"tabby", "termic"} {
		c, ok := by[want]
		if !ok {
			t.Fatalf("no candidate named %q; got %v (a name carrying the typed prefix is the duplication defect)", want, by)
		}
		if c.Path != filepath.Join(root, "repos", want) {
			t.Errorf("%s: path = %q, want the absolute %q", want, c.Path, filepath.Join(root, "repos", want))
		}
		if !c.IsDir {
			t.Errorf("%s: isDir = false, want true", want)
		}
		if c.Source != "path" {
			t.Errorf("%s: source = %q, want path", want, c.Source)
		}
	}
}

// TestScriptNamesAreLastSegment_TrailingSlashToken covers the step-into form,
// where the whole token is the prefix.
func TestScriptNamesAreLastSegment_TrailingSlashToken(t *testing.T) {
	root := fixtureTree(t)
	by := candidatesByName(runScript(t, root, "cd repos/", len("cd repos/")))
	for _, want := range []string{"tabby", "termic"} {
		if _, ok := by[want]; !ok {
			t.Errorf("no candidate named %q; got %v", want, by)
		}
	}
}

// TestScriptNamesAreLastSegment_BareToken pins the case that was already
// right, so a fix cannot pay for the nested case with this one.
func TestScriptNamesAreLastSegment_BareToken(t *testing.T) {
	root := fixtureTree(t)
	by := candidatesByName(runScript(t, root, "cd re", len("cd re")))
	if _, ok := by["repos"]; !ok {
		t.Errorf("no candidate named %q; got %v", "repos", by)
	}
}
