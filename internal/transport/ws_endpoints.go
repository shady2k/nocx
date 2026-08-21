package transport

// The endpoint-config handlers as constructed types (design §4.5.4,
// ADR-0030): each handler holds a ConfigOperation (gates [config, vault]
// — the key-bearing write paths mint and rotate through the vault) plus
// the Responder. Never the *WSServer: a handler constructed with the
// operation cannot reach a store it was not given.
//
// The pure wire helpers (wireEndpoint, wireEndpoints) stay here: they map
// the stored credential reference to the renderer's row handle (vault.RowFor)
// and touch no store, exactly like wireProfile.
//
// The API key is an INPUT only: it rides the create/update params once,
// is minted or rotated by the service, and never survives a result, a
// log line or the persisted record (credential.Secret redacts in every
// fmt/slog path).

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// endpointHandlers answers the endpoints.* methods. wired is true when the
// endpoint repository is wired; the old-style refusal without it is
// -32601 "endpoints not available", the same shape profiles and groups
// use.
type endpointHandlers struct {
	op    capability.ConfigOperation
	wired bool
	r     Responder
}

func (h endpointHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "endpoints not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		switch req.Method {
		case "endpoints.list":
			eps, err := svc.ListEndpoints()
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
				return nil
			}
			// Secret references stay backend-owned: hand the renderer row
			// handles (ADR-0017 §1).
			_ = h.r.TryResult(req.ID, mustMarshal(endpointsListResponse{Endpoints: wireEndpoints(eps)}))
		case "endpoints.create":
			var params endpointCreateParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			e := params.toEndpoint()
			// Mint an ID when the renderer sends none, exactly like
			// profiles.create.
			if e.ID == "" {
				e.ID = profile.NewEndpointID(e.Name)
			}
			var key credential.Secret
			if params.Key != "" {
				key = credential.NewSecret(params.Key)
			}
			created, err := svc.CreateEndpoint(ctx, e, key)
			if err != nil {
				// rpcErrorFor keeps the endpoint conflict codes (-32602) and
				// attaches the vault's reason when the mint failed because the
				// vault needs setup or is sealed: without data.reason the
				// renderer's operation-first wrapper (saveSecretWithVault) and
				// the dispatcher's sealed interception cannot tell the vault
				// from a disk error, so the setup/unlock sheet never opens and
				// the save dies in a toast (nocx-4egm, the shape of nocx-25k9.7).
				_ = h.r.TryError(req.ID, rpcErrorFor(endpointMethodErrorCode(err), "", err))
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(endpointResultResponse{Endpoint: wireEndpoint(created)}))
		case "endpoints.update":
			var params endpointUpdateParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if params.ID == "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "id required"})
				return nil
			}
			e := params.toEndpoint()
			// "Absent or empty key" keeps the existing material (design
			// §4.5.4); a non-empty one rotates or mints.
			var key *credential.Secret
			if params.Key != "" {
				sk := credential.NewSecret(params.Key)
				key = &sk
			}
			updated, err := svc.UpdateEndpoint(ctx, e, key)
			if err != nil {
				_ = h.r.TryError(req.ID, rpcErrorFor(endpointMethodErrorCode(err), "", err))
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(endpointResultResponse{Endpoint: wireEndpoint(updated)}))
		case "endpoints.delete":
			var params struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if err := svc.DeleteEndpoint(ctx, params.ID); err != nil {
				_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "", err))
				return nil
			}
			// Nothing to return; the list is the state (like
			// vault.deleteSecret's empty result).
			_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// endpointMethodErrorCode maps the endpoint store's sentinel errors to
// transport codes, mirroring profileMethodErrorCode: an existing or missing
// record is a conflict/not-found (-32602 family), everything else internal.
// A model the endpoint does not offer is the same class one step further in
// — the params named something that is not there — so roles.setDefault
// answers it with the same code as an endpoint id that names nothing.
func endpointMethodErrorCode(err error) int {
	switch {
	case errors.Is(err, profile.ErrEndpointExists),
		errors.Is(err, profile.ErrEndpointNotFound),
		errors.Is(err, profile.ErrEndpointModelNotFound):
		return -32602
	default:
		return -32603
	}
}

// endpointModelInput is the wire form of one model in create/update params.
type endpointModelInput struct {
	Name  string  `json:"name"`
	Alias *string `json:"alias"`
}

// endpointHeaderInput is the wire form of one custom header in create/update
// params and in endpoints.probe params (the probe tests the form's DRAFT
// values, so it carries the same rows). The value's SOURCE is chosen with
// the same control the endpoint's key uses: a literal Value, or the row
// handle of a vault secret the backend resolves (never material, and never
// the reference itself — ADR-0017 §1). Exactly one of Value and Secret is
// set; nil means the other.
type endpointHeaderInput struct {
	Name   string  `json:"name"`
	Value  *string `json:"value"`
	Secret *string `json:"secret"`
}

type endpointCreateParams struct {
	Name    string                 `json:"name"`
	BaseURL string                 `json:"baseUrl"`
	Schema  profile.EndpointSchema `json:"schema"`
	Key     string                 `json:"key"`
	Models  []endpointModelInput   `json:"models"`
	// Credential is the renderer's row handle when the form chose "use an
	// existing vault secret" (nocx-rzjw) — the reference instead of a mint.
	Credential string `json:"credential"`
	// Headers are the endpoint's custom HTTP headers (nocx-lyyk).
	Headers []endpointHeaderInput `json:"headers"`
}

