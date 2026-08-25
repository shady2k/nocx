package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/apibind"
	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/backup"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/connectfwd"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/snippet"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/transport/outbound"
	"github.com/shady2k/nocx/internal/tunnel"
	"github.com/shady2k/nocx/internal/uistate"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/version"
	gossh "golang.org/x/crypto/ssh"
)

// sessionRx wraps a session's output ring together with the current attached
// subscriber. It lives connection-independently so the ring survives a
// disconnect and a reattached connection becomes the new subscriber. Exactly
// one monitorExit goroutine is started per session (via monitorOnce).
type sessionRx struct {
	ring        *outputRing
	mu          sync.Mutex // protects subscriber, subState
	subscriber  *wsConn    // current attached connection (nil if none)
	subState    *connState // subscriber's connection-scoped state
	monitorOnce sync.Once
	// inputStalled is true from the moment this session's write queue
	// refuses a frame until it accepts one again. It exists to make the
	// notification fire once per stall rather than once per keystroke:
	// a person holding a key against a stuck channel would otherwise
	// raise a hundred of them.
	inputStalled atomic.Bool
}

func (rx *sessionRx) setSubscriber(wconn *wsConn, state *connState) {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	rx.subscriber = wconn
	rx.subState = state
}

func (rx *sessionRx) getSubscriber() (*wsConn, *connState) {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	return rx.subscriber, rx.subState
}

// clearSubscriber drops the subscriber slot iff it still holds wconn —
// connection teardown. A newer subscriber (a reattach that replaced this
// connection) is preserved. The slot is what the lifecycle establishment
// acknowledgement validates against (decision 9): a detached connection's
// late ack must never release an accept, and with the slot cleared on
// teardown it cannot.
func (rx *sessionRx) clearSubscriber(wconn *wsConn) {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	if rx.subscriber == wconn {
		rx.subscriber = nil
		rx.subState = nil
	}
}

type WSServer struct {
	log      log.Logger
	registry session.Registry
	server   *http.Server
	port     int

	// Per-launch capability token (bead nocx-hl3).
	token       string
	tokenSource io.Reader // entropy source; crypto/rand default
	origins     OriginPolicy

	// Override the listen address. Default 127.0.0.1:0.
	listenAddr string
	listener   net.Listener
	upgrader   websocket.Upgrader
	// Optional profile/group stores for the connection-manager control
	// plane (profiles.*, groups.*). When nil, those methods return a
	// JSON-RPC error.
	profiles    profile.ProfileRepository
	groups      profile.GroupRepository
	credentials credential.SecretStore
	// Vault lifecycle for vault.* RPC methods. When nil, those methods return a
	// JSON-RPC error.
	vaultLifecycle VaultLifecycle
	vaultReset     VaultResetService
	// Native dialog capability (dialog.* RPCs). When nil, those methods
	// return -32601: the dev-web harness has no Wails runtime to open a
	// dialog with. Set post-construction from main.go's WailsApp.startup,
	// which is the only place the Wails context exists; guarded because the
	// handler may read it while startup assigns it.
	dialogMu      sync.RWMutex
	dialogService DialogService
	// Native URL-open capability (shell.openUrl). When nil, the method
	// returns -32601: the dev-web harness has no Wails runtime to open a
	// browser with. Set post-construction from main.go's WailsApp.startup,
	// guarded like the dialog service.
	urlMu     sync.RWMutex
	urlOpener UrlOpener

	// Profile resolver maps profile IDs to SSH connect configs. The holder
	// is set post-construction (SetProfileResolver): the resolver depends on
	// the transport, so the operations and handlers built at construction
	// hold the holder and read the current value per call.
	resolver *resolverHolder

	// notifyRaiser raises program-sourced notifications through the notify
	// pipeline (ADR-0029). Wired through WithNotifyRaiser; when nil,
	// notify.raise answers -32601.
	notifyRaiser NotifyRaiser

	// Profile service provides a single validated write path for profiles
	// and groups through the domain layer.
	profileSvc *profile.ProfileService

	// broker is the server→client request broker (nocx-e2j1z): the
	// mechanism backend code asks the renderer through (readScreen — the
	// first production request). Constructed in buildControlPlane with the
	// server's own connection set and per-connection enqueue as its
	// delivery seams; its resolution RPCs register on the read-loop ingress
	// and its ConnectionLost signal fires in unregisterConn.
	broker *Broker
	// runLeaseCfg bounds one run execution (ADR-0020 decision 2 — the
	// wall-clock deadline, the inactivity deadline, the output budget and
	// the escalation grace RequestRun supervises every run under). Zero
	// means the package default (defaultRunLease); every bound zero
	// disables the lease and restores the pre-lease broker timeout.
	runLeaseCfg RunLeaseConfig
	// lanes is the per-session lane interactivity state (ADR-0020 decision
	// 3): the awaiting-takeover transition decided in Go from the
	// renderer's agent.laneInteractivity reports. RequestRun refuses a
	// lane awaiting takeover (the agent lost write authority); the run
	// lease suspends its enforcement while a TUI owns the lane.
	laneInteractivity *laneState
	// agentPolicy is the ONE global agent policy the ask run grants are
	// minted from (ADR-0020 §7 as amended 2026-08-16, accepted): the global
	// default of content.ResolvePolicy, overridden by the workspace grant
	// source (nocx-mp2vd) when that lands.
	// Named by the composition root. Unset, ask runs carry no grant and the
	// model is offered no tools — the state before readScreen (see
	// runGrantFor).
	agentPolicy assistant.GlobalPolicy
	// liveEffects is which of that policy's seven rows govern anything at
	// all: the effect classes at least one DECLARED tool carries. It is
	// static, derived at build time from the tool declaration table, and it
	// arrives from the composition root for the same reason every other
	// module does (AGENTS.md: interface-first + DI, wired at ONE root) —
	// reaching into the tool registry from a handler would make this the
	// single place the transport knows a concrete tools package. Unset, the
	// settings surface is told no row is live, which is a visible degrade
	// rather than a silent claim that all seven govern something.
	liveEffects []content.Effect
	// sessionPolicy holds each session's "allow in this session" answers —
	// the overlay content.ResolvePolicy applies on top of the global policy
	// for a run in that session. Dropped at every session teardown; the
	// store and the drops are ws_sessionpolicy.go's subject.
	sessionPolicy *sessionPolicyStore

	// settings registry backs the settings.* JSON-RPC methods.
	settings *settings.Registry
	// snippets is the snippet library service backing the snippets.* JSON-RPC
	// methods. When nil, those methods return -32601.
	snippets *snippet.Service
	// notes is the notes library service backing the notes.* JSON-RPC
	// methods. When nil, those methods return -32601.
	notes *note.Service
	// The API-testing surface (design §6, §7). Four seams that wire
	// independently, so each is its own field and each has its own -32601:
	// apiCollections is the collection folder service backing
	// api.collections.* and api.request.read/write; apiSender additionally
	// backs api.request.send; apiBindings is where an import puts a secret
	// VALUE (design §8.1) and additionally backs api.import.postman.
	// api.import.curl needs none of them.
	//
	// apiVariables is the binding document's READ half, and it is a separate
	// field from apiBindings because the two are separate contracts in
	// apibind for a reason that survives here: a holder of Store can write a
	// value and can only ever get an IDENTIFIER back, while a holder of
	// ValueResolver can ask what a variable is worth and has no parameter
	// through which an identifier could arrive. The send path is given only
	// the second, so no identifier for credential material exists anywhere
	// on the path from a collection file to a header (design §8).
	//
	// apiFetch is the fifth: it acquires an import document by URL, over
	// the same route table the sender dials through. It wires separately
	// too — a build without it still imports by path and by document, and
	// answers the URL entrance by name rather than pretending.
	apiCollections apicoll.Collections
	apiSender      apisend.Sender
	apiBindings    apibind.Store
	apiVariables   apibind.ValueResolver
	apiFetch       apifetch.Fetcher
	// uiState owns what the app remembers without being asked (ADR-0033);
	// it backs the uistate.* JSON-RPC methods. When nil, those return
	// -32601 and the shell keeps its declared defaults.
	uiState *uistate.Store
	// build is what app.about answers with: what this binary is. Zero-valued
	// unless the composition root passes one, and the zero value is honest —
	// every field then reads "unknown" rather than claiming a version this
	// build does not have (see WithBuildInfo).
	build version.BuildInfo
	// Structured backup capability and native file saver. The operation is
	// constructed after all options so it shares the current config gate.
	backupService   *backup.Service
	backupFileSaver func(string, string) (*backup.SaveResult, error)

	// sandboxSvc is the per-pane filesystem sandbox backend (ADR-0036),
	// answering sandbox.status and preparing sandboxed open requests. The
	// transport never renders policy — it validates the request and maps the
	// backend's typed errors to reserved codes.
	sandboxSvc sandbox.Service
	// sandboxAccess is the bounded, in-memory denied-access inbox. It owns
	// event state and promotion; the transport only validates and serializes.
	sandboxAccess *sandbox.AccessInbox

	// SSH config resolver and config path for the ssh.listAliases RPC.
	// When nil, the handler returns a JSON-RPC error. The resolver
	// answers values via ssh -G; enumeration reads Host patterns from
	// the config file directly (see internal/ssh/aliases.go for the
	// mechanics).
	sshConfigResolver ssh.ConfigResolver
	sshConfigPath     string

	// remoteLauncher builds the start command for integrated remote shells
	// (nocx-xs1d), adapted from shellintegration at the composition root.
	// Wired through WithRemoteLauncher; when nil, remote sessions open a
	// plain shell and report reason none.
	remoteLauncher ssh.RemoteLauncher

	// remoteInstaller publishes the integration bundle over SFTP. It is
	// stamped on the DIRECT-HOST ConnectConfig only: a saved profile gets
	// its own from the connection resolver, which is where a profile's
	// every other seam comes from too.
	//
	// It became load-bearing with the carrier (design §4.1). Before it, the
	// remote command was the self-installing launcher and carried a publish
	// prelude, so a direct-host session installed the bundle from inside the
	// command it ran; the carrier carries no payload, so the SFTP publish is
	// now the ONLY thing that installs it. Without this line a direct-host
	// session finds no launch carrier on the far host, names
	// generation-unavailable and stays conventional forever — a regression
	// with no diagnosis, since every part of it works.
	remoteInstaller ssh.RemoteInstaller

	// remoteLifecycle establishes the authenticated lifecycle channel for
	// remote sessions (ADR-0024 decision 2 "Over SSH"), stamped onto every
	// ConnectConfig alongside the launcher. Wired through
	// WithRemoteLifecycle; when nil, remote sessions open without a
	// channel and stay conventional.
	remoteLifecycle ssh.RemoteLifecycle
	// installedFacts is the backend-owned, persisted memory of which
	// resolved destinations carry a committed, protocol-compatible
	// integration (§5.4). Wired through WithInstalledFactStore; when nil,
	// the footprint surface answers an empty list (the P7 observation RPC
	// that used to write it was severed — ADR-0024 §1).
	installedFacts *ssh.InstalledFactStore
	// installedFactSeen bounds the write to once per domain: a lane
	// publishes a fact at every prompt, and the installation it reports does
	// not change while the shell that reported it is alive.
	installedFactMu   sync.Mutex
	installedFactSeen map[string]struct{}

	// remoteUninstaller removes the integration bundle on a remote host,
	// owning the dial-and-call (P10). Wired through WithRemoteUninstaller;
	// when nil, shell.footprint.uninstall answers an error and removes
	// nothing — the status surface never offers the button without it.
	remoteUninstaller RemoteUninstaller

	// localCompleter answers shell.complete for KindLocal sessions.
	// When nil, the method returns a JSON-RPC error for local sessions.
	localCompleter completion.Completer
	// sshCompleter answers shell.complete for KindRemote sessions with the
	// exact SSH options captured from the live terminal session. When nil,
	// the method returns a stated empty reason for SSH sessions.
	sshCompleter RemoteCompleter
	// commandNames answers shell.commandNames — the shared PATH name set,
	// cached per target by the backend (carrier design §8).
	commandNames CommandNamesResolver

	// Pending-capture registry: the backend-side holder of submitted
	// credentials awaiting a save decision (internal/credential). Created
	// at construction; nil only when its fingerprint key could not be
	// minted, in which case no offers are made and saves are refused.
	captures *credential.CaptureRegistry
	// nextConnID assigns the per-connection (per-tab) identity captures
	// are scoped to.
	nextConnID atomic.Uint64
	// prober validates credentials without opening a session (connections.test).
	// When nil, the handler returns a JSON-RPC error.
	prober Prober
	// hostKeyTruster appends offered host keys to known_hosts
	// (connections.trustHostKey — accept-on-first-use). When nil, the
	// handler returns a JSON-RPC error.
	hostKeyTruster HostKeyTruster
	// probeResultStore records probe outcomes as operational evidence.
	// When nil, probe results are not stored (the probe still runs and
	// returns its outcome to the caller).
	probeResultStore *ProbeResultStore
	// assistantClient is the eino-backed engine (nocx-edio) behind
	// endpoints.probe and agent.status's last-probe fact. When nil, the
	// endpoints.probe method answers -32601 "agent not available".
	assistantClient assistant.Client
	// assistantProbes records the last endpoints.probe outcome — the
	// process-lifetime "last probe result" agent.status reports. When nil,
	// probes still run and return their outcome, but agent.status reports
	// lastProbe null.
	assistantProbes *assistant.ProbeStore
	// agentKnownMaterial is the egress gate's vault comparison
	// (assistant.KnownMaterial, design §7.1): the seam that answers "does
	// this tool result contain a value the vault holds" — in the backend,
	// nothing leaving (ADR-0011 §2). When nil, a grant-carrying ask that
	// may execute tools fails at the middleware's construction, closed:
	// the gate must see short vault values or a result would leave
	// unscreened (assistant.newPolicyMiddleware).
	agentKnownMaterial assistant.KnownMaterial
	// agentApprovals is the process-lifetime approval store (design §7.2,
	// nocx-z9hj4): the transport owns ONE per server and passes it on
	// every Ask, so the run that escalated and the run that resumes consult
	// the SAME decisions. Process-lifetime like the checkpoint: it does
	// not survive a restart, which is what the recovery rule says.
	agentApprovals *assistant.ApprovalStore
	// pendingRuns holds a suspended run's stream context (question,
	// references, resolved endpoint material — everything the resume
	// re-drives) keyed by run id. The approval store is process-lifetime;
	// so is this: the agent is alive while the renderer is alive (design
	// §2.4). A run whose renderer never answers holds its context until
	// the run terminalizes or the process restarts.
	pendingRuns   map[int64]askRunContext
	pendingRunsMu sync.Mutex
	// agentProbeSub admits and runs endpoints.probe probes off the read
	// loop: a streaming probe can take tens of seconds and must never
	// freeze the socket that feeds every other tab. Capacity one composed
	// with the lane, exactly like probeSub: a second test is refused with
	// the control-saturated error.
	agentProbeSub control.Submission
	// askSub admits and runs the ask STREAM tasks (nocx-x8s2.2) off the
	// read loop: a model stream can take minutes, so it must not freeze
	// the socket. Bounded at askStreamCapacity — several asks overlap (the
	// acceptance criterion drives two at once) but a runaway renderer
	// cannot spawn unbounded model calls. The task context derives from
	// the connection, so a disconnect cancels the stream and the run
	// terminalizes.
	askSub control.Submission
	// lane is the ordinary control lane: the shared bounded worker pool every
	// admission-backed control method runs on (registration.go). Capacity
	// laneCapacity; a full lane refuses new work with the control-saturated
	// error instead of queueing it. Probe and dialog compose their own
	// capacity-one resource admissions with the lane (see below).
	lane control.Submission
	// laneCapacity is the ordinary lane's permit count, configurable for
	// tests that must saturate it deterministically.
	laneCapacity int
	// domainWaitTimeout bounds how long a request waits on a domain conflict
	// gate before the wait itself is refused (the wait bound of the
	// waiting-gate design). Long enough that a sequential client's
	// back-to-back requests — whose previous response left the gate held
	// for a moment — always clear it; short enough that a gate held by a
	// long operation (a dial, an import) cannot delay the answer forever.
	domainWaitTimeout time.Duration
	// domainMaxQueue bounds how many requests may wait on one domain gate
	// before further conflicting requests are refused instantly (the
	// queue-depth bound at the gate). Cross-method waiters on a shared
	// gate accumulate here.
	domainMaxQueue int
	// domainQueueDepth bounds in-flight tasks per operation (waiting on the
	// gates or running): a flood of conflicting requests is refused at
	// submit time rather than spawning unbounded tasks.
	domainQueueDepth int
	// probeSub admits and runs connections.test probes off the read loop:
	// a 30-second SSH probe must never freeze the socket that feeds every
	// other tab. Capacity one (a second probe is refused with the
	// control-saturated error) composed with the lane, so a probe also
	// occupies an ordinary worker permit; the permit frees when the task
	// returns, cancelled or not — that is what releases the admission slot
	// for the next connection.
	probeSub control.Submission
	// dialogSub runs the dialog methods off the read loop under a bounded
	// queue. It does NOT carry the native-picker capability: that is
	// dialogAdmit, acquired on the task goroutine.
	dialogSub control.Submission
	// dialogAdmit is the native picker itself — a capacity-one WAITING gate
	// composed with the lane, acquired inside the task (dialogHandlers), not
	// at submit time. It is a serialisation point, not an execution bound:
	// one picker at a time, and a request that arrives while the capability
	// is held waits for it. Only exhausting a bound refuses, which is what
	// still stops a second picker stacking over one a human left open.
	dialogAdmit control.Admission
	// inflight tracks admitted off-loop tasks so Stop cancels them and
	// waits, bounded, for them to drain (see Stop's documented maximum).
	inflight inflight
	// controlDrainTimeout is how long Stop waits for in-flight off-loop
	// control work to finish after cancelling it. Work that ignores
	// cancellation is abandoned at this bound.
	controlDrainTimeout time.Duration
	// methods is the validated control-method registration set: method →
	// submission + per-connection handler builder. Built once at
	// construction (registration.go); the per-connection materialisation
	// happens in handleSession.
	methods map[string]methodSpec
	// satNotify rate-limits the control.saturated notification a refused
	// notification (no id) triggers — one per class+scope per interval,
	// never one per refused frame.
	satNotify *saturatedNotifyLimiter

	// profileUsage tracks last-used timestamps for the sessions.status RPC.
	// When nil, the handler reports live-state from the registry but
	// last-used timestamps are unavailable (nocx-uxs5.4).
	profileUsage session.ProfileUsageTracker

	// contentDB is the durable content store backing history.query. When
	// nil, the method answers source=unavailable — the overlay then says
	// durable history is not running instead of presenting the in-memory
	// ledger as all history (contracts/history.query.schema.json).
	contentDB content.ContentDB

	// historyStatus is the raise/clear state of durable command history:
	// whether it is running and, when it is not, why. It answers
	// history.status and pushes history.statusChanged. When nil the server
	// makes no claim and reports history as running — the composition root
	// is what says otherwise (ws_history_status.go).
	historyStatus *HistoryStatus

	// filesys is the binding registry backing the files.* control plane
	// (fm-w8). When nil, those methods return -32601. The provider
	// factories and the revealer ride separate options; the transport
	// never constructs a provider itself (AD-8).
	filesys           *filesystem.Registry
	filesProviderFor  FilesystemProviderFactory
	revealer          FilesRevealer
	filesPollInterval time.Duration // transport-side digest-poll cadence (files.watch)

	// filesMu guards filesBindings and filesBySession: the transport's own
	// bookkeeping for bindings it issued (filesystem exposes neither a
	// binding's session nor its endpoint attestation, and the notification
	// addressing and files.reveal's local-only guard both need them).
	filesMu        sync.Mutex
	filesBindings  map[string]*filesBinding           // bindingID → bookkeeping
	filesBySession map[session.ID]map[string]struct{} // sessionID → bindingIDs

	// git is the binding registry backing the git.* control plane (spec
	// §5.1). When nil, those methods return -32601. The repo factory rides
	// a separate option; the transport never constructs a repository
	// itself (AD-8).
	git        *git.Registry
	gitFactory git.RepoFactory

	// gitMu guards gitBindings and gitBySession: the transport's own
	// bookkeeping for bindings it issued (internal/git exposes neither a
	// binding's session nor anything else the notification addressing
	// needs).
	gitMu        sync.Mutex
	gitBindings  map[string]*gitBinding             // bindingID → bookkeeping
	gitBySession map[session.ID]map[string]struct{} // sessionID → bindingIDs

	// ringsMu protects rx and stopped. One sessionRx per session;
	// keyed by session.ID. When stopped is true, getOrCreateRx returns nil
	// so no new rings are created after the server begins shutting down.
	ringsMu sync.Mutex
	rx      map[session.ID]*sessionRx
	stopped bool
	// lanesMu guards lanes: the per-session resize lanes (ws_session_ops.go).
	// One lane per session that has been resized or closed; entries are
	// never deleted (a closed lane is the tombstone that refuses every
	// later resize from any connection).
	lanesMu sync.Mutex
	lanes   map[session.ID]*sessionLane

	// tunnelConnector acquires an owned pooled-connection lease for a
	// forward (spec §7.3). *ssh.RealClient satisfies tunnel.Connector
	// without an adapter. When nil, the tunnel.* methods return a JSON-RPC
	// error; the transport never constructs an SSH client itself.
	tunnelConnector tunnel.Connector

	// discoverySched owns the port-discovery cadence (spec §4, nocx-wzc4.2):
	// settle sample, prompt debounce, hidden-tab pause, one-in-flight. When
	// nil, the ports.* methods return a JSON-RPC error and the cadence hooks
	// are no-ops.
	discoverySched *discovery.Scheduler
	// inBand builds the in-band bootstrap plan for shell.integrate
	// (nocx-ynsx). When nil, the method returns a JSON-RPC error; the
	// transport never constructs the capability itself.
	inBand InBandBootstrapper

	// tunnelMu guards tunnels and ownerTunnels. tunnels is the backend
	// id → tunnel map backing tunnel.stop; ownerTunnels scopes teardown to
	// the tab that opened each forward (spec §7.3) — closing one tab never
	// stops another tab's forward.
	tunnelMu     sync.Mutex
	tunnels      map[string]*tunnel.Tunnel
	ownerTunnels map[*wsConn]map[string]struct{}
	// connsMu protects conns. One entry per active WebSocket connection.
	connsMu sync.Mutex
	conns   map[*wsConn]struct{}

	// outboundBudget is the process-wide cap on queued outbound bytes,
	// shared by every connection's outbound queue (the per-connection
	// queue depth is the primary bound; this is the additional one).
	outboundBudget *outbound.Budget

	// asks is the shared backend→renderer ask machinery (unlock requests,
	// connection-password prompts): a pending registry keyed by
	// server-assigned opaque request id, one correlation mechanism for
	// every ask (unlock_requester.go, password_requester.go).
	asks askBroker

	// planMu guards planStore. Plans are decrypted import plans keyed by
	// opaque token, stored server-side so secrets never reach the renderer.
	planMu    sync.Mutex
	planStore map[string]*planEntry

	// lifecyclePub is the lifecycle publication boundary (ADR-0024 decision
	// 7, bead nocx-u7uh.5). When nil, no lifecycle adapters can be created
	// and no lifecycle.changed facts are routed — sessions stay
	// conventional. Wired by WithLifecyclePublisher at the composition
	// root; the shell-spawn path reads it to create adapters, and this
	// server is bound as the publisher's emitter after construction.
	lifecyclePub   *lifecyclepub.Publisher
	lifecycleMu    sync.Mutex
	lifecycleLanes map[lifecycle.LaneID]session.ID
	// integrations is the per-session integration axis published as
	// session.integrationChanged (nocx-dvql, ws_integration.go). Separate
	// from lifecycleLanes because it answers a different question: that map
	// says which session a DOMAIN belongs to, this one says what the
	// SESSION's launch started and how far it got.
	integrationMu sync.Mutex
	integrations  map[session.ID]*integrationStatus
	// bootstrapStages is how far each session's shell got through nocx's
	// rcfile (nocx-yww2). Its own map rather than a field of
	// integrationStatus, because the two arrive in an order nothing
	// controls: the shell can write its first fact microseconds after the
	// fork, while the launch registers the axis only once the pty is back.
	// A stage folded into the status would be dropped for arriving early,
	// and the failure it explains is exactly the one where the shell was
	// fast and then vanished.
	bootstrapStages map[session.ID]string
	// recoveryMu guards recoveries: the per-session restoration episodes
	// (ADR-0024 decision 8). The episode opens when a lost fact with a
	// recovery fence routes to a live session, and is cancelled when the
	// session closes — a late ack is rejected (session death wins).
	recoveryMu sync.Mutex
	recoveries map[session.ID]*recoveryState
	// transfers is the running-transfer registry and the one-shot tickets
	// that name their bodies (ws_upload.go), in BOTH directions: an upload
	// waiting for a body and a download waiting for a reader are the same
	// record with the same lifetime, so they share one map, one TTL, one
	// bound and — the part that matters — one cancellation fan-out.
	// Registering downloads in a second registry would be a second place
	// for files.close and session teardown to remember to look.
	//
	// Its zero value is usable, so it is a value rather than a pointer and
	// needs no line in the constructor. A transfer is registered per
	// SESSION (design D8): closing the binding, closing the session or
	// stopping the server cancels the set and waits for it to unwind,
	// bounded — never for the transfer.
	transfers transferRegistry

	// sources is the SOURCE-ticket mint (ws_upload_source.go) — the other
	// half of R2. The server owns it rather than being handed one, because
	// it is both ends of the same mechanism: the two mint sites reach it
	// through this server (the picker seam and the window drop), and
	// files.upload redeems from it. Two stores would be a ticket minted in
	// one and unclaimable in the other, which is the defect this field
	// exists to make unspellable.
	sources *SourceTicketStore
}

