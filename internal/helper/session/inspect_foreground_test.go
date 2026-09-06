package session

import "testing"

// The shell's own group in the foreground is a FACT, not an absence.
//
// Under ADR-0024 a command runs with job control off (`set +m`), so the
// foreground group IS the shell's own group for the whole time it runs. The
// observation used to omit the pgid in exactly that case, on the reading that
// "the shell is in front" means "nothing is running" — true at a prompt, and
// false for every command started that way. Omitting it left the coordinator
// unable to tell that state from "nobody could look", which is the difference
// between writing the terminal's interrupt and refusing the Stop outright
// (nocx-nekvj).
func TestObservationReportsTheShellsOwnForegroundGroup(t *testing.T) {
	insp := &osInspector{src: fakeSource{
		name: "procfs",
		live: map[int]bool{4242: true},
	}}
	obs := insp.Observe(4242, 4242)
	if obs == nil {
		t.Fatal("Observe returned nil for a live pid")
	}
	if obs.ForegroundPgid != 4242 {
		t.Fatalf("ForegroundPgid = %d, want the shell's own group 4242", obs.ForegroundPgid)
	}
	// And it is still not named as a JOB. A shell is not a command, and
	// naming it would put a lie in the diagnostic a person reads.
	if obs.ForegroundCommand != "" {
		t.Fatalf("ForegroundCommand = %q, want empty for the shell's own group", obs.ForegroundCommand)
	}
}

// A group that could not be read stays an absence, and is not reported as one
// that could.
func TestObservationOmitsAnUnreadableForegroundGroup(t *testing.T) {
	insp := &osInspector{src: fakeSource{
		name: "procfs",
		live: map[int]bool{4242: true},
	}}
	obs := insp.Observe(4242, 0)
	if obs == nil {
		t.Fatal("Observe returned nil for a live pid")
	}
	if obs.ForegroundPgid != 0 {
		t.Fatalf("ForegroundPgid = %d, want 0 when the group could not be read", obs.ForegroundPgid)
	}
}
