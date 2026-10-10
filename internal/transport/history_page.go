package transport

// session.historyPage (nocx-zg3k3.10.3): one page of a session's LIVE
// history, read as the emulator holds it.
//
// The method's result carries the page's FACTS — the interval in the
// session's absolute history row numbering, the floor below which nothing is
// retained, whether rows remain below — and the page id. The rows themselves
// never ride this result: terminal presentation data travels the binary data
// plane on the screen frame's own carrier (AD-1, ADR-0066, ADR-0073), keyed
// by that page id. A client matches the carrier document to this result by
// the id and never by arrival order — though on one socket the document is
// queued before the answer, so a client that reads in order never waits.
//
// The read belongs to the process that holds the terminal (ADR-0066): the
// HistoryPager seam is the helper relay, wired at the composition root the
// way ScreenResender is. A generation that predates the op answers a typed
// refusal (helper.ErrHistoryPageUnsupported), which is a fact about the
// generation and not about the session.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/transport/control"
)

// HistoryPage is what the pager seam answers: the page's facts beside the
// rows, pre-encoded by the ONE encoder of the rows vocabulary
// (sessionruntime.EncodeRows). It is the helper op's result carried across
// the process boundary, and nothing here re-derives a field of it.
type HistoryPage struct {
	Floor uint64
	Start uint64
	End   uint64
	More  bool
	Rows  json.RawMessage
}

// HistoryPager asks the process that holds a session's terminal for one page
// of the history its emulator retains.
type HistoryPager interface {
	HistoryPage(ctx context.Context, sessionID string, before *uint64, limit int) (HistoryPage, error)
}

// WithHistoryPager wires the route a page read takes to the helper that
// holds the session. Unwired, the method is not registered and a caller's
// next move is to stop asking rather than to fix its arguments
// (registration.go).
func WithHistoryPager(p HistoryPager) WSServerOption {
	return func(ws *WSServer) { ws.historyPager = p }
}

// historyPagePublisher is the publish seam the handler uses, and nothing
// more: a handler never reaches the *WSServer whole.
type historyPagePublisher interface {
	PublishHistoryPage(sid session.ID, doc []byte) bool
}

// sessionHistoryPageParams is the request (contracts/session.historyPage
// .params.schema.json). before is the exclusive upper bound in the absolute
// history row numbering, null for the head — the newest retained rows. It
// decodes as an int64 so a negative number is REFUSED BY NAME in the
// validator rather than failing the decode into an unsigned field with a
// message about JSON objects; the handler converts to the unsigned cursor
// only after that refusal.
type sessionHistoryPageParams struct {
	SessionID string `json:"sessionId"`
	Before    *int64 `json:"before"`
	Limit     int    `json:"limit"`
}

// cursor converts the validated bound to the unsigned cursor the seam takes.
func (p sessionHistoryPageParams) cursor() *uint64 {
	if p.Before == nil || *p.Before < 0 {
		return nil
	}
	before := uint64(*p.Before) //nolint:gosec // refused negative above
	return &before
}

// sessionHistoryPage is the result DTO, hand-written beside the schema per
// the contracts README: durableThrough is RESERVED for the live↔durable
// join and is null on every answer this task sends — a field the wire
// carries and nothing fills yet, on purpose.
type sessionHistoryPage struct {
	PageID         string  `json:"pageId"`
	Start          uint64  `json:"start"`
	End            uint64  `json:"end"`
	Floor          uint64  `json:"floor"`
	More           bool    `json:"more"`
	DurableThrough *uint64 `json:"durableThrough"`
}

// sessionHistoryPageRows is the carrier document: the page id the result
// names, and the page's rows in the frame contract's rows shape. Its schema
// is contracts/session.historyPageRows.schema.json; the $defs are the frame
// contract's, copied, and the guard test in ws_history_page_test.go keeps
// the copy identical rather than trusting either hand.
type sessionHistoryPageRows struct {
	PageID string          `json:"pageId"`
	Rows   json.RawMessage `json:"rows"`
}

