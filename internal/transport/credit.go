package transport

// Per-connection credit accounting for the control plane (ADR-8eej3e66 §7).
//
// Under ordinary load the bound is on what the client SENDS. The server
// advertises a window to the connection, the renderer holds calls beyond it
// in its own queue, and a peer that sends more than it was told is
// violating the protocol: it is logged, recorded in diagnostics and
// disconnected. There is no capacity error for a well-behaved client,
// because it never sends the call that would earn one.
//
// Two classes sit outside the ordinary window, for the same reason:
//
//   - RESCUE traffic takes no credit at all. cancel, ack, the resolvers, the
//     heartbeat and diagnostics are how a client rescues work that is already
//     waiting; making them queue behind the wait they rescue is the deadlock
//     this exemption exists to remove. The exempt set is the transport's
//     existing ingress-critical set (registration.go), which is where the
//     same property is already enforced for the read loop.
//   - vault.unseal has ONE reserved credit beyond the window, so a window
//     full of calls waiting on an unlock cannot hold up the answer that
//     releases them.
//
// The window is the ordinary lane's capacity with one permit held back for
// the reserved credit. That is what makes the reservation real: if the
// window equalled the lane, a full window of lane-holding calls could still
// starve the unseal at the lane. (A second CONNECTION can still hold lane
// permits this connection's window does not account for; that is the
// server-wide bound, not this per-connection one.)

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/diagnostics"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// reservedCreditMethods is the closed set of methods with a credit outside
// the advertised window, one reservation each. It is deliberately one method:
// the unlock is the only call whose progress other waiting calls depend on.
var reservedCreditMethods = []string{"vault.unseal"}

// DefaultCreditWindow is the migration's starting ordinary window, inherited
// from ADR-0026's eight exactly as ADR-8eej3e66 says the stage adopts it
// ("Eight is inherited from ADR-0026 as the migration's starting point and
// measures nothing about a bulkhead"). It is deliberately NOT the lane's
// capacity: the lane is an execution bound shared by every connection, while
// this window is what ONE connection may have outstanding, and a test that
// sets a small window must still be able to send its second
// request. A test or a later stage names a different value with
// WithCreditWindow.
const DefaultCreditWindow = 8

// creditConfig is the server's one credit declaration: the window every
// connection is told, and the two sets that are not counted against it.
type creditConfig struct {
	window   int
	exempt   map[string]struct{}
	reserved map[string]struct{}
}

func newCreditConfig(window int, exempt map[string]struct{}, reserved []string) creditConfig {
	if window < 1 {
		window = 1
	}
	res := make(map[string]struct{}, len(reserved))
	for _, method := range reserved {
		res[method] = struct{}{}
	}
	return creditConfig{window: window, exempt: exempt, reserved: res}
}

// advertisement is what transport.credit answers. The slices are sorted so
// the same server always says the same thing, in the same order.
func (c creditConfig) advertisement() transportCreditResult {
	window := c.window
	if window < 1 {
		window = 1
	}
	exempt := make([]string, 0, len(c.exempt))
	for method := range c.exempt {
		exempt = append(exempt, method)
	}
	sort.Strings(exempt)
	reserved := make([]string, 0, len(c.reserved))
	for method := range c.reserved {
		reserved = append(reserved, method)
	}
	sort.Strings(reserved)
	return transportCreditResult{Window: window, Exempt: exempt, Reserved: reserved}
}

// transportCreditResult is the wire shape of transport.credit
// (contracts/transport.credit.schema.json).
type transportCreditResult struct {
	Window   int      `json:"window"`
	Exempt   []string `json:"exempt"`
	Reserved []string `json:"reserved"`
}

// creditOverrun is one peer's frame beyond the window it was told.
type creditOverrun struct {
	// Class is "ordinary" or "reserved"; it names which bound the frame
	// took, never the request's data.
	Class    string
	InFlight int
	Window   int
}

// creditWindow is ONE connection's accounting.
type creditWindow struct {
	window   int
	exempt   map[string]struct{}
	reserved map[string]struct{}

	mu               sync.Mutex
	ordinary         int
	reservedInFlight int
	held             map[string][]*creditPermit
}

