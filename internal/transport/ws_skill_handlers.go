package transport

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/skill"
)

type skillSettingsSource interface {
	List() (skill.ListResult, error)
	SetEnabled(name string, enabled bool) error
	Remove(name string) error
	Approve(name string) error
	// Preview is the only method here that reaches the network, and it is
	// person-initiated: it fetches the document at a URL, parses it, refuses
	// what it must, scans it and answers. It writes nothing, so a refusal
	// leaves the library exactly as it was.
	Preview(ctx context.Context, url string) (skill.PreviewResult, error)
}

type skillSetEnabledParams struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type skillRemoveParams struct {
	Name string `json:"name"`
}

type skillPreviewParams struct {
	URL string `json:"url"`
}

type skillSettingsHandlers struct {
	source skillSettingsSource
	wired  bool
	r      Responder
}

func (h skillSettingsHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "skills not available"})
		return
	}
	switch req.Method {
	case "skills.list":
		result, err := h.source.List()
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(result))
	case "skills.setEnabled":
		var p skillSetEnabledParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		if err := h.source.SetEnabled(p.Name, p.Enabled); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(map[string]any{"name": p.Name, "enabled": p.Enabled}))
	case "skills.remove":
		var p skillRemoveParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		if err := h.source.Remove(p.Name); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(map[string]string{"name": p.Name}))
	case "skills.preview":
		var p skillPreviewParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.URL == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		// The refusal is the person's to read and already names the step
		// that refused, so it travels as the message rather than being
		// replaced by a transport sentence about an internal error.
		result, err := h.source.Preview(ctx, p.URL)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(result))
	case "skills.approve":
		var p skillRemoveParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		if err := h.source.Approve(p.Name); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(map[string]string{"name": p.Name, "status": string(skill.StatusApproved)}))
	}
}

func validateSkillSetEnabledRaw(raw json.RawMessage) string {
	var p skillSetEnabledParams
	if msg := decodeObject(raw, &p, "name", "enabled"); msg != "" {
		return msg
	}
	if msg := boundedRunes("name", p.Name, 128); msg != "" {
		return msg
	}
	if p.Name == "" {
		return "name is required"
	}
	return ""
}

func validateSkillRemoveRaw(raw json.RawMessage) string {
	var p skillRemoveParams
	if msg := decodeObject(raw, &p, "name"); msg != "" {
		return msg
	}
	if msg := boundedRunes("name", p.Name, 128); msg != "" {
		return msg
	}
	if p.Name == "" {
		return "name is required"
	}
	return ""
}

func validateSkillApproveRaw(raw json.RawMessage) string {
	return validateSkillRemoveRaw(raw)
}

func validateSkillPreviewRaw(raw json.RawMessage) string {
	var p skillPreviewParams
	if msg := decodeObject(raw, &p, "url"); msg != "" {
		return msg
	}
	if msg := boundedRunes("url", p.URL, 2048); msg != "" {
		return msg
	}
	if p.URL == "" {
		return "url is required"
	}
	return ""
}
