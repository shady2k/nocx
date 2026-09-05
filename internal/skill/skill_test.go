package skill

import (
	"reflect"
	"testing"
)

// THE VERDICT GATES NOTHING, and this is what says so. Offered() is the only
// filter on what the assistant is given (write.go:189), and a verdict entering
// it would mean a hostile file that talked the auditor into "clear" could
// switch itself on.
//
// The four cases are the whole truth table of the two stored facts, not a
// sample of it: a predicate that agreed with `enabled && status != changed`
// on three of four and diverged on the fourth would still pass a test that
// only tried "a clear one and a suspect one" — which is exactly the shape a
// verdict sneaking into a THIRD term would take, one combination at a time.
// The struct-field check is the other half: even a predicate that computes
// the right bool today has nothing to protect it from tomorrow if the type
// it reads from grows a field named after a verdict.
func TestOfferedIsEnabledAndStatusAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		status  Status
		want    bool
	}{
		{"enabled, approved", true, StatusApproved, true},
		{"enabled, changed", true, StatusChanged, false},
		{"disabled, approved", false, StatusApproved, false},
		{"disabled, changed", false, StatusChanged, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Skill{Enabled: tc.enabled, Status: tc.status}
			if got := s.Offered(); got != tc.want {
				t.Fatalf("Offered() = %v, want %v for enabled=%v status=%v", got, tc.want, tc.enabled, tc.status)
			}
		})
	}

	// No field for a verdict to occupy. This is the guard against the
	// failure mode itself, not against today's implementation of it: the
	// four cases above prove the CURRENT predicate has two terms; this
	// proves the TYPE has nowhere to grow a third without the change being
	// visible right here.
	fields := reflect.VisibleFields(reflect.TypeOf(Skill{}))
	for _, f := range fields {
		switch f.Name {
		case "Name", "Description", "Provenance", "BaseDir", "Enabled", "Status":
			continue
		default:
			t.Fatalf("Skill grew an unexpected field %q — Offered() must stay enabled-plus-status, and a new field beside it is exactly how a verdict would sneak in", f.Name)
		}
	}
}
