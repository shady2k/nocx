package proto

import (
	"encoding/hex"
	"errors"
	"time"
)

// The second half of the frozen helper ABI: what a session IS, how one is
// started, and the inventory of the sessions a generation holds.
//
// nocx-k6p18.1 froze the attach/ack/detach/reset half and deliberately stopped
// there, because freezing `spawn` and `sessions` without their semantics would
// have been worse than leaving them. Their semantics land here, with the
// service that answers them, and so do the shapes — this is the last moment
// either is free, since a published generation serves the shape it shipped for
// the life of its sessions.
//
// # Launch is the authority; the OS is evidence (D10)
//
// The single most likely design error in this area is to let /proc answer
// "what is this session". It cannot: argv is mutable by the process itself, a
// process can be replaced by exec, /proc does not exist on macOS and
// proc_pidinfo answers a different set of questions there. So the helper
// RECORDS what it launched, at the moment it launches it — LaunchRecord, which
// no later reading can contradict — and offers OS inspection separately, as
// Observation, which is nil when nobody could be asked. Two fields, never one:
// merging them would report a lie with the authority of a launch record.
//
// # No human-authored name, ever (D3)
//
// The helper may report DERIVED diagnostics — cwd, argv, foreground process,
// start time — because those are facts about a process and the OS is their
// source. It may not persist a name a person typed. In level 1 a friendly
// alias is a local projection owned by the local server; in level 2 the host's
// ledger becomes its owner. One owner ever, and TestTheHelperPersistsNoHuman-
// AuthoredName is that decision made enforceable.

// The session service's lifecycle operations. They are service-level additions
// to the frozen frame ABI: an older helper answers unknown_op without changing
// how frames are decoded.
const (
	// OpSpawn starts a shell under a new PTY and returns its inventory entry.
	OpSpawn = "spawn"
	// OpSpawnSSH starts one session whose PROCESS is a shell channel on a
	// connection THIS helper dialed — an ssh pane (nocx-50w7p.4).
	//
	// It is a second op beside `spawn` rather than a destination field on
	// SpawnParams, and the freeze is what decides that rather than taste: every
	// shape here is `additionalProperties: false`, so a field added to `spawn`
	// is a payload an older generation REJECTS, while a new OP is one it
	// answers `unknown_op` to — and `unknown_op` is what a coordinator already
	// reads as "this machine's helper is older than this app". A destination
	// folded into `spawn` would also have to be absent ON EVERY LOCAL SPAWN,
	// which is a field whose absence is the common case and whose presence
	// changes what the op means.
	//
	// It shares the launch union and the inventory entry with `spawn`: a
	// session is a session, and a caller that spawned one and a caller that
	// found one must hold the same value.
	OpSpawnSSH = "spawn-ssh"
	// OpSessions is the inventory: every live host session this generation
	// holds.
	OpSessions = "sessions"
	// OpResize sets a session's window size.
	OpResize = "resize"
	// OpCloseSession deliberately ends one helper-hosted session and removes it
	// from the inventory.
	OpCloseSession = "close-session"
	// OpSignal sends one signal to the session's process group.
	OpSignal = "signal"
	// OpAdoptLifecycle hands a REPLACING coordinator the lifecycle launch the
	// helper spawned a session's shell with, so it can take over the domain
	// the shell is still speaking on (nocx-k6p18.31).
	//
	// It exists because the helper is the only party that still holds that
	// identity. The capability is minted by a coordinator's kernel at spawn
	// and reaches the shell through the helper; when the coordinator is
	// replaced, the shell goes on stamping every frame with a domain no live
	// kernel recognises, and the replacement drops all of them. The shell
	// cannot be told anything new — its end of the channel is a descriptor
	// handed over at spawn and it never re-handshakes — so the only leg that
	// can be re-established is the coordinator's, and this is what
	// re-establishes it.
	//
	// WHY GIVING A BEARER VALUE BACK IS NOT A WIDENING. ADR-0024 §13 draws
	// the boundary at hostile bytes on the terminal and at a DESCENDANT of
	// the shell that inherited the descriptor; neither can reach this op,
	// which is answered only over an authenticated coordinator↔helper
	// connection — the same connection the value travelled out on, in the
	// same direction class, to the same trust class that minted it. A caller
	// that can reach it already holds the session's keyboard and its whole
	// output stream. What it must NOT become is a field of the inventory:
	// `sessions` is asked constantly and by callers with no adoption to do,
	// and a bearer value that rides every listing is a bearer value with no
	// bound on who has seen it. Hence a separate op, asked once, per session.
	OpAdoptLifecycle = "adopt-lifecycle"
	// OpLifecycleComplete carries one already-authenticated completion DOWN
	// to the session that owns the pane (owner decision 2026-09-19). The
	// coordinator's kernel has validated version, domain liveness, transport
	// binding, epoch, capability and the sequence rule — authentication does
	// not move — and this op is the carrier of the fact, not a second gate
	// on any of it: the helper hands it to the session runtime's
	// AuthenticatedEvents.Completed, whose own incarnation check is the only
	// judging this side of the wire does. An older generation answers
	// unknown_op, which is the sentence a coordinator already reads as "this
	// machine's helper is older than this app".
	OpLifecycleComplete = "lifecycle-complete"
)

// Incarnation is a session runtime's identity on the wire: the session the
// PTY belongs to and which generation of it is live. It is
// internal/sessionruntime.Incarnation's own pair, spelled the way the
// one-shot token already spells it (tokenWire's atSession/atGeneration) —
// and it is deliberately NOT HostSessionID, whose Generation is the
// content-addressed INSTALL id: a string naming which build of the helper
// minted the session, not a number counting the PTYs the session has had.
// One field is a string install id, the other a numeric runtime generation,
// and a wire that carried them as one value would parse the install name
// into a generation count the first time a test read it back.
type Incarnation struct {
	Session    string `json:"session"`
	Generation uint64 `json:"generation"`
}

// LifecycleCompleteParams carries one completed execution's authenticated
// fact down to the session that owns the pane.
//
// It carries only facts the kernel has already accepted, and it accepts
// nothing: the helper resolves the session, decodes the fence and hands all
// three to the runtime. The runtime's incarnation check is what refuses
// evidence naming a dead incarnation, and this op adds no second gate on
// top of it — a refusal here could only refuse a delivery, never re-judge
// the kernel's acceptance.
type LifecycleCompleteParams struct {
	// Session addresses the helper session, generation-qualified like every
	// other op on this service: it is the lookup, not the identity the
	// runtime judges.
	Session HostSessionID `json:"session"`
	// Incarnation is the runtime incarnation the sender believes is live.
	// The legitimate sender echoes back what the helper's spawn told it —
	// the runtime is created at generation 1 and never replaced inside one
	// helper process — so a stale sender is refused by the runtime rather
	// than applied late.
	Incarnation Incarnation `json:"incarnation"`
	// Nonce is the kernel's render fence for the completed execution, as 64
	// lowercase hex characters — the same fixed-width spelling the launch's
	// bearer values use. The rendezvous matches on it exactly; a decoder
	// that truncated or tolerated another spelling would hand the runtime a
	// nonce nothing sighted.
	Nonce string `json:"nonce"`
	// ExitCode is the exit status the kernel recorded, and null when the
	// shell named none. Null is an ANSWER and is always present rather than
	// omitted, like every other nullable field on this wire: "the kernel
	// recorded no exit code" and "this generation does not say" are
	// different bytes.
	ExitCode *int `json:"exitCode"`
}

