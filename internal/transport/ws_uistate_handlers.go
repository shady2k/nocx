package transport

// The uistate.* control handlers as constructed types: each holds a
// UIStateOperation and the Responder — never the *WSServer, and never the
// *uistate.Store directly. The store arrives inside op.Run, guard-bound, so a
// handler cannot reach the document outside the operation that gates it.
//
// UI state belongs to the config conflict domain for the same reason snippets
// and notes do: the document lives in the config directory, and a restore
// replaces that directory underneath a write.
//
// WHY THERE IS NO uistate.changed BROADCAST. One window observes this state
// and one window writes it; a notification would be the app telling itself
// what it just did. The settings registry has one because a setting is a
// decision several surfaces act on; this is not that (ADR-0033 §7).

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/uistate"
)

type uiStateHandlers struct {
	op    capability.UIStateOperation // nil → ui state not wired
	wired bool
	r     Responder
}

func (h uiStateHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "ui state not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.UIStateService) error {
		switch req.Method {
		case "uistate.get":
			_ = h.r.TryResult(req.ID, mustMarshal(wireLayout(svc.Layout(ctx))))
		case "uistate.set":
			var p uiStateSetParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			svc.SetLayout(ctx, uistate.Layout{
				Sidebar: uistate.Sidebar{
					Collapsed:    p.Sidebar.Collapsed,
					ActiveViewID: p.Sidebar.ActiveViewID,
					Width:        p.Sidebar.Width,
				},
				ActiveTab: p.ActiveTab,
			})
			// The stored value, not the sent one: the width is clamped on the
			// way in, and a renderer that never learns that holds a number
			// nobody will read back.
			_ = h.r.TryResult(req.ID, mustMarshal(wireLayout(svc.Layout(ctx))))
		case "uistate.window.get":
			_ = h.r.TryResult(req.ID, mustMarshal(wireWindow(svc.Window(ctx))))
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleWindowObserve folds one live sample relayed by a client's shell
// (ADR-0098). It is a notification, so there is no response to write: the
// sample is recorded and the next uistate.window.get answers with what the
// store now holds, folded — which is not always what was sent.
func (h uiStateHandlers) handleWindowObserve(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		return
	}
	var p uiStateWindow
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return
	}
	_ = h.op.Run(ctx, func(ctx context.Context, svc capability.UIStateService) error {
		svc.ObserveWindow(ctx, uistate.Window(p))
		return nil
	})
}

// uiStateLayout is the wire DTO for both methods. It is a hand-written struct
// beside the schema rather than a re-export of uistate.Layout, per the
// contracts README: the domain type is free to change shape without silently
// changing the wire.
type uiStateLayout struct {
	Sidebar   uiStateSidebar `json:"sidebar"`
	ActiveTab string         `json:"activeTab"`
}

type uiStateSidebar struct {
	Collapsed    bool   `json:"collapsed"`
	ActiveViewID string `json:"activeViewId"`
	Width        int    `json:"width"`
}

type uiStateSetParams struct {
	Sidebar   uiStateSidebar `json:"sidebar"`
	ActiveTab string         `json:"activeTab"`
}

func wireLayout(l uistate.Layout) uiStateLayout {
	return uiStateLayout{
		Sidebar: uiStateSidebar{
			Collapsed:    l.Sidebar.Collapsed,
			ActiveViewID: l.Sidebar.ActiveViewID,
			Width:        l.Sidebar.Width,
		},
		ActiveTab: l.ActiveTab,
	}
}

// maxUIStateIDRunes bounds the two free-text fields. Both are ids minted by
// the shell — a view id from the sidebar registry, a pane id from the tab
// manager — so this is generous for anything legitimate and exists because
// the control plane may not carry an unbounded string.
const maxUIStateIDRunes = 256

func validateUIStateSetRaw(raw json.RawMessage) string {
	var p uiStateSetParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := boundedRunes("sidebar.activeViewId", p.Sidebar.ActiveViewID, maxUIStateIDRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("activeTab", p.ActiveTab, maxUIStateIDRunes); msg != "" {
		return msg
	}
	// The width is NOT rejected when out of range: it is clamped by the store,
	// which is the single owner of that policy. Refusing it here would be a
	// second answer to one question, and the two would drift the first time
	// the bounds moved.
	return ""
}

// uiStateWindow is the wire DTO for the window half in BOTH directions
// (ADR-0098): what uistate.window.get answers and what one
// uistate.window.observe notification carries. A hand-written struct beside
// the schema for the same reason as uiStateLayout — the domain type is free
// to change shape without silently changing the wire.
type uiStateWindow struct {
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Maximised  bool   `json:"maximised"`
	FullScreen bool   `json:"fullScreen"`
	Displays   string `json:"displays"`
}

func wireWindow(w uistate.Window) uiStateWindow {
	return uiStateWindow{
		Width:      w.Width,
		Height:     w.Height,
		X:          w.X,
		Y:          w.Y,
		Maximised:  w.Maximised,
		FullScreen: w.FullScreen,
		Displays:   w.Displays,
	}
}

// The geometry bounds are generous rather than policy: what a size MEANS is
// uistate's (Restore clamps on read), and these exist only so the control
// plane cannot be made to carry an absurd integer. A position may be
// negative on purpose — a display left of or above the primary.
const (
	maxUIStateWindowExtent  = 100000
	maxUIStateDisplaysRunes = 256
)

func validateUIStateWindowObserveRaw(raw json.RawMessage) string {
	var p uiStateWindow
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	// A sample with no size is not a sample: the shell's probe discards a
	// zero reading rather than record it (nocx-39vhn's rule), and the store
	// reads zeros as "never saved". Refusing it here also keeps the method
	// answering a request that carries an id — every registered method must
	// (the wire sweep over them all).
	if p.Width <= 0 || p.Height <= 0 {
		return "a sample must carry a real size"
	}
	if p.Width > maxUIStateWindowExtent {
		return "width out of range"
	}
	if p.Height > maxUIStateWindowExtent {
		return "height out of range"
	}
	if p.X < -maxUIStateWindowExtent || p.X > maxUIStateWindowExtent {
		return "x out of range"
	}
	if p.Y < -maxUIStateWindowExtent || p.Y > maxUIStateWindowExtent {
		return "y out of range"
	}
	if msg := boundedRunes("displays", p.Displays, maxUIStateDisplaysRunes); msg != "" {
		return msg
	}
	return ""
}