func (c creditConfig) forConnection() *creditWindow {
	window := c.window
	if window < 1 {
		// A server that was never built with a control plane must not tell
		// every connection it has a window of zero; the schema refuses a
		// window below one, and a zero here would disconnect the first frame
		// of every client.
		window = 1
	}
	return &creditWindow{
		window: window, exempt: c.exempt, reserved: c.reserved,
		held: make(map[string][]*creditPermit),
	}
}

// admit takes one credit for a request, or reports the overrun that makes
// the peer a violator. A nil permit with a nil overrun means the method is
// exempt. The id keys the permit so the response (or a withdrawal) can
// release it without the caller having to carry it there.
func (c *creditWindow) admit(method string, id json.RawMessage) (*creditPermit, *creditOverrun) {
	if c == nil {
		return nil, nil
	}
	if _, ok := c.exempt[method]; ok {
		return nil, nil
	}
	_, reserved := c.reserved[method]

	c.mu.Lock()
	defer c.mu.Unlock()
	if reserved {
		if c.reservedInFlight >= 1 {
			return nil, &creditOverrun{Class: "reserved", InFlight: c.reservedInFlight + 1, Window: 1}
		}
		c.reservedInFlight++
		return c.trackLocked(method, id, "reserved"), nil
	}
	if c.ordinary >= c.window {
		return nil, &creditOverrun{Class: "ordinary", InFlight: c.ordinary + 1, Window: c.window}
	}
	c.ordinary++
	return c.trackLocked(method, id, "ordinary"), nil
}

// trackLocked records one admitted permit under the request id. An id can be
// sent twice (the renderer does not do it; a broken peer might), so the
// permits per id are a list rather than a single slot.
func (c *creditWindow) trackLocked(method string, id json.RawMessage, class string) *creditPermit {
	p := &creditPermit{window: c, class: class}
	key := string(id)
	c.held[key] = append(c.held[key], p)
	return p
}

// releaseByID releases the oldest permit held for a request id. It is the
// response path and the withdrawal path; an unknown id is a normal race and
// does nothing.
func (c *creditWindow) releaseByID(id json.RawMessage) {
	if c == nil {
		return
	}
	key := string(id)
	c.mu.Lock()
	permits := c.held[key]
	var permit *creditPermit
	if len(permits) > 0 {
		permit = permits[0]
		if len(permits) == 1 {
			delete(c.held, key)
		} else {
			c.held[key] = permits[1:]
		}
	}
	c.mu.Unlock()
	permit.Release()
}

func (c *creditWindow) release(class string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if class == "reserved" {
		if c.reservedInFlight > 0 {
			c.reservedInFlight--
		}
		return
	}
	if c.ordinary > 0 {
		c.ordinary--
	}
}

// creditPermit is one held credit. Release is idempotent because the same
// admission has two owners: the response that answers it, and the task that
// runs it (which releases a withdrawn or failed request that never answered).
type creditPermit struct {
	window *creditWindow
	class  string
	once   sync.Once
}

func (p *creditPermit) Release() {
	if p == nil {
		return
	}
	p.once.Do(func() { p.window.release(p.class) })
}

// creditSpec registers transport.credit: the advertisement a connection
// reads before it sends its first ordinary call.
func (s *WSServer) creditSpec() methodSpec {
	return reg(control.ImmediateSubmission{}, catalog.OpTransportCredit, noParams(), func(_ *wsConn, _ *connState, r Responder) handlerFunc {
		return func(_ context.Context, req jsonrpcRequest) {
			_ = r.TryResult(req.ID, mustMarshal(s.credit.advertisement()))
		}
	})
}

// noteCreditOverrun is the ONE place a peer's violation is logged and
// recorded. Both are required by the record: the log is where a shipped
// build's reader finds it, and Copy diagnostics is where the person who
// pasted it gets the same fact. Neither carries request data — method and
// class are server vocabulary, the counts are numbers.
func (s *WSServer) noteCreditOverrun(ctx context.Context, w *wsConn, method string, over *creditOverrun) {
	now := time.Now()
	if s.diagnostics != nil {
		s.diagnostics.RecordCreditOverrun(now, diagnostics.CreditOverrun{
			Conn: w.id, Method: method, Class: over.Class,
			InFlight: over.InFlight, Window: over.Window,
		})
	}
	log.From(ctx).Warn("control credit window exceeded",
		"method", method,
		"class", over.Class,
		"in_flight", over.InFlight,
		"window", over.Window,
		"conn", w.id)
}
