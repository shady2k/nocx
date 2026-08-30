package content

import "testing"

func TestResourceReportEffectKeepsWorstCaseForWrites(t *testing.T) {
	declared := EffectMutateDestructive
	for _, report := range []ResourceReport{
		{Resources: []Resource{{Path: "/etc/x", Verb: ResourceWrite}}},
		{Resources: []Resource{{Path: "/tmp/x", Verb: ResourceDelete}}},
		{Resources: []Resource{{Path: "example.com", Verb: ResourceNetwork}}},
		{Unresolved: []UnresolvedResource{{Path: "$BUILD", Verb: ResourceDelete, Reason: "contains a shell variable"}}},
	} {
		if got := report.Effect(declared); got != declared {
			t.Fatalf("report %+v lowered effect to %q, want declared %q", report, got, declared)
		}
	}
}

func TestResourceReportEffectLowersReadOnlyReport(t *testing.T) {
	report := ResourceReport{Resources: []Resource{
		{Path: "a", Verb: ResourceRead},
		{Path: "b", Verb: ResourceRead},
	}}
	if got := report.Effect(EffectMutateDestructive); got != EffectObserve {
		t.Fatalf("read-only report effect = %q, want %q", got, EffectObserve)
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
