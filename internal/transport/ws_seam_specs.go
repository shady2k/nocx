package transport

// seamSpecs — the seam-backed control methods as constructed types
// (migration map, "Seam handlers"): connections.test, connections.trustHostKey,
// dialog.openFile/openDirectory, sshConfig.aliases/path, sessions.status, fs.complete,
// tunnel.open/stop, ports.status/sample/pause/visible and shell.openUrl. Each
// handler holds only its seams — the resolver holder, prober, dialog service
// holder, tunnel ledger, discovery scheduler, url opener holder — and its
// Responder; never the *WSServer, so a handler cannot reach a store it was
// not constructed with. The seams are built here from the transport's fields,
// once per server, and shared across the methods of their domain.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

func (s *WSServer) seamSpecs(lane control.Admission, sessionGate control.Admission) []methodSpec {
	// Seams shared across the methods of their domain. The holders point at
	// the WSServer's own fields: the dialog and url services are assigned
	// post-construction (SetDialogService / SetUrlOpener) while handlers may
	// be reading them, and the tunnel ledger is the same maps that tab
	// teardown and stored-forward replay use — one state, several narrow
	// holders.
	dialog := &dialogServiceHolder{mu: &s.dialogMu, svc: &s.dialogService}
	opener := &urlOpenerHolder{mu: &s.urlMu, svc: &s.urlOpener}
	ledger := &tunnelLedger{mu: &s.tunnelMu, tunnels: &s.tunnels, owners: &s.ownerTunnels}

	// sessions.status is the one capability-gated method here: a whole-domain
	// SessionOperation over the session gate (migration map).
	sessionOp := capability.NewSessionOperation(sessionGate, lane, s.registry, s.profileUsage)
	statusSub := s.operationQueue("sessions-status")

	return []methodSpec{
		// connections.test owns its own admission (probe capacity-one
		// composed with the lane, wrapped in the inflight set) — the
		// registration IS that submission, so the probe acquires the lane
		// exactly once.
		regResponder(s.probeSub, "connections.test", params(validateConnectionsTestRaw), func(r Responder) handlerFunc {
			h := probeHandlers{
				resolver:         s.resolver,
				prober:           s.prober,
				probeResultStore: s.probeResultStore,
				r:                r,
			}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleConnectionsTest(ctx, req) }
		}),
		regResponder(s.lane, "connections.trustHostKey", params(validateTrustHostKeyRaw), func(r Responder) handlerFunc {
			h := trustHostKeyHandlers{truster: s.hostKeyTruster, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleConnectionsTrustHostKey(ctx, req) }
		}),
		// The dialog methods run under a bounded queue submission wrapped
		// in the inflight set; the native-picker capability itself is
		// dialogAdmit, a capacity-one WAITING gate the handler acquires on
		// the task goroutine (ws.go says why it may not be a submission's).
		// All three own the SAME gate: the native dialog is one capability,
		// so no picker can open over an outstanding one, whichever method
		// asked for it.
		regResponder(s.dialogSub, "dialog.openFile", noParams(), func(r Responder) handlerFunc {
			h := dialogHandlers{dialog: dialog, admit: s.dialogAdmit, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleDialogOpenFile(ctx, req) }
		}),
		regResponder(s.dialogSub, "dialog.openDirectory", noParams(), func(r Responder) handlerFunc {
			h := dialogHandlers{dialog: dialog, admit: s.dialogAdmit, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleDialogOpenDirectory(ctx, req) }
		}),
		// dialog.openFileForUpload rides the SAME capacity-one picker
		// gate: it opens the same native picker, and two pickers must
		// never stack whichever method asked for them.
		regResponder(s.dialogSub, "dialog.openFileForUpload", noParams(), func(r Responder) handlerFunc {
			h := dialogHandlers{dialog: dialog, admit: s.dialogAdmit, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleDialogOpenFileForUpload(ctx, req) }
		}),
		regResponder(s.lane, "sshConfig.aliases", noParams(), func(r Responder) handlerFunc {
			h := sshConfigHandlers{resolver: s.sshConfigResolver, path: s.sshConfigPath, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSSHConfigAliases(ctx, req) }
		}),
		regResponder(s.lane, "sshConfig.path", noParams(), func(r Responder) handlerFunc {
			h := sshConfigHandlers{resolver: s.sshConfigResolver, path: s.sshConfigPath, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSSHConfigPath(ctx, req) }
		}),
		regResponder(statusSub, "sessions.status", params(validateSessionsStatusRaw), func(r Responder) handlerFunc {
			h := sessionsStatusHandlers{op: sessionOp, log: s.log, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSessionsStatus(ctx, req) }
		}),
		regResponder(s.lane, "fs.complete", params(validateFsCompleteRaw), func(r Responder) handlerFunc {
			h := fsCompleteHandlers{r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleFsComplete(req) }
		}),
		// tunnel.open needs the connection as the owner-map key (spec §7.3):
		// the forward's owner is the tab that opened it, so the handler
		// receives the *wsConn per call.
		reg(s.lane, "tunnel.open", params(validateTunnelOpenRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := tunnelHandlers{resolver: s.resolver, connector: s.tunnelConnector, ledger: ledger, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleTunnelOpen(ctx, w, r, req) }
		}),
		regResponder(s.lane, "tunnel.stop", params(validateTunnelStopRaw), func(r Responder) handlerFunc {
			h := tunnelHandlers{resolver: s.resolver, connector: s.tunnelConnector, ledger: ledger, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleTunnelStop(req) }
		}),
		regResponder(s.lane, "ports.status", params(validatePortsProfileRaw), func(r Responder) handlerFunc {
			h := portsHandlers{sched: s.discoverySched, ledger: ledger, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePortsMethod(req) }
		}),
		regResponder(s.lane, "ports.sample", params(validatePortsProfileRaw), func(r Responder) handlerFunc {
			h := portsHandlers{sched: s.discoverySched, ledger: ledger, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePortsMethod(req) }
		}),
		regResponder(s.lane, "ports.pause", params(validatePortsPauseRaw), func(r Responder) handlerFunc {
			h := portsHandlers{sched: s.discoverySched, ledger: ledger, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePortsMethod(req) }
		}),
		regResponder(s.lane, "ports.visible", params(validatePortsPauseRaw), func(r Responder) handlerFunc {
			h := portsHandlers{sched: s.discoverySched, ledger: ledger, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePortsMethod(req) }
		}),
		regResponder(s.lane, "shell.openUrl", params(validateShellOpenUrlRaw), func(r Responder) handlerFunc {
			h := openUrlHandlers{opener: opener, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleShellOpenUrl(ctx, req) }
		}),
	}
}

