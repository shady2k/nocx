package agenttools

import (
	"strings"

	"github.com/shady2k/nocx/internal/content"
)

// DefaultSchemaTokenLimit is the measured point at which presenting every
// eligible schema costs more than the model context budget reserved for the
// conversation. The estimate is schema bytes divided by four, not tool count.
const DefaultSchemaTokenLimit = 4096

// The default essential set keeps direct file reads available for workspace
// facts and current-session reads available for pane facts.
var defaultEssentialTools = []string{"files.read", "session.read"}

// PresentationConfig controls the model-facing projection. Loaded contains
// names only; Project resolves each name back through the immutable registry so
// a changed declaration cannot leave a stale schema in the model context.
type PresentationConfig struct {
	Lazy             bool
	Essential        []string
	Loaded           []string
	SchemaTokenLimit int
}

// PresentationProjection is the current model-facing view and the catalog of
// eligible tools that are intentionally hidden from that view.
type PresentationProjection struct {
	Visible      []Tool
	Catalog      []Tool
	Lazy         bool
	SchemaTokens int
}

// Project derives the presentation view from the current grant every time it
// is called. Loaded names are presentation state only: ForGrant remains the
// authority boundary and removes a name whenever the grant no longer permits
// it.
func (r Registry) Project(g content.Grant, cfg PresentationConfig) PresentationProjection {
	eligible := r.ForGrant(g)
	tokens := SchemaTokenEstimate(eligible)
	limit := cfg.SchemaTokenLimit
	if limit <= 0 {
		limit = DefaultSchemaTokenLimit
	}
	hasCatalogOnly := false
	for _, tool := range eligible {
		if tool.CatalogOnly {
			hasCatalogOnly = true
			break
		}
	}
	lazy := cfg.Lazy && tokens >= limit
	if !lazy && !hasCatalogOnly {
		return PresentationProjection{Visible: eligible, SchemaTokens: tokens}
	}

	if cfg.Essential == nil {
		cfg.Essential = defaultEssentialTools
	}

	essential := make(map[string]struct{}, len(cfg.Essential))
	for _, name := range cfg.Essential {
		essential[name] = struct{}{}
	}
	loaded := make(map[string]struct{}, len(cfg.Loaded))
	for _, name := range cfg.Loaded {
		loaded[name] = struct{}{}
	}

	visible := make([]Tool, 0, len(essential)+len(loaded))
	catalog := make([]Tool, 0, len(eligible))
	for _, tool := range eligible {
		if tool.CatalogOnly {
			if _, ok := loaded[tool.Name]; ok {
				visible = append(visible, tool)
			} else {
				catalog = append(catalog, tool)
			}
			continue
		}
		if !lazy {
			visible = append(visible, tool)
			continue
		}
		if _, ok := essential[tool.Name]; ok {
			visible = append(visible, tool)
			continue
		}
		if _, ok := loaded[tool.Name]; ok {
			visible = append(visible, tool)
			continue
		}
		catalog = append(catalog, tool)
	}
	return PresentationProjection{
		Visible:      visible,
		Catalog:      catalog,
		Lazy:         true,
		SchemaTokens: tokens,
	}
}

// Search returns descriptions and schemas from the current eligible set, not
// from Registry.All. A visible tool is omitted because it is already in the
// model context; the result is therefore exactly the hidden catalog slice that
// matches the query.
func (r Registry) Search(g content.Grant, query string, visible []Tool) []Tool {
	eligible := r.ForGrant(g)
	visibleNames := make(map[string]struct{}, len(visible))
	for _, tool := range visible {
		visibleNames[tool.Name] = struct{}{}
	}
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]Tool, 0, len(eligible))
	for _, tool := range eligible {
		if _, ok := visibleNames[tool.Name]; ok {
			continue
		}
		searchText := tool.Name + " " + tool.Description + " " + tool.CatalogSearchText
		if query != "" && !strings.Contains(strings.ToLower(searchText), query) {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// SchemaTokenEstimate measures the schemas presented to a model. It is a
// deterministic provider-neutral estimate: four UTF-8 bytes per token.
func SchemaTokenEstimate(tools []Tool) int {
	tokens := 0
	for _, tool := range tools {
		tokens += (len(tool.ParamsSchema) + 3) / 4
	}
	return tokens
}
