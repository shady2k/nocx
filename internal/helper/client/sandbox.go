package client

import (
	"context"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sandbox"
)

type SandboxPreparation struct {
	Ticket      string
	OperationID string
	LaunchID    string
	Mode        string
	ExpiresAt   string
	Policy      *sandbox.Policy
	Digest      string
}

type SandboxGrant struct {
	ID      int64
	Digest  string
	Version int
}

type SandboxSession struct {
	HostSessionID HostSessionID
	OperationID   string
	LaunchID      string
	Mode          string
	Grant         *SandboxGrant
	Enforcement   string
	Observer      string
}

// Request DTOs follow Spawn's existing wire boundary; result DTOs remain the
// coordinator's typed projections rather than changing frozen SessionEntry.
func (c *Client) SandboxPrepare(ctx context.Context, params proto.SandboxPrepareParams) (SandboxPreparation, error) {
	var result proto.SandboxPrepareResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpSandboxPrepare, params, &result); err != nil {
		return SandboxPreparation{}, err
	}
	preparation := SandboxPreparation{Ticket: result.Ticket, OperationID: result.OperationID, LaunchID: result.LaunchID, Mode: string(result.Mode), ExpiresAt: result.ExpiresAt}
	if result.Enforce != nil {
		preparation.Policy = &result.Enforce.Policy
		preparation.Digest = result.Enforce.Digest
	}
	return preparation, nil
}

func (c *Client) SandboxLaunch(ctx context.Context, params proto.SandboxLaunchParams) (SessionEntry, error) {
	var result proto.SpawnResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpSandboxLaunch, params, &result); err != nil {
		return SessionEntry{}, err
	}
	return mapSessionEntry(result.Entry), nil
}

func (c *Client) SandboxGet(ctx context.Context, id HostSessionID) (SandboxSession, error) {
	var result proto.SandboxGetResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpSandboxGet, proto.SandboxGetParams{Session: proto.HostSessionID{Generation: proto.GenerationID(id.Generation), Session: id.Session}}, &result); err != nil {
		return SandboxSession{}, err
	}
	state := SandboxSession{HostSessionID: HostSessionID{Generation: string(result.Session.Generation), Session: result.Session.Session}, OperationID: result.OperationID, LaunchID: result.LaunchID, Mode: string(result.Mode), Enforcement: result.Enforcement, Observer: result.Observer}
	if result.Grant != nil {
		state.Grant = &SandboxGrant{ID: result.Grant.ID, Digest: result.Grant.Digest, Version: result.Grant.Version}
	}
	return state, nil
}

func (c *Client) SandboxDiscard(ctx context.Context, ticket string) error {
	return c.Call(ctx, proto.ServiceSession, proto.OpSandboxDiscard, proto.SandboxDiscardParams{Ticket: ticket}, nil)
}
