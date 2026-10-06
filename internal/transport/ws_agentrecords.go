// Agent launch records are the one description consumed by wrappers and the
// coordinator. These methods let Settings edit that existing owner rather
// than keeping a renderer-side list (nocx-h64wy).
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// AgentRecordsStore is the settings seam over the live agent record owner.
type AgentRecordsStore interface {
	Entry(id string) (agentrecord.Entry, bool)
	List() ([]agentrecord.Entry, error)
	Save(id string, doc agentrecord.Document) error
	Remove(id string) error
}

type agentRecordResumeDTO struct {
	SessionIDArgs []string `json:"sessionIdArgs"`
	ResumeIDArgs  []string `json:"resumeIdArgs"`
	ResumeCwdArgs []string `json:"resumeCwdArgs"`
}

type agentRecordRow struct {
	ID          string               `json:"id"`
	Builtin     bool                 `json:"builtin"`
	State       agentrecord.State    `json:"state"`
	Problem     string               `json:"problem"`
	DisplayName string               `json:"displayName"`
	Command     string               `json:"command"`
	Args        []string             `json:"args"`
	Icon        string               `json:"icon"`
	Colour      string               `json:"colour"`
	Disabled    bool                 `json:"disabled"`
	Env         []string             `json:"env"`
	Resume      agentRecordResumeDTO `json:"resume"`
}

type agentRecordsListResult struct {
	Agents []agentRecordRow `json:"agents"`
}
type agentRecordsSaveParams struct {
	ID          string             `json:"id"`
	DisplayName string             `json:"displayName"`
	Command     string             `json:"command"`
	Args        []string           `json:"args"`
	Icon        string             `json:"icon"`
	Colour      string             `json:"colour"`
	Disabled    bool               `json:"disabled"`
	Env         []string           `json:"env"`
	Resume      agentrecord.Resume `json:"resume"`
}
type agentRecordsRemoveParams struct {
	ID string `json:"id"`
}
type agentRecordsSavedResult struct {
	Agent agentRecordRow `json:"agent"`
}
type agentRecordsRemovedResult struct {
	Removed bool `json:"removed"`
}

func validateAgentRecordsSave(raw json.RawMessage) string {
	var p agentRecordsSaveParams
	if msg := decodeObject(raw, &p, "id", "displayName", "command", "args", "icon", "colour", "disabled", "env", "resume"); msg != "" {
		return msg
	}
	if msg := requireAgentRecordFields(raw, "id", "displayName", "command", "args", "icon", "colour", "disabled", "env", "resume"); msg != "" {
		return msg
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "params must be a JSON object"
	}
	if msg := requireAgentRecordFields(object["resume"], "sessionIdArgs", "resumeIdArgs", "resumeCwdArgs"); msg != "" {
		return "resume " + msg
	}
	if p.ID == "" {
		return "id is required"
	}
	if !storage.ValidDocumentName(p.ID) {
		return "id is not a valid agent name"
	}
	if p.Command == "" {
		return "command is required"
	}
	return ""
}

func requireAgentRecordFields(raw json.RawMessage, fields ...string) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return "params must be a JSON object"
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok {
			return field + " is required"
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return field + " must not be null"
		}
	}
	return ""
}

func validateAgentRecordsRemove(raw json.RawMessage) string {
	var p agentRecordsRemoveParams
	if msg := decodeObject(raw, &p, "id"); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	if !storage.ValidDocumentName(p.ID) {
		return "id is not a valid agent name"
	}
	return ""
}

type agentRecordsHandlers struct {
	store AgentRecordsStore
	wired bool
	r     Responder
}

func (s *WSServer) agentRecordsSpecs() []methodSpec {
	return []methodSpec{
		regResponder(s.lane, "agentRecords.list", noParams(), func(r Responder) handlerFunc {
			h := agentRecordsHandlers{store: s.agentRecords, wired: s.agentRecords != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleList(ctx, req) }
		}),
		regResponder(s.lane, "agentRecords.save", params(validateAgentRecordsSave), func(r Responder) handlerFunc {
			h := agentRecordsHandlers{store: s.agentRecords, wired: s.agentRecords != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSave(ctx, req) }
		}),
		regResponder(s.lane, "agentRecords.remove", params(validateAgentRecordsRemove), func(r Responder) handlerFunc {
			h := agentRecordsHandlers{store: s.agentRecords, wired: s.agentRecords != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleRemove(ctx, req) }
		}),
	}
}

func (h agentRecordsHandlers) unavailable(req jsonrpcRequest, method string) bool {
	if h.wired {
		return false
	}
	_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: method + " not available"})
	return true
}

func (h agentRecordsHandlers) handleList(ctx context.Context, req jsonrpcRequest) {
	if h.unavailable(req, "agentRecords.list") {
		return
	}
	entries, err := h.store.List()
	if err != nil {
		log.From(ctx).Error("could not read agent records", "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32000, Message: "could not read agent records: " + fmt.Sprint(err)})
		return
	}
	rows := make([]agentRecordRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, rowFromEntry(entry))
	}
	_ = h.r.TryResult(req.ID, mustMarshal(agentRecordsListResult{Agents: rows}))
}

func (h agentRecordsHandlers) handleSave(_ context.Context, req jsonrpcRequest) {
	if h.unavailable(req, "agentRecords.save") {
		return
	}
	var p agentRecordsSaveParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	doc := agentrecord.Document{
		DisplayName: p.DisplayName, Command: p.Command, Args: nonNilStrings(p.Args),
		Icon: p.Icon, Colour: p.Colour, Disabled: p.Disabled, Env: nonNilStrings(p.Env), Resume: p.Resume,
	}
	if err := h.store.Save(p.ID, doc); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: fmt.Sprintf("agent record was not saved: %s", err)})
		return
	}
	entry, ok := h.store.Entry(p.ID)
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32000, Message: "agent record was saved, but is absent from the record store"})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(agentRecordsSavedResult{Agent: rowFromEntry(entry)}))
}

func (h agentRecordsHandlers) handleRemove(ctx context.Context, req jsonrpcRequest) {
	if h.unavailable(req, "agentRecords.remove") {
		return
	}
	var p agentRecordsRemoveParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	if err := h.store.Remove(p.ID); err != nil {
		log.From(ctx).Error("agent record was not removed", "agent_id", p.ID, "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "agent record was not removed: " + fmt.Sprint(err)})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(agentRecordsRemovedResult{Removed: true}))
}

func rowFromEntry(entry agentrecord.Entry) agentRecordRow {
	r := entry.Record
	return agentRecordRow{
		ID: entry.ID, Builtin: entry.Builtin, State: entry.State, Problem: entry.Problem,
		DisplayName: r.DisplayName, Command: r.Command, Args: nonNilStrings(r.Args), Icon: r.Icon,
		Colour: r.Colour, Disabled: r.Disabled, Env: nonNilStrings(r.Env),
		Resume: agentRecordResumeDTO{SessionIDArgs: nonNilStrings(r.Resume.SessionIDArgs), ResumeIDArgs: nonNilStrings(r.Resume.ResumeIDArgs), ResumeCwdArgs: nonNilStrings(r.Resume.ResumeCwdArgs)},
	}
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
