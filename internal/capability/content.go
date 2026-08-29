package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ContentService is the content domain surface: the durable command-history
// store (ADR-0011 §5). It is what a ContentOperation hands its callback.
// Read policy: reads participate in the content gate — the store is one
// database and history.record writes rows the query reads.
type ContentService interface {
	// QueryHistory serves history.query: one page of the recall ladder,
	// answered FROM THE LEDGER since nocx-rtg0.19 retired command_history.
	// The rung, its coordinates and the cursor arrive already expressed as
	// the ledger's own query — the transport owns the translation, because
	// it owns the wire's vocabulary and this layer owns none of it.
	QueryHistory(ctx context.Context, q content.LedgerQuery) (content.LedgerPage, error)
	// RecordCommand stores one completed command (history.record) and
	// returns the entry id the backend minted for it. When the live History
	// policy is off it succeeds and returns ("", nil) — a command runs and
	// no row appears, never an error, and the empty id says there is
	// nothing to reference.
	RecordCommand(ctx context.Context, in content.CompletedCommand) (string, error)
	// The capture-save link rewrite is NOT here. It is one behaviour with
	// one seam — CaptureSaveService.RewriteRedaction, which is the only
	// thing secrets.captureSave ever reached — and the copy that used to sit
	// on this interface had no caller at all (AD-8: a second surface for one
	// behaviour goes out of step with the first the moment either changes).
}

// ContentOperation is the typed operation for the content domain. Its gate
// is [content].
type ContentOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, ContentService) error) error
}

// NewContentOperation builds a ContentOperation that acquires the content
// gate before the execution lane.
func NewContentOperation(contentGate, lane control.Admission, db content.ContentDB) ContentOperation {
	g := &guard{}
	return newOperation[ContentService](Direct("ContentOperation"), control.NewComposite(contentGate, lane), g, newContentService(g, db))
}

// newContentService builds the concrete content service bound to guard g.
func newContentService(g *guard, db content.ContentDB) *contentService {
	return &contentService{guard: g, db: db}
}

type contentService struct {
	guard *guard
	db    content.ContentDB
}

func (s *contentService) QueryHistory(ctx context.Context, q content.LedgerQuery) (content.LedgerPage, error) {
	if err := s.guard.check(); err != nil {
		return content.LedgerPage{}, err
	}
	return s.db.Ledger().QueryEntries(ctx, q)
}

func (s *contentService) RecordCommand(ctx context.Context, in content.CompletedCommand) (string, error) {
	if err := s.guard.check(); err != nil {
		return "", err
	}
	return s.db.Ledger().RecordCompleted(ctx, in)
}
