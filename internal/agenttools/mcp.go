package agenttools

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/mcp"
	"github.com/shady2k/nocx/internal/profile"
)

const mcpDescription = "Call one enabled tool on a configured MCP server. The server is activated only after nocx validates and authorizes this exact call."

var mcpResultSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["serverId","tool","isError","text","resources","omitted"],
  "properties":{
    "serverId":{"type":"string"},
    "tool":{"type":"string"},
    "isError":{"type":"boolean"},
    "text":{"type":"array","maxItems":64,"items":{"type":"string"}},
    "structuredContent":{},
    "resources":{"type":"array","maxItems":64,"items":{"type":"object","additionalProperties":false,"required":["uri"],"properties":{"uri":{"type":"string"},"name":{"type":"string"},"title":{"type":"string"},"mimeType":{"type":"string"},"text":{"type":"string"}}}},
    "omitted":{"type":"array","maxItems":64,"items":{"type":"object","additionalProperties":false,"required":["type","reason"],"properties":{"type":{"type":"string"},"mimeType":{"type":"string"},"bytes":{"type":"integer"},"reason":{"type":"string"}}}}
  }
}`)

// MCPCatalogSnapshot is a validated, immutable copy of one persisted server
// catalog. Its fields are deliberately private: callers can construct it only
// from a profile record that passed the profile and activation validators.
// Constructing a snapshot performs no discovery and never touches Runtime.
type MCPCatalogSnapshot struct {
	activation mcp.Activation
	state      profile.MCPCatalogState
	tools      []mcpCatalogTool
}

type mcpCatalogTool struct {
	name        string
	description string
	enabled     bool
}

// NewMCPCatalogSnapshot copies one server record into the run-facing shape.
// A later mutation of the record cannot change a registry already composed
// from this snapshot.
func NewMCPCatalogSnapshot(server profile.MCPServer) (MCPCatalogSnapshot, error) {
	activation, err := mcp.ActivationFromServer(server)
	if err != nil {
		return MCPCatalogSnapshot{}, err
	}
	tools := make([]mcpCatalogTool, len(server.Catalog.Tools))
	for i, tool := range server.Catalog.Tools {
		tools[i] = mcpCatalogTool{name: tool.Name, description: tool.Description, enabled: tool.Enabled}
	}
	return MCPCatalogSnapshot{activation: activation, state: server.Catalog.State, tools: tools}, nil
}

// MCPModelName is the stable provider-safe name for a remote tool identity.
func MCPModelName(serverID, remoteTool string) string {
	digest := sha256.Sum256([]byte(serverID + "\x00" + remoteTool))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])
	return "mcp_" + strings.ToLower(encoded[:32])
}

// MCPScope is the exact authority handed to the MCP executor. The activation
// details are private so the executor can invoke this server/tool pair but
// cannot substitute another identity after Narrow.
type MCPScope struct {
	RunID            string
	ServerID         string
	ServerRevision   uint64
	CatalogDigest    string
	RemoteTool       string
	DescriptorDigest string
	Destination      string
	activation       mcp.Activation
}

// Invocation binds validated arguments to the exact immutable activation that
// produced this capability.
func (s *MCPScope) Invocation(arguments json.RawMessage) (mcp.Invocation, error) {
	if s == nil || s.RunID == "" || s.ServerID == "" || s.RemoteTool == "" || s.DescriptorDigest == "" || s.Destination == "" {
		return mcp.Invocation{}, errors.New("MCP capability is incomplete")
	}
	return mcp.Invocation{
		RunID:            s.RunID,
		Activation:       s.activation,
		RemoteTool:       s.RemoteTool,
		DescriptorDigest: s.DescriptorDigest,
		Arguments:        append(json.RawMessage(nil), arguments...),
	}, nil
}

// WithMCP returns a new registry containing the receiver's tools plus enabled
// tools from fresh, enabled snapshots. It never mutates the receiver and never
// calls an MCP runtime. Invalid rows and model-name collisions fail closed.
func (r Registry) WithMCP(snapshots []MCPCatalogSnapshot) (Registry, error) {
	tools := append([]Tool(nil), r.tools...)
	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		names[tool.Name] = struct{}{}
	}
	for _, snapshot := range snapshots {
		if snapshot.state != profile.MCPCatalogFresh || !snapshot.activation.Enabled {
			continue
		}
		if len(snapshot.tools) != len(snapshot.activation.Tools) {
			return Registry{}, errors.New("MCP catalog snapshot tool set is inconsistent")
		}
		destination, err := mcpDestination(snapshot.activation)
		if err != nil {
			return Registry{}, err
		}
		for i, row := range snapshot.tools {
			if !row.enabled {
				continue
			}
			descriptor := snapshot.activation.Tools[i]
			if descriptor.Name != row.name {
				return Registry{}, errors.New("MCP catalog snapshot tool order is inconsistent")
			}
			name := MCPModelName(snapshot.activation.ServerID, descriptor.Name)
			if _, exists := names[name]; exists {
				return Registry{}, fmt.Errorf("MCP model tool name collision %q", name)
			}
			names[name] = struct{}{}
			activation := snapshot.activation
			remoteTool := descriptor.Name
			descriptorDigest := descriptor.DescriptorDigest
			serverID := activation.ServerID
			serverRevision := activation.ServerRevision
			catalogDigest := activation.CatalogDigest
			resource := func(_ map[string]any, _ RunContext) ([]ResourceRef, error) {
				return []ResourceRef{{Kind: content.ResourceDestination, ID: destination}}, nil
			}
			narrow := func(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
				if runCtx.RunID == "" {
					return nil, errors.New("MCP call has no run identity")
				}
				if len(resources) != 1 || resources[0].Kind != content.ResourceDestination || resources[0].ID != destination || !resourceInGrant(grant, resources[0]) {
					return nil, errors.New("MCP destination is outside the run grant")
				}
				return &MCPScope{
					RunID: runCtx.RunID, ServerID: serverID, ServerRevision: serverRevision,
					CatalogDigest: catalogDigest, RemoteTool: remoteTool,
					DescriptorDigest: descriptorDigest, Destination: destination,
					activation: activation,
				}, nil
			}
			searchText := strings.Join([]string{activation.Name, remoteTool, row.description}, " ")
			tools = append(tools, Tool{
				Declaration: Declaration{
					Name: name, Description: mcpDescription,
					Effect:            []content.Effect{content.EffectDelegate},
					OutputTrust:       OutputTrustUntrusted,
					ResultBound:       ResultBound{MaxBytes: int64(activation.Limits.MaxResultBytes), Truncation: TruncationDropTail},
					Deadline:          activation.Limits.CallTimeout,
					Cancellation:      CancellationReturnError,
					ResourceKinds:     []content.ResourceKind{content.ResourceDestination},
					ResolveResources:  resource,
					Executes:          InMCP,
					Narrow:            narrow,
					CatalogSearchText: searchText,
				},
				CatalogOnly:  true,
				Effect:       content.EffectDelegate,
				ParamsSchema: append(json.RawMessage(nil), descriptor.InputSchema...),
				ResultSchema: append(json.RawMessage(nil), mcpResultSchema...),
			})
		}
	}
	return Registry{tools: tools}, nil
}

func mcpDestination(activation mcp.Activation) (string, error) {
	switch activation.Transport {
	case mcp.TransportStdio:
		if activation.ServerID == "" {
			return "", errors.New("MCP stdio server has no identity")
		}
		return "mcp+stdio:" + activation.ServerID, nil
	case mcp.TransportStreamableHTTP:
		if activation.HTTP == nil || activation.HTTP.Endpoint == "" {
			return "", errors.New("MCP HTTP server has no destination")
		}
		return activation.HTTP.Endpoint, nil
	default:
		return "", errors.New("MCP server has unsupported transport")
	}
}
