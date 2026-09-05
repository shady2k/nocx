package transport

// skills.audit — the reading a person asks for (design §7).
//
// The opposite shape to the install-time classifier §4 killed. That one ran
// on bytes nobody had asked about, gated nothing, and certified nothing while
// looking as though it did. This one is pressed by the person, about a skill
// they already hold, and produces a verdict and prose they act on
// themselves — design §7's refusal to conclude is reversed, and what
// replaces it as the defence is inertness (see skillAuditResult below).
//
// IT IS A BUTTON AND NOT A PAGE LOAD, and that is a decision the wire
// enforces rather than a habit the renderer keeps: opening a card is
// skills.list, skills.files and skills.file, none of which reaches a model,
// and this is a method of its own that nothing else calls. role.go's rule —
// an unassigned role must not spend money silently — has the same shape and
// the same reason.
//
// WHAT IT DOES NOT DO. Nothing in this file writes. The result changes no
// switch, no digest and no status; what the assistant is offered is still
// `enabled && !changed` computed by the store, and the audit touches neither
// term. That is asserted rather than assumed — see
// TestSkillsAudit_ChangesNothingAboutWhatTheAssistantMayDo, which scripts the
// model to say "enable it" and compares the whole of skills.list before and
// after.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/skill"
)

// skillAuditSource is what an audit asks of the skill library: the bundle,
// composed once, by the same walk and the same containment every other read
// of a skill goes through.
type skillAuditSource interface {
	Audit(name string) (skill.AuditMaterial, error)
}

// skillAuditEngine is the model call. It is a narrow interface over
// assistant.Client rather than the whole of it because this handler has no
// business being able to start an ask.
type skillAuditEngine interface {
	AuditSkill(ctx context.Context, p assistant.SkillAuditParams) (assistant.SkillReading, error)
}

// skillAuditResult is the wire shape (contracts/skills.audit.schema.json).
//
// IT CARRIES A VERDICT AND THE VERDICT DECIDES NOTHING — design §7's refusal
// to conclude is reversed, and what replaces it as the defence is that the
// verdict is inert. Every other field is one of three things: a fact about
// the REQUEST (which skill, which root), a fact about the CALL (which role
// answered, on which endpoint and model), or a fact about what was READ (the
// paths, the omissions, the budget, and the scan's own matches). The verdict
// alone is an opinion about the skill, and nothing here lets it become more
// than that: what the assistant is offered is still Skill.Offered() — the
// person's switch and the digest comparison — and this call touches neither,
// which is exactly what an install classifier's `risk` field failed to keep
// true and what §4 removed it for.
//
// Report is ONE prose field on purpose. The obvious alternative was three —
// what it instructs, what it reaches for, the findings in context — and it
// was rejected because a form with slots is a form a surface can read: an
// empty third box says "nothing found", which is a verdict wearing a
// layout. The three questions are asked in the PROMPT instead, where they
// shape the answer without becoming a schema.
type skillAuditResult struct {
	// Name and Provenance are the skill as RESOLVED, never the string that
	// was asked for: a reader labels what it is describing, and the two
	// differ exactly when two roots hold one name.
	Name       string           `json:"name"`
	Provenance skill.Provenance `json:"provenance"`
	// Role is which role's model actually answered — "auditing", or
	// "answering" when the auditing role has no assignment and the audit
	// fell back to it. It is a fact about the CALL and it travels because
	// role.go forbids spending the person's money on a model they did not
	// choose without saying so. A boolean "usedFallback" was rejected: it
	// names a comparison rather than the thing, and the surface would have
	// had to know what it was being compared to.
	Role string `json:"role"`
	// Endpoint and Model are the resolved pair, so the note the surface
	// draws can name what it billed. Model is the RESOLVED id and never a
	// self-report from the answer, which is classifier.go's rule about the
	// same fact.
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	// Verdict is the model's conclusion, in the closed vocabulary the parser
	// enforces exactly (assistant.SkillVerdict). IT DECIDES NOTHING — see the
	// type's doc comment for what keeps that true.
	Verdict string `json:"verdict"`
	// Report is the auditing model's prose, verbatim and bounded.
	Report string `json:"report"`
	// Read, Omitted and MaxBytes are what the reading is ABOUT. They travel
	// because a report on a subset the reader cannot identify reads exactly
	// like a report on the whole skill.
	Read     []string              `json:"read"`
	Omitted  []skill.AuditOmission `json:"omitted"`
	MaxBytes int                   `json:"maxBytes"`
	// Findings are OUR scan's matches over exactly the bytes that were sent,
	// each named with the file it matched in. They are here rather than left
	// to the model's prose because a line number a model reported would be a
	// self-report about a document only it can see; these are checkable
	// against skills.file, which is what makes the prose beside them worth
	// reading.
	Findings []skill.Finding `json:"findings"`
}

