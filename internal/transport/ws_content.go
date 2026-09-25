package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── history.* ingress bounds and validators (the per-field sweep) ─────────

// maxSearchTextRunes bounds the history.query search filter: a substring
// over command, so a filter longer than the longest recordable command can
// never match anything.
const maxSearchTextRunes = maxRecordCommandRunes

// validateHistoryQueryRaw checks history.query against the recall contract:
// the closed scope enum, the conditional pane/cwd/host presence ("" is a
// legitimate directory rung and local host, but pane ids must be non-empty),
// the opaque `before` row handle, and the search-filter bound. The limit is
// deliberately left to the handler's documented clamp (<1 → 50, >200 → 200):
// the clamp is the product contract for that field, not a refusal.
func validateHistoryQueryRaw(raw json.RawMessage) string {
	var p historyQueryParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	switch p.Scope {
	case "pane", "directory", "host", "everywhere":
	default:
		return "scope must be one of pane, directory, host, everywhere"
	}
	if p.Scope == "pane" && (p.PaneID == nil || *p.PaneID == "") {
		return "paneId is required and must be non-empty for scope=pane"
	}
	if p.Scope == "directory" && p.Cwd == nil {
		return "cwd is required for scope=directory"
	}
	if p.Scope == "host" && p.Host == nil {
		return "host is required for scope=host"
	}
	// THE CURSOR IS OPAQUE, and this bound may not know more about it than
	// the handler does. It used to ParseInt the handle, which was true of the
	// interim command_history's rowid and is false of the ledger's
	// client-minted UUIDv7 — so after nocx-rtg0.19 every real `before` a
	// renderer could send was refused here, in front of a handler that had
	// already stopped reading the shape (parseHistoryQueryParams). Two owners
	// of one predicate, and the one that lost the plot won by running first.
	// The only cursor that is refusable without reading it is the empty one,
	// which names no row at all — the same rule, in the same words, as the
	// handler's.
	if p.Before != nil && *p.Before == "" {
		return "before must be the opaque row id of the previous page"
	}
	if p.Text != nil && utf8.RuneCountInString(*p.Text) > maxSearchTextRunes {
		return fmt.Sprintf("text exceeds %d characters", maxSearchTextRunes)
	}
	return ""
}

func (s *WSServer) contentSpecs(lane control.Admission, contentGate control.Admission, contentSub control.Submission) []methodSpec {
	var contentOp capability.ContentOperation
	if s.contentDB != nil {
		contentOp = capability.NewContentOperation(contentGate, lane, s.contentDB)
	}
	specs := []methodSpec{
		regResponder(contentSub, "history.query", params(validateHistoryQueryRaw), func(r Responder) handlerFunc {
			h := historyQueryHandlers{op: contentOp, durable: s.historyDurableAvailable, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleHistoryQuery(ctx, req) }
		}),
	}
	return specs
}
