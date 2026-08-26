package transport

// The model-role methods (bead nocx-e6kn2): roles.list, roles.assign and
// roles.setDefault (bead nocx-rikz5) — the wire half of "a feature asks for
// a role, never for a model id".
//
// The handler holds only its seams — a ConfigOperation and the Responder —
// exactly like the endpoint handlers. The RESOLUTION (role → endpoint +
// model, with the visible refusals) lives behind that operation:
// svc.ResolveRole is the ONE resolver, used by the ask transaction, and
// THIS surface is where a person changes what a role resolves to.

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/profile"
)

// roleHandlers answers the roles.* methods. wired is true when the role
// store is wired; without it the methods refuse with -32601, the same shape
// the endpoints methods use.
type roleHandlers struct {
	op    capability.ConfigOperation
	wired bool
	r     Responder
}

func (h roleHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "roles not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		switch req.Method {
		case "roles.list":
			h.respondWithTable(req, svc)
		case "roles.assign":
			var params roleAssignParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			msg := validateRoleAssign(params)
			if msg != "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
				return nil
			}
			err := svc.AssignRole(roleAssignParamsToStored(params))
			if err != nil {
				_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "", err))
				return nil
			}
			h.respondWithTable(req, svc)
		case "roles.setDefault":
			var params roleSetDefaultParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if msg := validateRoleSetDefault(params); msg != "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
				return nil
			}
			// The endpoint's EXISTENCE and whether it offers the MODEL are
			// the store's checks, not this handler's: they happen under the
			// same lock as the write, so validate-and-write is one
			// operation (AD-8 — one owner per behaviour, and the owner is
			// whoever holds the lock). Both come back as sentinels
			// endpointMethodErrorCode already spells -32602, the same code
			// an endpoints.* method answers for the same mistake.
			err := svc.SetDefaultModel(profile.DefaultModel{EndpointID: params.EndpointID, Model: params.Model})
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: endpointMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			h.respondWithTable(req, svc)
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// respondWithTable answers with the ONE shape roles.list, roles.assign and
// roles.setDefault all return: the whole role table AND the default, read
// back after any write. Both fields come from one answer so the surface
// can never render a table from one moment and a default from another —
// the reason the write returns the state rather than an acknowledgement.
func (h roleHandlers) respondWithTable(req jsonrpcRequest, svc capability.ConfigService) {
	assignments, err := svc.ListRoleAssignments()
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}
	def, err := svc.DefaultModel()
	if err != nil {
		// RETURNED, never swallowed into "no default": a store that
		// cannot answer must not look like a person who chose nothing,
		// or an unreadable file renders as an honest "choose a model"
		// and sends someone to re-choose what they already chose.
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}
	// The wire lists EVERY role in the closed set: an unassigned role is a
	// null row, never an absent one (the surface's "no model assigned"
	// state is an assignment the product shows, not a role the product
	// hides).
	_ = h.r.TryResult(req.ID, mustMarshal(rolesListResponse{
		Roles:   wireRoles(assignments),
		Default: wireDefaultModel(def),
	}))
}

// wireDefaultModel is the default's wire form: the pair, or null when
// nobody has chosen one. Null is the state a fresh profile is in and the
// state in which the assistant is not ready — never a pair the product
// invented on the person's behalf.
func wireDefaultModel(d profile.DefaultModel) *defaultModelWire {
	if !d.IsSet() {
		return nil
	}
	return &defaultModelWire{EndpointID: d.EndpointID, Model: d.Model}
}

// defaultModelWire is the default (endpoint, model) pair on the wire. A
// pointer in the response so "unset" marshals as null rather than as a
// pair of empty strings, which the contract refuses.
type defaultModelWire struct {
	EndpointID string `json:"endpointId"`
	Model      string `json:"model"`
}

// rolesListResponse is the wire shape of BOTH roles.list and roles.assign
// (declared once in contracts/roles.list.schema.json and referenced
// cross-file by roles.assign, the git.status pattern): the full role table
// after the write. The assign returns the table rather than the one row so
// the renderer's single shape is the source of truth it renders from.
type rolesListResponse struct {
	Roles []profile.RoleDTO `json:"roles"`
	// Default is the pair every role WITHOUT an assignment of its own
	// resolves through (bead nocx-rikz5), null when nobody has chosen
	// one. It rides the SAME result as the roles because the surface
	// adopts both from one answer.
	Default *defaultModelWire `json:"default"`
}

