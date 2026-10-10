package app

// The coordinator's half of the history page (nocx-zg3k3.10.3): which helper
// holds a pane, and how one page of its live history is asked for.
//
// It lives beside paneScreen (panescreen.go) and answers through the SAME
// owner lookup every screen read takes — this machine's opener claims what
// is its own, and the remote registry answers for a helper that is not here
// — because "which helper holds this pane's terminal" is one question with
// one owner, and a second derivation of it is the defect AD-8 exists for.
// The composition root wires this as the transport's HistoryPager the way it
// wires paneScreen as its ScreenResender.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/transport"
)

// historyPageTimeout bounds ONE page read, exactly as paneScreenTimeout
// bounds one frame read: the caller is a scroll handler on the control
// plane, and the call reaches another process. Without it a helper that
// stopped answering would park the read lane for as long as the socket
// lives.
const historyPageTimeout = paneScreenTimeout

// historyPageSource adapts a helper's wire client to transport.HistoryPager
// for one pane at a time, resolved per call by the shared owner lookup.
type historyPageSource struct {
	paneScreen *paneScreen
}

func newHistoryPageSource(ps *paneScreen) *historyPageSource {
	return &historyPageSource{paneScreen: ps}
}

// HistoryPage reads one page of a session's live history from the helper
// that holds it. The rows arrive pre-encoded in the frame contract's rows
// vocabulary and cross this seam unchanged — re-encoding them here would be
// a second encoder for one vocabulary.
func (h historyPageSource) HistoryPage(ctx context.Context, sessionID string, before *uint64, limit int) (transport.HistoryPage, error) {
	c, id, err := h.paneScreen.owner(ctx, sessionID)
	if err != nil {
		return transport.HistoryPage{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, historyPageTimeout)
	defer cancel()
	res, err := c.HistoryPage(callCtx, client.HostSessionID{Generation: id.Generation, Session: id.Session}, before, limit)
	if err != nil {
		return transport.HistoryPage{}, err
	}
	if !json.Valid(res.Rows) {
		return transport.HistoryPage{}, fmt.Errorf("app: the helper answered a history page whose rows are not JSON")
	}
	return transport.HistoryPage{
		Floor: res.Floor,
		Start: res.Start,
		End:   res.End,
		More:  res.More,
		Rows:  res.Rows,
	}, nil
}

var _ transport.HistoryPager = historyPageSource{}
