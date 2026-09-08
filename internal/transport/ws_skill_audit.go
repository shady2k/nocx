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
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/skill"
)

// skillCheckStore is the transport's view of content.SkillCheckRepository —
// declared here rather than imported so the handler depends on the method
// set it actually uses and not on the content package's whole repository
// surface (AD-8). content.SkillCheckRepository satisfies it structurally;
// nothing converts between them.
type skillCheckStore interface {
	Put(ctx context.Context, check content.SkillCheck) error
	Get(ctx context.Context, name string) (content.SkillCheck, bool, error)
}

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
	AuditSkill(ctx context.Context, p assistant.SkillAuditParams) (assistant.SkillAuditResult, error)
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
	// Stored says whether this reading was written to the person's machine
	// so opening the same skill again costs nothing: "yes" once content.db
	// has it, "no" when it was not written — because the bytes moved during
	// the call, because there is no store on this machine, or because the
	// store refused the write — with StoredError naming which. The RPC
	// itself errors only when the check could not be PRODUCED at all; a
	// check that was produced but not saved still reaches the person, which
	// is the whole reason this field exists rather than folding the failure
	// into the same error path as an unreachable model.
	Stored string `json:"stored"`
	// StoredError is why Stored is "no". Empty when Stored is "yes".
	StoredError string `json:"storedError,omitempty"`
}

