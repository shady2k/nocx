package transport

// The assistant's control-plane methods as constructed types (design §7,
// nocx-edio): agent.status and endpoints.probe. Each handler holds only its
// seams — the config operation (endpoint list), the credential store
// (credential resolvability), the assistant client (the probe) and the
// probe store (the last-probe fact) — plus its Responder; never the
// *WSServer, so a handler cannot reach a store it was not constructed with.
//
// The ask transaction (agent.ask, agent.cancel, agent.approve, the run
// state machine, the ledger writes) is nocx-f4s5 and deliberately does not
// live here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// agentStatusResult is the agent.status wire shape, pinned by
// contracts/agent.status.schema.json. lastProbe is required on the wire and
// null when none has run — a nil pointer marshals to null. credential names
// which of the credential facts is true (design §7, ADR-0032): one
// authoritative enum, never a boolean that hides the reason. It is null only
// when no endpoint is configured (there is nothing to ask about).
//
// The three facts the product distinguishes are 'none' (the endpoint has no
// reference at all), 'deleted' (the referenced secret is gone) and 'sealed'
// (the vault cannot answer right now) — each gets its own sentence in
// agentStatusLine instead of all three reading "the vault may be locked".
// 'unavailable' is the honest fallback for a store failure that is none of
// those (a provider hiccup): it is never mislabelled as one of the three.
type agentStatusResult struct {
	EndpointConfigured bool                   `json:"endpointConfigured"`
	Credential         *string                `json:"credential"`
	LastProbe          *assistant.ProbeResult `json:"lastProbe"`
}

// The credential enum: the wire's single vocabulary for "can the ask
// authenticate", and the reason when it cannot.
const (
	credResolvable  = "resolvable"
	credNone        = "none"
	credDeleted     = "deleted"
	credSealed      = "sealed"
	credUnavailable = "unavailable"
)

// endpointProbeParams are the form's DRAFT values (design §4.5) plus the
// endpoint id when the form is editing a SAVED endpoint. The key is an
// input that rides the params once and never crosses back (ADR-0030).
// Params are not contracted (contracts/README.md) — the handler validates
// what it parses.
//
// The credential resolution rule, in code:
//
//  1. A non-empty key WINS — the user typed a key to test it before saving
//     it (the other half of what this button is for), so the stored
//     credential must not be consulted at all. The other order would
//     silently test the credential the user is actively replacing.
//  2. Else, endpointId names the record and the BACKEND resolves the
//     credential that record owns — exactly how connections.test resolves
//     a profile by its id (the renderer never re-fetches the material,
//     which ADR-0030 forbids crossing back). A sealed or unavailable vault
//     is a probe RESULT naming that, never a Go error and never a
//     no-key dial (which would 401 and lie about a working endpoint).
//  3. Else (no key, no id, or a saved endpoint with no credential) the
//     probe runs without one — the local-model case.
//
// The baseUrl and model stay the form's: the button sits on the form, so
// the form's target is what is tested; only the credential is resolved.
type endpointProbeParams struct {
	Name       string `json:"name"`
	BaseURL    string `json:"baseUrl"`
	Key        string `json:"key"`
	Model      string `json:"model"`
	EndpointID string `json:"endpointId"`
	// Headers are the form's DRAFT custom headers (nocx-lyyk) — the same
	// wire rows the create/update params carry, because the probe tests
	// what will actually be used. A secret-valued row is a row handle the
	// backend resolves to material before the probe dials.
	Headers []endpointHeaderInput `json:"headers"`
}

// assistantStatusHandlers answers agent.status. wired is true when the endpoint
// repository is wired; without it the method refuses with -32601, the same
// shape profiles and groups use.
type assistantStatusHandlers struct {
	op      capability.ConfigOperation
	secrets credential.Resolver
	probes  *assistant.ProbeStore
	wired   bool
	r       Responder
}

