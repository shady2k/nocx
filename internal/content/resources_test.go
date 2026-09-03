package content

import "testing"

var allEffectsForTest = []Effect{
	EffectObserve, EffectMutateReversible, EffectMutateDestructive,
	EffectPrivilegeChange, EffectDisclose, EffectCrossBoundary, EffectDelegate,
}

func TestResourceReportEffectMapsEachResolvedVerb(t *testing.T) {
	tests := []struct {
		name   string
		verb   ResourceVerb
		effect Effect
	}{
		{name: "read", verb: ResourceRead, effect: EffectObserve},
		{name: "write", verb: ResourceWrite, effect: EffectMutateReversible},
		{name: "delete", verb: ResourceDelete, effect: EffectMutateDestructive},
		{name: "network", verb: ResourceNetwork, effect: EffectCrossBoundary},
		{name: "execute", verb: ResourceExecute, effect: EffectDelegate},
		{name: "source", verb: ResourceSource, effect: EffectPrivilegeChange},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := ResourceReport{Resources: []Resource{{Path: "/tmp/x", Verb: tc.verb}}}
			if got := report.Effect(allEffectsForTest); got != tc.effect {
				t.Fatalf("verb %s lowered effect to %q, want %q", tc.verb, got, tc.effect)
			}
		})
	}
}

func TestResourceReportEffectKeepsDeclaredAboveCeiling(t *testing.T) {
	tests := []struct {
		name     string
		declared Effect
		verb     ResourceVerb
	}{
		{name: "execute cannot lower destructive", declared: EffectMutateDestructive, verb: ResourceExecute},
		{name: "network cannot lower destructive", declared: EffectMutateDestructive, verb: ResourceNetwork},
		{name: "write cannot raise observe", declared: EffectObserve, verb: ResourceWrite},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report := ResourceReport{Resources: []Resource{{Path: "/tmp/x", Verb: tc.verb}}}
			if got := report.Effect([]Effect{tc.declared}); got != tc.declared {
				t.Fatalf("verb %s changed declared %q to %q", tc.verb, tc.declared, got)
			}
		})
	}
}

// A mixed report is no longer in this list: since nocx-jxq97 it selects the
// worst of its DERIVED candidates, not the declared worst. The case is kept
// here in the form that is still true of it — a candidate the tool never
// declared cannot be selected, so a one-member declaration is unmovable.
func TestResourceReportEffectKeepsDeclaredForUnresolvedAndUnknown(t *testing.T) {
	declared := EffectDelegate
	for _, report := range []ResourceReport{
		{Resources: []Resource{{Path: "/tmp/x", Verb: ResourceUnknown}}},
		{Resources: []Resource{
			{Path: "/tmp/x", Verb: ResourceRead},
			{Path: "/tmp/y", Verb: ResourceWrite},
		}},
		{Unresolved: []UnresolvedResource{{Path: "$BUILD", Verb: ResourceDelete, Reason: "contains a shell variable"}}},
	} {
		if got := report.Effect([]Effect{declared}); got != declared {
			t.Fatalf("report %+v lowered effect to %q, want declared %q", report, got, declared)
		}
	}
}

func TestResourceReportEffectLowersReadOnlyReport(t *testing.T) {
	report := ResourceReport{Resources: []Resource{
		{Path: "a", Verb: ResourceRead},
		{Path: "b", Verb: ResourceRead},
	}}
	if got := report.Effect(allEffectsForTest); got != EffectObserve {
		t.Fatalf("read-only report effect = %q, want %q", got, EffectObserve)
	}
}

