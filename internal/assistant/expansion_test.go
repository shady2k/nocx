package assistant

import (
	"strings"
	"testing"
)

// The classifier is SYNTACTIC and therefore decidable: a construct is safe to
// ask a live shell for when reading it cannot change anything. These tables
// are the whole contract — a construct that is not in the safe table is not
// asked for, and one that is not in the unsafe table must never appear there.

func expansionOf(t *testing.T, command, text string) Expansion {
	t.Helper()
	report := expansionsIn(command)
	for _, e := range report.Expansions {
		if e.Text == text {
			return e
		}
	}
	t.Fatalf("expansionsIn(%q) reported no expansion %q; got %+v", command, text, report.Expansions)
	return Expansion{}
}

func TestExpansionsIn_PureReadsMayBeAskedFor(t *testing.T) {
	for _, tc := range []struct {
		command string
		text    string
		kind    ExpansionKind
	}{
		{`cat $HOME/x`, `$HOME`, ExpansionParameter},
		{`cat ${HOME}/x`, `${HOME}`, ExpansionParameter},
		{`cat ${HOME:-/tmp}/x`, `${HOME:-/tmp}`, ExpansionParameter},
		{`cat ${HOME#/}/x`, `${HOME#/}`, ExpansionParameter},
		{`cat ${HOME/a/b}/x`, `${HOME/a/b}`, ExpansionParameter},
		{`cat ${#HOME}`, `${#HOME}`, ExpansionParameter},
		{`cat "$HOME/x"`, `$HOME`, ExpansionParameter},
		{`cat ~/x`, `~/x`, ExpansionTilde},
		{`cat ~alice/x`, `~alice/x`, ExpansionTilde},
		{`ls *.log`, `*.log`, ExpansionGlob},
		{`ls a{b,c}d`, `a{b,c}d`, ExpansionBrace},
		{`echo $((1 + 2))`, `$((1 + 2))`, ExpansionArithmetic},
	} {
		t.Run(tc.command, func(t *testing.T) {
			got := expansionOf(t, tc.command, tc.text)
			if !got.Askable {
				t.Fatalf("%q: askable = false (%s), want true", tc.text, got.Reason)
			}
			if got.Kind != tc.kind {
				t.Fatalf("%q: kind = %q, want %q", tc.text, got.Kind, tc.kind)
			}
			if got.Reason != "" {
				t.Fatalf("%q: askable expansion carries reason %q", tc.text, got.Reason)
			}
		})
	}
}

func TestExpansionsIn_EffectfulExpansionsAreNeverAskedFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		text    string
		reason  string
	}{
		{"command substitution", "cat $(pwd)/x", "$(pwd)/x", "runs a command"},
		{"backticks", "cat `pwd`/x", "`pwd`/x", "runs a command"},
		{"process substitution in", "diff <(ls) b", "<(ls)", "runs a command"},
		{"process substitution out", "tee >(cat) b", ">(cat)", "runs a command"},
		{"assigning default", "cat ${HOME:=/tmp}/x", "${HOME:=/tmp}/x", "assigns"},
		{"assigning default unset form", "cat ${HOME=/tmp}/x", "${HOME=/tmp}/x", "assigns"},
		{"error when unset", "cat ${HOME:?gone}/x", "${HOME:?gone}/x", "exits the shell"},
		{"error when unset short", "cat ${HOME?gone}/x", "${HOME?gone}/x", "exits the shell"},
		{"arithmetic increment", "echo $((i++))", "$((i++))", "assigns"},
		{"arithmetic assignment", "echo $((i = 5))", "$((i = 5))", "assigns"},
		{"nested substitution in a brace", "cat ${x:-$(pwd)}", "${x:-$(pwd)}", "runs a command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := expansionOf(t, tc.command, tc.text)
			if got.Askable {
				t.Fatalf("%q: askable = true, want false", tc.text)
			}
			if !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("%q: reason = %q, want it to contain %q", tc.text, got.Reason, tc.reason)
			}
		})
	}
}

// The acceptance criterion, asserted at the classifier: no query ever names a
// command substitution, whatever else the command contains.
func TestExpansionsIn_ACommandSubstitutionIsNeverInTheQuery(t *testing.T) {
	q := ExpansionQueryFor("cat $HOME/x $(hostname).log `date`")
	for _, expr := range q.Expressions {
		if strings.Contains(expr, "$(") || strings.Contains(expr, "`") {
			t.Fatalf("the query carries %q, which the shell would have to execute", expr)
		}
	}
	if len(q.Expressions) == 0 {
		t.Fatal("the query named nothing at all; $HOME is a pure read and should be in it")
	}
}

func TestExpansionsIn_SingleQuotesExpandNothing(t *testing.T) {
	report := expansionsIn(`echo '$HOME ~ *.log $(pwd)'`)
	if len(report.Expansions) != 0 {
		t.Fatalf("single-quoted text produced expansions: %+v", report.Expansions)
	}
}

func TestExpansionsIn_DoubleQuotesSuppressGlobAndTilde(t *testing.T) {
	report := expansionsIn(`ls "*.log" "~/x"`)
	if len(report.Expansions) != 0 {
		t.Fatalf("double-quoted glob/tilde produced expansions: %+v", report.Expansions)
	}
}

