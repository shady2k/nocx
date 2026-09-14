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
		Launch: &helperclient.LaunchRecord{
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

// contractSSHEntry is the OTHER branch of the launch union (nocx-s8mfn): a
// session whose process is a shell channel on a connection the helper dialed,
// which is what every ssh pane this machine opens is. It is the entry that
// reaches this contract for the first time now that the inventory asks this
// machine's daemon what it holds — until then the ssh branch was invented by
// the mapper and never crossed the wire.
//
// THE THREE ABSENCES ARE THE ASSERTION, and each is a different fact:
// `launch` is absent because there is no process on the helper's machine; a
// local record here would carry pid 0, which is the kernel's scheduler; and
// `observed` is null because the helper's inspector is never asked about a
// pid it does not have.
func contractSSHEntry() helperclient.SessionEntry {
	return helperclient.SessionEntry{
		HostSessionID: helperclient.HostSessionID{Generation: "generation-a", Session: "fedcba9876543210fedcba9876543210"},
		Workspace:     "workspace-a",
		StartedAt:     "2026-08-31T21:00:00.123456789Z",
		RemoteLaunch: &helperclient.RemoteLaunch{
			Host: "host.example", Port: 22, User: "dev", IdentityRef: "sec:screen-proof:1",
			Shell: "auto", Cwd: "", Cols: 80, Rows: 24, WindowBytes: 262144,
		},
		Observed:    nil,
		Window:      helperclient.WindowSpan{Base: 0, Written: 0},
		Writer:      nil,
		WriterEpoch: 0,
		Exit:        nil,
	}
}

func TestSessionsInventory_SSHBranchConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sessions.inventory.schema.json")
	raw := mustMarshal(hostSessionInventoryResult{Sessions: []helperclient.SessionEntry{contractSSHEntry()}})
	validateJSON(t, schema, raw, "sessions.inventory DTO carrying the ssh launch branch")
}

func TestSessionsInventory_SSHBranchOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "sessions.inventory.schema.json")
	entry := contractSSHEntry()
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
	validateJSON(t, schema, envelope.Result, "sessions.inventory ssh-branch result, off the real socket")

	var result hostSessionInventoryResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one entry", result.Sessions)
	}
	got := result.Sessions[0]
	if got.RemoteLaunch == nil || got.RemoteLaunch.Host != "host.example" {
		t.Fatalf("the ssh destination did not survive the wire: %+v", got.RemoteLaunch)
	}
	if got.Launch != nil {
		t.Fatalf("the ssh entry came back carrying a local launch record: %+v", *got.Launch)
	}
}

// TestSessionsInventory_LaunchUnionIsExact is the NEGATIVE half, and it is what
// makes the union more than a pair of optional fields.
//
// Both payloads below are things the DTO can be made to marshal, and both say
// something a reader cannot act on: an entry carrying two launch records (which
// machine is this process on?) and an entry carrying none (no process at all,
// of a session that is by definition running one). `oneOf` refuses both — the
// first matches both branches, the second matches neither — so the contract
// says exactly one, which is the fact every reader depends on.
func TestSessionsInventory_LaunchUnionIsExact(t *testing.T) {
	schema := loadSchema(t, "sessions.inventory.schema.json")

	both := contractSSHEntry()
	both.Launch = &helperclient.LaunchRecord{Shell: "/bin/bash", Pid: 41, Pgid: 41, Cols: 80, Rows: 24}
	if err := validateJSONErr(schema, mustMarshal(hostSessionInventoryResult{Sessions: []helperclient.SessionEntry{both}})); err == nil {
		t.Error("an entry carrying BOTH launch branches satisfied the contract, so nothing says which machine its process is on")
	}

	neither := contractInventoryEntry()
	neither.Launch = nil
	if err := validateJSONErr(schema, mustMarshal(hostSessionInventoryResult{Sessions: []helperclient.SessionEntry{neither}})); err == nil {
		t.Error("an entry carrying NO launch branch satisfied the contract, so a session that is running can be reported with no process at all")
	}

	// And the two honest shapes still do, so the refusals above are the union
	// and not a contract that refuses everything.
	validateJSON(t, schema, mustMarshal(hostSessionInventoryResult{Sessions: []helperclient.SessionEntry{contractSSHEntry()}}), "the ssh branch")
	validateJSON(t, schema, mustMarshal(hostSessionInventoryResult{Sessions: []helperclient.SessionEntry{contractInventoryEntry()}}), "the local branch")
}
