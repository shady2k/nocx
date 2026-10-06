package shellintegration

// The generated agent wrappers (nocx-t5e7d, the owner's shell-bundle decision):
// a connected shell offers a wrapper per ENABLED agent of the record, and what
// travels to a host is the NAME and nothing else.
//
// The security half is the reason this file exists and is not a formality. The
// bundle is published to hosts nobody here controls, so an argument or an
// environment line that reached it would be a person's own configuration — a
// key, a path, a credential — copied onto somebody else's machine. The record
// is read LOCALLY; the host receives a list of names.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentrecord"
)

// recordStore writes a record set under a fresh app directory and opens it.
func recordStore(t *testing.T, docs map[string]string) *agentrecord.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, agentrecord.DirName), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for id, body := range docs {
		p := filepath.Join(dir, agentrecord.DirName, id+".json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	s, err := agentrecord.New(dir)
	if err != nil {
		t.Fatalf("agentrecord.New: %v", err)
	}
	return s
}

// The block offers each enabled agent by name, and it carries NOTHING but the
// name: no argument, no environment line, no flag. It is asserted as an exact
// text, because "carries no arguments" is a claim a substring check cannot
// make.
func TestTheGeneratedBlockOffersEveryAgentByNameAndNothingElse(t *testing.T) {
	got := agentWrappers([]string{"claude", "codex", "omp", "prime-agent"})
	want := `claude() { __nocx_agent_run claude "$@"; }
codex() { __nocx_agent_run codex "$@"; }
omp() { __nocx_agent_run omp "$@"; }
prime-agent() { __nocx_agent_run prime-agent "$@"; }
`
	if got != want {
		t.Fatalf("block =\n%s\nwant:\n%s", got, want)
	}
	for _, forbidden := range []string{"--mcp-config", "=", "export", "$(", "${", "-"} {
		if forbidden == "-" {
			continue
		}
		if strings.Contains(strings.ReplaceAll(got, "prime-agent", "primeagent"), forbidden) {
			t.Fatalf("the block carries %q, and only names may travel to a host", forbidden)
		}
	}
}

// THE SECURITY HALF. A person's own record — a key in an environment line and
// arguments nobody else has — changes the record nocx reads where nocx runs,
// and leaves the bundle byte-identical: the host is told the name, never the
// record.
func TestAPersonsRecordDoesNotReachTheBundle(t *testing.T) {
	shipped := recordStore(t, nil)
	edited := recordStore(t, map[string]string{
		"claude": `{
  "version": 1,
  "command": "/opt/my-claude",
  "args": ["--dangerously-skip-permissions", "--model", "secret-model-name"],
  "env": ["ANTHROPIC_API_KEY=sk-ant-not-a-real-key"],
  "resume": {"resumeCwdArgs": ["--continue"]}
}`,
	})

	// The record really did change locally, or the rest of this proves nothing.
	if len(edited.EnabledNames()) != len(shipped.EnabledNames()) {
		t.Fatalf("the edit changed the enabled set: %v vs %v", edited.EnabledNames(), shipped.EnabledNames())
	}

	plain := launchBundle(shipped.EnabledNames())
	personal := launchBundle(edited.EnabledNames())

	if plain.Version != personal.Version {
		t.Fatalf("version moved on an edit that changed no name: %q vs %q", plain.Version, personal.Version)
	}
	if plain.generation() != personal.generation() {
		t.Fatalf("generation moved on an edit that changed no name: %q vs %q", plain.generation(), personal.generation())
	}
	for _, a := range plain.Files {
		if string(a.Data) != string(fileOf(t, personal, a.Name)) {
			t.Fatalf("%s differs after an edit that changed no agent NAME, so the person's record reached the bundle", a.Name)
		}
	}
	for _, a := range personal.Files {
		for _, secret := range []string{
			"sk-ant-not-a-real-key", "ANTHROPIC_API_KEY",
			"--dangerously-skip-permissions", "secret-model-name", "/opt/my-claude",
		} {
			if strings.Contains(string(a.Data), secret) {
				t.Fatalf("%s carries %q, which is the person's own record travelling to a host", a.Name, secret)
			}
		}
	}
}

// Adding an agent a person owns changes what the shell offers and moves the
// bundle's identity, so a host already connected is published a new generation
// rather than keeping the old set — and what changed is the NAME list and
// nothing beside it.
func TestAnAddedAgentMovesTheGenerationAndOnlyTheNameList(t *testing.T) {
	shipped := recordStore(t, nil)
	added := recordStore(t, map[string]string{
		"myagent": `{"version": 1, "command": "my-agent", "args": ["--fast"], "env": ["MYAGENT_KEY=nope"]}`,
	})

	plain := launchBundle(shipped.EnabledNames())
	grown := launchBundle(added.EnabledNames())

	if plain.generation() == grown.generation() {
		t.Fatalf("the generation did not move when the enabled set did: %q", plain.generation())
	}
	if got := grown.Agents; !sameNames(got, []string{"claude", "myagent"}) {
		t.Fatalf("the bundle's agents = %v, want the shipped agent and the person's", got)
	}
	for _, f := range grown.Files {
		for _, secret := range []string{"MYAGENT_KEY", "--fast"} {
			if strings.Contains(string(f.Data), secret) {
				t.Fatalf("%s carries %q: an added agent's arguments and environment must not travel", f.Name, secret)
			}
		}
	}
	// The two scripts differ EXACTLY by the block: substituting one name list
	// for the other turns one into the other, byte for byte.
	plainScript := string(fileOf(t, plain, "nocx.bash"))
	grownScript := string(fileOf(t, grown, "nocx.bash"))
	if want := strings.Replace(grownScript, agentWrappers(grown.Agents), agentWrappers(plain.Agents), 1); want != plainScript {
		t.Fatalf("adding an agent changed something outside the wrapper block")
	}
}

// The block is generated INTO the scripts, so no list has to be kept in step
// with the record by hand — and the hand-written line is gone.
func TestTheScriptsCarryTheGeneratedBlockAndNoHandWrittenList(t *testing.T) {
	for name, raw := range map[string]string{"nocx.bash": bashScriptRaw, "nocx.zsh": zshScriptRaw} {
		if !strings.Contains(raw, agentBlockMarker) {
			t.Fatalf("%s has no generator marker, so the wrappers would be hand-written again", name)
		}
		if strings.Contains(raw, "claude() {") {
			t.Fatalf("%s still spells a wrapper by hand", name)
		}
	}
	for name, rendered := range map[string]string{"nocx.bash": bashScript, "nocx.zsh": zshScript} {
		if strings.Contains(rendered, agentBlockMarker) {
			t.Fatalf("%s still carries the marker, so nothing generated the block", name)
		}
		got := agentrecord.ShippedNames()
		if len(got) == 0 {
			t.Fatalf("this build ships no agent at all, which no build may do")
		}
		for _, agent := range got {
			if !strings.Contains(rendered, agent+"() { __nocx_agent_run "+agent+` "$@"; }`) {
				t.Fatalf("%s does not wrap %q, which this build ships", name, agent)
			}
		}
	}
}

// An agent whose name is not one a shell can define a function with is not
// wrapped, and it does not take the good names with it: a name here is a
// command name, and one a shell cannot define is not one a person could type
// either.
func TestANameAShellCannotDefineIsNotWrapped(t *testing.T) {
	got := agentWrappers([]string{"claude", "a.b", "1nope", "../escape", "has space"})
	if got != `claude() { __nocx_agent_run claude "$@"; }`+"\n" {
		t.Fatalf("block =\n%s\nwant only the name a shell can define", got)
	}
}

// The local (helper-embedded) scripts and the published bundle come from ONE
// generator, so the shape a host receives and the shape a local pane receives
// cannot drift: both carry the block, and neither carries the marker.
func TestTheSameGeneratorRendersBothDeliveryPaths(t *testing.T) {
	local, err := LocalBashRcfile(LaunchOptions{SessionID: "s-1", Enhanced: true})
	if err != nil {
		t.Fatalf("LocalBashRcfile: %v", err)
	}
	if strings.Contains(local, agentBlockMarker) {
		t.Fatalf("the local rcfile carries the marker unsubstituted")
	}
	for _, agent := range agentrecord.ShippedNames() {
		want := agent + "() { __nocx_agent_run " + agent + ` "$@"; }`
		if !strings.Contains(local, want) {
			t.Fatalf("the local rcfile does not wrap %q, which this build ships", agent)
		}
		if !strings.Contains(string(fileOf(t, launchBundle(agentrecord.ShippedNames()), "nocx.bash")), want) {
			t.Fatalf("the published script does not wrap %q", agent)
		}
	}
}

// shippedBundle is what THIS BUILD publishes for its own agent set — the bundle
// every test that is not about the agent set itself means.
func shippedBundle() Bundle { return launchBundle(shippedAgentNames()) }

// fileOf is the named file's bytes, and it fails rather than answering empty:
// a missing file would make every assertion above pass vacuously.
func fileOf(t *testing.T, b Bundle, name string) []byte {
	t.Helper()
	f, ok := b.file(name)
	if !ok {
		t.Fatalf("the bundle carries no %s", name)
	}
	return f.Data
}

// A host already carrying this build's bundle is republished when the agent set
// changes, and NOT republished when nothing about it has — which is the whole
// of "an agent list that changes must be a different generation" (nocx-t5e7d).
// It is asserted at the publisher, because that is where the skip is decided.
func TestAChangedAgentSetIsPublishedAndTheSameOneIsNot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	fsys := NewOSFS()

	first, err := NewPublisher(testLogger(), fsys, root).Publish(launchBundle([]string{"claude"}))
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if !first.Published {
		t.Fatalf("the first publish reported %+v, want a publish", first)
	}

	same, err := NewPublisher(testLogger(), fsys, root).Publish(launchBundle([]string{"claude"}))
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if same.Published {
		t.Fatalf("the same build and the same agent set published again: %+v", same)
	}
	if same.Reason != "already-installed" {
		t.Fatalf("second publish reason = %q, want already-installed", same.Reason)
	}

	// One more enabled agent. Same build, same version — and the host must be
	// told, because the scripts it holds would wrap one agent and this machine
	// offers two.
	grown, err := NewPublisher(testLogger(), fsys, root).Publish(launchBundle([]string{"claude", "codex"}))
	if err != nil {
		t.Fatalf("publish with an added agent: %v", err)
	}
	if !grown.Published {
		t.Fatalf("an added agent did not republish: %+v", grown)
	}
	if grown.Generation == first.Generation {
		t.Fatalf("both sets published under generation %q", grown.Generation)
	}
	if grown.Version != first.Version {
		t.Fatalf("version moved with the agent set (%q -> %q); the version is the BUILD's, and the set is the generation's", first.Version, grown.Version)
	}
	// What the host now holds wraps both, by name.
	installed := string(readFileT(t, filepath.Join(root, integrationDir, grown.Generation, "nocx.bash")))
	for _, want := range []string{`claude() { __nocx_agent_run claude "$@"; }`, `codex() { __nocx_agent_run codex "$@"; }`} {
		if !strings.Contains(installed, want) {
			t.Fatalf("the published script is missing %q", want)
		}
	}
}

