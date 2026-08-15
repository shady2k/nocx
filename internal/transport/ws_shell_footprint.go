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
//     generation last OBSERVED via an accepted passport, keyed by the
//     resolved destination identity — so the surface can show the footprint
//     of a host nocx can no longer reach, and a host wiped since then is
//     described as "last seen", never as installed now.
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

	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
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
// fact (P7, design §5.4): the memory that makes the second connection to a
// host cheaper than the first. shell.footprint.status reads it to report
// what nocx wrote and where; when not wired, the surface answers an empty
// list. The observation RPC that used to WRITE the store was severed with
// the P7 delivery surface (ADR-0024 §1 — a passport is tty bytes and cannot
// activate a domain), so the transport only ever reads; the migration bead
// reconnects the writers to authenticated facts.
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

// HelperChannelCloser closes every live helper channel on one machine
// (remote-helper design D25) before its install directory is removed: the
// backend knows which version its own channels run, so an uninstall closes
// the channels IT holds — a helper running from a different nocx instance
// sharing the same $HOME is out of reach, which the design accepts. The
// single implementation is the composition root's helper registry
// (internal/app); when not wired, shell.footprint.helperUninstall refuses
// — without a closer the handler cannot prove the close-before-remove
// order that is the whole point of the rule.
type HelperChannelCloser interface {
	CloseHelpersFor(fingerprint string)
}

// RemoteHelperUninstaller removes a helper install from a remote host,
// owning the dial-and-call end to end (D25). The single implementation is
// *ssh.RealClient, whose UninstallHelper acquires the same write-capable
// SFTP lease an install uses and delegates deploy.Uninstall to it — the
// raw SSH client never leaves internal/ssh. Wired at the composition root;
// when not wired, shell.footprint.helperUninstall answers an error and
// removes nothing.
type RemoteHelperUninstaller interface {
	UninstallHelper(ctx context.Context, host string, opts ...ssh.ConnectOption) (removed bool, err error)
}

// WithHelperChannelCloser attaches the D25 channel closer behind
// shell.footprint.helperUninstall. Without it the method refuses: an
// uninstall that cannot close the running helper's channel before removing
// its directory is exactly the half-dead-process state D25 exists to
// forbid.
func WithHelperChannelCloser(c HelperChannelCloser) WSServerOption {
	return func(s *WSServer) { s.helperCloser = c }
}

// WithRemoteHelperUninstaller attaches the helper-removal capability behind
// shell.footprint.helperUninstall. Without it the method refuses: an
// uninstall offered but not wired would fail at click time, which
// AGENTS.md rule 1 forbids.
func WithRemoteHelperUninstaller(u RemoteHelperUninstaller) WSServerOption {
	return func(s *WSServer) { s.helperUninstaller = u }
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
	// helperInstalls is the observed-helper-installs store (remote-helper
	// design D8): the memory that lets the same surface list the helper
	// footprint without connecting. When nil, the helpers list is empty —
	// nothing is claimed installed that cannot be shown.
	helperInstalls *consent.InstallStore
	// consent is the per-machine relay-tier answer store (remote-helper
	// design D8): the write half shell.footprint.consent persists grants
	// through. When nil, the method refuses — the consent prompt is never
	// offered by a server that cannot record the answer.
	consent  *consent.Store
	registry session.Registry
	// helperUninstaller removes a helper install tree on a remote host
	// (D25); closer closes every live helper channel on the machine
	// BEFORE the tree is removed — the order D25 is written around. Both
	// nil means shell.footprint.helperUninstall refuses.
	helperUninstaller RemoteHelperUninstaller
	closer            HelperChannelCloser
	resolver          *resolverHolder // profile resolver, readable post-construction
	sshCfg            ssh.ConfigResolver
	profiles          profile.ProfileRepository
	log               log.Logger
}

// WithHelperInstallStore attaches the observed helper installs behind
// shell.footprint.status (remote-helper design D8): the helper row of the
// footprint screen. Without it the surface answers an empty helpers list —
// never a claim the surface cannot back.
func WithHelperInstallStore(store *consent.InstallStore) WSServerOption {
	return func(s *WSServer) { s.helperInstalls = store }
}

// WithHelperConsentStore attaches the per-machine relay-tier answer store
// behind shell.footprint.consent (remote-helper design D8): the accept
// RPC the git panel's consent prompt calls. Without it the method refuses
// — an accept offered but not persistable would fail at click time, which
// AGENTS.md rule 1 forbids.
func WithHelperConsentStore(store *consent.Store) WSServerOption {
	return func(s *WSServer) { s.helperConsent = store }
}

