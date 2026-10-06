package app

import (
	"context"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/workers"
)

// The agent record as the restart restore's probe (nocx-t5e7d).
//
// It answers the two questions workers.RestartProbe asks, and it is TWO OWNERS
// on purpose rather than a second opinion about either:
//
//   - whether the launch directory is still usable is the filesystem's answer,
//     and workers.DiskProbe already owns it. Nothing about a directory is the
//     record's business, so that half is delegated rather than restated.
//   - whether this agent, under this identity, names a conversation that can be
//     continued is the RECORD's answer, and the shipped probe says so in its own
//     doc comment: "an agent nocx never saw" is not a question a restart record
//     can answer about itself. It is asked of the store (Store.ResumeAnswer),
//     which owns the reasoning, so this type is only the seam between two
//     packages that must not import each other — internal/agentrecord is a leaf
//     by construction, and internal/workers is very much not.
//
// It FAILS CLOSED, in the same direction as the probe it replaces: an agent with
// no record, a record that cannot be used, and a mode the agent's record
// declares no arguments for are all refusals, and a refusal here is a restore
// saying WHY the pane cannot come back rather than an empty shell that claims it
// did (nocx-xn63t.5.2).
type agentProbe struct {
	store *agentrecord.Store
}

// The seam, checked at build time rather than at the one call site that would
// notice.
var _ workers.RestartProbe = agentProbe{}

// Checkout is the shipped probe's own answer, delegated.
func (p agentProbe) Checkout(ctx context.Context, worktree workers.Worktree, cwd string) error {
	return workers.DiskProbe{}.Checkout(ctx, worktree, cwd)
}

// Resume asks the shipped probe for the identity's own completeness — one owner
// of that rule, and the dead-code ratchet said so by reporting DiskProbe.Resume
// as newly unreachable the moment this probe began answering without it — and
// then asks the record the half nothing else can answer.
func (p agentProbe) Resume(ctx context.Context, agent string, resume workers.ResumeIdentity) error {
	if err := (workers.DiskProbe{}).Resume(ctx, agent, resume); err != nil {
		return err
	}
	return p.store.ResumeAnswer(agent, resume.Mode)
}
