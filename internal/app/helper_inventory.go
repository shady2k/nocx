package app

import (
	"context"
	"errors"

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
//
// TWO ROUTES, BECAUSE TWO KINDS OF HELPER HOLD SESSIONS (nocx-s8mfn). The
// registry answers for the far generations this coordinator holds; the local
// opener answers for the panes THIS machine's daemon is carrying — which since
// nocx-50w7p.5 includes every ssh pane, whose destination is remote while its
// carrier is here. Until this arm existed the RPC walked the registry alone and
// refused ("no active helper") for exactly those panes, although a helper was
// holding every one of them.
//
// THE ORDER MATCHES paneScreen.owner's: this machine's opener first, then the
// far registry. One owner per fact (AD-8) — the inventory does not decide which
// helper could be holding what, it asks the two parties that already know.
type helperSessionInventories struct {
	registry *helperRegistry
	// local is this machine's route, and nil is a legitimate wiring: a
	// composition root that builds no local opener has only far generations to
	// answer for, and this file must not invent a local one.
	local localSessionHoldings
}

// localSessionHoldings is this machine's daemon as the inventory needs it: the
// entries of the sessions this coordinator holds on it, or nil when it holds
// none. It is a one-method seam rather than the whole opener because that is
// the entire question the aggregate asks — the localHelperOpener satisfies it,
// and a test can answer it without a daemon.
type localSessionHoldings interface {
	heldSessions(ctx context.Context) ([]helperclient.SessionEntry, error)
}

// errNoHelperToAsk is the refusal the inventory gives when NOBODY could be
// asked: the registry holds no far generation and this machine's opener holds
// no session on its daemon. An empty answer is only safe to read as "no
// sessions" after a helper actually answered, so this is an error and never an
// empty list.
var errNoHelperToAsk = errors.New(
	"helper session inventory unavailable: no helper is holding a session for this coordinator")

func (p *helperSessionInventories) Sessions(ctx context.Context) ([]helperclient.SessionEntry, error) {
	local, err := p.localSessions(ctx)
	if err != nil {
		return nil, err
	}
	// ASKED ONLY WHEN THERE IS SOMETHING TO ASK. registry.sessions reports "no
	// active helper" as an error, and that error is not a failure of a helper —
	// it is this route having nothing to say, which is a legitimate state
	// beside a local arm that answered. Which of the two it is has to be
	// decided before the call, because the call conflates them; and when it
	// decides "there is something", a later failure stays fatal: a partial list
	// must never be presented as a complete inventory.
	if !p.registry.holdsAny() {
		if local == nil {
			return nil, errNoHelperToAsk
		}
		return local, nil
	}
	remote, err := p.registry.sessions(ctx)
	if err != nil {
		return nil, err
	}
	return append(local, remote...), nil
}

// localSessions is this machine's half, and the place the "nothing held" case
// is named: a nil opener and an opener holding nothing are the same answer to
// the aggregate, so neither needs a branch of its own above.
func (p *helperSessionInventories) localSessions(ctx context.Context) ([]helperclient.SessionEntry, error) {
	if p.local == nil {
		return nil, nil
	}
	return p.local.heldSessions(ctx)
}

// holdsAny reports whether this coordinator holds any helper generation at all,
// which is the difference between "this route has nothing to ask" and "this
// route's helper failed". It is the registry's OWN map (hosts), read under the
// registry's own lock, and it lives in this file because the inventory is the
// caller that needs the distinction: it is the same state `sessions` walks, not
// a second record of it.
func (r *helperRegistry) holdsAny() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hosts) > 0
}

var _ interface {
	Sessions(context.Context) ([]helperclient.SessionEntry, error)
} = (*helperSessionInventories)(nil)

var _ sessionInventory = (*helperSessionInventory)(nil)
