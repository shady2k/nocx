package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// fakeShell answers one query the way a live shell would, and records what
// it was asked — the seam is asserted BY TRYING, never by inspecting the
// classifier a second time.
type fakeShell struct {
	mu       sync.Mutex
	asked    []ExpansionQuery
	values   map[string]string
	counts   map[string]int
	programs []ProgramFact
	err      error
}

func (f *fakeShell) Expand(_ context.Context, _ string, q ExpansionQuery) (ExpansionAnswer, error) {
	f.mu.Lock()
	f.asked = append(f.asked, q)
	f.mu.Unlock()
	if f.err != nil {
		return ExpansionAnswer{}, f.err
	}
	answer := ExpansionAnswer{Programs: f.programs}
	for _, expr := range q.Expressions {
		value, ok := f.values[expr]
		if !ok {
			continue
		}
		answer.Values = append(answer.Values, ExpansionValue{
			Expression: expr, Value: value, Count: f.counts[expr],
		})
	}
	return answer, nil
}

func (f *fakeShell) queries() []ExpansionQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ExpansionQuery(nil), f.asked...)
}

func partFor(t *testing.T, facts ExpansionFacts, text string) ExpansionPart {
	t.Helper()
	for _, p := range facts.Parts {
		if p.Text == text {
			return p
		}
	}
	t.Fatalf("no part %q in %+v", text, facts.Parts)
	return ExpansionPart{}
}

// The acceptance criterion the window is judged on: the expanded form sits
// BESIDE the verbatim one, every unsafe expansion is left exactly as written
// and labelled with its reason, and the verbatim command is untouched.
func TestExpansionFactsFor_ShowsTheExpandedFormBesideTheVerbatimOne(t *testing.T) {
	shell := &fakeShell{values: map[string]string{"$HOME": "/home/dev"}}
	const command = "rm -rf $HOME/x $(id -u)"

	facts := ExpansionFactsFor(context.Background(), shell, "session-a", command)

	if !facts.Asked {
		t.Fatalf("asked = false (%s), want true: the shell answered", facts.Reason)
	}
	if facts.Command == command {
		t.Fatalf("display = %q, want the expanded form beside the verbatim one", facts.Command)
	}
	if !strings.Contains(facts.Command, "/home/dev/x") {
		t.Fatalf("display = %q, want $HOME replaced by what the shell said", facts.Command)
	}
	if !strings.Contains(facts.Command, "$(id -u)") {
		t.Fatalf("display = %q, want the command substitution left exactly as written", facts.Command)
	}
	unsafe := partFor(t, facts, "$(id -u)")
	if unsafe.State != ExpansionUnsafe {
		t.Fatalf("$(id -u) state = %q, want %q", unsafe.State, ExpansionUnsafe)
	}
	if unsafe.Reason == "" {
		t.Fatal("an unsafe expansion reached the surface with no reason to show")
	}
	expanded := partFor(t, facts, "$HOME")
	if expanded.State != ExpansionExpanded || expanded.Value != "/home/dev" {
		t.Fatalf("$HOME part = %+v, want expanded to /home/dev", expanded)
	}
}

// Criterion 3, at the seam a shell would actually be reached through: no
// query the source ever receives names a command substitution.
func TestExpansionFactsFor_NeverAsksForACommandSubstitution(t *testing.T) {
	shell := &fakeShell{values: map[string]string{"$HOME": "/home/dev"}}
	ExpansionFactsFor(context.Background(), shell, "session-a", "cat $HOME/x $(hostname) `date` <(ls)")
	for _, q := range shell.queries() {
		for _, expr := range q.Expressions {
			if strings.ContainsAny(expr, "`") || strings.Contains(expr, "$(") ||
				strings.Contains(expr, "<(") || strings.Contains(expr, ">(") {
				t.Fatalf("the shell was asked for %q, which it would have to execute", expr)
			}
		}
	}
}

// One query per approval, at the seam: several variables and several
// program words, one round trip.
func TestExpansionFactsFor_AsksOnce(t *testing.T) {
	shell := &fakeShell{values: map[string]string{"$HOME": "/h", "$USER": "dev"}}
	ExpansionFactsFor(context.Background(), shell, "session-a", "cp $HOME/a $USER && ls $HOME")
	if got := len(shell.queries()); got != 1 {
		t.Fatalf("the shell was asked %d times for one approval, want 1", got)
	}
}

