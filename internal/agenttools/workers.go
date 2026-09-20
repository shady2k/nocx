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
	"github.com/shady2k/nocx/internal/session"
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
	identity     session.Identity
	environments map[string]struct{}
}

// NewWorkerCoordinator keeps only the environments the grant named. It holds no
// participant ids at all: a participant is reached through the record keyed by
// this session, so there is nothing here for a revoked delegation to leave
// behind.
//
// identity is the session's own INCARNATION (RunContext.ControllerIdentity),
// never derived from a participant's liveness: a delegation this coordinator
// creates by spawning is bound to which incarnation of it granted the
// authority (nocx-bm99e), and this is where that travels from.
func NewWorkerCoordinator(sessionID string, identity session.Identity, scopes []content.GrantScope) *WorkerCoordinator {
	c := &WorkerCoordinator{session: sessionID, identity: identity, environments: make(map[string]struct{})}
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

// Identity is the incarnation of Session this coordinator was bound under.
// It travels onto workers.RegisterRequest.CoordinatorIdentity when this
// coordinator spawns a worker (executeWorkerSpawn), never substituted with
// the spawned participant's own liveness epoch.
func (c *WorkerCoordinator) Identity() session.Identity {
	if c == nil {
		return session.Identity{}
	}
	return c.identity
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

// Mailbox is the box a coordinator reads for its own mail, and it is the SAME
// answer WorkerParticipant.Mailbox gives one for a worker: the holder's own.
//
// What differs is which identity "its own" is, and the difference is the
// design's rather than an economy. A participant is named by its participant
// id, which outlives every run it makes; a coordinator is named by its SESSION
// (AD-7), which is what makes a coordinator that RESTARTED the same reader —
// the property D3 already rests on for holdings and the wake.
//
// It is a method rather than a second field for the reason the participant's
// is: the two cannot drift.
func (c *WorkerCoordinator) Mailbox() string {
	if c == nil {
		return ""
	}
	return c.session
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

// ErrNoParticipant is what a narrow returns for a run the authorizer did not
// establish as a worker's.
//
// IT IS EXPORTED, and the reason is the tool that made it reachable
// (nocx-luqz9.4): workers.report is a WORKER's call and nothing else's, so a
// coordinator — or an ordinary run — that reaches it is refused HERE, at the
// constructor, and the endpoint has to turn that refusal into a sentence the
// agent can act on. An unexported sentinel would have left it in the default
// arm, whose sentence calls an unclassified failure a fault inside nocx and
// tells the model to stop — which is wrong twice over: nothing failed, and
// what the caller should do is use the calls it does have.
var ErrNoParticipant = errors.New("agenttools: this run is not a worker participant")

// narrowWorkerParticipant builds the participant capability from the run's own
// identity. The id comes from the run context and never from the call's
// arguments: a call that could name a participant would be the ambient
// dispatcher API ADR-0028 decision 4 rejects, and it would let one worker read
// another's mail by typing its id.
//
// A run with no participant is REFUSED here rather than narrowed to an empty
// capability. An empty participant names mailbox "", which belongs to nobody,
// and a mailbox belonging to nobody must not be reachable at all.
//
// TWO CALLERS, ONE REFUSAL. workers.report is this narrow's alone — a worker
// reporting to its coordinator, which no other run identity has any business
// doing — and workers.inbox reaches it through narrowWorkerMailbox when the run
// IS a participant. Both therefore refuse a coordinator with this one sentinel,
// which is correct: the fact is the same fact ("this run is not a worker"), and
// a second sentinel saying it again would be a second name for one thing, with
// the endpoint then owing two sentences that must stay distinct.
func narrowWorkerParticipant(_ content.Grant, _ []ResourceRef, runCtx RunContext) (Capability, error) {
	if runCtx.Participant == "" {
		return nil, ErrNoParticipant
	}
	return NewWorkerParticipant(runCtx.Participant), nil
}

// Mailbox is the one thing workers.inbox needs from the capability it was
// narrowed to: which box is this holder's own.
//
// It is an interface with one method and it is NOT a third authority. Nothing
// about what the holder MAY do is reachable through it — the two concrete types
// keep their own powers, and the dispatcher's type switch still proves the
// distinction exhaustive everywhere authority is exercised. What this exists for
// is a call where the answer is genuinely the same act for both callers: "read
// my own mailbox", where the only thing that differs is which identity "my own"
// is.
type Mailbox interface {
	Mailbox() string
}

// errNoMailbox is what a narrow answers for a run that is neither a worker nor a
// coordinator — a run whose mailbox nothing addresses and which must not be
// handed one that belongs to nobody.
var errNoMailbox = errors.New("agenttools: this run has no mailbox")

// narrowWorkerMailbox builds the capability for workers.inbox, whichever of the
// two callers is asking (nocx-luqz9.2, design §4.5).
//
// It is the ONE narrow that can return either type, and that is the shape of the
// act rather than a relaxation of A8's two types: a coordinator reading the
// observations its workers' panes produced reads the SAME mailbox a worker reads
// for the mail its coordinator left it, with one cursor and one order. Which box
// that is comes from what the run IS — a participant's own id, or the session a
// coordinator is — and never from anything the call carries, which is A9's rule
// and the reason a caller cannot name somebody else's mail.
//
// The participant half is delegated rather than restated, so "how a participant
// capability is built from a run context" keeps one owner: narrowWorkerParticipant
// is the same function the tool used before this task and the same one its own
// tests exercise.
func narrowWorkerMailbox(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	if runCtx.Participant != "" {
		return narrowWorkerParticipant(grant, resources, runCtx)
	}
	if runCtx.Session == "" {
		// Neither identity: a run that is not a worker and has no session is
		// nothing's reader, and an empty mailbox belongs to nobody.
		return nil, errNoMailbox
	}
	return narrowWorkers(grant, resources, runCtx)
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
	return NewWorkerCoordinator(runCtx.Session, runCtx.ControllerIdentity, scopes), nil
}

// errNoWorkspace is what resourceParticipantWorkspace answers for a run whose
// context names no workspace: there is no sub-scope to resolve, and returning an
// empty one would be a scope covering nothing at a call that needs to cover
// something. It is a DIFFERENT fact from ErrNoParticipant — a run can be a
// participant and still have no workspace here — and the two are separate names
// for the reason this package keeps every refusal separate: a reader that cannot
// tell them apart looks in the wrong place.
var errNoWorkspace = errors.New("agenttools: this run names no workspace")

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
//
// It answers errNoWorkspace for a run whose context names no workspace, which
// is the fact above the reason it is a refusal rather than an empty scope.
func resourceParticipantWorkspace(_ map[string]any, runCtx RunContext) ([]ResourceRef, error) {
	if runCtx.Workspace == "" {
		return nil, errNoWorkspace
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

// spawnInvocationRelation is workers.spawn's InvocationRelation: the validated
// arguments choose between two shapes, and this function is the one place
// that reading is stated.
// A plain spawn is delegation and nothing else, so the shape it returns is
// the singleton delegate row — the narrowing that keeps a grant refusing
// mutation from refusing it, and keeps a grant refusing DELEGATION from
// letting the tool's mutation row speak for a spawn that never mutates. A
// worktree ask creates a branch and a linked checkout before any worker
// exists — a reversible mutation the same call performs — so its shape is
// the CONJUNCTION of delegate and mutate-reversible: the verdict takes the
// strictest of the two rows, and a grant refusing either refuses the call
// before a checkout exists (ADR-0053 as amended, nocx-ykjai).
func spawnInvocationRelation(args map[string]any) (EffectRelation, []content.Effect, bool) {
	if _, ok := args["worktree"].(map[string]any); !ok {
		return EffectsAlternative, []content.Effect{content.EffectDelegate}, true
	}
	return EffectsConjunctive, []content.Effect{content.EffectDelegate, content.EffectMutateReversible}, true
}
