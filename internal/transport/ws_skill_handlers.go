package transport

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/skill"
)

type skillSettingsSource interface {
	List() (skill.ListResult, error)
	SetEnabled(name string, enabled bool) error
	Remove(name string) error
	Approve(name string) error
	// File is the person's read path for one file of one discovered skill.
	// It takes no context because it reaches no network and no ctx-aware
	// seam: it is a bounded read of a local file the store has already
	// resolved.
	File(name, path string) (skill.FileResult, error)
	// Files is what that read path can be pointed AT: every file the skill
	// carries, as they are on disk now. Nothing else on the wire answers it
	// — skills.list answers with skills, not with their contents — so the
	// card that design §8 requires could not be drawn without it.
	Files(name string) (skill.FilesResult, error)
	// Audit composes the bundle one audit reads (skill/audit.go). It is on
	// this interface rather than one of its own because the settings
	// surface asks the library exactly one kind of question — what is this
	// skill — and a second interface over the same object would be a second
	// place to keep that answer. It reaches no network and no model: the
	// model call belongs to the engine, and this is only the bytes.
	Audit(name string) (skill.AuditMaterial, error)
	// Scan is the static scan's own answer for one discovered skill
	// (scan_skill.go): which files matched, how many patterns each one
	// matched, and which files it could not read. It is deliberately its
	// OWN method rather than a field Files also fills — Files stays a bare
	// directory listing so a person's file list renders before this answers
	// (nocx-4m1n1's own review: folding the scan into Files made the list
	// wait on reading and scanning the whole bundle first).
	Scan(name string) (skill.ScanResult, error)
}

type skillSetEnabledParams struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type skillRemoveParams struct {
	Name string `json:"name"`
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
	// checks is skills.list's read of what a model already concluded — the
	// same skillCheckStore skills.audit writes to and skills.check reads
	// from (ws_skill_audit.go, ws_skill_check.go). Nil on a machine with no
	// content.db wired, in which case every row simply carries no check, the
	// same "nothing to show here" skills.check already answers with. Set
	// only on the skills.list registration (ws_config_handlers.go): the
	// other methods on this handler never read a check.
	checks skillCheckStore
	// log is where a Get failure on one row goes when it must not take the
	// whole list down with it (see withStoredChecks). Nil is a valid value —
	// tests that build this handler directly need not supply one — and the
	// degrade happens either way; only the trail differs.
	log   log.Logger
	wired bool
	r     Responder
}

// skillsListCheck is one row's summary of a stored check: a date, a verdict
// and a model — exactly what a person can act on regardless of what the
// bytes are now. Currency (whether those bytes still match) is deliberately
// NOT here: skills.check already refuses to put a digest recomputation on
// this hot path (ws_skill_check.go's header comment), because skills.list
// refreshes after every toggle, delete and approve and content.db is
// single-connection — the cipher enciphers whole 4096-byte blocks rather
// than a byte range (internal/content/sqlite.go:65, ADR-0043) — and that is
// the identical judgement internal/skill/files.go:13 already records about
// why a bundle's manifest is not a field on the list either. A recomputation
// belongs to the rare event (opening one card), not the frequent one
// (refreshing all of them).
type skillsListCheck struct {
	At      string `json:"at"`
	Verdict string `json:"verdict"`
	Model   string `json:"model"`
}

// skillsListEntry is skill.ListedSkill plus what content knows about it.
// Declared here, never as a field on ListedSkill itself, because
// content.SkillCheck is content's concept and skill must not import content
// (AD-8) — the same boundary skillCheckDTO draws for skills.check
// (ws_skill_check.go).
type skillsListEntry struct {
	skill.ListedSkill
	// Check is nil — no key at all on the wire, via omitempty — for a skill
	// nobody has run skills.audit against. An empty object would render as a
	// row saying something about a check that does not exist.
	Check *skillsListCheck `json:"check,omitempty"`
}

// skillsListResult is skill.ListResult with each row's check attached.
type skillsListResult struct {
	Skills []skillsListEntry `json:"skills"`
	// Refused travels straight through: a refused directory has no check to
	// attach and nothing to enable, and it is on the wire so a person can
	// SEE the directory nocx would not index (nocx-j0lei). Never nil — the
	// contract requires the key, and an absent array and an empty one would
	// be two ways to say nothing was refused.
	Refused       []skill.Refusal `json:"refused"`
	DocumentPath  string          `json:"documentPath"`
	DocumentError string          `json:"documentError,omitempty"`
}