// ── seam ingress bounds ────────────────────────────────────────────────────
//
// Every one of these params is renderer-supplied and reaches something real:
// a profile store key, a known_hosts file, a filesystem path, a bind or dial
// address, the OS url opener. The bounds below are the ones this repo did not
// already own; where a rule exists elsewhere (maxIDRunes for renderer-
// supplied ids, maxCwdRunes for renderer-supplied cwd/paths, the URL rule
// shell.openUrl's handler already enforced), the validator calls it instead
// of copying it.

const (
	// maxHostKeyRunes bounds the base64 host-key blob echoed back from a
	// connections.test result. An RSA-4096 key is roughly 740 bytes of
	// base64; generous — and still a bound.
	maxHostKeyRunes = 8_000
	// maxStatusProfileIDs bounds how many profile ids one sessions.status
	// call may name. The frontend renders roughly 40 rows; 512 is far above
	// that and still bounds a call that iterates the list server-side.
	maxStatusProfileIDs = 512
	// maxOpenURLRunes bounds the shell.openUrl target. Wire-cost bound, the
	// same order as endpoints.probe's URL bound.
	maxOpenURLRunes = 2_000
)

// validateStringBound applies the two universal string rules: a rune ceiling
// and no control characters. A control character in a value that becomes a
// path, a header, a file line or an argv element is never legitimate
// (hasControlChars is the assistant surface's rule, shared here).
func validateStringBound(field, value string, max int) string {
	if utf8.RuneCountInString(value) > max {
		return fmt.Sprintf("%s exceeds %d characters", field, max)
	}
	if hasControlChars(value) {
		return field + " must not contain control characters"
	}
	return ""
}

