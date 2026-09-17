// Package replaylocal replays a capture through an emulator in THIS process.
//
// # Who this is for, and who it is not for
//
// Two callers, and neither is the coordinator. cmd/agent-capture is a tool a
// person points at a capture file, and tests need the screens a rule is
// asserted against. The PRODUCT's replay is the helper's (proto.OpReplay):
// cmd/nocx-server is built CGO_ENABLED=0, so an emulator there is not a
// choice it may make, and a second emulator beside the runtime is what
// ADR-0066 refuses.
//
// The emulator is nevertheless the SAME one — the adapter in
// internal/emulator/ghostty, through paneview.Replay — so a frame produced
// here is the frame the product produces. That is the whole reason this
// package exists rather than a second implementation being written beside it:
// a rule verified against another emulator's screen is verified against a
// screen nobody has.
package replaylocal

import (
	"context"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
	"github.com/shady2k/nocx/internal/paneview"
)

// Replayer is agentcapture.Replay over an emulator in this process.
type Replayer struct{}

var _ agentcapture.Replay = Replayer{}

// Replay feeds the capture's bytes to a fresh terminal and answers one frame
// per mark.
func (Replayer) Replay(_ context.Context, header agentcapture.Header, chunks []agentcapture.Chunk, through []int) ([]paneview.Frame, error) {
	data := make([][]byte, len(chunks))
	for i, c := range chunks {
		data[i] = []byte(c.Data)
	}
	return paneview.Replay(ghostty.New, emulator.Geometry{Cols: header.Cols, Rows: header.Rows}, data, through)
}
