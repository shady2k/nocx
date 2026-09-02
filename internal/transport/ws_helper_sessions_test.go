package transport

import (
	"context"
	"encoding/json"
	"testing"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
)

type fakeHostSessionInventory struct {
	entries []helperclient.SessionEntry
	err     error
}

func (f fakeHostSessionInventory) Sessions(context.Context) ([]helperclient.SessionEntry, error) {
	return f.entries, f.err
}

func contractInventoryEntry() helperclient.SessionEntry {
	return helperclient.SessionEntry{
		HostSessionID: helperclient.HostSessionID{Generation: "generation-a", Session: "0123456789abcdef0123456789abcdef"},
		Workspace:     "workspace-a",
		StartedAt:     "2026-08-31T21:00:00.123456789Z",
		Launch: helperclient.LaunchRecord{
			Shell: "/bin/bash", Cwd: "/srv", Pid: 41, Pgid: 41, Cols: 80, Rows: 24, WindowBytes: 262144,
		},
		// The process-status triple is POPULATED here on purpose. A fixture
		// that leaves a field at its zero value marshals nothing for it —
		// every one of them is omitempty — so the schema never sees the key
		// and a misspelt json tag passes the gate. That is the shape
		// vault.status's missing defaultProvider had.
		Observed: &helperclient.Observation{
			Source: "proc", Cwd: "/srv", Argv: []string{},
			StartTime: "2026-08-31T20:59:00.5Z", Ppid: 40, State: "sleeping",
			Unavailable: []string{},
		},
		Window:      helperclient.WindowSpan{Base: 0, Written: 12},
		Writer:      nil,
		WriterEpoch: 0,
		Exit:        nil,
	}
}

func TestSessionsInventory_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sessions.inventory.schema.json")
	entry := contractInventoryEntry()
	raw := mustMarshal(hostSessionInventoryResult{Sessions: []helperclient.SessionEntry{entry}})
	validateJSON(t, schema, raw, "sessions.inventory DTO")
}

func TestSessionsInventory_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sessions.inventory.schema.json")
	entry := contractInventoryEntry()
	h := newInventoryHarness(t, WithHostSessionInventory(fakeHostSessionInventory{entries: []helperclient.SessionEntry{entry}}))

	raw := jsonrpcCall(t, h.conn, "sessions.inventory", map[string]any{})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode sessions.inventory response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("sessions.inventory returned error: %s", raw)
	}
	validateJSON(t, schema, envelope.Result, "sessions.inventory over-the-wire result")
	var result hostSessionInventoryResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].HostSessionID.Generation != "generation-a" {
		t.Fatalf("sessions = %+v, want one generation-a entry", result.Sessions)
	}
	observed := result.Sessions[0].Observed
	if observed == nil || observed.Source != "proc" || observed.Cwd != "/srv" || observed.Argv == nil || len(observed.Argv) != 0 {
		t.Fatalf("observation = %+v, want proc /srv and an explicit empty argv", observed)
	}
	if observed.StartTime != "2026-08-31T20:59:00.5Z" || observed.Ppid != 40 || observed.State != "sleeping" {
		t.Fatalf("process-status triple = %q/%d/%q off the real socket, want the values the handler was given", observed.StartTime, observed.Ppid, observed.State)
	}
}

// TestSessionsInventory_UnavailableDiagnosticsConformToContract is the other
// half. The wire's `unavailable` vocabulary is a closed enum, so a helper that
// names a diagnostic the schema does not know produces a payload the renderer
// refuses — and the three added by nocx-k6p18.12 have to be in that set or an
// inspector that could not read /proc/<pid>/stat emits an invalid inventory.
func TestSessionsInventory_UnavailableDiagnosticsConformToContract(t *testing.T) {
	schema := loadSchema(t, "sessions.inventory.schema.json")
	entry := contractInventoryEntry()
	entry.Observed = &helperclient.Observation{
		Source: "proc", Argv: []string{},
		Unavailable: []string{"cwd", "argv", "foregroundCommand", "startTime", "ppid", "state"},
	}
	raw := mustMarshal(hostSessionInventoryResult{Sessions: []helperclient.SessionEntry{entry}})
	validateJSON(t, schema, raw, "sessions.inventory DTO naming every diagnostic unavailable")
}

func TestSessionsInventory_UnwiredIsUnavailable(t *testing.T) {
	h := newInventoryHarness(t)
	raw := jsonrpcCall(t, h.conn, "sessions.inventory", map[string]any{})
	var envelope struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != -32601 {
		t.Fatalf("error = %+v, want method unavailable", envelope.Error)
	}
}
