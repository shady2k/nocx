package shellintegration

import (
	"os/exec"
	"regexp"
	"testing"
)

// macOS still ships GNU bash 3.2.57 as /bin/bash — Apple froze it at the last
// GPLv2 release in 2007 — and this is a macOS-first product, so 3.2 is the
// OLDEST bash the integration script must load in, not an edge case.
//
// The trap this test exists for: a version test cannot guard SYNTAX. bash
// parses a function's whole body before executing any of it, so
//
//	if (( BASH_VERSINFO[0] >= 4 )); then coproc name { ...; }; fi
//
// is rejected by 3.2 at the `coproc` token even though the branch is never
// taken. That shipped (nocx-cn86): every bash shell on macOS died at
// "syntax error near unexpected token `}'" while sourcing, started with no
// integration at all, and 30 tests in this package went red on the macOS CI
// job — the ONLY gate in the repo that runs a real 3.2. A bash-4+ construct
// belongs inside an `eval` string, which is parsed when the branch runs.
//
// A parse check, deliberately, not an execution one: what 3.2 must do is
// accept the file. What it then DOES on macOS is covered by the tests in
// scripts_exec_test.go, which run against /bin/bash on the macOS runner.
func TestBashScript_ParsesUnderBash32(t *testing.T) {
	bash32 := requireBash32(t)
	script := writeScriptFile(t, "nocx.bash", bashScript)

	// -n: read and parse, run nothing.
	// #nosec G204 — bash32 is the requireBash32-resolved path and script is
	// this test's own temp file; neither is input.
	out, err := exec.Command(bash32, "-n", script).CombinedOutput()
	if err != nil {
		t.Fatalf("the shipped bash script does not parse under bash 3.2 (macOS /bin/bash):\n%s\n"+
			"A bash 4+ construct outside an eval is the usual cause — a runtime version\n"+
			"guard does not help, because bash parses the whole body first.", out)
	}
}

// bashCandidates are the names this package looks for a GNU bash under: the
// PATH bash (a developer's own shell, and the 5.x side of the matrix on
// Linux), the fixture the CI Linux image and scripts/install-bash32.sh
// install,
// and macOS's frozen /bin/bash. One list, because "which bashes exist here"
// is one question — requireBash32 and bashVariants are two readings of it,
// not two implementations.
var bashCandidates = []string{"bash", "bash32", "/bin/bash"}

var bashVersionRe = regexp.MustCompile(`GNU bash, version (\d+\.\d+)`)

// resolveBash resolves one candidate name to its path and major.minor version
// ("3.2", "5.2"). ok is false when the name is not on PATH or does not answer
// --version as a GNU bash.
func resolveBash(name string) (path, version string, ok bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", "", false
	}
	// #nosec G204 — name comes from bashCandidates, a fixed list, never input.
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", "", false
	}
	m := bashVersionRe.FindStringSubmatch(string(out))
	if m == nil {
		return "", "", false
	}
	return path, m[1], true
}

// noBash32 is what both readings say when the platform's oldest bash is
// absent. It fails rather than skips, for the reason nocx-gd84 gave for zsh:
// a skip here reports green on every Linux machine in the project, which is
// every machine except the CI runner that found the bug in the first place.
const noBash32 = "no GNU bash 3.2 found (looked for `bash`, `bash32` and /bin/bash).\n" +
	"3.2 is macOS's /bin/bash and the OLDEST bash this product must work on, so a run\n" +
	"without it is not a run. The container images install the fixture:\n" +
	"  sh -c '. ./.githooks/containerized-tests.sh; go_test_containerized' .githooks/pre-commit"

// requireBash32 returns a path to a GNU bash 3.2 — macOS's /bin/bash, or the
// `bash32` fixture on Linux.
func requireBash32(t *testing.T) string {
	t.Helper()
	for _, cand := range bashCandidates {
		if path, version, ok := resolveBash(cand); ok && version == "3.2" {
			return path
		}
	}
	t.Fatal(noBash32)
	return ""
}

// bashVariant is one bash the suite drives the shipped script through: the
// name startChannelShell resolves, and the version that names the subtest.
type bashVariant struct {
	name    string
	version string
}

// bashVariants returns every DISTINCT GNU bash on this machine, deduped by
// version so a name that is another name's build runs the body once.
//
// It exists because a parse check cannot report what 3.2 DOES. The version
// guard that shipped in nocx-cn86 made the script parse under 3.2 and stop
// there: `read -N` is bash 4.1+, it is an OPTION rather than syntax, so
// `bash -n` accepts it and the handshake then hangs on a probe that can never
// succeed — every macOS bash session a conventional terminal, with 12 tests
// red on the one CI job that runs a real 3.2 and nothing red anywhere a
// developer looks. Running the SAME bodies against both bashes is what closes
// that: on Linux the fixture makes the defect reproducible without a Mac.
func bashVariants(t *testing.T) []bashVariant {
	t.Helper()
	var variants []bashVariant
	seen := make(map[string]bool)
	has32 := false
	for _, cand := range bashCandidates {
		_, version, ok := resolveBash(cand)
		if !ok || seen[version] {
			continue
		}
		seen[version] = true
		if version == "3.2" {
			has32 = true
		}
		variants = append(variants, bashVariant{name: cand, version: version})
	}
	if !has32 {
		t.Fatal(noBash32)
	}
	return variants
}

// forEachBash runs body once per distinct bash, as a subtest named for the
// version — so a failure says WHICH bash broke, in its own name.
func forEachBash(t *testing.T, body func(t *testing.T, shell string)) {
	t.Helper()
	for _, v := range bashVariants(t) {
		t.Run("bash"+v.version, func(t *testing.T) { body(t, v.name) })
	}
}