// Where our integration is not deployed — a remote host, a native prompt —
// expand nothing and mark every variable unresolved. The surface must be
// able to tell this apart from "unsafe, left as written", so the state is
// its own.
func TestExpansionFactsFor_WithNoSourceMarksEveryVariableNotAsked(t *testing.T) {
	const command = "rm -rf $HOME/x"
	facts := ExpansionFactsFor(context.Background(), nil, "session-a", command)

	if facts.Asked {
		t.Fatal("asked = true with no source wired")
	}
	if facts.Reason == "" {
		t.Fatal("nothing was expanded and the window was given no reason to show")
	}
	if facts.Command != command {
		t.Fatalf("display = %q, want the verbatim command when nothing was read", facts.Command)
	}
	if got := partFor(t, facts, "$HOME").State; got != ExpansionUnasked {
		t.Fatalf("$HOME state = %q, want %q — never %q, which would claim we refused",
			got, ExpansionUnasked, ExpansionUnsafe)
	}
}

// The external call FAILS: the shell is there and does not answer. The
// question is thinner, never refused, and the reason says so.
func TestExpansionFactsFor_AShellThatDoesNotAnswerIsNotAsked(t *testing.T) {
	shell := &fakeShell{err: errors.New("the channel closed")}
	facts := ExpansionFactsFor(context.Background(), shell, "session-a", "rm -rf $HOME/x")

	if facts.Asked {
		t.Fatal("asked = true although the shell returned an error")
	}
	if !strings.Contains(facts.Reason, "the channel closed") {
		t.Fatalf("reason = %q, want it to carry what went wrong", facts.Reason)
	}
	if got := partFor(t, facts, "$HOME").State; got != ExpansionUnasked {
		t.Fatalf("$HOME state = %q, want %q", got, ExpansionUnasked)
	}
}

// A named unavailability reaches the person in its own words, so "no
// integration here" and "the shell did not answer" are not one sentence.
func TestExpansionFactsFor_CarriesTheSourcesOwnUnavailableSentence(t *testing.T) {
	shell := &fakeShell{err: &ExpansionUnavailableError{Reason: "this host has no nocx integration"}}
	facts := ExpansionFactsFor(context.Background(), shell, "session-a", "ls $HOME")
	if facts.Reason != "this host has no nocx integration" {
		t.Fatalf("reason = %q, want the source's own sentence", facts.Reason)
	}
	if !errors.Is(&ExpansionUnavailableError{}, ErrExpansionUnavailable) {
		t.Fatal("a named unavailability must still test as ErrExpansionUnavailable")
	}
}

// A glob answers with a COUNT: the list can be enormous and 143 paths inline
// is not a question anybody can read.
func TestExpansionFactsFor_AGlobCarriesItsCount(t *testing.T) {
	shell := &fakeShell{
		values: map[string]string{"*.log": "a.log b.log …"},
		counts: map[string]int{"*.log": 143},
	}
	facts := ExpansionFactsFor(context.Background(), shell, "session-a", "rm *.log")
	part := partFor(t, facts, "*.log")
	if part.State != ExpansionExpanded || part.Count != 143 {
		t.Fatalf("glob part = %+v, want expanded with a count of 143", part)
	}
}

// The alias question: nocx does not read rc files, so what `ls` MEANS is a
// fact only the live shell has.
func TestExpansionFactsFor_CarriesWhatEachProgramWordIs(t *testing.T) {
	shell := &fakeShell{programs: []ProgramFact{{Word: "ls", Kind: ProgramAlias, Target: "ls --color"}}}
	facts := ExpansionFactsFor(context.Background(), shell, "session-a", "ls /tmp")
	if len(facts.Programs) != 1 || facts.Programs[0].Kind != ProgramAlias {
		t.Fatalf("programs = %+v, want ls reported as an alias", facts.Programs)
	}
}

// ── the fence between the question and the submission ─────────────────────

// Criterion 4: a value that changed refuses the run, and the sentence names
// the variable.
func TestVerifyExpansions_RefusesNamingTheVariableThatChanged(t *testing.T) {
	shell := &fakeShell{values: map[string]string{"$HOME": "/tmp"}}
	shown := []ExpansionValue{{Expression: "$HOME", Value: "/home/dev"}}

	err := VerifyExpansions(context.Background(), shell, "session-a", shown)

	var changed *ExpansionChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("err = %v, want an ExpansionChangedError", err)
	}
	if changed.Expression != "$HOME" {
		t.Fatalf("the refusal names %q, want $HOME", changed.Expression)
	}
	if !strings.Contains(err.Error(), "$HOME") {
		t.Fatalf("the sentence %q does not name the variable", err.Error())
	}
}

// A value that vanished — the variable is now unset — is a change too. The
// same refusal, named the same way.
func TestVerifyExpansions_RefusesWhenAValueIsNoLongerReadable(t *testing.T) {
	shell := &fakeShell{values: map[string]string{}}
	shown := []ExpansionValue{{Expression: "$HOME", Value: "/home/dev"}}
	var changed *ExpansionChangedError
	if err := VerifyExpansions(context.Background(), shell, "session-a", shown); !errors.As(err, &changed) {
		t.Fatalf("err = %v, want the run refused when the value can no longer be read back", err)
	}
}

