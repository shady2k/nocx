package transport

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/capability"
)

type apiRequestScopeVariableParam struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type apiRequestScopeParams struct {
	Handle     string                          `json:"handle"`
	RelPath    string                          `json:"relPath"`
	EnvRelPath string                          `json:"envRelPath"`
	Variables  *[]apiRequestScopeVariableParam `json:"variables"`
}

type apiRequestScopeResponse struct {
	Variables []apiRequestScopeVariableWire `json:"variables"`
}

type apiRequestScopeVariableWire struct {
	Name       string `json:"name"`
	Value      string `json:"value"`
	Scope      string `json:"scope"`
	From       string `json:"from"`
	Overridden bool   `json:"overridden"`
	Refused    string `json:"refused"`
}

func wireRequestScope(result capability.RequestScopeResult) apiRequestScopeResponse {
	variables := make([]apiRequestScopeVariableWire, 0, len(result.Variables))
	for _, variable := range result.Variables {
		variables = append(variables, apiRequestScopeVariableWire{
			Name: variable.Name, Value: variable.Value, Scope: variable.Scope,
			From: variable.From, Overridden: variable.Overridden, Refused: variable.Refused,
		})
	}
	return apiRequestScopeResponse{Variables: variables}
}

func (h apiCollectionHandlers) handleRequestScope(ctx context.Context, req jsonrpcRequest, svc capability.APICollectionService) {
	var p apiRequestScopeParams
	if !h.decode(req, &p) {
		return
	}
	variables := make([]apicoll.Param, len(*p.Variables))
	for i, variable := range *p.Variables {
		variables[i] = apicoll.Param{
			Name: variable.Name, Value: variable.Value, Enabled: variable.Enabled,
		}
	}
	scope, err := svc.RequestScope(
		ctx,
		apicoll.HandleID(p.Handle),
		p.RelPath,
		p.EnvRelPath,
		variables,
	)
	if err != nil {
		h.fail(req, err)
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(wireRequestScope(scope)))
}

func validateAPIRequestScopeRaw(raw json.RawMessage) string {
	var p apiRequestScopeParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateAPIHandle(p.Handle); msg != "" {
		return msg
	}
	if msg := validateAPIRelPath(p.RelPath); msg != "" {
		return msg
	}
	if msg := boundedRunes("envRelPath", p.EnvRelPath, maxPathRunes); msg != "" {
		return msg
	}
	if p.Variables == nil {
		return "variables is required"
	}
	if len(*p.Variables) > maxAPIRequestRows {
		return "variables has too many rows"
	}
	for _, variable := range *p.Variables {
		if msg := boundedRunes("variables.name", variable.Name, maxConfigNameRunes); msg != "" {
			return msg
		}
		if msg := boundedRunes("variables.value", variable.Value, maxHeaderValueRunes); msg != "" {
			return msg
		}
	}
	return ""
}
