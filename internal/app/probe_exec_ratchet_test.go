package app

// THE RATCHET, IN THE ONLY FORM THAT SURVIVES THE NEXT PERSON.
//
// D3 says no free-form exec crosses the helper wire: the helper runs a CLOSED
// SET of named probes whose command text lives in internal/remoteprobe, and the
// coordinator names one rather than composing one (nocx-50w7p.9). That is a
// property of the whole tree and of every change made after it, and a runtime
// check cannot state it — it can only report what one run happened to ask.
//
// So the check is on the SOURCE, in three parts, and each states what it cannot
// prove:
//
//  1. the exec-shaped ssh seam exists in the coordinator's production code in
//     exactly one place, and the list of those places is closed;
//  2. the text of a probe command exists in exactly one package;
//  3. no helper params schema carries a command, a script or an argv.
//
// Together they are a source walk, and a source walk proves where a NAME
// appears, not what runs: a file could alias the type, reach exec through a
// second seam, or put a command in a field called something innocent. That is
// why it is PAIRED rather than trusted alone — the behavioural half is
// TestThePlatformProbeSeamRunsItsOneNamedCommandAndNothingElse (the seam refuses
// every command but its own, before anything is sent) in
// helper_probeops_test.go, and TestTheProbeOpsCarryNoCommandAndNoFreeFormArgument
// (every probe payload recorded OFF THE WIRE, character for character) in
// internal/helper/client/probes_test.go.

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codeOf reads one production file as CODE, with comments removed.
//
// The walk below is a token search, and a comment mentioning the seam is not a
// use of it: the interface's own doc names it, and so does every consumer's
// history note. Parsing and re-printing is what makes the difference — and it is
// also what keeps the walk honest about a file that fails to parse, which is a
// failure and never a silent skip.
func codeOf(t *testing.T, rel string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(repoRoot, rel), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, file); err != nil {
		t.Fatalf("print %s: %v", rel, err)
	}
	return buf.String()
}

// repoRoot is where this test's walk starts: the module root, two levels up
// from internal/app.
const repoRoot = "../.."

// execSeamFiles are the production files allowed to mention the exec-shaped ssh
// seam (ssh.DiscoveryConn's type name, whatever an importer aliases the package
// to), each with the reason it is on the list.
//
// ADDING A FILE HERE IS THE DECISION THIS TEST EXISTS TO MAKE SOMEBODY TAKE. The
// seam is one command wide only because ONE caller converts it; a second caller
// is a second command on the wire unless it is retyped to a named probe first.
var execSeamFiles = map[string]string{
	// The declaration, and the interface's own doc says why it still exists
	// and what bounds it.
	"internal/ssh/ssh_discovery.go": "the seam's declaration",
	// The install path's one-command conversion (deploy's platform probe). Its
	// file is owned by another task, which is why the seam was kept rather than
	// retyped with the rest.
	"internal/app/helper_git.go": "the platform probe's conversion into deploy's one-command seam",
	// The composition root's dispatch: which lease the install path's probe is
	// served by.
	"internal/app/helper_sshchan.go": "the dispatch that serves that lane from this machine's helper",
	// The implementation: a lease on the helper, one op wide.
	"internal/app/helper_probeops.go": "the helper-backed implementation",
}

func TestTheExecShapedSSHSeamHasOneCallSite(t *testing.T) {
	found := map[string]bool{}
	for _, path := range productionFiles(t, []string{"internal", "cmd"}) {
		if !strings.Contains(codeOf(t, path), "DiscoveryConn") {
			continue
		}
		if _, ok := execSeamFiles[path]; !ok {
			t.Errorf("%s mentions the exec-shaped ssh seam and is not on the list.\n\n"+
				"The seam is one command wide only because one caller converts it to a typed op "+
				"(app.helperProbes.platformLease). A second caller is a second command on the wire "+
				"unless it is retyped to a named probe first — add the probe in internal/remoteprobe "+
				"and internal/helper/sshsvc, or say here why this file needs the seam.", path)
			continue
		}
		found[path] = true
	}
	for path := range execSeamFiles {
		if !found[path] {
			t.Errorf("%s is on the exec-seam list and does not mention it any more: remove its row, "+
				"or restore what went missing", path)
		}
	}
}

func TestNoProbeCommandTextLivesOutsideItsOwner(t *testing.T) {
	// The fixed words a probe runs. Each may appear in the package that owns it
	// (internal/remoteprobe) and nowhere else in production code: a second copy
	// is a command composed where it should be named, which is the shape this
	// whole task removed.
	owned := map[string]string{
		"uname -s -m": "uname",
		"echo $HOME":  "home",
		"NOCX-PD/1":   "the port probes' framing sentinel",
		"NOCX_CN":     "the enumeration's framing marker",
		"NOCXEOF_":    "the completion heredoc delimiter",
	}
	for _, path := range productionFiles(t, []string{"internal", "cmd"}) {
		if path == "internal/remoteprobe/remoteprobe.go" || strings.HasPrefix(path, "internal/remoteprobe/") {
			continue
		}
		code := codeOf(t, path)
		for word, what := range owned {
			if strings.Contains(code, word) {
				t.Errorf("%s carries %q (%s), which internal/remoteprobe owns.\n\n"+
					"A probe's command is composed by the helper from that package, and a copy here "+
					"is a command composed where it should be named.", path, word, what)
			}
		}
	}
}

// TestNoHelperParamsCarryACommand reads every frozen params schema for a field
// that could carry one. It is the wire-side statement of D3: a shape with an
// `argv` is a shape that will one day hold one.
func TestNoHelperParamsCarryACommand(t *testing.T) {
	dir := filepath.Join(repoRoot, "contracts", "helper")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read contracts/helper: %v", err)
	}
	// `shell` is deliberately NOT here: spawn-ssh's own `shell` is the far
	// side's shell FAMILY, a member of shellintegration's closed set, and an
	// enum naming a family is a typed statement about a host rather than a
	// program to run. Every name below is one that a command would be written
	// INTO.
	forbidden := map[string]bool{
		"command": true, "cmd": true, "script": true, "argv": true, "args": true,
		"arguments": true, "snippet": true,
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".params.schema.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a path under contracts/
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		walkSchemaProperties(name, doc, forbidden, t)
		checked++
	}
	if checked < 10 {
		t.Fatalf("walked %d params schemas; the helper ABI has more than that, so this check stopped early", checked)
	}
}

// walkSchemaProperties reports any property whose NAME says it could carry a
// command, at any depth.
func walkSchemaProperties(schema string, node any, forbidden map[string]bool, t *testing.T) {
	switch v := node.(type) {
	case map[string]any:
		if props, ok := v["properties"].(map[string]any); ok {
			for prop := range props {
				if forbidden[strings.ToLower(prop)] {
					t.Errorf("%s declares a %q property: a params shape may name a probe, never carry a command", schema, prop)
				}
			}
		}
		for _, child := range v {
			walkSchemaProperties(schema, child, forbidden, t)
		}
	case []any:
		for _, child := range v {
			walkSchemaProperties(schema, child, forbidden, t)
		}
	}
}

// productionFiles lists the non-test Go files under the given roots, relative to
// the repo root, sorted for a stable report.
func productionFiles(t *testing.T, roots []string) []string {
	t.Helper()
	var out []string
	for _, root := range roots {
		base := filepath.Join(repoRoot, root)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "vendor", ".git", "node_modules", "spikes":
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, rerr := filepath.Rel(repoRoot, path)
			if rerr != nil {
				return rerr
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	return out
}