// ── Tabby import plan store (server-side, never reaches renderer) ─────────

// planEntry holds a decrypted import plan for one-time execution.
// inProgress prevents concurrent execute calls for the same token.
type planEntry struct {
	plan       *importPlan
	createdAt  time.Time
	inProgress bool
}

// importPlan is the complete decrypted plan for a Tabby import.
// Stored server-side by opaque token; secrets never leave the backend.
type importPlan struct {
	profiles []profile.SSHProfile
	groups   []profile.ProfileGroup
	creds    []credentialPlan
}

// credentialPlan pairs a decrypted tabby secret with the connection target it
// was keyed to. For a password, target* records the connection the tabby vault
// keyed the secret to — the profile whose options match carries the binding
// (ADR-0017). Passphrases have no target: a passphrase belongs to a private
// key, not to a connection, and stays an unbound vault row the editor can
// bind.
type credentialPlan struct {
	name         string
	secret       string
	isPassphrase bool
	targetUser   string
	targetHost   string
	targetPort   int
}

// planTTL is how long a plan remains valid after creation.
const planTTL = 10 * time.Minute

// maxPlans bounds the in-memory plan map to prevent unbounded accumulation.
const maxPlans = 100

// storePlan stores a plan and returns an opaque token.
func (s *WSServer) storePlan(plan *importPlan) (string, error) {
	s.planMu.Lock()
	defer s.planMu.Unlock()

	// Lazy init.
	if s.planStore == nil {
		s.planStore = make(map[string]*planEntry)
	}

	// Evict expired entries before adding.
	now := time.Now()
	for k, e := range s.planStore {
		if now.Sub(e.createdAt) > planTTL {
			delete(s.planStore, k)
		}
	}

	if len(s.planStore) >= maxPlans {
		return "", errors.New("plan store full")
	}

	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf[:])
	s.planStore[token] = &planEntry{plan: plan, createdAt: now}
	return token, nil
}

// claimPlan marks a plan as in-progress and returns it. Returns nil if not
// found, expired, or already claimed by a concurrent caller.
func (s *WSServer) claimPlan(token string) *importPlan {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	e, ok := s.planStore[token]
	if !ok || e.inProgress {
		return nil
	}
	if time.Since(e.createdAt) > planTTL {
		delete(s.planStore, token)
		return nil
	}
	e.inProgress = true
	return e.plan
}

// releasePlan clears the in-progress flag so the plan can be retried (e.g.
// after vault setup/unlock). No-op if the token does not exist.
func (s *WSServer) releasePlan(token string) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if e, ok := s.planStore[token]; ok {
		e.inProgress = false
	}
}

// finishPlan removes a completed plan from the store. No-op if not found.
func (s *WSServer) finishPlan(token string) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	delete(s.planStore, token)
}

// ProfileResolver maps a profile ID to an SSH host and connect config.
// Passwords are never carried in the returned config — they are late-bound
// via the credential store wired into ConnectConfig.
type ProfileResolver interface {
	Resolve(profileID string) (host string, cfg *ssh.ConnectConfig, err error)
}

// resolverHolder is the mutable profile-resolver seam. The resolver is set
// post-construction (SetProfileResolver) because it depends on the transport
// (the connection-password ask) and must be created after the transport
// exists. The operations and seam handlers that use it therefore hold the
// holder and read the current value per call, never a captured nil. It
// satisfies both transport.ProfileResolver and capability.ProfileResolver
// (identical signatures), so it can be handed to an OpenOperation at
// construction and still observe a later SetProfileResolver.
type resolverHolder struct {
	mu sync.RWMutex
	r  ProfileResolver
}

func (h *resolverHolder) set(r ProfileResolver) {
	h.mu.Lock()
	h.r = r
	h.mu.Unlock()
}

// get returns the current resolver and whether one is wired.
func (h *resolverHolder) get() (ProfileResolver, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.r, h.r != nil
}

// Resolve implements ProfileResolver (and capability.ProfileResolver),
// forwarding to the current value. An unwired holder answers an error, which
// the callers surface as their not-available refusal.
func (h *resolverHolder) Resolve(profileID string) (string, *ssh.ConnectConfig, error) {
	r, ok := h.get()
	if !ok {
		return "", nil, errors.New("no profile resolver wired")
	}
	return r.Resolve(profileID)
}

// WithProfileResolver attaches a profile resolver for SSH connection setup.
func WithProfileResolver(r ProfileResolver) WSServerOption {
	return func(s *WSServer) { s.resolver.set(r) }
}

// SetProfileResolver sets the profile resolver post-construction. Used when
// the resolver depends on the transport (the connection-password ask) and
// must be created after the transport exists.
func (s *WSServer) SetProfileResolver(r ProfileResolver) {
	s.resolver.set(r)
}

// WithSSHConfigResolver attaches the SSH config resolver and config path
// for the ssh.listAliases RPC. The resolver answers values via ssh -G;
// the config path is used to enumerate Host patterns (see aliases.go for
// the split rationale). When not wired, ssh.listAliases returns a JSON-RPC
// error.
func WithSSHConfigResolver(resolver ssh.ConfigResolver, configPath string) WSServerOption {
	return func(s *WSServer) { s.sshConfigResolver = resolver; s.sshConfigPath = configPath }
}

