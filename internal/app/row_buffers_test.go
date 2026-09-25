package app

import (
	"testing"

	"github.com/shady2k/nocx/internal/settings"
)

type fakeCoordinatorBuffer struct{ bytes int64 }

func (f *fakeCoordinatorBuffer) SetBlockRowsBufferBytes(n int64) { f.bytes = n }

// The two buffers are the person's settings (nocx-2v80t.3.36): what a
// session opened now gets is what the settings say now, and a change reaches
// the next session — the helper's through the spawn, the coordinator's
// through the transport's attach.
func TestTheRowBuffersFollowTheSettings(t *testing.T) {
	reg := settings.New(&appFakeDoc{}, nil)
	bufs := &rowBuffers{}
	tp := &fakeCoordinatorBuffer{}
	watchRowBuffers(reg, bufs, tp)

	// Paired: nothing configured is the declared default.
	if got := bufs.helperBytes(); got != 20<<20 {
		t.Fatalf("the helper's buffer is %d bytes by default, want 20 MB", got)
	}
	if tp.bytes != 20<<20 {
		t.Fatalf("the coordinator's buffer is %d bytes by default, want 20 MB", tp.bytes)
	}

	if err := reg.SetNumber(settings.HistoryHelperBufferMB, 3); err != nil {
		t.Fatalf("set the helper's buffer: %v", err)
	}
	if err := reg.SetNumber(settings.HistoryCoordinatorBufferMB, 5); err != nil {
		t.Fatalf("set the coordinator's buffer: %v", err)
	}
	if got := bufs.helperBytes(); got != 3<<20 {
		t.Fatalf("after the change a new spawn asks for %d bytes, want 3 MB", got)
	}
	if tp.bytes != 5<<20 {
		t.Fatalf("after the change the transport attaches with %d bytes, want 5 MB", tp.bytes)
	}
}