// withStoredChecks attaches each row's check by name. One content.db read
// per row (content.SkillCheckRepository.Get), never a filesystem walk: the
// walk skills.list performs is the one discoverDetailed already did inside
// source.List() above, and Get reaches no filesystem at all — it is not the
// method that composes a bundle or recomputes a digest (that one is
// skillSettingsSource.Audit, which this function never calls, and
// TestSkillsListDoesNotRecomputeAnyBundleDigest asserts it stays that way).
//
// A BUILTIN IS NEVER ASKED. skills.audit refuses to check one before a role
// is even resolved, so a builtin can never have a row in the store —
// ws_skill_check.go's handler makes the identical call ("never observable as
// a store call at all") for the same reason. Skipping it here keeps that one
// judgement owned in one place instead of two files agreeing by accident,
// and it removes a guaranteed-miss read per builtin on every refresh.
//
// A GET FAILURE DEGRADES THE ROW, NOT THE PAGE. checks == nil (no content.db
// wired) already answers "no check" without an error a few lines below; a
// wired store that fails to answer for one row must not disagree with that
// by taking the whole list down — toggles, deletes and approvals travel on
// this same method. The check is a RECORD of a past reading; the list is the
// CONTROL SURFACE a person operates the library through, and a record being
// unreadable must never cost them their switch. So the row loses only its
// check, and the failure is logged rather than swallowed in silence.
func withStoredChecks(ctx context.Context, result skill.ListResult, checks skillCheckStore, logger log.Logger) skillsListResult {
	out := skillsListResult{
		Skills:        make([]skillsListEntry, 0, len(result.Skills)),
		Refused:       result.Refused,
		DocumentPath:  result.DocumentPath,
		DocumentError: result.DocumentError,
	}
	if out.Refused == nil {
		out.Refused = []skill.Refusal{}
	}
	for _, listed := range result.Skills {
		entry := skillsListEntry{ListedSkill: listed}
		if checks != nil && listed.Provenance != skill.ProvenanceBuiltin {
			// SEEN AND LEFT: Get returns the whole stored check — report
			// prose, every read path, every omission and finding — to build
			// three scalars from it, on a store whose cipher enciphers
			// whole blocks per read (ADR-0043). Narrowing this needs new
			// interface surface on skillCheckStore, and that is worth
			// measuring before adding rather than guessing at up front.
			stored, found, err := checks.Get(ctx, listed.Name)
			switch {
			case err != nil:
				if logger != nil {
					logger.Warn("skills.list: could not read the stored check for a row; the row carries no check rather than the list failing",
						"skill", listed.Name, "error", err)
				}
			case found:
				entry.Check = &skillsListCheck{
					At:      checkTimeRFC3339(stored.CheckedAt),
					Verdict: stored.Verdict,
					Model:   stored.Model,
				}
			}
		}
		out.Skills = append(out.Skills, entry)
	}
	return out
}

// checkTimeRFC3339 renders a content.db unix-millis timestamp the way every
// skills.* wire result now names a check's time. skills.check's own
// checkedAt used to travel as the bare millis integer; unified here so the
// row (skills.list) and the card (skills.check) never show two
// representations, and so two callers never format the same value two ways.
func checkTimeRFC3339(unixMillis int64) string {
	return time.UnixMilli(unixMillis).UTC().Format(time.RFC3339)
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
		_ = h.r.TryResult(req.ID, mustMarshal(withStoredChecks(ctx, result, h.checks, h.log)))
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
	case "skills.scan":
		var p skillRemoveParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		// A name no root holds is the store's own sentence about a
		// request there is nothing to describe for -- skills.files'
		// reason, unchanged. A file the SCAN could not read is not one
		// of these: it is named in the result's own `omitted`, the same
		// split skills.audit already makes.
		scanned, err := h.source.Scan(p.Name)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(scanned))
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

func validateSkillScanRaw(raw json.RawMessage) string {
	return validateSkillRemoveRaw(raw)
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