// WithRemoteLauncher attaches the remote shell launcher that builds the start
// command for integrated remote shells (nocx-xs1d). The composition root
// adapts shellintegration.NewRemoteLauncher through its own ssh.RemoteLauncher
// adapter; this option is how the transport passes it into every ConnectConfig
// it builds. When not wired, remote sessions fall back to a plain shell and
// report reason none — the launcher is an opt-in per connection, never a
// transport default.
func WithRemoteLauncher(l ssh.RemoteLauncher) WSServerOption {
	return func(s *WSServer) { s.remoteLauncher = l }
}

// WithRemoteInstaller attaches the SFTP publisher stamped onto direct-host
// ConnectConfigs. See remoteInstaller for why the carrier made it the only
// thing that installs the bundle on that path.
func WithRemoteInstaller(i ssh.RemoteInstaller) WSServerOption {
	return func(s *WSServer) { s.remoteInstaller = i }
}

// WithRemoteLifecycle attaches the lifecycle-channel establisher that
// remote sessions consult (ADR-0024 decision 2 "Over SSH"), stamped onto
// every ConnectConfig alongside the launcher. The composition root
// implements it with the lifecycle kernel and the ssh client. When not
// wired, remote sessions open without a channel and stay conventional —
// the same opt-in-per-connection shape as the launcher.
func WithRemoteLifecycle(l ssh.RemoteLifecycle) WSServerOption {
	return func(s *WSServer) { s.remoteLifecycle = l }
}

// RemoteCompleter runs one completion against the immutable SSH route copied
// from the live session. The options are part of the contract: omitting them
// silently replaces a jump-routed pooled connection with a direct dial.
type RemoteCompleter interface {
	Complete(context.Context, completion.Request, ...ssh.ConnectOption) (*completion.Response, error)
}

// WithCompleters attaches the completion sources for shell.complete
// (nocx-w7h.15). local answers KindLocal sessions; remote answers
// KindRemote sessions through a DiscoveryConn acquired with that session's
// exact SSH options. Either may be nil — the handler then returns a stated
// empty reason for that session kind rather than a JSON-RPC error.
func WithCompleters(local completion.Completer, remote RemoteCompleter) WSServerOption {
	return func(s *WSServer) {
		s.localCompleter = local
		s.sshCompleter = remote
	}
}

// WithProbeResultStore attaches a probe result store for recording outcomes
// of connections.test probes. When nil, probe outcomes are still returned to
// the caller but not persisted in memory.
func WithProbeResultStore(s *ProbeResultStore) WSServerOption {
	return func(ws *WSServer) { ws.probeResultStore = s }
}

// WithAssistantClient attaches the assistant engine (nocx-edio): the one
// eino-backed client behind the endpoints.probe probe and the future ask
// transaction. When nil, endpoints.probe answers -32601 "agent not
// available".
func WithAssistantClient(ac assistant.Client) WSServerOption {
	return func(ws *WSServer) { ws.assistantClient = ac }
}

// WithRunLease names the lease bounds every run execution is supervised
// under (ADR-0020 decision 2): the wall-clock deadline, the inactivity
// deadline, the output budget and the escalation grace. Zero fields mean
// the corresponding default (defaultRunLease); a config with every bound
// zero disables the lease and restores the pre-lease broker timeout.
func WithRunLease(cfg RunLeaseConfig) WSServerOption {
	return func(s *WSServer) { s.runLeaseCfg = cfg }
}

// WithAgentKnownMaterial attaches the egress gate's vault comparison — the
// composition root wires the vault adapter here (NewVaultKnownMaterial).
// When nil, a run that may execute tools fails closed at the middleware's
// construction; the rule is not weakened by leaving it unwired.
func WithAgentKnownMaterial(km assistant.KnownMaterial) WSServerOption {
	return func(ws *WSServer) { ws.agentKnownMaterial = km }
}

// WithAgentPolicy attaches the ONE global agent policy the ask run grants
// are minted from (ADR-0020 §7 as amended — amendment proposed, awaiting
// owner approval). The run mint resolves it through content.ResolvePolicy —
// the one place the global-default/workspace-override order is stated. When
// nil/unset, ask runs carry no grant and the model is offered no tools.
func WithAgentPolicy(p assistant.GlobalPolicy) WSServerOption {
	return func(ws *WSServer) { ws.agentPolicy = p }
}

// WithLiveEffects names which effect classes a declared tool actually
// carries — policy.get's "live". The value is agenttools.LiveEffects(), read
// at the composition root beside WithAgentPolicy: the policy says what a run
// MAY do, this says which of those rows anything can do at all, and the
// settings surface needs both to avoid drawing five controls that govern
// nothing as equals to the two that do.
//
// Passed in rather than reached for. It is static data off a compile-time
// table, so the root is where it becomes a dependency — the same reason
// WithBuildInfo reads internal/version there instead of in a handler.
func WithLiveEffects(live []content.Effect) WSServerOption {
	return func(ws *WSServer) { ws.liveEffects = live }
}

// WithAssistantProbeStore attaches the process-lifetime store of the last
// endpoints.probe outcome — agent.status's "last probe result" fact. When
// nil, probes still run and return their outcome, but agent.status reports
// lastProbe null.
func WithAssistantProbeStore(store *assistant.ProbeStore) WSServerOption {
	return func(ws *WSServer) { ws.assistantProbes = store }
}

// WSServerOption configures a WSServer.
type WSServerOption func(*WSServer)

// WithProfileRepository attaches a profile repository to the server, enabling
// the profiles.* JSON-RPC methods.
func WithProfileRepository(pr profile.ProfileRepository) WSServerOption {
	return func(s *WSServer) { s.profiles = pr }
}

// WithGroupRepository attaches a group repository to the server, enabling the
// groups.* JSON-RPC methods.
func WithGroupRepository(gr profile.GroupRepository) WSServerOption {
	return func(s *WSServer) { s.groups = gr }
}

// WithCredentialStore attaches a credential store, enabling the
// secrets.* and vault.* secret operations.
func WithCredentialStore(cs credential.SecretStore) WSServerOption {
	return func(s *WSServer) { s.credentials = cs }
}

// credentialResolver is the STANCED read seam over the credential store
// (nocx-k41yv): the form every handler that resolves material on a person's
// behalf is wired with. Handlers hold this rather than the store, because
// the store's Get takes no stance and a seam that can be bypassed is the
// one that was — three times.
//
// The sealed predicate is injected here, at the composition root, because
// internal/credential must not import the vault (the vault imports it) and
// because which implementation's sealed error is in play is precisely a
// composition decision.
func (s *WSServer) credentialResolver() credential.Resolver {
	if s.credentials == nil {
		return nil
	}
	return credential.NewResolver(s.credentials, func(err error) bool {
		return errors.Is(err, vault.ErrVaultSealed)
	})
}

// WithCredentialStore attaches a credential store, enabling the

// WithSettingsRegistry attaches a settings registry to the server, enabling
// the settings.* JSON-RPC methods. Also wires the registry's change notifier
// so every connected client receives settings.changed broadcasts.
func WithSettingsRegistry(r *settings.Registry) WSServerOption {
	return func(s *WSServer) {
		s.settings = r

		r.SetNotifier(func(revision int, keys []string) {
			s.broadcastSettingsChanged(revision, keys)
		})
	}
}

// WithBackupService attaches the structured Backup & Restore service.
func WithBackupService(svc *backup.Service) WSServerOption {
	return func(s *WSServer) { s.backupService = svc }
}

// WithSandboxService attaches the filesystem sandbox backend, enabling
// sandbox.status and sandboxed open requests. Without it, sandbox.status
// reports -32601 and a sandboxed open fails closed as -32007 setup-failed
// when the PTY factory cannot prepare native enforcement.
func WithSandboxService(svc sandbox.Service) WSServerOption {
	return func(s *WSServer) { s.sandboxSvc = svc }
}

// WithSandboxAccessInbox enables sandbox.access.* and broadcasts revision-only
// invalidations. The callback is process-lifetime, like settings.changed.
func WithSandboxAccessInbox(inbox *sandbox.AccessInbox) WSServerOption {
	return func(s *WSServer) {
		s.sandboxAccess = inbox
		if inbox != nil {
			inbox.Subscribe(func(revision uint64) {
				s.broadcastSandboxAccessChanged(revision)
			})
		}
	}
}

// WithBackupFileSaver injects the native save-file capability. Tests can
// provide a deterministic writer; production passes backup.SaveToFile.
func WithBackupFileSaver(saver func(string, string) (*backup.SaveResult, error)) WSServerOption {
	return func(s *WSServer) { s.backupFileSaver = saver }
}

// WithContentDB attaches the durable content store backing history.query.
// When absent, the method answers source=session so the overlay labels what
// it shows "this session only" instead of presenting the in-memory ledger as
// all history (contracts/history.query.schema.json).
func WithContentDB(db content.ContentDB) WSServerOption {
	return func(s *WSServer) { s.contentDB = db }
}

// WithProfileService attaches a profile domain service for import
// operations, providing a single validated write path and atomic imports.
func WithProfileService(svc *profile.ProfileService) WSServerOption {
	return func(s *WSServer) { s.profileSvc = svc }
}

// WithSnippets attaches the snippet library service to the server, enabling
// the snippets.* JSON-RPC methods. When nil, those methods return -32601.
func WithSnippets(svc *snippet.Service) WSServerOption {
	return func(s *WSServer) { s.snippets = svc }
}

// WithNotes attaches the notes library service, enabling the notes.*
// JSON-RPC methods. When nil, those methods return -32601 and the panel
// says the library is unavailable — never an empty list.
func WithNotes(svc *note.Service) WSServerOption {
	return func(s *WSServer) { s.notes = svc }
}

// WithUIState attaches the UI-state store, enabling the uistate.* JSON-RPC
// methods. When nil, those methods return -32601 and the shell falls back to
// its declared defaults — a sidebar at 240px that does not survive a restart,
// which is what the product looked like before ADR-0033.
func WithUIState(store *uistate.Store) WSServerOption {
	return func(s *WSServer) { s.uiState = store }
}

// WithAPI attaches the API-testing collection service and the sender,
// enabling api.collections.*, api.request.read/write and api.request.send.
//
// The whole folder surface, not the reading half: api.collections.create
// mints a folder and api.request.send resolves the environment it is sent
// under, so both the creator and the environment reader are reached from
// this one domain — through one handle table and one root re-validation.
// With no collection service those methods return -32601; with a collection
// service but no sender, everything but api.request.send answers — a
// collection you can read and edit but not fire, which is an honest half of
// the feature rather than a send that quietly does nothing.
func WithAPI(collections apicoll.Collections, sender apisend.Sender) WSServerOption {
	return func(s *WSServer) {
		s.apiCollections = collections
		s.apiSender = sender
	}
}

// WithAPIBindings attaches the binding document — the only thing in the API
// surface that holds an identifier for stored credential material (design
// §8.1) — enabling api.import.postman, which writes the secret values a
// Postman export carries. When nil that method returns -32601: an import
// that had nowhere to put a token would have to either drop it silently or
// write it into the collection folder, and the folder being safe to commit
// BY CONSTRUCTION is the whole security argument.
func WithAPIBindings(store apibind.Store) WSServerOption {
	return func(s *WSServer) { s.apiBindings = store }
}

// WithAPIVariables attaches the binding document's READ half — the one that
// answers "what is this variable worth" and never yields an identifier.
// api.request.send needs it because a collection file names a VARIABLE for
// its auth (design §8) and the send is the moment that name has to become a
// header.
//
// It is a second option rather than a second parameter of WithAPIBindings
// because the two halves genuinely wire apart: a build that can import but
// not resolve, or resolve but not import, is a coherent half of the feature
// and says so through its own -32601 or its own unresolved-variable
// refusal. When nil, an auth variable resolves to nothing and the send is
// BLOCKED, naming the variable — never sent with an empty credential, which
// is the plausible-looking request §6.5 spends a paragraph refusing.
func WithAPIVariables(values apibind.ValueResolver) WSServerOption {
	return func(s *WSServer) { s.apiVariables = values }
}

// WithAPIImportFetcher attaches the seam that acquires an import document by
// URL (internal/apifetch), enabling api.import.postman's third source.
//
// It is a third option rather than a parameter of WithAPI because it wires
// apart from the sender in exactly the way the other three do: a build with
// a sender and no fetcher can send requests and cannot fetch an export, and
// says so. Without it, `url` is refused by name (ErrImportURLUnavailable)
// while `path` and `document` go on working — absence is the capability, and
// the renderer draws the entrance from what the backend answers.
func WithAPIImportFetcher(f apifetch.Fetcher) WSServerOption {
	return func(s *WSServer) { s.apiFetch = f }
}

// WithBuildInfo attaches the running binary's description, which is what
// app.about answers with. The transport is told rather than reading
// internal/version itself: that package's vars are link-time state, and a
// second reader of them inside the transport would be a second place the
// answer lives — and would leave no way for a test to assert what this method
// sends for a build nobody can produce on demand.
//
// Not attaching it is not a failure mode with a hole in it: the descriptor's
// fields are then the zero value, and the DTO says "unknown" in each, which is
// what the About page is built to render.
func WithBuildInfo(b version.BuildInfo) WSServerOption {
	return func(s *WSServer) { s.build = b }
}

// WithCaptureRegistry injects the pending-capture registry. Test seam:
// production constructs its own. The injected registry must not be shared
// across servers.
func WithCaptureRegistry(r *credential.CaptureRegistry) WSServerOption {
	return func(s *WSServer) {
		s.captures = r
	}
}

// WithVaultLifecycle attaches the vault seal-lifecycle surface, enabling the
// vault.* JSON-RPC methods.
func WithVaultLifecycle(vl VaultLifecycle) WSServerOption {
	return func(s *WSServer) { s.vaultLifecycle = vl }
}

// WithFilesystemRegistry attaches the binding registry backing the files.*
// control plane (fm-w8). When absent, those methods return -32601. The
// composition root constructs the registry (internal/app/app.go); without
// this line the whole filesystem package is reachable from its own tests
// and nowhere else (AGENTS.md check 5).
func WithFilesystemRegistry(r *filesystem.Registry) WSServerOption {
	return func(s *WSServer) { s.filesys = r }
}

// WithFilesystemProviderFactory attaches the provider builder files.open
// uses. The composition root decides which sessions get which providers —
// local.New for local sessions today, the SFTP provider with the SFTP wave
// (design §6 step 4) — and the transport never constructs a provider
// itself (AD-8). When absent, files.open returns an error.
func WithFilesystemProviderFactory(f FilesystemProviderFactory) WSServerOption {
	return func(s *WSServer) { s.filesProviderFor = f }
}

// WithGitRegistry attaches the binding registry backing the git.* control
// plane (spec §5.1). When absent, those methods return -32601. The
// composition root constructs the registry (internal/app/app.go); without
// this line the whole git package is reachable from its own tests and
// nowhere else (AGENTS.md check 5).
func WithGitRegistry(r *git.Registry) WSServerOption {
	return func(s *WSServer) { s.git = r }
}

// WithGitRepoFactory attaches the repo factory git.open invokes. The
// composition root wires the local factory (internal/git/local) for local
// sessions; the transport never constructs one itself (AD-8, D16 — the
// factory IS the local/remote seam). When absent, git.open answers an
// error.
func WithGitRepoFactory(f git.RepoFactory) WSServerOption {
	return func(s *WSServer) { s.gitFactory = f }
}

// WithFilesRevealer attaches the OS file-manager reveal capability
// (files.reveal). When absent, files.reveal returns -32601: the dev-web
// harness has no Wails runtime, and a reveal that did nothing would be a
// silent lie (design §5.2).
func WithFilesRevealer(r FilesRevealer) WSServerOption {
	return func(s *WSServer) { s.revealer = r }
}

