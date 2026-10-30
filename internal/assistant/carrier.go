package assistant

import (
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// unknownToolResult is policy-aware but effect-free. Both engine adapters use
// this response; an undeclared name can never dispatch through this path.
func unknownToolResult(registry agenttools.Registry, grant content.Grant, name string, toolName func(string) string) string {
	if refused := registry.RefusedEffects(grant, name); len(refused) > 0 {
		if len(refused) == 1 {
			return refusalResult(renderModelToolName(name, toolName), RefusedByDecision, "")
		}
		return withheldToolResult(renderModelToolName(name, toolName), refused)
	}
	names := presentationToolNames(registry.ForGrant(grant))
	for i, name := range names {
		names[i] = renderModelToolName(name, toolName)
	}
	displayName := name
	if _, known := registry.Lookup(name); known {
		displayName = renderModelToolName(name, toolName)
	}
	return fmt.Sprintf("There is no such tool %q. The tools you may call are the ones you were given and no others: %s.",
		displayName, strings.Join(names, ", "))
}

func renderModelToolName(name string, mapper func(string) string) string {
	if mapper != nil {
		return mapper(name)
	}
	return name
}