type skillAuditHandlers struct {
	source      skillAuditSource
	engine      skillAuditEngine
	configOp    capability.ConfigOperation
	credentials credential.Resolver
	// checks is where the reading is filed once the model has answered. Nil
	// means no store is wired on this machine — the audit still returns its
	// report, and Stored says "no" rather than the RPC pretending nothing
	// was asked for.
	checks skillCheckStore
	log    log.Logger
	wired  bool
	r      Responder
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

	// BUILTIN IS NOT CHECKED, and the refusal is here rather than only in
	// the UI because a refusal only the renderer knows about is one a
	// second caller walks straight past. Its bytes came with the binary and
	// the person decided about them when they installed nocx; a model's
	// opinion on our own shipped files is theatre with a bill attached.
	// Before the role is resolved, so it costs nothing.
	if material.Provenance == skill.ProvenanceBuiltin {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "a builtin skill is not checked: its files came with nocx itself"})
		return
	}

	role, endpoint, model, key, headers, err := h.resolveAuditModel(ctx)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	// SKILL.md is the skill's own claim, and the reduce measures the verdict
	// against it — so it is passed verbatim as well as read as a file. It is
	// the FIRST entry by construction (internal/skill walks the manifest, and
	// the manifest opens with it), and a bundle whose first file is something
	// else is a bundle whose SKILL.md could not be read, which Audit already
	// refuses.
	overview, purpose := "", ""
	if len(material.Files) > 0 {
		overview = material.Files[0].Text
		purpose = skillPurpose(overview)
	}

	// WHAT WAS ASKED, BEFORE IT IS ASKED (nocx-w155y's diagnosis). A reading
	// that hangs or fails used to leave NOTHING in the log — the last line was
	// the vault's "secret retrieved" and then silence, so working out where it
	// stopped meant reading the process's open sockets. These two lines make
	// the span visible from both ends: what went out, and what came back or
	// did not. Sizes and names only; the document is a stranger's text and the
	// report is a model's description of it, and neither belongs in a log.
	askedAt := time.Now()
	if h.log != nil {
		h.log.Info("skill: audit asked",
			"skill", material.Name, "role", string(role),
			"endpoint", endpoint.Name, "model", model,
			"documentBytes", len(material.Document),
			"files", len(material.Read), "omitted", len(material.Omitted))
	}

	result, err := h.engine.AuditSkill(ctx, assistant.SkillAuditParams{
		Key: key, BaseURL: endpoint.BaseURL, Model: model, Headers: headers,
		Name: material.Name, Purpose: purpose, Overview: overview,
		Files: auditFiles(material.Files),
	})
	if err != nil {
		if h.log != nil {
			// The elapsed time is the fact that distinguishes the three ways
			// this fails: a refusal comes back at once, a bad address takes a
			// dial, and a provider that accepted and then said nothing takes
			// the whole budget.
			h.log.Warn("skill: audit failed",
				"skill", material.Name, "endpoint", endpoint.Name, "model", model,
				"documentBytes", len(material.Document),
				"elapsed", time.Since(askedAt), "error", err.Error())
		}
		// The engine's sentence travels, AND THE PAIR IT WAS ABOUT travels with
		// it. A reading that did not happen is a refusal the person reads,
		// never an empty report — an empty report is indistinguishable from a
		// clean one, which is the whole reason this feature refuses to certify
		// anything. But the person who pressed Check never named an endpoint:
		// the reading resolved its own, from the auditing role, or the
		// answering one, or the machine's default. A refusal that does not say
		// which one it was sends them to change the wrong thing — it cost
		// exactly that once, a timeout diagnosed against the remote provider
		// while the call had gone to the local one (nocx-w155y).
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32603,
			Message: fmt.Sprintf("%s (endpoint %q, model %q)", err.Error(), endpoint.Name, model),
		})
		return
	}
	reading := result.SkillReading
	if h.log != nil {
		// What it COST and what it read, never the report: the prose is a
		// stranger's document described by a model, and a log is not where
		// that belongs. notesFailed is the map pass's own outcome — a reading
		// drawn from eight notes and one file nobody could read is a different
		// reading from one drawn from nine, and only this line says which.
		h.log.Info("skill: audit answered",
			"notes", len(result.Files), "notesFailed", failedNotes(result.Files),
			"skill", material.Name, "role", string(role), "model", model,
			"elapsed", time.Since(askedAt),
			"files", len(material.Read), "omitted", len(material.Omitted),
			"findings", len(material.Findings))
	}

	// THE INTERVAL, stated: the material's digest is true of the bytes from
	// the moment Audit composed the document above until this recomposition
	// agrees with it. The model call sits inside that span and is slow, so
	// a delete, a reinstall or an ordinary edit can land in the middle of
	// it. A check filed against a digest nobody's disk now matches would
	// describe a document that does not exist, so it is discarded rather
	// than stored — not stored with a stale digest, which the reader could
	// mistake for a check that is merely old.
	//
	// No lock is held across the model call above, and none is taken here
	// either: the row is keyed by name and the write is last-writer-wins,
	// which is the whole of the concurrency story (design §7). Recomposing
	// through h.source rather than comparing to a cached copy is what makes
	// this catch a reinstall or an edit and not only a delete — a fresh
	// Audit sees whatever is on disk right now, cached or not.
	stored, storedErr := "yes", ""
	if after, reErr := h.source.Audit(material.Name); reErr != nil || after.Digest != material.Digest {
		stored = "no"
		// One sentence covers three different things that can have happened
		// — the skill was edited, removed, or replaced (a reinstall) — and
		// "changed" alone would be wrong for the middle one: a person reading
		// "changed" about a skill that no longer resolves at all is the
		// reader most likely to be confused, since nothing changed, it is
		// simply gone. reErr != nil is exactly that case (Audit refuses a
		// name no root holds); a digest mismatch with no error is the other
		// two.
		storedErr = "the skill's files no longer match what was checked — edited, removed, or replaced while the check was running — so this reading was not saved"
	} else if h.checks == nil {
		stored, storedErr = "no", "there is no store for checks on this machine"
	} else if putErr := h.checks.Put(ctx, content.SkillCheck{
		Name:       material.Name,
		Provenance: string(material.Provenance),
		Verdict:    string(reading.Verdict),
		Report:     reading.Report,
		Role:       string(role),
		Endpoint:   endpoint.Name,
		Model:      model,
		Digest:     material.Digest,
		CheckedAt:  time.Now().UnixMilli(),
		Read:       material.Read,
		Omitted:    auditOmissions(material.Omitted),
		Findings:   auditFindings(material.Findings),
		MaxBytes:   int64(material.MaxBytes),
	}); putErr != nil {
		// A STORE FAILURE MUST NOT SWALLOW THE ANSWER. The person pressed a
		// button and a model was already billed by the time this write is
		// attempted; refusing to show the report because content.db could
		// not take it would spend their money for nothing. The RPC errors
		// only when the check could not be PRODUCED — this branch is about
		// one that was and could not be SAVED, which is what Stored exists
		// to say.
		stored, storedErr = "no", putErr.Error()
	}

	_ = h.r.TryResult(req.ID, mustMarshal(skillAuditResult{
		Name:        material.Name,
		Provenance:  material.Provenance,
		Role:        string(role),
		Endpoint:    endpoint.Name,
		Model:       model,
		Verdict:     string(reading.Verdict),
		Report:      reading.Report,
		Read:        material.Read,
		Omitted:     material.Omitted,
		MaxBytes:    material.MaxBytes,
		Findings:    material.Findings,
		Stored:      stored,
		StoredError: storedErr,
	}))
}

