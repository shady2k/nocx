package transport

import (
	"context"

	"github.com/shady2k/nocx/internal/notify"
)

// notifyCatalogueResult is the wire shape of notify.catalogue. The transport
// exposes the catalogue's human vocabulary without making the renderer carry a
// second declaration of notification kinds.
type notifyCatalogueResult struct {
	Kinds []notifyCatalogueKind `json:"kinds"`
}

type notifyCatalogueKind struct {
	Kind        notify.Kind `json:"kind"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
}

func notifyCatalogueResultFor(c *notify.Catalogue) notifyCatalogueResult {
	kinds := c.PresentedKinds()
	result := notifyCatalogueResult{Kinds: make([]notifyCatalogueKind, 0, len(kinds))}
	for _, kind := range kinds {
		result.Kinds = append(result.Kinds, notifyCatalogueKind{
			Kind:        kind.Kind,
			Label:       kind.Label,
			Description: kind.Description,
		})
	}
	return result
}

func (s *WSServer) notifyCatalogueSpec() methodSpec {
	return regResponder(s.lane, "notify.catalogue", noParams(), func(r Responder) handlerFunc {
		return func(_ context.Context, req jsonrpcRequest) {
			_ = r.TryResult(req.ID, mustMarshal(notifyCatalogueResultFor(notify.DefaultCatalogue())))
		}
	})
}
