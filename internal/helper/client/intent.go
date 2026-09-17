package client

// The coordinator's side of the one-shot write path (nocx-6q1uh.6, spec §6,
// §7.2): read a consistent snapshot, mint a target from it, spend the
// target's token as an intent, poll or bump as needed. Every method here is
// a thin wire crossing — the DECISIONS (which rows to target, whether an
// intent's commitBy has passed) are the coordinator's own, made from what
// these methods hand back.

import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// ErrIntentUnsupported is a helper generation that does not answer the
// one-shot write path's ops (snapshot, target, intent, intent-status,
// access-bump) at all.
//
// It is a fact about the GENERATION, the same distinction ErrScreenUnsupported
// draws: a coordinator that could not tell "this generation predates the
// one-shot write path" from "this session is gone" would report a live pane
// as unreachable rather than as one this app cannot yet write into safely.
var ErrIntentUnsupported = errors.New("helper: this generation does not answer the one-shot write path")

// unsupportedIfUnknownOp maps ErrCodeUnknownOp onto ErrIntentUnsupported and
// passes every other error through unchanged — the same shape Screen and
// Replay already use for the frozen ABI's own generation gap.
func unsupportedIfUnknownOp(err error) error {
	var refusal *RefusalError
	if errors.As(err, &refusal) && refusal.Code == proto.ErrCodeUnknownOp {
		return ErrIntentUnsupported
	}
	return err
}

// Snapshot asks a session's runtime for a consistent read of its screen: the
// frame a caller classifies, and the facts (access epoch, read barrier) a
// target minted from it will carry.
func (c *Client) Snapshot(ctx context.Context, id HostSessionID) (proto.SnapshotResult, error) {
	var result proto.SnapshotResult
	err := c.Call(ctx, proto.ServiceSession, proto.OpSnapshot, proto.SnapshotParams{
		Session: proto.HostSessionID{Generation: proto.GenerationID(id.Generation), Session: id.Session},
	}, &result)
	if err != nil {
		return proto.SnapshotResult{}, unsupportedIfUnknownOp(err)
	}
	return result, nil
}

// Target mints a one-shot, signed token from a retained snapshot.
func (c *Client) Target(ctx context.Context, p proto.TargetParams) (proto.TargetResult, error) {
	var result proto.TargetResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpTarget, p, &result); err != nil {
		return proto.TargetResult{}, unsupportedIfUnknownOp(err)
	}
	return result, nil
}

// Intent spends a target's token: a key or text write, validated and encoded
// against the same screen the token was minted from, at most once.
func (c *Client) Intent(ctx context.Context, p proto.IntentParams) (proto.IntentResult, error) {
	var result proto.IntentResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpIntent, p, &result); err != nil {
		return proto.IntentResult{}, unsupportedIfUnknownOp(err)
	}
	return result, nil
}

// IntentStatus answers what became of a token-bound intent without spending
// or re-presenting it.
func (c *Client) IntentStatus(ctx context.Context, id HostSessionID, tokenID string) (proto.IntentStatusResult, error) {
	var result proto.IntentStatusResult
	err := c.Call(ctx, proto.ServiceSession, proto.OpIntentStatus, proto.IntentStatusParams{
		Session: proto.HostSessionID{Generation: proto.GenerationID(id.Generation), Session: id.Session},
		TokenID: tokenID,
	}, &result)
	if err != nil {
		return proto.IntentStatusResult{}, unsupportedIfUnknownOp(err)
	}
	return result, nil
}

// AccessBump raises a session's access epoch past above, refusing every
// older uncommitted intent access_revoked before it returns (spec §7.2).
func (c *Client) AccessBump(ctx context.Context, id HostSessionID, above uint64) (proto.AccessBumpResult, error) {
	var result proto.AccessBumpResult
	err := c.Call(ctx, proto.ServiceSession, proto.OpAccessBump, proto.AccessBumpParams{
		Session: proto.HostSessionID{Generation: proto.GenerationID(id.Generation), Session: id.Session},
		Above:   above,
	}, &result)
	if err != nil {
		return proto.AccessBumpResult{}, unsupportedIfUnknownOp(err)
	}
	return result, nil
}