// roleAssignParams is the wire form of one assignment write. Params are not
// contracted (contracts/README.md) — the handler validates what it parses.
// (endpointId, model) are BOTH present or BOTH null: a role is assigned to
// a specific (endpoint, model) pair, or it is unassigned; a half-assignment
// has no meaning.
type roleAssignParams struct {
	Role       string  `json:"role"`
	EndpointID *string `json:"endpointId"`
	Model      *string `json:"model"`
}

func validateRoleAssign(p roleAssignParams) string {
	if _, err := profile.ParseModelRole(p.Role); err != nil {
		return err.Error()
	}
	if (p.EndpointID == nil) != (p.Model == nil) {
		return "endpointId and model must be provided together — a role is assigned to an (endpoint, model) pair or to nothing"
	}
	if p.EndpointID != nil {
		if msg := configIDRunes("endpointId", *p.EndpointID); msg != "" {
			return msg
		}
		if msg := boundedRunes("model", *p.Model, maxConfigNameRunes); msg != "" {
			return msg
		}
	}
	return ""
}

// validateRoleAssignRaw is the registered validator for roles.assign.
func validateRoleAssignRaw(raw json.RawMessage) string {
	var p roleAssignParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return validateRoleAssign(p)
}

// roleSetDefaultParams is the wire form of the default write. Unlike an
// assignment's pair these are plain strings: BOTH empty is the CLEAR write,
// which is a value a person can choose, not an absence.
type roleSetDefaultParams struct {
	EndpointID string `json:"endpointId"`
	Model      string `json:"model"`
}

// validateRoleSetDefault checks the SHAPE — both present, or both empty.
// Whether the endpoint exists, and whether it offers the model, are the
// STORE's questions: it answers both against the document it is about to
// write. Resolution still refuses a pair that dangles later, and neither
// place ever repairs the pair into a neighbouring model.
func validateRoleSetDefault(p roleSetDefaultParams) string {
	if err := profile.ValidateDefaultModel(profile.DefaultModel{EndpointID: p.EndpointID, Model: p.Model}); err != nil {
		return err.Error()
	}
	if p.EndpointID != "" {
		if msg := configIDRunes("endpointId", p.EndpointID); msg != "" {
			return msg
		}
		if msg := boundedRunes("model", p.Model, maxConfigNameRunes); msg != "" {
			return msg
		}
	}
	return ""
}

// validateRoleSetDefaultRaw is the registered validator for
// roles.setDefault.
func validateRoleSetDefaultRaw(raw json.RawMessage) string {
	var p roleSetDefaultParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return validateRoleSetDefault(p)
}

func roleAssignParamsToStored(p roleAssignParams) profile.RoleAssignment {
	a := profile.RoleAssignment{Role: profile.ModelRole(p.Role)}
	if p.EndpointID != nil {
		a.EndpointID = *p.EndpointID
		a.Model = *p.Model
	}
	return a
}

// wireRoles completes the stored assignments to the closed role set: every
// role of profile.AllRoles() appears, its assignment merged in or null. The
// reference to the endpoint NEVER leaves as the material or the secret —
// the row carries the endpoint id and the model id, and the renderer joins
// its own endpoint list for the display names it already holds.
func wireRoles(assignments []profile.RoleAssignment) []profile.RoleDTO {
	byRole := make(map[profile.ModelRole]profile.RoleAssignment, len(assignments))
	for _, a := range assignments {
		byRole[a.Role] = a
	}
	roles := profile.AllRoles()
	out := make([]profile.RoleDTO, 0, len(roles))
	for _, r := range roles {
		row := profile.RoleDTO{Role: r}
		if a, ok := byRole[r]; ok {
			id, model := a.EndpointID, a.Model
			row.EndpointID = &id
			row.Model = &model
		}
		out = append(out, row)
	}
	return out
}
