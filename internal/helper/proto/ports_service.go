package proto

// The `ports` service: how a coordinator reads a host's listening sockets
// through the far helper that holds its connection (ADR-727g63tr, design
// §5.10, nocx-oalo1.13.2).
//
// # Why a service and not an ssh op
//
// Port discovery used to run over ssh: the coordinator asked a lease to exec a
// named probe and parsed what came back. The owner's invariant of 2026-09-13
// puts every connection in the helper, and a probe is a command on that
// connection — so the ladder moved to the far side, beside the connection it
// serves. What crosses here is a QUESTION and its ANSWER, never a command or an
// argv (D3): the helper owns the ladder, the host's tools, and the sentence a
// failure is explained with.
//
// # Three outcomes, and no RPC-level refusal
//
// `available`, `available-limited`, `unavailable` and `failed-transiently` are
// answers, not refusals. In particular `unavailable` — a host with no port tool
// at all — is the SERVICE's answer, so the connection's helper axis stays
// `serving` and Git and Files keep working on the same connection (design
// scenario 8). A refusal here would tell a person their helper is broken when
// the truth is that this host has no ss.
//
// # Why the shapes are here and not in the service package
//
// Both ends of this socket are the same Go package, and
// `contracts/helper/ports.sample*` freezes each shape. Keeping the DTOs beside
// the op constant is what lets the contract test marshal the EXACT struct the
// service returns rather than a second, hand-written copy that agrees until one
// of them is edited.
//
// The result is mapped from portsprobe's vocabulary by the service, not by the
// ladder: the ladder answers Sample, and this file is the wire's spelling of
// it. Both spellings are pinned by tests on the service side.

// ServicePorts is the name of the helper service that samples this machine's
// listening sockets for a coordinator.
const ServicePorts = "ports"

// OpPortsSample runs ONE sampling pass over the host's own tools and answers
// what it found. It takes no params: the ladder, its order and its bounds are
// the helper's, and a caller that could choose a rung could make the helper run
// a command it did not choose.
const OpPortsSample = "sample"

// The state spellings a sample answers with. They are portsprobe's own values,
// character for character — a second vocabulary for one fact is the defect
// AD-8 names — and the schema's enum is the fence that keeps them equal.
const (
	// PortsStateAvailable: the pass ran and its table is the answer. An empty
	// listener list here means "nothing is listening", which is a fact.
	PortsStateAvailable = "available"
	// PortsStateAvailableLimited: the pass ran, but no row carries process
	// evidence the probe could see.
	PortsStateAvailableLimited = "available-limited"
	// PortsStateUnavailable: no port tool exists on this host (scenario 8).
	PortsStateUnavailable = "unavailable"
	// PortsStateFailedTransiently: the pass hit its bound, a capture was
	// truncated, or a tool could not be executed. Retryable, and never a
	// connection-loss statement.
	PortsStateFailedTransiently = "failed-transiently"
)

// PortsSampleParams is deliberately empty. A caller has nothing to say about a
// sample: the host is the connection's, the ladder is the helper's, and the
// bounds are the helper's own. The empty shape is also the one every older
// generation accepts, which is what keeps this op's refusal a fact about the
// generation (`unknown_service`) rather than a `bad_params` lie about a request
// that was well-formed.
type PortsSampleParams struct{}

// PortsSampleResult is one sampling pass.
type PortsSampleResult struct {
	// State is one of the PortsState* spellings.
	State string `json:"state"`
	// Listeners is never null: an empty pass marshals as [], so a reader never
	// has to tell "nothing listens" from "no answer".
	Listeners []PortsListener `json:"listeners"`
	// Probe names the rung that produced the sample, empty when none did.
	Probe string `json:"probe"`
	// ProbesTried names every rung this connection has run, in order, each
	// once. Never null, and the diagnostics half of `unavailable`.
	ProbesTried []string `json:"probesTried"`
	// Classification is the helper's sentence about why this is the answer —
	// displayed, never matched. A caller that switches on prose is the defect
	// the typed states exist to prevent.
	Classification string `json:"classification"`
	// Stderr is a bounded excerpt of the probe's own stderr, for a person
	// looking at a host whose tool refused its flags.
	Stderr string `json:"stderr"`
}

// PortsListener is one listening TCP port, as the host's tool reported it.
type PortsListener struct {
	// Family is "ipv4" or "ipv6".
	Family string `json:"family"`
	// Address is the bind address as the probe reported it, which may be a
	// wildcard.
	Address string       `json:"address"`
	Port    int          `json:"port"`
	Process PortsProcess `json:"process"`
}

// PortsProcess is the process evidence for one listener. It is three-valued and
// never an empty string: "nobody owns it" and "I was not allowed to see" are
// different facts and the panel renders them differently.
type PortsProcess struct {
	// Evidence is "known", "permission-denied" or "unsupported" — portsprobe's
	// own ProcessEvidence values.
	Evidence string `json:"evidence"`
	// Name and PID are valid when Evidence is "known".
	Name string `json:"name"`
	PID  int    `json:"pid"`
}
