package transport

// THE SWITCH, WHERE A PERSON REACHES IT (nocx-d6gn4.8, criterion 1): the
// method is chosen on the settings screen and the NEXT question is answered
// by the method chosen — asserted in both directions, and through the same
// settings.set call the screen makes, with no save step and no restart.
//
// The read is per ask on purpose. A value captured when the connection opened
// would make "switch the method and ask again" mean "switch the method,
// reconnect, and ask again", which is not a switch anybody can use to compare
// two methods against each other in one sitting.

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
)

func TestAgentAsk_ThePersonsChosenCarrierDrivesTheNextQuestion(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	client := &scriptedAssistantClient{deltas: []string{"ok"}}
	h := newAskHarnessWithOpts(t, client,
		WithSettingsRegistry(reg), WithAgentPolicy(askPolicyStore(t)))
	h.createEndpoint()
	sid := openLocalSession(t, h.conn)

	askOnce := func(askID string, id int) assistant.CarrierKind {
		t.Helper()
		if _, errObj := askOverWire(t, h.conn, map[string]any{
			"askId": askID, "sessionId": sid, "question": "what is here?",
			"cwd": "/repo", "attachedContent": []any{},
		}, id); errObj != nil {
			t.Fatalf("ask refused: %+v", errObj)
		}
		for range client.deltaCount() {
			readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
		}
		return client.receivedParams.Carrier
	}

	// Nobody has chosen: the shipped method, which is the authority floor.
	if got := askOnce("ask-carrier-1", 2); got != assistant.CarrierCalls {
		t.Fatalf("carrier before any choice = %q, want %q", got, assistant.CarrierCalls)
	}

	// The person chooses the program carrier on the settings screen.
	if resp := jsonrpcCall(t, h.conn, "settings.set", map[string]any{
		"key": "assistant.carrier", "value": string(assistant.CarrierProgram),
	}); isErrorResponse(t, resp) {
		t.Fatalf("settings.set refused the carrier: %s", resp)
	}
	if got := askOnce("ask-carrier-2", 3); got != assistant.CarrierProgram {
		t.Fatalf("carrier after choosing %q = %q", assistant.CarrierProgram, got)
	}

	// AND BACK. One direction is a default that happens to match; two is a
	// switch.
	if resp := jsonrpcCall(t, h.conn, "settings.set", map[string]any{
		"key": "assistant.carrier", "value": string(assistant.CarrierCalls),
	}); isErrorResponse(t, resp) {
		t.Fatalf("settings.set refused the carrier: %s", resp)
	}
	if got := askOnce("ask-carrier-3", 4); got != assistant.CarrierCalls {
		t.Fatalf("carrier after switching back = %q, want %q", got, assistant.CarrierCalls)
	}
}

// The value the settings document can hold is bounded by the declaration, so
// a run can never be driven by a method the product does not have. Asserted
// here rather than trusted: the engine refuses an unknown carrier
// (assistant.TestAsk_AnUnknownCarrierIsRefusedRatherThanGuessed), and this is
// the other end — nothing can put one there through the wire.
func TestAgentSettings_AnUnknownCarrierCannotBeStored(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	client := &scriptedAssistantClient{deltas: []string{"ok"}}
	h := newAskHarnessWithOpts(t, client,
		WithSettingsRegistry(reg), WithAgentPolicy(askPolicyStore(t)))

	if resp := jsonrpcCall(t, h.conn, "settings.set", map[string]any{
		"key": "assistant.carrier", "value": "interpretive-dance",
	}); !isErrorResponse(t, resp) {
		t.Fatalf("settings.set stored a method the product does not have: %s", resp)
	}
	if got, _ := reg.GetSelect(settings.AssistantCarrier); got != "calls" {
		t.Fatalf("the stored carrier is %q after a refused write", got)
	}
}