// resolveEndpointSchema completes a schema the wire params omitted. The
// backend owns an endpoint's schema until the form grows a control for it
// (design §4.5, decision 2): today there is exactly ONE legal dialect, and
// a renderer that sent "openai-compatible" would be stating a fact it
// never decided — the form has no control that chose it. The moment a
// second dialect exists, that constant and the backend's validation would
// become two owners of one value that must change in lockstep (AD-8),
// arriving on a schedule. So the value is completed here, at the wire seam
// that maps params to records, and the renderer-side alternative is
// rejected because a constant nobody chose is not a fact. When a
// dialect select lands, the renderer starts sending a value a person
// actually picked, and this default comes out.
func resolveEndpointSchema(s profile.EndpointSchema) profile.EndpointSchema {
	if s == "" {
		return profile.EndpointSchemaOpenAICompatible
	}
	return s
}

func (p endpointCreateParams) toEndpoint() profile.Endpoint {
	return profile.Endpoint{
		Name:          p.Name,
		BaseURL:       p.BaseURL,
		Schema:        resolveEndpointSchema(p.Schema),
		CredentialRef: p.Credential,
		Models:        wireModelsToStored(p.Models),
		Headers:       wireHeadersToStored(p.Headers),
	}
}

// wireHeadersToStored maps the wire header rows to the record form. A
// literal stays a literal; a secret row handle rides in ValueRef for the
// service to resolve (the same wire form the profile options use).
func wireHeadersToStored(in []endpointHeaderInput) []profile.EndpointHeader {
	if in == nil {
		return nil
	}
	out := make([]profile.EndpointHeader, len(in))
	for i, h := range in {
		out[i] = profile.EndpointHeader{Name: h.Name, Value: h.Value, ValueRef: derefOrEmpty(h.Secret)}
	}
	return out
}

// endpointUpdateParams is the full-replace update: same fields as create,
// plus the id. key is optional and empty means "keep the existing
// material" (design §4.5.4).
type endpointUpdateParams struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	BaseURL    string                 `json:"baseUrl"`
	Schema     profile.EndpointSchema `json:"schema"`
	Key        string                 `json:"key"`
	Credential string                 `json:"credential"`
	Models     []endpointModelInput   `json:"models"`
	Headers    []endpointHeaderInput  `json:"headers"`
}

func (p endpointUpdateParams) toEndpoint() profile.Endpoint {
	return profile.Endpoint{
		ID:            p.ID,
		Name:          p.Name,
		BaseURL:       p.BaseURL,
		Schema:        resolveEndpointSchema(p.Schema),
		CredentialRef: p.Credential,
		Models:        wireModelsToStored(p.Models),
		Headers:       wireHeadersToStored(p.Headers),
	}
}

func wireModelsToStored(in []endpointModelInput) []profile.EndpointModel {
	if in == nil {
		return nil
	}
	out := make([]profile.EndpointModel, len(in))
	for i, m := range in {
		out[i] = profile.EndpointModel{Name: m.Name, Alias: m.Alias}
	}
	return out
}

// Wire result shapes. Both are pinned by contracts/endpoints.*.schema.json
// and the renderer's types are generated from those files, so a field added
// here that is not added there fails the contract test rather than reaching
// a renderer that cannot see it.
type endpointsListResponse struct {
	Endpoints []profile.EndpointDTO `json:"endpoints"`
}

type endpointResultResponse struct {
	Endpoint profile.EndpointDTO `json:"endpoint"`
}

// wireEndpoint maps a stored endpoint to its wire form: the credential
// reference becomes the renderer's row handle, or null when no key is set.
// The reference never crosses the wire (ADR-0017 §1).
func wireEndpoint(e profile.Endpoint) profile.EndpointDTO {
	dto := profile.EndpointDTO{
		ID:      e.ID,
		Name:    e.Name,
		BaseURL: e.BaseURL,
		Schema:  e.Schema,
		Models:  wireModelsToDTO(e.Models),
		Headers: make([]profile.EndpointHeaderDTO, 0, len(e.Headers)),
	}
	if e.CredentialRef != "" {
		row := vault.RowFor(credential.SecretID(e.CredentialRef))
		dto.Credential = &row
	}
	for _, h := range e.Headers {
		row := profile.EndpointHeaderDTO{Name: h.Name, Value: h.Value}
		if h.ValueRef != "" {
			rowHandle := vault.RowFor(credential.SecretID(h.ValueRef))
			row.Secret = &rowHandle
		}
		dto.Headers = append(dto.Headers, row)
	}
	return dto
}

func wireModelsToDTO(in []profile.EndpointModel) []profile.EndpointModelDTO {
	if in == nil {
		return nil
	}
	out := make([]profile.EndpointModelDTO, len(in))
	for i, m := range in {
		out[i] = profile.EndpointModelDTO(m)
	}
	return out
}

// wireEndpoints maps a stored list to its wire form. Never null: an empty
// list is [] — the contract declares an array and a null there has cost
// this project a defect once already (nocx-25k9.14).
func wireEndpoints(eps []profile.Endpoint) []profile.EndpointDTO {
	if eps == nil {
		return []profile.EndpointDTO{}
	}
	out := make([]profile.EndpointDTO, len(eps))
	for i := range eps {
		out[i] = wireEndpoint(eps[i])
	}
	return out
}