// LifecycleCompleteResult is deliberately empty, like AckResult and
// ResizeResult: the answer to "did the completion land" is the absence of an
// error. It exists so the op has a result type at all, the way every other
// op does.
type LifecycleCompleteResult struct{}

// AdoptLifecycleParams names the session whose lifecycle identity the caller
// intends to take over.
type AdoptLifecycleParams struct {
	Session HostSessionID `json:"session"`
}

// AdoptLifecycleResult is the launch the helper spawned this session's shell
// with, or null when the session is conventional and there is nothing to
// adopt.
//
// Null is an ANSWER and is always present rather than omitted, for the reason
// every other nullable field on this wire is: a coordinator must be able to
// tell "this session has no lifecycle channel" from "this generation does not
// answer the question", because the first is a conventional pane that is
// correct as it stands and the second is a degrade the product has to state.
type AdoptLifecycleResult struct {
	Lifecycle *LifecycleLaunch `json:"lifecycle"`
}

// CloseSessionParams deliberately ends one helper-hosted session.
type CloseSessionParams struct {
	Session HostSessionID `json:"session"`
}

// CloseSessionResult is empty; success is the answer that the session ended.
type CloseSessionResult struct{}

// SignalParams sends Signal to a process group of the session.
type SignalParams struct {
	Session HostSessionID `json:"session"`
	Signal  int           `json:"signal"`
	// Pgid names the group to signal. Zero — the level-1 shape, and still
	// legitimate — means the SESSION's own group, which is the shell.
	//
	// It exists because a stop is two statements and not one (nocx-uvac6.11):
	// name the addressee once, then signal THAT group through the whole
	// escalation, so a shell that starts another job between SIGINT and
	// SIGKILL is not hit by the second. The caller names it from the
	// foreground group this session's own inventory entry reports, and it
	// must be able to keep naming it — a helper that re-resolved "whatever is
	// in front now" on every call would be the race the ladder exists to
	// avoid.
	//
	// IT GRANTS THE CALLER NOTHING IT DID NOT ALREADY HAVE. Reaching this
	// service means having authenticated as the account that owns the helper
	// (D12 locally, ssh remotely), and that account may call kill(2) on the
	// same group directly. So this is a convenience of ADDRESSING and not a
	// widening of authority, which is why the helper signals what it is told
	// rather than keeping a policy about which groups are allowed — the
	// helper owns no policy (D3).
	Pgid int `json:"pgid,omitempty"`
}

// SignalResult is empty; success is the answer that the signal was delivered.
type SignalResult struct{}

// The events a helper sends unsolicited, as TypeNotify frames on the same wire
// as the data frames — so a reader sees exactly which bytes each fact sits
// between.
const (
	// EventSessionReset is the LIVE reset of one subscriber whose cursor fell
	// behind the window's base. Its params are a SessionReset.
	EventSessionReset = "reset"
	// EventSessionExit is the process ending. Its params are a SessionExit.
	// The helper owns exit status (D3), and it is a notification rather than
	// only an inventory field because a reader waiting on a command must not
	// have to poll to learn it finished.
	EventSessionExit = "exit"
	// EventSessionLiveness is what an ssh session's own keepalive prober
	// learned about the far end since the last time it checked. Its params
	// are a SessionLiveness (nocx-y6fh7 item 6).
	//
	// It is a NOTIFICATION, on the same reasoning EventSessionExit already
	// gives: the party that can observe this is the helper — it holds the
	// connection since ADR-0057 — and a coordinator polling for it would be
	// asking a question the answer to which is "nothing has changed" almost
	// every time. A session's terminal death is still reported through
	// EventSessionExit exactly as any other end is (the prober closes the
	// transport when it gives up, which is the ordinary channel-closed path
	// every other exit takes); this event is for the NON-terminal half — the
	// far end answering late, or not at all yet — which has no "the process
	// ended" fact to ride.
	EventSessionLiveness = "liveness"
)

// Notification is the payload of a TypeNotify frame: a service, an event and
// the event's own params. It is shaped like Request minus the id, because an
// unsolicited fact has no answer — and it carries its service so a later
// service's events cannot be mistaken for this one's.
type Notification struct {
	Service string `json:"service"`
	Event   string `json:"event"`
	// Params is the event's payload. It is `any` rather than json.RawMessage
	// because the sender marshals a typed value; the receiver decodes the
	// whole notification into a shape naming the event it expects.
	Params any `json:"params"`
}

// WorkspaceID is D15's reservation: opaque, coordinator-minted, and NEVER a
// display name. Human names bring rename, collision, normalisation, case and
// guessability into execution-host policy, and the helper owns no policy.
//
// It is unused by this generation — the workspace is coordinator-owned and
// `workspace.Default` is a coordinator-side constant — and it is required to
// stay. Document 2 makes one workspace reachable from two machines at once and
// needs no wire break to do it, precisely because the room is carried from the
// first day. A later optimisation looking only at what is READ would find the
// field unused and remove it; TestSpawnAndSessionsCarryTheReservedWorkspace is
// what stops that.
type WorkspaceID string

