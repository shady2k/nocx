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
	// File is the person's read path for one file of one discovered skill.
	// It takes no context because it reaches no network and no ctx-aware
	// seam: it is a bounded read of a local file the store has already
	// resolved.
	File(name, path string) (skill.FileResult, error)
	// Files is what that read path can be pointed AT: every file the skill
	// carries, as they are on disk now. Nothing else on the wire answers it
	// — skills.list answers with skills and skills.preview.files is
	// pre-install — so the card that design §8 requires could not be drawn
	// without it.
	Files(name string) (skill.FilesResult, error)
	// Audit composes the bundle one audit reads (skill/audit.go). It is on
	// this interface rather than one of its own because the settings
	// surface asks the library exactly one kind of question — what is this
	// skill — and a second interface over the same object would be a second
	// place to keep that answer. It reaches no network and no model: the
	// model call belongs to the engine, and this is only the bytes.
	Audit(name string) (skill.AuditMaterial, error)
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

// skillFileParams names one file of one skill. The path is relative to the
// skill's own directory; whether it stays there is the store's question and
// not this struct's, because containment has one owner (internal/skill's
// locate) and a bound checked twice is a bound that can disagree with itself.
type skillFileParams struct {
	Name string `json:"name"`
	Path string `json:"path"`
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
	case "skills.file":
		var p skillFileParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" || p.Path == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		// Only the refusals of the REQUEST arrive here — the file is gone,
		// the path leaves the skill, no root holds that name — and each is
		// already the store's own sentence, so it travels as the message.
		// The two refusals that describe a file which exists (it is not
		// text; it is larger than the read budget) are carried in the
		// RESULT instead, and the reasoning for that split is in
		// internal/skill/file.go where the decision is made.
		file, err := h.source.File(p.Name, p.Path)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(file))
	case "skills.files":
		var p skillRemoveParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		// A name no root holds, and an unreadable directory, are the store's
		// own sentences about a request there is nothing to describe for, so
		// each travels as the message. The one degrade that is NOT an error
		// is the cut: the list stops at the cap and the result says so, for
		// the reason file.go gives for its two carried refusals — a viewer
		// handed an error has no count and no cap to name.
		files, err := h.source.Files(p.Name)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(files))
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

// validateSkillFilesRaw is the name-only params shape a third time. It is a
// call through rather than a copy for validateSkillURLRaw's reason: three
// contracts declaring one field is one answer to what a skill-name param is,
// and three copies of the bound would agree until somebody widened one.
func validateSkillFilesRaw(raw json.RawMessage) string {
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

// validateSkillFileRaw bounds the wire request before the store is asked. The
// path bound is generous because a skill may nest reference material, and it
// is a bound on the WIRE only: what the path may point at is decided once, in
// internal/skill.
func validateSkillFileRaw(raw json.RawMessage) string {
	var p skillFileParams
	if msg := decodeObject(raw, &p, "name", "path"); msg != "" {
		return msg
	}
	if msg := boundedRunes("name", p.Name, 128); msg != "" {
		return msg
	}
	if msg := boundedRunes("path", p.Path, 1024); msg != "" {
		return msg
	}
	if p.Name == "" {
		return "name is required"
	}
	if p.Path == "" {
		return "path is required"
	}
	return ""
}
