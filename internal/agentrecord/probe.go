package agentrecord

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/workers"
)

// Probe answers the restart restore's two questions about one persisted
// worker: whether the place it was launched in is still there, and whether the
// identity it names is one nocx can act on (nocx-xn63t.5.1's RestartProbe).
//
// It exists because nocx-xn63t.5.1 shipped the FILESYSTEM half of that answer
// and said so: its own DiskProbe answers the resume question from the record's
// completeness, and its doc comment names this package as the thing that
// replaces it — "an agent nocx never saw" is not a question the restart record
// can answer about itself, because the record only knows what a launch was
// told. The record knows which agents exist, which of them this build ships,
// and which resume shapes each of them declares, so it is the owner of that
// half and the only thing that can say a name somebody typed is not an agent.
//
// The two halves stay two: the checkout question is delegated, unchanged, to
// the probe that already owns it. Nothing about a directory is the record's
// business, and a second answer to it here would be a second answer.
//
// It FAILS CLOSED. An agent with no record, a record that could not be used, a
// resume identity that names no conversation and a mode the agent's own record
// declares no args for are all refusals — and a refusal here is the restore
// saying why the pane cannot come back, never an empty shell that claims it
// did (nocx-xn63t.5.2).
type Probe struct {
	// store is the record set this probe answers from. A nil store refuses
	// every record rather than answering permissively, which is the same
	// direction a nil probe is refused in workers.Restore itself: a restore
	// that could not look must not claim the pane is fine.
	store *Store
}

// Probe is this store's answer to the restore's question. It is a method
// rather than a constructor call at each call site so the composition root
// passes a value that visibly came from the store it built, and so a second
// store cannot be wired in beside the first by accident.
func (s *Store) Probe() Probe { return Probe{store: s} }

// The seam, checked at build time rather than at the one call site that would
// notice: a probe that stopped satisfying it would fail to compile here rather
// than to restore a worker at a backend start.
var _ workers.RestartProbe = Probe{}

// Checkout answers whether the recorded launch directory is still usable. The
// record has nothing to add to it, so it is the shipped probe's own answer,
// delegated rather than restated.
func (p Probe) Checkout(ctx context.Context, worktree workers.Worktree, cwd string) error {
	return workers.DiskProbe{}.Checkout(ctx, worktree, cwd)
}

// Resume answers whether this agent, under this identity, names a conversation
// that can be continued — and it is the question the restart record could not
// answer about itself.
//
// TWO QUESTIONS, ONE AFTER THE OTHER, and neither restated:
//
//  1. The identity's own completeness, which is workers.DiskProbe's answer and
//     stays there: an unnamed agent, a mode of "none" or one this build does
//     not know, and a by-id identity that names no id are all not a
//     conversation. Asking the shipped probe rather than writing that rule
//     again is what keeps one owner of it — and the dead-code ratchet is what
//     said so, by reporting DiskProbe.Resume as newly unreachable the moment
//     this probe began answering without it.
//  2. The agent's own record, which is the half nothing else can answer: does
//     nocx know an agent by this name at all, is its record usable, and does
//     its record declare the args a resume in this mode would be built from.
//
// The order is the order of what a person needs to hear: whether there is a
// conversation at all, and then whether this agent can be given one back.
func (p Probe) Resume(ctx context.Context, agent string, resume workers.ResumeIdentity) error {
	if err := (workers.DiskProbe{}).Resume(ctx, agent, resume); err != nil {
		return err
	}
	entry, known := p.store.entry(agent)
	switch {
	case !known:
		// THE ANSWER DiskProbe COULD NOT GIVE. It said "an agent nocx never
		// saw" in its own comment and then could not tell one from a name
		// somebody invented, because whether nocx has heard of an agent was
		// nobody's fact until this record existed.
		return fmt.Errorf("nocx has no record of an agent called %q, so there is nothing to launch", agent)
	case entry.State == StateUnreadable:
		return fmt.Errorf("nocx cannot read its record for the agent %q (%s), so it will not launch it", agent, entry.Problem)
	}
	if !entry.Record.Resume.Supports(resume.Mode) {
		modes := entry.Record.Resume.Modes()
		if len(modes) == 0 {
			return fmt.Errorf("the record for %q declares no way to resume a conversation", agent)
		}
		return fmt.Errorf("the record for %q resumes %v, and this identity asks for %q", agent, modes, resume.Mode)
	}
	return nil
}