// SpawnParams starts one shell under one PTY.
//
// There is no argv, and there is no command: host.Register refuses any op whose
// params carry a free-form []string (D3), and that refusal is the point rather
// than an obstacle. The helper resolves the login shell through
// internal/loginshell, which is the same owner the coordinator's own local PTY
// asks — one answer to "which shell", not two.
type SpawnParams struct {
	// Workspace is D15's reservation. Empty is legitimate today.
	Workspace WorkspaceID `json:"workspace"`
	// Cwd is where the shell starts. Empty means the helper's own default,
	// which is the user's home — the same resolution internal/pty already
	// makes, rather than a second answer to the same question.
	Cwd string `json:"cwd"`
	// Env are additional environment entries for the shell, as a MAP rather
	// than the []string exec wants. Not only because D3's registration rule
	// refuses the slice: a map cannot express a positional argument, so no
	// caller can smuggle argv through it, and a duplicate key is impossible
	// rather than last-wins.
	Env map[string]string `json:"env,omitempty"`
	// Cols and Rows are the initial window size. XPixel and YPixel are the
	// client's cell metrics in TIOCSWINSZ's own units — the WHOLE text area
	// in pixels (cols × cell width, rows × cell height) — and zero means the
	// client has not measured itself yet. The helper decodes them into the
	// per-cell metric the runtime commits, at exactly one boundary
	// (internal/helper/session.cellGeometry).
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	XPixel uint16 `json:"xpixel"`
	YPixel uint16 `json:"ypixel"`
	// WindowBytes is the bound on this session's output window (D8). The
	// coordinator decides and the helper applies, clamped to the helper's own
	// floor, ceiling and aggregate budget — and the session keeps the bound it
	// was given for its whole life, so changing the setting affects the next
	// session and never a running one.
	//
	// int64 rather than int so the conversion on a 32-bit host cannot
	// overflow, which D8 asks for by name. Zero means the helper's default.
	WindowBytes int64 `json:"windowBytes"`
	// Lifecycle is optional for conventional sessions. When present, the
	// helper passes its descriptor-side channel and these values to the shell.
	// The capability is never copied into argv or environment.
	Lifecycle *LifecycleLaunch `json:"lifecycle,omitempty"`
	// IdempotencyKey is the caller's name for the SPAWN, not for the session
	// (L7 of the local-helper design). The helper mints the session id, so the
	// coordinator cannot record what it is about to get — but it can record
	// what it is about to ask for, and this is that record: a pane's claim is
	// written durably with this key BEFORE the spawn, and a spawn repeated
	// with the same key answers with the session the first one made rather
	// than forking a second shell.
	//
	// That is what lets the claim precede the first irreversible effect, which
	// is the worker record's own rule and was bought by the same failure: a
	// coordinator that dies between the helper's spawn and the durable binding
	// leaves a live PTY no pane claims, and — with the daemon lifecycle
	// unimplemented — the helper holds it forever.
	//
	// It is OPTIONAL and empty is legitimate: a caller that minted no claim
	// gets no promise, which is what every level-1 caller has always had. It
	// is opaque to the helper, which stores it and compares it and never
	// parses it, and it is never a name a person typed — the helper owns no
	// policy and persists no human-authored name (D3).
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// AgentToolEndpoint is the tool endpoint ON THIS HELPER'S MACHINE that
	// the pane this op starts belongs to: the CALLER's own tool socket
	// (internal/toolendpoint's), which is what the shell's NOCX_TOOL_SOCKET
	// names when the caller has one.
	//
	// It is a fact about the REQUEST and never about the daemon, and that is
	// the whole of why it is here (nocx-50w7p.18). A helper generation's
	// endpoint socket is keyed by the generation and not by a coordinator: one
	// account's daemon serves several coordinators at once (D12), so a value
	// this process read once from the environment of whichever coordinator
	// started it is a value about THAT coordinator — and a pane opened by
	// another one would carry it, reaching an endpoint that never asked for
	// that pane. The pane's owner is the party that knows, so the pane's
	// owner says it, per spawn.
	//
	// Empty is a real state rather than "not set yet": a coordinator that runs
	// no tool endpoint has none to name (cmd/nocx-server answers nil, nil when
	// it has no tool surface), and the pane then renders no NOCX_TOOL_SOCKET
	// at all — the shell's own refusal text is what a user sees, which is the
	// soft degrade nocx-2tesu exists to keep soft.
	AgentToolEndpoint string `json:"agentToolEndpoint,omitempty"`
	// AgentToolToken is the bearer that admits this pane's far agent, minted by
	// the coordinator that opened the pane (nocx-50w7p.16): what the far
	// launcher renders into the frame its shell reads, and what the agent's MCP
	// bridge presents before its first request.
	//
	// IT IS ON THIS SHAPE BY ANALOGY WITH SSHSpawnParams AND OUT OF THE SAME
	// NEED (nocx-e2bws): a pane on a far host is reached by a coordinator
	// whose pid relation to the pane does not exist, so the endpoint cannot
	// admit it by process ownership and needs the interval's bearer instead.
	// A pane on THIS machine is admitted as its coordinator's own child, which
	// is why the field is optional and empty for the ordinary local pane.
	//
	// It is a SECRET and this struct is not a place it rests: it goes into the
	// launch options, which render it into the descriptor the shell reads —
	// never into the agent env block, and never into a log line.
	AgentToolToken string `json:"agentToolToken,omitempty"`
}

// MaxIdempotencyKey bounds the key a caller may mint. The helper keeps one
// entry per live key for the life of the session it names, so the bound is
// what keeps a caller's bookkeeping from becoming the helper's memory
// footprint. It is stated here, next to the field, because the contract
// declares it and an unenforced bound in a schema is theatre.
const MaxIdempotencyKey = 128

// LifecycleLaunch carries the coordinator-minted authenticated channel
// bootstrap to the helper. Addressing is public; Capability and Recovery are
// bearer values and must be consumed only by the shell integration rcfile.
type LifecycleLaunch struct {
	Lane       string `json:"lane"`
	Domain     string `json:"domain"`
	Epoch      uint64 `json:"epoch"`
	Capability string `json:"capability"`
	Recovery   string `json:"recovery"`
}

// SpawnResult is the new session's inventory entry — the same shape `sessions`
// returns, so a caller that spawned one and a caller that found one hold the
// same value and cannot drift into two decoders.
type SpawnResult struct {
	Entry SessionEntry `json:"entry"`
}

// SSHShellKind is the far shell a remote session is launched FOR, in a closed
// set.
//
// It is the same four-member vocabulary shellintegration.ShellKind already
// owns, spelled here rather than imported because this package is the wire's
// leaf: proto is linked by every helper, including the untagged artifact
// deployed to somebody else's host, and that artifact carries no launcher
// (plan §1). The pairing is checked rather than hoped for —
// TestTheSSHShellKindSpellingsMatchTheLauncher is what keeps the two tables
// from drifting, in the package that converts between them.
type SSHShellKind string

const (
	// SSHShellAuto means the FAR side decides: the launcher emits one
	// strictly-POSIX dispatcher that detects the login shell at runtime and
	// execs the matching tier. It is the honest default, because which shell a
	// host logs you into is the host's business and the coordinator has not
	// always asked.
	SSHShellAuto SSHShellKind = "auto"
	// SSHShellBash and SSHShellZsh pin the tier a profile names.
	SSHShellBash SSHShellKind = "bash"
	SSHShellZsh  SSHShellKind = "zsh"
	// SSHShellUnknown means "start it, integrate nothing, and say so" — never
	// "substitute bash".
	SSHShellUnknown SSHShellKind = "unknown"
)

// SSHMode is what the caller WANTS of this session's integration, in the
// closed set internal/profile declares as DesiredMode.
//
// Spelled here for the leaf-package reason above, and the GATE is not spelled
// here: whether a mode delivers shell integration is profile.DesiredMode
// .DeliversScripts(), one predicate with one owner, which the helper's ssh
// spawner asks directly (it is build-tagged, and a tagged helper links
// internal/profile already through internal/ssh).
type SSHMode string