// DefaultControlLaneCapacity is the ordinary lane's permit count: the number
// of control tasks that may run concurrently before new work is refused with
// the saturation error. Larger than one so non-conflicting long operations
// genuinely overlap (a tabby import beside a git status); small enough that
// a burst of control requests cannot spawn unbounded goroutines. The
// composition root names it explicitly (app.go), the same way the D14 bounds
// are named there; the option is also how tests saturate the lane with a
// known number of blocked tasks.
const DefaultControlLaneCapacity = 8

// DefaultDomainConflictWaitTimeout is how long a conflicting control request
// waits on its domain gate before being refused. The window it exists to
// bridge is the task tail: a handler enqueues its response and the permit is
// released a moment later, so a sequential client's next request can arrive
// while the gate is still held. That window is microseconds; a second is
// generous headroom for a genuinely queued request (the brief's "queue of
// length two") while keeping the wait perceptually short when the gate is
// held by a long operation.
const DefaultDomainConflictWaitTimeout = time.Second

// DefaultDomainMaxQueue is the default number of requests that may wait on
// one domain gate; a conflicting request beyond it is refused instantly.
const DefaultDomainMaxQueue = 8

// DefaultDomainQueueDepth is the default number of in-flight tasks one
// operation may have (waiting on the gates or running); a request beyond it
// is refused at submit time.
const DefaultDomainQueueDepth = 8

// askStreamCapacity bounds concurrent model streams (agent.ask, nocx-
// x8s2.2). Several asks must overlap — the acceptance criterion drives two
// at once and they stream concurrently — but a runaway renderer cannot
// spawn unbounded model calls. A stream beyond the capacity refuses at
// submit time and the run terminalizes failed ("too many answers in
// flight") rather than queueing behind an unbounded backlog.
const askStreamCapacity = 4

// WithDomainConflictWaitTimeout sets how long a request waits on a domain
// conflict gate before the wait is refused. Tests use a short value to
// exhaust the bound deterministically and a long one to hold a conflict
// open; the composition root names the production value explicitly, the
// same way it names the lane capacity.
func WithDomainConflictWaitTimeout(d time.Duration) WSServerOption {
	return func(s *WSServer) { s.domainWaitTimeout = d }
}

// WithControlLaneCapacity sets the ordinary lane's permit count. The
// composition root names the production value explicitly; tests use it to
// saturate the lane with a known number of blocked tasks.
func WithControlLaneCapacity(n int) WSServerOption {
	return func(s *WSServer) { s.laneCapacity = n }
}

func NewWSServer(logger log.Logger, reg session.Registry, opts ...WSServerOption) *WSServer {
	s := &WSServer{
		log:      logger,
		registry: reg,
		upgrader: websocket.Upgrader{
			// CheckOrigin is always permissive; our own authorize
			// call handles origin/host policy before the upgrade.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		rx:                  make(map[session.ID]*sessionRx),
		conns:               make(map[*wsConn]struct{}),
		outboundBudget:      outbound.NewBudget(outboundBudgetBytes),
		tunnels:             make(map[string]*tunnel.Tunnel),
		resolver:            &resolverHolder{},
		ownerTunnels:        make(map[*wsConn]map[string]struct{}),
		origins:             LoopbackOriginPolicy{},
		agentApprovals:      assistant.NewApprovalStore(),
		sessionPolicy:       newSessionPolicyStore(),
		laneInteractivity:   newLaneState(),
		satNotify:           newSaturatedNotifyLimiter(time.Second),
		pendingRuns:         make(map[int64]askRunContext),
		filesBindings:       make(map[string]*filesBinding),
		lanes:               make(map[session.ID]*sessionLane),
		filesBySession:      make(map[session.ID]map[string]struct{}),
		filesPollInterval:   defaultFilesPollInterval,
		laneCapacity:        DefaultControlLaneCapacity,
		domainWaitTimeout:   DefaultDomainConflictWaitTimeout,
		domainMaxQueue:      DefaultDomainMaxQueue,
		domainQueueDepth:    DefaultDomainQueueDepth,
		controlDrainTimeout: defaultControlDrainTimeout,
		gitBindings:         make(map[string]*gitBinding),
		gitBySession:        make(map[session.ID]map[string]struct{}),
	}
	// The mint's emitter is this server: a drop is told to the renderer
	// over this socket. Constructed here so there is exactly one store per
	// server and no window in which a mint site could reach a different one.
	s.sources = NewSourceTicketStore(s)
	if caps, err := credential.NewCaptureRegistry(); err != nil {
		// No entropy for the fingerprint key: no offers are made and
		// capture saves are refused. A predictable fingerprint key would be
		// worse than none — the equality facts would be forgeable.
		logger.Error("capture registry unavailable; no offers will be made", "error", err)
	} else {
		s.captures = caps
	}
	for _, o := range opts {
		o(s)
	}
	s.buildControlPlane()
	return s
}

// buildControlPlane wires the scheduling contract after every option has
// been applied: the lane, the capacity-one resource admissions (probe,
// agent probe, dialog), the domain gates and the validated registration
// set. A registration that cannot be validated fails the server build
// rather than freezing a socket at runtime.
func (s *WSServer) buildControlPlane() {
	lane := control.NewSemaphore("control", s.laneCapacity)
	s.lane = control.NewBoundedSubmission(lane)
	// Probe and agent probe keep their own capacity-one resource admissions
	// composed with the lane (canonical order: resource before execution
	// permit): a second probe is refused even while the lane has free
	// permits, and every task still occupies one lane permit.
	s.probeSub = &inflightSubmission{inflight: &s.inflight, inner: control.NewBoundedSubmission(control.NewCompositeNonblocking(
		control.NewSemaphore("probe", 1), lane))}
	s.agentProbeSub = &inflightSubmission{inflight: &s.inflight, inner: control.NewBoundedSubmission(control.NewCompositeNonblocking(
		control.NewSemaphore("agent-probe", 1), lane))}
	s.askSub = &inflightSubmission{inflight: &s.inflight, inner: control.NewBoundedSubmission(control.NewCompositeNonblocking(
		control.NewSemaphore("agent-ask", askStreamCapacity), lane))}
	// The native picker is the one of these that is NOT an execution bound.
	// A probe competes for a scarce worker; a picker is a serialisation
	// point — one at a time, and the second may proceed once the first
	// closes. Held in the instant-refusal class it refused its own tail: a
	// handler enqueues its response inside the task and the permit is
	// returned only after the task goroutine returns, so a sequential
	// client's very next dialog request landed in that window and was told
	// "Control plane busy" for doing nothing wrong. That is precisely the
	// window ADR-0026 item 4 says the waiting gate exists to bridge, and it
	// is what made the R2 sweep report the picker opening zero times:
	// dialog.openFile sorts immediately before dialog.openFileForUpload.
	//
	// So the capability moves to the waiting class and, because a waiting
	// admission may never be wired into a Submission (ADR-0026 item 3 of
	// Enforcement — a compile error, not a convention), it is acquired on
	// the task goroutine by the handler. The submission that remains is a
	// bounded queue, exactly as a domain operation's is.
	s.dialogSub = &inflightSubmission{inflight: &s.inflight, inner: s.operationQueue("dialog")}
	s.dialogAdmit = control.NewComposite(
		control.NewWaitingSemaphore("dialog", 1, s.domainMaxQueue, s.domainWaitTimeout), lane)
	// Domain-gated methods do not register on the lane submission: their
	// operation acquires the conflict gates (waiting, bounded) and THEN the
	// lane inside Run, on the task goroutine, so waiting conflict work never
	// occupies a worker permit and the read loop never blocks on a conflict.
	// The per-operation queue submissions bound in-flight tasks per operation.
	gates := s.domainGates()
	immediate := control.ImmediateSubmission{}
	// The request broker is constructed here, once the server's connection
	// set exists: its delivery seams are this server's own snapshot and
	// per-connection enqueue, and its resolution methods register on the
	// ingress-critical set below (the resolution must never wait behind the
	// lane — a pending requestor blocks on it).
	s.broker = NewBroker(s.rendererConns, s.rendererDeliver)
	configOp, endpointWired := s.buildConfigOp(lane, gates.config, gates.vault)
	_ = endpointWired
	specs := make([]methodSpec, 0, 96)
	specs = append(specs, s.sessionSpecs(lane, gates.session, gates.config)...)
	specs = append(specs, s.signalSpecs(lane, gates.session)...)
	specs = append(specs, s.askResolverSpecs(immediate)...)
	specs = append(specs, s.laneInteractivitySpec(immediate))
	specs = append(specs, s.brokerSpecs(immediate)...)
	specs = append(specs, s.configSpecs(lane, gates.config, gates.vault, configOp, endpointWired)...)
	specs = append(specs, s.backupSpecs(lane, gates.config)...)
	specs = append(specs, s.vaultSpecs(lane, gates.config, gates.vault)...)
	specs = append(specs, s.notifySpecs()...)
	specs = append(specs, s.secretSpecs(lane, gates.config, gates.vault, gates.content)...)
	specs = append(specs, s.gitSpecs(lane, gates.session, gates.git)...)
	specs = append(specs, s.filesSpecs(lane, gates.session, gates.filesystem)...)
	specs = append(specs, s.apiSpecs(lane, gates.api, gates.vault)...)
	contentSub := s.operationQueue("content")
	specs = append(specs, s.contentSpecs(lane, gates.content, contentSub)...)
	// history.status rides the plain lane, not the content queue: it is a
	// mutex read of in-memory state and must stay answerable while the
	// content domain is exactly what is broken.
	specs = append(specs, s.historyStatusSpecs(s.lane)...)
	specs = append(specs, s.agentSpecs(contentSub, lane, gates.content, configOp, endpointWired, s.credentialResolver(), s.assistantClient, s.askSub)...)
	specs = append(specs, s.ledgerSpecs(contentSub, lane, gates.content)...)
	specs = append(specs, s.layoutSpecs(contentSub, lane, gates.content)...)
	specs = append(specs, s.shellSpecs(lane, gates.session)...)
	specs = append(specs, s.aboutSpecs()...)
	specs = append(specs, s.lifecycleSpecs()...)
	specs = append(specs, s.policySpecs()...)
	specs = append(specs, s.seamSpecs(lane, gates.session)...)
	methods, err := buildMethodSpecs(specs)
	if err != nil {
		panic("nocx: control-plane registration: " + err.Error())
	}
	s.methods = methods
}

// operationQueue builds the bounded queue-submission for one operation's
// methods: at most domainQueueDepth tasks of the operation may be in flight
// (waiting on the conflict gates or running) before new work is refused at
// submit time. The refusal is the queue-depth half of the waiting-gate
// bound; the gate itself carries the other half (its own waiters cap), and
// the lane — acquired by the operation AFTER the gates, inside Run — is the
// execution bound.
func (s *WSServer) operationQueue(name string) control.Submission {
	return control.NewBoundedSubmission(control.NewSemaphore(name+"-queue", s.domainQueueDepth))
}

// domainGates builds one gate per domain, capacity 1 — the conservative
// whole-domain exclusion (capability package doc). The gates WAIT (bounded)
// for capacity: a conflicting operation queues rather than being refused,
// so a sequential client's back-to-back requests are never told the control
// plane is busy. The composition root (this function) is the only place a
// gate is constructed; operations take them as separate parameters and
// compose in the canonical order.
type domainGates struct {
	config, vault, content, session, git, filesystem, api control.Admission
}

func (s *WSServer) domainGates() domainGates {
	return domainGates{
		config:     capability.Gate(capability.GateConfig, 1, s.domainMaxQueue, s.domainWaitTimeout),
		vault:      capability.Gate(capability.GateVault, 1, s.domainMaxQueue, s.domainWaitTimeout),
		content:    capability.Gate(capability.GateContent, 1, s.domainMaxQueue, s.domainWaitTimeout),
		session:    capability.Gate(capability.GateSession, 1, s.domainMaxQueue, s.domainWaitTimeout),
		git:        capability.Gate(capability.GateGit, 1, s.domainMaxQueue, s.domainWaitTimeout),
		filesystem: capability.Gate(capability.GateFilesystem, 1, s.domainMaxQueue, s.domainWaitTimeout),
		api:        capability.Gate(capability.GateAPI, 1, s.domainMaxQueue, s.domainWaitTimeout),
	}
}

func (s *WSServer) Start(ctx context.Context) error {
	// Fail closed: no entropy → no token → no connections.
	if err := s.mintToken(); err != nil {
		return err
	}

	// Configure the upgrader to accept only our token as the subprotocol,
	// so it echoes the selected protocol on upgrade (RFC 6455).
	s.upgrader.Subprotocols = []string{tokenProtocol(s.token)}

	mux := http.NewServeMux()
	mux.HandleFunc("/session", s.handleSession)
	// The upload route (ADR: an HTTP upload beside the WebSocket, upload
	// design D3). Bytes travel as a streamed POST rather than as a new
	// binary msg-type because the data plane carries PTY I/O: a
	// multi-gigabyte upload multiplexed onto it would compete with terminal
	// responsiveness and would need application-level credit and reconnect
	// semantics invented for the purpose, where an HTTP request is an
	// independently flow-controlled byte stream on its own connection.
	//
	// The method is in each pattern, so anything but POST and OPTIONS is
	// still answered 405 by the mux itself. OPTIONS is a route of its own
	// rather than a branch inside handleUpload because the two share
	// nothing past the origin check: the preflight never reads the ticket
	// (ws_upload.go, the CORS block), and a branch is how it would one day
	// come to.
	mux.HandleFunc("POST "+uploadRoutePrefix+"{ticket}", s.handleUpload)
	mux.HandleFunc("OPTIONS "+uploadRoutePrefix+"{ticket}", s.handleUploadPreflight)
	// The download route, and the argument for it being HTTP is the
	// upload's argument with every term stronger (ws_download.go): the
	// bytes travel the SAME direction as bulk PTY output, the outbound
	// queue is deliberately lossy and a file's bytes may not be dropped,
	// and a browser can stream a response to disk where a page holding
	// WebSocket messages would have to buffer the whole file in the
	// renderer's heap first.
	//
	// GET only, and no OPTIONS beside it: a GET with no request header
	// outside the CORS safelist is a simple request, so a browser never
	// preflights it, and a route answering a request nobody makes is a
	// route nobody exercises. The origin headers still go on the reply,
	// because a page reading this with fetch needs them.
	mux.HandleFunc("GET "+downloadRoutePrefix+"{ticket}", s.handleDownloadFetch)

	addr := s.listenAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("ws listen: %w", err)
	}
	s.listener = listener
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return fmt.Errorf("ws listen: not a TCP address")
	}
	s.port = tcpAddr.Port

	s.server = &http.Server{
		Handler: mux,
		// Deliberately zero: /session is a long-lived upgrade, and this
		// setting would bound its header block along with everything
		// else's. The upload route's own header deadline is applied one
		// layer down, by uploadGuardConn, which can tell the two routes
		// apart before the parse finishes because it reads the request
		// line itself (ws_upload.go, §5.4).
		ReadHeaderTimeout: 0,
		// The guard is the net.Conn the listener returned, so this is
		// where a handler gets hold of the one watching its own request —
		// which is how the upload route sees a body that overran its own
		// Content-Length (§5.4), something net/http hides by design.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if g, ok := c.(*uploadGuardConn); ok {
				return context.WithValue(ctx, uploadGuardKey{}, g)
			}
			return ctx
		},
		// StateIdle is the only moment net/http says "that request is
		// over and the next has not started", which is exactly the
		// interval the guard needs to re-open on a reused connection.
		ConnState: func(c net.Conn, state http.ConnState) {
			if g, ok := c.(*uploadGuardConn); ok && state == http.StateIdle {
				g.restart()
			}
		},
	}
	guarded := uploadGuardListener{Listener: listener, timeout: s.transfers.headerDeadline}
	go func() {
		if err := s.server.Serve(guarded); err != nil && err != http.ErrServerClosed {
			s.log.Error("ws server error", "error", err)
		}
	}()

	s.log.Info("ws server started", "port", s.port)
	return nil
}

