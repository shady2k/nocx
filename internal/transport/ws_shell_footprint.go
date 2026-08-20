package transport

// shell.footprint.* — the visible, removable footprint of nocx's silent
// install (P10, design §4.1 and §9). N3 installs the script bundle on a
// remote host WITHOUT asking; this surface is the other half of that trade:
// the product says what it wrote, where, and offers to remove it.
//
// Two methods, two very different costs:
//
//   - shell.footprint.status is READ-ONLY and never connects. The answer is
//     P7's installed fact — the protocol version, script version and
//     generation last OBSERVED, keyed by the resolved destination identity —
//     so the surface can show the footprint of a host nocx can no longer
//     reach, and a host wiped since then is described as "last seen", never
//     as installed now. The observation is the generation the far shell
//     names on the authenticated channel, not an inferred passport
//     (ADR-0024 §1); ws_installed_fact.go is the writer.
//   - shell.footprint.uninstall CONNECTS. Removing files is inherently a
//     dial: nocx owns credentials only for saved connections, so uninstall
//     is offered exactly there. The dial-and-call is owned by an internal/ssh
//     capability (UninstallIntegration); the transport never sees a raw SSH
//     client. Publisher.Uninstall() is the remover — manifest-owned,
//     unmodified files only; a modified file is a reported conflict and
//     stays; ~/.nocx is never removed recursively.

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/ssh"
)

// footprintPath is the design's canonical spelling of the remote install
// directory (§4). It is reported without connecting — the remote $HOME is
// unknowable from here — and it is the exact path a user removes by hand
// when no saved connection exists.
const footprintPath = "~/.nocx"

// RemoteUninstaller removes nocx's shell integration from a remote host.
// The single implementation is *ssh.RealClient, whose UninstallIntegration
// acquires the pooled connection the way Connect does, asks the SFTP carrier
// for the remote home and delegates Publisher.Uninstall to it — the raw SSH
// client never leaves internal/ssh. Wired at the composition root; when not
// wired, shell.footprint.uninstall answers an error and removes nothing.
type RemoteUninstaller interface {
	UninstallIntegration(ctx context.Context, host string, opts ...ssh.ConnectOption) (removed, conflicts []string, err error)
}

// WithInstalledFactStore attaches the backend-owned, persisted installed
// fact (P7, design §5.4): the inventory of what nocx wrote and where.
// shell.footprint.status reads it; when not wired, the surface answers an
// empty list. It is the store's ONLY reader — the delivery planner that
// once consulted it per destination is gone with the far-side guard, and
// the per-identity lookup went with it (nocx-m8jwn.10). The writer is
// ws_installed_fact.go, which records the generation the far shell names on
// the authenticated channel; the P7 observation RPC that used to write it
// was severed (ADR-0024 §1 — a passport is tty bytes and cannot activate a
// domain) and replaced there (nocx-ak2d).
func WithInstalledFactStore(store *ssh.InstalledFactStore) WSServerOption {
	return func(s *WSServer) { s.installedFacts = store }
}

// WithRemoteUninstaller attaches the uninstall capability behind the
// shell.footprint.uninstall JSON-RPC method. Without it the method refuses:
// an uninstall offered but not wired would fail at click time, which
// AGENTS.md rule 1 forbids.
func WithRemoteUninstaller(u RemoteUninstaller) WSServerOption {
	return func(s *WSServer) { s.remoteUninstaller = u }
}

// footprintHandlers answers shell.footprint.status and
// shell.footprint.uninstall: the visible, removable footprint of the silent
// install (P10, design §4.1 and §9). It holds ONLY seams — the
// installed-fact store, the remote uninstaller, the profile resolver holder,
// the ssh -G oracle and the profile repository the removable pass reads
// through — never the *WSServer, and no capability: the surface is a read of
// transport-owned facts plus a dial-and-call owned by the internal/ssh
// capability (UninstallIntegration).
type footprintHandlers struct {
	r           Responder
	facts       *ssh.InstalledFactStore
	uninstaller RemoteUninstaller
	resolver    *resolverHolder // profile resolver, readable post-construction
	sshCfg      ssh.ConfigResolver
	profiles    profile.ProfileRepository
	log         log.Logger
}

// shellFootprintDestination is one destination's footprint on the wire,
// matching contracts/shell.footprint.status.schema.json exactly.
type shellFootprintDestination struct {
	// Identity is the resolved destination key (user@host:port) the fact
	// is stored under — the same key two typed lines that resolve to the
	// same destination share (ADR-0015 narrowing).
	Identity string `json:"identity"`
	// Generation is the committed generation the far shell last named on
	// the authenticated channel (e.g. "v10"), preserved verbatim.
	Generation string `json:"generation"`
	// Path is the install directory on the remote host (~/.nocx).
	Path string `json:"path"`
	// ProtocolVersion is the manifest protocol last observed.
	ProtocolVersion string `json:"protocolVersion"`
	// ScriptVersion is the script version last observed.
	ScriptVersion string `json:"scriptVersion"`
	// LastObservedAt is when nocx last SAW this bundle — an observation,
	// never a claim about what is on the host right now: this call does
	// not connect.
	LastObservedAt time.Time `json:"lastObservedAt"`
	// RemovableProfileID names a saved connection that resolves to this
	// destination and can remove it. Absence IS the explanation: the
	// surface renders the manual-removal note from this one field and
	// offers no button — an action offered must be valid from the state
	// the user is in.
	RemovableProfileID *string `json:"removableProfileId"`
}

// shellFootprintStatusResult is the result of shell.footprint.status,
// matching contracts/shell.footprint.status.schema.json exactly.
type shellFootprintStatusResult struct {
	Destinations []shellFootprintDestination `json:"destinations"`
}