const (
	// SSHModeAuto: the caller has not answered for this destination. Scripts
	// as the default does, and the helper may be offered.
	SSHModeAuto SSHMode = "auto"
	// SSHModeRaw: nothing is added to the far side. A plain login shell, no
	// carrier, no frames, no publish.
	SSHModeRaw SSHMode = "raw"
	// SSHModeScript and SSHModeHelper integrate through the script tiers.
	SSHModeScript SSHMode = "script"
	SSHModeHelper SSHMode = "helper"
)

// SSHSpawnParams starts one session whose process is a shell channel on a
// connection this helper dials (nocx-50w7p.4).
//
// # There is no command here either, and this is the op it matters most for
//
// D3 refuses any op whose params carry a free-form []string, because an argv
// reaches a command line on somebody else's machine. The remote command this
// op results in is the launch CARRIER — bounded, payload-free, and built by
// the helper from internal/shellintegration — and the caller cannot name it,
// shorten it or substitute it. What the caller names is a DESTINATION and the
// shape of the session, which is the same division `spawn` draws between the
// environment and the program.
//
// The destination is RESOLVED (host, port, user) for the reason ProbeParams
// states at length: alias resolution, ~/.ssh/config merging and the
// credential's own authorization stay in the coordinator, which is the party
// that reads the config and holds the binding.
type SSHSpawnParams struct {
	// Workspace is D15's reservation, as in SpawnParams.
	Workspace WorkspaceID `json:"workspace"`
	// Destination is where the channel is opened and what it authenticates
	// with. Its identity is a REFERENCE plus, for a key, the public half the
	// helper must declare before the coordinator is asked to sign.
	Destination SSHDestination `json:"destination"`
	// AcceptOnTrust is the CALLER's decision about a host key nobody has
	// recorded, exactly as it is on a probe and on a channel open: the helper
	// may not trust a host on its own initiative, and the accept flow that
	// sets this flag already ran in the coordinator.
	AcceptOnTrust bool `json:"acceptOnTrust"`
	// HostKeyFingerprint is the fingerprint the caller BELIEVES this host
	// presents — the value its own known_hosts answered with, or the one a
	// person just accepted. Empty means the caller has no expectation and the
	// coordinator's verdict alone decides.
	//
	// When it is present it is ENFORCED, before anything is authenticated: a
	// handshake that offers a different key ends the spawn with
	// `host-key-changed`. It is a local bind on top of the coordinator's
	// verdict and not a substitute for it — the verdict travels over a
	// connection this helper has, and this value travelled with the REQUEST —
	// so two facts must agree before a shell is opened on somebody's host.
	HostKeyFingerprint string `json:"hostKeyFingerprint"`
	// Shell is the far shell this session is launched for. Empty means
	// SSHShellAuto.
	Shell SSHShellKind `json:"shell"`
	// Cwd is where the caller wants the far shell to start, and this
	// generation can only honour an EMPTY one.
	//
	// A non-empty value is refused by name rather than accepted and ignored.
	// The far login shell starts in the far account's own directory and this
	// helper has no mechanism to move it: the only ways to name a directory on
	// the far side travel as a command (which this wire refuses) or as an
	// extension the launcher does not carry. A field that accepted a value
	// nothing acts on is the shape that a later generation starts reading
	// under a caller that never expected it to — and the launch record has no
	// `cwd` key for a remote session for the same reason: this helper resolved
	// no directory, so it reports none.
	Cwd string `json:"cwd"`
	// Cols and Rows are the size the channel's pty is requested at. XPixel
	// and YPixel are the client's cell metrics in TIOCSWINSZ's whole-area
	// units, and zero means not measured. They reach the helper's runtime
	// geometry — what the published frames carry — and not the far pty,
	// whose window-change carries no pixel fields at all (x/crypto/ssh has
	// none to send).
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	XPixel uint16 `json:"xpixel"`
	YPixel uint16 `json:"ypixel"`
	// WindowBytes is the bound on this session's output window, clamped by the
	// helper exactly as SpawnParams' is. Zero means the helper's default.
	WindowBytes int64 `json:"windowBytes"`
	// Lifecycle is optional, on the same terms as SpawnParams'.
	//
	// WHAT THE HELPER DOES WITH IT, stated rather than implied: it asks the far
	// side for a loopback listener on the connection it dials (plan §6 — the
	// lifecycle tunnel is a remote listener, never a coordinator-side one),
	// renders the port into the launcher, and carries the capability in frame 2
	// of the bootstrap. A request it CANNOT honour — the far side refused the
	// listener — is refused by name before anything is spawned, so a caller
	// never waits out a hello budget for a channel that was never opened. The
	// two facts that the channel exists are the two a caller can already check:
	// the session's lifecycle window, and what `adopt-lifecycle` answers.
	Lifecycle *LifecycleLaunch `json:"lifecycle,omitempty"`
	// DesiredMode is the caller's integration intent, and the helper applies
	// profile.DesiredMode's own gate to it: `raw` opens a plain login shell
	// and integrates nothing, and an unrecognised value fails closed.
	DesiredMode SSHMode `json:"desiredMode"`
	// AgentToolEndpoint is the LOCAL socket every connection arriving on the
	// far-side tool socket is piped into: this pane's own coordinator's tool
	// endpoint on THIS machine, which is the one that asked for the pane.
	//
	// It is the second half of the pair above and it is deliberately a second
	// field: AgentToolSocketPath names a path on the FAR host, and this names
	// the endpoint on the machine the helper runs on. They are different
	// machines and different values, and one field could only have held the
	// first (nocx-50w7p.14).
	//
	// It travels PER SPAWN for the reason SpawnParams.AgentToolEndpoint does,
	// and it is the same defect one hop out (nocx-50w7p.18): a helper daemon
	// serves several coordinators of one account (D12), so an endpoint fixed
	// at the daemon's own start is a fact about whichever coordinator started
	// it — and a pane opened by another one would have its far agent's tool
	// connections forwarded to a coordinator that never asked for that pane.
	//
	// A far socket path with no endpoint here is REFUSED BY NAME before
	// anything is dialed: a forward to nothing is the silent degrade a launch
	// must never carry.
	AgentToolEndpoint string `json:"agentToolEndpoint,omitempty"`
	// AgentToolToken is the bearer the pane's agent presents to be admitted
	// (nocx-50w7p.16): what the coordinator minted for THIS pane and its
	// admission epoch, and what the endpoint on the coordinator's machine
	// compares before it will admit a connection arriving on the far-side tool
	// socket.
	//
	// It travels per request for the same reason AgentToolEndpoint does — a
	// daemon serves several coordinators, and this is a fact about the pane one
	// of them opened — and it is a SECRET, so the helper treats it as one: it
	// reaches the far shell by the descriptor frame and is never part of the
	// launch record, the logs, or anything the helper hands a child process.
	// Empty is a real state: a coordinator that requires no bearer (the local
	// tree rule admits those panes) sends none, and a pane whose frame carries
	// none admits nobody answering to a token.
	AgentToolToken string `json:"agentToolToken,omitempty"`
	// AgentToolsAbsent is WHY this pane's agent gets no nocx tools, from the
	// closed set internal/shellintegration owns (nocx-e2bws): the code, not a
	// sentence, because the SHELL renders the sentence a person reads and the
	// fact the shell cannot see is the reason.
	//
	// It travels on the ssh spawn because that is the route the reason is a
	// fact about: THIS machine's helper carries a pane whose shell runs on a
	// host with no nocx helper of its own, so there is no bridge for the agent
	// to run there and no socket for it to dial — and a shell told nothing
	// reports "path is not configured", which sends a person looking for a
	// setting no host has.
	//
	// Empty is the honest state for every other pane: nocx did not say, and the
	// shell keeps its own sentence.
	AgentToolsAbsent string `json:"agentToolsAbsent,omitempty"`
	// IdempotencyKey is the caller's name for the spawn, on exactly the terms
	// SpawnParams states: a repeat answers with the session the first one made
	// rather than forking a second remote shell.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// KeepaliveIntervalMS is how often, in milliseconds, THIS HELPER probes
	// the far end once the channel is open. Zero means no probing at all.
	//
	// It travels here because the helper is the party that now HOLDS the ssh
	// connection (ADR-0057): the coordinator has no transport of its own left
	// to probe, so "how often" is a fact this spawn must carry rather than a
	// setting the far side of the wire could apply on its own — the same
	// reason Shell and DesiredMode travel per spawn rather than living in the
	// helper's own defaults. Zero is a real, honest state (a profile with no
	// interval configured), not a gap: nocx-y6fh7 item 6 measured that the
	// pre-ADR-0057 coordinator-side prober silently vanished for every
	// helper-hosted pane once the dial moved here and nothing replaced it —
	// ssh-reconnect.spec.ts's silent-death and slow-host journeys had nothing
	// left probing at all.
	KeepaliveIntervalMS int64 `json:"keepaliveIntervalMs,omitempty"`
	// KeepaliveCountMax is the number of consecutive keepalive failures this
	// helper tolerates before it gives up on the channel, on the same terms
	// ssh.ConnectConfig.KeepaliveCountMax already states for the coordinator's
	// own (non-helper) dials. Meaningless when KeepaliveIntervalMS is zero.
	KeepaliveCountMax int `json:"keepaliveCountMax,omitempty"`
}

