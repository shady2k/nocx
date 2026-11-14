package transport

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/note"
)

func TestAgentAsk_BareTextCreatesAndSearchesNoteThroughProvider(t *testing.T) {
	const article = "Debian 11 LTS reaches end of life in June 2024."
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	noteStore, err := note.Open(t.Context(), note.Config{
		Path: filepath.Join(t.TempDir(), "notes.db"),
		Key:  key,
	})
	if err != nil {
		t.Fatalf("note.Open: %v", err)
	}
	t.Cleanup(func() { _ = noteStore.Close() })
	var nextID atomic.Int64
	noteSvc := note.NewService(noteStore, func() string {
		return fmt.Sprintf("bare-note-%d", nextID.Add(1))
	}, nil)

	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepTool, Name: "notes.create", Arguments: map[string]any{"body": article}},
		{Kind: protocol.StepAnswer, Text: "saved the text as a note"},
		{Kind: protocol.StepEnd},
	}},
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithNotes(noteSvc),
	)
	sid := openLocalSession(t, h.conn)
	result, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "bare-text-note",
		"sessionId": sid,
		"question":  article,
		"cwd":       "/repo",
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	if result.State != "prepared" {
		t.Fatalf("agent.ask state = %q, want prepared", result.State)
	}
	stateRaw := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		t.Fatalf("runState: %v", err)
	}
	if state.State != string(content.RunCompleted) {
		t.Fatalf("runState = %s, want completed", stateRaw)
	}

	if got := h.toolResult(); !strings.Contains(got, "bare-note-1") {
		t.Fatalf("agent received note result = %q, want the created note", got)
	}

	listRaw := jsonrpcCallWithID(t, h.conn, "notes.list", map[string]any{}, 3)
	var listed struct {
		Result struct {
			Notes []struct {
				ID string `json:"id"`
			} `json:"notes"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(listRaw, &listed); err != nil {
		t.Fatalf("notes.list: %v", err)
	}
	if listed.Error != nil {
		t.Fatalf("notes.list: %+v", listed.Error)
	}
	if len(listed.Result.Notes) != 1 || listed.Result.Notes[0].ID != "bare-note-1" {
		t.Fatalf("notes.list = %+v, want the created note", listed.Result.Notes)
	}

	searchRaw := jsonrpcCallWithID(t, h.conn, "notes.search", map[string]any{"query": "Debian 11 LTS"}, 4)
	var searched struct {
		Result struct {
			Matches []struct {
				ID      string `json:"id"`
				Excerpt string `json:"excerpt"`
			} `json:"matches"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(searchRaw, &searched); err != nil {
		t.Fatalf("notes.search: %v", err)
	}
	if searched.Error != nil {
		t.Fatalf("notes.search: %+v", searched.Error)
	}
	if len(searched.Result.Matches) != 1 || searched.Result.Matches[0].ID != "bare-note-1" {
		t.Fatalf("notes.search = %+v, want the created note", searched.Result.Matches)
	}
	if !strings.Contains(searched.Result.Matches[0].Excerpt, "Debian 11 LTS") {
		t.Fatalf("notes.search excerpt = %q, want the article text", searched.Result.Matches[0].Excerpt)
	}
}