// The generated block must PARSE, and a function name a shell cannot define
// would take the whole script down with it rather than just its own line —
// which is why the generator refuses such a name. This runs both delivered
// shells over the real bytes, and calls a hyphenated wrapper in each, because
// `prime-agent` is the name that makes the question concrete.
func TestTheGeneratedBlockParsesAndItsWrappersCallInBothShells(t *testing.T) {
	for _, tc := range []struct{ shell, script string }{
		{"bash", bashScript},
		{"zsh", zshScript},
	} {
		shell, err := exec.LookPath(tc.shell)
		if err != nil {
			t.Skipf("%s is not on this host", tc.shell)
		}
		path := filepath.Join(t.TempDir(), tc.shell+".sh")
		if werr := os.WriteFile(path, []byte(tc.script), 0o600); werr != nil {
			t.Fatalf("write: %v", werr)
		}
		if out, nerr := exec.Command(shell, "-n", path).CombinedOutput(); nerr != nil { //nolint:gosec // the shell under test and a path this test wrote
			t.Fatalf("the delivered %s script does not parse: %v\n%s", tc.shell, nerr, out)
		}
		// And a wrapper for a hyphenated agent is DEFINED and CALLABLE: the
		// block is only useful if the name a person types is a command their
		// shell accepts.
		probe := "__nocx_agent_run() { printf '%s|%s' \"$1\" \"$#\"; }\n" +
			agentWrappers([]string{"prime-agent"}) + "prime-agent one two\n"
		out, cerr := exec.Command(shell, "-c", probe).CombinedOutput() //nolint:gosec // a script this test composed
		if cerr != nil {
			t.Fatalf("%s could not call the generated wrapper: %v\n%s", tc.shell, cerr, out)
		}
		if string(out) != "prime-agent|3" {
			t.Fatalf("%s called the wrapper as %q, want the agent name and the person's own three arguments", tc.shell, out)
		}
	}
}

