package assistant

// THE DECLARED-CALL CARRIER: the model names one effect at a time, the host
// runs it through the effect kernel, and the result returns to the model
// before it names the next.
//
// The effect kernel remains the single owner of validation, policy, attempt
// recording, narrowed capability, execution and egress screening. This
// interface keeps the framework adapter independent from that owner.

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/shady2k/nocx/internal/content"

	"github.com/shady2k/nocx/internal/agenttools"
)

type carrier interface {
	Declare(permitted []agenttools.Tool) ([]tool.BaseTool, error)
	Invoke(ctx context.Context, name, callID, rawArgs string) (modelResult, error)
	FrameForModel(tool, result string) string
	UnknownTool(name, rawArgs string) (string, error)
}

// ── The declared-call carrier ──────────────────────────────────────────

// callsCarrier is the kernel offered directly: every declared tool is a tool
// the model may name, and one invocation is one effect.
type callsCarrier struct{ *effectKernel }

func (c *callsCarrier) Invoke(ctx context.Context, name, callID, rawArgs string) (modelResult, error) {
	return c.effectKernel.invokeClassified(ctx, name, callID, rawArgs)
}

func (c *callsCarrier) Declare(permitted []agenttools.Tool) ([]tool.BaseTool, error) {
	return declaredTools(permitted)
}

func (c *callsCarrier) UnknownTool(name, _ string) (string, error) {
	if decl, ok := c.effectKernel.registry.Lookup(name); ok &&
		c.effectKernel.grant.Policy.DecisionFor(decl.Effect) == content.DecisionRefuse {
		return refusalResult(name, RefusedByDecision, ""), nil
	}
	return fmt.Sprintf("There is no such tool %q. The tools you may call are the ones you were given and no others: %s.",
		name, strings.Join(permittedNames(c.effectKernel), ", ")), nil
}

func permittedNames(k *effectKernel) []string {
	var out []string
	for _, t := range k.registry.ForGrant(k.grant) {
		out = append(out, t.Name)
	}
	return out
}