// CHANGED EXPECTATION (nocx-jxq97): the read+write report that used to be in
// this list now selects mutate-reversible — the worst of what it actually did —
// rather than delegate, the worst member of a declaration set it never
// exercised. It is asserted in its new form by
// TestResourceReportEffectMixedTakesWorstDerivedNotWorstDeclared. What remains
// here is the half that did not move: a report the parser could not fully
// account for still takes the declared worst, even when the set is plural.
func TestResourceReportEffectPluralUnknownTakesWorstMember(t *testing.T) {
	declared := []Effect{
		EffectObserve, EffectMutateReversible, EffectMutateDestructive,
		EffectCrossBoundary, EffectDelegate,
	}
	for _, report := range []ResourceReport{
		{Resources: []Resource{{Path: "/tmp/x", Verb: ResourceUnknown}}},
		{Unresolved: []UnresolvedResource{{Path: "$BUILD", Verb: ResourceDelete}}},
	} {
		if got := report.Effect(declared); got != EffectDelegate {
			t.Fatalf("report %+v selected %q, want worst declared member %q", report, got, EffectDelegate)
		}
	}
}

func TestWorstEffect_EmptySetIsUnclassified(t *testing.T) {
	if got := WorstEffect(nil); got != "" {
		t.Fatalf("WorstEffect(nil) = %q, want empty effect", got)
	}
}

func TestUnresolvedResourceRequiresHumanReason(t *testing.T) {
	report := ResourceReport{Unresolved: []UnresolvedResource{{
		Path: "$BUILD", Verb: ResourceDelete, Reason: "could not resolve $BUILD without executing the shell",
	}}}
	if report.Unresolved[0].Reason == "" {
		t.Fatal("unresolved resource has no human-readable reason")
	}
}

// runDeclaredForTest is session.run's declaration set (agenttools/registry.go).
// It is the set that made nocx-jxq97 visible: it contains delegate, which
// effectOrder ranks highest, so WorstEffect(declared) is delegate for every
// command that tool carries — whatever the command actually did.
var runDeclaredForTest = []Effect{
	EffectObserve, EffectMutateReversible, EffectMutateDestructive,
	EffectDelegate, EffectCrossBoundary,
}

func reportOfVerbs(verbs ...ResourceVerb) ResourceReport {
	report := ResourceReport{}
	for i, verb := range verbs {
		report.Resources = append(report.Resources, Resource{
			Path: string(rune('a'+i)) + "-target", Verb: verb,
		})
	}
	return report
}

// TestResourceReportEffectMixedTakesWorstDerivedNotWorstDeclared is the rule
// nocx-jxq97 installs: a report whose resolved resources derive more than one
// candidate lands on the worst of THOSE candidates, never on the worst member
// of the tool's declaration set. There is a case for every pair the parser can
// produce today, and each is asserted in both resource orders — the order the
// command spells its arguments in must not decide which row a person answers.
func TestResourceReportEffectMixedTakesWorstDerivedNotWorstDeclared(t *testing.T) {
	tests := []struct {
		name  string
		verbs []ResourceVerb
		want  Effect
	}{
		{"network and write", []ResourceVerb{ResourceNetwork, ResourceWrite}, EffectCrossBoundary},
		{"read and write", []ResourceVerb{ResourceRead, ResourceWrite}, EffectMutateReversible},
		{"read and network", []ResourceVerb{ResourceRead, ResourceNetwork}, EffectCrossBoundary},
		{"execute and read", []ResourceVerb{ResourceExecute, ResourceRead}, EffectDelegate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forward := reportOfVerbs(tc.verbs...)
			if got := forward.Effect(runDeclaredForTest); got != tc.want {
				t.Fatalf("%v selected %q, want the worst derived candidate %q", tc.verbs, got, tc.want)
			}
			reversed := reportOfVerbs(tc.verbs[1], tc.verbs[0])
			if got := reversed.Effect(runDeclaredForTest); got != tc.want {
				t.Fatalf("reversing the resources selected %q, want %q — resource order is not a decision", got, tc.want)
			}
		})
	}
}

