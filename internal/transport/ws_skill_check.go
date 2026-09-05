package transport

// skills.check — reading what is stored, and whether it still fits.
//
// skills.audit is the one skills.* method that spends a model call; this is
// the one that spends NOTHING. It answers what a card wants to know on
// opening — has this skill been checked, and if so, is that reading still
// about the bytes on disk — without ever resolving a role, unlocking the
// vault, or asking a model anything. It reads a row content.db may already
// hold and recomputes a digest to compare it against.
//
// THE CURRENCY COMPARISON LIVES HERE, DELIBERATELY, AND NOT ON skills.list.
// skills.list refreshes after every toggle, delete and approve
// (ws_skill_handlers.go), so a per-row recomposition there would put a
// directory walk on the hot path of every one of those refreshes — the same
// judgement internal/skill/files.go:13 already records about why a bundle's
// manifest is not a field on the list either. It would also be worse there
// than the cost of one walk suggests: content.db is single-connection,
// because the cipher enciphers whole 4096-byte blocks rather than a byte
// range (internal/content/sqlite.go:65, ADR-0043), so every row's currency
// check on every refresh would serialise behind every other read and write
// the store does. Opening a card is a rare event a person does once per
// card, not once per list refresh, and can afford what the list cannot.
//
// A STALE CHECK IS STILL THE CHECK. When the recomputed digest disagrees
// with the one on the row, this does not delete the row and does not fold
// it into "checked: false" — that would make "nobody has looked" and "the
// bytes moved since somebody looked" the same state on screen, and would
// throw away a reading the person already paid a model for. Only `current`
// moves.

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/skill"
)

// skillCheckResult is the wire shape (contracts/skills.check.schema.json).
type skillCheckResult struct {
	// Name is the skill as RESOLVED, the same convention skillAuditResult
	// keeps: a reader labels what it is describing, never the string that
	// was asked for.
	Name string `json:"name"`
	// Checked is false for three different reasons — nobody has run an
	// audit, this machine's store is absent or a stub, or the skill is a
	// builtin that skills.audit refuses before a role is even resolved —
	// and the wire deliberately does not distinguish them: all three are
	// one fact from the person's side ("there is nothing to show here"),
	// and a caller with three branches for one screen state is a caller
	// that draws three screens for it.
	Checked bool `json:"checked"`
	// Check and Current are present exactly when Checked is true —
	// enforced by the schema's allOf, not only by this comment.
	Check   *skillCheckDTO `json:"check,omitempty"`
	Current *bool          `json:"current,omitempty"`
}

// skillCheckDTO carries content.SkillCheck's fields on the wire.
//
// It is a second type rather than json tags on content.SkillCheck itself
// because content must not know it sits on a wire (AD-8: a store is a
// store, not a wire format), and because Omitted/Findings need the skill
// package's json-tagged shapes (skill.AuditOmission, skill.Finding) rather
// than content's untagged ones. checkOmissions/checkFindings below do that
// reconciliation for the read direction, mirroring what
// auditOmissions/auditFindings (ws_skill_audit.go) already do for the write.
// uncheckedSkillCheckResult is the "nothing to show here" result, built once
// rather than at each of its three call sites (a builtin, no store wired, a
// store with no row) — the three call sites differ in WHY there is nothing,
// which is exactly the distinction the wire deliberately does not carry, so
// there is exactly one literal for the one shape they all produce.
func uncheckedSkillCheckResult(name string) skillCheckResult {
	return skillCheckResult{Name: name, Checked: false}
}

type skillCheckDTO struct {
	Provenance string `json:"provenance"`
	Verdict    string `json:"verdict"`
	Report     string `json:"report"`
	Role       string `json:"role"`
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	// Digest is the check's OWN recorded digest — the bytes the model was
	// given when it ran — never the freshly recomputed one. `current` is
	// where the comparison's answer lives; this field is evidence of what
	// was checked, not a live fact.
	Digest    string                `json:"digest"`
	CheckedAt int64                 `json:"checkedAt"`
	Read      []string              `json:"read"`
	Omitted   []skill.AuditOmission `json:"omitted"`
	Findings  []skill.Finding       `json:"findings"`
	MaxBytes  int64                 `json:"maxBytes"`
}

type skillCheckHandlers struct {
	// source recomposes the bundle to resolve the name and provenance and
	// to recompute the digest the currency comparison needs — the same
	// interface skillAuditHandlers.source satisfies (ws_skill_audit.go),
	// reused rather than duplicated: recomposing a bundle is one behaviour
	// with one owner regardless of which method is asking for it.
	source skillAuditSource
	// checks is nil when no store is wired on this machine, and a stub
	// (content.Stub.SkillChecks()) when there is no content key — both
	// answer Checked:false with no error, which is the "store not there"
	// case skillAuditHandlers.checks already documents.
	checks skillCheckStore
	wired  bool
	r      Responder
}