// shellFootprintUninstallResult is the result of shell.footprint.uninstall,
// matching contracts/shell.footprint.uninstall.schema.json exactly. The two
// lists are root-relative paths: a conflict is information the user acts on
// (the file stayed, and why), never an error to swallow.
type shellFootprintUninstallResult struct {
	Removed   []string `json:"removed"`
	Conflicts []string `json:"conflicts"`
}

// handleFootprintStatus serves shell.footprint.status: every recorded
// installed fact, plus which destinations a saved connection can remove.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"shell.footprint.status"}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"destinations":[{"identity":"pi@192.168.0.93:22","generation":"v10","path":"~/.nocx","protocolVersion":"1","scriptVersion":"0.6.0","lastObservedAt":"…","removableProfileId":"p_01"}]}}
//
// The removable pass resolves every saved profile through the SAME ssh -G
// oracle path and cache the fact-writer uses — never a reconstructed
// HostConfig, because two ways of computing an identity are how a footprint
// starts disagreeing with the profiles that could remove it. A profile that
// cannot be resolved (oracle failure, cyclic jump, missing profile) simply
// marks nothing removable: we could not prove the mapping, and the surface
// says removal needs a saved connection rather than offering a button that
// would fail at click time.
func (h footprintHandlers) handleFootprintStatus(ctx context.Context, req jsonrpcRequest) {
	destinations := make([]shellFootprintDestination, 0)
	if h.facts != nil {
		removable := h.removableProfiles(ctx)
		for _, f := range h.facts.All() {
			destinations = append(destinations, shellFootprintDestination{
				Identity:           f.Identity,
				Generation:         f.Generation,
				Path:               footprintPath,
				ProtocolVersion:    f.Protocol,
				ScriptVersion:      f.ScriptVersion,
				LastObservedAt:     f.ObservedAt,
				RemovableProfileID: removable[f.Identity],
			})
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(shellFootprintStatusResult{
		Destinations: destinations,
	}))
}

// removableProfiles maps resolved destination identity → saved profile id,
// for every profile that resolves. Fail-closed: any missing dependency
// (no profile store, no resolver, no oracle) or any resolution failure
// yields an empty map — nothing is claimed removable that cannot be proven.
func (h footprintHandlers) removableProfiles(ctx context.Context) map[string]*string {
	byIdentity := map[string]*string{}
	if h.profiles == nil || h.resolver == nil || h.sshCfg == nil {
		return byIdentity
	}
	profs, err := h.profiles.LoadProfiles()
	if err != nil {
		return byIdentity
	}
	for _, p := range profs {
		host, cfg, err := h.resolver.Resolve(p.ID)
		if err != nil {
			continue // a profile we cannot build a config for cannot be proven removable
		}
		argv := profileOracleArgv(host, cfg.User, cfg.Port)
		hc, err := h.sshCfg.ResolveArgv(ctx, argv)
		if err != nil {
			continue // the oracle cannot answer; do not guess the mapping
		}
		id := p.ID
		byIdentity[ssh.IdentityKey(hc)] = &id
	}
	return byIdentity
}

// profileOracleArgv builds the ssh -G oracle argv for a saved connection:
// the typed line a user would write to reach it. The user and port come
// from the RESOLVED config (the profile cascade), so a group-level user or
// port lands in the identity exactly as the connection would use it. -l
// spells the user the way ssh itself overrides a config User directive; a
// host that already carries user@ keeps it (the -l, when present, wins in
// OpenSSH, and ssh -G answers accordingly).
func profileOracleArgv(host, user string, port int) []string {
	argv := []string{"ssh", "-G"}
	if port > 0 && port != 22 {
		argv = append(argv, "-p", strconv.Itoa(port))
	}
	if user != "" {
		argv = append(argv, "-l", user)
	}
	return append(argv, host)
}

// handleFootprintUninstall serves shell.footprint.uninstall: remove the
// integration bundle on the host a saved profile connects to.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"shell.footprint.uninstall","params":{"profileId":"p_01"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"removed":["integration/v10/nocx.zsh","manifest.json"],"conflicts":["integration/v10/nocx.bash"]}}
//
// Only a saved connection is accepted — that is where nocx owns credentials.
// A direct-host destination is refused with a profile error; the surface
// never offers this button for one, because the status call reports
// removableProfileId only when a profile resolves to the destination.
func (h footprintHandlers) handleFootprintUninstall(ctx context.Context, req jsonrpcRequest) {
	var params struct {
		ProfileID string `json:"profileId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ProfileID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "profileId is required"})
		return
	}
	if h.resolver == nil || h.uninstaller == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "uninstall is not available"})
		return
	}

	host, cfg, err := h.resolver.Resolve(params.ProfileID)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "unknown profile"})
		return
	}

	// The resolved config travels as one ConnectOption, the same shape the
	// discovery scheduler uses: every credential, key and jump hop the
	// profile resolved to reaches the dial exactly as a tab's would.
	opts := []ssh.ConnectOption{func(dst *ssh.ConnectConfig) { *dst = *cfg }}
	removed, conflicts, err := h.uninstaller.UninstallIntegration(ctx, host, opts...)
	if err != nil {
		h.log.Warn("shell.footprint.uninstall failed", "profileId", params.ProfileID, "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "uninstall failed"})
		return
	}
	if removed == nil {
		removed = []string{}
	}
	if conflicts == nil {
		conflicts = []string{}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(shellFootprintUninstallResult{
		Removed:   removed,
		Conflicts: conflicts,
	}))
}
