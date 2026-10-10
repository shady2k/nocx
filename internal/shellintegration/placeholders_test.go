package shellintegration

// Placeholder expansion at spawn (nocx-bag8j, the 2026-08-15 spec's §5).
//
// What a person gets that they could not before: a launch line a record
// carries — or a coordinator types — may name the facts of the spawn it is
// part of, and the process the pane starts receives them as arguments rather
// than as a literal "{WORKSPACE_SLUG}" an agent has to misread.
//
// The two ways this goes wrong are both named in the bead, and both are
// asserted here rather than trusted: an unknown placeholder that expands to
// empty silently rewrites the line its author wrote, and an expansion that
// happens BEFORE the argv split turns a branch name with a space into two
// arguments — a failure that appears far from its cause.

import (
	"reflect"
	"strings"
	"testing"
)

// everyPlaceholder is the closed set §5 names, each in the value it is about.
func everyPlaceholder() map[string]string {
	return map[string]string{
		"UUID":           "018f6a2c-7b1d-7cc2-9f3a-4d1e2b3c4d5e",
		"WORKSPACE_SLUG": "the-workspace",
		"WORKSPACE_NAME": "The Workspace",
		"WORKSPACE_ID":   "ws-1234",
		"WORKSPACE_PATH": "/home/someone/repo",
		"BRANCH":         "worker/fix-parse",
		"PORT":           "4321",
	}
}

func TestEachNamedPlaceholderExpandsToItsValue(t *testing.T) {
	values := everyPlaceholder()
	for name, value := range values {
		argv := ExpandAgentArgv([]string{"--name", "{" + name + "}"}, values)
		if len(argv) != 2 {
			t.Fatalf("{%s}: argv = %q, want two elements", name, argv)
		}
		if argv[1] != value {
			t.Fatalf("{%s} expanded to %q, want %q", name, argv[1], value)
		}
	}
}

func TestAPlaceholderExpandsCaseInsensitively(t *testing.T) {
	values := everyPlaceholder()
	argv := ExpandAgentArgv([]string{"--name", "{workspace_slug}", "{Branch}", "{uuid}"}, values)
	want := []string{"--name", "the-workspace", "worker/fix-parse", "018f6a2c-7b1d-7cc2-9f3a-4d1e2b3c4d5e"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

// THE FALSIFIER, first in the pair: an unknown placeholder passes THROUGH.
// termic passes it through deliberately, so an odd argument shape is not
// silently mangled into a different but plausible one; the reason travels
// with the behaviour.
func TestAnUnknownPlaceholderPassesThroughUnchanged(t *testing.T) {
	values := everyPlaceholder()
	for _, token := range []string{"{NOT_A_PLACEHOLDER}", "{workspace-slug}", "{}", "{ WORKSPACE_SLUG }", "{WORKSPACE_SLUG"} {
		argv := ExpandAgentArgv([]string{token}, values)
		if len(argv) != 1 || argv[0] != token {
			t.Fatalf("%q became %q, and an unknown placeholder reaching the process as something else is a line rewritten in silence", token, argv)
		}
	}
}

// A placeholder with NO value in the set is the same fact: expansion has
// nothing to say, so the token the record carries is the token the process
// sees. Emptying it would make a launch lie about the argument it was given.
func TestAPlaceholderWithNoValueReachesTheProcessUnchanged(t *testing.T) {
	values := everyPlaceholder()
	delete(values, "PORT")
	argv := ExpandAgentArgv([]string{"--port", "{PORT}", "--name", "{WORKSPACE_SLUG}"}, values)
	want := []string{"--port", "{PORT}", "--name", "the-workspace"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

// A placeholder inside a larger argument expands in place — termic's shape:
// the token is the unit of substitution, not the argument.
func TestAPlaceholderInsideAnArgumentExpandsInPlace(t *testing.T) {
	values := everyPlaceholder()
	argv := ExpandAgentArgv([]string{"--model-path", "/tmp/{WORKSPACE_SLUG}/model"}, values)
	if len(argv) != 2 || argv[1] != "/tmp/the-workspace/model" {
		t.Fatalf("argv = %q, want the value substituted inside the argument", argv)
	}
}

// THE SECOND FALSIFIER: expansion happens AFTER the argv split, so a value
// containing a space stays ONE argument. Doing it the other way round —
// expanding the string and then splitting it — is the defect the bead names:
// a branch name with a space becomes two arguments and the failure appears
// far from its cause.
func TestAValueContainingASpaceStaysOneArgvElement(t *testing.T) {
	values := everyPlaceholder()
	values["WORKSPACE_NAME"] = "The Main Workspace"
	values["BRANCH"] = "worker/fix parse"
	argv := ExpandAgentArgv([]string{"--name", "{WORKSPACE_NAME}", "--branch", "{BRANCH}"}, values)
	want := []string{"--name", "The Main Workspace", "--branch", "worker/fix parse"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

// SplitAgentCommand is the other half of the same decision: a free-form
// command string becomes argv honouring quotes, and the expansion runs over
// THAT. The two together are what a record's args line is.
func TestAFreeFormCommandSplitsHonouringQuotes(t *testing.T) {
	argv, err := SplitAgentCommand(`claude --name "The Main Workspace" --flag 'a b' bare`)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	want := []string{"claude", "--name", "The Main Workspace", "--flag", "a b", "bare"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

// A quote INSIDE an argument of the other kind is data, and an unclosed quote
// is a line nobody can launch honestly — the split refuses it rather than
// guessing where it ended.
func TestSplitAgentCommandRefusesAnUnclosedQuote(t *testing.T) {
	if _, err := SplitAgentCommand(`claude --name "never closed`); err == nil {
		t.Fatal("an unclosed quote split into a launch, and a line the shell itself would refuse must not become one nocx runs")
	}
}

// The whole path: split, expand, and the line the pane receives puts the
// value with the space back as ONE argument — this is the composition the
// spawn asks for, asserted as a round trip.
func TestACommandLineRoundTripsThroughSplitExpandAndQuote(t *testing.T) {
	values := everyPlaceholder()
	values["BRANCH"] = "worker/fix parse"
	argv, err := SplitAgentCommand(`claude --branch {BRANCH} --name {WORKSPACE_NAME}`)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	got := ExpandAgentArgv(argv, values)
	want := []string{"claude", "--branch", "worker/fix parse", "--name", "The Workspace"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	line := QuoteAgentArgv(got)
	again, err := SplitAgentCommand(line)
	if err != nil {
		t.Fatalf("re-split %q: %v", line, err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("the quoted line %q split back to %q, want %q", line, again, want)
	}
	if !strings.Contains(line, "'worker/fix parse'") {
		t.Fatalf("the quoted line %q does not keep the spaced value one argument", line)
	}
}
