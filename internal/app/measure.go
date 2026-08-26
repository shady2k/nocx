package app

// The dev measurement read (nocx-d6gn4.9, consumed by nocx-d6gn4.10).
//
// It lives at the composition root because that is where the ledger already
// is. The alternative — a command of its own that opens content.db a second
// time — was rejected for a reason worth writing down: content.Open REBUILDS
// the file when the schema differs and applies the retention policy it is
// given, so a "read-only report" opening the store its own way could discard
// the history it was asked to describe. A reader must not be able to do that.

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// measureQueryLimit is how many action entries one report reads. A dev read
// over one session, not a paging API: when it saturates, the report says so
// rather than quietly describing a prefix as the whole.
const measureQueryLimit = 500

// MeasureAgentRuns reports what the assistant's recorded tool calls say about
// how DEEP the dependent chains of real tasks are — the figure the
// program-carrier experiment is decided on.
//
// It reads through the ledger's own QueryEntries, never a second SQL path.
// The second return value is true when the read saturated its limit, which
// means the figures describe the most recent entries and not all of them.
func (a *App) MeasureAgentRuns(ctx context.Context) ([]assistant.RunMeasurement, bool, error) {
	if a.content == nil {
		return nil, false, fmt.Errorf("no ledger is wired: durable history is off, so there is nothing recorded to measure")
	}
	page, err := a.content.Ledger().QueryEntries(ctx, content.LedgerQuery{
		Kind:  content.EntryAction,
		Limit: measureQueryLimit,
	})
	if err != nil {
		return nil, false, fmt.Errorf("query action entries: %w", err)
	}
	return assistant.MeasureRuns(page.Entries), !page.Exhausted, nil
}