// shellFootprintConsentResult is the result of shell.footprint.consent,
// matching contracts/shell.footprint.consent.schema.json exactly: the
// machine has been raised to the relay tier (D8).
type shellFootprintConsentResult struct {
	State string `json:"state"`
}

// handleConsent serves shell.footprint.consent: the git panel's Accept —
// raise the session's machine to the relay tier (D8). The machine is
// resolved from the SESSION the requesting connection owns (connState,
// D15) and keyed by its host public-key fingerprint — never a
// client-supplied fingerprint, never a session the caller does not own.
// An empty fingerprint refuses: consent under one would make every
// machine share one answer (consent design §3.2).
//
//	--> {"jsonrpc":"2.0","id":1,"method":"shell.footprint.consent","params":{"sessionId":"…"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"state":"granted"}}
func (h footprintHandlers) handleConsent(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.consent == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "shell.footprint.consent is not available (no consent store wired)"})
		return
	}
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId required"})
		return
	}
	sid := session.ID(params.SessionID)
	if !state.has(sid) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	sess, err := h.registry.Get(sid)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	fp := sess.HostKeyFingerprint()
	if fp == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: this session has no host key — consent cannot be granted"})
		return
	}
	if err := h.consent.Grant(fp); err != nil {
		h.log.Warn("shell.footprint.consent failed", "host", sess.Host(), "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "shell.footprint.consent: " + err.Error()})
		return
	}
	h.log.Info("helper consent granted", "host", sess.Host())
	_ = h.r.TryResult(req.ID, mustMarshal(shellFootprintConsentResult{State: string(consent.Granted)}))
}

