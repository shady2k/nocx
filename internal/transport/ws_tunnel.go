package transport

// tunnel.* — local port forwarding (spec §7, nocx-8gix). The transport owns
// the RPC surface and the tab-scoped lifetime; the forwarding itself lives in
// internal/tunnel, and the SSH connection lease in internal/ssh.

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/tunnel"
)

// WithTunnelConnector attaches the connector that acquires an owned pooled
// SSH connection lease for a forward (spec §7.3). The single implementation
// is *ssh.RealClient, which satisfies tunnel.Connector without an adapter —
// the signatures are identical. When not wired, the tunnel.* methods return
// a JSON-RPC error; the transport never constructs an SSH client itself.
func WithTunnelConnector(c tunnel.Connector) WSServerOption {
	return func(s *WSServer) { s.tunnelConnector = c }
}

// ---------------------------------------------------------------------------
// tunnel.open / tunnel.stop — JSON-RPC types
// ---------------------------------------------------------------------------

// tunnelOpenParams is the payload of the "tunnel.open" RPC call.
type tunnelOpenParams struct {
	ProfileID string `json:"profileId"`
	// Host is the local bind host; empty means the backend default
	// 127.0.0.1 (spec §7.1: never 0.0.0.0 by default).
	Host string `json:"host,omitempty"`
	// Port is the local bind port; 0 means allocate, and the result
	// reports the OS-assigned port, never 0.
	Port int `json:"port,omitempty"`
	// Destination is the remote target, host:port, dialed over the SSH
	// connection.
	Destination string `json:"destination"`
	// Scope is the owner label the renderer attaches (tab or session id).
	Scope string `json:"scope,omitempty"`
}

// tunnelStopParams is the payload of the "tunnel.stop" RPC call: the id from
// a tunnel.open result.
type tunnelStopParams struct {
	ID string `json:"id"`
}

// tunnelBind is an address:port pair on the wire — requested or actual.
type tunnelBind struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// tunnelRecord is the full forward record the spec §7 names: direction,
// requested and actual bind, destination, scope/owner, lifecycle state, stop
// reason, error — plus the success-time bind caveat only remote (-R)
// forwards carry. Shared by tunnel.open and tunnel.stop results.
type tunnelRecord struct {
	ID            string     `json:"id"`
	Direction     string     `json:"direction"`
	RequestedBind tunnelBind `json:"requestedBind"`
	ActualBind    tunnelBind `json:"actualBind"`
	Destination   string     `json:"destination"`
	Scope         string     `json:"scope"`
	Caveat        string     `json:"caveat"`
	State         string     `json:"state"`
	StopReason    *string    `json:"stopReason"`
	Error         *string    `json:"error"`
}

// tunnelRecordFrom maps a tunnel.Tunnel onto the wire record. The bind host
// is always present — tunnel.New defaults an empty one to 127.0.0.1. Actual
// is the OS-bound address: a requested port 0 is reported as the allocated
// port, never as 0. stopReason and error are null while the tunnel runs.
func tunnelRecordFrom(t *tunnel.Tunnel) tunnelRecord {
	rec := tunnelRecord{
		ID:            t.ID,
		Direction:     string(t.Direction),
		RequestedBind: tunnelBind{Host: t.Bind.Host, Port: t.Bind.Port},
		ActualBind:    tunnelBind{Host: t.Actual().Host, Port: t.Actual().Port},
		Destination:   t.Destination,
		Scope:         t.Scope,
		Caveat:        t.Caveat(),
		State:         string(t.State()),
	}
	if t.State() == tunnel.StateStopped {
		reason := string(t.StopReason())
		rec.StopReason = &reason
		if err := t.Err(); err != nil {
			msg := err.Error()
			rec.Error = &msg
		}
	}
	return rec
}

// ---------------------------------------------------------------------------
// tunnelHandlers answers tunnel.open and tunnel.stop. It holds the resolver
// holder, the connector and the tunnel ledger — the transport-owned forward
// registry (spec §7.3) — and its Responder; nothing else.
type tunnelHandlers struct {
	resolver  *resolverHolder
	connector tunnel.Connector
	ledger    *tunnelLedger
	r         Responder
}