// THE DELIVERY DISTINCTION, which is the rule rather than a detail (nocx-t5e7d,
// the owner's decision of 2026-10-05): nocx's own tool-surface argument reaches
// a pane on THIS machine and reaches no host at all. One generator renders
// both, so this asserts both halves of the same rule against the real bytes.
func TestTheToolSurfaceArgumentIsLocalOnly(t *testing.T) {
	local, err := LocalBashRcfile(LaunchOptions{SessionID: "s-1", Enhanced: true})
	if err != nil {
		t.Fatalf("LocalBashRcfile: %v", err)
	}
	if !strings.Contains(local, `--mcp-config "$__nocx_agent_launch_dir/mcp.json"`) {
		t.Fatalf("the local rcfile does not point its agent at the tool surface:\n%s", local)
	}

	published := string(fileOf(t, launchBundle([]string{"claude"}), "nocx.bash"))
	if strings.Contains(published, "--mcp-config") {
		t.Fatalf("the published script carries a per-agent argument, and a host receives names and nothing else")
	}
	if !strings.Contains(published, `command "$__agent" "$@"`) {
		t.Fatalf("the published script does not run the agent at all:\n%s", published)
	}
	// And the same rule in the other shell: the entry the wrapper runs is
	// generated per delivery, not per file.
	zsh := string(fileOf(t, launchBundle([]string{"claude"}), "nocx.zsh"))
	if strings.Contains(zsh, "--mcp-config") {
		t.Fatalf("the published zsh script carries a per-agent argument")
	}
	if !strings.Contains(zshScript, "--mcp-config") {
		t.Fatalf("the local zsh script does not point its agent at the tool surface")
	}
}

