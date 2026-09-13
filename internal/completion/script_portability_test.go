package completion

// The remote completion script must run on the OLDEST bash it can meet, and
// that is bash 3.2 — macOS has shipped it since 2007 and still does, because
// bash went GPLv3 in 4.0. A host running stock macOS is not exotic; it is one
// of the two platforms this project targets.
//
// This matters because the failure is silent to the code that reads it. The
// script died on `declare -A` with "invalid option" and exit 2, the remote
// produced no candidates, and the completer reported an empty list — which is
// indistinguishable, at the seam, from a directory that genuinely holds
// nothing (nocx-smy9). TestSSHCompleter_E2E_RemotePaths caught it only because
// it runs against a real bash, and only on a machine whose bash is old: on the
// Linux CI job, with bash 5, it passes with the defect present.
//
// So the constructs are also checked statically, where the bash version of the
// machine running the tests cannot hide them.
import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/remoteprobe"
)

// bash4Only names constructs the script must not use, with the version that
// introduced each — a reader who hits this test needs to know what to use
// instead, not just that they may not.
var bash4Only = []struct {
	token  string
	since  string
	insted string
}{
	{"declare -A", "4.0", "a delimited string plus a quoted membership test, as the path dedup does"},
	{"local -A", "4.0", "the same"},
	{"typeset -A", "4.0", "the same"},
	{"${!", "4.0 for the [@] form", "iterate the values you already have"},
	{",,}", "4.0", "tr '[:upper:]' '[:lower:]'"},
	{"^^}", "4.0", "tr '[:lower:]' '[:upper:]'"},
	{"mapfile", "4.0", "a while read loop"},
	{"readarray", "4.0", "a while read loop"},
	{"&>>", "4.0", ">>file 2>&1"},
	{"[[ -v ", "4.2", "[[ -n ${name:-} ]]"},
	{"EPOCHREALTIME", "5.0", "nothing — the script must not need a clock"},
	{"EPOCHSECONDS", "5.0", "nothing — the script must not need a clock"},
}

// shippedScript is the completion script AS SHIPPED, taken out of the command
// the helper composes: the script lives in internal/remoteprobe now (D3 — no
// command crosses the helper wire, so the text belongs to the process that runs
// it), and reading it from the composed command is what keeps this check
// pointed at the bytes a host would actually receive.
func shippedScript() string {
	cmd := remoteprobe.CompletionCommand("/tmp", "ls ", 3, 50, "portability")
	lines := strings.Split(cmd, "\n")
	// The command is header, script, delimiter: drop the first and last lines.
	if len(lines) > 2 {
		lines = lines[1 : len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// scriptCode is the script with comment lines removed. The comments explain
// which constructs are forbidden and name them, so scanning the raw text
// reports the explanation as a violation.
func scriptCode() string {
	var b strings.Builder
	for _, line := range strings.Split(shippedScript(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestRemoteScriptRunsOnBash32(t *testing.T) {
	code := scriptCode()
	for _, b := range bash4Only {
		if strings.Contains(code, b.token) {
			t.Errorf("nocx_complete.bash uses %q, which needs bash %s; macOS ships 3.2, where the script aborts and the remote returns nothing. Use %s.",
				b.token, b.since, b.insted)
		}
	}
}

// TestRemoteScriptDedupesPathsAcrossBothPasses pins the behaviour the
// associative array was there for: `compgen -f` lists directories too, so a
// directory appears in both passes and must be offered once.
func TestRemoteScriptDedupesPathsAcrossBothPasses(t *testing.T) {
	if !strings.Contains(shippedScript(), `seen_paths="$seen_paths|$entry|"`) {
		t.Error("the path dedup is gone; a directory is listed by both compgen -f and compgen -d and would be offered twice")
	}
	// The membership test must quote the entry inside the pattern, or a
	// filename holding *, ? or [ is glob-matched instead of compared.
	if !strings.Contains(shippedScript(), `[[ "$seen_paths" != *"|$entry|"* ]]`) {
		t.Error("the dedup membership test must quote $entry inside the pattern, so a filename with a glob character is compared literally")
	}
}