// validateProfileID checks the one renderer-supplied profile id shape every
// profile-id-taking seam method shares: required, bounded (maxIDRunes — the
// agent surface's id bound), control-free. The stored id is a store key and
// a resolver input; a control character in it is a hostile or broken caller.
func validateProfileID(id string) string {
	if id == "" {
		return "profileId is required"
	}
	if utf8.RuneCountInString(id) > maxIDRunes {
		return fmt.Sprintf("profileId exceeds %d characters", maxIDRunes)
	}
	if hasControlChars(id) {
		return "profileId must not contain control characters"
	}
	return ""
}

// validateSessionIDShape reports whether s is a server-minted session or
// tunnel id: 32 lowercase hex chars. The shape's owner is session.IDToBytes
// (session.NewID and tunnel.newID both mint it — tunnel.go says so) — call
// it rather than copying the hex check.
func validateSessionIDShape(s string) string {
	if _, err := session.IDToBytes(session.ID(s)); err != nil {
		return "must be a 32-character hex id"
	}
	return ""
}

// validateConnectionsTestRaw is the registered validator for
// connections.test: the profileId names the record the probe resolves, so a
// missing or hostile id is refused before the resolver is touched.
func validateConnectionsTestRaw(raw json.RawMessage) string {
	var p connectionsTestParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	return validateProfileID(p.ProfileID)
}

// validateTrustHostKeyRaw is the registered validator for
// connections.trustHostKey: host and key are echoed verbatim from a
// connections.test result and become a known_hosts line — a file write, so
// control characters are refused, and the key must actually be the base64
// the truster decodes (the handler's own decode, moved earlier: an
// un-decodable key is a params shape error, not a trust failure).
func validateTrustHostKeyRaw(raw json.RawMessage) string {
	var p connectionsTrustHostKeyParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.Host == "" {
		return "host is required"
	}
	if p.Key == "" {
		return "key is required"
	}
	if msg := validateStringBound("host", p.Host, maxHostRunes); msg != "" {
		return msg
	}
	if msg := validateStringBound("key", p.Key, maxHostKeyRunes); msg != "" {
		return msg
	}
	if _, err := base64.StdEncoding.DecodeString(p.Key); err != nil {
		return "key must be base64"
	}
	return ""
}

// validateSessionsStatusRaw is the registered validator for sessions.status:
// profileIds must be present (an empty list is ordinary — the frontend can
// have zero rows), bounded in count and in each entry, and control-free.
func validateSessionsStatusRaw(raw json.RawMessage) string {
	var p struct {
		ProfileIDs *[]string `json:"profileIds"`
	}
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.ProfileIDs == nil {
		return "profileIds is required"
	}
	if len(*p.ProfileIDs) > maxStatusProfileIDs {
		return fmt.Sprintf("profileIds exceeds %d entries", maxStatusProfileIDs)
	}
	for _, id := range *p.ProfileIDs {
		if utf8.RuneCountInString(id) > maxIDRunes {
			return fmt.Sprintf("profileIds entry exceeds %d characters", maxIDRunes)
		}
		if hasControlChars(id) {
			return "profileIds entries must not contain control characters"
		}
	}
	return ""
}