// The local pane offers the agents the RECORD gives it, not the ones the build
// happens to carry: a person's own agent is wrapped and one they switched off
// is not, without either of them having to be in the binary.
func TestTheLocalPaneOffersTheAgentsItIsGiven(t *testing.T) {
	// The set a person's record produces: their own agent added, the build's
	// agent switched off, which is what internal/agentrecord.EnabledNames
	// returns for that record (its own test pins that half).
	given := []string{"myagent", "codex"}
	rc, err := LocalBashRcfile(LaunchOptions{SessionID: "s-1", Enhanced: true, Agents: given})
	if err != nil {
		t.Fatalf("LocalBashRcfile: %v", err)
	}
	for _, agent := range given {
		want := agent + "() { __nocx_agent_run " + agent + ` "$@"; }`
		if !strings.Contains(rc, want) {
			t.Fatalf("the local pane does not wrap %q, which the record offered:\n%s", agent, rc)
		}
	}
	if strings.Contains(rc, "claude()") {
		t.Fatalf("the local pane still wraps an agent the record did not offer")
	}
	// A caller that named no agents gets this build's own set rather than none:
	// a shell with no wrappers at all is a terminal that quietly stopped being
	// orchestrated.
	plain, err := LocalBashRcfile(LaunchOptions{SessionID: "s-1", Enhanced: true})
	if err != nil {
		t.Fatalf("LocalBashRcfile: %v", err)
	}
	for _, agent := range agentrecord.ShippedNames() {
		if !strings.Contains(plain, agent+"() {") {
			t.Fatalf("a caller that named no agents got no wrapper for %q", agent)
		}
	}
}
