package agenttools

// The worker capabilities (nocx-dkawo.8, nocx-rowqt.9).
//
// TWO TYPES AND NOT ONE WITH A ROLE FLAG, and since nocx-rowqt.9 both of them
// exist: WorkerCoordinator is what a run holds over its own worker, WorkerParticipant
// is what a worker holds over itself. Until then only the first was built, and
// the consequence was a writer with no reader — workers.say dropped mail into a
// box nothing could open, which is a soft degrade visible nowhere. A coordinator and a participant hold
// different authorities over the same objects, and the type switch at the
// dispatcher is what proves the distinction is exhaustive. A boolean inside
// one type proves nothing and is one refactor away from being read wrong —
// which is why Runner and RunWatcher are already two types for two
// authorities over the same sessions.
//
// THE HOLDER'S OWN RESOURCES LIVE INSIDE THE OBJECT. No call here takes a
// participant argument, and that is the design rather than an economy: the
// mailbox read, the inbox check, the report and this holdings call all name
// the holder's own resources, and passing an id to be checked is the ambient
// dispatcher API ADR-0028 decision 4 rejects. session.run's schema is the
// local proof it is avoidable — it has no session parameter at all, so the
// model cannot express "run in another pane".

import (
	"errors"

	"github.com/shady2k/nocx/internal/content"
)

// WorkerCoordinator is the narrowed authority a run holds over its own worker: it
// may ask what its SESSION holds, and it may spawn into an environment its
// grant named.
//
// The coordinator session is inside the object and is never a parameter. It is
// the run's own session, which is what D3's question is actually about — a
// coordinator whose run has ended asks what its session holds, and a call that
// let it name a different session would be answering somebody else's question.
type WorkerCoordinator struct {
	session      string
	environments map[string]struct{}
}

// NewWorkerCoordinator keeps only the environments the grant named. It holds no
// participant ids at all: a participant is reached through the record keyed by
// this session, so there is nothing here for a revoked delegation to leave
// behind.
func NewWorkerCoordinator(session string, scopes []content.GrantScope) *WorkerCoordinator {
	c := &WorkerCoordinator{session: session, environments: make(map[string]struct{})}
	for _, s := range scopes {
		if s.Kind == content.ResourceEnvironment && s.ID != "" {
			c.environments[s.ID] = struct{}{}
		}
	}
	return c
}

// Session is the coordinator session every holdings answer is about.
func (c *WorkerCoordinator) Session() string {
	if c == nil {
		return ""
	}
	return c.session
}

// MaySpawnInto reports whether the grant named this environment. A spawn
// outside it is REFUSED and the refusal names the environment; escalating
// instead is a property of a policy row rather than a special case for one
// tool, and is deliberately not invented here.
func (c *WorkerCoordinator) MaySpawnInto(environment string) bool {
	if c == nil || environment == "" {
		return false
	}
	_, ok := c.environments[environment]
	return ok
}

// Environments lists what the grant named, so a refusal can say what WAS
// available rather than only what was not.
func (c *WorkerCoordinator) Environments() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.environments))
	for id := range c.environments {
		out = append(out, id)
	}
	return out
}

// WorkerParticipant is the other authority over the same objects: what a WORKER
// holds over itself. It may read the mailbox that is its own and nothing else.
//
// The participant id is inside the object for the coordinator session's
// reason, and the consequence is stronger here: a worker asking for its mail
// has no way to EXPRESS another worker's mailbox, so "cannot read a
// neighbour's mail" is a property of the type rather than of a check somebody
// has to remember to write. The id itself is backend-owned (A9) and never
// travels to the agent — the worker knows its session, and the record turns
// that into this.
//
// It deliberately holds no environments and no coordinator session. A worker
// that could name either would hold half of a coordinator's authority, and
// the type would then be a coordinator with fields left empty rather than a
// different authority.
type WorkerParticipant struct {
	participant string
}

// NewWorkerParticipant binds the capability to one participant.
func NewWorkerParticipant(participant string) *WorkerParticipant {
	return &WorkerParticipant{participant: participant}
}

// Participant is who this capability is, and the only participant it can name.
func (p *WorkerParticipant) Participant() string {
	if p == nil {
		return ""
	}
	return p.participant
}

// Mailbox is the box this capability may read. It is the participant's own id
// because that is how a worker is named as a reader (internal/workers.ReaderID),
// and it is a method rather than a second field so the two cannot drift.
func (p *WorkerParticipant) Mailbox() string {
	if p == nil {
		return ""
	}
	return p.participant
}

