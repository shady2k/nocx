package transport

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/capability"
	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/profile"
)

// answeringModel reads the conversation before the role: changed choices only
// govern new conversations. Catalogue validation never substitutes a model.
func (h agentHandlers) answeringModel(ctx context.Context, conversation string) (string, error) {
	var model string
	if conversation != "" {
		if err := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
			var err error
			model, err = svc.ConversationModel(ctx, conversation, h.harnessID)
			return err
		}); err != nil {
			return "", err
		}
	}
	var models []agentharness.Model
	if catalogue, ok := h.harness.(agentharness.Cataloguer); ok {
		var err error
		models, err = catalogue.Models(ctx)
		if err != nil {
			return "", fmt.Errorf("the answering agent's model catalogue could not be read: %w", err)
		}
	}
	if model != "" {
		if _, ok := h.harness.(agentharness.Cataloguer); ok {
			found := false
			for _, candidate := range models {
				if candidate.ID == model {
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("the conversation's model %q is no longer in the answering agent's catalogue; start a new conversation and choose an available model in Settings", model)
			}
		}
		return model, nil
	}
	if h.configOp == nil {
		return "", nil
	}
	err := h.configOp.Run(ctx, func(_ context.Context, svc capability.ConfigService) error {
		var err error
		model, err = svc.ResolveRole(profile.RoleAnswering, h.harnessID, models)
		return err
	})
	return model, err
}
