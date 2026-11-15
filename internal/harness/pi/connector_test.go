package pi

import "github.com/shady2k/nocx/internal/harness"

// newTestConnector uses the production constructor without a tool endpoint.
func newTestConnector(dataDir string, env []string) *Connector {
	return NewWithBase(dataDir, env, harness.Base{})
}
