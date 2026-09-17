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
	"fmt"

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

// The two target-mint refusals a caller can act on (nocx-xn63t.4.1, spec
// §6.1, §6.2). They are sentinels rather than wire codes because the code is
// the HELPER's vocabulary and what crosses into the coordinator is a fact
// about the caller's own request: one is "wait, the book is full", the other
// is "take a fresh snapshot". A caller that cannot tell those apart either
// retries the wrong one or stops asking.
var (
	// ErrTargetCapacity is a mint refused because the session's token book
	// already holds maxLiveTokens live slots — and by spec nothing is ever
	// evicted to make room, so a slot comes back only once a token has
	// expired AND the retention after it has passed (about six minutes).
	// Retrying immediately is the one thing that cannot help.
	ErrTargetCapacity = errors.New("helper: the session's target book is full")
	// ErrSnapshotGone is a mint refused because the retained snapshot it
	// would have been minted from had already been evicted: no target can
	// describe the frame the caller classified any more. A fresh read takes
	// a fresh snapshot, so asking again is the way through.
	ErrSnapshotGone = errors.New("helper: the snapshot a target would name is gone")
)

// ClassifyTargetRefusal names a target-mint refusal's wire code with its
// sentinel, and passes every other error through untouched — including a
// refusal whose code this build does not know, which stays the opaque
// *RefusalError it arrived as.
//
// It is exported and called at the CROSSING rather than applied inside
// Client.Target, because the crossing is where the coordinator owns the fact:
// internal/app's session.read receives its helper through its own narrow seam
// (paneHelpers), and a refusal reaching it may have come from the real client,
// from the local helper's bridge, or from a test double that never spoke to
// Client.Target at all. Classifying here, once, on whatever that seam handed
// back, is what makes errors.Is work for every one of them — and it keeps
// internal/app free of the wire's spellings.
//
// The helpers' own spellings live in proto (ErrCodeCapacity,
// ErrCodeSnapshotGone) and the helper writes them in
// internal/helper/session.Service.Refusal; a test in this package asserts the
// two agree by asking the real service, so a rename on either side fails a
// build rather than silently costing a caller its sentence.
func ClassifyTargetRefusal(err error) error {
	if err == nil {
		return nil
	}
	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		return err
	}
	switch refusal.Code {
	case proto.ErrCodeCapacity:
		return fmt.Errorf("%w: %w", ErrTargetCapacity, err)
	case proto.ErrCodeSnapshotGone:
		return fmt.Errorf("%w: %w", ErrSnapshotGone, err)
	default:
		return err
	}
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