// validateHistoryPageRaw is the registered validator for session.historyPage.
// The session id is server-minted, so the 32-hex shape is the honest check;
// limit is the page's own bound, the same number the schema carries and the
// seam refuses — one bound, not three spellings of it.
func validateHistoryPageRaw(raw json.RawMessage) string {
	var p sessionHistoryPageParams
	if msg := decodeObject(raw, &p, "sessionId", "limit"); msg != "" {
		return msg
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return "params must be a JSON object"
	}
	if _, ok := present["before"]; !ok {
		return "before is required (null asks for the head)"
	}
	if p.Before != nil && *p.Before < 0 {
		return "before must not be negative"
	}
	if p.Limit < 1 || p.Limit > sessionruntime.MaxHistoryPageRows {
		return "limit is out of range 1..64"
	}
	return ""
}

// historyPageHandlers answers session.historyPage. It holds the per-session
// operation factory, the pager seam, the publish seam and its Responder —
// never the *WSServer whole.
type historyPageHandlers struct {
	ops       *capability.SessionOperations
	pager     HistoryPager
	publisher historyPagePublisher
	r         Responder
}

// handleHistoryPage answers with one page's facts, having queued its rows on
// the carrier first.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"session.historyPage",
//	     "params":{"sessionId":"…","before":null,"limit":30}}
//	<-- (binary, screen carrier) {"pageId":"…","rows":[…]}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"pageId":"…","start":47,"end":77,
//	     "floor":0,"more":false,"durableThrough":null}}
//
// The session gate is held only for the claim the method shares with every
// session-plane read (the session exists HERE, in the words session.output
// refuses by); the read itself is the helper's round trip and runs outside
// the gate, exactly as session.output's store read does.
func (h historyPageHandlers) handleHistoryPage(ctx context.Context, req jsonrpcRequest) {
	var params sessionHistoryPageParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	sid := session.ID(params.SessionID)
	op, err := h.ops.ForSession(sid)
	if err != nil {
		_ = h.r.TryError(req.ID, refuseClaim(reasonUnknownSession, "Invalid params: unknown sessionId"))
		return
	}
	if runErr := op.Run(ctx, func(_ context.Context, svc capability.SessionService) error {
		_, gerr := svc.Get(sid)
		return gerr
	}); runErr != nil {
		answerOperationRefusal(h.r, req, runErr)
		return
	}

	res, err := h.pager.HistoryPage(ctx, params.SessionID, params.cursor(), params.Limit)
	if err != nil {
		log.From(ctx).Warn("session history page could not be read",
			"session_id", params.SessionID, "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	pageID, err := mintHistoryPageID()
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "history page could not be minted"})
		return
	}
	doc, err := json.Marshal(sessionHistoryPageRows{PageID: pageID, Rows: res.Rows})
	if err != nil {
		log.From(ctx).Warn("session history page could not be encoded",
			"session_id", params.SessionID, "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "history page could not be encoded"})
		return
	}
	// The rows are QUEUED BEFORE the answer names them: one socket, one FIFO,
	// so a client that reads in order finds the document waiting when the
	// result arrives. A refused publish is the caller's answer, not a log
	// line — a page is not superseded by the next revision the way a frame
	// is, so silence here would be a request that never answers.
	if !h.publisher.PublishHistoryPage(sid, doc) {
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32603,
			Message: "history page dropped: nobody is attached to this session, or its outbound queue refused the page",
		})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(sessionHistoryPage{
		PageID:         pageID,
		Start:          res.Start,
		End:            res.End,
		Floor:          res.Floor,
		More:           res.More,
		DurableThrough: nil,
	}))
}

// historyPageSpecs registers session.historyPage on its own per-operation
// queue: a page read is not part of the attach act the session queue
// serialises — it happens on scroll, later, against a session that has long
// been established. Unwired pager leaves the method unregistered
// (whenAvailable), the way an unwired store leaves session.output.
func (s *WSServer) historyPageSpecs(lane control.Admission, sessionGate control.Admission) []methodSpec {
	historyOp := capability.NewSessionOperations(sessionGate, lane, s.registry, s.profileUsage)
	return []methodSpec{
		whenAvailable(regResponder(s.operationQueue("history-page"), "session.historyPage", params(validateHistoryPageRaw), func(r Responder) handlerFunc {
			h := historyPageHandlers{ops: historyOp, pager: s.historyPager, publisher: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleHistoryPage(ctx, req) }
		}), func() bool { return s.historyPager != nil }, "method not found: history pager not wired"),
	}
}

// mintHistoryPageID mints the id that keys one carrier document to its
// result: 16 random bytes, hex — the same shape and entropy the request
// broker's ids carry.
func mintHistoryPageID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
