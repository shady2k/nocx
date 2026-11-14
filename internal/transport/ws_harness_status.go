package transport

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/capability"
	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/profile"
)

// harnessStatusResult is the current answering agent, never endpoint health.
type harnessStatusResult struct {
	Answering harnessAnswering `json:"answering"`
	Message   *string          `json:"message"`
}
type harnessAnswering struct {
	Ready   bool    `json:"ready"`
	Reason  *string `json:"reason"`
	AgentID *string `json:"agentId"`
	Model   *string `json:"model"`
}

func (h agentHandlers) handleHarnessStatus(ctx context.Context, req jsonrpcRequest) {
	result := harnessStatusResult{}
	refuse := func(reason, message string) { result.Answering.Reason = &reason; result.Message = &message }
	if h.harness == nil {
		refuse("unavailable", "Choose an available agent in Settings")
	} else {
		result.Answering.AgentID = &h.harnessID
		// Status describes a new conversation; the empty id skips durable pins.
		model, err := h.answeringModel(ctx, "")
		if err != nil {
			reason := "unavailable"
			if errors.Is(err, profile.ErrRoleModelGone) {
				reason = "model-gone"
				// Keep the removed choice visible rather than showing the agent's default.
				_ = h.configOp.Run(ctx, func(_ context.Context, svc capability.ConfigService) error {
					assignments, loadErr := svc.ListRoleAssignments()
					for _, assignment := range assignments {
						if assignment.Role == profile.RoleAnswering && assignment.AgentID == h.harnessID {
							result.Answering.Model = &assignment.Model
							break
						}
					}
					return loadErr
				})
				refuse(reason, fmt.Sprint(err))
			} else {
				// Catalogue discovery can fail because the agent is absent or not
				// configured. Keep its actionable readiness diagnostic in that case.
				switch ready := h.harness.Readiness(ctx); ready.State {
				case agentharness.NotInstalled:
					refuse("not-installed", ready.Message)
				case agentharness.NotConfigured:
					refuse("not-configured", ready.Message)
				default:
					refuse(reason, fmt.Sprint(err))
				}
			}
		} else {
			if model != "" {
				result.Answering.Model = &model
			}
			var ready agentharness.Readiness
			if checker, ok := h.harness.(agentharness.ModelReadiness); ok {
				ready = checker.ReadinessForModel(ctx, model)
			} else {
				ready = h.harness.Readiness(ctx)
			}
			switch ready.State {
			case agentharness.Ready:
				result.Answering.Ready = true
			case agentharness.NotInstalled:
				refuse("not-installed", ready.Message)
			case agentharness.NotConfigured:
				refuse("not-configured", ready.Message)
			default:
				refuse("unavailable", ready.Message)
			}
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(result))
}