type skillAuditHandlers struct {
	source      skillAuditSource
	engine      skillAuditEngine
	configOp    capability.ConfigOperation
	credentials credential.Resolver
	log         log.Logger
	wired       bool
	r           Responder
}

func (h skillAuditHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "auditing a skill is not available: no skill library or no assistant engine is wired"})
		return
	}
	var p skillRemoveParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	// THE BYTES FIRST, and deliberately before the model is resolved. A
	// skill that vanished between the card opening and the button being
	// pressed must cost nothing at all — resolving a role and unlocking a
	// vault to then discover there is nothing to read would spend the
	// person's attention on a call that could never have answered.
	material, err := h.source.Audit(p.Name)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	role, endpoint, model, key, headers, err := h.resolveAuditModel(ctx)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	reading, err := h.engine.AuditSkill(ctx, assistant.SkillAuditParams{
		Key: key, BaseURL: endpoint.BaseURL, Model: model, Headers: headers,
		Document: material.Document,
	})
	if err != nil {
		// The engine's sentence travels. A reading that did not happen is a
		// refusal the person reads, never an empty report — an empty report
		// is indistinguishable from a clean one, which is the whole reason
		// this feature refuses to certify anything.
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}
	if h.log != nil {
		// What it COST and what it read, never the report: the prose is a
		// stranger's document described by a model, and a log is not where
		// that belongs.
		h.log.Info("skill: audit answered",
			"skill", material.Name, "role", string(role), "model", model,
			"files", len(material.Read), "omitted", len(material.Omitted),
			"findings", len(material.Findings))
	}
	_ = h.r.TryResult(req.ID, mustMarshal(skillAuditResult{
		Name:       material.Name,
		Provenance: material.Provenance,
		Role:       string(role),
		Endpoint:   endpoint.Name,
		Model:      model,
		Verdict:    string(reading.Verdict),
		Report:     reading.Report,
		Read:       material.Read,
		Omitted:    material.Omitted,
		MaxBytes:   material.MaxBytes,
		Findings:   material.Findings,
	}))
}

// resolveAuditModel resolves the auditing role, falling back to the answering
// role when — and only when — the auditing role has no assignment and no
// default stands behind it.
//
// The fallback is HERE and not inside profile.ResolveRole, which is the
// split role.go insists on: the resolver refuses, and a consumer that has a
// reason to spend somebody else's endpoint says so out loud. Every other
// refusal — an endpoint that was deleted, a model an endpoint no longer
// offers — travels unchanged, because those are the person being told what
// disappeared and repairing them into a neighbour is the silent provider
// change role.go forbids.
func (h skillAuditHandlers) resolveAuditModel(ctx context.Context) (
	profile.ModelRole, profile.Endpoint, string, credential.Secret, []assistant.Header, error,
) {
	if h.configOp == nil {
		return "", profile.Endpoint{}, "", credential.Secret{}, nil,
			errors.New("skill audit: no endpoint store is wired, so no model can be resolved")
	}
	var (
		role     = profile.RoleAuditing
		endpoint profile.Endpoint
		model    string
		key      credential.Secret
		headers  []assistant.Header
	)
	err := h.configOp.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		ep, m, resolveErr := svc.ResolveRole(profile.RoleAuditing)
		if errors.Is(resolveErr, profile.ErrRoleUnassigned) {
			role = profile.RoleAnswering
			ep, m, resolveErr = svc.ResolveRole(profile.RoleAnswering)
			if errors.Is(resolveErr, profile.ErrRoleUnassigned) {
				// Neither role and no default: there is no model on this
				// machine to read anything with, and the sentence says what
				// to do about it rather than repeating the resolver's.
				return errors.New("no model is assigned to the auditing role, and none to the answering role either — an audit is a model call, so assign one under Model roles in Settings first")
			}
		}
		if resolveErr != nil {
			return fmt.Errorf("skill audit: %w", resolveErr)
		}
		// "audit a skill" rather than "answer the ask": this string is what
		// the vault shows a person when it raises an unlock, and an unlock
		// prompt that named the wrong reason would be the product lying
		// about why it wants a key.
		k, hs, materialErr := resolveEndpointMaterial(ctx, h.credentials, ep, credential.Operation("audit a skill"))
		if materialErr != nil {
			return materialErr
		}
		endpoint, model, key, headers = ep, m, k, hs
		return nil
	})
	if err != nil {
		return "", profile.Endpoint{}, "", credential.Secret{}, nil, err
	}
	return role, endpoint, model, key, headers, nil
}

// validateSkillAuditRaw is the name-only params shape a fourth time, and a
// call through rather than a copy for validateSkillFilesRaw's reason: four
// contracts declaring one field is one answer to what a skill-name param is,
// and four copies of the bound would agree until somebody widened one.
func validateSkillAuditRaw(raw json.RawMessage) string {
	return validateSkillRemoveRaw(raw)
}