// SessionsParams asks for the inventory. The workspace filter is D15's
// reservation on the read side; empty means every session this generation
// holds, which is what level 1 always asks for.
type SessionsParams struct {
	Workspace WorkspaceID `json:"workspace"`
}

// SessionsResult is the inventory (D10). Sessions is never null on the wire:
// an empty inventory is `[]`, because a decoder distinguishing "no sessions"
// from "no answer" needs the empty array to arrive as one.
type SessionsResult struct {
	Sessions []SessionEntry `json:"sessions"`
}

// SessionEntry is one live host session as the helper knows it.
type SessionEntry struct {
	// Session is the durable handle, qualified by the generation that minted
	// it — which is this generation, since a helper can only report its own.
	Session HostSessionID `json:"session"`
	// Workspace is D15's reservation, echoed back as it was given.
	Workspace WorkspaceID `json:"workspace"`
	// StartedAt is when the helper spawned the process, in RFC 3339 with
	// nanoseconds and an offset. A wall-clock time rather than a monotonic
	// duration because the reader is a different process on a different
	// machine, and "how long ago" is a question only the reader's own clock
	// can answer honestly.
	//
	// It is the HELPER's record and not the kernel's. Observation.StartTime
	// is the kernel's answer about the process wearing this pid now, and the
	// pair is the pid-reuse guard: the two disagreeing is the only way
	// anything can notice that the pid was recycled. Never read one for the
	// other.
	StartedAt string `json:"startedAt"`
	// Launch is what the helper recorded when it spawned. AUTHORITY (D10).
	Launch LaunchRecord `json:"launch"`
	// Observed is what the OS says now. EVIDENCE, and null when the OS could
	// not be asked — never an empty record passed off as an answer, and never
	// an OMITTED field either: absent and null are different bytes, and a
	// reader must be able to tell "this generation reports no observation"
	// from "this generation does not send observations". That distinction is
	// what vault.status's missing defaultProvider cost a release to learn.
	Observed *Observation `json:"observed"`
	// Window is where the session's output stream currently stands. It is
	// what a reader with no position of its own attaches at: `base` is the
	// oldest byte that still exists, exactly as sessions.live's replayFrom
	// tells a fresh renderer today.
	Window WindowSpan `json:"window"`
	// LifecycleWindow is the bounded raw lifecycle-byte window. It is separate
	// from PTY output but uses the same Window/Resume owner and offsets.
	LifecycleWindow WindowSpan `json:"lifecycleWindow"`
	// Writer names the subscriber holding the session's one write capability,
	// and is null when nobody holds it — always present, like WriteGrant's own
	// Holder, so "nobody is writing" and "this helper does not say" are
	// different bytes.
	Writer *SubscriberID `json:"writer"`
	// WriterEpoch is that holder's lease, and zero when nobody holds it.
	WriterEpoch LeaseEpoch `json:"writerEpoch"`
	// Exit is the process's status once it has ended, and null while it runs.
	// The helper owns exit status (D3), and the entry keeps carrying it: a
	// coordinator replaced during a command comes back to an entry that can
	// still tell it how the command ended.
	Exit *SessionExitStatus `json:"exit"`
}

// LaunchKind discriminates the launch union (nocx-50w7p.4). It is a required
// field of every branch rather than something a reader infers from which keys
// are present, because "which of these is it" is the FIRST question a decoder
// asks and inferring it from a key's presence makes an incomplete record
// indistinguishable from the other branch.
type LaunchKind string

const (
	// LaunchKindLocal is a process on THIS machine: a PTY, a pid and a
	// process group the helper owns and signals.
	LaunchKindLocal LaunchKind = "local"
	// LaunchKindSSH is a shell channel on a connection this helper dialed:
	// the process is on the far host and this machine has no pid for it.
	LaunchKindSSH LaunchKind = "ssh"
)

// LaunchRecord is what the helper recorded at the moment it spawned.
// Nothing read from the OS afterwards may overwrite it: this is the canonical
// identity of the session (D10), and OS inspection is a cross-check against it.
//
// # Why it is a union now, and why the branches are separate TYPES
//
// A session's process is either a PTY this helper owns or a shell channel on a
// remote host, and the two have different facts: a local shell has a pid and a
// process group, a remote one has a destination and no pid AT ALL. Spelling
// that as one flat record with an optional pid would be a record whose `pid`
// key exists and reads 0 for a remote session — and 0 is the kernel scheduler,
// so a reader that trusted the key would ask the OS about a process this
// machine never started. Absence is the honest encoding, and a union with two
// branch TYPES is how absence stops being a convention and becomes a fact the
// decoder enforces: `sshLaunchRecord` declares no `pid` key, so no generation
// can put one there and no reader can find one.
type LaunchRecord struct {
	// Kind is the discriminator, and it is required.
	Kind LaunchKind `json:"kind"`
	// Local is the branch for a process on this machine, and is absent for the
	// other. Exactly one of the two is present; the schema enforces that with
	// `oneOf`, and the helper always sets exactly one.
	Local *LocalLaunchRecord `json:"local,omitempty"`
	// SSH is the branch for a remote shell channel.
	SSH *SSHLaunchRecord `json:"ssh,omitempty"`
}

