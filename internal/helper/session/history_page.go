package session

// The live-history read, helper side (nocx-zg3k3.10.3): the op that reaches
// the runtime's own page read. It sits beside the screen reads (screen.go)
// for the reason those state: the emulator is this process's, and a page of
// its scrollback is a QUESTION to the runtime beside the PTY, never
// something the coordinator could derive.
//
// The read itself is sessionruntime.Session.ReadHistoryPage — one acquisition
// of the runtime lock over the head, the owed debt, the total and the rows —
// and this handler adds only the wire spelling: the rows go out pre-encoded
// through sessionruntime.EncodeRows, the ONE encoder of the rows vocabulary,
// so a page a coordinator relays and the frames it paints are the same cells.

import (
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// historyPage answers one page of a session's live history.
func (s *Service) historyPage(p proto.HistoryPageParams) (proto.HistoryPageResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.HistoryPageResult{}, err
	}
	return hs.historyPage(p.Before, int(p.Limit))
}

// historyPage reads the page off the session's runtime and encodes its rows.
// It takes no lock of the host session: the lock it takes is the runtime's,
// exactly as readFrame does (screen.go) — a page read that waited on
// hs.mu would park behind a program that has stopped reading.
func (hs *hostSession) historyPage(before *uint64, limit int) (proto.HistoryPageResult, error) {
	view, err := hs.runtime.ReadHistoryPage(before, limit)
	if err != nil {
		return proto.HistoryPageResult{}, err
	}
	if hs.scrollback.Load() == 0 {
		// The setting belongs to the helper's live surface, not the runtime:
		// the runtime keeps a bounded capture floor so departures remain
		// durable. Preserve the runtime's cursor/floor snapshot, but expose an
		// empty interval so no captured rows become live scrollback.
		view.Start = view.End
		view.More = false
		view.Rows = nil
	}
	rows, err := sessionruntime.EncodeRows(view.Rows)
	if err != nil {
		return proto.HistoryPageResult{}, err
	}
	return proto.HistoryPageResult{
		Floor: view.Floor,
		Start: view.Start,
		End:   view.End,
		More:  view.More,
		Rows:  rows,
	}, nil
}