func (s *WSServer) Stop(ctx context.Context) error {
	// Mark stopped first so getOrCreateRx refuses new rings while
	// hijacked WebSocket handlers are still running. Do NOT nil s.rx —
	// the map stays usable for lookups until the final goroutine exits.
	s.ringsMu.Lock()
	s.stopped = true
	s.ringsMu.Unlock()
	// Cancel every running upload first. A transfer holds (over SFTP) a
	// pooled connection reference for its lifetime and nothing else in this
	// teardown would release it, and the POST that carries its body waits
	// on the transfer — so an uncancelled one would also hold Shutdown open
	// below. The wait inside is bounded and never waits for the upload.
	s.cancelAllTransfers()
	// And let go of every unredeemed source ticket. Each holds an open
	// descriptor on a file a person chose (ws_upload_source.go), and a
	// stopped server is the last of the three events that end that
	// interval — the other two being the claim and the TTL.
	s.sources.Close()

	// Cancel and drain in-flight off-loop control work (probes, dialogs).
	// Cancellation makes cooperative tasks return promptly; waitDrained
	// bounds the wait at controlDrainTimeout (the documented maximum, see
	// ws_control.go) and ABANDONS whatever ignores cancellation — the
	// forced-abandonment policy for work outside a commit interval. Stop
	// therefore terminates within the documented maximum against any
	// dependency, cooperative or not.
	s.inflight.stop()
	if !s.inflight.waitDrained(s.controlDrainTimeout) {
		s.log.Warn("abandoning in-flight control work at shutdown", "maxWait", s.controlDrainTimeout)
	}

	// Shutdown stops accepting new connections but hijacked WebSocket
	// handlers can still run after it returns. That is why the stopped
	// flag, not map-nilling, is the safety mechanism.
	var shutdownErr error
	if s.server != nil {
		shutdownErr = s.server.Shutdown(ctx)
	}

	// Close all rings so blocked writers and waiters unblock.
	s.ringsMu.Lock()
	for _, rx := range s.rx {
		rx.ring.close()
	}
	s.ringsMu.Unlock()

	for _, sess := range s.registry.List() {
		s.closeLane(sess.ID())
		_ = s.registry.Close(sess.ID())
		// Files (fm-w8): Stop closes every session, which closes its
		// bindings (spec §5.1); the watcher goroutines stop with them.
		s.filesSessionClosed(sess.ID())
		// Git (spec §5.5): Stop closes every session, which closes its git
		// bindings too. No subscriber is attached at shutdown — nobody to
		// notify, and gitSessionClosed's nil capture is exactly that case.
		s.gitSessionClosed(sess.ID(), nil)
		// The session's "allow in this session" answers die with it, on
		// every path that ends one (ws_sessionpolicy.go). Nothing persists
		// them, so shutdown is the last of the three.
		s.sessionPolicy.Drop(sess.ID())
	}

	// Application shutdown destroys every pending capture (the contract's
	// list names it).
	if s.captures != nil {
		s.captures.DestroyAll()
	}

	return shutdownErr
}

func (s *WSServer) Port() int {
	return s.port
}

// --- ring helpers (connection-independent, keyed by session.ID) ----------

func (s *WSServer) getRx(id session.ID) *sessionRx {
	s.ringsMu.Lock()
	defer s.ringsMu.Unlock()
	return s.rx[id]
}

func (s *WSServer) getOrCreateRx(id session.ID) *sessionRx {
	s.ringsMu.Lock()
	defer s.ringsMu.Unlock()

	if s.stopped {
		return nil
	}

	if rx, ok := s.rx[id]; ok {
		return rx
	}
	rx := &sessionRx{ring: newOutputRing()}
	s.rx[id] = rx
	return rx
}

// removeRx drops a session's receiver and returns it, or nil when another
// goroutine removed it first.
//
// The return value is a CLAIM, not a convenience. A session has two teardown
// owners — monitorExit, when the shell exits or the channel drops, and
// closeSession, when the user closes the tab — and on an explicit close BOTH
// run, because closing the session through the registry is what wakes the
// monitor. Deleting a session's files/git bindings is also the only chance to
// ANNOUNCE them: the destination is resolved at emit time from the receiver
// being removed here, so an owner captures the subscriber and then deletes.
// Two owners doing that is one of them deleting the bindings the other was
// about to announce, and the terminal notification is lost with no trace
// (nocx-2h08: git.changed(reason=sessionClosed) never arrived, and whichever
// test was waiting for a notification reported the package's 30-second bound).
//
// So the receiver is the token. Whoever removes it captured the subscriber
// from it and owns the binding teardown; the other leaves the bindings alone.
// The interval, both ends named: the session is claimed from the moment this
// returns non-nil until that owner's gitSessionClosed has enqueued the last
// terminal notification.
func (s *WSServer) removeRx(id session.ID) *sessionRx {
	s.ringsMu.Lock()
	defer s.ringsMu.Unlock()
	rx, ok := s.rx[id]
	if !ok {
		return nil
	}
	delete(s.rx, id)
	return rx
}

// Responder is the outbound capability handed to control-plane handlers:
// every response is a non-blocking enqueue into the connection's outbound
// side. None of these methods may block — a handler that can block on the
// socket is the defect this whole package boundary exists to remove
// (nocx-o2le): the read loop must never wait behind a renderer.
//
// Responses and notifications are different classes of frame. TryResult and
// TryError (the other half of a promise) go through the connection's
// reserved response queue, which the refreshable data plane cannot consume;
// if even that capacity is exhausted the connection closes rather than drop
// the response, so a caller's promise always settles — result, error, or
// disconnect. TryNotify is refreshable state: on a saturated queue it is
// dropped and the stall policy applies (mark stalled, reserve one
// control-overload notice, close as a last resort), which is safe because
// the renderer re-syncs from the next notification.
//
// The exceptions that keep a *wsConn rather than a Responder are exactly
// the ones that need the connection as an identity, not as a writer:
// handleHistoryRecord (capture tab id), handleTunnelOpen (owner-tunnel map
// key), handleOpen/handleAttach/setSubscriber (register the connection as
// the session's subscriber), and the infrastructure (readLoop, the
// handleControlFrame dispatcher, ringToConn, closeSession). None of them
// can write to the socket: the only write path on *wsConn is the
// Responder trio below.
type Responder interface {
	TryResult(id json.RawMessage, result json.RawMessage) error
	TryError(id json.RawMessage, rpcErr RPCError) error
	TryNotify(method string, params json.RawMessage) error
}

// RPCError is the payload of a JSON-RPC error response. Data is omitted
// from the wire when nil (parity with jsonrpcErrorObj's omitempty). Method is
// internal-only metadata used by the sole response seam to diagnose an
// in-handler saturation refusal; it is never serialized.
type RPCError struct {
	Code    int
	Message string
	Data    any
	method  string
}

// wsConn wraps a connection's outbound side (outbound.Conn — the socket,
// the queue and the pump) together with the per-connection identity the
// capture registry scopes captures to. It implements Responder; there is no
// other write path. The raw *websocket.Conn lives in package outbound and
// never leaves it, so reaching past the queue is not expressible from this
// package, not merely discouraged.
//
// id is the per-connection (per-tab) identity: backend-assigned, monotonic,
// and never reused.
type wsConn struct {
	out *outbound.Conn
	log log.Logger
	id  uint64
	// methods is this connection's materialised control-handler set
	// (registration.go): method → submission + handler closure. The handlers
	// are constructed with THIS connection's Responder, so the set is
	// connection-scoped, built once on first control frame.
	methods map[string]controlMethod
}

func newWSConn(s *WSServer, conn *websocket.Conn, id uint64) *wsConn {
	return &wsConn{
		out: outbound.New(outbound.NewWebSocket(conn, wsReadLimit), outbound.Config{
			Budget: s.outboundBudget,
			OnStall: func(stalled bool) {
				if stalled {
					s.log.Warn("connection outbound stalled", "conn", id)
				} else {
					s.log.Debug("connection outbound recovered", "conn", id)
				}
			},
		}),
		log: s.log,
		id:  id,
	}
}

func (w *wsConn) TryResult(id json.RawMessage, result json.RawMessage) error {
	return w.out.TryEnqueueResponse(mustMarshal(newJSONRPCResult(id, result)))
}

func (w *wsConn) TryError(id json.RawMessage, rpcErr RPCError) error {
	if rpcErr.Code == SaturationErrorCode {
		if data, ok := rpcErr.Data.(saturationData); ok {
			logSaturationRefusal(w.log, rpcErr.method, "request", data)
		}
	}
	obj := &jsonrpcErrorObj{Code: rpcErr.Code, Message: rpcErr.Message}
	if rpcErr.Data != nil {
		obj.Data = rpcErr.Data
	}
	return w.out.TryEnqueueResponse(mustMarshal(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   obj,
	}))
}

func (w *wsConn) TryNotify(method string, params json.RawMessage) error {
	return w.out.TryEnqueue(websocket.TextMessage, mustMarshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}))
}

// respond delivers a pre-built jsonrpcResponse through the Responder
// capability. It is the migration seam for handlers that build a response
// in a local variable before replying; the enqueue is non-blocking either
// way, and the wire shape is byte-identical to the old direct write.
func respond(r Responder, resp jsonrpcResponse) error {
	if resp.Error != nil {
		return r.TryError(resp.ID, RPCError{
			Code:    resp.Error.Code,
			Message: resp.Error.Message,
			Data:    resp.Error.Data,
		})
	}
	return r.TryResult(resp.ID, resp.Result)
}

// connState tracks sessions this connection is attached to (opened or
// reattached). On disconnect the entries are discarded — sessions and their
// rings survive (AD-9). It still gates data-frame/resize/close so a
// connection cannot touch a session it has not opened or reattached to.
// generation is the tab's command-submission counter: it is what makes
// "a superseding submission from that tab" a backend fact — the capture
// scope rides on it (the renderer's session-local command ids never cross
// the wire).
type connState struct {
	mu         sync.Mutex
	sessions   map[session.ID]session.Session
	generation uint64
}

func newConnState() *connState {
	return &connState{sessions: make(map[session.ID]session.Session)}
}

// nextGeneration advances and returns the tab's submission counter.
func (c *connState) nextGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	return c.generation
}

func (c *connState) add(sess session.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[sess.ID()] = sess
}

func (c *connState) remove(id session.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, id)
}

func (c *connState) has(id session.ID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.sessions[id]
	return ok
}

// get returns the connection's session object for id, if the connection
// owns it — what a handler needs to derive backend-authoritative facts
// (the ledger environment) from the session.
func (c *connState) get(id session.ID) (session.Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sessions[id]
	return s, ok
}

// Owns reports whether this connection has opened or reattached to the
// session — the exported form of has, added so connState satisfies
// filesystem.Caller and Registry.Acquire re-checks ownership on every
// files.* call (D15). One line of forwarding; the authorisation answer
// still comes from the one place that already owns it.
func (c *connState) Owns(id session.ID) bool { return c.has(id) }

// --- JSON-RPC types -------------------------------------------------------

// jsonrpcRequest is a JSON-RPC 2.0 request.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *jsonrpcErrorObj `json:"error,omitempty"`
}

type jsonrpcErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func newJSONRPCError(id json.RawMessage, code int, msg string) jsonrpcResponse {
	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcErrorObj{Code: code, Message: msg},
	}
}