// shellFootprintDestination is one destination's footprint on the wire,
// matching contracts/shell.footprint.status.schema.json exactly.
type shellFootprintDestination struct {
	// Identity is the resolved destination key (user@host:port) the fact
	// is stored under — the same key two typed lines that resolve to the
	// same destination share (ADR-0015 narrowing).
	Identity string `json:"identity"`
	// Generation is the committed generation the last accepted passport
	// named (e.g. "v10"), preserved verbatim.
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

// shellFootprintHelper is one observed helper installation on one machine,
// matching contracts/shell.footprint.status.schema.json exactly. Like the
// destinations, it is an observation recorded when the install completed —
// this call never connects, so the row is "installed, as last observed",
// never a claim about the host's current state.
type shellFootprintHelper struct {
	// Identity is the destination identity (user@host:port) the screen
	// shows.
	Identity string `json:"identity"`
	// Fingerprint is the host public-key fingerprint the consent answer
	// is keyed by (consent design §3.2) — the same machine reached any
	// way is one row.
	Fingerprint string `json:"fingerprint"`
	// Path is the versioned install directory on the remote host.
	Path string `json:"path"`
	// Hash is the content hash of the installed binary (D7).
	Hash string `json:"hash"`
	// InstalledAt is when nocx last observed this install complete.
	InstalledAt time.Time `json:"installedAt"`
	// RemovableProfileID names a saved connection that resolves to this
	// destination and can remove it — the same removable pass the
	// destinations use, so one spelling of "which profile reaches this
	// machine" answers both row kinds. Absence IS the explanation: the
	// surface renders the manual-removal note and offers no button.
	RemovableProfileID *string `json:"removableProfileId"`
}

// shellFootprintStatusResult is the result of shell.footprint.status,
// matching contracts/shell.footprint.status.schema.json exactly. helpers is
// the observed helper footprint (remote-helper design D8); it is absent
// when no install has been recorded, never null.
type shellFootprintStatusResult struct {
	Destinations []shellFootprintDestination `json:"destinations"`
	Helpers      []shellFootprintHelper      `json:"helpers,omitempty"`
}

// shellFootprintUninstallResult is the result of shell.footprint.uninstall,
// matching contracts/shell.footprint.uninstall.schema.json exactly. The two
// lists are root-relative paths: a conflict is information the user acts on
// (the file stayed, and why), never an error to swallow.
type shellFootprintUninstallResult struct {
	Removed   []string `json:"removed"`
	Conflicts []string `json:"conflicts"`
}

// shellFootprintHelperUninstallResult is the result of
// shell.footprint.helperUninstall, matching
// contracts/shell.footprint.helperUninstall.schema.json exactly. removed
// reports whether a helper tree existed on the host at all: a host with
// nothing installed uninstalls cleanly — the no-op that succeeds — and a
// user clicking remove twice sees removed=false, never a failure.
type shellFootprintHelperUninstallResult struct {
	Removed bool `json:"removed"`
}

// handleFootprintStatus serves shell.footprint.status: every recorded
// installed fact and every observed helper install, plus which
// destinations a saved connection can remove.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"shell.footprint.status"}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"destinations":[{"identity":"pi@192.168.0.93:22","generation":"v10","path":"~/.nocx","protocolVersion":"1","scriptVersion":"0.6.0","lastObservedAt":"…","removableProfileId":"p_01"}],"helpers":[{"identity":"u@db01:22","fingerprint":"SHA256:…","path":"~/.nocx/helper/1-linux-amd64-…/","hash":"…","installedAt":"…"}]}}
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
	// The helper footprint (D8): recorded installs only — an observation,
	// never a claim about what is on the host right now.
	helpers := make([]shellFootprintHelper, 0)
	if h.helperInstalls != nil {
		removable := h.removableProfiles(ctx)
		for _, in := range h.helperInstalls.All() {
			helpers = append(helpers, shellFootprintHelper{
				Identity:           in.Identity,
				Fingerprint:        in.Fingerprint,
				Path:               in.Path,
				Hash:               in.Hash,
				InstalledAt:        in.InstalledAt,
				RemovableProfileID: removable[in.Identity],
			})
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(shellFootprintStatusResult{
		Destinations: destinations,
		Helpers:      helpers,
	}))
}

// handleFootprintHelperUninstall serves shell.footprint.helperUninstall:
// remove the helper install tree on the host a saved profile connects to
// (remote-helper design D25), then forget the observation so the surface
// stops listing it.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"shell.footprint.helperUninstall","params":{"profileId":"p_01","fingerprint":"SHA256:…","path":"~/.nocx/helper/1-linux-amd64-…/"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"removed":true}}
//
// The order is D25 and it is not negotiable: the closer closes every live
// helper channel on this machine BEFORE the tree is removed — no helper may
// be running out of a directory being deleted. Then the capability dials
// and removes the whole ~/.nocx/helper tree (a no-op that succeeds when
// nothing is installed), and only a removal that succeeded forgets the
// store row — a host that stays reachable never vanishes from the listing
// while its helper is still there. The fingerprint+path pair names the
// observed row, the same key the listing used; profileId is where nocx owns
// credentials, exactly as shell.footprint.uninstall requires.
func (h footprintHandlers) handleFootprintHelperUninstall(ctx context.Context, req jsonrpcRequest) {
	var params struct {
		ProfileID   string `json:"profileId"`
		Fingerprint string `json:"fingerprint"`
		Path        string `json:"path"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ProfileID == "" || params.Fingerprint == "" || params.Path == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "profileId, fingerprint and path are required"})
		return
	}
	if h.resolver == nil || h.helperUninstaller == nil || h.closer == nil {
		// Without the closer the handler cannot prove the close-before-
		// remove order — the whole point of D25 — so it refuses rather
		// than removing a directory a helper may be running out of.
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "helper uninstall is not available"})
		return
	}

	host, cfg, err := h.resolver.Resolve(params.ProfileID)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "unknown profile"})
		return
	}

	// D25 first: every live helper channel on this machine is closed
	// before the install directory is removed.
	h.closer.CloseHelpersFor(params.Fingerprint)

	// The resolved config travels as one ConnectOption, the same shape the
	// discovery scheduler uses: every credential, key and jump hop the
	// profile resolved to reaches the dial exactly as a tab's would.
	opts := []ssh.ConnectOption{func(dst *ssh.ConnectConfig) { *dst = *cfg }}
	removed, err := h.helperUninstaller.UninstallHelper(ctx, host, opts...)
	if err != nil {
		h.log.Warn("shell.footprint.helperUninstall failed", "profileId", params.ProfileID, "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "helper uninstall failed"})
		return
	}
	// The inventory row follows the remote removal: only a removal that
	// succeeded is forgotten. A failure here is surfaced, never silent —
	// the directory is gone but the listing would otherwise advertise an
	// install that no longer exists, and a retry heals it (the remote
	// half is then the idempotent no-op).
	if h.helperInstalls != nil {
		if rerr := h.helperInstalls.Remove(params.Fingerprint, params.Path); rerr != nil {
			h.log.Warn("helper removed but the footprint observation was not cleared",
				"host", host, "error", rerr)
			_ = h.r.TryError(req.ID, RPCError{
				Code:    -32603,
				Message: "the helper was removed from " + host + " but the local footprint could not be updated: " + rerr.Error(),
			})
			return
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(shellFootprintHelperUninstallResult{Removed: removed}))
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
