package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/mcp"
)

func executeMCP(ctx context.Context, capability agenttools.Capability, args json.RawMessage, runtime mcp.Runtime) (string, error) {
	scope, ok := capability.(*agenttools.MCPScope)
	if !ok {
		return "", fmt.Errorf("MCP tool: capability is %T, not *agenttools.MCPScope", capability)
	}
	if runtime == nil {
		return "", errors.New("MCP tool: no runtime is wired for this run")
	}
	invocation, err := scope.Invocation(args)
	if err != nil {
		return "", fmt.Errorf("MCP tool: %w", err)
	}
	result, err := runtime.Invoke(ctx, invocation)
	if err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		message, boundErr := boundedMCPError(err, ctx)
		if boundErr != nil {
			return "", boundErr
		}
		return message, nil
	}
	if result.Text == nil {
		result.Text = []string{}
	}
	if result.Resources == nil {
		result.Resources = []mcp.Resource{}
	}
	if result.Omitted == nil {
		result.Omitted = []mcp.Omitted{}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", errors.New("MCP tool: result could not be encoded")
	}
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	if int64(len(encoded)) > bound.MaxBytes {
		return "", errors.New("MCP tool: runtime returned a result beyond the declared bound")
	}
	return string(encoded), nil
}

func boundedMCPError(cause error, ctx context.Context) (string, error) {
	bound, err := toolBound(ctx)
	if err != nil {
		return "", err
	}
	message := "MCP tool call failed: " + cause.Error()
	limit := int(bound.MaxBytes)
	if limit < 1 {
		return "MCP tool call failed", nil
	}
	if len(message) > limit {
		message = message[:limit]
		for len(message) > 0 && !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return message, nil
}
