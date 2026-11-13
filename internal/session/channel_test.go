package session

import (
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
)

// Compile-time assertion that the local transport type satisfies the unified
// Channel interface (AD-7). If this fails to compile, the interface contract
// has drifted and must be reconciled.
func TestPtySatisfiesChannel(t *testing.T) {
	var _ Channel = pty.NewStub(log.NewSlogAdapter(nil))
}

func TestChannelInterfaceShape(t *testing.T) {
	var c Channel = pty.NewStub(log.NewSlogAdapter(nil))
	_ = c.Done()
	_ = c.Close()
}