func (h assistantStatusHandlers) handleAgentStatus(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "agent not available"})
		return
	}
	res := agentStatusResult{LastProbe: nil}
	if h.probes != nil {
		res.LastProbe = h.probes.Last()
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		eps, err := svc.ListEndpoints()
		if err != nil {
			return err
		}
		res.EndpointConfigured = len(eps) > 0
		if !res.EndpointConfigured {
			return nil // credential stays null: nothing to ask about
		}
		// One resolvable endpoint is enough to ask. When none resolves, the
		// reason is the most actionable one present: a sealed vault
		// outranks the rest (the ask surface's whole point is to offer the
		// unlock), then the first endpoint's own fact.
		resolved := false
		sealed := false
		firstOther := ""
		for _, ep := range eps {
			st := h.credentialStateFor(ctx, ep.CredentialRef)
			switch st {
			case credResolvable:
				resolved = true
			case credSealed:
				sealed = true
			default:
				if firstOther == "" {
					firstOther = st
				}
			}
		}
		cred := credResolvable
		if !resolved {
			switch {
			case sealed:
				cred = credSealed
			case firstOther != "":
				cred = firstOther
			}
		}
		res.Credential = &cred
		return nil
	})
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(res))
}

// credentialStateFor classifies one endpoint's credential reference into the
// wire's facts: 'resolvable' when the vault answers, 'none' when the
// endpoint has no reference (or no store is wired), 'deleted' when the
// referenced secret is gone, 'sealed' when the vault cannot answer right
// now, and 'unavailable' for a store failure that is none of those — it is
// never mislabelled as one of the three.
//
// This is a read that REPORTS: it swallows the sealed condition instead of
// surfacing it, so agent.status never raises the unlock prompt while
// somebody is looking at a settings page (asserted by assertNoPendingAsk).
func (h assistantStatusHandlers) credentialStateFor(ctx context.Context, ref string) string {
	if ref == "" || h.secrets == nil {
		return credNone
	}
	secret, err := h.secrets.Resolve(ctx, credential.SecretID(ref), credential.ToReport)
	if err != nil {
		switch {
		case errors.Is(err, credential.ErrSealedQuiet):
			// The stance did the work: a ToReport resolution translates the
			// sealed condition into an error the seam cannot recognize, so
			// this page can name the state without any chance of a prompt
			// appearing over somebody who was only reading.
			return credSealed
		case errors.Is(err, vault.ErrSecretNotFound):
			return credDeleted
		default:
			return credUnavailable
		}
	}
	if secret.IsEmpty() {
		return credDeleted
	}
	return credResolvable
}

// assistantProbeHandlers answers endpoints.probe: probe the form's draft
// values with the engine the ask transaction will use, record the outcome,
// and return it. wired is true when the assistant client is present;
// without it the method refuses with -32601. The op and secrets seams are
// the credential resolution (the endpointId path): the op names the
// record, the secret store resolves the material — the same two seams
// agent.status holds, and the same split the ask path uses (record under
// the config operation, material at stream time).
type assistantProbeHandlers struct {
	op      capability.ConfigOperation
	secrets credential.Resolver
	client  assistant.Client
	probes  *assistant.ProbeStore
	wired   bool
	r       Responder
}

func (h assistantProbeHandlers) handleEndpointProbe(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "agent not available"})
		return
	}
	var params endpointProbeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	key, refused, resolveErr := h.resolveProbeCredential(ctx, params)
	if resolveErr != nil {
		// The renderer named a record that does not exist (deleted
		// meanwhile): a caller error, exactly as connections.test surfaces
		// a profile that does not resolve — never a fabricated verdict.
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: resolveErr.Error()})
		return
	}
	if refused != nil {
		// A sealed or unavailable vault is a probe RESULT naming that —
		// the Test button that hangs or lies is the thing being fixed, and
		// a Go error here would be a second kind of lie. Recorded like any
		// other outcome, so agent.status reports it.
		if h.probes != nil {
			h.probes.Record(*refused)
		}
		_ = h.r.TryResult(req.ID, mustMarshal(*refused))
		return
	}

	headers, refusedHeaders, headerErr := h.resolveProbeHeaders(ctx, params)
	if headerErr != nil {
		// An unknown row is a caller error, the same shape as the record
		// above; the sealed vault keeps its dispatcher seam (ADR-0032).
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: headerErr.Error()})
		return
	}
	if refusedHeaders != nil {
		if h.probes != nil {
			h.probes.Record(*refusedHeaders)
		}
		_ = h.r.TryResult(req.ID, mustMarshal(*refusedHeaders))
		return
	}

	res, err := h.client.Probe(ctx, assistant.ProbeParams{
		Name:    params.Name,
		BaseURL: params.BaseURL,
		Key:     key,
		Model:   params.Model,
		Headers: headers,
	})
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}
	if h.probes != nil {
		h.probes.Record(res)
	}
	_ = h.r.TryResult(req.ID, mustMarshal(res))
}

