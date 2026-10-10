package proto

// The session service's live-history op (nocx-zg3k3.10.3): one page of the
// scrollback a session's emulator holds, read as the emulator holds it.
//
// It lives beside the screen reads (screen.go) because the EMULATOR does —
// ADR-0066 put one beside every PTY, and the coordinator has no terminal
// state of its own to answer from. The result carries the page's facts; the
// rows ride pre-encoded in the frame contract's rows vocabulary, the same
// bytes sessionruntime.EncodeRows gives a block's rows, so the live page and
// the live frame are one cell vocabulary on both planes. The coordinator
// mints the page id that keys the carrier document to the renderer — minting
// nothing here keeps this op a question about a named session, the identity
// rule ADR-0073 already stated for the screen.

import "encoding/json"

// OpHistoryPage reads one page of a session's live history.
const OpHistoryPage = "history-page"

// HistoryPageParams is one page request: the session, the exclusive upper
// bound in the absolute history row numbering (nil asks for the head — the
// newest retained rows), and the most rows the page may carry. A cursor
// below the retained floor is not an error; the answer is the empty interval
// at the cursor with the floor stated.
type HistoryPageParams struct {
	Session HostSessionID `json:"session"`
	Before  *uint64       `json:"before"`
	Limit   uint32        `json:"limit"`
}

// HistoryPageResult is one page: the interval [Start,End) of absolute row
// numbers the rows were read at — Start is always the exclusive lower bound
// of what was DELIVERED, so the next page backwards is Before = Start — the
// Floor below which nothing is retained, whether rows remain below Start,
// and the rows themselves, oldest first, pre-encoded in the frame contract's
// rows array shape.
type HistoryPageResult struct {
	Floor uint64          `json:"floor"`
	Start uint64          `json:"start"`
	End   uint64          `json:"end"`
	More  bool            `json:"more"`
	Rows  json.RawMessage `json:"rows"`
}
