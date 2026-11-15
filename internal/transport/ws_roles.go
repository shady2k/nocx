package transport

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/profile"
)

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
		var p roleAssignParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return nil
		}
		if req.Method == "roles.assign" {
			if msg := validateRoleAssign(p); msg != "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: msg})
				return nil
			}
			a := profile.RoleAssignment{Role: profile.ModelRole(p.Role), AgentID: p.AgentID}
			if p.Model != nil {
				a.Model = *p.Model
			}
			if err := svc.AssignRole(a); err != nil {
				_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "", err))
				return nil
			}
		}
		assignments, err := svc.ListRoleAssignments()
		if err != nil {
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "", err))
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(rolesListResponse{AgentID: p.AgentID, Roles: wireRoles(p.AgentID, assignments)}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

type rolesListResponse struct {
	AgentID string            `json:"agentId"`
	Roles   []profile.RoleDTO `json:"roles"`
}
type roleListParams struct {
	AgentID string `json:"agentId"`
}
type roleAssignParams struct {
	AgentID string  `json:"agentId"`
	Role    string  `json:"role"`
	Model   *string `json:"model"`
}

func validateRoleListRaw(raw json.RawMessage) string {
	var p roleListParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return validateRoleAgent(p.AgentID)
}

func validateRoleAssign(p roleAssignParams) string {
	if msg := validateRoleAgent(p.AgentID); msg != "" {
		return msg
	}
	if _, err := profile.ParseModelRole(p.Role); err != nil {
		return err.Error()
	}
	if p.Model != nil {
		if *p.Model == "" {
			return "model must not be empty; use null for the agent default"
		}
		return boundedRunes("model", *p.Model, maxConfigNameRunes)
	}
	return ""
}

func validateRoleAssignRaw(raw json.RawMessage) string {
	var p roleAssignParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return validateRoleAssign(p)
}

func wireRoles(agentID string, assignments []profile.RoleAssignment) []profile.RoleDTO {
	byRole := map[profile.ModelRole]string{}
	for _, a := range assignments {
		if a.AgentID == agentID {
			byRole[a.Role] = a.Model
		}
	}
	out := make([]profile.RoleDTO, 0, len(profile.AllRoles()))
	for _, r := range profile.AllRoles() {
		row := profile.RoleDTO{Role: r}
		if m, ok := byRole[r]; ok {
			row.Model = &m
		}
		out = append(out, row)
	}
	return out
}

func validateRoleAgent(id string) string {
	if id == "" {
		return "agentId is required"
	}
	return configIDRunes("agentId", id)
}