// resolveProbeHeaders applies the credential resolution's rule to the
// form's DRAFT custom headers (nocx-lyyk): a literal rides as-is; a
// secret-valued row resolves to material at probe time. Anything
// unresolvable is a refused probe RESULT naming the header — never a
// no-header dial, which would 401 and lie about a working endpoint. The
// sealed vault keeps its dispatcher seam, exactly like the credential.
func (h assistantProbeHandlers) resolveProbeHeaders(ctx context.Context, params endpointProbeParams) ([]assistant.Header, *assistant.ProbeResult, error) {
	if len(params.Headers) == 0 {
		return nil, nil, nil
	}

	// Resolve every row handle to material in one service pass, keyed by
	// the row so the final list preserves the DRAFT's order. Literals need
	// no vault at all, so a literal-only draft resolves even without the
	// vault seams (the dev-web harness).
	rows := make([]string, 0, len(params.Headers))
	rowIndex := make(map[string]int, len(params.Headers))
	for _, hd := range params.Headers {
		if hd.Secret == nil {
			continue
		}
		if _, seen := rowIndex[*hd.Secret]; !seen {
			rowIndex[*hd.Secret] = len(rows)
			rows = append(rows, *hd.Secret)
		}
	}
	if len(rows) == 0 {
		out := make([]assistant.Header, 0, len(params.Headers))
		for _, hd := range params.Headers {
			out = append(out, assistant.Header{Name: hd.Name, Value: derefOrEmpty(hd.Value)})
		}
		return out, nil, nil
	}
	if h.op == nil || h.secrets == nil {
		return nil, refusedProbeHeadersResult(params, params.Headers), nil
	}

	refs := make([]string, len(rows))
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		for i, row := range rows {
			ref, resolveErr := svc.ResolveSecretRow(row)
			if resolveErr != nil {
				return resolveErr
			}
			refs[i] = ref
		}
		return nil
	})
	if err != nil {
		// An unresolvable row is the header's analogue of the credential
		// that cannot be resolved (deleted meanwhile, or a stale handle):
		// a refused probe RESULT naming the header, never a Go error and
		// never a no-header dial.
		return nil, refusedProbeHeadersResult(params, params.Headers), nil
	}

	material := make(map[string]string, len(rows))
	for i, ref := range refs {
		secret, getErr := h.secrets.Resolve(
			ctx, credential.SecretID(ref), credential.ForOperation)
		if getErr != nil {
			if errors.Is(getErr, vault.ErrVaultSealed) {
				return nil, nil, vault.ErrVaultSealed
			}
			return nil, refusedProbeHeadersResult(params, params.Headers), nil
		}
		if secret.IsEmpty() {
			return nil, refusedProbeHeadersResult(params, params.Headers), nil
		}
		_ = secret.Use(func(b []byte) error {
			material[rows[i]] = string(b)
			return nil
		})
	}

	out := make([]assistant.Header, 0, len(params.Headers))
	for _, hd := range params.Headers {
		if hd.Secret == nil {
			out = append(out, assistant.Header{Name: hd.Name, Value: derefOrEmpty(hd.Value)})
			continue
		}
		out = append(out, assistant.Header{Name: hd.Name, Value: material[*hd.Secret]})
	}
	return out, nil, nil
}

