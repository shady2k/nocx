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

	if err := reg.SetNumber(settings.HistoryHelperBufferMB, 6); err != nil {
		t.Fatalf("set the helper's buffer: %v", err)
	}
	if err := reg.SetNumber(settings.HistoryCoordinatorBufferMB, 7); err != nil {
		t.Fatalf("set the coordinator's buffer: %v", err)
	}
	if got := bufs.helperBytes(); got != 6<<20 {
		t.Fatalf("after the change a new spawn asks for %d bytes, want 6 MB", got)
	}
	if tp.bytes != 7<<20 {
		t.Fatalf("after the change the transport attaches with %d bytes, want 7 MB", tp.bytes)
	}
}

// A fraction of a MB is kept, not truncated (nocx-2v80t.3.38): 4.5 MB is
// 4.5 MiB of buffer, not 4. Paired with the whole-MB values above.
func TestAFractionalBufferSettingIsNotTruncated(t *testing.T) {
	reg := settings.New(&appFakeDoc{}, nil)
	bufs := &rowBuffers{}
	tp := &fakeCoordinatorBuffer{}
	watchRowBuffers(reg, bufs, tp)

	if err := reg.SetNumber(settings.HistoryHelperBufferMB, 4.5); err != nil {
		t.Fatalf("set the helper's buffer: %v", err)
	}
	if err := reg.SetNumber(settings.HistoryCoordinatorBufferMB, 6.25); err != nil {
		t.Fatalf("set the coordinator's buffer: %v", err)
	}
	if got, want := bufs.helperBytes(), int64(4.5*(1<<20)); got != want {
		t.Fatalf("4.5 MB became %d bytes, want %d", got, want)
	}
	if got, want := tp.bytes, int64(6.25*(1<<20)); got != want {
		t.Fatalf("6.25 MB became %d bytes, want %d", got, want)
	}
}
