package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── history.* ingress bounds and validators (the per-field sweep) ─────────

// maxRecordCommandRunes bounds the command text history.record accepts: a
// command record line — far above any real command a terminal submits (the
// lifecycle kernel bounds the same product class at 4096 bytes), and the
// per-field wire-cost ceiling under the 64 KiB frame budget.
const maxRecordCommandRunes = 16_384

// maxSearchTextRunes bounds the history.query search filter: a substring
// over command, so a filter longer than the longest recordable command can
// never match anything.
const maxSearchTextRunes = maxRecordCommandRunes

// validateHistoryQueryRaw checks history.query against the recall contract:
// the closed scope enum, the conditional cwd/host presence ("" is a
// legitimate rung — a command whose cwd was never known — so PRESENCE is
// what is required, never a non-empty value), the opaque `before` row
// handle, and the search-filter bound. The limit is deliberately left to the
// handler's documented clamp (<1 → 50, >200 → 200): the clamp is the
// product contract for that field, not a refusal.
func validateHistoryQueryRaw(raw json.RawMessage) string {
	var p historyQueryParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	switch p.Scope {
	case "directory", "host", "everywhere":
	default:
		return "scope must be one of directory, host, everywhere"
	}
	if p.Scope == "directory" && p.Cwd == nil {
		return "cwd is required for scope=directory"
	}
	if p.Scope == "host" && p.Host == nil {
		return "host is required for scope=host"
	}
	if p.Before != nil {
		if _, err := strconv.ParseInt(*p.Before, 10, 64); err != nil {
			return "before must be the opaque row id of the previous page"
		}
	}
	if p.Text != nil && utf8.RuneCountInString(*p.Text) > maxSearchTextRunes {
		return fmt.Sprintf("text exceeds %d characters", maxSearchTextRunes)
	}
	return ""
}

// validateHistoryRecordRaw checks history.record: the handler's own check
// (validateHistoryRecord — one owner) moved before the handler, plus the
// per-field length bounds that check does not carry.
func validateHistoryRecordRaw(raw json.RawMessage) string {
	var p historyRecordParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateHistoryRecord(p); msg != "" {
		return msg
	}
	if utf8.RuneCountInString(p.Command) > maxRecordCommandRunes {
		return fmt.Sprintf("command exceeds %d characters", maxRecordCommandRunes)
	}
	if utf8.RuneCountInString(p.Cwd) > maxCwdRunes {
		return "cwd exceeds the length bound"
	}
	if utf8.RuneCountInString(p.Host) > maxHostRunes {
		return "host exceeds the length bound"
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
			h := historyQueryHandlers{op: contentOp, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleHistoryQuery(ctx, req) }
		}),
		reg(contentSub, "history.record", params(validateHistoryRecordRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := historyRecordHandlers{op: contentOp, captures: s.captures, machine: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) {
				h.handleHistoryRecord(ctx, w, state, req)
			}
		}),
	}
	return specs
}