// refusedProbeHeadersResult is the unavailable-header probe verdict: a probe
// RESULT naming the header, never a Go error and never a no-header dial.
func refusedProbeHeadersResult(params endpointProbeParams, headers []endpointHeaderInput) *assistant.ProbeResult {
	kind := assistant.ProbeModel
	if params.Model == "" {
		kind = assistant.ProbeConnection
	}
	names := make([]string, 0, len(headers))
	for _, hd := range headers {
		if hd.Secret != nil {
			names = append(names, hd.Name)
		}
	}
	return &assistant.ProbeResult{
		EndpointName: params.Name,
		Model:        params.Model,
		Kind:         kind,
		OK:           false,
		Error:        fmt.Sprintf("the header %q references an unavailable secret", strings.Join(names, ", ")),
		At:           time.Now(),
	}
}

// resolveProbeCredential applies the endpoints.probe resolution rule (the
// params comment carries the rule and the rejected alternative):
//
//  1. A non-empty typed key wins — no vault read, no record lookup.
//  2. Else, endpointId names the record; its OWN credential is resolved
//     from the vault. Unavailable (sealed vault, deleted secret, missing
func (h assistantProbeHandlers) resolveProbeCredential(ctx context.Context, params endpointProbeParams) (credential.Secret, *assistant.ProbeResult, error) {
	typed := credential.NewSecret(params.Key)
	if !typed.IsEmpty() {
		return typed, nil, nil
	}
	if params.EndpointID == "" {
		return credential.Secret{}, nil, nil
	}

	var ref string
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		ep, err := svc.GetEndpoint(params.EndpointID)
		if err != nil {
			return err
		}
		ref = ep.CredentialRef
		return nil
	})
	if err != nil {
		return credential.Secret{}, nil, err
	}
	if ref == "" {
		// The endpoint honestly has no credential (created without one, or
		// its key was deleted on the Secrets page): probe without one.
		return credential.Secret{}, nil, nil
	}
	if h.secrets == nil {
		// No store to resolve with: the probe must not dial without the
		// credential (a no-key dial would 401 and lie about a working
		// endpoint). This is a build-configuration state, not the sealed
		// vault, so it stays a refused result with the honest sentence.
		return credential.Secret{}, refusedProbeResult(params), nil
	}
	secret, err := h.secrets.Resolve(ctx, credential.SecretID(ref), credential.ForOperation)
	if err != nil {
		if errors.Is(err, vault.ErrVaultSealed) {
			// The vault is sealed: this is a sealed-vault failure. The
			// dispatcher's seam normalizes it to the canonical error, the
			// renderer raises the unlock and re-sends the probe — the call
			// completes once the vault answers (ADR-0032). Never a probe
			// RESULT naming the sealed state: that was the dead end this
			// bead exists to delete.
			return credential.Secret{}, nil, vault.ErrVaultSealed
		}
		return credential.Secret{}, refusedProbeResult(params), nil
	}
	if secret.IsEmpty() {
		return credential.Secret{}, refusedProbeResult(params), nil
	}
	return secret, nil, nil
}

// ── endpoints.probe ingress bounds ────────────────────────────────────────
//
// Every one of these params is renderer-supplied and reaches something real:
// the base URL is dialled, the model goes into the request body, and the key
// goes into an HTTP header. Before nocx-q27y this method checked only that
// the base URL was non-empty — the thinnest ingress in the assistant surface,
// and the one that had grown a second reachable shape.
const (
	// maxProbeNameRunes bounds the display name, which is only echoed back
	// in the result and stored as the "last probe" fact.
	maxProbeNameRunes = 200
	// maxProbeURLRunes bounds the base URL. Far above any real endpoint;
	// this is a wire-cost bound, not a naming rule.
	maxProbeURLRunes = 2_000
	// maxProbeModelRunes bounds the model id.
	maxProbeModelRunes = 200
	// maxProbeKeyRunes bounds the API key. Generous — some providers issue
	// long JWTs — and still a bound.
	maxProbeKeyRunes = 8_000
	// maxProbeIDRunes bounds the renderer-supplied endpoint id, matching the
	// ask path's maxIDRunes.
	maxProbeIDRunes = 128
)

