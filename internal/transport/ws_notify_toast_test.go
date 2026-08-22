package transport

// notify.toast — the toast sink's wire half (nocx-c6ef, plan D2). The sink
// lives in internal/notify and hands the event to a port; this is the
// implementation of that port, and these tests drive it through the real
// socket.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/notify"
)

func newToastWS(t *testing.T) (*WSServer, *websocket.Conn) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	// A dial that has returned is not yet a client: the server registers the
	// connection on its own goroutine, and Toast answers "unavailable" when
	// it finds none registered. Wait on the registration -- the observable --
	// rather than on a duration, or this passes on a quiet machine and fails
	// in the container under load, which is exactly what it did.
	waitForConns(t, ws, 1)
	return ws, conn
}

func TestNotifyToast_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.toast.schema.json")
	ws, conn := newToastWS(t)

	err := ws.Toast(context.Background(), notify.Event{
		SessionID: "s1", Title: "deploy failed", Body: "exit status 1",
		Level: notify.LevelWarning,
	})
	if err != nil {
		t.Fatalf("Toast: %v", err)
	}

	params := readNotification(t, conn, "notify.toast", wantWithin)
	validateJSON(t, schema, params, "notify.toast params")
	var got notifyToastParams
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got.Title != "deploy failed" || got.Body != "exit status 1" || got.Level != "warning" {
		t.Errorf("notify.toast params = %+v, want the event's title, body and level", got)
	}
}

// Every renderer attached to this backend sees it: the toast is a window-level
// surface, and a second window that missed it would be a window the
// notification never reached.
func TestNotifyToast_ReachesEveryAttachedRenderer(t *testing.T) {
	ws, first := newToastWS(t)
	second := connectWS(t, ws)
	t.Cleanup(func() { _ = second.Close() })
	waitForConns(t, ws, 2)

	if err := ws.Toast(context.Background(), notify.Event{Title: "build finished", Level: notify.LevelSuccess}); err != nil {
		t.Fatalf("Toast: %v", err)
	}

	for name, conn := range map[string]*websocket.Conn{"first client": first, "second client": second} {
		params := readNotification(t, conn, "notify.toast", wantWithin)
		var got notifyToastParams
		if err := json.Unmarshal(params, &got); err != nil {
			t.Fatalf("%s: unmarshal params: %v", name, err)
		}
		if got.Title != "build finished" {
			t.Errorf("%s: title = %q, want the event's", name, got.Title)
		}
	}
}

// With no renderer attached there is no toast surface on this host right now,
// and the port says so with the same word the unavailable attention host uses
// — so the composition root's one exemption recognises it and the feed is not
// filled with a row per notification saying nobody was looking.
func TestNotifyToast_WithNoRendererAttachedReportsUnavailable(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	err := ws.Toast(context.Background(), notify.Event{Title: "nobody is looking"})
	if !errors.Is(err, notify.ErrUnavailable) {
		t.Fatalf("Toast with no renderer = %v, want ErrUnavailable", err)
	}
}

// A cancelled invocation returns at once and presents nothing: the router
// imposes a finite deadline on every sink invocation and a sink must honour
// it (ADR-0029 §2.2).
func TestNotifyToast_HonoursACancelledContext(t *testing.T) {
	ws, _ := newToastWS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ws.Toast(ctx, notify.Event{Title: "too late"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Toast on a cancelled ctx = %v, want context.Canceled", err)
	}
}

// The level is a closed enum on the wire. An event that carries none — which
// no nocx-stamped event should, but the zero value exists — must still
// satisfy the contract rather than send "" and be refused by the renderer's
// generated type.
func TestNotifyToast_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.toast.schema.json")
	for name, ev := range map[string]notify.Event{
		"a program's request": {Title: "", Body: "build finished", Level: notify.LevelInfo},
		"a warning":           {Title: "Not delivered", Body: "banner could not deliver it", Level: notify.LevelWarning},
		"danger":              {Title: "vault sealed", Body: "", Level: notify.LevelDanger},
		"success":             {Title: "deploy", Body: "to staging", Level: notify.LevelSuccess},
		"no level at all":     {Title: "unstamped", Body: ""},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(toastParamsFor(ev))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "notify.toast DTO ("+name+")")
		})
	}
}
