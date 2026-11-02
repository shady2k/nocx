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
	ID          string      `json:"id"`
	Kind        notify.Kind `json:"kind"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	// LabelKey and DescriptionKey are the translation keys the label and
	// description resolve through (localisation design Q2, Option B):
	// minted from the kind id, never declared by hand. The English strings
	// beside them are the visible fallback.
	LabelKey       string `json:"labelKey,omitempty"`
	DescriptionKey string `json:"descriptionKey,omitempty"`
}

func notifyCatalogueResultFor(c *notify.Catalogue) notifyCatalogueResult {
	kinds := c.PresentedKinds()
	result := notifyCatalogueResult{Kinds: make([]notifyCatalogueKind, 0, len(kinds))}
	for _, kind := range kinds {
		result.Kinds = append(result.Kinds, notifyCatalogueKind{
			ID:             kind.ID,
			Kind:           kind.Kind,
			Label:          kind.Label,
			Description:    kind.Description,
			LabelKey:       notify.KindLabelKey(kind.ID),
			DescriptionKey: notify.KindDescriptionKey(kind.ID),
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
