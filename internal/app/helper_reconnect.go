package app

import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// RegisterReconnect registers the connection supervisor's operation before any
// shell is torn down. The supervisor rejects a connection that recovered.
func (o *localHelperOpener) RegisterReconnect(ctx context.Context, cfg session.Config, sequence uint64) error {
	if cfg.Remote == nil {
		return errors.New("reconnect has no SSH destination")
	}
	target, err := o.resolveTarget(ctx, cfg.Host, session.SSHOptionsFromConfig(cfg.Remote))
	if err != nil {
		return err
	}
	c, _, err := o.connect(ctx)
	if err != nil {
		return err
	}
	return c.RegisterReconnect(ctx, ssh.WireDestination(target), sequence)
}

type paneConnectionOwner struct {
	*panedriver.Driver
	opener *localHelperOpener
}

// PaneOpening paints the pane's first observation before the opener is asked
// anything. It binds a provisional connection id; the opener's real bind
// replaces it before the spawn, and the provisional entry is dropped.
func (o paneConnectionOwner) PaneOpening(paneID string) uint64 {
	if o.Driver == nil || paneID == "" {
		return 0
	}
	return o.Driver.Bind(paneID, "pending:"+paneID)
}

// PaneOpeningFailed settles a provisional opening whose opener failed before
// the real bind, so the pane is not left counting "Connecting".
func (o paneConnectionOwner) PaneOpeningFailed(paneID string, generation uint64) {
	if o.Driver != nil {
		o.Driver.FailOpening(paneID, generation)
	}
}

func (o paneConnectionOwner) PaneClosed(paneID string) {
	o.Driver.PaneClosed(paneID)
	o.opener.mu.Lock()
	c := o.opener.client
	o.opener.mu.Unlock()
	if c != nil {
		scope := catalog.BeginInternal(context.Background(), catalog.OpInternalSshRelease)
		defer scope.Complete()
		ctx, cancel := context.WithDeadline(catalog.WithScope(context.Background(), scope), scope.Deadline())
		defer cancel()
		_ = c.ReleaseConnection(ctx, paneID)
	}
}

func (h *hostedOpeners) RegisterReconnect(ctx context.Context, cfg session.Config, sequence uint64) error {
	return h.local.RegisterReconnect(ctx, cfg, sequence)
}