// TestResourceReportEffectMixedNetworkAndWriteNoLongerLandsInDelegate names the
// behaviour change so a reviewer reads it rather than finds it. `curl -o /tmp/x
// https://y` reaches a host and writes a file; until nocx-jxq97 it classified as
// `delegate` — "hand work to another agent", which it never did — because
// session.run declares delegate and effectOrder ranks it highest.
//
// THIS IS A WEAKENING FOR ONE CONFIGURATION, deliberately. A person who set the
// delegate row to refuse and the cross-boundary row to permit was refused this
// command and is now permitted it. That is the price of the row being one the
// call belongs in: the old answer was strict only by accident of enum order —
// effectOrder is an evaluation lattice, not a risk ranking — and nobody can
// usefully answer a question about work handed to another agent when no work
// was handed to anyone.
func TestResourceReportEffectMixedNetworkAndWriteNoLongerLandsInDelegate(t *testing.T) {
	report := ResourceReport{Resources: []Resource{
		{Path: "/tmp/x", Verb: ResourceWrite},
		{Path: "https://y", Verb: ResourceNetwork},
	}}
	if got := report.Effect(runDeclaredForTest); got == EffectDelegate {
		t.Fatal("a curl that writes a file still lands in the delegate row")
	}
	if got := report.Effect(runDeclaredForTest); got != EffectCrossBoundary {
		t.Fatalf("effect = %q, want %q — the worst of what it actually did", got, EffectCrossBoundary)
	}
}

// TestResourceReportEffectSingleCandidateIsUnchanged pins the half that did not
// move: one derived candidate, however many resources carry it, selects exactly
// what it selected before nocx-jxq97.
func TestResourceReportEffectSingleCandidateIsUnchanged(t *testing.T) {
	tests := []struct {
		verb ResourceVerb
		want Effect
	}{
		{ResourceRead, EffectObserve},
		{ResourceWrite, EffectMutateReversible},
		{ResourceDelete, EffectMutateDestructive},
		{ResourceNetwork, EffectCrossBoundary},
		{ResourceExecute, EffectDelegate},
		{ResourceSource, EffectPrivilegeChange},
	}
	for _, tc := range tests {
		t.Run(string(tc.verb), func(t *testing.T) {
			for _, report := range []ResourceReport{
				reportOfVerbs(tc.verb),
				reportOfVerbs(tc.verb, tc.verb),
				reportOfVerbs(tc.verb, tc.verb, tc.verb),
			} {
				if got := report.Effect(allEffectsForTest); got != tc.want {
					t.Fatalf("%d resources of verb %s selected %q, want %q",
						len(report.Resources), tc.verb, got, tc.want)
				}
			}
		})
	}
}

// TestResourceReportEffectUnresolvedOrUnknownStillTakesWorstDeclared is the half
// nocx-jxq97 must NOT weaken: a report the parser could not fully account for
// has no honest candidate set, so uncertainty still tightens to the declaration
// set's worst member — and the selection carries no candidates, because a
// partial list read as a complete one is exactly the dishonesty this change
// exists to remove.
func TestResourceReportEffectUnresolvedOrUnknownStillTakesWorstDeclared(t *testing.T) {
	tests := []struct {
		name   string
		report ResourceReport
	}{
		{"an unknown verb", reportOfVerbs(ResourceUnknown)},
		{"an unknown verb beside resolved ones", reportOfVerbs(ResourceNetwork, ResourceWrite, ResourceUnknown)},
		{"an unresolved part", ResourceReport{Unresolved: []UnresolvedResource{
			{Path: "$BUILD", Verb: ResourceDelete, Reason: "contains a shell variable"},
		}}},
		{"an unresolved part beside resolved ones", ResourceReport{
			Resources: []Resource{
				{Path: "https://y", Verb: ResourceNetwork},
				{Path: "/tmp/x", Verb: ResourceWrite},
			},
			Unresolved: []UnresolvedResource{
				{Path: "$OUT", Verb: ResourceWrite, Reason: "contains a shell variable"},
			},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selection := tc.report.SelectEffect(runDeclaredForTest)
			if want := WorstEffect(runDeclaredForTest); selection.Effect != want {
				t.Fatalf("effect = %q, want the declared set's worst member %q", selection.Effect, want)
			}
			if len(selection.Candidates) != 0 {
				t.Fatalf("candidates = %v, want none — the report is not a complete account", selection.Candidates)
			}
		})
	}
}

