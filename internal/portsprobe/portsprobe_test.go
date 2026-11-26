package portsprobe

import (
	"testing"
)

// TestSampleState pins what a valid sample's rows MEAN as an outcome. It is
// the projection both the ladder and the local kernel read go through, so a
// change here moves the remote panel and the local panel together.
func TestSampleState(t *testing.T) {
	known := Listener{Port: 1, Process: Process{Evidence: EvidenceKnown}}
	denied := Listener{Port: 2, Process: Process{Evidence: EvidencePermissionDenied}}
	unsupported := Listener{Port: 3, Process: Process{Evidence: EvidenceUnsupported}}

	if got := SampleState(nil); got != StateAvailable {
		t.Errorf("empty sample state = %v, want available (no listeners observed)", got)
	}
	if got := SampleState([]Listener{known, denied}); got != StateAvailable {
		t.Errorf("mixed evidence state = %v, want available", got)
	}
	if got := SampleState([]Listener{denied, denied}); got != StateAvailableLimited {
		t.Errorf("all-denied state = %v, want available-limited", got)
	}
	if got := SampleState([]Listener{unsupported}); got != StateAvailableLimited {
		t.Errorf("unsupported evidence state = %v, want available-limited", got)
	}
}