// errNoParticipant is what a narrow returns for a run the authorizer did not
// establish as a worker's.
var errNoParticipant = errors.New("agenttools: this run is not a worker participant")

// narrowWorkerParticipant builds the participant capability from the run's own
// identity. The id comes from the run context and never from the call's
// arguments: a call that could name a participant would be the ambient
// dispatcher API ADR-0028 decision 4 rejects, and it would let one worker read
// another's mail by typing its id.
//
// A run with no participant is REFUSED here rather than narrowed to an empty
// capability. An empty participant names mailbox "", which belongs to nobody,
// and a mailbox belonging to nobody must not be reachable at all.
func narrowWorkerParticipant(_ content.Grant, _ []ResourceRef, runCtx RunContext) (Capability, error) {
	if runCtx.Participant == "" {
		return nil, errNoParticipant
	}
	return NewWorkerParticipant(runCtx.Participant), nil
}

// narrowWorkers builds the coordinator capability from the run's grant. Both worker
// tools share it: they are two acts of one authority, and a second constructor
// would be a second answer to "what may this run do to its own worker".
func narrowWorkers(grant content.Grant, _ []ResourceRef, runCtx RunContext) (Capability, error) {
	scopes := make([]content.GrantScope, 0, len(grant.Scopes))
	for _, s := range grant.Scopes {
		if s.Kind == content.ResourceEnvironment {
			scopes = append(scopes, s)
		}
	}
	return NewWorkerCoordinator(runCtx.Session, scopes), nil
}

// resourceParticipantWorkspace names the resource a participant's call is
// about, as A11 of the authority model decided it: a participant is addressed
// as a SUB-SCOPE OF ResourceWorkspace, not as a ninth ResourceKind. The kind
// set is closed at eight and guarded twice — validResourceKind and the
// grant_scopes CHECK — and ResourceContent already carries "note/<id>" and
// "skill/<id>", so sub-scoping inside a kind is the established move.
//
// It is the workspace and not the pane, and that is a smaller claim than it
// looks. What narrows a participant to ITS OWN mailbox is the capability
// (A9's rule: the holder's own resources live inside the object), never this
// scope; the scope's only job is offer-eligibility — whether Registry.ForGrant
// hands this declaration to the run at all. A pane-depth id would need the tab
// the worker's pane sits in, which the authorizer does not have and would have
// to invent, and it would buy nothing the capability does not already give.
//
// This kind is what keeps the two capabilities' offer sets disjoint. Nothing
// else in the tree mints a ResourceWorkspace scope — an ordinary run's fence
// is session, path, content, destination and environment — so no coordinator
// is ever offered a participant's call, and the participant's own grant names
// no session or environment, so it is offered none of the coordinator's four.
func resourceParticipantWorkspace(_ map[string]any, runCtx RunContext) ([]ResourceRef, error) {
	if runCtx.Workspace == "" {
		return nil, errNoParticipant
	}
	return []ResourceRef{{
		Kind: content.ResourceWorkspace,
		ID:   ParticipantWorkspaceScopeID(runCtx.Workspace),
	}}, nil
}

// ParticipantWorkspaceScopeID builds the canonical scope id a participant's
// call names. It is exported because the authorizer mints the grant and this
// resolver names the resource, and the two must agree — a second spelling in
// either place is a tool that assembles and is never offered.
func ParticipantWorkspaceScopeID(workspace string) string {
	return "workspace/" + workspace
}

// resourceLocalEnvironment names the environment a spawn would reach.
//
// It is a CONSTANT and not an argument, and that is the honest shape of this
// slice rather than a simplification: the spawner opens a local session, so
// the only environment a worker can be started in is the machine nocx itself
// runs on. A parameter would let the model name an environment nothing could
// deliver, and the refusal would then come from the wrong place.
//
// The id is derived through content.EnvironmentIDFor, which is the single
// owner of "what is this environment called" — deterministic from kind and
// endpoint, so the fence and the resolver name the same string without either
// restating the other's rule.
func resourceLocalEnvironment(map[string]any, RunContext) ([]ResourceRef, error) {
	return []ResourceRef{{
		Kind: content.ResourceEnvironment,
		ID:   content.EnvironmentIDFor(content.EnvLocal, ""),
	}}, nil
}
