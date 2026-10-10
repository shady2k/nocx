package sessionruntime

// The live history page's read (nocx-zg3k3.10.3): one page of the scrollback
// the emulator holds, as the emulator holds it.
//
// # The cursor, and what it must survive
//
// A client pages BACKWARDS while output keeps arriving, so the cursor it
// holds cannot be a position in the buffer — positions move. It is an
// ABSOLUTE history row number: this package has counted every row that left
// the top of the screen since the session began (screenDepartedRows), and a
// row keeps the number of its departure for its whole life. Against that
// numbering:
//
//   - output arriving appends at the head and moves nothing below it;
//   - a pane that grew pulled rows back onto the screen: they are OWED their
//     departure (ReportedRowsOnScreen) and are not in the history any more,
//     so the head the history is measured against is the count minus what is
//     owed, and the first page never names a row that is on the screen;
//   - the alternate screen holds no history: a page answers the empty
//     interval at the head and never the primary's rows, and the primary's
//     rows come back under the same numbers when it is restored;
//   - retention pruning and an erase-saved-lines (ED3) really remove rows:
//     the floor — the number below which nothing is retained — rises, and a
//     cursor below it is answered with the empty interval at that cursor and
//     the floor stated, never a short list a caller could read as a short
//     history. After ED3 the floor IS the head: the emulator's history is
//     what it holds, and no boundary is invented.
//
// # One instant
//
// The head, the debt, the total and the rows are taken under ONE acquisition
// of the runtime lock, exactly as [Session.ReadDepartedScreen] takes the
// departure count and the terminal: a page measured against a head its rows
// disagree with is the torn read nocx-zg3k3.5.10 already paid for once.
//
// The bound on one page is a page ON PURPOSE: one helper round trip and one
// carrier document, small by construction, with paging — not a bigger page —
// as how a caller reaches deeper history.

import (
	"fmt"

	"github.com/shady2k/nocx/internal/emulator"
)

// MaxHistoryPageRows bounds one page. Every layer that spells the request
// holds the same number: the wire contract's schema maximum, the transport's
// params validator, and this seam's own refusal — one bound, not three.
const MaxHistoryPageRows = 64

// HistoryPageView is one page answer: the interval of absolute history row
// numbers the rows were read at, the floor below which nothing is retained,
// whether rows remain below, and the rows themselves, oldest first, in the
// emulator's own copy. Start is always the exclusive lower bound of what was
// DELIVERED — an empty page's interval is empty at the cursor it was taken
// at — so a caller's next page is always before = Start, whatever the answer
// held.
type HistoryPageView struct {
	Floor uint64
	Start uint64
	End   uint64
	More  bool
	Rows  []emulator.Row
}

// ReadHistoryPage reads one page of the session's live history. before is
// the exclusive upper bound in the absolute history row numbering, or nil
// for the head — the newest retained rows. limit is clamped to
// [1, MaxHistoryPageRows] by REFUSAL, never by silence: a caller asking for
// more than a page is naming a shape the wire does not carry.
func (s *Session) ReadHistoryPage(before *uint64, limit int) (HistoryPageView, error) {
	if limit <= 0 || limit > MaxHistoryPageRows {
		return HistoryPageView{}, fmt.Errorf("sessionruntime: history page limit %d out of range 1..%d",
			limit, MaxHistoryPageRows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base, err := s.emulator.HistoryRows(0, 0)
	if err != nil {
		return HistoryPageView{}, err
	}
	owed, err := s.emulator.ReportedRowsOnScreen()
	if err != nil {
		return HistoryPageView{}, err
	}
	// The head: every row that ever departed, less the ones a growing pane
	// pulled back onto the screen. Every owed row was counted when it first
	// departed, so the subtraction cannot go round the counter.
	head := s.screenDepartedRows - uint64(owed) //nolint:gosec // a row count, not a byte count
	total := uint64(base.Total)                 //nolint:gosec // a row count, not a byte count
	// The floor: head less what the history holds. Only a capture that tore
	// can push the true floor below zero — rows departed that the report
	// never let this session count — and a torn report is a hole the stream
	// already states elsewhere; here the numbering clamps at the buffer's
	// beginning and stays consistent, because it never heals backwards.
	var floor uint64
	if head > total {
		floor = head - total
	}
	end := head
	if before != nil && *before < end {
		end = *before
	}
	start := end
	if end > floor {
		start = floor
		if room := end - floor; room > uint64(limit) { //nolint:gosec // a row count, not a byte count
			start = end - uint64(limit) //nolint:gosec // a row count, not a byte count
		}
	}
	var rows []emulator.Row
	if end > start {
		// [start, end) sits inside [floor, head) by construction, so the
		// history index asked for is start-floor and the count is the
		// interval's own: the emulator clamps nothing here, and what it
		// returns is the page whole.
		page, err := s.emulator.HistoryRows(int(start-floor), int(end-start)) //nolint:gosec // a row count, not a byte count
		if err != nil {
			return HistoryPageView{}, err
		}
		rows = page.Rows
	}
	return HistoryPageView{
		Floor: floor,
		Start: start,
		End:   start + uint64(len(rows)), //nolint:gosec // a row count, not a byte count
		More:  start > floor,
		Rows:  rows,
	}, nil
}
