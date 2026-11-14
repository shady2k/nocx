package harness

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// cataloguePassTimeout bounds shared work independently of any one caller.
const cataloguePassTimeout = 30 * time.Second

// AgentCatalogue is the connector's readiness and, when ready, its models.
type AgentCatalogue struct {
	ID        string
	Readiness Readiness
	Models    []Model
}

// CatalogueService caches connector facts until Settings requests a refresh.
type CatalogueService struct {
	mu         sync.Mutex
	connectors map[string]Harness
	cached     []AgentCatalogue
	flight     *cataloguePass
}

type cataloguePass struct {
	done chan struct{}
	rows []AgentCatalogue
	err  error
}

// NewCatalogue takes a snapshot of the connectors wired at the composition root.
func NewCatalogue(connectors map[string]Harness) *CatalogueService {
	return &CatalogueService{connectors: maps.Clone(connectors)}
}

// Catalogue returns one shared pass. A cancelled waiter leaves the bounded pass
// running for other callers; a failed pass never replaces the last good cache.
func (c *CatalogueService) Catalogue(ctx context.Context, refresh bool) ([]AgentCatalogue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	pass := c.flight
	if pass == nil {
		if c.cached != nil && !refresh {
			rows := cloneCatalogue(c.cached)
			c.mu.Unlock()
			return rows, nil
		}
		pass = &cataloguePass{done: make(chan struct{})}
		c.flight = pass
		go c.completePass(context.WithoutCancel(ctx), pass)
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-pass.done:
		return cloneCatalogue(pass.rows), pass.err
	}
}

func (c *CatalogueService) completePass(ctx context.Context, pass *cataloguePass) {
	ctx, cancel := context.WithTimeout(ctx, cataloguePassTimeout)
	defer cancel()
	rows, err := c.catalogue(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	pass.rows, pass.err = rows, err
	if err == nil {
		c.cached = rows
	}
	c.flight = nil
	close(pass.done)
}

func (c *CatalogueService) catalogue(ctx context.Context) ([]AgentCatalogue, error) {
	rows := make([]AgentCatalogue, 0, len(c.connectors))
	for _, id := range slices.Sorted(maps.Keys(c.connectors)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		connector := c.connectors[id]
		row := AgentCatalogue{ID: id, Readiness: connector.Readiness(ctx)}
		if row.Readiness.State == Ready {
			if cataloguer, ok := connector.(Cataloguer); ok {
				models, err := cataloguer.Models(ctx)
				if err != nil {
					return nil, fmt.Errorf("harness catalogue %s: models: %w", id, err)
				}
				row.Models = slices.Clone(models)
			}
		}
		rows = append(rows, row)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func cloneCatalogue(rows []AgentCatalogue) []AgentCatalogue {
	cloned := slices.Clone(rows)
	for i := range cloned {
		cloned[i].Models = slices.Clone(rows[i].Models)
	}
	return cloned
}
