package app

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/workers"
)

// The agent record as the restart restore's probe (nocx-t5e7d).
//
// It answers the checkout question through the filesystem and builds resume
// argv from the agent record, with TWO OWNERS rather than a second opinion:
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

// Resume asks the shipped probe to validate identity and location, then asks
// the record whether that mode is supported and returns its argv. DiskProbe
// remains the owner of identity completeness; the agent record owns launch
// syntax.
func (p agentProbe) Resume(ctx context.Context, agent string, worktree workers.Worktree, resume workers.ResumeIdentity) ([]string, error) {
	if _, err := (workers.DiskProbe{}).Resume(ctx, agent, worktree, resume); err != nil {
		return nil, err
	}
	if err := p.store.ResumeAnswer(agent, resume.Mode); err != nil {
		return nil, err
	}
	entry, known := p.store.Entry(agent)
	if !known {
		// ResumeAnswer has already named the unknown agent; this is only the
		// store's closed-state guard if its contract ever changes.
		return nil, fmt.Errorf("nocx has no record of an agent called %q", agent)
	}
	return resumeArgumentsFor(entry.Record, resume)
}

// resumeArgumentsFor turns the selected identity into the current agent's
// recorded argv. The mode was chosen from the task's location by
// workers.ResumeIdentityFor; this function refuses a mismatch rather than
// falling back to whichever session is newest.
func resumeArgumentsFor(record agentrecord.Record, resume workers.ResumeIdentity) ([]string, error) {
	var args []string
	switch resume.Mode {
	case workers.ResumeByCwd:
		args = record.Resume.ResumeCwdArgs
	case workers.ResumeByID:
		args = record.Resume.ResumeIDArgs
	default:
		return nil, fmt.Errorf("the record's resume identity (%q) names no conversation", resume.Mode)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("the record for %q declares no arguments for resume mode %q", record.ID, resume.Mode)
	}
	values := map[string]string{}
	if resume.Mode == workers.ResumeByID {
		values["UUID"] = resume.ID
	}
	return shellintegration.ExpandAgentArgv(args, values), nil
}
