//go:build !nocx_dev_bind

package transport

import (
	"testing"

	"github.com/shady2k/nocx/internal/session"
)

// The shipped build reads neither name, and this is the assertion that says so
// — the sibling of TestDevBindPortIsIgnoredWithoutTheTag, for the stand's
// session-scoped startup bound (nocx-evj4b.1.6, review round 3). An
// environment must not be able to lengthen a session's `starting` interval in a
// binary a person runs.
func TestStartupBoundIgnoresTheStandsNumberWithoutTheTag(t *testing.T) {
	t.Setenv(devStartupBoundEnv, "120s")
	t.Setenv(devStartupBoundUserEnv, "nocx-e2e-long-bound")

	sid := session.ID("sid-shipped")
	devNoteStartupBound(sid, "nocx-e2e-long-bound")
	if bound, ok := devTakeStartupBound(sid); ok {
		t.Fatalf("the shipped build took the stand's startup bound (%v)", bound)
	}
}
