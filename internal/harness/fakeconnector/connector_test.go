package fakeconnector_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeconnector"
	"github.com/shady2k/nocx/internal/harness/harnesstest"
)

// TestConformance is the whole of this connector's acceptance: it passes the
// same suite every connector runs, on the fake agent.
func TestConformance(t *testing.T) {
	harnesstest.Run(t, func(t *testing.T, setup harnesstest.Setup) harness.Harness {
		return fakeconnector.New(fakeconnector.Config{
			AgentPath: setup.AgentPath,
			Script:    setup.Script,
		})
	})
}