// LocalLaunchRecord is the launch record a PTY session has always had. It is
// the same seven facts as before the union existed — a local session's record
// is unchanged, and only its position moved.
type LocalLaunchRecord struct {
	// Shell is the binary the helper actually started, as exec resolved it.
	Shell string `json:"shell"`
	// Cwd is the directory the helper started it in — the resolved one, not
	// the requested one, so an empty request is answered with the answer.
	Cwd string `json:"cwd"`
	// Pid is the shell process. Pgid is its process group, which is what the
	// helper signals: it owns the PTY and its process group (D3).
	Pid  int `json:"pid"`
	Pgid int `json:"pgid"`
	// Cols and Rows are the size the session was started at. The CURRENT size
	// is not here: a resize changes it, so a launch record claiming it would
	// be a fact that silently goes stale.
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
	// WindowBytes is the bound this session actually got, after the helper's
	// floor, ceiling and budget were applied to what the coordinator asked
	// for. Reported rather than assumed: a caller whose request was clamped
	// must be able to see that it was.
	WindowBytes int64 `json:"windowBytes"`
}

// SSHLaunchRecord is the launch record of a session whose process is a shell
// channel on a connection this helper dialed.
//
// # What is deliberately NOT here
//
// There is no `pid` and no `pgid`, and that is the shape rather than an
// omission: the process is on another machine, its pid belongs to that
// machine's namespace, and pid 0 — the only value left if a record insisted on
// carrying one — is the kernel scheduler. A helper that wrote it would report
// the scheduler's facts under this session's authority.
//
// There is no `cwd` VALUE either, and the key is kept rather than dropped:
// every reader of a launch record asks the same five questions of whichever
// branch it holds, and a key that exists in one branch and not the other is a
// decoder that has to branch before it can parse. What it carries is the
// honest answer — EMPTY — meaning this helper resolved no directory, because
// the far login shell starts in the far account's own home and nothing on this
// wire can see or move it. `spawn-ssh` refuses a caller's non-empty cwd by
// name rather than accepting one it could never honour, so the empty value is
// a fact and not a placeholder.
type SSHLaunchRecord struct {
	// Host, Port and User are the RESOLVED destination, echoed so a reader of
	// the inventory knows which machine this pane is on. The identity is a
	// REFERENCE and never material: the credential reference is the
	// coordinator's opaque handle and the helper never interprets it.
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	IdentityRef string `json:"identityRef"`
	// Shell is the far shell this session was launched FOR, in SSHShellKind's
	// closed set. `auto` is the honest value for the ordinary case: the far
	// side's own dispatcher decides which tier runs, and its answer is not
	// reported back on this wire.
	Shell string `json:"shell"`
	// Cwd is empty, always, in this generation — see the type's own comment:
	// the far login shell's directory is the far side's answer and this helper
	// neither asks for it nor changes it.
	Cwd string `json:"cwd"`
	// Cols and Rows are the size the channel's pty was requested at. The
	// CURRENT size is not here, for the same reason the local record omits it.
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
	// WindowBytes is the bound this session actually got.
	WindowBytes int64 `json:"windowBytes"`
}

// WindowBytes reports the output-window bound this session actually got, from
// whichever branch the record is. It is a method rather than a field on the
// union because the value belongs to the BRANCH: the two records carry it for
// the same reason and it is one question to a reader.
func (r LaunchRecord) WindowBytes() int64 {
	switch {
	case r.Local != nil:
		return r.Local.WindowBytes
	case r.SSH != nil:
		return r.SSH.WindowBytes
	}
	return 0
}

// Shell names the shell this session runs, from whichever branch it is. For a
// local session it is the resolved binary; for an ssh session it is the far
// shell kind the launcher was built for.
func (r LaunchRecord) Shell() string {
	switch {
	case r.Local != nil:
		return r.Local.Shell
	case r.SSH != nil:
		return r.SSH.Shell
	}
	return ""
}

// LocalPid is the pid of a process THIS machine runs, and ZERO when the
// session has no process here.
//
// Zero is the answer rather than a placeholder, and the caller acts on it: the
// OS-evidence seam takes a pid, pid 0 is the scheduler, and a session with no
// local process must therefore not be observed at all — which is exactly what
// the helper does with this value (session.entry), rather than passing a zero
// along and asking the kernel about the scheduler.
func (r LaunchRecord) LocalPid() int {
	if r.Local == nil {
		return 0
	}
	return r.Local.Pid
}

// LocalPgid is the process group the helper owns for a LOCAL session, and zero
// for one it does not own. Zero means "no group here", which the signal path
// reads as "the session's own process, whatever kind it is" — for a remote
// session that is the shell channel.
func (r LaunchRecord) LocalPgid() int {
	if r.Local == nil {
		return 0
	}
	return r.Local.Pgid
}

// IsLocal reports whether this session's process runs on this machine. It is
// the question the OS-evidence seam must ask before it is asked anything else.
func (r LaunchRecord) IsLocal() bool { return r.Local != nil }

