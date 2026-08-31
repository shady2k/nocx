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

func TestResourceReportEffectKeepsDeclaredForUnresolvedUnknownAndMixed(t *testing.T) {
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

func TestResourceReportEffectPluralUnknownTakesWorstMember(t *testing.T) {
	declared := []Effect{
		EffectObserve, EffectMutateReversible, EffectMutateDestructive,
		EffectCrossBoundary, EffectDelegate,
	}
	for _, report := range []ResourceReport{
		{Resources: []Resource{{Path: "/tmp/x", Verb: ResourceUnknown}}},
		{Resources: []Resource{
			{Path: "/tmp/x", Verb: ResourceRead},
			{Path: "/tmp/y", Verb: ResourceWrite},
		}},
		{Unresolved: []UnresolvedResource{{Path: "$BUILD", Verb: ResourceDelete}}},
	} {
		if got := report.Effect(declared); got != EffectDelegate {
			t.Fatalf("report %+v selected %q, want worst declared member %q", report, got, EffectDelegate)
		}
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