// validateFsCompleteRaw is the registered validator for fs.complete: text is
// required (the partial path being completed) and both text and cwd are
// filesystem paths, held to the agent surface's path bound and control-free.
// limit is left to the handler, which clamps it to 1..200 — any int is a
// legitimate request, and refusing one the handler accepts would be a
// behaviour change.
func validateFsCompleteRaw(raw json.RawMessage) string {
	var p fsCompleteParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.Text == "" {
		return "text is required"
	}
	if msg := validateStringBound("text", p.Text, maxCwdRunes); msg != "" {
		return msg
	}
	if msg := validateStringBound("cwd", p.Cwd, maxCwdRunes); msg != "" {
		return msg
	}
	return ""
}

// validateTunnelOpenRaw is the registered validator for tunnel.open: the
// profileId names the record the forward resolves, port is the local bind
// (0 allocates, and nothing above 65535 can ever bind), and the destination
// is dialed over SSH — it must be a real host:port, which is exactly the
// shape tunnel.New already enforces at start (moved earlier, where a refusal
// is -32602 instead of a dial failure).
func validateTunnelOpenRaw(raw json.RawMessage) string {
	var p tunnelOpenParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if msg := validateProfileID(p.ProfileID); msg != "" {
		return msg
	}
	if msg := validateStringBound("host", p.Host, maxHostRunes); msg != "" {
		return msg
	}
	if p.Port < 0 || p.Port > 65535 {
		return "port must be between 0 and 65535"
	}
	if p.Destination == "" {
		return "destination is required"
	}
	if msg := validateStringBound("destination", p.Destination, maxHostRunes); msg != "" {
		return msg
	}
	// net.SplitHostPort only splits; it does not check that the port is a
	// number in range, so both are checked here — the shape the dial
	// actually accepts.
	_, portStr, err := net.SplitHostPort(p.Destination)
	if err != nil {
		return "destination must be host:port"
	}
	dstPort, err := strconv.Atoi(portStr)
	if err != nil {
		return "destination must be host:port"
	}
	if dstPort < 1 || dstPort > 65535 {
		return "destination port must be between 1 and 65535"
	}
	if utf8.RuneCountInString(p.Scope) > maxIDRunes {
		return fmt.Sprintf("scope exceeds %d characters", maxIDRunes)
	}
	return ""
}

// validateTunnelStopRaw is the registered validator for tunnel.stop: the id
// is the backend-minted forward id, the same 32-hex shape session ids have.
func validateTunnelStopRaw(raw json.RawMessage) string {
	var p tunnelStopParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.ID == "" {
		return "id is required"
	}
	if msg := validateSessionIDShape(p.ID); msg != "" {
		return "id " + msg
	}
	return ""
}

// validatePortsProfileRaw is the registered validator for ports.status and
// ports.sample: the profileId keys the scheduler's target. The reserved
// "local" target is a profile id on this surface (frontend/src/ports-client.ts),
// so the bound and control check apply and no id shape does.
func validatePortsProfileRaw(raw json.RawMessage) string {
	var p portsProfileParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	return validateProfileID(p.ProfileID)
}

// validatePortsPauseRaw is the registered validator for ports.pause and
// ports.visible: the profileId keys the scheduler's target; paused/visible
// are JSON booleans, so the decoder enforces their type.
func validatePortsPauseRaw(raw json.RawMessage) string {
	var p portsPauseParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	return validateProfileID(p.ProfileID)
}

// validateShellOpenUrlRaw is the registered validator for shell.openUrl. The
// URL is handed to the OS opener, so the handler's own rule is moved here —
// only http(s) URLs with a host cross into the browser (a scheme the shell
// would happily open is not a URL this panel may ever send a user to) — and
// made complete: a bound, and no control characters in what reaches an
// external program.
func validateShellOpenUrlRaw(raw json.RawMessage) string {
	var p shellOpenUrlParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.URL == "" {
		return "url is required"
	}
	if utf8.RuneCountInString(p.URL) > maxOpenURLRunes {
		return fmt.Sprintf("url exceeds %d characters", maxOpenURLRunes)
	}
	if hasControlChars(p.URL) {
		return "url must not contain control characters"
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return "url must be a valid URL"
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "only http(s) URLs can be opened"
	}
	return ""
}
