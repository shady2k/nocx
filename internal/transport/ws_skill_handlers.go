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
	// Preview is one of the two methods here that reach the network, and both
	// are person-initiated: it fetches the document at a URL, parses it,
	// refuses what it must, scans it and answers. It writes nothing, so a
	// refusal leaves the library exactly as it was.
	Preview(ctx context.Context, url string) (skill.PreviewResult, error)
	// Install adopts the document at a URL the person has just read. It
	// fetches it AGAIN and compares against what the preview showed, because
	// a body that made a round trip through the renderer is a body that could
	// have changed on the way back — which is why the params carry the
	// address and nothing else.
	Install(ctx context.Context, url string) (skill.InstallResult, error)
}

type skillSetEnabledParams struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type skillRemoveParams struct {
	Name string `json:"name"`
}

// skillURLParams is the params shape of both network-reaching skill methods:
// one address and nothing else. It is one struct rather than two identical
// ones, because "install what you just read" is expressed by the two methods
// naming the SAME address, and a second shape would invite a second field.
type skillURLParams struct {
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
		var p skillURLParams
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
	case "skills.install":
		var p skillURLParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.URL == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		// Same as the preview: the refusal already names the step that
		// refused and what state the library was left in, so it travels as
		// the message rather than being replaced by a transport sentence
		// about an internal error.
		installed, err := h.source.Install(ctx, p.URL)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(installed))
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

// validateSkillURLRaw is registered for both skills.preview and
// skills.install, because both contracts declare the same one field and a
// second copy of this function would be a second answer to what an address
// param is.
func validateSkillURLRaw(raw json.RawMessage) string {
	var p skillURLParams
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