// The external call fails at the fence. An approval whose premise cannot be
// re-established is not an approval: refuse, do not proceed.
func TestVerifyExpansions_RefusesWhenTheShellCannotBeAskedAgain(t *testing.T) {
	shell := &fakeShell{err: errors.New("the channel closed")}
	shown := []ExpansionValue{{Expression: "$HOME", Value: "/home/dev"}}
	var unverifiable *ExpansionUnverifiableError
	if err := VerifyExpansions(context.Background(), shell, "session-a", shown); !errors.As(err, &unverifiable) {
		t.Fatalf("err = %v, want the run refused when the values cannot be re-read", err)
	}
}

// Nothing was ever expanded — the remote host — so there is nothing to
// compare and the call proceeds. The person approved a window that said
// exactly that; refusing here would refuse every command on every
// un-integrated host.
func TestVerifyExpansions_AllowsWhenNothingWasExpanded(t *testing.T) {
	if err := VerifyExpansions(context.Background(), nil, "session-a", nil); err != nil {
		t.Fatalf("err = %v, want no refusal when nothing was expanded", err)
	}
}

func TestVerifyExpansions_AllowsWhenEveryValueStillReadsTheSame(t *testing.T) {
	shell := &fakeShell{values: map[string]string{"$HOME": "/home/dev"}}
	shown := []ExpansionValue{{Expression: "$HOME", Value: "/home/dev"}}
	if err := VerifyExpansions(context.Background(), shell, "session-a", shown); err != nil {
		t.Fatalf("err = %v, want the run allowed when nothing moved", err)
	}
}

// A glob whose match COUNT moved is a change: the same pattern now names a
// different set of paths, which is exactly the substitution hazard.
func TestVerifyExpansions_RefusesWhenAGlobNowMatchesADifferentSet(t *testing.T) {
	shell := &fakeShell{values: map[string]string{"*.log": "a.log"}, counts: map[string]int{"*.log": 9}}
	shown := []ExpansionValue{{Expression: "*.log", Value: "a.log", Count: 1}}
	var changed *ExpansionChangedError
	if err := VerifyExpansions(context.Background(), shell, "session-a", shown); !errors.As(err, &changed) {
		t.Fatalf("err = %v, want the run refused when the match set moved", err)
	}
}

// ── the criterion that matters most ───────────────────────────────────────

// THE COMMAND SENT TO THE SHELL IS BYTE-IDENTICAL TO THE ONE THE MODEL
// PROPOSED. Asserted directly, through the seam a command actually leaves by
// — nothing in the expansion path may re-quote, normalise or substitute it.
func TestExecuteRun_SubmitsTheModelsCommandByteForByte(t *testing.T) {
	exitZero := 0
	for _, command := range []string{
		`rm -rf $HOME/x`,
		`echo "$HOME  spaced"`,
		`cat ${HOME:-/tmp}/a  ${HOME#/}`,
		"ls $(pwd) `date` ~/x *.log",
		`printf '%s\n' "$USER"`,
		`HOME=/tmp rm -rf $HOME/x`,
		"echo 'a'\"b\"c\\ d",
	} {
		t.Run(command, func(t *testing.T) {
			runner := agenttools.NewRunner([]content.GrantScope{
				{Kind: content.ResourceSession, ID: "session-a"},
			})
			req := &recordingRunner{
				body: runResolvedBody("entry-1", &exitZero, "success", 1, 0, 1, "ok"),
			}
			args, err := json.Marshal(map[string]string{"command": command})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := executeRun(toolTestContext(), runner, req, args, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			calls := req.runCalls()
			if len(calls) != 1 {
				t.Fatalf("the renderer was asked %d times, want once", len(calls))
			}
			if calls[0].command != command {
				t.Fatalf("the command submitted was %q, want the model's own %q byte for byte",
					calls[0].command, command)
			}
		})
	}
}

// A command whose every expansion is refused expands to ITSELF — the
// display form is byte-identical to the verbatim one, so a person is never
// shown a second block implying something was resolved when nothing was.
func TestExpansionFactsFor_ACommandWithNothingSafeExpandsToItself(t *testing.T) {
	shell := &fakeShell{values: map[string]string{"$HOME": "/home/dev"}}
	const command = "cat $(hostname) `date` ${x:=1}"
	facts := ExpansionFactsFor(context.Background(), shell, "session-a", command)
	if facts.Command != command {
		t.Fatalf("display = %q, want the verbatim command %q", facts.Command, command)
	}
	for _, part := range facts.Parts {
		if part.State != ExpansionUnsafe {
			t.Fatalf("part %+v reached the surface as %q, want every one unsafe", part, part.State)
		}
	}
}