// handleTunnelOpen establishes one local forward and reports the record.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"tunnel.open","params":{"profileId":"ssh:p1:1","port":0,"destination":"db.internal:5432","scope":"tab:1"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"id":"ab12…","direction":"local","requestedBind":{"host":"127.0.0.1","port":0},"actualBind":{"host":"127.0.0.1","port":43210},"destination":"db.internal:5432","scope":"tab:1","state":"running","stopReason":null,"error":null}}
//
// The bind happens before this returns (spec §7.1): EADDRINUSE, an invalid
// address and a permission error are synchronous, user-visible failures.
// wconn is the *wsConn of the calling connection: it is the forward's owner
// (the owner-map key, spec §7.3), passed per call by the build closure.
func (h tunnelHandlers) handleTunnelOpen(ctx context.Context, wconn *wsConn, r Responder, req jsonrpcRequest) {
	var params tunnelOpenParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if params.ProfileID == "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: profileId required"})
		return
	}
	if params.Destination == "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: destination required"})
		return
	}
	if params.Port < 0 {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: port must not be negative"})
		return
	}
	if h.connector == nil {
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: "Forwarding not available (no tunnel connector wired)"})
		return
	}
	if _, ok := h.resolver.get(); !ok {
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: "Forwarding not available (no profile resolver wired)"})
		return
	}

	host, cfg, err := h.resolver.Resolve(params.ProfileID)
	if err != nil {
		// Resolving reads the stored secret, so a sealed vault surfaces
		// here — the renderer needs the reason to offer the unlock prompt.
		_ = r.TryError(req.ID, rpcErrorFor(-32603, "Resolve failed: ", err))
		return
	}

	t, err := tunnel.New(tunnel.Spec{
		Direction:   tunnel.DirectionLocal,
		Bind:        tunnel.Bind{Host: params.Host, Port: params.Port},
		Destination: params.Destination,
		Scope:       params.Scope,
		Provenance:  tunnel.ProvenanceManual,
	}, h.connector)
	if err != nil {
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	// The forward's connection is the profile's own: the WHOLE resolved
	// config — credentials, jump route, authorized endpoints — rides one
	// option that copies it into the connector's ConnectConfig, so the
	// forward is authorized and pool-keyed exactly like a tab (AD-4). It
	// acquires its OWN pooled reference (spec §7.3): closing the tab that
	// opened it can never kill another tab's forward on the same
	// connection.
	opts := []ssh.ConnectOption{func(dst *ssh.ConnectConfig) { *dst = *cfg }}
	if err := t.Start(ctx, host, opts...); err != nil {
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	h.ledger.track(wconn, t)
	_ = r.TryResult(req.ID, mustMarshal(tunnelRecordFrom(t)))
}

// handleTunnelStop stops one forward by its backend id and reports the
// stopped record. An id that does not exist is an error, not a silent
// success.
//
//	--> {"jsonrpc":"2.0","id":2,"method":"tunnel.stop","params":{"id":"ab12…"}}
//	<-- {"jsonrpc":"2.0","id":2,"result":{"id":"ab12…","direction":"local",…,"state":"stopped","stopReason":"user","error":null}}
func (h tunnelHandlers) handleTunnelStop(req jsonrpcRequest) {
	var params tunnelStopParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if params.ID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: id required"})
		return
	}
	if h.connector == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Forwarding not available (no tunnel connector wired)"})
		return
	}

	t := h.ledger.stopByID(params.ID)
	if t == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "tunnel not found: " + params.ID})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(tunnelRecordFrom(t)))
}

// ---------------------------------------------------------------------------
// Ledger (spec §7.3)
// ---------------------------------------------------------------------------

// tunnelLedger is the transport-owned forward ledger, as a narrow seam: the
// mutex and the two maps it guards. It points at the WSServer's own ledger
// state — constructed once per server in seamSpecs — so tunnel.open,
// tunnel.stop and ports.status share the exact maps that tab teardown and
// stored-forward replay use: one ledger, several narrow holders.
type tunnelLedger struct {
	mu      *sync.Mutex
	tunnels *map[string]*tunnel.Tunnel
	owners  *map[*wsConn]map[string]struct{}
}

// track registers a tunnel with its owner connection, so tab teardown stops
// exactly the tunnels that tab opened — and no one else's.
func (l *tunnelLedger) track(wc *wsConn, t *tunnel.Tunnel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	(*l.tunnels)[t.ID] = t
	owned := (*l.owners)[wc]
	if owned == nil {
		owned = make(map[string]struct{})
		(*l.owners)[wc] = owned
	}
	owned[t.ID] = struct{}{}
}

// stopByID stops the tunnel and forgets it everywhere. Returns nil for an
// unknown id. Shared by tunnel.stop and tab teardown.
func (l *tunnelLedger) stopByID(id string) *tunnel.Tunnel {
	l.mu.Lock()
	t := (*l.tunnels)[id]
	delete(*l.tunnels, id)
	for wc, owned := range *l.owners {
		delete(owned, id)
		if len(owned) == 0 {
			delete(*l.owners, wc)
		}
	}
	l.mu.Unlock()
	if t != nil {
		t.Stop()
	}
	return t
}

// stopOwner stops every tunnel the connection opened. Called on disconnect:
// closing the creating tab tears down its forwards even though the shared
// connection survives (spec §7.3). Each forward holds its OWN pooled
// reference, so this never touches another tab's forward.
func (l *tunnelLedger) stopOwner(wc *wsConn) {
	l.mu.Lock()
	owned := (*l.owners)[wc]
	delete(*l.owners, wc)
	l.mu.Unlock()
	for id := range owned {
		l.stopByID(id)
	}
}

// forwardRecords snapshots the tracked forwards. User-stopped records leave
// the ledger at stop time; records stopped by transport death stay until
// stopped or their owning tab closes, so the panel sees the loss.
func (l *tunnelLedger) forwardRecords() []tunnelRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	recs := make([]tunnelRecord, 0, len(*l.tunnels))
	for _, t := range *l.tunnels {
		recs = append(recs, tunnelRecordFrom(t))
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	return recs
}

// stopOwnerTunnels stops every tunnel the connection opened. Called on
// disconnect (ws.go): the WSServer's ledger is the same state the handlers'
// ledger points at, so this routes through a ledger view of it.
func (s *WSServer) stopOwnerTunnels(wc *wsConn) {
	led := &tunnelLedger{mu: &s.tunnelMu, tunnels: &s.tunnels, owners: &s.ownerTunnels}
	led.stopOwner(wc)
}
