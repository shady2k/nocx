package transport

import (
	"context"
	"errors"
	"fmt"

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
			}
			refuse(reason, fmt.Sprint(err))
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
