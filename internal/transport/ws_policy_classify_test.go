package transport

// policy.classify over the real socket: the method that READS a command line
// a person typed, so a widening permit can be minted from a classification
// rather than from a word somebody liked the look of.
//
// The first test in this file is the one the method exists for. Every other
// property here — the program word, the effect, the refusals — is worth
// having, and none of them would matter if the act of asking "what does this
// command do" could do the thing.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// classify drives policy.classify over the real socket and answers with the
// result and the error, so each test asserts the one it is about.
func classify(t *testing.T, h *askHarness, command string) (policyClassifyResult, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCall(t, h.conn, "policy.classify", map[string]any{"command": command})
	var env struct {
		Result policyClassifyResult `json:"result"`
		Error  *jsonrpcErrorObj     `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("policy.classify %s: %v", raw, err)
	}
	return env.Result, env.Error
}

// TestPolicyClassify_ReadsTheCommandAndNeverRunsIt is the criterion this whole
// method exists for, asserted the only way it can be: with commands whose
// execution would leave something behind, and a filesystem that is empty
// afterwards.
//
// A permit is minted from what this call answers. If the reading were a run,
// then "may I allow this?" would already have done it — and the surface that
// asks would be the most dangerous control in the product rather than the
// safest. Three shapes are driven because three different parts of the parser
// touch the text: an ordinary program with an operand, an option whose value
// is a written path, and a shell redirection.
func TestPolicyClassify_ReadsTheCommandAndNeverRunsIt(t *testing.T) {
	h, _ := newPolicyHarness(t)
	dir := t.TempDir()
	proofs := map[string]string{
		"touch":       filepath.Join(dir, "touched"),
		"option path": filepath.Join(dir, "sorted"),
		"redirection": filepath.Join(dir, "redirected"),
	}
	for name, command := range map[string]string{
		"touch":       "touch " + proofs["touch"],
		"option path": "sort -o " + proofs["option path"] + " /etc/hosts",
		"redirection": "echo proof > " + proofs["redirection"],
	} {
		t.Run(name, func(t *testing.T) {
			if _, rpcErr := classify(t, h, command); rpcErr != nil {
				t.Fatalf("policy.classify %q: %+v", command, rpcErr)
			}
		})
	}
	for name, path := range proofs {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: %q exists after policy.classify (stat err %v); "+
				"the method RAN the command it was asked to read", name, path, err)
		}
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("temp dir holds %d entries after three classifications (err %v); it must hold none",
			len(entries), err)
	}
}

// TestPolicyClassify_AnswersTheReadingARunWouldHave: the command word, the
// canonical parse and the effect, all from the parser and the classifier a RUN
// uses. A second reading here would offer a permit for one account of the
// command and enforce another.
func TestPolicyClassify_AnswersTheReadingARunWouldHave(t *testing.T) {
	h, _ := newPolicyHarness(t)

	got, rpcErr := classify(t, h, "df -h")
	if rpcErr != nil {
		t.Fatalf("policy.classify: %+v", rpcErr)
	}
	if !got.Eligible {
		t.Fatalf("df -h is not eligible (%q); a command the parser reads whole can be widened", got.Reason)
	}
	if got.Program != "df" {
		t.Fatalf("program = %q, want df", got.Program)
	}
	if got.Effect != content.EffectObserve {
		t.Fatalf("effect = %q, want observe — what a run classifying `df -h` would see", got.Effect)
	}
	if len(got.Commands) != 1 || len(got.Commands[0]) != 2 ||
		got.Commands[0][0] != "df" || got.Commands[0][1] != "-h" {
		t.Fatalf("commands = %v, want [[df -h]] — the canonical parse a rule is written over", got.Commands)
	}
	if got.Reason != "" {
		t.Fatalf("reason = %q on an eligible reading, want empty", got.Reason)
	}
}

// TestPolicyClassify_NamesTheFeatureARefusalCanMatch: a refusal over a class of
// command lines matches a FACT the classifier recorded, never the spelling of a
// token, so the surface offering that refusal has to be told which facts this
// command carries.
func TestPolicyClassify_NamesTheFeatureARefusalCanMatch(t *testing.T) {
	h, _ := newPolicyHarness(t)

	got, rpcErr := classify(t, h, "sort -o /tmp/out /tmp/in")
	if rpcErr != nil {
		t.Fatalf("policy.classify: %+v", rpcErr)
	}
	if !got.Eligible {
		t.Fatalf("sort -o is not eligible: %q", got.Reason)
	}
	if len(got.Features) != 1 || got.Features[0] != content.FeatureWritesOptionNamedPath {
		t.Fatalf("features = %v, want [%s]", got.Features, content.FeatureWritesOptionNamedPath)
	}

	plain, rpcErr := classify(t, h, "df -h")
	if rpcErr != nil {
		t.Fatalf("policy.classify: %+v", rpcErr)
	}
	if len(plain.Features) != 0 {
		t.Fatalf("features = %v on a command carrying none, want []", plain.Features)
	}
}

// TestPolicyClassify_RefusesWhatCannotBecomeARule, in content's own words.
//
// Each of these is a command whose meaning can differ between the reading and
// the next match, so a rule written over it would be a permission a person
// believes they have and does not. content.StandingRule already decides this
// and already says why for each; classify asks IT rather than deciding a second
// time, and the assertion is on the sentence a person is shown.
func TestPolicyClassify_RefusesWhatCannotBecomeARule(t *testing.T) {
	h, _ := newPolicyHarness(t)

	for name, tc := range map[string]struct{ command, contains string }{
		"a wrapper":            {"sudo df -h", "indirect wrapper"},
		"a compound":           {"df -h && rm -rf /tmp/x", "more than one command"},
		"an unresolved token":  {"cat $HOME/notes", "unresolved input"},
		"a command not read":   {"find . -name x", "unresolved input"},
		"a shell substitution": {"cat $(which df)", "shell feature"},
	} {
		t.Run(name, func(t *testing.T) {
			got, rpcErr := classify(t, h, tc.command)
			if rpcErr != nil {
				t.Fatalf("policy.classify %q: %+v", tc.command, rpcErr)
			}
			if got.Eligible {
				t.Fatalf("%q is eligible; a rule over it would cover command lines nobody read", tc.command)
			}
			if got.Reason == "" {
				t.Fatalf("%q was refused silently; a person cannot act on nothing", tc.command)
			}
			if !strings.Contains(got.Reason, tc.contains) {
				t.Fatalf("reason = %q, want it to say %q", got.Reason, tc.contains)
			}
			if got.Effect != "" {
				t.Fatalf("effect = %q on a reading that failed; an effect nobody can rely on is worse than none",
					got.Effect)
			}
		})
	}
}

// TestPolicyClassify_MintsAPermitBoundToTheEffectItWasReadUnder drives the whole
// gesture the page makes: read the command, then write the widened rule the
// reading justifies, then ask the evaluator what it now decides.
//
// The point is the second half. The permit reaches the same program while it
// keeps doing what it was read doing, and does NOT reach it doing something the
// reading never saw — which is what `grantedUnder` is for and why a permit may
// not be typed.
func TestPolicyClassify_MintsAPermitBoundToTheEffectItWasReadUnder(t *testing.T) {
	h, store := newPolicyHarness(t)

	read, rpcErr := classify(t, h, "sort /tmp/in")
	if rpcErr != nil {
		t.Fatalf("policy.classify: %+v", rpcErr)
	}
	if !read.Eligible || read.Effect != content.EffectObserve {
		t.Fatalf("classify = %+v, want an eligible observe reading", read)
	}

	if _, rpcErr := setRule(t, h, map[string]any{
		"selector":     map[string]any{"program": read.Program},
		"decision":     "permit",
		"grantedUnder": string(read.Effect),
	}); rpcErr != nil {
		t.Fatalf("policy.setRule: %+v", rpcErr)
	}

	policy := store.Policy()
	reading := policy.EvaluateInvocation(content.EffectObserve,
		assistant.CanonicalInvocation("sort /etc/hosts"), nil)
	if reading.Decision != content.DecisionPermit {
		t.Fatalf("an observing sort = %s, want permit — the rule was granted for exactly this",
			reading.Decision)
	}
	writing := policy.EvaluateInvocation(content.EffectMutateReversible,
		assistant.CanonicalInvocation("sort -o /tmp/out /tmp/in"), nil)
	if writing.Decision == content.DecisionPermit {
		t.Fatalf("a writing sort = permit; the permit was granted while the command only read")
	}
}

// TestPolicyClassify_RefusesAnEmptyOrOversizedCommand — the envelope, at the
// same bound policy.explain reads a command line to. A truncated command would
// be a classification of a different command.
func TestPolicyClassify_RefusesAnEmptyOrOversizedCommand(t *testing.T) {
	h, _ := newPolicyHarness(t)

	for name, params := range map[string]map[string]any{
		"empty":   {"command": ""},
		"missing": {},
		"unknown key": {
			"command": "df -h", "effect": "observe",
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := jsonrpcCall(t, h.conn, "policy.classify", params)
			var env struct {
				Error *jsonrpcErrorObj `json:"error"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("policy.classify %s: %v", raw, err)
			}
			if env.Error == nil || env.Error.Code != -32602 {
				t.Fatalf("policy.classify %v = %s, want invalid params", params, raw)
			}
		})
	}
}
