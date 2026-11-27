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
	"context"
	"encoding/json"
	"os"

	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/session"
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

// portsPaneParams is the payload shared by ports.status and ports.sample.
type portsPaneParams struct {
	PaneID string `json:"paneId"`
}

// portsPauseParams is the payload of ports.pause and ports.visible: the
// user's Pause/Resume control, and the panel watcher's visibility signal.
type portsPauseParams struct {
	PaneID  string `json:"paneId"`
	Paused  bool   `json:"paused"`
	Visible bool   `json:"visible"`
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
}

// portsStatusResult is the full status for one pane: the discovery state
// plus every forward the backend tracks for the connection. Forwards include
// stopped-by-transport records (connection loss) — they stay in the ledger
// until stopped or the owning tab closes; the panel merges its own
// user-stopped records on top.
type portsStatusResult struct {
	PaneID    string         `json:"paneId"`
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

type portsErrorData struct {
	Reason string `json:"reason"`
}

const (
	portsUnknownPane       = "unknown-pane"
	portsHelperUnavailable = "helper-unavailable"
)

type portsHandlers struct {
	machine *WSServer
	ledger  *tunnelLedger
	w       *wsConn
	r       Responder
}

func (h portsHandlers) handlePortsMethod(ctx context.Context, req jsonrpcRequest) {
	sched := h.machine.discoverySched
	if sched == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Port discovery is not available"})
		return
	}
	var p portsPauseParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid ports params"})
		return
	}
	target, reason := h.target(p.PaneID)
	if reason != "" {
		if reason == portsHelperUnavailable && req.Method == "ports.visible" && !p.Visible {
			// Remember a hide while the helper is away, so recovering it
			// does not resume polling a panel that has already closed.
			sched.SetVisible(target, false)
		}
		message := "Unknown pane"
		if reason == portsHelperUnavailable {
			message = "The pane's helper is not serving ports"
		}
		_ = h.r.TryError(req.ID, RPCError{
			Code: -32602, Message: message, Data: portsErrorData{Reason: reason},
		})
		return
	}
	switch req.Method {
	case "ports.sample":
		sched.SampleNow(ctx, target)
		fallthrough
	case "ports.status":
		st := sched.Status(target)
		forwards := []tunnelRecord{}
		if target != discovery.LocalTargetID {
			forwards = h.ledger.forwardRecords()
		}
		_ = h.r.TryResult(req.ID, mustMarshal(portsStatusResult{
			PaneID: p.PaneID, Host: st.Host, Discovery: portsDiscoveryFrom(st), Forwards: forwards,
		}))
	case "ports.pause":
		sched.SetPaused(target, p.Paused)
		_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
	case "ports.visible":
		sched.SetVisible(target, p.Visible)
		_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
	}
}

// target uses the same pane ownership and connection identity as helperRefresh.
// The session supplies the host label and distinguishes the local machine.
func (h portsHandlers) target(paneID string) (id string, reason string) {
	s := h.machine
	s.paneMu.Lock()
	defer s.paneMu.Unlock()
	rec := s.paneRecords[paneID]
	if rec == nil || rec.wconn != h.w {
		return "", portsUnknownPane
	}
	var current session.Session
	for _, sess := range s.registry.List() {
		if sess.PaneID() == paneID && (current == nil || sess.OpenedAt().After(current.OpenedAt())) {
			current = sess
		}
	}
	if current == nil {
		return rec.connectionID, portsHelperUnavailable
	}
	host := current.Host()
	id = rec.connectionID
	if current.Kind() == session.KindLocal {
		id = discovery.LocalTargetID
		host, _ = os.Hostname()
	}
	if !s.discoverySched.Activate(id, host) {
		return id, portsHelperUnavailable
	}
	return id, ""
}

// discoveryUpLocal starts the initial local settle sample at session open.
func (s *WSServer) discoveryUpLocal() {
	if s.discoverySched != nil {
		host, _ := os.Hostname()
		s.discoverySched.Activate(discovery.LocalTargetID, host)
	}
}

func (s *WSServer) discoverySessionClosed(sess session.Session) {
	if s.discoverySched == nil || sess == nil || sess.Kind() != session.KindLocal {
		return
	}
	for _, other := range s.registry.List() {
		if other.Kind() == session.KindLocal {
			return
		}
	}
	s.discoverySched.LocalClosed()
}