func (h skillCheckHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "checking a skill is not available: no skill library is wired"})
		return
	}
	var p skillRemoveParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	// THE BYTES FIRST, same as skills.audit and for the same reason: the
	// resolved name, the resolved provenance and the digest to compare
	// against all come out of one recomposition, so there is exactly one
	// walk here rather than a second one to learn "is this a builtin" and
	// a third to learn "what does the disk say now". A name no root holds
	// is a refusal rather than an invented "checked: false": there is
	// nothing here to describe, the same reason skill.Audit itself refuses
	// rather than answering with an empty document.
	material, err := h.source.Audit(p.Name)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	// BUILTIN NEVER TOUCHES THE STORE. Its bytes came with nocx itself, and
	// skills.audit refuses to spend a model call on one before a role is
	// even resolved — so a builtin can never have a row, skills.audit being
	// the only writer. Answered here rather than by falling through to an
	// empty Get so that "a builtin was asked about" is never observable as
	// a store call at all.
	if material.Provenance == skill.ProvenanceBuiltin {
		_ = h.r.TryResult(req.ID, mustMarshal(uncheckedSkillCheckResult(material.Name)))
		return
	}

	if h.checks == nil {
		_ = h.r.TryResult(req.ID, mustMarshal(uncheckedSkillCheckResult(material.Name)))
		return
	}

	stored, found, getErr := h.checks.Get(ctx, material.Name)
	if getErr != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: getErr.Error()})
		return
	}
	if !found {
		_ = h.r.TryResult(req.ID, mustMarshal(uncheckedSkillCheckResult(material.Name)))
		return
	}

	// THE COMPARISON: material.Digest is fresh, out of the recomposition
	// above; stored.Digest is what the model was given when the check was
	// made. Equal means the bytes have not moved since; anything else — an
	// edit, a reinstall — and the check is still returned whole, only
	// `current` moves.
	current := stored.Digest == material.Digest
	_ = h.r.TryResult(req.ID, mustMarshal(skillCheckResult{
		Name:    material.Name,
		Checked: true,
		Check:   toSkillCheckDTO(stored),
		Current: &current,
	}))
}

// toSkillCheckDTO converts the store's shape to the wire's. See
// skillCheckDTO's doc comment for why the two are not one type.
func toSkillCheckDTO(c content.SkillCheck) *skillCheckDTO {
	return &skillCheckDTO{
		Provenance: c.Provenance,
		Verdict:    c.Verdict,
		Report:     c.Report,
		Role:       c.Role,
		Endpoint:   c.Endpoint,
		Model:      c.Model,
		Digest:     c.Digest,
		CheckedAt:  c.CheckedAt,
		Read:       nonNilSkillCheckReads(c.Read),
		Omitted:    checkOmissions(c.Omitted),
		Findings:   checkFindings(c.Findings),
		MaxBytes:   c.MaxBytes,
	}
}

// checkOmissions is auditOmissions' mirror for the read direction: content
// must not import skill (AD-8), so the two shapes are declared apart and
// reconciled here rather than the other way round.
func checkOmissions(in []content.SkillCheckOmission) []skill.AuditOmission {
	out := make([]skill.AuditOmission, 0, len(in))
	for _, o := range in {
		out = append(out, skill.AuditOmission{Path: o.Path, Reason: skill.AuditOmissionReason(o.Reason)})
	}
	return out
}

// checkFindings is auditFindings' mirror for the read direction, for the
// same reason checkOmissions is.
func checkFindings(in []content.SkillCheckFinding) []skill.Finding {
	out := make([]skill.Finding, 0, len(in))
	for _, f := range in {
		out = append(out, skill.Finding{Path: f.Path, LineNumber: f.LineNumber, Line: f.Line, PatternID: f.PatternID})
	}
	return out
}

// nonNilSkillCheckReads turns a nil Read slice into []. content.db's own Get
// already normalises this on its shipped read path (skill_check_sqlite.go);
// this exists for the shape a test fake can still hand back — one built
// directly against a content.SkillCheck literal, which nils an unset slice
// rather than normalising it the way the sqlite writer does on its way out.
func nonNilSkillCheckReads(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// validateSkillCheckRaw is the name-only params shape a fifth time, and a
// call through rather than a copy for validateSkillFilesRaw's reason: five
// contracts declaring one field is one answer to what a skill-name param
// is, and five copies of the bound would agree until somebody widened one.
func validateSkillCheckRaw(raw json.RawMessage) string {
	return validateSkillRemoveRaw(raw)
}
