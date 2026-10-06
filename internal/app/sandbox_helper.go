package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
)

// SandboxPrepare reserves authority on the exact installed local generation.
func (o *localHelperOpener) SandboxPrepare(ctx context.Context, params proto.SandboxPrepareParams) (transport.SandboxPrepared, error) {
	c, generation, err := o.connect(ctx)
	if err != nil {
		return transport.SandboxPrepared{}, err
	}
	prepared, err := c.SandboxPrepare(ctx, params)
	if err != nil {
		return transport.SandboxPrepared{}, err
	}
	return transport.SandboxPrepared{Preparation: prepared, Generation: generation}, nil
}

func (o *localHelperOpener) SandboxDiscard(ctx context.Context, prepared transport.SandboxPrepared) error {
	c, err := o.sandboxGeneration(ctx, prepared.Generation)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.SandboxDiscard(ctx, prepared.Preparation.Ticket)
}

func (o *localHelperOpener) SandboxOpenCandidate(ctx context.Context, cfg session.Config, prepared transport.SandboxPrepared, params proto.SandboxLaunchParams) (transport.SandboxCandidate, error) {
	if cfg.Kind != session.KindLocal || cfg.LaunchBinding.LaunchID == "" ||
		cfg.LaunchBinding.LaunchID != prepared.Preparation.LaunchID ||
		params.LaunchID != prepared.Preparation.LaunchID || params.OperationID != prepared.Preparation.OperationID ||
		params.Ticket != prepared.Preparation.Ticket || params.Mode != proto.SandboxMode(cfg.LaunchBinding.Mode) ||
		params.Mode != proto.SandboxMode(prepared.Preparation.Mode) {
		return transport.SandboxCandidate{}, errors.New("sandbox launch does not match its prepared local operation")
	}
	if params.Mode == proto.SandboxEnforce {
		if params.Grant == nil || params.Grant.ID != cfg.LaunchBinding.GrantID ||
			params.Grant.Digest != cfg.LaunchBinding.Digest || params.Grant.Version != cfg.LaunchBinding.Version ||
			prepared.Preparation.Policy == nil || prepared.Preparation.Policy.Version != cfg.LaunchBinding.Version ||
			params.Grant.Digest != prepared.Preparation.Digest {
			return transport.SandboxCandidate{}, errors.New("sandbox grant does not match the prepared launch binding")
		}
	} else if params.Grant != nil || cfg.LaunchBinding.GrantID != 0 || cfg.LaunchBinding.Digest != "" || cfg.LaunchBinding.Version != 0 {
		return transport.SandboxCandidate{}, errors.New("Off sandbox launch must not carry a grant")
	}
	if params.Shape.Cols != cfg.Cols || params.Shape.Rows != cfg.Rows || params.Shape.XPixel != cfg.XPixel || params.Shape.YPixel != cfg.YPixel {
		return transport.SandboxCandidate{}, errors.New("sandbox launch geometry does not match the session configuration")
	}
	c, generation, err := o.connect(ctx)
	if err != nil {
		return transport.SandboxCandidate{}, err
	}
	if generation != prepared.Generation {
		return transport.SandboxCandidate{}, fmt.Errorf("sandbox preparation generation %q is not connected generation %q", prepared.Generation, generation)
	}
	spawn := hostedSpawn{
		client: c, registry: o.registry, lifecycle: o.kernel, loss: o.lifecycleLoss,
		publishScreen: o.publishScreen, publishSandboxAccess: o.publishSandboxAccess,
		blockRows: o.blockRows, environmentEntries: o.environmentEntries, cursors: o.lifecycleCursors,
		stopping: o.lifecycleStopping, helloTimeout: lifecycle.HelloTimeout, log: o.log,
	}
	candidate, err := spawn.openCandidate(ctx, cfg, func(ctx context.Context, life *proto.LifecycleLaunch) (helperclient.SessionEntry, error) {
		launch := params
		launch.Shape.Lifecycle = life
		return c.SandboxLaunch(ctx, launch)
	})
	if err != nil {
		return transport.SandboxCandidate{}, err
	}
	identity := content.HelperIdentity{Generation: generation, SessionID: candidate.entry.HostSessionID.Session}
	var publishOnce sync.Once
	var published transport.HostedSessionOpen
	var publishErr error
	return transport.SandboxCandidate{
		Identity: identity,
		Entry:    candidate.entry,
		Publish: func(publishCtx context.Context) (transport.HostedSessionOpen, error) {
			publishOnce.Do(func() {
				result, err := candidate.Publish(publishCtx)
				if err != nil {
					publishErr = err
					return
				}
				published, publishErr = o.hostedOpenResult(cfg, generation, result)
			})
			return published, publishErr
		},
		Abort: candidate.Abort,
	}, nil
}

