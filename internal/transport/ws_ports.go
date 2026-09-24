package transport

// ports.* — the read path for port discovery and the forwards ledger
// (spec §9, nocx-wzc4.2). The scheduler (internal/discovery) owns the
// cadence — when a probe runs — and the transport owns the wire: one
// ports.status result assembling the discovery state for an authenticated
// target plus every forward the backend currently tracks. tunnel.open and
// tunnel.stop stay the only write paths; the panel drives them directly.
//
// The result shape is declared once in contracts/ports.status.schema.json;
// ports.sample returns the same shape (a retry is a fresh status).

import (
	"encoding/json"
	"os"

	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// iso8601UTC is the wire format of the last-successful-sample time. UTC with
// an explicit offset — the renderer displays it, never subtracts it.
const iso8601UTC = "2006-01-02T15:04:05Z07:00"

// WithDiscoveryScheduler attaches the discovery scheduler, enabling the
// ports.* JSON-RPC methods. When not wired, they return a JSON-RPC error;
// the transport never constructs a detector or acquires a lease itself.
func WithDiscoveryScheduler(s *discovery.Scheduler) WSServerOption {
	return func(ws *WSServer) { ws.discoverySched = s }
}

// ---------------------------------------------------------------------------
// ports.* — JSON-RPC types
// ---------------------------------------------------------------------------

// portsProfileParams is the payload shared by ports.status and ports.sample.
type portsProfileParams struct {
	ProfileID string `json:"profileId"`
}

// portsPauseParams is the payload of ports.pause and ports.visible: the
// user's Pause/Resume control, and the panel watcher's visibility signal.
type portsPauseParams struct {
	ProfileID string `json:"profileId"`
	Paused    bool   `json:"paused"`
	Visible   bool   `json:"visible"`
}

// portsProcess is the three-valued process evidence of one listener (spec
// §5): known | permission-denied | unsupported — never an empty string,
// because "nobody owns it" and "I was not allowed to see" are different
// facts and must render differently.
type portsProcess struct {
	Evidence string `json:"evidence"`
	Name     string `json:"name"`
	PID      int    `json:"pid"`
}

// portsListener is one remote listening TCP port.
type portsListener struct {
	Family  string       `json:"family"`
	Address string       `json:"address"`
	Port    int          `json:"port"`
	Process portsProcess `json:"process"`
}

// portsDiscovery is the discovery half of the status result. The listeners
// slice is never null: no listeners is [], which the schema pins.
type portsDiscovery struct {
	State          string          `json:"state"`
	Listeners      []portsListener `json:"listeners"`
	Probe          string          `json:"probe"`
	ProbesTried    []string        `json:"probesTried"`
	Classification string          `json:"classification"`
	Stderr         string          `json:"stderr"`
	LastSampleAt   *string         `json:"lastSampleAt"`
	Paused         bool            `json:"paused"`
	Visible        bool            `json:"visible"`
	ConnLost       bool            `json:"connLost"`
}

// portsStatusResult is the full status for one profile: the discovery state
// plus every forward the backend tracks for the connection. Forwards include
// stopped-by-transport records (connection loss) — they stay in the ledger
// until stopped or the owning tab closes; the panel merges its own
// user-stopped records on top.
type portsStatusResult struct {
	ProfileID string         `json:"profileId"`
	Host      string         `json:"host"`
	Discovery portsDiscovery `json:"discovery"`
	Forwards  []tunnelRecord `json:"forwards"`
}

// portsDiscoveryFrom projects a scheduler status onto the wire. LastSampleAt
// is ISO-8601 UTC — display-only, so no epoch arithmetic on the renderer.
func portsDiscoveryFrom(st discovery.TargetStatus) portsDiscovery {
	d := portsDiscovery{
		State:          string(st.Sample.State),
		Probe:          st.Sample.Probe,
		ProbesTried:    orEmpty(st.Sample.ProbesTried),
		Classification: st.Sample.Classification,
		Stderr:         st.Sample.Stderr,
		Paused:         st.Paused,
		Visible:        st.Visible,
		ConnLost:       st.ConnLost,
		Listeners:      make([]portsListener, 0, len(st.Sample.Listeners)),
	}
	for _, l := range st.Sample.Listeners {
		d.Listeners = append(d.Listeners, portsListener{
			Family:  string(l.Family),
			Address: l.Address,
			Port:    l.Port,
			Process: portsProcess{
				Evidence: string(l.Process.Evidence),
				Name:     l.Process.Name,
				PID:      l.Process.PID,
			},
		})
	}
	if !st.LastSampleAt.IsZero() {
		t := st.LastSampleAt.UTC().Format(iso8601UTC)
		d.LastSampleAt = &t
	}
	return d
}

// orEmpty keeps a nil slice off the wire as the empty array the schema
// requires — a null where the renderer's type says list has cost this repo a
// defect once already.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// portsHandlers answers the four ports.* methods. It holds the discovery
// scheduler seam, the tunnel ledger (whose forwards the status result
// assembles) and its Responder; nothing else.
type portsHandlers struct {
	sched  *discovery.Scheduler
	ledger *tunnelLedger
	r      Responder
}

// handlePortsMethod dispatches the four ports.* methods. When the scheduler
// is not wired the methods answer -32603, like tunnel.* without a connector.
func (h portsHandlers) handlePortsMethod(req jsonrpcRequest) {
	if h.sched == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Port discovery not available (no discovery scheduler wired)"})
		return
	}
	switch req.Method {
	case "ports.status":
		profileID, ok := portsProfileParam(h.r, req)
		if !ok {
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(h.portsStatus(profileID)))
	case "ports.sample":
		profileID, ok := portsProfileParam(h.r, req)
		if !ok {
			return
		}
		// Retry semantics (spec §4): clear a terminal refusal, sample now.
		h.sched.SampleNow(profileID)
		_ = h.r.TryResult(req.ID, mustMarshal(h.portsStatus(profileID)))
	case "ports.pause":
		var params portsPauseParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ProfileID == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: profileId required"})
			return
		}
		h.sched.SetPaused(params.ProfileID, params.Paused)
		_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
	case "ports.visible":
		var params portsPauseParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ProfileID == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: profileId required"})
			return
		}
		h.sched.SetVisible(params.ProfileID, params.Visible)
		_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
	}
}