// validateProbeParamsRaw is the registered validator (registration.go): it
// decodes the params and applies validateProbeParams, so the handler is never
// entered with anything it would have had to check itself. The exemplar for
// converting a method off genericObject — decode, then check every reachable
// field.
func validateProbeParamsRaw(raw json.RawMessage) string {
	var p endpointProbeParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	return validateProbeParams(p)
}

// validateProbeParams checks every reachable endpoints.probe param, returning
// a non-empty message on the first failure. Returns "" when the params are
// acceptable.
func validateProbeParams(p endpointProbeParams) string {
	if p.BaseURL == "" {
		return "baseUrl is required"
	}
	if n := utf8.RuneCountInString(p.BaseURL); n > maxProbeURLRunes {
		return fmt.Sprintf("baseUrl exceeds %d characters", maxProbeURLRunes)
	}
	// The SAME parse-level rule a stored endpoint is held to
	// (profile.ValidateBaseURL) — one owner, so a URL refused at save time
	// cannot be silently dialled at test time. The address policy proper
	// stays at dial time (internal/assistant/httpguard.go), where it can be
	// enforced against the address actually connected.
	if err := profile.ValidateBaseURL(p.BaseURL); err != nil {
		return err.Error()
	}
	if utf8.RuneCountInString(p.Name) > maxProbeNameRunes {
		return fmt.Sprintf("name exceeds %d characters", maxProbeNameRunes)
	}
	if utf8.RuneCountInString(p.Model) > maxProbeModelRunes {
		return fmt.Sprintf("model exceeds %d characters", maxProbeModelRunes)
	}
	if utf8.RuneCountInString(p.Key) > maxProbeKeyRunes {
		return fmt.Sprintf("key exceeds %d characters", maxProbeKeyRunes)
	}
	if utf8.RuneCountInString(p.EndpointID) > maxProbeIDRunes {
		return fmt.Sprintf("endpointId exceeds %d characters", maxProbeIDRunes)
	}
	// The key becomes an Authorization header and the model becomes part of
	// a request body. A control character in either is never legitimate, and
	// refusing it here means the header writer never has to be the last line
	// of defence against a newline in a credential.
	if hasControlChars(p.Key) {
		return "key must not contain control characters"
	}
	if hasControlChars(p.Model) {
		return "model must not contain control characters"
	}
	// The draft headers ride the probe's requests verbatim — the same rows
	// the create/update params validate, via the SAME validator, so a header
	// refused at save time is refused at test time.
	if msg := validateEndpointHeaderRows(p.Headers); msg != "" {
		return msg
	}
	return ""
}

// hasControlChars reports whether s contains any C0/C1 control character or
// DEL. Tabs and newlines are control characters too, and neither belongs in
// a credential, a model id or a URL. One predicate for the whole wire: the
// implementation lives in internal/profile (the same owner the stored-record
// header validation uses) so a control character cannot be refused in one
// place and dialled in another.
func hasControlChars(s string) bool {
	return profile.HasControlChars(s)
}

// refusedProbeResult is the unavailable-credential probe verdict: a probe
// RESULT naming the state, never a Go error and never a no-key dial (which
// would 401 and lie about a working endpoint). It covers the record whose
// credential cannot be resolved — deleted, empty, or a store failure —
// NOT the sealed vault, which is a sealed-vault failure normalized by the
// dispatcher seam into the canonical error (ADR-0032).
func refusedProbeResult(params endpointProbeParams) *assistant.ProbeResult {
	kind := assistant.ProbeModel
	if params.Model == "" {
		kind = assistant.ProbeConnection
	}
	return &assistant.ProbeResult{
		EndpointName: params.Name,
		Model:        params.Model,
		Kind:         kind,
		OK:           false,
		Error:        "the endpoint's credential is unavailable",
		At:           time.Now(),
	}
}