func (o *localHelperOpener) SandboxBinding(ctx context.Context, identity content.HelperIdentity) (helperclient.SandboxSession, error) {
	if identity.Host != "" || identity.Account != "" || identity.Generation == "" || identity.SessionID == "" {
		return helperclient.SandboxSession{}, content.ErrInvalidLaunch
	}
	c, err := o.sandboxGeneration(ctx, identity.Generation)
	if err != nil {
		return helperclient.SandboxSession{}, err
	}
	defer func() { _ = c.Close() }()
	return c.SandboxGet(ctx, helperclient.HostSessionID{Generation: identity.Generation, Session: identity.SessionID})
}

func (o *localHelperOpener) SandboxInventory(ctx context.Context, generation string) ([]helperclient.SessionEntry, error) {
	o.mu.Lock()
	dir := o.dir
	o.mu.Unlock()
	if dir == "" || generation == "" {
		return nil, errNoLocalGeneration
	}
	return o.LocalSessions(ctx, generation)
}

func (o *localHelperOpener) SandboxClose(ctx context.Context, identity content.HelperIdentity) error {
	if identity.Host != "" || identity.Account != "" || identity.Generation == "" || identity.SessionID == "" {
		return content.ErrInvalidLaunch
	}
	c, err := o.sandboxGeneration(ctx, identity.Generation)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.CloseSession(ctx, helperclient.HostSessionID{Generation: identity.Generation, Session: identity.SessionID})
}

var _ transport.SandboxHelper = (*localHelperOpener)(nil)

// hostedOpeners is the app's dispatch surface for sandbox helper operations.
func (h *hostedOpeners) SandboxPrepare(ctx context.Context, params proto.SandboxPrepareParams) (transport.SandboxPrepared, error) {
	return h.local.SandboxPrepare(ctx, params)
}

func (h *hostedOpeners) SandboxDiscard(ctx context.Context, prepared transport.SandboxPrepared) error {
	return h.local.SandboxDiscard(ctx, prepared)
}

func (h *hostedOpeners) SandboxOpenCandidate(ctx context.Context, cfg session.Config, prepared transport.SandboxPrepared, params proto.SandboxLaunchParams) (transport.SandboxCandidate, error) {
	return h.local.SandboxOpenCandidate(ctx, cfg, prepared, params)
}

func (h *hostedOpeners) SandboxBinding(ctx context.Context, identity content.HelperIdentity) (helperclient.SandboxSession, error) {
	return h.local.SandboxBinding(ctx, identity)
}

func (h *hostedOpeners) SandboxInventory(ctx context.Context, generation string) ([]helperclient.SessionEntry, error) {
	return h.local.SandboxInventory(ctx, generation)
}

func (h *hostedOpeners) SandboxClose(ctx context.Context, identity content.HelperIdentity) error {
	return h.local.SandboxClose(ctx, identity)
}

var _ transport.SandboxHelper = (*hostedOpeners)(nil)

func (o *localHelperOpener) SandboxRollback(ctx context.Context, generation, operationID, launchID string) (*helperclient.SessionEntry, error) {
	c, err := o.sandboxGeneration(ctx, generation)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	entry, err := c.SandboxRollback(ctx, operationID, launchID)
	if err != nil {
		return nil, err
	}
	if entry != nil && entry.HostSessionID.Generation != generation {
		return nil, errors.New("sandbox rollback returned an entry from a different helper generation")
	}
	return entry, nil
}

