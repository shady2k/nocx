package app

// The adapter between the coordinator's pane state and the wire (design §5.6,
// nocx-oalo1.5.3). It holds no decision, and that is exactly why it is worth a
// test: the two presence rules it encodes are the ones a value type cannot
// express -- a plain-shell count of ZERO on a connected connection, and the
// absence of the count on an opening one.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/panedriver"
)

func TestPaneConnectionWireKeepsThePresenceRulesOfTheState(t *testing.T) {
	at := time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC)

	opening := paneConnectionWire(panedriver.Snapshot{
		PaneID: "pane-1", ConnectionID: "conn-1", ConnectionEpoch: 3,
		InstanceID: "instance-1",
		Transport:  panedriver.Transport{State: "opening", Stage: "tcp", StateMs: 40},
		Attempt:    panedriver.Attempt{Shown: "none", Candidate: "none"},
		ObservedAt: at,
	})
	if opening.Transport.State != "opening" || opening.Transport.Stage != "tcp" {
		t.Fatalf("transport = %+v, want opening(tcp)", opening.Transport)
	}
	if opening.Transport.PlainShells != nil {
		t.Fatal("an opening carries plainShells")
	}
	if opening.ObservedAt != "2026-11-14T07:00:00Z" {
		t.Fatalf("observedAt = %q", opening.ObservedAt)
	}
	if opening.Quality != nil {
		t.Fatal("an unmeasured connection carries quality")
	}

	connected := paneConnectionWire(panedriver.Snapshot{
		PaneID: "pane-1", ConnectionID: "conn-1", ConnectionEpoch: 4,
		InstanceID: "instance-1", SessionID: "sess-1",
		Transport: panedriver.Transport{
			State: "connected", StateMs: 1200, PlainShells: 0, HasPlainShells: true,
		},
		Attempt:    panedriver.Attempt{Shown: "none", Candidate: "none"},
		ObservedAt: at,
	})
	if connected.Transport.PlainShells == nil || *connected.Transport.PlainShells != 0 {
		t.Fatal("connected with no shell must carry plainShells 0, not omit it")
	}
	if connected.SessionID != "sess-1" {
		t.Fatalf("sessionId = %q", connected.SessionID)
	}

	stalled := paneConnectionWire(panedriver.Snapshot{
		PaneID: "pane-1", ConnectionID: "conn-1", ConnectionEpoch: 5,
		InstanceID: "instance-1",
		Transport: panedriver.Transport{
			State: "stalled", StateMs: 45000, PlainShells: 2, HasPlainShells: true,
			LookAnsweredAt: at.Add(20 * time.Second), HasLook: true,
		},
		Quality:    &panedriver.Quality{RoundTripMs: 620, Slow: true},
		Attempt:    panedriver.Attempt{Shown: "none", Candidate: "none"},
		ObservedAt: at,
	})
	if stalled.Transport.PlainShells == nil || *stalled.Transport.PlainShells != 2 {
		t.Fatal("stalled lost its shell count")
	}
	if stalled.Transport.LookAnsweredAt != "2026-11-14T07:00:20Z" {
		t.Fatalf("lookAnsweredAt = %q", stalled.Transport.LookAnsweredAt)
	}
	if stalled.Quality == nil || stalled.Quality.RoundTripMS != 620 || !stalled.Quality.Slow {
		t.Fatalf("quality = %+v", stalled.Quality)
	}
	if stalled.Reconnect != nil {
		t.Fatalf("a pane with no offer carried %+v", stalled.Reconnect)
	}
}

// TestAReconnectingCauseStaysOffTheWire: the retry cause is the backend's
// decision input, and the renderer's exact reconnecting branch has no room for
// it (design §5.6 names a cause for failed and ended only).
func TestAReconnectingCauseStaysOffTheWire(t *testing.T) {
	wire := paneConnectionWire(panedriver.Snapshot{
		PaneID: "pane-1", ConnectionID: "conn-1", ConnectionEpoch: 3,
		InstanceID: "instance-1",
		Transport:  panedriver.Transport{State: "reconnecting", Attempt: 2, Cause: "lost-link", StateMs: 400},
		Attempt:    panedriver.Attempt{Shown: "none", Candidate: "none"},
		ObservedAt: time.Now(),
	})
	if wire.Transport.Cause != "" {
		t.Fatalf("a reconnecting payload carried cause %q", wire.Transport.Cause)
	}
	if wire.Transport.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", wire.Transport.Attempt)
	}
	failed := paneConnectionWire(panedriver.Snapshot{
		PaneID: "pane-1", ConnectionID: "conn-1", ConnectionEpoch: 4,
		InstanceID: "instance-1",
		Transport:  panedriver.Transport{State: "failed", Cause: "host-key-changed"},
		Attempt:    panedriver.Attempt{Shown: "none", Candidate: "none"},
		ObservedAt: time.Now(),
	})
	if failed.Transport.Cause != "host-key-changed" {
		t.Fatalf("a failed payload lost its cause: %+v", failed.Transport)
	}
}

// TestPaneConnectionWireCarriesTheOfferOnlyWhenThereIsOne: the object's
// PRESENCE is the button (design §5.8), so nil and an empty object are not the
// same statement.
func TestPaneConnectionWireCarriesTheOfferOnlyWhenThereIsOne(t *testing.T) {
	offered := paneConnectionWire(panedriver.Snapshot{
		PaneID: "pane-1", ConnectionID: "conn-1", ConnectionEpoch: 2,
		InstanceID: "instance-1",
		Transport:  panedriver.Transport{State: "stalled", StateMs: 45000, PlainShells: 1, HasPlainShells: true},
		Reconnect:  &panedriver.Reconnect{EndsShells: 1},
		Attempt:    panedriver.Attempt{Shown: "none", Candidate: "none"},
		ObservedAt: time.Now(),
	})
	if offered.Reconnect == nil || offered.Reconnect.EndsShells != 1 {
		t.Fatalf("offer = %+v, want endsShells 1", offered.Reconnect)
	}
	if !strings.Contains(mustJSON(t, offered), `"reconnect":{"endsShells":1}`) {
		t.Fatalf("the offer does not marshal as the contract's shape: %s", mustJSON(t, offered))
	}
}

// mustJSON renders a payload the way the wire would, for the shape assertion
// above: a pointer to zero must be present, not omitted.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}
