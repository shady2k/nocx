package sandbox

import (
	"context"
	"errors"
	"testing"
)

func TestSeatbeltProbe_ReasonMapping(t *testing.T) {
	origPath := sandboxExecPath
	origProbe := sandboxExecProbe
	defer func() {
		sandboxExecPath = origPath
		sandboxExecProbe = origProbe
	}()

	t.Run("binary missing", func(t *testing.T) {
		sandboxExecPath = "/nonexistent/sandbox-exec"
		var p seatbeltProbe
		st := p.status(context.Background())
		if st.Reason != ReasonSandboxExecUnavailable {
			t.Errorf("reason = %q, want %q", st.Reason, ReasonSandboxExecUnavailable)
		}
		if st.Available {
			t.Error("must not be available")
		}
	})

	t.Run("probe failure is not cached", func(t *testing.T) {
		sandboxExecPath = "/usr/bin/true" // exists, so the probe seam is reached
		calls := 0
		sandboxExecProbe = func(_ context.Context, _ string) error {
			calls++
			return errors.New("profile rejected")
		}
		var p seatbeltProbe
		if st := p.status(context.Background()); st.Reason != ReasonProbeFailed {
			t.Errorf("reason = %q, want %q", st.Reason, ReasonProbeFailed)
		}
		if st := p.status(context.Background()); st.Reason != ReasonProbeFailed {
			t.Errorf("second probe: reason = %q, want %q", st.Reason, ReasonProbeFailed)
		}
		if calls != 2 {
			t.Errorf("probe calls = %d, want 2 (failures must be re-probed)", calls)
		}
	})

	t.Run("success cached for app lifetime", func(t *testing.T) {
		sandboxExecPath = "/usr/bin/true"
		calls := 0
		sandboxExecProbe = func(_ context.Context, _ string) error {
			calls++
			return nil
		}
		var p seatbeltProbe
		for i := 0; i < 3; i++ {
			st := p.status(context.Background())
			if !st.Available || st.Backend != BackendSeatbelt {
				t.Fatalf("call %d: %+v, want available seatbelt", i, st)
			}
		}
		if calls != 1 {
			t.Errorf("probe calls = %d, want 1 (success must be cached)", calls)
		}
	})
}