// TestResourceReportEffectIsNeverWeakerThanItsStrictestDerivedCandidate is the
// invariant stated over every pair of mapped verbs, with the whole enum
// declared so the declaration ceiling never intervenes.
func TestResourceReportEffectIsNeverWeakerThanItsStrictestDerivedCandidate(t *testing.T) {
	verbs := []ResourceVerb{
		ResourceRead, ResourceWrite, ResourceDelete,
		ResourceNetwork, ResourceExecute, ResourceSource,
	}
	for _, first := range verbs {
		for _, second := range verbs {
			report := reportOfVerbs(first, second)
			selection := report.SelectEffect(allEffectsForTest)
			for _, candidate := range selection.Candidates {
				if effectOrder(selection.Effect) < effectOrder(candidate) {
					t.Fatalf("%s+%s selected %q, which is below its derived candidate %q",
						first, second, selection.Effect, candidate)
				}
			}
		}
	}
}

// TestResourceReportEffectMixedKeepsTheDeclarationAsCeiling: a derived candidate
// the tool never declared cannot be selected. The declaration set remains the
// ceiling it has always been, and an unrepresented class falls back to the
// declared worst rather than to a permissive result.
func TestResourceReportEffectMixedKeepsTheDeclarationAsCeiling(t *testing.T) {
	report := reportOfVerbs(ResourceNetwork, ResourceWrite)
	declared := []Effect{EffectObserve, EffectMutateReversible}
	if got := report.Effect(declared); got != EffectMutateReversible {
		t.Fatalf("effect = %q, want %q — cross-boundary is not declared, so the declared worst stands",
			got, EffectMutateReversible)
	}
}

// TestResourceReportSelectEffectCarriesEveryDerivedCandidate is the second half
// of nocx-jxq97: one row governs (ADR-0020 §7), and the person is still entitled
// to see that `curl -o /tmp/x https://y` both reached a host and wrote a file.
func TestResourceReportSelectEffectCarriesEveryDerivedCandidate(t *testing.T) {
	report := ResourceReport{Resources: []Resource{
		{Path: "/tmp/x", Verb: ResourceWrite},
		{Path: "https://y", Verb: ResourceNetwork},
	}}
	selection := report.SelectEffect(runDeclaredForTest)
	if selection.Effect != EffectCrossBoundary {
		t.Fatalf("effect = %q, want %q", selection.Effect, EffectCrossBoundary)
	}
	for _, want := range []Effect{EffectCrossBoundary, EffectMutateReversible} {
		if !containsEffect(selection.Candidates, want) {
			t.Fatalf("candidates = %v, want %q among them", selection.Candidates, want)
		}
	}
	if len(selection.Candidates) != 2 {
		t.Fatalf("candidates = %v, want exactly the two the command derived", selection.Candidates)
	}
}

// TestResourceReportSelectEffectDeduplicatesCandidates: three reads are one
// candidate, so a surface says "reads files" once.
func TestResourceReportSelectEffectDeduplicatesCandidates(t *testing.T) {
	selection := reportOfVerbs(ResourceRead, ResourceRead, ResourceRead).SelectEffect(allEffectsForTest)
	if len(selection.Candidates) != 1 || selection.Candidates[0] != EffectObserve {
		t.Fatalf("candidates = %v, want exactly [%q]", selection.Candidates, EffectObserve)
	}
}

// TestResourceReportEffectAgreesWithSelectEffect: Effect is the row half of the
// same computation, never a second one that can drift from it.
func TestResourceReportEffectAgreesWithSelectEffect(t *testing.T) {
	for _, report := range []ResourceReport{
		{},
		reportOfVerbs(ResourceRead),
		reportOfVerbs(ResourceNetwork, ResourceWrite),
		reportOfVerbs(ResourceUnknown),
		{Unresolved: []UnresolvedResource{{Path: "$X", Verb: ResourceRead, Reason: "a shell variable"}}},
	} {
		for _, declared := range [][]Effect{nil, allEffectsForTest, runDeclaredForTest, {EffectObserve}} {
			if got, want := report.Effect(declared), report.SelectEffect(declared).Effect; got != want {
				t.Fatalf("Effect = %q but SelectEffect = %q for %+v under %v", got, want, report, declared)
			}
		}
	}
}
