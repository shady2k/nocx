package ghostty

import (
	"github.com/shady2k/nocx/internal/emulator"
)

// The engineering bounds a session's scrollback is kept under, and the one
// measured constant they are computed from. None of them is a setting: the
// person's number is terminal.scrollbackLines (internal/settings), and these
// stand beside it the way History's diskCeilingMiB stands beside its
// retentionMiB — the bound nobody reasons about, doing its work in code.
//
// scrollbackRowBytesPerColumn is MEASURED against the linked library, not
// guessed (the line maximum's own comment in internal/settings says nobody
// had measured a line's cost; these probes were run 2026-10-01 at 80×24
// against the pinned archive): a 4 MiB byte budget retained 5203 plain rows
// and an 8 MiB one retained 10983, so a plain row costs ~780 bytes of cell
// storage at 80 columns — 9.75 bytes per column. The cost is per CELL
// storage and scales with the pane's width, which is what
// scrollbackPageRowsAt below is derived from. For scale: a heavily styled
// row (truecolour set per cell) measured 5-7 KB — the same 4 MiB budget
// retained only 555-1132 of them, page-granular pruning oscillating around
// the boundary — and the default's 10,000 plain rows need about 8 MB.
const scrollbackRowBytesPerColumn = 9.75

// scrollbackMaxBytes is the internal memory ceiling, 32 MiB per session.
//
// It is four times the default's plain cost (about 8 MB at the measurement
// width), so the default keeps its promise on panes several times wider than
// the measurement, while output whose per-row cost is far above plain — the
// heavily styled rows the setting's description names — reaches the ceiling
// first at any width: the same 4 MiB budget retained only 555-1132 of those
// rows where it retained 5203 plain ones. It is also the per-session guard
// rail: at the declared line maximum of 100,000 a pane would spend ~78 MB on
// plain rows alone, and a few such panes would crowd this process past the
// aggregate budget its row buffers already draw on (AD-10). Raising it is a
// separate change behind that same measurement, not a guess made here.
const scrollbackMaxBytes uint64 = 32 << 20

// scrollbackPage is the library's pruning granularity, one page of cell
// storage, the size its own documentation quotes and the probes are
// consistent with: pruning lands the retained count on a page boundary, so
// the granularity decides how far from the ask the retained count can sit.
const scrollbackPage uint64 = 400 << 10

// scrollbackPageRowsAt is the rows one page holds at a pane's width — how
// coarse the library's pruning is THERE, and so how far from the ask the
// retained count may sit there. It shrinks as the pane widens (measured
// shortfall under an un-topped limit of 10000: 335 rows at 80 columns, 221
// at 148, 120 at 200), which is why the compensation is computed per
// geometry and never hard-coded.
func scrollbackPageRowsAt(cols int) uint64 {
	if cols <= 0 {
		return 0
	}
	rows := uint64(float64(scrollbackPage) / (float64(cols) * scrollbackRowBytesPerColumn))
	if rows == 0 {
		return 1
	}
	return rows
}

// ApplyScrollback applies the session's retention budget — the library's two
// limits together, [scrollbackMaxBytes] always and the person's line count
// widened by one page of rows at THIS pane's width — and re-baselines the
// departure measurement across the application.
//
// The page widening is what makes the person's number the count they get to
// scroll through. The library prunes to a page boundary at or BELOW the
// limit it is given, so an ask applied verbatim retains up to one page FEWER
// lines than asked (measured, at limit 10000: 9665 retained at 80 columns).
// Applied one page wide, what is retained lands above the ask and within
// one page of it at this geometry — the behaviour the setting's description
// states ("usually keeps somewhat more than this"). Zero is not widened: it
// selects the library's bounded capture floor; the helper clamps the live
// history page to the person's zero setting.
//
// The re-baseline is the load-bearing half. Rows the prune takes were
// already reported when they left the screen; they ceased in the library's
// storage, they did not leave the screen a second time. Without the fresh
// baseline the next feed reads the depth's fall as retention pruning INSIDE
// that feed and flags an interval incomplete for a loss that never happened
// (noteDepartedLocked's prune branch). The treatment is the one a resize
// already gets (rebaselineLocked), borrowed with the geometry held STILL:
// the active buffer is measured fresh, the hidden one starts from a fresh
// measurement at its next read — pruning took pages of its history too,
// and it cannot be measured from here — and the refill debt both carry
// survives, because un-reporting a reported row is the defect this port
// exists to prevent. No resize is FABRICATED by the borrow: rebaseline
// Locked stamps the hidden buffer with the rows it is handed, and at
// t.geom.Rows — unchanged, for nothing but history moved — the hidden
// buffer's pushed/refill arithmetic in noteDepartedLocked is vacuous
// (zero pushed, zero refill), exactly the truth for a mutation that
// touched history and never the screen.
//
// A zero budget applies the library's bounded capture floor; a positive one
// lower than the current depth prunes it at once — the library answers the
// set eagerly (measured: depth 4977 fell to 931 at the set, before any
// feed). An error leaves the previous budget in force.
func (t *terminal) ApplyScrollback(maxLines uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.t == nil {
		return emulator.ErrClosed
	}
	lines := maxLines
	bytes := scrollbackMaxBytes
	// At zero, keep that nonzero byte ceiling and leave lines at zero so rows
	// continue through scrollback pages and the history-erased callback. The
	// library interprets lines=0 as its bounded capture floor (243 rows at
	// 80 columns in the Linux-linked archive), not as no history. The helper
	// enforces the person's zero setting at the live history surface.
	if lines > 0 {
		lines += scrollbackPageRowsAt(t.geom.Cols)
	}
	if err := t.setScrollbackBudget(&bytes, &lines); err != nil {
		return err
	}
	t.rebaselineLocked(t.geom.Rows)
	return nil
}
