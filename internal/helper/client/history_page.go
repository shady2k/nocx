package client

// The live-history read, coordinator side (nocx-zg3k3.10.3): the call that
// asks a session's helper for one page of the scrollback its emulator holds.
// The mirror of client/screen.go's reads, with the same three answers and
// the same typed refusal for a generation that predates the op.

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// ErrHistoryPageUnsupported is a helper generation that does not answer
// OpHistoryPage. It is a fact about the GENERATION and not about the pane:
// two generations are resident at once by design, and one of them cannot
// page a pane's history at all. A caller that could not tell it from "the
// session is gone" would report a live pane as broken.
var ErrHistoryPageUnsupported = errors.New("helper: this generation does not page a session's history")

// HistoryPage reads one page of a session's live history. The rows ride the
// result pre-encoded in the frame contract's rows vocabulary; what the
// caller relays to its own client is the coordinator's business (the page
// id it mints and the carrier it publishes on), never this call's.
func (c *Client) HistoryPage(ctx context.Context, id HostSessionID, before *uint64, limit int) (proto.HistoryPageResult, error) {
	if limit < 1 || limit > sessionruntime.MaxHistoryPageRows {
		return proto.HistoryPageResult{}, fmt.Errorf("helper: history page limit %d out of range 1..%d",
			limit, sessionruntime.MaxHistoryPageRows)
	}
	var result proto.HistoryPageResult
	err := c.Call(ctx, proto.ServiceSession, proto.OpHistoryPage, proto.HistoryPageParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(id.Generation),
			Session:    id.Session,
		},
		Before: before,
		Limit:  uint32(limit), //nolint:gosec // bounded to 1..MaxHistoryPageRows above
	}, &result)
	if err != nil {
		var refusal *RefusalError
		if errors.As(err, &refusal) && refusal.Code == proto.ErrCodeUnknownOp {
			return proto.HistoryPageResult{}, ErrHistoryPageUnsupported
		}
		return proto.HistoryPageResult{}, err
	}
	return result, nil
}
