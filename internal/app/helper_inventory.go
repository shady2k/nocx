package app

import (
	"context"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
)

// helperSessionInventory adapts one live helper connection to the
// reconciliation seam. Generation is the id-space owner; host and account
// select the execution target before that generation is queried.
type helperSessionInventory struct {
	client     *helperclient.Client
	generation string
	host       string
	account    string
}

func (i *helperSessionInventory) Host() string    { return i.host }
func (i *helperSessionInventory) Account() string { return i.account }

func (i *helperSessionInventory) Generation() string { return i.generation }

// Owns answers ownership of the id space, not whether a particular session is
// live. An absent id in an owned generation is the answer that can safely
// produce VerdictAbsent; an empty generation owns nothing.
func (i *helperSessionInventory) Owns(_ string) bool { return i.generation != "" }

func (i *helperSessionInventory) LiveSessions(ctx context.Context) (map[string]struct{}, error) {
	entries, err := i.client.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.HostSessionID.Generation != i.generation {
			continue
		}
		live[entry.HostSessionID.Session] = struct{}{}
	}
	return live, nil
}

// helperSessionInventories is the live helper inventory provider used by the
// transport RPC. It intentionally returns coordinator DTOs from helper/client
// and never exposes the frozen proto package above that boundary.
type helperSessionInventories struct {
	registry *helperRegistry
}

func (p *helperSessionInventories) Sessions(ctx context.Context) ([]helperclient.SessionEntry, error) {
	return p.registry.sessions(ctx)
}

var _ interface {
	Sessions(context.Context) ([]helperclient.SessionEntry, error)
} = (*helperSessionInventories)(nil)

var _ sessionInventory = (*helperSessionInventory)(nil)