// A word is expanded as a whole, so one unsafe construct anywhere in it makes
// the whole word unsafe: the safe half of a word cannot be shown expanded
// while the rest is left as written without misdescribing what will run.
func TestExpansionsIn_AnUnsafeConstructPoisonsItsWholeWord(t *testing.T) {
	got := expansionOf(t, "cat $HOME/$(id -u)/x", "$HOME/$(id -u)/x")
	if got.Askable {
		t.Fatal("a word carrying a command substitution was offered to the shell")
	}
	q := ExpansionQueryFor("cat $HOME/$(id -u)/x")
	if len(q.Expressions) != 0 {
		t.Fatalf("query = %v, want nothing askable in that command", q.Expressions)
	}
}

// A value that changes on every read cannot be shown as a fact, and asking
// for one would make the re-read comparison refuse every run that used it.
func TestExpansionsIn_VolatileParametersAreNotFacts(t *testing.T) {
	got := expansionOf(t, "echo $RANDOM", "$RANDOM")
	if got.Askable {
		t.Fatal("$RANDOM was offered to the shell as though it were a fact")
	}
	if !strings.Contains(got.Reason, "changes") {
		t.Fatalf("reason = %q, want it to say the value changes", got.Reason)
	}
}

// Criterion 5: `HOME=/tmp cmd` is the PARSE's business. The live shell knows
// nothing about it, so the expansion of an assigned name is not asked for and
// the assignment itself is reported as a fact.
func TestExpansionsIn_AnAssignmentPrefixIsAccountedForByTheParse(t *testing.T) {
	report := expansionsIn("HOME=/tmp rm -rf $HOME/x")
	if len(report.Assignments) != 1 ||
		report.Assignments[0].Name != "HOME" || report.Assignments[0].Value != "/tmp" {
		t.Fatalf("assignments = %+v, want one HOME=/tmp", report.Assignments)
	}
	got := expansionOf(t, "HOME=/tmp rm -rf $HOME/x", "$HOME")
	if got.Askable {
		t.Fatal("$HOME was asked of the shell although the command re-points HOME itself")
	}
	if !strings.Contains(got.Reason, "HOME") {
		t.Fatalf("reason = %q, want it to name HOME", got.Reason)
	}
	if q := ExpansionQueryFor("HOME=/tmp rm -rf $HOME/x"); len(q.Expressions) != 0 {
		t.Fatalf("query = %v, want nothing: the shell cannot answer for this HOME", q.Expressions)
	}
}

// The program word of every subcommand is asked about, because an alias or a
// function can make `ls` mean something else and nocx does not read rc files.
func TestExpansionQueryFor_AsksWhatEachProgramWordIs(t *testing.T) {
	q := ExpansionQueryFor("ls -l | grep x && rm -rf /tmp/y")
	want := map[string]bool{"ls": false, "grep": false, "rm": false}
	for _, p := range q.Programs {
		if _, ok := want[p]; !ok {
			t.Fatalf("query names program %q, which is not a program word of the command", p)
		}
		want[p] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("query does not ask what %q is", name)
		}
	}
}

// One query per approval, not one per variable — and each expression once.
func TestExpansionQueryFor_IsOneQueryWithNoDuplicates(t *testing.T) {
	q := ExpansionQueryFor("cp $HOME/a $HOME/b && ls $HOME")
	count := 0
	for _, expr := range q.Expressions {
		if expr == "$HOME" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("$HOME appears %d times in one query, want 1", count)
	}
}

// The classifier and the effect parser must not drift: every construct
// splitCommand disqualifies a command for is a construct the classifier
// refuses to ask about.
func TestExpansionsIn_AgreesWithTheParserOnSubstitution(t *testing.T) {
	for _, command := range []string{
		"cat $(pwd)",
		"cat `pwd`",
		"diff <(ls) b",
		`cat "$(pwd)"`,
		"cat ${x:-$(pwd)}",
	} {
		t.Run(command, func(t *testing.T) {
			_, disqualified, ok := splitCommand(command)
			if !ok || !disqualified {
				t.Fatalf("splitCommand(%q) = disqualified %v, ok %v — the premise of this test is wrong", command, disqualified, ok)
			}
			report := expansionsIn(command)
			unsafe := false
			for _, e := range report.Expansions {
				if !e.Askable {
					unsafe = true
				}
			}
			if !unsafe {
				t.Fatalf("expansionsIn(%q) found nothing unsafe while the parser disqualified it", command)
			}
		})
	}
}

// The display form: the expanded text sits BESIDE the verbatim string and
// never replaces it, and every unsafe part is left exactly as written.
func TestExpandedDisplay_SubstitutesOnlyWhatWasAnswered(t *testing.T) {
	command := "cat $HOME/x $(id -u) $USER"
	report := expansionsIn(command)
	values := map[string]string{"$HOME": "/home/dev", "$USER": "dev"}
	got := expandedDisplay(command, report, values)
	if strings.Contains(got, "$HOME") {
		t.Fatalf("display = %q, want $HOME replaced by its value", got)
	}
	if !strings.Contains(got, "$(id -u)") {
		t.Fatalf("display = %q, want the command substitution left exactly as written", got)
	}
	if got == command {
		t.Fatalf("display = %q, want it to differ from the verbatim command", got)
	}
}

func TestExpandedDisplay_WithNoAnswersIsTheVerbatimCommand(t *testing.T) {
	command := "cat $HOME/x"
	if got := expandedDisplay(command, expansionsIn(command), nil); got != command {
		t.Fatalf("display = %q, want the verbatim command %q", got, command)
	}
}
