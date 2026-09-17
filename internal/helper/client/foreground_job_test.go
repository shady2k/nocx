package client

import (
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/pty"
)

// classifyForegroundObservation is the whole of what ForegroundJob decides
// once it has the helper's answer, and it is tested here rather than through a
// live helper because what broke was the DECISION, not the transport
// (nocx-nekvj).
func TestForegroundJobClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		obs       *Observation
		launchPID int
		wantPgid  int
		wantErr   error
	}{
		{
			// The state ADR-0024's `set +m` produces for the whole time a
			// command runs: the shell and its command share one group. It is
			// PROTECTED, not absent — the ladder must write the terminal's
			// interrupt rather than signal a group that contains the shell.
			name:      "the shell's own group is protected",
			obs:       &Observation{ForegroundPgid: 4242},
			launchPID: 4242,
			wantErr:   pty.ErrProtectedForeground,
		},
		{
			name:      "a job of its own is named and returned",
			obs:       &Observation{ForegroundPgid: 4311},
			launchPID: 4242,
			wantPgid:  4311,
		},
		{
			// Nobody could look. It is NOT ErrNoForeground: a caller that read
			// it as "nothing is running" would report a quiet pane about a
			// command that is plainly there.
			name:      "an unreadable group is a diagnosis, not an answer",
			obs:       &Observation{},
			launchPID: 4242,
			wantErr:   ErrNoForegroundJob,
		},
		{
			name:      "no observation at all is the same diagnosis",
			obs:       nil,
			launchPID: 4242,
			wantErr:   ErrNoForegroundJob,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pgid, err := classifyForegroundObservation(tc.obs, tc.launchPID)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if pgid != tc.wantPgid {
				t.Fatalf("pgid = %d, want %d", pgid, tc.wantPgid)
			}
		})
	}
}

// ErrProtectedForeground WRAPS ErrNoForeground, and the transport's classifier
// tests the specific one first. This pins the order-dependence so a refactor
// that folded the two would fail here rather than in a 90-minute suite.
func TestProtectedForegroundIsAlsoNoForeground(t *testing.T) {
	_, err := classifyForegroundObservation(&Observation{ForegroundPgid: 7}, 7)
	if !errors.Is(err, pty.ErrProtectedForeground) {
		t.Fatalf("err = %v, want ErrProtectedForeground", err)
	}
	if !errors.Is(err, pty.ErrNoForeground) {
		t.Fatalf("err = %v, want it to wrap ErrNoForeground", err)
	}
}
