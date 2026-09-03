package agenttools

// The wave coordinator's capability (nocx-dkawo.8).
//
// TWO TYPES AND NOT ONE WITH A ROLE FLAG. A coordinator and a participant hold
// different authorities over the same objects, and the type switch at the
// dispatcher is what proves the distinction is exhaustive. A boolean inside
// one type proves nothing and is one refactor away from being read wrong —
// which is why Runner and RunWatcher are already two types for two
// authorities over the same sessions.
//
// THE HOLDER'S OWN RESOURCES LIVE INSIDE THE OBJECT. Neither call here takes a
// participant argument, and that is the design rather than an economy: the
// mailbox read, the inbox check, the report and this holdings call all name
// the holder's own resources, and passing an id to be checked is the ambient
// dispatcher API ADR-0028 decision 4 rejects. session.run's schema is the
// local proof it is avoidable — it has no session parameter at all, so the
// model cannot express "run in another pane".

import "github.com/shady2k/nocx/internal/content"

// WaveCoordinator is the narrowed authority a run holds over its own wave: it
// may ask what its SESSION holds, and it may spawn into an environment its
// grant named.
//
// The coordinator session is inside the object and is never a parameter. It is
// the run's own session, which is what D3's question is actually about — a
// coordinator whose run has ended asks what its session holds, and a call that
// let it name a different session would be answering somebody else's question.
type WaveCoordinator struct {
	session      string
	environments map[string]struct{}
}

// NewWaveCoordinator keeps only the environments the grant named. It holds no
// participant ids at all: a participant is reached through the record keyed by
// this session, so there is nothing here for a revoked delegation to leave
// behind.
func NewWaveCoordinator(session string, scopes []content.GrantScope) *WaveCoordinator {
	c := &WaveCoordinator{session: session, environments: make(map[string]struct{})}
	for _, s := range scopes {
		if s.Kind == content.ResourceEnvironment && s.ID != "" {
			c.environments[s.ID] = struct{}{}
		}
	}
	return c
}

// Session is the coordinator session every holdings answer is about.
func (c *WaveCoordinator) Session() string {
	if c == nil {
		return ""
	}
	return c.session
}

// MaySpawnInto reports whether the grant named this environment. A spawn
// outside it is REFUSED and the refusal names the environment; escalating
// instead is a property of a policy row rather than a special case for one
// tool, and is deliberately not invented here.
func (c *WaveCoordinator) MaySpawnInto(environment string) bool {
	if c == nil || environment == "" {
		return false
	}
	_, ok := c.environments[environment]
	return ok
}

// Environments lists what the grant named, so a refusal can say what WAS
// available rather than only what was not.
func (c *WaveCoordinator) Environments() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.environments))
	for id := range c.environments {
		out = append(out, id)
	}
	return out
}

// narrowWave builds the coordinator capability from the run's grant. Both wave
// tools share it: they are two acts of one authority, and a second constructor
// would be a second answer to "what may this run do to its own wave".
func narrowWave(grant content.Grant, _ []ResourceRef, runCtx RunContext) (Capability, error) {
	scopes := make([]content.GrantScope, 0, len(grant.Scopes))
	for _, s := range grant.Scopes {
		if s.Kind == content.ResourceEnvironment {
			scopes = append(scopes, s)
		}
	}
	return NewWaveCoordinator(runCtx.Session, scopes), nil
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