// auditOmissions converts the skill package's omission shape to the store's.
// content must not import skill (AD-8: the store is a store and knows
// nothing about a bundle's provenance or its scan patterns), so the two
// shapes are declared separately and this is where they are reconciled.
func auditOmissions(in []skill.AuditOmission) []content.SkillCheckOmission {
	out := make([]content.SkillCheckOmission, 0, len(in))
	for _, o := range in {
		out = append(out, content.SkillCheckOmission{Path: o.Path, Reason: string(o.Reason)})
	}
	return out
}

// auditFindings converts the skill package's finding shape to the store's,
// for the same reason auditOmissions does.
func auditFindings(in []skill.Finding) []content.SkillCheckFinding {
	out := make([]content.SkillCheckFinding, 0, len(in))
	for _, f := range in {
		out = append(out, content.SkillCheckFinding{
			Path: f.Path, LineNumber: f.LineNumber, Line: f.Line, PatternID: f.PatternID,
		})
	}
	return out
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
	)
	err := h.configOp.Run(ctx, func(_ context.Context, svc capability.ConfigService) error {
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
		endpoint, model = ep, m
		return nil
	})
	if err != nil {
		return "", profile.Endpoint{}, "", credential.Secret{}, nil, err
	}

	// THE MATERIAL IS READ AFTER THE OPERATION HAS RELEASED, and the position
	// is the whole of the fix (nocx-9fzkk, and nocx-o3606 before it). The
	// config operation is composed from the config gate AND THE VAULT GATE
	// (buildConfigOp), and an operation-stance read raises the vault's own
	// unlock and waits for a person. vault.unseal — the answer to that very
	// prompt — needs the vault gate, so resolving inside the callback shows
	// somebody "Unlock the vault to audit a skill" and then refuses their
	// Unlock with "Control plane busy". The role resolution needs the store
	// and the material does not, so they are two steps and only the first
	// takes a gate.
	//
	// "audit a skill" rather than "answer the ask": this string is what the
	// vault shows a person when it raises the unlock, and a prompt that named
	// the wrong reason would be the product lying about why it wants a key.
	key, headers, materialErr := resolveEndpointMaterial(
		ctx, h.credentials, endpoint, credential.Operation("audit a skill"))
	if materialErr != nil {
		return "", profile.Endpoint{}, "", credential.Secret{}, nil, materialErr
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

// auditFiles converts internal/skill's view of a bundle to the engine's. The
// two shapes are declared separately for auditOmissions' reason: the engine
// knows nothing about provenance, roots or scan findings, and a shared struct
// would carry all three into a package that must not have them.
func auditFiles(in []skill.AuditFile) []assistant.SkillAuditFile {
	out := make([]assistant.SkillAuditFile, 0, len(in))
	for _, f := range in {
		out = append(out, assistant.SkillAuditFile{Path: f.Path, Text: f.Text})
	}
	return out
}

// failedNotes counts the files the map pass could not read. It is a log fact
// rather than a wire one: the person is told what the reading concluded, and
// the reduce is told which files it was told nothing about, so a count on the
// wire would be a third owner of the same fact.
func failedNotes(notes []assistant.SkillFileReading) int {
	n := 0
	for _, note := range notes {
		if note.Err != nil {
			n++
		}
	}
	return n
}

// skillPurpose is the skill's own one-line claim, taken from the frontmatter
// SKILL.md opens with. It is what a per-file call is measured against, and it
// is read here rather than parsed a second time in internal/skill because the
// bytes are already in hand — a file that has no frontmatter description
// yields "", and the per-file prompt omits the line rather than inventing one.
func skillPurpose(overview string) string {
	const marker = "description:"
	for _, line := range strings.Split(overview, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, marker) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
		}
		if trimmed == "---" && strings.Contains(overview, marker) {
			continue
		}
	}
	return ""
}