// Observation is what the OS says about the process NOW. It is evidence: a
// cross-check against the launch record and a source of the derived
// diagnostics D3 permits, never the canonical identity.
type Observation struct {
	// Source names where the evidence came from — "procfs" on Linux, "sysctl"
	// on macOS. Evidence that cannot say where it came from cannot be weighed
	// against a launch record it contradicts, and the two sources answer
	// different subsets: see Unavailable.
	Source string `json:"source"`
	// Cwd is the process's current working directory. It CHANGES as the user
	// cds, which is exactly why it is here and not in the launch record.
	Cwd string `json:"cwd,omitempty"`
	// Argv is the shell's own argument vector as the OS reports it. It is
	// mutable by the process itself, which is why it is evidence — and it is
	// a []string in a RESULT, which D3's registration rule does not touch:
	// that rule refuses argv as an INPUT, because an input reaches a command
	// line.
	Argv []string `json:"argv,omitempty"`
	// ForegroundPgid is the process group the terminal is currently giving
	// input to, and ForegroundCommand its command name: "what is running in
	// this session right now", which is the diagnostic a person actually
	// wants. Zero and empty when the shell itself is in the foreground.
	ForegroundPgid    int    `json:"foregroundPgid,omitempty"`
	ForegroundCommand string `json:"foregroundCommand,omitempty"`
	// StartTime is when the KERNEL says this process began, RFC 3339 with
	// nanoseconds — the same spelling FormatTime gives every other time on
	// this wire.
	//
	// # Why this is not SessionEntry.StartedAt under a second name (nocx-k6p18.12)
	//
	// The two are adjacent and they are not the same fact, and the reason
	// they must both exist is the reason they may disagree. StartedAt is the
	// HELPER's record of when it spawned — authority, one entry up, and
	// unfalsifiable by anything read later. StartTime is the OS's answer
	// about whatever process now wears that pid — evidence, in here with the
	// rest of the evidence, and it is the PID-REUSE GUARD: a pid alone cannot
	// say whether the process answering today is the one we launched, and
	// (pid, startTime) can. Merge them and the guard has nothing to compare
	// against; give the evidence the authority's name and a reader cannot
	// tell which one it is holding.
	//
	// So they are told apart by WHERE THEY SIT rather than by a longer name:
	// `entry.startedAt` is what we did, `entry.observed.startTime` is what
	// the kernel says, and every field inside `observed` is already scoped by
	// that. A `processStartTime` here would be the only field in the record
	// carrying a prefix its siblings do not need.
	//
	// Its resolution differs by platform and neither platform is exact:
	// macOS reports microseconds, Linux reports hundredths of a second (see
	// the procfs source). It is an identity, not a stopwatch.
	StartTime string `json:"startTime,omitempty"`
	// Ppid is the parent the OS reports NOW. It is evidence rather than a
	// constant: a helper that dies leaves its sessions reparented, and the
	// entry then says so instead of implying the helper still owns them.
	Ppid int `json:"ppid,omitempty"`
	// State is the kernel's process state, in the closed vocabulary
	// ProcessState declares. It is the answer to "is this shell running,
	// sleeping, stopped or a zombie", which a stopped session and a live one
	// are indistinguishable without.
	State ProcessState `json:"state,omitempty"`
	// Unavailable names every diagnostic above that this inspector was asked
	// for and could not supply. It is ALWAYS present — `[]` when everything
	// asked for was answered — and never omitted, for the same reason
	// `observed` itself is never omitted.
	//
	// # Why a field rather than an empty value (nocx-k6p18.10)
	//
	// A missing diagnostic and a stale one must not look alike, and here they
	// otherwise would. Every field above is `omitempty`, so a diagnostic the
	// OS could not answer arrives as nothing at all — and a reader with
	// nothing falls back to the LAUNCH record, which was true once and goes
	// stale the moment the user cds. That is a stale value presented as a
	// current observation: exactly the lie the authority/evidence split exists
	// to prevent, arriving by the back door.
	//
	// macOS is where it bites and why this exists. There is no /proc, and the
	// only route to another process's working directory is
	// proc_pidinfo(PROC_PIDVNODEPATHINFO), which needs cgo. Nothing forbids
	// cgo; what it costs is the build, and the cost is named in
	// nocx-k6p18.14. sysctl answers
	// argv and the foreground command cgo-free and cannot answer cwd at all,
	// so the shipped platform reports `["cwd"]` here and a reader can say "we
	// do not know where this shell is" instead of showing where it started.
	//
	// The rule is per-OBSERVATION and not per-platform: a /proc read that was
	// refused is named the same way, because the reader's problem is identical
	// and the reason is not its business.
	Unavailable []Diagnostic `json:"unavailable"`
}

// Diagnostic names one derived observation the helper may report. The set is
// closed and matches Observation's own optional fields, so a reader can switch
// on it exhaustively: a free-form string here would let a later generation
// invent a name nothing understands, which is the same defect
// additionalProperties:false refuses one level up.
type Diagnostic string

const (
	// DiagnosticCwd is the process's current working directory — the one that
	// changes as the user cds, and the one macOS cannot answer cgo-free.
	DiagnosticCwd Diagnostic = "cwd"
	// DiagnosticArgv is the shell's own argument vector as the OS reports it.
	DiagnosticArgv Diagnostic = "argv"
	// DiagnosticForegroundCommand is the command name of whatever holds the
	// terminal's foreground group. It is named unavailable only when there
	// WAS a foreground group to ask about: a shell alone in the foreground is
	// an answer, said by omission, and not a diagnostic that went missing.
	DiagnosticForegroundCommand Diagnostic = "foregroundCommand"
	// DiagnosticStartTime is the kernel's own start time for the process —
	// the pid-reuse guard, and the fact whose absence quietly turns every
	// liveness answer into "some process has this pid".
	DiagnosticStartTime Diagnostic = "startTime"
	// DiagnosticPpid is the parent pid.
	DiagnosticPpid Diagnostic = "ppid"
	// DiagnosticState is the kernel's process state. It is named unavailable
	// both when the status read failed and when the kernel answered a code
	// this closed vocabulary cannot spell: inventing a value for a state we
	// cannot name is the same defect as leaving the field blank, one step
	// further from being noticed.
	DiagnosticState Diagnostic = "state"
)

// ProcessState is the kernel's process state, normalised into one closed
// vocabulary — because the two kernels spell it differently and a field
// carrying `R` from one and `2` from the other would be two facts wearing one
// name. Linux's /proc/<pid>/stat gives a letter, macOS's kinfo_proc gives an
// SRUN/SSLEEP/SSTOP/SZOMB integer, and both are mapped here rather than at the
// reader, which cannot know which kernel answered.
//
// The set is the INTERSECTION plus what one platform can say and the other
// cannot lie about. `uninterruptible` exists because Linux distinguishes it
// and a shell hung on a dead network mount is precisely the session a person
// is looking for; macOS does not distinguish it and reports `sleeping`, which
// is less precise and still true. A code outside this set is reported as the
// state being UNAVAILABLE — never as a value invented to fill the field.
type ProcessState string

const (
	// ProcessRunning is on a CPU or on a run queue. Linux `R`, macOS SRUN.
	ProcessRunning ProcessState = "running"
	// ProcessSleeping is an interruptible wait — what an idle shell at a
	// prompt is. Linux `S`, macOS SSLEEP.
	ProcessSleeping ProcessState = "sleeping"
	// ProcessUninterruptible is a wait no signal can break, which is how a
	// shell on a wedged mount looks. Linux `D`; macOS cannot distinguish it
	// and answers ProcessSleeping.
	ProcessUninterruptible ProcessState = "uninterruptible"
	// ProcessStopped is suspended — SIGSTOP, ^Z, or a debugger. Linux `T`
	// and `t`, macOS SSTOP. This is the state that makes a dead-looking
	// session explicable.
	ProcessStopped ProcessState = "stopped"
	// ProcessZombie has exited and nobody has reaped it. Linux `Z`, macOS
	// SZOMB. The session's window still exists; the shell does not.
	ProcessZombie ProcessState = "zombie"
)

// WindowSpan is the current extent of a session's output window: Base is the
// oldest offset that still exists, Written is the total ever produced. The
// interval it states has both ends, deliberately — a reader can tell from it
// exactly which offsets will be served and which will be reset.
type WindowSpan struct {
	Base    StreamOffset `json:"base"`
	Written StreamOffset `json:"written"`
}

