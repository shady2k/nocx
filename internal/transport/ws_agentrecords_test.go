package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/log"
)

func agentRecordsConnection(t *testing.T, store AgentRecordsStore) (*websocket.Conn, func()) {
	t.Helper()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithAgentRecords(store))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := connectWS(t, ws)
	return conn, func() { _ = conn.Close(); _ = ws.Stop(ctx) }
}

func TestAgentRecordsCRUD_RealSocketUsesTheLiveStoreAndExactContracts(t *testing.T) {
	store, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn, cleanup := agentRecordsConnection(t, store)
	defer cleanup()

	call := func(method string, params any, contract string) []byte {
		t.Helper()
		resp := jsonrpcCall(t, conn, method, params)
		var env rpcEnvelope
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("%s response: %v", method, err)
		}
		if env.Error != nil {
			t.Fatalf("%s: %+v", method, env.Error)
		}
		validateJSON(t, loadSchema(t, contract), env.Result, method+" wire")
		return env.Result
	}

	initial := call("agentRecords.list", map[string]any{}, "agentRecords.list.schema.json")
	var listed agentRecordsListResult
	if err := json.Unmarshal(initial, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Agents) != len(agentrecord.ShippedNames()) {
		t.Fatalf("initial agent count = %d, shipped = %d", len(listed.Agents), len(agentrecord.ShippedNames()))
	}

	params := map[string]any{
		"id": "custom-checker", "displayName": "Custom checker", "command": "/usr/bin/custom-checker",
		"args": []string{"--mode", "fast"}, "icon": "", "colour": "", "disabled": false,
		"env": []string{"MODE=fast"}, "resume": map[string]any{"sessionIdArgs": []string{}, "resumeIdArgs": []string{}, "resumeCwdArgs": []string{}},
	}
	var saved agentRecordsSavedResult
	if err := json.Unmarshal(call("agentRecords.save", params, "agentRecords.save.schema.json"), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Agent.ID != "custom-checker" || saved.Agent.Builtin || saved.Agent.Command != "/usr/bin/custom-checker" {
		t.Fatalf("saved row = %+v", saved.Agent)
	}
	if got := store.EnabledNames(); !containsAgentRecordName(got, "custom-checker") {
		t.Fatalf("next offering omitted newly saved record: %v", got)
	}

	params["id"] = "claude"
	params["displayName"] = "Claude edited"
	params["command"] = "/opt/claude"
	if err := json.Unmarshal(call("agentRecords.save", params, "agentRecords.save.schema.json"), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Agent.Builtin || saved.Agent.Command != "/opt/claude" {
		t.Fatalf("edited built-in = %+v", saved.Agent)
	}
	if err := json.Unmarshal(call("agentRecords.remove", map[string]string{"id": "custom-checker"}, "agentRecords.remove.schema.json"), &agentRecordsRemovedResult{}); err != nil {
		t.Fatal(err)
	}
	if got := store.EnabledNames(); containsAgentRecordName(got, "custom-checker") {
		t.Fatalf("removed agent remains offered: %v", got)
	}

	resp := jsonrpcCall(t, conn, "agentRecords.remove", map[string]string{"id": "claude"})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil {
		t.Fatalf("built-in remove answered %s, want refusal", env.Result)
	}
}

func TestAgentRecordsSaveRefusesMalformedDocumentWithoutReplacingCurrentRecord(t *testing.T) {
	store, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn, cleanup := agentRecordsConnection(t, store)
	defer cleanup()
	good := map[string]any{"id": "custom", "displayName": "", "command": "custom", "args": []string{}, "icon": "", "colour": "", "disabled": false, "env": []string{}, "resume": map[string]any{"sessionIdArgs": []string{}, "resumeIdArgs": []string{}, "resumeCwdArgs": []string{}}}
	if resp := jsonrpcCall(t, conn, "agentRecords.save", good); len(resp) == 0 {
		t.Fatal("empty save response")
	}
	good["command"] = ""
	resp := jsonrpcCall(t, conn, "agentRecords.save", good)
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil {
		t.Fatalf("empty command was accepted: %s", env.Result)
	}
	entry, ok := store.Entry("custom")
	if !ok || entry.Record.Command != "custom" {
		t.Fatalf("failed update replaced original record: %+v, known=%v", entry, ok)
	}
}

func TestAgentRecordsDTOConformsToContracts(t *testing.T) {
	row := agentRecordRow{ID: "agent", Builtin: false, State: agentrecord.StateUser, Problem: "", DisplayName: "Agent", Command: "agent", Args: []string{}, Icon: "", Colour: "", Disabled: false, Env: []string{}, Resume: agentRecordResumeDTO{SessionIDArgs: []string{}, ResumeIDArgs: []string{}, ResumeCwdArgs: []string{}}}
	for name, value := range map[string]any{
		"agentRecords.list.schema.json":   agentRecordsListResult{Agents: []agentRecordRow{row}},
		"agentRecords.save.schema.json":   agentRecordsSavedResult{Agent: row},
		"agentRecords.remove.schema.json": agentRecordsRemovedResult{Removed: true},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		validateJSON(t, loadSchema(t, name), raw, name+" DTO")
	}
}

func containsAgentRecordName(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// THE RECORD'S NON-LAUNCH WORDS SURVIVE A SETTINGS SAVE (nocx-3an1s.1.3).
//
// The document is replaced wholesale on save, so a field the form does not
// carry is a field an edit of another field deletes. `claude update` depends on
// this list, and the wrapper reads it from the record: losing it silently would
// put the tool-surface argument back on a maintenance command.
func TestAgentRecordsSaveKeepsTheNonLaunchSubcommands(t *testing.T) {
	store, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conn, cleanup := agentRecordsConnection(t, store)
	defer cleanup()

	params := map[string]any{
		"id": "custom", "displayName": "Custom", "command": "custom",
		"args": []string{}, "icon": "", "colour": "", "disabled": false,
		"env":                  []string{},
		"nonLaunchSubcommands": []string{"update", "update-config"},
		"resume":               map[string]any{"sessionIdArgs": []string{}, "resumeIdArgs": []string{}, "resumeCwdArgs": []string{}},
	}
	resp := jsonrpcCall(t, conn, "agentRecords.save", params)
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("save: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agentRecords.save.schema.json"), env.Result, "agentRecords.save wire")

	entry, ok := store.Entry("custom")
	if !ok {
		t.Fatal("saved record is absent from the store")
	}
	if got := entry.Record.NonLaunchSubcommands; len(got) != 2 || got[0] != "update" || got[1] != "update-config" {
		t.Fatalf("saved non-launch subcommands = %v", got)
	}

	// And the list answers with them, so the next Settings edit starts from
	// what is on disk rather than from nothing.
	listed := jsonrpcCall(t, conn, "agentRecords.list", map[string]any{})
	var listEnv rpcEnvelope
	if err := json.Unmarshal(listed, &listEnv); err != nil {
		t.Fatal(err)
	}
	validateJSON(t, loadSchema(t, "agentRecords.list.schema.json"), listEnv.Result, "agentRecords.list wire")
	var result agentRecordsListResult
	if err := json.Unmarshal(listEnv.Result, &result); err != nil {
		t.Fatal(err)
	}
	for _, row := range result.Agents {
		if row.ID == "custom" {
			if len(row.NonLaunchSubcommands) != 2 || row.NonLaunchSubcommands[0] != "update" {
				t.Fatalf("listed non-launch subcommands = %v", row.NonLaunchSubcommands)
			}
			return
		}
	}
	t.Fatal("the saved record is absent from the listing")
}