func (h *hostedOpeners) SandboxRollback(ctx context.Context, generation, operationID, launchID string) (*helperclient.SessionEntry, error) {
	return h.local.SandboxRollback(ctx, generation, operationID, launchID)
}

// Recovery reaches a serving generation; it never installs or starts a helper.
func (o *localHelperOpener) sandboxGeneration(ctx context.Context, generation string) (*helperclient.Client, error) {
	o.mu.Lock()
	dir := o.dir
	o.mu.Unlock()
	if dir == "" || generation == "" {
		return nil, errNoLocalGeneration
	}
	c, err := helperlocal.Open(ctx, helperlocal.Config{Dir: dir, Generation: proto.GenerationID(generation), Binary: "", Log: o.log})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errLocalEndpointUnreachable, err)
	}
	return c, nil
}

func (o *localHelperOpener) SandboxAccessList(ctx context.Context, identity content.HelperIdentity, params proto.SandboxAccessListParams) (proto.SandboxAccessListResult, error) {
	if identity.Host != "" || identity.Account != "" || identity.Generation == "" || identity.SessionID == "" {
		return proto.SandboxAccessListResult{}, content.ErrInvalidLaunch
	}
	c, err := o.sandboxGeneration(ctx, identity.Generation)
	if err != nil {
		return proto.SandboxAccessListResult{}, err
	}
	defer func() { _ = c.Close() }()
	params.Session = proto.HostSessionID{Generation: proto.GenerationID(identity.Generation), Session: identity.SessionID}
	return c.SandboxAccessList(ctx, params)
}

func (o *localHelperOpener) SandboxAccessReserve(ctx context.Context, identity content.HelperIdentity, params proto.SandboxAccessReserveParams) (proto.SandboxAccessReserveResult, error) {
	if identity.Host != "" || identity.Account != "" || identity.Generation == "" || identity.SessionID == "" {
		return proto.SandboxAccessReserveResult{}, content.ErrInvalidLaunch
	}
	c, err := o.sandboxGeneration(ctx, identity.Generation)
	if err != nil {
		return proto.SandboxAccessReserveResult{}, err
	}
	defer func() { _ = c.Close() }()
	params.Session = proto.HostSessionID{Generation: proto.GenerationID(identity.Generation), Session: identity.SessionID}
	return c.SandboxAccessReserve(ctx, params)
}

func (o *localHelperOpener) SandboxAccessFinish(ctx context.Context, identity content.HelperIdentity, params proto.SandboxAccessFinishParams) (proto.SandboxAccessFinishResult, error) {
	if identity.Host != "" || identity.Account != "" || identity.Generation == "" || identity.SessionID == "" {
		return proto.SandboxAccessFinishResult{}, content.ErrInvalidLaunch
	}
	c, err := o.sandboxGeneration(ctx, identity.Generation)
	if err != nil {
		return proto.SandboxAccessFinishResult{}, err
	}
	defer func() { _ = c.Close() }()
	params.Session = proto.HostSessionID{Generation: proto.GenerationID(identity.Generation), Session: identity.SessionID}
	return c.SandboxAccessFinish(ctx, params)
}

func (h *hostedOpeners) SandboxAccessList(ctx context.Context, identity content.HelperIdentity, params proto.SandboxAccessListParams) (proto.SandboxAccessListResult, error) {
	return h.local.SandboxAccessList(ctx, identity, params)
}

func (h *hostedOpeners) SandboxAccessReserve(ctx context.Context, identity content.HelperIdentity, params proto.SandboxAccessReserveParams) (proto.SandboxAccessReserveResult, error) {
	return h.local.SandboxAccessReserve(ctx, identity, params)
}

func (h *hostedOpeners) SandboxAccessFinish(ctx context.Context, identity content.HelperIdentity, params proto.SandboxAccessFinishParams) (proto.SandboxAccessFinishResult, error) {
	return h.local.SandboxAccessFinish(ctx, identity, params)
}
