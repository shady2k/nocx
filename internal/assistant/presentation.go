package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/shady2k/nocx/internal/agenttools"
)

const toolsSearchName = "tools.search"

type presentationState struct {
	mu     sync.RWMutex
	loaded map[string]struct{}
}

func newPresentationState(names []string) *presentationState {
	s := &presentationState{loaded: make(map[string]struct{}, len(names))}
	s.Load(names...)
	return s
}

func (s *presentationState) Load(names ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			s.loaded[name] = struct{}{}
		}
	}
}

func (s *presentationState) Names() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.loaded))
	for name := range s.loaded {
		names = append(names, name)
	}
	return names
}

func (m *policyMiddleware) presentationProjection() agenttools.PresentationProjection {
	grant := m.kernel.grant
	if m.grantProvider != nil {
		grant = m.grantProvider()
	}
	cfg := m.presentation
	cfg.Loaded = m.presentationState.Names()
	return m.kernel.registry.Project(grant, cfg)
}

func (m *policyMiddleware) searchTools(ctx context.Context, rawArgs string) (string, error) {
	var req struct {
		Query string `json:"query"`
	}
	decoder := json.NewDecoder(strings.NewReader(rawArgs))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return "", fmt.Errorf("tools.search: invalid arguments: %w", err)
	}
	projection := m.presentationProjection()
	grant := m.kernel.grant
	if m.grantProvider != nil {
		grant = m.grantProvider()
	}
	matches := m.kernel.registry.Search(grant, req.Query, projection.Visible)
	m.rememberLoaded(presentationToolNames(matches)...)
	if m.kernel.log != nil {
		m.kernel.log.Info("assistant tool catalog searched", "query", req.Query, "matches", len(matches))
	}
	result := struct {
		Query string             `json:"query"`
		Tools []searchToolResult `json:"tools"`
	}{Query: req.Query, Tools: make([]searchToolResult, 0, len(matches))}
	for _, match := range matches {
		result.Tools = append(result.Tools, searchToolResult{
			Name:        match.Name,
			Description: match.Description,
			Parameters:  match.ParamsSchema,
		})
	}
	return marshalSearchResult(result)
}

type searchToolResult struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func marshalSearchResult(result any) (string, error) {
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("tools.search: encode result: %w", err)
	}
	return string(out), nil
}

func searchToolInfo(raw []byte) (*schema.ToolInfo, error) {
	var params jsonschema.Schema
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("tools.search: params schema: %w", err)
	}
	return &schema.ToolInfo{
		Name:        toolsSearchName,
		Desc:        "Find an eligible tool by name or capability words, then call it by its returned name.",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&params),
	}, nil
}

func (m *policyMiddleware) searchTool() (tool.BaseTool, error) {
	info, err := searchToolInfo(m.searchSchema)
	if err != nil {
		return nil, err
	}
	return &presentationSearchTool{middleware: m, info: info}, nil
}

type presentationSearchTool struct {
	middleware *policyMiddleware
	info       *schema.ToolInfo
}

func (t *presentationSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *presentationSearchTool) InvokableRun(ctx context.Context, rawArgs string, _ ...tool.Option) (string, error) {
	return t.middleware.searchTools(ctx, rawArgs)
}

func presentationToolNames(tools []agenttools.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func declaredToolInfos(permitted []agenttools.Tool) ([]*schema.ToolInfo, error) {
	declared, err := declaredTools(permitted)
	if err != nil {
		return nil, err
	}
	infos := make([]*schema.ToolInfo, 0, len(declared))
	for _, candidate := range declared {
		info, err := candidate.Info(context.Background())
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func (m *policyMiddleware) rememberLoaded(names ...string) {
	m.presentationState.Load(names...)
}
