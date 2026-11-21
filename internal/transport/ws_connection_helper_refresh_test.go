package transport

// connection.helperRefresh, the far helper's one action (ADR-727g63tr
// decision 2). What this file pins is the GATE and the seam: a press is
// answered only for a pane this connection opened, at the epoch whose helper
// object offered refresh, on an axis that is still helper-lost. Every other
// state has no button, so the call is refused rather than allowed to reach the
// owner and start something nobody asked for.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

type helperRefreshProbe struct {
	mu     sync.Mutex
	calls  []string
	err    error
	closed []string
}

func (p *helperRefreshProbe) RefreshHelper(_ context.Context, paneID string, _ uint64) error {
	p.mu.Lock()
	p.calls = append(p.calls, paneID)
	err := p.err
	p.mu.Unlock()
	return err
}

func (p *helperRefreshProbe) PaneClosed(paneID string) {
	p.mu.Lock()
	p.closed = append(p.closed, paneID)
	p.mu.Unlock()
}

func (p *helperRefreshProbe) refreshed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func publishHelperFrame(ws *WSServer, paneID string, epoch uint64, helper PaneConnectionHelper) {
	ws.PublishPaneConnection(PaneConnection{
		Generation:      1,
		PaneID:          paneID,
		ConnectionID:    "conn-1",
		ConnectionEpoch: epoch,
		InstanceID:      "instance-1",
		Helper:          helper,
		Transport:       PaneConnectionTransport{State: "connected", StateMs: 1000, PlainShells: pointerTo(1)},
		Attempt:         PaneConnectionAttempt{Shown: "none", Candidate: "none"},
		ObservedAt:      "2026-11-20T10:00:00Z",
	})
}

func pointerTo(n int) *int { return &n }

func TestConnectionHelperRefreshRedialsTheOfferedAxis(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")
	probe := &helperRefreshProbe{}
	ws.paneConnectionPolicy = probe
	publishHelperFrame(ws, "pane-1", 1, PaneConnectionHelper{
		State: "unavailable", Reason: "helper-lost", Action: "refresh",
	})

	resp := vaultCall(t, conn, "connection.helperRefresh", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("answer = %+v", resp.Error)
	}
	validateJSON(t, loadSchema(t, "connection.helperRefresh.schema.json"), resp.Result, "refresh registration")
	var got connectionHelperRefreshResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.ConnectionID != "conn-1" || !got.Refreshed {
		t.Fatalf("result = %+v, want conn-1 refreshed", got)
	}
	if calls := probe.refreshed(); len(calls) != 1 || calls[0] != "pane-1" {
		t.Fatalf("refreshes = %v, want one for pane-1", calls)
	}
}

func TestConnectionHelperRefreshRefusesAnAxisWithoutTheOffer(t *testing.T) {
	for _, helper := range []PaneConnectionHelper{
		{State: "serving", Action: "none"},
		{State: "starting", Action: "none"},
		{State: "paused", Reason: "connection", Action: "none"},
		{State: "unavailable", Reason: "install-failed", Action: "none"},
		{State: "unavailable", Reason: "mode-raw", Action: "host-settings"},
	} {
		t.Run(helper.State+"/"+helper.Reason, func(t *testing.T) {
			ws, stop := newPaneConnectionServer(t)
			defer stop()
			conn := connectWS(t, ws)
			wc := soleWSConn(t, ws)
			bindTestPane(t, ws, wc, "pane-1")
			probe := &helperRefreshProbe{}
			ws.paneConnectionPolicy = probe
			publishHelperFrame(ws, "pane-1", 1, helper)

			resp := vaultCall(t, conn, "connection.helperRefresh", map[string]any{
				"paneId": "pane-1", "connectionEpoch": 1,
			}, 1)
			if resp.Error == nil || resp.Error.Code != -32602 {
				t.Fatalf("answer = %+v, want -32602", resp)
			}
			if resp.Error.Message != "Invalid params: "+helperRefreshRefusal {
				t.Fatalf("message = %q", resp.Error.Message)
			}
			if calls := probe.refreshed(); len(calls) != 0 {
				t.Fatalf("a refused press reached the owner: %v", calls)
			}
		})
	}
}

func TestConnectionHelperRefreshRefusesAStaleEpoch(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")
	probe := &helperRefreshProbe{}
	ws.paneConnectionPolicy = probe
	publishHelperFrame(ws, "pane-1", 1, PaneConnectionHelper{
		State: "unavailable", Reason: "helper-lost", Action: "refresh",
	})
	publishHelperFrame(ws, "pane-1", 2, PaneConnectionHelper{State: "serving", Action: "none"})

	resp := vaultCall(t, conn, "connection.helperRefresh", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 || resp.Error.Message != "Invalid params: stale connectionEpoch" {
		t.Fatalf("answer = %+v, want stale connectionEpoch", resp)
	}
	if calls := probe.refreshed(); len(calls) != 0 {
		t.Fatalf("a stale press reached the owner: %v", calls)
	}
}

func TestConnectionHelperRefreshRefusesAPaneThisConnectionNeverOpened(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "connection.helperRefresh", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 || resp.Error.Message != "Invalid params: unknown paneId" {
		t.Fatalf("answer = %+v, want unknown paneId", resp)
	}
}

func TestConnectionHelperRefreshSaysWhenNoOwnerIsWired(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")
	publishHelperFrame(ws, "pane-1", 1, PaneConnectionHelper{
		State: "unavailable", Reason: "helper-lost", Action: "refresh",
	})

	resp := vaultCall(t, conn, "connection.helperRefresh", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Fatalf("answer = %+v, want -32603 for a missing owner", resp)
	}
}

// A refusal from the owner is the axis having moved between the frame and the
// press. It is a refused press, not a server fault, and the real cause stays in
// the log.
func TestConnectionHelperRefreshRefusesWhenTheOwnerSaysTheOfferIsGone(t *testing.T) {
	ws, stop := newPaneConnectionServer(t)
	defer stop()
	conn := connectWS(t, ws)
	wc := soleWSConn(t, ws)
	bindTestPane(t, ws, wc, "pane-1")
	probe := &helperRefreshProbe{err: errors.New("the axis moved")}
	ws.paneConnectionPolicy = probe
	publishHelperFrame(ws, "pane-1", 1, PaneConnectionHelper{
		State: "unavailable", Reason: "helper-lost", Action: "refresh",
	})

	resp := vaultCall(t, conn, "connection.helperRefresh", map[string]any{
		"paneId": "pane-1", "connectionEpoch": 1,
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 || resp.Error.Message != "Invalid params: "+helperRefreshRefusal {
		t.Fatalf("answer = %+v, want a refused press", resp)
	}
}
