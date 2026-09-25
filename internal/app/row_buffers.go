package app

import (
	"math"
	"sync/atomic"

	"github.com/shady2k/nocx/internal/settings"
)

// rowBuffers is the composition root's hold on the two row-buffer settings
// (nocx-2v80t.3.36): the helper's, which a spawn sends as its
// RowBufferBytes, and the coordinator's, which the transport applies to the
// sessions it attaches from then on. Both are read at the moment a session
// is opened, so a changed value applies to the next session and never to a
// running one — the helper and the transport each keep the bound a session
// was opened with.
type rowBuffers struct {
	helper atomic.Int64
}

// helperBytes is what a spawn opened now asks the helper for; zero, the
// helper's own default, when nothing was read yet.
func (b *rowBuffers) helperBytes() int64 {
	if b == nil {
		return 0
	}
	return b.helper.Load()
}

// coordinatorBuffer is the transport's half, as this file needs it.
type coordinatorBuffer interface {
	SetBlockRowsBufferBytes(n int64)
}

// applyRowBuffers reads both settings into their owners.
func applyRowBuffers(reg *settings.Registry, b *rowBuffers, tp coordinatorBuffer) {
	if v, err := reg.GetNumber(settings.HistoryHelperBufferMB); err == nil {
		b.helper.Store(megabytes(v))
	}
	if v, err := reg.GetNumber(settings.HistoryCoordinatorBufferMB); err == nil {
		tp.SetBlockRowsBufferBytes(megabytes(v))
	}
}

// megabytes converts a setting in MB to bytes, keeping a fraction the person
// typed (nocx-2v80t.3.38): 4.5 MB is 4.5 MiB, never 4. Rounded to the byte.
func megabytes(v float64) int64 {
	return int64(math.Round(v * (1 << 20)))
}

// watchRowBuffers applies both now and again whenever either changes.
func watchRowBuffers(reg *settings.Registry, b *rowBuffers, tp coordinatorBuffer) {
	applyRowBuffers(reg, b, tp)
	reg.AddNotifier(func(_ int, keys []string) {
		for _, k := range keys {
			if k == settings.HistoryHelperBufferMB.Key() || k == settings.HistoryCoordinatorBufferMB.Key() {
				applyRowBuffers(reg, b, tp)
				return
			}
		}
	})
}
