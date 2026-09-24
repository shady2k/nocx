package transport

// secrets.paneClosed — the renderer telling the backend that a pane closed,
// so that pane's pending captures die with it (nocx-tsajw). The capture
// contract (internal/credential/capture.go) names pane closure as a
// destruction trigger, but the transport had no way to fire it: the pending
// captures were scoped to the connection, so a pane's offer sat in backend
// memory until the connection dropped, the vault sealed, or the app quit.
// This notification is the missing trigger: the renderer mints a per-pane
// identity, carries it on lifecycle history so captures are scoped to the
// pane, and announces the pane's death here so those captures are destroyed
// with it.
//
// IT WAS CALLED pane.close AND WAS RENAMED (nocx-isoph.4). The layout chain
// gained a real removal method for the durable object — panes.close, beside
// panes.create and panes.move — and two methods a letter apart, one meaning
// "drop this pane's pending captures" and the other "remove the pane from
// the chain", is the two-surfaces-one-word defect AGENTS.md names: the loser
// goes on advertising what it can no longer deliver. So the destructive act
// lives in the layout family and this one moved into the domain that already
// owns a pending capture (secrets.captureSave, secrets.captureDismiss),
// named in the past tense because it reports rather than asks.
//
// They are not merged and must not be. This touches no store and needs no
// answer; panes.close rewrites three tables in one transaction and can mint a
// replacement tab. A renderer sends both when a pane it owns goes away, and
// sends only this one when the pane's chrome dies without the pane being
// removed from the chain.
//
// The params shape is declared once in
// contracts/secrets.paneClosed.schema.json (the renderer's type is generated
// from it); this handler is the Go side's check. It is a notification with no
// response — the renderer is fire-and-forget, and a lost frame is covered by
// the transport-disconnect trigger (DestroyConnection), which is the same
// destruction the pane's death would have caused anyway.
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
)

// paneClosedParams is the payload of the secrets.paneClosed notification.
type paneClosedParams struct {
	PaneID string `json:"paneId"`
}

// maxPaneIDRunes bounds the renderer-minted pane identity that lifecycle
// history and secrets.paneClosed accept: a UUIDv7 is 36 characters, and any
// real identity fits far under this. The bound is per-field wire-cost
// hygiene — the same shape as every other string bound in this package — so
// a hostile frame cannot make the server hold an unbounded string in a capture
// scope.
//
// IT IS DELIBERATELY NOT THE LAYOUT'S UUIDv7 CHECK, though the renderer now
// mints one v7 identity and sends it to both (nocx-isoph.4). The layout
// validates the shape because it STORES the id and an id it stored under a
// wrong shape is a row nobody can address; this domain stores nothing and
// keys a scope by whatever it is given, bound to the connection. Restating
// the shape rule here would give it a second owner in a package with no id of
// its own — and the two would agree until the day the layout learned about a
// v8.
const maxPaneIDRunes = 128

// validatePaneClosedRaw checks the secrets.paneClosed notification: the paneId must be
// present and within bounds. The connection half of the destruction key is
// never on this wire — it is the handler's own identity, added at destroy
// time so a pane id from one connection can never reach another's captures.
func validatePaneClosedRaw(raw json.RawMessage) string {
	var p paneClosedParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.PaneID) == "" {
		return "paneId is required"
	}
	if utf8.RuneCountInString(p.PaneID) > maxPaneIDRunes {
		return fmt.Sprintf("paneId exceeds %d characters", maxPaneIDRunes)
	}
	return ""
}

// paneClosedHandlers answers secrets.paneClosed: registry only, no capability — destroying
// a pending capture touches no store, exactly like secrets.captureDismiss.
// It holds the *wsConn as identity (reg, not regResponder) because the
// destruction key is (connection, pane), and the connection half is the
// handler's own fact.
type paneClosedHandlers struct {
	captures *credential.CaptureRegistry
	log      log.Logger
}

// handlePaneClosed destroys the closed pane's pending captures. The validator has
// already refused a missing or oversized paneId; the defensive re-parse and
// the silent return on failure mirror ackHandler — a notification has no
// response to carry an error, and nothing is left half-destroyed.
func (h paneClosedHandlers) handlePaneClosed(_ context.Context, wconn *wsConn, req jsonrpcRequest) {
	var p paneClosedParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		h.log.Warn("secrets.paneClosed invalid params")
		return
	}
	if h.captures != nil {
		h.captures.DestroyPane(connectionID(wconn), p.PaneID)
	}
}