func portsProfileParam(wconn Responder, req jsonrpcRequest) (string, bool) {
	var params portsProfileParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ProfileID == "" {
		_ = wconn.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: profileId required"})
		return "", false
	}
	return params.ProfileID, true
}

// portsStatus assembles the result: the scheduler's status for the profile
// plus the transport's forward ledger, in stable id order (map iteration is
// random; the renderer must not see a reordered list per call).
func (h portsHandlers) portsStatus(profileID string) portsStatusResult {
	st := h.sched.Status(profileID)
	return portsStatusResult{
		ProfileID: profileID,
		Host:      st.Host,
		Discovery: portsDiscoveryFrom(st),
		Forwards:  h.ledger.forwardRecords(),
	}
}

// ---------------------------------------------------------------------------
// Cadence hooks — the transport tells the scheduler what it sees
// ---------------------------------------------------------------------------

// discoveryUp is called from handleOpen once a remote session on a saved
// profile has opened: the target is up, schedule the settle sample (spec
// §4). The resolved config rides as the lease-keying option, exactly as the
// tunnel path hands it to its connector (AD-4). Ad-hoc hosts (no profile)
// get no discovery — consent is per-profile (spec §6).
func (s *WSServer) discoveryUp(profileID, host string, cfg *ssh.ConnectConfig) {
	if s.discoverySched == nil || profileID == "" || cfg == nil {
		return
	}
	// The lease-keying option is the whole resolved config, exactly as the
	// tunnel path hands it to its connector (AD-4). The host is the
	// scheduler's per-target display material; the ssh.ConnectConfig itself
	// carries no host field, so the caller (handleOpen) passes the resolved
	// session host.
	opts := []ssh.ConnectOption{func(dst *ssh.ConnectConfig) { *dst = *cfg }}
	s.discoverySched.ConnectionUp(profileID, host, opts...)
}

// discoveryUpLocal schedules discovery for the machine the app runs on,
// keyed by the reserved discovery.LocalTargetID — the wire identity for a
// local tab, which has no profile. The machine is its own consent (there is
// no remote host to ask); the cadence — settle, prompt debounce, hidden-tab
// pause, user Pause — governs it exactly like a profile target, and the
// target is torn down when the last local tab closes. Host is the machine's
// hostname, display material only.
func (s *WSServer) discoveryUpLocal() {
	if s.discoverySched == nil {
		return
	}
	host, _ := os.Hostname()
	s.discoverySched.ConnectionUp(discovery.LocalTargetID, host)
}

// discoverySessionClosed is called after a session tears down. When the
// closed session was the last one on its profile, the target is forgotten
// and its lease released — a background poll never outlives its consumer.
func (s *WSServer) discoverySessionClosed(sess session.Session) {
	if s.discoverySched == nil || sess == nil {
		return
	}
	if pid := sess.ProfileID(); pid != "" {
		for _, other := range s.registry.List() {
			if other.ProfileID() == pid {
				return // still live
			}
		}
		s.discoverySched.ConnectionDown(pid)
		return
	}
	if sess.Kind() == session.KindLocal {
		for _, other := range s.registry.List() {
			if other.Kind() == session.KindLocal {
				return // still live
			}
		}
		// The last local tab closed: the local target is forgotten — no
		// background poll outlives its consumer, local or remote.
		s.discoverySched.ConnectionDown(discovery.LocalTargetID)
	}
}