// SessionExitCause is WHY a session ended, in a closed set that is empty
// (omitted) for the ordinary case: a process the far side actually ran to
// completion or that closed with no cause this helper can name.
//
// It exists because "Code == -1, Signal == 0" is one shape with two
// different meanings (nocx-y6fh7 item 6, round 3): the far side hanging up
// mid-session with no status to report (TestAChannelLostMidSessionEndsThe
// SessionWithAStatus, which stays exactly as it is), and THIS helper's own
// keepalive prober giving up and closing the connection itself. Both leave
// no process status to collect, so only the helper — the party running the
// prober since ADR-0057 — can tell them apart, and it does so by naming the
// cause rather than by the coordinator guessing from a code that is
// identical either way.
type SessionExitCause string

const (
	// ExitCauseKeepaliveLost is a connection this helper's OWN prober gave
	// up on: it stopped believing the far end was there and closed the
	// transport itself. This is CONNECTION LOSS, not a clean process exit —
	// the coordinator's ExitOutcome maps it to Interrupted so the pane is
	// offered the way back, the same offer any other channel loss gets.
	ExitCauseKeepaliveLost SessionExitCause = "keepalive-lost"
)

// SessionExitStatus is how a session's process ended.
type SessionExitStatus struct {
	// Code is the exit status, or -1 when the process was killed by a signal
	// (Signal is then set) or when no status could be collected.
	Code int `json:"code"`
	// Signal is the signal that ended it, and zero otherwise.
	Signal int `json:"signal,omitempty"`
	// At is when the helper observed the end, RFC 3339 with nanoseconds.
	At string `json:"at"`
	// Cause names WHY this ended when Code/Signal alone cannot say — see
	// SessionExitCause. Empty is the ordinary case: an authoritative exit,
	// or a loss with no more specific cause to report.
	Cause SessionExitCause `json:"cause,omitempty"`
}

// SessionExit is the EventSessionExit notification's params.
type SessionExit struct {
	Session HostSessionID     `json:"session"`
	Status  SessionExitStatus `json:"status"`
}

// SessionLiveness is the EventSessionLiveness notification's params: one
// ssh session's keepalive prober reporting whether the far end answered this
// round, and how long it took when it did.
//
// It names the session rather than the destination the way SessionExit does,
// for the same reason: this is a fact about a PROCESS this generation is
// answerable for, not about a host in the abstract. A helper that shares one
// pooled connection across several sessions (AD-4) reports against whichever
// session's own spawn armed the prober; nothing here claims the fact for
// every session on that connection, which is the coordinator's own concern
// to fan out if it chooses to (session.Reg.ObserveHost already does, keyed
// by host, for the sessions it is told about).
type SessionLiveness struct {
	Session HostSessionID `json:"session"`
	// Responsive is this round's verdict: the far end answered a keepalive
	// request before the deadline, or it did not.
	Responsive bool `json:"responsive"`
	// RoundTripMS is how long an answered round took, in milliseconds. Zero
	// (and omitted) means unresponsive, or a first round with nothing yet to
	// measure — the same "no measurement" reading the coordinator's own
	// direct dials already give this fact (ssh.Reachability.RoundTrip).
	RoundTripMS int64 `json:"roundTripMs,omitempty"`
}

// AckResult is deliberately empty, like ResizeResult: the answer to "did the
// cursor advance" is the absence of an error. It exists so every op has a
// result type, rather than one of them answering with a bare null that a
// decoder has to special-case.
type AckResult struct{}

// ResizeParams sets one session's window size. XPixel and YPixel carry the
// client's cell metrics in TIOCSWINSZ's whole-text-area units, and zero
// means not measured — the session keeps running with no cell metric rather
// than inventing one.
type ResizeParams struct {
	Session HostSessionID `json:"session"`
	Cols    uint16        `json:"cols"`
	Rows    uint16        `json:"rows"`
	XPixel  uint16        `json:"xpixel"`
	YPixel  uint16        `json:"ypixel"`
}

// ResizeResult is deliberately empty: the answer to "did the resize land" is
// the absence of an error. It exists so the op has a result type at all, the
// way every other op does.
type ResizeResult struct{}

// FormatTime is how every time on this wire is spelled: RFC 3339, nanoseconds,
// with an offset. One spelling, in one place, because two would eventually be
// parsed by one decoder.
func FormatTime(t time.Time) string { return t.Format(time.RFC3339Nano) }

// ResumeAt is THE decision rule of the helper's bounded output window: is the
// requested offset still in the window, and where does the reader restart.
//
// nocx-k6p18.1 deliberately did not write it, so that it would land together
// with the window it decides for and have exactly one owner. That owner is
// here, beside the Resume shape that states its answer, and
// internal/transport's outputRing.snapshot — which has asked the same question
// since AD-9 — delegates its verdict to it rather than keeping a second
// derivation of the same predicate.
//
// The two windows differ in ONE thing and the difference is in the caller, not
// here: where a reset restarts. This window is capacity-reclaimed, so a
// request below the base is a fact about the stream — nobody ever held those
// bytes — and the honest restart is the oldest byte that still exists, with
// the hole stated. The coordinator's ring is lossless, so a byte leaves it
// only after a consumer passed it, and a request below ITS base is a stale
// cursor rather than a loss; that caller therefore reads only the verdict.
//
// base ≤ written is the caller's invariant, held by the window from the moment
// it is created until it is closed: base only advances by reclaiming bytes
// that were written, and written only grows.
func ResumeAt(base, written, requested StreamOffset) Resume {
	if requested < base {
		return Resume{
			Reset: true,
			From:  base,
			Gap:   &Gap{Start: requested, End: base, Reason: GapReasonWindow},
		}
	}
	if requested > written {
		// Ahead of the stream: a caller defect, and NOT a reset. Answering it
		// with a reset would tell the reader that bytes were lost which were
		// never produced — a false statement in the product. It is parked at
		// the end, where it waits for bytes that do not exist yet.
		return Resume{Resumed: true, From: written}
	}
	return Resume{Resumed: true, From: requested}
}

// SessionHex spells a session's 16 raw id bytes as the 32 lowercase hex
// characters HostSessionID.Session carries. The two spellings are one identity
// — the control plane addresses a session by the hex, the data frame by the
// raw bytes — and this pair is the only crossing between them, so a session
// reached on one plane is reachable on the other without a lookup.
func SessionHex(raw [16]byte) string { return hex.EncodeToString(raw[:]) }

// ErrSessionIDMalformed reports a session id that is not 32 hex characters.
var ErrSessionIDMalformed = errors.New("proto: session id is not 32 hex characters")

// SessionBytes is SessionHex's inverse.
func SessionBytes(s string) ([16]byte, error) {
	var out [16]byte
	if len(s) != 32 {
		return out, ErrSessionIDMalformed
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return out, ErrSessionIDMalformed
	}
	copy(out[:], raw)
	return out, nil
}
