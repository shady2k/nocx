// Connector facts for Settings, independent of the agent launch records.
package transport

import (
	"context"
	"encoding/json"
	"fmt"

	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/operation/catalog"
)

// AgentCatalogue supplies cached connector readiness and available models.
type AgentCatalogue interface {
	Catalogue(context.Context, bool) ([]agentharness.AgentCatalogue, error)
}

type agentReadinessDTO struct {
	State   agentharness.ReadinessState `json:"state"`
	Message string                      `json:"message"`
}
type agentModelDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type agentCatalogueRow struct {
	ID        string            `json:"id"`
	Readiness agentReadinessDTO `json:"readiness"`
	Models    []agentModelDTO   `json:"models"`
}
type agentRecordsCatalogueResult struct {
	Agents []agentCatalogueRow `json:"agents"`
}

// WithAgentCatalogue connects Settings to the harness interfaces.
func WithAgentCatalogue(service AgentCatalogue) WSServerOption {
	return func(s *WSServer) { s.agentCatalogue = service }
}

func (s *WSServer) agentCatalogueSpecs() []methodSpec {
	return []methodSpec{regResponder(s.work, catalog.OpAgentRecordsCatalogue, params(validateAgentRecordsDetect), func(r Responder) handlerFunc {
		return func(ctx context.Context, req jsonrpcRequest) {
			if s.agentCatalogue == nil {
				_ = r.TryError(req.ID, RPCError{Code: -32601, Message: "agentRecords.catalogue not available"})
				return
			}
			var p agentRecordsDetectParams
			if len(req.Params) > 0 {
				if err := json.Unmarshal(req.Params, &p); err != nil {
					_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
					return
				}
			}
			agents, err := s.agentCatalogue.Catalogue(ctx, p.Refresh)
			if err != nil {
				log.From(ctx).Error("agent catalogue could not be read", "error", err)
				_ = r.TryError(req.ID, RPCError{Code: -32000, Message: fmt.Sprint("agent catalogue could not be read: ", err)})
				return
			}
			rows := make([]agentCatalogueRow, 0, len(agents))
			for _, agent := range agents {
				models := make([]agentModelDTO, 0, len(agent.Models))
				for _, model := range agent.Models {
					models = append(models, agentModelDTO{ID: model.ID, Name: model.Name})
				}
				rows = append(rows, agentCatalogueRow{ID: agent.ID, Readiness: agentReadinessDTO{State: agent.Readiness.State, Message: agent.Readiness.Message}, Models: models})
			}
			_ = r.TryResult(req.ID, mustMarshal(agentRecordsCatalogueResult{Agents: rows}))
		}
	})}
}
