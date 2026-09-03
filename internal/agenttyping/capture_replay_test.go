package agenttyping_test

// The corpus has ONE owner, and it is internal/agentdriver: those captures are
// real byte streams off a real PTY at 120x40, and the rule under test was
// written from them. This replays them where they live rather than copying
// them here — a second copy of a corpus is a second corpus, and the day one is
// re-taken the other goes on proving something about a screen that no longer
// exists.
//
// The replay itself is internal/agentcapture's, which owns the format and
// replays it through a panegrid Store fed from byte zero. That is the same
// path the product's frames come out of, so the frames these tests classify
// are frames the product makes.

import (
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

func replay(t *testing.T, name string, atMs int64) panegrid.Frame {
	t.Helper()
	path := filepath.Join("..", "agentdriver", "testdata", "captures", name+".jsonl")
	header, chunks, err := agentcapture.Read(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	moments, err := agentcapture.Frames(log.NewSlogAdapter(nil), header, chunks, []int64{atMs})
	if err != nil {
		t.Fatalf("replay %s to %dms: %v", path, atMs, err)
	}
	return moments[0].Frame
}
