package sessionruntime

import (
	"fmt"

	"github.com/shady2k/nocx/internal/emulator"
)

// MaxScreenCaptureRows bounds one coherent live-screen capture. A caller that
// needs a larger span must request it in smaller ranges; this method never
// silently shortens an absolute row interval.
const MaxScreenCaptureRows = 10000

// MaxScreenRowOffset is the largest absolute row address that can cross JSON
// as an exact JavaScript number. The capture refuses larger counters rather
// than letting a renderer or assistant lose row identity to float rounding.
const MaxScreenRowOffset uint64 = 1<<53 - 1

// ScreenRange is an absolute, half-open row interval in the active buffer.
type ScreenRange struct {
	Start uint64
	End   uint64
}

// ScreenCapture is one backend-owned reading of the active buffer. The rows,
// absolute range, retained floor, buffer identity, revision, cursor and stream
// completeness all describe the same instant under Session.mu.
type ScreenCapture struct {
	Floor        uint64
	Start        uint64
	End          uint64
	Total        uint64
	Rows         []emulator.Row
	Cursor       emulator.Cursor
	CursorRow    uint64
	Identity     ScreenIdentity
	Generation   uint64
	Revision     Revision
	Completeness Completeness
}

// ReadScreenCapture reads a complete absolute range from the runtime's active
// buffer. A nil range means every row still retained, from Floor through Total.
// An explicit range outside that retained interval is refused rather than
// clipped, because clipping would make the returned rows appear to occupy the
// rows the caller actually requested.
func (s *Session) ReadScreenCapture(region *ScreenRange) (ScreenCapture, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	term := s.emulator
	geom, err := term.Geometry()
	if err != nil {
		return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture geometry: %w", err)
	}
	if !geom.Valid() {
		return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture geometry %dx%d is invalid", geom.Cols, geom.Rows)
	}
	geometryRows, err := screenIntToUint64(geom.Rows, "screen rows")
	if err != nil {
		return ScreenCapture{}, err
	}
	active, err := term.Screen()
	if err != nil {
		return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture buffer: %w", err)
	}
	alt := active == emulator.ScreenAlternate
	s.observeBufferLocked(alt)

	cursor, err := term.Cursor()
	if err != nil {
		return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture cursor: %w", err)
	}
	cursorOffset, err := screenIntToUint64(cursor.Y, "cursor row")
	if err != nil || cursor.Y >= geom.Rows {
		return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture cursor row %d is outside geometry", cursor.Y)
	}
	visible := make([]emulator.Row, geom.Rows)
	for y := range geom.Rows {
		visible[y], err = term.Row(y)
		if err != nil {
			return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture row %d: %w", y, err)
		}
	}

	var history emulator.HistoryPage
	var head uint64
	if !alt {
		history, err = term.HistoryRows(0, 0)
		if err != nil {
			return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture history size: %w", err)
		}
		owed, owedErr := term.ReportedRowsOnScreen()
		if owedErr != nil {
			return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture history boundary: %w", owedErr)
		}
		owedRows, owedConversionErr := screenIntToUint64(owed, "reported screen rows")
		if owedConversionErr != nil {
			return ScreenCapture{}, owedConversionErr
		}
		if owedRows > s.screenDepartedRows {
			return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture history boundary exceeds departed rows")
		}
		head = s.screenDepartedRows - owedRows
	}
	if head > MaxScreenRowOffset || geometryRows > MaxScreenRowOffset-head {
		return ScreenCapture{}, fmt.Errorf("sessionruntime: absolute screen rows exceed JSON safe-integer ceiling %d", MaxScreenRowOffset)
	}
	total := head + geometryRows
	historyRows, err := screenIntToUint64(history.Total, "history row count")
	if err != nil {
		return ScreenCapture{}, err
	}
	floor := uint64(0)
	if head > historyRows {
		floor = head - historyRows
	}
	start, end := floor, total
	if region != nil {
		start, end = region.Start, region.End
	}
	if start > end || start < floor || end > total {
		return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture range [%d,%d) is outside retained rows [%d,%d)", start, end, floor, total)
	}
	if end-start > MaxScreenCaptureRows {
		return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture range has %d rows; maximum is %d", end-start, MaxScreenCaptureRows)
	}

	rows := make([]emulator.Row, 0, end-start)
	historyEnd := end
	if historyEnd > head {
		historyEnd = head
	}
	if start < historyEnd {
		historyStart, conversionErr := screenUint64ToInt(start-floor, "history start")
		if conversionErr != nil {
			return ScreenCapture{}, conversionErr
		}
		historyCount, conversionErr := screenUint64ToInt(historyEnd-start, "history range length")
		if conversionErr != nil {
			return ScreenCapture{}, conversionErr
		}
		page, pageErr := term.HistoryRows(historyStart, historyCount)
		if pageErr != nil {
			return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture history rows: %w", pageErr)
		}
		if len(page.Rows) != historyCount {
			return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture history changed within one read")
		}
		rows = append(rows, page.Rows...)
	}
	visibleStart := start
	if visibleStart < head {
		visibleStart = head
	}
	visibleEnd := end
	if visibleEnd > total {
		visibleEnd = total
	}
	for absolute := visibleStart; absolute < visibleEnd; absolute++ {
		visibleIndex, conversionErr := screenUint64ToInt(absolute-head, "visible row index")
		if conversionErr != nil || visibleIndex >= len(visible) {
			return ScreenCapture{}, fmt.Errorf("sessionruntime: screen capture visible row %d is outside geometry", absolute)
		}
		rows = append(rows, visible[visibleIndex])
	}

	return ScreenCapture{
		Floor: floor, Start: start, End: end, Total: total, Rows: rows,
		Cursor: cursor, CursorRow: head + cursorOffset,
		Identity: ScreenIdentity{
			At: s.inc, AltScreen: alt, BufferInstance: s.bufferInstance,
			Cols: geom.Cols, Rows: geom.Rows,
		},
		Generation: uint64(s.rev), Revision: s.rev, Completeness: s.completeness,
	}, nil
}

func screenIntToUint64(value int, name string) (uint64, error) {
	if value < 0 {
		return 0, fmt.Errorf("sessionruntime: %s is negative: %d", name, value)
	}
	return uint64(value), nil
}

func screenUint64ToInt(value uint64, name string) (int, error) {
	if value > uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("sessionruntime: %s %d exceeds platform int", name, value)
	}
	return int(value), nil
}