func newJSONRPCResult(id json.RawMessage, result json.RawMessage) jsonrpcResponse {
	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// isJSONObject returns true if data, ignoring leading whitespace, starts with
// an opening brace ('{').
func isJSONObject(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// openParams is the strictly-decoded payload of the "open" RPC method
// (decodeOpenParams). Unknown members — including the obsolete `enhanced`
// renderer field — duplicate keys, wrong types, and trailing JSON are
// rejected as invalid params, so a malformed frame can never silently
// become an ordinary or SSH launch.
type openParams struct {
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	XPixel uint16 `json:"xpixel"`
	YPixel uint16 `json:"ypixel"`
	// Cwd is an optional canonical local directory for an ordinary replacement
	// shell. It is rejected on SSH and sandbox requests.
	Cwd string `json:"cwd,omitempty"`

	// SSH fields — when Kind="ssh", the session opens an SSH channel.
	// ProfileID identifies the SSH profile to connect to; the backend
	// resolves host, credentials and jump host from the profile store.
	// Passwords are never carried in the open params — they are late-bound
	// inside the SSH auth chain from the credential store.
	Kind      string `json:"kind,omitempty"`
	ProfileID string `json:"profileId,omitempty"`
	// Host opens a direct SSH connection without a saved profile. When set
	// (and ProfileID is empty), the backend resolves the host through
	// ~/.ssh/config (ssh -G) and opens a direct session. User optionally
	// overrides the resolved user.
	Host string `json:"host,omitempty"`
	User string `json:"user,omitempty"`
	// Sandbox is the strict opt-in filesystem sandbox request (ADR-0039 §5).
	// A present, valid object opts in; omitted means ordinary local; null is
	// rejected at decode. The renderer supplies workspace, settingsRevision
	// and bounded class-scoped add/remove deltas — never policy roots.
	Sandbox *openSandboxParams `json:"sandbox"`
	// Shell pins the far shell the launcher must target (nocx-pu4.1): a
	// user who knows their host runs zsh can say so, and where detection
	// is wrong they have an override. Empty means detect — the launcher
	// receives ShellAuto and the far login shell decides (nocx-6rj0).
	// Values: bash | zsh | unknown | auto; anything else is ignored with
	// a warn, never honoured (detection is the safe degrade for a
	// meaningless pin, and the launcher refuses unmapped kinds rather
	// than guessing if one slips past).
	Shell string `json:"shell,omitempty"`
	// Parent names the session this one is being opened FROM (nocx-9hu9d):
	// the tab the user was in when they asked for another one. Absent for a
	// root session, which is every session the product opens today.
	//
	// It is a CLAIM, not a fact: the renderer says where the open came from,
	// and the session registry decides whether that could be true (a live
	// session of this backend instance, at that epoch, whose own ancestry
	// does not reach the new session). A refused claim opens nothing.
	//
	// It carries provenance only. Nothing about this session's capabilities,
	// gates or lanes reads it — the parent gains no right over the child by
	// being named here (ADR-0020 §5).
	Parent *openParentParams `json:"parent,omitempty"`
	// PaneID names the PANE this session is the pipe of (nocx-isoph.2). It
	// is the durable identity — client-minted, UUIDv7, and UNTRUSTED like
	// every other id in the layout chain (design §7) — and it is what the
	// backend walks pane → tab → workspace to answer the ack's workspaceId
	// with. A pane and its session are two objects because D5 says so: the
	// process dies with the backend and the pane does not, so the session id
	// is minted here (AD-7) and the pane id can only come from the renderer.
	//
	// Absent means "this session is not attached to a recorded pane", which
	// is every open until the renderer starts minting them (nocx-isoph.4),
	// and the workspace is then the default. Present but naming no pane is
	// refused: the two are different facts.
	PaneID string `json:"paneId,omitempty"`
}

// openParentParams is the claimed parent edge: the FULL identity of the
// opener, never a bare session id. The id alone re-resolves to whatever holds
// it now, which is exactly the ambiguity (instanceId, sessionEpoch) exists to
// remove (nocx-3oupk) — and the edge is written once and never revisited, so
// an ambiguity admitted here is permanent.
type openParentParams struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
}

// openSandboxParams is the strictly-decoded sandbox block of open
// (ADR-0039 §5). workspace and settingsRevision are required; addWritable,
// removeWritable, addReadOnly and removeReadOnly are optional bounded deltas.
// Populated by decodeOpenSandbox, never by a permissive Unmarshal.
type openSandboxParams struct {
	Workspace        string
	SettingsRevision int
	// ProfileRevision is the nullable per-workspace profile revision the
	// dialog displayed (design 2026-08-23 §4.3): null for a standard-source
	// launch, the exact sandboxProfile.revision for a workspace-source one.
	ProfileRevision *int64
	AddWritable     []string
	RemoveWritable  []string
	AddReadOnly     []string
	RemoveReadOnly  []string
}

// resizeParams is the payload of the "resize" RPC method.
type resizeParams struct {
	SessionID string `json:"sessionId"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
	XPixel    uint16 `json:"xpixel"`
	YPixel    uint16 `json:"ypixel"`
}

// closeParams is the payload of the "close" RPC method.
type closeParams struct {
	SessionID string `json:"sessionId"`
}

// attachParams is the payload of the "attach" RPC method (AD-9 reconnect).
type attachParams struct {
	SessionID string `json:"sessionId"`
	Offset    uint64 `json:"offset"`
}

// ackParams is the payload of the "ack" notification (AD-9 trimming).
type ackParams struct {
	SessionID string `json:"sessionId"`
	Offset    uint64 `json:"offset"`
}

// ── ingress limits ─────────────────────────────────────────────────────────

// wsReadLimit is the hard ceiling on a single WebSocket frame, set on the
// connection in handleSession. A frame above it is refused by the protocol
// layer before the read loop ever sees it: gorilla closes the connection
// with close code 1009 (message too big) and ReadMessage returns an error.
// That is the chosen failure mode — clean and per-connection; other
// connections and their sessions are untouched (AD-9). It is the
// last-resort bound behind the per-method params budget: 16 MiB exceeds the
// largest declared budget (8 MiB for document-carrying methods) plus
// envelope and base64 overhead, so no legitimate frame can trip it.
const wsReadLimit = 16 << 20 // 16 MiB

// outboundBudgetBytes caps queued outbound bytes across all connections of
// one server. The per-connection queue (outbound.DefaultQueueDepth frames,
// up to ~2 MiB at the largest data frame) is the primary bound; this is the
// process-wide additional bound the design calls for. 32 MiB tolerates
// roughly a dozen saturated connections before the shared budget itself
// trips the stall policy.
const outboundBudgetBytes = 32 << 20 // 32 MiB

// envelopeScanCap bounds how many bytes of a control frame the read loop
// scans looking for the JSON-RPC envelope. The envelope of a legitimate
// request is a few hundred bytes — the renderer serializes method before
// params (frontend/src/ipc/dispatcher.ts emits {jsonrpc, id, method,
// params}) — so a frame whose method does not appear within the cap is
// refused without ever scanning the rest of it.
const envelopeScanCap = 4 << 10 // 4 KiB

// Params budgets cap the TOTAL control frame — envelope plus params plus
// JSON overhead. The envelope is ~200 bytes, so the params allowance is
// effectively the budget. Sized against measured real payloads: a portable
// backup of 25 profiles plus settings plus 300 command-history rows travels
// as a 123 KB frame; a 20-host Tabby config as a 2.7 KB frame; a pasted
// private key as ~0.6 KB.
const (
	// budgetTiny covers vault.unlockResolved and connections.passwordResolved,
	// the only methods that run on the read loop itself: their frames must be
	// small so the loop can never be made to spend real work on them.
	budgetTiny = 1 << 10 // 1 KiB
	// budgetDefault covers every ordinary method. 64 KiB is ~100x the largest
	// ordinary frame measured (a pasted key).
	budgetDefault = 64 << 10 // 64 KiB
	// budgetDocument covers methods that legitimately carry a whole document:
	// an exported backup or a Tabby config. 8 MiB is ~68x the measured
	// realistic backup, comfortably absorbing a year of command history.
	budgetDocument = 8 << 20 // 8 MiB
)

// paramsBudgetForMethod returns the frame budget for a control-plane method.
// A frame above its method's budget is refused by handleControlFrame before
// any params decoding, so the read loop never touches the payload of an
// oversized frame.
func paramsBudgetForMethod(method string) int {
	switch method {
	case "vault.unlockResolved", "connections.passwordResolved":
		return budgetTiny
	case "agent.readScreenResolved":
		// A readScreen resolution carries a live frame — every cell of a
		// screen with its attributes — bounded by the frame validation
		// (rows ≤ 10k, cols ≤ 2k, 5M chars) and this wire budget, the
		// document tier. The broker's own per-kind bound matches.
		return budgetDocument
	case "agent.runResolved":
		// A run resolution carries a command's output window — text
		// bounded by the renderer's maxRunOutputWindowChars clamp and this
		// wire budget, the document tier. The broker's own per-kind bound
		// matches.
		return budgetDocument
	case "backup.create", "backup.preview", "backup.restore", "backup.saveToFile",
		"profiles.importTabby", "profiles.tabbyPreview",
		// api.import.postman carries a Postman export INLINE as `document`
		// (the route for a backend that is not the person's machine), so
		// it is a document-carrying method in exactly the sense this tier
		// names. budgetDefault is 64 KiB because it was sized on an
		// ORDINARY frame — a pasted key, a form — and an export of a
		// working API with its saved examples is not that size class;
		// apiimport bounds the document it parses at 16 MiB, which is the
		// size an export is expected to reach.
		//
		// This tier is not the bound a caller meets. That is
		// maxAPIImportDocumentRunes (1 MiB, ws_api_handlers.go), which is
		// what the refusal names and what points at `path` for anything
		// larger; the budget only has to sit above it so the refusal comes
		// from the method rather than from the frame.
		"api.import.postman":
		return budgetDocument
	default:
		return budgetDefault
	}
}

// errEnvelopeNotObject reports a control frame whose first JSON token is
// not an object.
var errEnvelopeNotObject = errors.New("not a JSON object")

// errEnvelopeTooLarge reports a control frame whose envelope could not be
// decoded within the scan cap: the method did not appear in time, which is
// how a huge params value is refused without ever being materialised.
var errEnvelopeTooLarge = errors.New("envelope exceeds the scan cap")

var errEnvelopeDuplicateMember = errors.New("duplicate envelope member")

// decodeEnvelope extracts the JSON-RPC envelope — jsonrpc, id, method —
// from a control frame WITHOUT decoding the rest of it, using a stdlib
// json.Decoder capped at envelopeScanCap bytes (io.LimitReader). It walks
// the top-level members with Token(), decodes keys and values with standard
// JSON semantics (escapes, Unicode, control characters and literals all
// follow encoding/json), and stops the moment method is decoded: params and
// anything after it are never tokenized, which is what keeps a huge frame
// cheap for the read loop.
//
// The cap is load-bearing: Token() materialises each token it returns, but
// it can only read — and therefore allocate — as much input as the limited
// reader provides. A frame whose method does not appear within the cap
// exhausts the reader and is refused here, without ever materialising the
// value that follows (a 16 MiB params string before method costs a few KiB).
//
// Error contract, mirroring the old whole-frame json.Unmarshal:
//   - errEnvelopeNotObject (no JSON object) → -32600 Invalid Request
//   - errEnvelopeTooLarge (method not found within the cap) → -32600
//   - errEnvelopeDuplicateMember (an envelope member repeats) → -32600
//   - a cleanly-formed object with no method returns (req, nil) with an
//     empty Method, which the caller answers with -32600 — same as before
//   - any other failure (invalid escape, control character, mismatched
//     container, invalid literal, truncated frame) → -32700 Parse error
func decodeEnvelope(data []byte) (jsonrpcRequest, error) {
	var req jsonrpcRequest
	er := &envelopeReader{r: bytes.NewReader(data), cap: envelopeScanCap}
	dec := json.NewDecoder(er)

	tok, err := dec.Token()
	if err != nil {
		if !startsWithObject(data) {
			// No JSON object — parity with the old first-byte check.
			return req, errEnvelopeNotObject
		}
		return req, envelopeError(er, err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return req, errEnvelopeNotObject
	}

	// Duplicate envelope members are refused so the budget gate and the
	// full decode cannot read the same frame differently.
	seen := make(map[string]bool, 4)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return req, envelopeError(er, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return req, errors.New("non-string member name")
		}
		switch key {
		case "jsonrpc", "id", "method", "params":
			if seen[key] {
				return req, errEnvelopeDuplicateMember
			}
			seen[key] = true
		}

		switch key {
		case "method":
			var m string
			if err := dec.Decode(&m); err != nil {
				return req, envelopeError(er, err)
			}
			req.Method = m
			return req, nil // envelope complete — params is never tokenized
		case "jsonrpc":
			var v string
			if err := dec.Decode(&v); err != nil {
				return req, envelopeError(er, err)
			}
			req.JSONRPC = v
		case "id":
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				return req, envelopeError(er, err)
			}
			// An explicit null id behaves exactly like an absent one (as
			// with the old whole-frame unmarshal): the response carries no
			// id field at all.
			if string(v) != "null" {
				req.ID = v
			}
		default:
			// Skip the value, decoding into a throwaway RawMessage. Bounded
			// by the cap: the decoder cannot read more than the limited
			// reader provides, so a huge value is never materialised.
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				return req, envelopeError(er, err)
			}
		}
	}
	return req, nil
}

// envelopeReader serves at most cap bytes of a control frame to the
// json.Decoder, recording whether the cap was consumed. encoding/json's
// InputOffset does not reliably reflect a read truncated by a limit, so
// this wrapper is how decodeEnvelope distinguishes "method never appeared
// within the cap" (errEnvelopeTooLarge) from a syntax error inside it.
type envelopeReader struct {
	r   *bytes.Reader
	n   int
	cap int
	hit bool // the cap was consumed
}

func (er *envelopeReader) Read(p []byte) (int, error) {
	remaining := er.cap - er.n
	if remaining <= 0 {
		er.hit = true
		return 0, io.EOF
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := er.r.Read(p)
	er.n += n
	if n == remaining {
		// The whole budget went into one read: any later failure is cap
		// exhaustion, not a syntax error.
		er.hit = true
	}
	return n, err
}

// startsWithObject reports whether data's first non-whitespace byte within
// envelopeScanCap bytes is '{'. Parity with the old scanner's first-byte
// check: a frame that does not begin with an object is an invalid request,
// not a parse error. It never scans more than the cap, so a hostile
// whitespace frame cannot make the error path O(frame).
func startsWithObject(data []byte) bool {
	if len(data) > envelopeScanCap {
		data = data[:envelopeScanCap]
	}
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return b == '{'
		}
	}
	return false
}

// envelopeError maps a tokenizer failure to the envelope error contract: a
// failure after the capped reader was exhausted means the method did not
// appear within envelopeScanCap bytes (-32600); anything else is a syntax
// error (-32700).
func envelopeError(er *envelopeReader, err error) error {
	if er.hit {
		return errEnvelopeTooLarge
	}
	return err
}

// --- HTTP handler ---------------------------------------------------------

func (s *WSServer) handleSession(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error("ws upgrade", "error", err)
		return
	}
	// The outbound constructor applies the ingress read limit and takes
	// ownership of the socket: from here the pump is the only writer and
	// out.Close() (below) is the only teardown. No other reference to the
	// raw *websocket.Conn outlives this function.
	// Derive a cancel context so that when handleSession returns,
	// ringToConn goroutines blocked in waitForData receive ctx.Done()
	// and exit. r.Context() is NOT reliably cancelled for hijacked
	// WebSocket connections.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	wconn := newWSConn(s, conn, s.nextConnID.Add(1))

	s.registerConn(wconn)
	defer s.unregisterConn(wconn)
	// Closing the outbound side closes the socket and stops the pump. It
	// also unblocks a pump write in flight, so teardown never waits on a
	// stuck renderer.
	defer wconn.out.Close()
	state := newConnState()
	// Materialise the connection's control-handler set once, here, where the
	// connState exists: the handlers' build closures capture it (and the
	// connection's Responder), so every dispatch after this has its state.
	wconn.methods = connMethods(s.methods, wconn, state)
	readErr := make(chan error, 1)
	go s.readLoop(ctx, wconn, state, readErr)

	<-readErr

	// Connection dropped. Clear this connection from every subscriber slot
	// it still holds (a newer subscriber is preserved), then wake any ring
	// waiters blocked on this connection's sessions. The cancel above also
	// fires (via defer) which is the primary exit signal for ringToConn.
	state.mu.Lock()
	for sid := range state.sessions {
		if rx := s.getRx(sid); rx != nil {
			rx.clearSubscriber(wconn)
			rx.ring.wake()
		}
	}
	state.mu.Unlock()
}

func (s *WSServer) readLoop(ctx context.Context, wconn *wsConn, state *connState, readErr chan<- error) {
	defer func() { readErr <- nil }()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgType, data, err := wconn.out.ReadMessage()
		if err != nil {
			return
		}

		switch msgType {
		case websocket.TextMessage:
			s.handleControlFrame(ctx, wconn, state, data)
		case websocket.BinaryMessage:
			s.handleDataFrame(state, data)
		}
	}
}

func (s *WSServer) handleControlFrame(ctx context.Context, wconn *wsConn, state *connState, data []byte) {
	// Envelope-only decode through a capped stdlib tokenizer: the read loop
	// learns jsonrpc, id and method and stops there. The tokenizer reads at
	// most envelopeScanCap bytes (io.LimitReader), so a frame's size cannot
	// make this step expensive — a huge params value before method is never
	// materialised, it just exhausts the cap and is refused.
	req, err := decodeEnvelope(data)
	if err != nil {
		// Transport-wide rule: any control frame may carry a secret, so
		// none are logged verbatim. Log size and category only.
		switch {
		case errors.Is(err, errEnvelopeNotObject),
			errors.Is(err, errEnvelopeTooLarge),
			errors.Is(err, errEnvelopeDuplicateMember):
			s.log.Warn("jsonrpc invalid request", "len", len(data), "category", err.Error())
			resp := newJSONRPCError(json.RawMessage("null"), -32600, "Invalid Request")
			_ = respond(wconn, resp)
		default:
			s.log.Warn("jsonrpc parse error", "len", len(data), "category", "syntax_error")
			resp := newJSONRPCError(json.RawMessage("null"), -32700, "Parse error")
			_ = respond(wconn, resp)
		}
		return
	}

	if req.JSONRPC != "2.0" || req.Method == "" {
		resp := newJSONRPCError(req.ID, -32600, "Invalid Request")
		_ = respond(wconn, resp)
		return
	}

	// Per-method params budget: an oversized frame is refused by length
	// BEFORE any full decode, so the read loop never spends time on it —
	// the connection survives and reads the next frame immediately.
	budgetedMethod := req.Method
	if budget := paramsBudgetForMethod(budgetedMethod); len(data) > budget {
		s.log.Warn("jsonrpc params over budget", "method", req.Method, "len", len(data))
		resp := newJSONRPCError(req.ID, -32602, fmt.Sprintf(
			"params exceed the size budget for method %s (%d bytes)", req.Method, budget))
		_ = respond(wconn, resp)
		return
	}

	// The frame is within budget: decode it fully. This validates
	// everything the envelope pass deliberately skipped — escapes, control
	// characters, container matching, literals, and any bytes after params.
	if err := json.Unmarshal(data, &req); err != nil {
		s.log.Warn("jsonrpc parse error", "len", len(data), "category", "syntax_error")
		resp := newJSONRPCError(json.RawMessage("null"), -32700, "Parse error")
		_ = respond(wconn, resp)
		return
	}
	// An explicit null id behaves exactly like an absent one: the response
	// carries no id field (parity with the envelope pass and the old
	// whole-frame unmarshal).
	if string(req.ID) == "null" {
		req.ID = nil
	}

	// The envelope pass and the full decode must agree on the method.
	// encoding/json resolves a repeated top-level member to its LAST
	// occurrence, so without this check a frame could take one method's
	// budget (say the 8 MiB document tier of backup.preview) and dispatch as
	// another (say the 1 KiB resolver tier of vault.unlockResolved).
	if req.Method != budgetedMethod {
		s.log.Warn("jsonrpc invalid request", "len", len(data), "category", "method_mismatch")
		resp := newJSONRPCError(req.ID, -32600, "Invalid Request")
		_ = respond(wconn, resp)
		return
	}

	// Dispatch through the registration set: the method's submission decides
	// how the work runs (immediate on the read loop, on a worker under the
	// lane, or refused) — there is no branch on method names here, and no
	// switch. The per-connection handler set is materialised once per
	// connection in handleSession.
	m, ok := s.connMethodsFor(wconn)[req.Method]
	if !ok {
		resp := newJSONRPCError(req.ID, -32601, "Method not found")
		_ = respond(wconn, resp)
		return
	}
	rej := m.submission.TrySubmit(ctx, control.Task{Run: func(pctx context.Context) {
		m.handle(pctx, req)
	}})
	if rej == nil {
		return
	}
	sat := saturationErrorFor(rej)
	// Refused. A request (has an id) answers with the saturation error; a
	// notification (no id) has no response to carry it, so the server emits
	// the rate-limited control.saturated notification instead.
	if req.ID != nil {
		_ = wconn.TryError(req.ID, saturationRPCError(req.Method, rej))
		return
	}
	logSaturationRefusal(s.log, req.Method, "notification", sat.Data)
	s.emitSaturatedNotification(wconn, req.Method, rej)
}

// connMethodsFor returns the connection's materialised handler set. It is
// built once in handleSession, where the connState exists (the handlers'
// build closures capture it), so a real connection always has its set before
// its read loop starts. Hand-build wsConns in tests that never dispatch have
// a nil map; dispatch on one is a test bug, not a supported path.
func (s *WSServer) connMethodsFor(wconn *wsConn) map[string]controlMethod {
	if wconn.methods == nil {
		wconn.methods = connMethods(s.methods, wconn, nil)
	}
	return wconn.methods
}

// emitSaturatedNotification sends the control.saturated notification for a
// refused notification, rate-limited per (methodClass, scope): a burst of
// refused notifications must not flood the wire.
func (s *WSServer) emitSaturatedNotification(wconn Responder, method string, rej *control.Rejection) {
	class := methodClassFor(method)
	if !s.satNotify.allow(class, rej.Scope) {
		return
	}
	params := saturatedNotificationParamsFor(class, rej.Scope)
	_ = wconn.TryNotify("control.saturated", mustMarshal(params))
}

// --- control-plane handlers -----------------------------------------------

// desiredModeForAck reports the resolved destination mode for the open ack
// (nocx-mlm7). The resolver stamps the mode on the ConnectConfig it builds
// from the profile's effective desiredMode; a direct-host open (alias or
// ad-hoc — no profile to say otherwise) and a local session keep the
// hardcoded default: script (N3 — wrap and install automatically). Unknown
// values fall back to the same default so malformed profile data can never
// violate the open schema over the real socket.
func desiredModeForAck(remote *ssh.ConnectConfig) string {
	if remote == nil || remote.DesiredMode == "" {
		return string(profile.DesiredScript)
	}
	switch profile.DesiredMode(remote.DesiredMode) {
	case profile.DesiredRaw, profile.DesiredScript, profile.DesiredRelay:
		return remote.DesiredMode
	default:
		return string(profile.DesiredScript)
	}
}

// replayStoredForwards opens the profile's stored forwards at connect time
// (spec §8, D5; nocx-wzc4.5): the ports a user always forwards to a host
// are configured once and are simply there when the connection comes up.
// It runs on its own goroutine after the open ack — never before — so a
// slow connector acquire cannot delay the ack.
//
// Every result with a tunnel record is registered in the transport ledger
// WITHOUT a pane owner (spec §7.3): stored forwards are connection-owned
// and must survive pane close. A row that failed to start is still
// registered so ports.status shows it — the panel must not contradict a
// forward the backend knows failed (AGENTS.md: a soft degrade must be
// visible in the product, not only in a log). One row's failure never
// stops another's; that contract lives in connectfwd.Replay, and this
// loop never flattens a row's outcome into a generic failure.
func (s *WSServer) replayStoredForwards(profileID, host string, cfg *ssh.ConnectConfig) {
	if profileID == "" || cfg == nil || s.tunnelConnector == nil || s.profiles == nil {
		return
	}
	profs, err := s.profiles.LoadProfiles()
	if err != nil {
		s.log.Warn("stored forwards not replayed: profile store read failed", "profileId", profileID, "error", err)
		return
	}
	var forwards []profile.ForwardSpec
	for i := range profs {
		if profs[i].ID == profileID && profs[i].Options.Forwards != nil {
			forwards = *profs[i].Options.Forwards
			break
		}
	}
	if len(forwards) == 0 {
		return
	}
	// Background is deliberate — connection-owned rows, not request work:
	// the forwards a user configured on a profile must come up even if this
	// WebSocket dies mid-replay (spec §7.3: closing the tab leaves them
	// running). Owner: the stored-forward rows (connection-owned, tracked in
	// the tunnel ledger). Closing event: connectfwd.Replay returning after
	// every row was started or refused — the tunnels it starts outlive this
	// goroutine and end at server shutdown or their own stop.
	// The forward's connection is the profile's own: the WHOLE resolved
	// config — credentials, jump route, authorized endpoints — rides one
	// option that copies it into the connector's ConnectConfig, so the
	// forward is authorized and pool-keyed exactly like a tab (AD-4).
	opts := []ssh.ConnectOption{func(dst *ssh.ConnectConfig) { *dst = *cfg }}
	results := connectfwd.Replay(context.Background(), profileID, forwards, host, s.tunnelConnector, opts)
	for _, r := range results {
		if r.Tunnel == nil {
			// tunnel.New rejected the spec — no record exists to register.
			// The row's own failure is logged; there is nothing the panel
			// could show for a row that was never a tunnel.
			if r.Err != nil {
				s.log.Warn("stored forward rejected", "profileId", profileID, "index", r.Index, "error", r.Err)
			}
			continue
		}
		s.trackTunnelConnectionOwned(r.Tunnel)
		if r.Err != nil {
			s.log.Warn("stored forward failed to start", "profileId", profileID, "index", r.Index, "tunnelId", r.Tunnel.ID, "error", r.Err)
		}
	}
}

// trackTunnelConnectionOwned registers a tunnel in the ledger WITHOUT a tab
// owner (spec §7.3): a stored forward is the connection's, not the opening
// tab's, so closing the tab must not stop it. It stays stoppable through
// tunnel.stop and visible in ports.status for the life of the connection.
func (s *WSServer) trackTunnelConnectionOwned(t *tunnel.Tunnel) {
	s.tunnelMu.Lock()
	s.tunnels[t.ID] = t
	s.tunnelMu.Unlock()
}

// handleDataFrame routes an inbound binary frame to the correct session.
func (s *WSServer) handleDataFrame(state *connState, data []byte) {
	frame, err := DecodeFrame(data)
	if err != nil {
		s.log.Warn("bad data frame", "error", err, "len", len(data))
		return
	}

	switch frame.MsgType {
	case MsgTypeData:
		sid := session.IDFromBytes(frame.SessionID)
		if !state.has(sid) {
			s.log.Warn("data frame for unknown session", "session_id", string(sid))
			return
		}
		sess, err := s.registry.Get(sid)
		if err != nil {
			s.log.Warn("data frame for unknown session", "session_id", string(sid))
			return
		}
		// Enqueue the payload without blocking the readLoop. A dead SSH
		// channel (a NAT or firewall dropping silently, no RST) makes
		// ch.Write block forever, and the readLoop is the one goroutine
		// feeding EVERY session on this connection — so one dead tab
		// froze all of them (nocx-o2le). EnqueueWrite puts the frame on
		// a bounded per-session queue and returns immediately; the
		// readLoop is its sole sender, which is what keeps the queue in
		// the order the user typed.
		//
		// A full queue means the channel has stopped accepting bytes.
		// The frame is dropped — and the tab is TOLD, because input
		// that silently disappears is indistinguishable from a terminal
		// that ignores you.
		// The data plane's arrival log. Debug, so it costs nothing until
		// somebody asks — and somebody does: "were the keystrokes sent at
		// all, or did the renderer swallow them?" is the first question of
		// every input-routing defect, and without this it can only be
		// guessed at from the far side of the socket.
		//
		// This is the one plane carrying exactly what the user typed, so it
		// carries their passwords: every password for a host nocx holds no
		// credentials for is typed into a running ssh and arrives here as an
		// ordinary frame. log.Sensitive is what decides whether the bytes
		// reach a file — shown in a development build, redacted to a length
		// in a shipped one — and that decision is the logger's, not this
		// call site's, precisely so no call site can get it wrong.
		s.log.Debug("data frame",
			"session_id", string(sid),
			"bytes", len(frame.Payload),
			"payload", log.Sensitive(frame.Payload))
		if !sess.EnqueueWrite(frame.Payload) {
			s.log.Warn("session write queue full or closed, dropping frame",
				"session_id", string(sid))
			s.notifyInputStalled(sid)
		} else if rx := s.getRx(sid); rx != nil {
			rx.inputStalled.Store(false)
		}
	case MsgTypeMetadata:
		s.log.Info("metadata frame received (reserved for Phase-2 — dropped)")
	default:
		s.log.Warn("unknown msg-type in data frame", "msgType", frame.MsgType)
	}
}

// --- session / ring plumbing ----------------------------------------------

// pumpToRing reads PTY output and writes it into the replay ring.
// Uses background context so the pump outlives any single WebSocket
// connection (AD-9). Blocks on ring.write when the ring is full and
// nothing has been acked — that is the AD-10 backpressure seam.
func (s *WSServer) pumpToRing(ctx context.Context, sess session.Session, ring *outputRing) {
	err := sess.StartOutput(ctx, func(data []byte) error {
		return ring.write(data)
	})
	if err != nil {
		s.log.Debug("session output ended", "session_id", string(sess.ID()), "error", err)
	}
}

// ringToConn streams the output ring to a WebSocket connection starting at
// the given byte offset. Exits when the connection drops or the ring closes.
//
// Enforces AD-10 credit-based flow control: a subscriber stops sending once
// unacked bytes reach CreditLimit and resumes when an ack frees room. Each
// send is capped at FairChunk bytes so a flooding session releases the
// shared wsConn write mutex between chunks, giving other sessions a chance
// to send (cross-tab fairness).
func (s *WSServer) ringToConn(ctx context.Context, wconn *wsConn, sidBytes [16]byte, ring *outputRing, startOffset uint64) {
	var pending []byte
	pos := startOffset

	for {
		// Wait until the in-flight window has room (AD-10). The ring owns
		// the predicate; acked may legitimately exceed pos after a large
		// ack on reattach, which counts as no bytes unacked.
		if ring.waitForCredit(ctx, pos, CreditLimit) {
			return
		}

		if len(pending) == 0 {
			var data []byte
			data, _, _ = ring.snapshot(pos)
			if len(data) == 0 {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if ring.waitForData(ctx, pos) {
					return
				}
				continue
			}
			pending = data
		}

		// Cap each frame at FairChunk for cross-session fairness (AD-10).
		// Splitting one PTY read (~32 KB) into ≤4 frames keeps one
		// flooding session from occupying the shared outbound queue.
		chunk := pending
		if len(chunk) > FairChunk {
			chunk = chunk[:FairChunk]
		}

		f := Frame{
			Version:   FrameVersion,
			MsgType:   MsgTypeData,
			SessionID: sidBytes,
			Payload:   chunk,
		}
		// The enqueue is non-blocking; a full queue means the renderer is
		// behind (the stall policy has already told it so). Wait for the
		// pump to drain rather than dropping PTY output — the ring is the
		// source of truth and pos advances only once the frame is queued,
		// so a reconnect at the renderer's ack offset replays anything
		// that never made it (AD-9).
		for {
			if err := wconn.out.TryEnqueue(websocket.BinaryMessage, f.Encode()); err == nil {
				break
			}
			if werr := wconn.out.WaitForRoom(ctx); werr != nil {
				return
			}
		}
		pos += uint64(len(chunk))
		pending = pending[len(chunk):]
	}
}

// monitorExit waits for the PTY to exit, then cleans up the ring and
// session and notifies the current subscriber. Exactly one instance runs
// per session (enforced by sessionRx.monitorOnce). Uses background context
// so it fires even after the WebSocket connection drops (AD-9).
func (s *WSServer) monitorExit(rx *sessionRx, sess session.Session) {
	<-sess.Done()

	// The session died on its own: the close gate is terminal here too, so
	// a resize in flight on a dead channel is cancelled and nothing new is
	// enqueued. Same gate as the explicit-close path (closeLane is
	// idempotent).
	s.closeLane(sess.ID())

	wconn, state := rx.getSubscriber()
	if state != nil {
		state.remove(sess.ID())
	}
	rx.ring.close()
	// The claim on the binding teardown (removeRx). On an explicit close the
	// handler's closeSession is running the same teardown on its own
	// goroutine; exactly one of us may delete the bindings, because deleting
	// them is also the one chance to announce them.
	owns := s.removeRx(sess.ID()) != nil
	_ = s.registry.Close(sess.ID())

	// The two responsibilities this path used to drop. closeSession has had
	// both since it was written; monitorExit is the OTHER teardown owner and
	// carried neither, so everything below held only for a session the user
	// closed by hand and not for one whose shell simply exited — which is
	// the ordinary way a session ends.
	//
	// cancelRecovery: protocol §12.1 says that when the pty/SSH channel's
	// Done() closes the session is dead, and the backend must "cancel any
	// pending restoration, reject late acknowledgements ... and make no
	// restoration claim". Without this an episode opened moments earlier
	// outlived the session, so a late lifecycle.recoverAck could still be
	// accepted for a lane whose shell was gone.
	//
	// unregisterLifecycleLanes: without it the lane→session map kept an
	// entry per dead session for the life of the process, and PublishLifecycle
	// went on resolving that lane to a session nobody can reach.
	s.cancelRecovery(sess.ID())
	s.unregisterLifecycleLanes(sess.ID())
	s.unregisterIntegration(sess.ID())

	// Port discovery (nocx-wzc4.2): if this was the last session on its
	// profile, forget the target and release its lease.
	s.discoverySessionClosed(sess)

	// Files (fm-w8) and git (spec §5.5): closing the terminal closes its
	// bindings (spec §5.1), and closing a binding is also the moment it is
	// announced — to the subscriber captured above, because removeRx has
	// already run and an emit-time lookup would find nobody (spec §5.2,
	// nocx-lzfb). Only the claimant does this: the other owner captured the
	// same subscriber and will announce with it, and both running is how the
	// announcement got lost (nocx-2h08).
	if owns {
		s.filesSessionClosed(sess.ID())
		s.gitSessionClosed(sess.ID(), wconn)
	}
	// The session's "allow in this session" answers die with it
	// (ws_sessionpolicy.go). Outside the claim: the claim exists because
	// deleting a git binding is also the one chance to announce it, and
	// forgetting an overlay announces nothing. Dropping twice costs nothing;
	// dropping never leaves a permission alive past the session it was
	// scoped to.
	s.sessionPolicy.Drop(sess.ID())

	if wconn == nil {
		return
	}

	// The cause discriminates an authoritative shell exit (with its status)
	// from a loss, so a tab whose ssh connection dropped is marked instead
	// of destroyed (nocx-ictcq). The classification is the session layer's
	// single owner; here the outcome is only mapped onto the wire fields.
	cause, status := sess.ExitOutcome()
	var statusPtr *int
	if cause == session.ExitExited {
		statusPtr = &status
	}
	ident := sess.Identity()
	if err := wconn.TryNotify("exit", mustMarshal(exitNotificationParams{
		SessionID:    string(sess.ID()),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
		Cause:        string(cause),
		Status:       statusPtr,
	})); err != nil {
		s.log.Debug("write exit notification", "error", err)
	}
}

// exitNotificationParams is the exit notification payload, declared once
// (contracts/exit.schema.json) and pinned by the contract tests: a closed-set
// cause discriminating an authoritative shell exit from a loss, with the
// exit status present exactly when the cause is "exited". additionalProperties
// is false on the schema, so a field added here but not declared there fails
// the DTO contract check (AD-8).
type exitNotificationParams struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	Cause        string `json:"cause"`
	Status       *int   `json:"status,omitempty"`
}

// notifyInputStalled tells the tab that its keystrokes are being dropped:
// the session's write queue is full, which means the channel underneath it
// has stopped accepting bytes. It fires once per stall — the flag clears on
// the next frame the queue accepts.
//
// This exists because the alternative is a slog.Warn nobody reads while the
// product goes on presenting a terminal that looks alive and ignores every
// key. A degrade the UI contradicts is how a broken session survives a
// release (nocx-o2le).
func (s *WSServer) notifyInputStalled(sid session.ID) {
	rx := s.getRx(sid)
	if rx == nil || !rx.inputStalled.CompareAndSwap(false, true) {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		// Nobody is attached to be told. Drop the latch so the tab that
		// attaches next is told by the next refused frame — a stall that
		// began while the renderer was away is still in force when it
		// returns, and the flag only clears when the queue ACCEPTS a frame,
		// which a stuck channel never does.
		rx.inputStalled.Store(false)
		return
	}
	if err := wconn.TryNotify("inputStalled", mustMarshal(map[string]string{
		"sessionId": string(sid),
	})); err != nil {
		// THE LATCH MUST NOT OUTLIVE A NOTIFICATION THAT WAS NOT SENT.
		// TryNotify is the droppable path, and its own contract says why
		// that is safe: "the frame is dropped, which is safe because a
		// notification is refreshable state the renderer re-syncs from the
		// next one" (outbound.TryEnqueue). This one is not refreshable.
		// It fires once per stall and the flag clears only when the write
		// queue ACCEPTS a frame, which is exactly what a stalled session
		// never does — so a dropped frame here meant the tab was never
		// told, for the whole life of the stall.
		//
		// And the moment it is most likely to be dropped is the moment it
		// matters: the outbound queue is full when the connection is
		// drowning, which is the same trouble that stalls the session.
		//
		// Clearing the latch costs at most a duplicate notification — the
		// renderer already treats this as a state, not an event — and buys
		// the promise the message exists to keep. Without it the warning
		// stays a slog.Warn nobody reads while the product presents a
		// terminal that looks alive and ignores every key (nocx-o2le).
		rx.inputStalled.Store(false)
		s.log.Debug("write inputStalled notification", "error", err)
	}
}

// closeSession tears down a session's transport state after its registry
// entry has been closed: the ring, the files and git bindings, and the
// discovery target. The registry close itself is the caller's — the
// explicit-close handler closes it through the session operation, monitorExit
// and Stop close it directly (AD-9) — because the session gate is a
// capability concern and the teardown is transport lifecycle (migration map,
// close finding). sess is the pre-close registry value, needed by the
// discovery teardown; nil is tolerated (callers without it).
func (s *WSServer) closeSession(sid session.ID, sess session.Session) {
	// Removing the receiver is the claim (removeRx), and the subscriber comes
	// off the receiver this goroutine removed — so the git teardown below
	// always has a destination, which an emit-time lookup no longer would.
	// A nil claim means monitorExit got there first: an explicit close wakes
	// it through the registry close, so both owners run on every hand-closed
	// tab. It captured the same subscriber and will announce with it; running
	// the teardown here as well would delete the bindings it is about to
	// announce and lose the notification (spec §5.2, nocx-lzfb, nocx-2h08).
	rx := s.removeRx(sid)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	rx.ring.close()
	// Session death wins (decision 8): cancel any pending restoration
	// episode as teardown begins, so a late ack is rejected and no
	// recovery is promised over a dead connection. The registry close is
	// the caller's under main's signature (nocx-292k).
	s.cancelRecovery(sid)
	s.filesSessionClosed(sid)
	s.laneInteractivity.remove(sid)
	s.gitSessionClosed(sid, wconn)
	// The session's "allow in this session" answers die with it
	// (ws_sessionpolicy.go).
	s.sessionPolicy.Drop(sid)
	s.unregisterLifecycleLanes(sid)
	s.unregisterIntegration(sid)
	s.discoverySessionClosed(sess)
}

// --- profile/group control-plane handlers -------------------------------
//
// These methods back the connection-manager JSON-RPC calls (AD-1 control
// plane). Each returns a JSON-RPC error (-32601) when the relevant store is
// not wired (WithProfileRepository/WithGroupRepository not called).

// profileMethodErrorCode maps a store error to a JSON-RPC code, same pattern
// as credentialErrorCode.
func profileMethodErrorCode(err error) int {
	switch {
	case errors.Is(err, profile.ErrProfileExists),
		errors.Is(err, profile.ErrProfileNotFound),
		errors.Is(err, profile.ErrProfileIDRequired),
		errors.Is(err, profile.ErrGroupExists),
		errors.Is(err, profile.ErrGroupNotFound),
		errors.Is(err, profile.ErrGroupIDRequired):
		return -32602
	default:
		return -32603
	}
}

// defaultsChanged reports whether two ProfileDefaults blocks differ.
// Both nil and empty defaults are treated as equivalent.
func defaultsChanged(a, b *profile.ProfileDefaults) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil || b == nil {
		return true
	}
	aJSON, errA := a.MarshalJSON()
	bJSON, errB := b.MarshalJSON()
	if errA != nil || errB != nil {
		return true
	}
	return string(aJSON) != string(bJSON)
}

// errInvalidKeyMaterial is returned when key text does not parse as a private
// key. It maps to error.data.reason: "invalid-key" in the JSON-RPC response.
type errInvalidKeyMaterial struct{ msg string }

// errInvalidKeyPassphrase is returned when a key passphrase cannot be stored:
// it does not open the stored key, or there is no stored key to verify it
// against. Maps to error.data.reason: "invalid-key-passphrase".
type errInvalidKeyPassphrase struct{ msg string }

func (e *errInvalidKeyPassphrase) Error() string { return e.msg }

func (e *errInvalidKeyMaterial) Error() string { return e.msg }

func parsePrivateKeyMaterial(keyText string) (fingerprint string, passphraseWanted bool, err error) {
	keyBytes := []byte(keyText)
	parsed, parseErr := gossh.ParseRawPrivateKey(keyBytes)
	if parseErr == nil {
		// Unencrypted key — wrap as signer and extract fingerprint.
		signer, signerErr := gossh.NewSignerFromKey(parsed)
		if signerErr != nil {
			return "", false, &errInvalidKeyMaterial{msg: fmt.Sprintf("cannot create signer from key: %v", signerErr)}
		}
		return gossh.FingerprintSHA256(signer.PublicKey()), false, nil
	}

	// Encrypted or otherwise unparseable.
	var passErr *gossh.PassphraseMissingError
	if errors.As(parseErr, &passErr) {
		if passErr.PublicKey != nil {
			// OpenSSH format encrypted key — public half is readable.
			return gossh.FingerprintSHA256(passErr.PublicKey), true, nil
		}
		// Traditional PEM-encrypted key: readable, usable, and its public half
		// is behind the passphrase we were not given.
		//
		// This used to be rejected, telling the user to convert the key. That
		// was wrong twice. The key works — ssh_auth.go already opens exactly
		// this shape with ParsePrivateKeyWithPassphrase, and it works in every
		// other client — so refusing it turned "I cannot compute a fingerprint
		// yet" into "your key is invalid". And the remedy quoted was not one:
		// RFC4716 is a PUBLIC key format, so the command would not have
		// converted the private key at all.
		//
		// The fingerprint is left empty, which this function's own contract
		// has always permitted. Empty means unknown-until-unlocked, not
		// absent: nothing downstream may treat it as an identity. The renderer
		// is told it wants a passphrase, so the empty fingerprint is never a
		// silent absence.
		return "", true, nil
	}

	return "", false, &errInvalidKeyMaterial{msg: fmt.Sprintf("not a valid private key: %v", parseErr)}
}

// handleImportTabby parses a Tabby config YAML and imports SSH profiles,
// groups, and optionally vault secrets into the wired profile/group
// repositories and SecretStore.
//
// When the config carries an encrypted vault section and passphrase is provided,
// every decrypted secret is stored via Vault.Create and bound to the profile
// whose connection target the tabby vault keyed it to, by host+port+user. An
// encrypted vault without a passphrase is an error.
//
// Order (ADR-0011 §4): secrets are written first, then the metadata that
// references them. A crash mid-import orphans secrets — reconciliation
// recovers on next start. The reverse would leave metadata pointing at
// nothing, which is a broken profile the user must repair by hand.
//
// Collision policy (nocx-y910.1): profiles and groups overwrite on duplicate
// ID.
//
// The passphrase is a parameter of the operation, asked once, stored nowhere
// — no field, no cache, no package variable.
// privateKeyLabel names the secret that carries an imported private-key
// passphrase. Tabby identifies the key only by a hash, which is unreadable on
// its own, so the label says what it is and keeps enough of the hash to tell
// two of them apart.
// ── Tabby import preview types ──────────────────────────────────────────

// TabbyPreviewResponse is returned by profiles.tabbyPreview.
type TabbyPreviewResponse struct {
	ProfilesToImport int             `json:"profilesToImport"`
	GroupsToImport   int             `json:"groupsToImport"`
	SecretsToImport  int             `json:"secretsToImport"`
	ProfileEntries   []ProfileEntry  `json:"profileEntries,omitempty"`
	GroupNames       []string        `json:"groupNames,omitempty"`
	SecretEntries    []SecretEntry   `json:"secretEntries,omitempty"`
	SkippedSecrets   []SkippedInfo   `json:"skippedSecrets,omitempty"`
	Collisions       []CollisionInfo `json:"collisions,omitempty"`
	SecretProvider   string          `json:"secretProvider"`
	PlanToken        string          `json:"planToken"`
}

// ProfileEntry describes one profile the import would create or modify.
type ProfileEntry struct {
	Name   string `json:"name"`
	Action string `json:"action"` // "new", "overwrite", "needs-review"
}

// SecretEntry describes one secret the import would create.
type SecretEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "password" or "passphrase"
}

// SkippedInfo describes one skipped secret and why.
type SkippedInfo struct {
	SecretType string `json:"secretType"`
	Reason     string `json:"reason"`
}

// CollisionInfo describes one collision in an import plan.
type CollisionInfo struct {
	Kind   string `json:"kind"`   // "profile", "group", "credential"
	Name   string `json:"name"`   // the identifier that collides
	Policy string `json:"policy"` // "overwrite", "refuse", "needs-review"
}

// tabbyExecuteParams is the payload for profiles.tabbyExecute.
type tabbyExecuteParams struct {
	PlanToken string `json:"planToken"`
}

// planTabbyImport parses a Tabby config, decrypts its vault (if passphrase
// supplied), and plans every profile, group, and secret WITHOUT writing
// anything. Returns the full importPlan for execution and a preview response
// for the renderer. The plan is stored server-side by the returned token.

// secretProviderName returns a human-readable name for where secrets would be
// stored. Uses the vault lifecycle if wired, otherwise returns "secret store".
func (s *WSServer) secretProviderName(ctx context.Context) string {
	if s.vaultLifecycle == nil {
		return "secret store"
	}
	snap := s.vaultLifecycle.Snapshot(ctx)
	for _, p := range snap.Providers {
		if string(p.ID) == "system" && p.Writable && p.Ready {
			return "OS keychain"
		}
	}
	return "encrypted file"
}

func privateKeyLabel(hash string) string {
	short := hash
	if len(short) > 8 {
		short = short[:8]
	}
	return "Tabby key " + short
}

// values we construct ourselves, which are always serializable).
func mustMarshal(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		panic("marshal: " + err.Error())
	}
	return out
}

// --- connection registry ---------------------------------------------------

// registerConn adds a connection to the broadcast set.
func (s *WSServer) registerConn(wc *wsConn) {
	s.connsMu.Lock()
	s.conns[wc] = struct{}{}
	s.connsMu.Unlock()
}

// unregisterConn removes a connection from the broadcast set and destroys
// every pending capture it owns: a transport disconnect is on the capture
// contract's destruction list, and one WebSocket carries every pane in a
// window, so the connection's death takes all of them (DestroyConnection —
// pane closure is a separate, per-pane trigger the renderer fires through
// pane.close). It also stops the tunnels the
// pane opened — pane-scoped teardown (spec §7.3): each forward holds its OWN
// pooled reference, so stopping this pane's forwards never touches another
// pane's on the same shared connection.
func (s *WSServer) unregisterConn(wc *wsConn) {
	s.connsMu.Lock()
	delete(s.conns, wc)
	s.connsMu.Unlock()
	if s.captures != nil {
		s.captures.DestroyConnection(connectionID(wc))
	}
	// The request broker's lifecycle signal (nocx-e2j1z): a pending request
	// this connection could answer loses an answerer, and a request with no
	// surviving recipient terminalizes instead of hanging. Called on EVERY
	// teardown — this function is the single path out of handleSession, so
	// a hijacked or stopped connection reaches it the same way a closed
	// socket does.
	if s.broker != nil {
		s.broker.ConnectionLost(wc)
	}
	s.stopOwnerTunnels(wc)
}

// broadcastSettingsChanged sends a settings.changed notification to every
// connected client. Best-effort and non-blocking: each notification is one
// enqueue into the connection's outbound queue, so a slow renderer delays
// its own connection only and never this domain callback.
func (s *WSServer) broadcastSettingsChanged(revision int, keys []string) {
	s.connsMu.Lock()
	// Copy under lock so enqueues happen outside the critical section.
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()

	params := map[string]any{
		"revision": revision,
		"keys":     keys,
	}
	for _, wc := range conns {
		_ = wc.TryNotify("settings.changed", mustMarshal(params))
	}
}

func (s *WSServer) broadcastSandboxAccessChanged(revision uint64) {
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()
	params := mustMarshal(map[string]uint64{"revision": revision})
	for _, wc := range conns {
		_ = wc.TryNotify("sandbox.access.changed", params)
	}
}

// rendererConns is the broker's Conns seam: a snapshot of the renderer
// connections currently attached. The snapshot is taken at Request time and
// is that request's recipient set — the only connections that can resolve
// it.
func (s *WSServer) rendererConns() []Conn {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	out := make([]Conn, 0, len(s.conns))
	for wc := range s.conns {
		out = append(out, wc)
	}
	return out
}

// rendererDeliver is the broker's Deliver seam: one request notification to
// one connection, through the connection's outbound enqueue — the pump is
// the sole writer (Responder's rule), so the broker never writes the socket
// directly. The returned error is the enqueue's real error (a full or
// closed outbound), which is what lets an undelivered request terminalize
// rather than wait for a timeout that may not come.
func (s *WSServer) rendererDeliver(conn Conn, method string, params json.RawMessage) error {
	wc, ok := conn.(*wsConn)
	if !ok {
		return fmt.Errorf("renderer deliver: connection %T is not a *wsConn", conn)
	}
	return wc.TryNotify(method, params)
}
