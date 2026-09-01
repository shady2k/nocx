// Package session is the helper's PTY-owning service — the name
// internal/helper/host reserved and refused to register until now (D15).
//
// It is what makes the helper the INTEGRATION rather than a script: the helper
// is the shell's PARENT, so nothing has to be inserted into an rc file and
// nothing has to be delivered by SFTP, and the session it owns outlives the
// coordinator that asked for it (level-1 design D1, D3, D10).
//
// # What it owns, and what it must never own
//
// Owns: the PTY and its process group, the bounded output window, the session
// inventory, exit status, and the enforcement of one writer.
//
// Never owns: blocks, the ledger, content.db, UI state, product policy, or a
// human-authored name. Fat infrastructure, thin product — a survival component
// that must stay compatible across generations has no business carrying
// SQLCipher and a vault. On names specifically: the helper may report DERIVED
// diagnostics, because those are facts about a process and the OS is their
// source; it may not persist a name a person typed. A friendly alias is a
// local projection owned by the local server. One owner ever.
package session

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// The refusals this service can answer with. Each is a fact the caller can act
// on: ErrNoSuchSession is the ANSWER that a session does not exist — which is
// what the coordinator's reconciliation turns into the `absent` verdict — and
// it is emphatically not what a failed connection produces, because that is
// `unknown` and never a verdict (level-1 D5).
var (
	ErrNoSuchSession = errors.New("session: no such session in this generation")
	ErrNotAttached   = errors.New("session: subscriber is not attached")
	ErrBadSubscriber = errors.New("session: subscriber id is not 32 hex characters")
	ErrAckAhead      = errors.New("session: ack is ahead of what was produced")
	ErrAckBehind     = errors.New("session: ack is behind the current cursor")
	ErrNoWriter      = errors.New("session: no attachment holds the write capability")
	ErrNotTheWriter  = errors.New("session: this subscriber does not hold the write capability")
	ErrStaleLease    = errors.New("session: the frame carries a stale lease epoch")
	ErrBudget        = errors.New("session: the helper's aggregate window budget is exhausted")
	ErrSpawn         = errors.New("session: the shell could not be started")
	ErrSignal        = errors.New("session: signal is invalid or unavailable")
)

// Limits are the helper's bounds on output windows: D8 asks for all three,
// because raising a default sixteenfold makes simultaneous pressure real, and
// the cost is spent on somebody else's machine.
type Limits struct {
	// DefaultWindowBytes is what a spawn that names no bound gets.
	DefaultWindowBytes int64
	// MinWindowBytes is the floor, and it is strictly and meaningfully above
	// creditLimit rather than above it by one byte: a window that binds before
	// the credit window turns ordinary flow control into data loss.
	MinWindowBytes int64
	// MaxWindowBytes is the ceiling. A floor alone stops one misconfiguration
	// and does nothing about a corrupted or extreme value.
	MaxWindowBytes int64
	// BudgetBytes is the helper-wide aggregate. The worst case on a host is
	// its live session count times the bound, in the helper's memory, on a VM
	// that may be small — so the sum is bounded too, and the eviction rule is
	// stated rather than left implicit: nothing is evicted, the SPAWN is
	// refused. Killing somebody's running shell to make room for a new one is
	// not a memory-management decision the helper is entitled to take.
	BudgetBytes int64
}

// DefaultLimits are D8's numbers: a 4 MiB default raised from the coordinator
// ring's shipped 256 KiB, a floor four times the credit limit, a ceiling that
// bounds one corrupted value, and an aggregate that bounds the sum.
func DefaultLimits() Limits {
	return Limits{
		DefaultWindowBytes: 4 << 20,
		MinWindowBytes:     4 * creditLimit,
		MaxWindowBytes:     64 << 20,
		BudgetBytes:        512 << 20,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.DefaultWindowBytes <= 0 {
		l.DefaultWindowBytes = d.DefaultWindowBytes
	}
	if l.MinWindowBytes <= 0 {
		l.MinWindowBytes = d.MinWindowBytes
	}
	if l.MaxWindowBytes <= 0 {
		l.MaxWindowBytes = d.MaxWindowBytes
	}
	if l.BudgetBytes <= 0 {
		l.BudgetBytes = d.BudgetBytes
	}
	// D8's floor is ENFORCED, not merely documented, and the reason is
	// measurable rather than aesthetic. The per-subscriber pump runs at most
	// creditLimit ahead of the reader's acks, so with a window no larger than
	// that allowance the pump can never fall behind the window's base: the
	// reset path becomes unreachable and the credit accounting silently
	// becomes the only bound. That is a window that looks configured and is
	// not, which is worse than either setting. Two credit windows is the
	// smallest bound that is meaningfully above one.
	if floor := int64(2 * creditLimit); l.MinWindowBytes < floor {
		l.MinWindowBytes = floor
	}
	if l.MaxWindowBytes < l.MinWindowBytes {
		l.MaxWindowBytes = l.MinWindowBytes
	}
	if l.DefaultWindowBytes < l.MinWindowBytes {
		l.DefaultWindowBytes = l.MinWindowBytes
	}
	if l.DefaultWindowBytes > l.MaxWindowBytes {
		l.DefaultWindowBytes = l.MaxWindowBytes
	}
	return l
}

// Options are the service's dependencies, wired at the composition root
// (cmd/nocx-helper). Everything the service reaches outside itself is here:
// the spawner, the OS inspector, the clock and the id source.
type Options struct {
	// Generation is this content-addressed install. Every session this
	// service mints is qualified by it, so a durable handle addresses its
	// generation rather than needing a lookup service (D10).
	Generation proto.GenerationID
	Spawner    Spawner
	// Inspector is optional: nil means this helper offers no OS evidence,
	// which is the honest answer on a platform that has none.
	Inspector Inspector
	Log       *slog.Logger
	Limits    Limits
	// Now and NewID are seams for tests. Production leaves them nil.
	Now   func() time.Time
	NewID func() ([16]byte, error)
}

// Service is the helper's `session` service.
type Service struct {
	generation proto.GenerationID
	spawner    Spawner
	inspector  Inspector
	log        *slog.Logger
	limits     Limits
	now        func() time.Time
	newID      func() ([16]byte, error)

	mu       sync.Mutex
	sessions map[string]*hostSession
	budget   int64
	// sinks are the connections currently bound, and there may be SEVERAL:
	// D12 is same-UID trust, so any nocx under that account may connect, and
	// the helper's accept loop serves them all at once. It is deliberately a
	// field rather than a constructor argument: the service outlives every
	// connection it serves, which is the whole of D1.
	//
	// It is a set rather than one value because the alternative was measured
	// and it is wrong: with "the newest connection wins", a second coordinator
	// binding silently stole the first one's data frames, and the FIRST one's
	// release then found it no longer held the slot and dropped nothing — so a
	// dead connection's write capability was never released and no later
	// coordinator could ever take it.
	sinks map[Sink]struct{}
	// attachSeq mints attachment ids, which are disposable and never reach
	// the ledger (D2) — so a counter is enough and a random id would only
	// suggest otherwise.
	attachSeq uint64
}

// Compile-time proof that this satisfies the host's extension points: a
// service, a coder of its own refusals, and a receiver of data-plane frames.
var (
	_ host.Service      = (*Service)(nil)
	_ host.RefusalCoder = (*Service)(nil)
	_ host.DataPlane    = (*Service)(nil)
)

// New builds the service. It starts nothing: a helper with no sessions holds
// no PTY, and the first spawn is what makes this generation resident.
func New(opts Options) *Service {
	s := &Service{
		generation: opts.Generation,
		spawner:    opts.Spawner,
		inspector:  opts.Inspector,
		log:        opts.Log,
		limits:     opts.Limits.withDefaults(),
		now:        opts.Now,
		newID:      opts.NewID,
		sessions:   make(map[string]*hostSession),
		sinks:      make(map[Sink]struct{}),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newID == nil {
		s.newID = randomID
	}
	return s
}

// Bind adds sink to the connections this service speaks on and returns the
// release that ends it. Releasing drops every attachment made ON THAT
// CONNECTION — an attachment IS a connection and its lease (D2) — and touches
// no session, no window, no process, and no other connection's attachments.
//
// The interval, both ends named: from Bind until the returned release, frames
// produced for a subscriber that attached on that connection go to that sink
// and to nothing else; after it they go nowhere and are not queued for a
// connection that may never come. A coordinator that drained into no consumer
// would be a second window with its own capacity and owner, which is the thing
// this design has one of (D16).
//
// Several connections may be bound at once and none displaces another: the
// helper's accept loop puts one protocol engine on each, and this is the seam
// where "process-scoped registry, connection-scoped engines" is actually
// enforced rather than described.
func (s *Service) Bind(sink Sink) (release func()) {
	s.mu.Lock()
	s.sinks[sink] = struct{}{}
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.sinks, sink)
			live := s.live()
			s.mu.Unlock()
			for _, hs := range live {
				hs.releaseConnection(sink)
			}
		})
	}
}

// Close ends every session this helper holds. It is process shutdown, not a
// caller's request: ending one session deliberately is close-session and is
// nocx-k6p18.7's verb.
func (s *Service) Close() {
	s.mu.Lock()
	live := s.live()
	s.sessions = make(map[string]*hostSession)
	s.budget = 0
	s.sinks = make(map[Sink]struct{})
	s.mu.Unlock()
	for _, hs := range live {
		hs.stop()
	}
}

// WindowBytesInUse is the aggregate this helper has committed. Exported so the
// budget can be asserted on rather than inferred from behaviour.
func (s *Service) WindowBytesInUse() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget
}

// live lists the sessions. The caller must hold s.mu.
func (s *Service) live() []*hostSession {
	out := make([]*hostSession, 0, len(s.sessions))
	for _, hs := range s.sessions {
		out = append(out, hs)
	}
	return out
}

// --- host.Service -----------------------------------------------------------

func (s *Service) Name() string { return proto.ServiceSession }

func (s *Service) Ops() []string {
	return []string{
		proto.OpSpawn, proto.OpSessions, proto.OpAttach, proto.OpAck,
		proto.OpDetach, proto.OpResize, proto.OpCloseSession, proto.OpSignal,
	}
}

func (s *Service) ParamsSchema(op string) *host.Schema {
	switch op {
	case proto.OpSpawn:
		return host.SchemaFor(proto.SpawnParams{})
	case proto.OpSessions:
		return host.SchemaFor(proto.SessionsParams{})
	case proto.OpAttach:
		return host.SchemaFor(proto.AttachParams{})
	case proto.OpAck:
		return host.SchemaFor(proto.AckParams{})
	case proto.OpDetach:
		return host.SchemaFor(proto.DetachParams{})
	case proto.OpResize:
		return host.SchemaFor(proto.ResizeParams{})
	case proto.OpCloseSession:
		return host.SchemaFor(proto.CloseSessionParams{})
	case proto.OpSignal:
		return host.SchemaFor(proto.SignalParams{})
	}
	return nil
}

// RefusesCancel: no operation here refuses cancellation. Every one of them is
// short and none half-applies — the long-running thing is the SESSION, and a
// session is not a request.
func (s *Service) RefusesCancel(string) bool { return false }

// Refusal codes this service's errors for the wire, so the coordinator
// switches on a code rather than on a message. ErrNoSuchSession is the one
// that matters most: the coordinator's reconciliation turns exactly this code
// into the `absent` verdict, and anything it cannot recognise stays `unknown`.
func (s *Service) Refusal(err error) (string, json.RawMessage) {
	switch {
	case errors.Is(err, ErrNoSuchSession):
		return proto.ErrCodeNoSuchSession, nil
	case errors.Is(err, ErrNotAttached), errors.Is(err, ErrBadSubscriber),
		errors.Is(err, ErrAckAhead), errors.Is(err, ErrAckBehind):
		return proto.ErrCodeBadParams, nil
	case errors.Is(err, ErrNoWriter), errors.Is(err, ErrNotTheWriter), errors.Is(err, ErrStaleLease):
		return proto.ErrCodeWriteRefused, nil
	case errors.Is(err, ErrBudget):
		return proto.ErrCodeWindowBudget, nil
	case errors.Is(err, ErrSignal):
		return proto.ErrCodeBadParams, nil
	case errors.Is(err, ErrSpawn):
		return proto.ErrCodeSpawnFailed, nil
	}
	return "", nil
}

func (s *Service) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	switch op {
	case proto.OpSpawn:
		var p proto.SpawnParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return s.spawn(p)
	case proto.OpSessions:
		var p proto.SessionsParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return s.inventory(p), nil
	case proto.OpAttach:
		var p proto.AttachParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return s.attach(ctx, p)
	case proto.OpAck:
		var p proto.AckParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		hs, err := s.find(p.Session)
		if err != nil {
			return nil, err
		}
		sink, _ := host.ConnectionFrom(ctx).(Sink)
		if err := hs.ack(sink, p.Subscriber, p.Offset); err != nil {
			return nil, err
		}
		return proto.AckResult{}, nil
	case proto.OpDetach:
		var p proto.DetachParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		sink, _ := host.ConnectionFrom(ctx).(Sink)
		return s.detach(sink, p), nil
	case proto.OpResize:
		var p proto.ResizeParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		hs, err := s.find(p.Session)
		if err != nil {
			return nil, err
		}
		if err := hs.proc.Resize(ctx, p.Cols, p.Rows, 0, 0); err != nil {
			return nil, err
		}
		return proto.ResizeResult{}, nil
	case proto.OpCloseSession:
		var p proto.CloseSessionParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if err := s.closeSession(p); err != nil {
			return nil, err
		}
		return proto.CloseSessionResult{}, nil
	case proto.OpSignal:
		var p proto.SignalParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if err := s.signal(p); err != nil {
			return nil, err
		}
		return proto.SignalResult{}, nil
	}
	return nil, fmt.Errorf("session: no op %q", op)
}

func decode(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// --- the operations ---------------------------------------------------------

// spawn starts a shell under a new PTY and returns its inventory entry.
//
// The ORDER here is the whole of the partial-failure story, and each step
// names what is true if the next one fails:
//
//  1. clamp the bound and reserve it against the aggregate budget. A refusal
//     here has forked NOTHING — a budget checked after the fork would leave
//     an orphan shell every time it refused.
//  2. mint the id and spawn with it. The id is not visible in the inventory
//     until the PTY exists, but the spawner needs it to activate the
//     in-memory shell integration without an installed script. A spawn or id
//     failure releases the reservation and returns with no entry or process.
//  3. allocate the window, record the launch and register. Only now is the
//     session findable — which is the opening end of the interval "a session
//     is in the inventory from the moment its PTY exists".
//  4. start the output pump and the exit watcher. Both are attached to a
//     session that already exists, so a process that exits between step 3 and
//     step 4 is still observed: the watcher sees an already-closed Done.
func (s *Service) spawn(p proto.SpawnParams) (proto.SpawnResult, error) {
	bound := s.clamp(p.WindowBytes)

	s.mu.Lock()
	if s.budget+bound > s.limits.BudgetBytes {
		s.mu.Unlock()
		return proto.SpawnResult{}, fmt.Errorf("%w: %d bytes committed of %d", ErrBudget, s.budget, s.limits.BudgetBytes)
	}
	s.budget += bound
	s.mu.Unlock()

	cols, rows := p.Cols, p.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	raw, err := s.newID()
	if err != nil {
		s.mu.Lock()
		s.budget -= bound
		s.mu.Unlock()
		return proto.SpawnResult{}, fmt.Errorf("%w: %v", ErrSpawn, err)
	}

	proc, err := s.spawner.Spawn(SpawnRequest{
		SessionID: proto.SessionHex(raw),
		Cwd:       p.Cwd,
		Env:       p.Env,
		Cols:      cols,
		Rows:      rows,
	})
	if err != nil {
		s.mu.Lock()
		s.budget -= bound
		s.mu.Unlock()
		return proto.SpawnResult{}, fmt.Errorf("%w: %v", ErrSpawn, err)
	}

	hs := &hostSession{
		id:          proto.HostSessionID{Generation: s.generation, Session: proto.SessionHex(raw)},
		raw:         raw,
		workspace:   p.Workspace,
		startedAt:   s.now(),
		proc:        proc,
		win:         newWindow(bound),
		log:         s.log,
		subs:        make(map[proto.SubscriberID]*subscriber),
		attachments: make(map[proto.AttachmentID]*attachment),
	}
	hs.launch = proto.LaunchRecord{
		Shell:       proc.Shell(),
		Cwd:         resolvedCwd(p.Cwd, proc),
		Pid:         proc.Pid(),
		Pgid:        processGroup(proc),
		Cols:        cols,
		Rows:        rows,
		WindowBytes: bound,
	}

	s.mu.Lock()

	s.sessions[hs.id.Session] = hs
	s.mu.Unlock()

	go hs.pump()
	go hs.watchExit(s.now, s.notifyExit)

	s.log.Info("session spawned", "session", hs.id.Session, "generation", string(s.generation),
		"shell", hs.launch.Shell, "pid", hs.launch.Pid, "pgid", hs.launch.Pgid, "windowBytes", bound)
	return proto.SpawnResult{Entry: hs.entry(s.inspector)}, nil
}

// closeSession ends the PTY first, then removes its inventory row and releases
// the reserved window budget. The row is present until this operation starts
// and absent after it returns; a disconnect alone never reaches this path.
func (s *Service) closeSession(p proto.CloseSessionParams) error {
	hs, err := s.find(p.Session)
	if err != nil {
		return err
	}
	hs.stop()

	s.mu.Lock()
	if current, ok := s.sessions[p.Session.Session]; ok && current == hs {
		delete(s.sessions, p.Session.Session)
		s.budget -= hs.launch.WindowBytes
	}
	s.mu.Unlock()
	return nil
}

const maxSignal = 64

// signal sends a bounded POSIX signal to the process group recorded at spawn.
// The launch pgid is authoritative: looking up the current foreground group
// could signal an unrelated command after the session changed state.
func (s *Service) signal(p proto.SignalParams) error {
	if p.Signal <= 0 || p.Signal > maxSignal {
		return fmt.Errorf("%w: %d is outside 1..%d", ErrSignal, p.Signal, maxSignal)
	}
	hs, err := s.find(p.Session)
	if err != nil {
		return err
	}
	signaller, ok := hs.proc.(ProcessGroupSignaller)
	if !ok {
		return fmt.Errorf("%w: process groups are unavailable", ErrSignal)
	}
	if err := signaller.SignalProcessGroup(hs.launch.Pgid, syscall.Signal(p.Signal)); err != nil {
		return fmt.Errorf("%w: %v", ErrSignal, err)
	}
	return nil
}

// clamp applies D8's floor and ceiling to what the coordinator asked for, and
// the result is REPORTED in the launch record: a caller whose request was
// clamped must be able to see that it was.
func (s *Service) clamp(want int64) int64 {
	if want <= 0 {
		want = s.limits.DefaultWindowBytes
	}
	if want < s.limits.MinWindowBytes {
		want = s.limits.MinWindowBytes
	}
	if want > s.limits.MaxWindowBytes {
		want = s.limits.MaxWindowBytes
	}
	return want
}

// inventory is D10: the helper holds the PTYs, so it is the only thing that
// can answer. The workspace filter is D15's reservation on the read side and
// is empty in every level-1 call.
func (s *Service) inventory(p proto.SessionsParams) proto.SessionsResult {
	s.mu.Lock()
	live := s.live()
	s.mu.Unlock()

	// Never null: a decoder distinguishing "no sessions" from "no answer"
	// needs the empty inventory to arrive as an empty array.
	out := make([]proto.SessionEntry, 0, len(live))
	for _, hs := range live {
		if p.Workspace != "" && hs.workspace != p.Workspace {
			continue
		}
		out = append(out, hs.entry(s.inspector))
	}
	return proto.SessionsResult{Sessions: out}
}

// attach reads the connection out of the REQUEST rather than out of the
// service, because the service has several and only the request knows which
// one asked. Binding a subscriber's pump to "the current connection" was the
// defect the socket surfaced: with two coordinators connected, the second one
// to bind received the first one's bytes.
func (s *Service) attach(ctx context.Context, p proto.AttachParams) (proto.AttachResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.AttachResult{}, err
	}
	sink, ok := host.ConnectionFrom(ctx).(Sink)
	if !ok || sink == nil {
		return proto.AttachResult{}, ErrNotAttached
	}
	s.mu.Lock()
	_, bound := s.sinks[sink]
	s.mu.Unlock()
	if !bound {
		return proto.AttachResult{}, ErrNotAttached
	}
	return hs.attach(p, sink, s.mintAttachment, s.log)
}

func (s *Service) detach(sink Sink, p proto.DetachParams) proto.DetachResult {
	s.mu.Lock()
	live := s.live()
	s.mu.Unlock()
	for _, hs := range live {
		if released, found := hs.detach(sink, p.Attachment); found {
			return proto.DetachResult{ReleasedWrite: released}
		}
	}
	// An attachment that is already gone is not an error: a detach racing a
	// dropped connection is the ordinary case, and the caller's intent — this
	// attachment is over — is satisfied either way.
	return proto.DetachResult{}
}

// find resolves a durable handle, generation included. A handle minted by
// ANOTHER generation names nothing here: two generations are resident at once
// and each mints its own ids, so serving a foreign handle by ignoring its
// qualification would eventually hand a caller somebody else's PTY.
func (s *Service) find(id proto.HostSessionID) (*hostSession, error) {
	if id.Generation != s.generation {
		return nil, fmt.Errorf("%w: %s is generation %q, this is %q", ErrNoSuchSession, id.Session, id.Generation, s.generation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hs, ok := s.sessions[id.Session]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchSession, id.Session)
	}
	return hs, nil
}

func (s *Service) mintAttachment() proto.AttachmentID {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachSeq++
	return proto.AttachmentID(fmt.Sprintf("att-%d", s.attachSeq))
}

// notifyExit tells EVERY bound connection that a session ended. An exit is a
// fact about a session, not about one attachment, and a coordinator that is
// watching it must hear it whether or not another coordinator is also
// watching. With nobody connected the status is not lost: it is in the entry,
// and the inventory is what a replacing coordinator asks first.
func (s *Service) notifyExit(e proto.SessionExit) {
	s.mu.Lock()
	sinks := make([]Sink, 0, len(s.sinks))
	for sink := range s.sinks {
		sinks = append(sinks, sink)
	}
	s.mu.Unlock()
	for _, sink := range sinks {
		if err := sink.SendNotification(proto.Notification{
			Service: proto.ServiceSession, Event: proto.EventSessionExit, Params: e,
		}); err != nil {
			s.log.Warn("exit notification not delivered", "session", e.Session.Session, "err", err)
		}
	}
}

// SessionData routes one inbound data-plane frame to its session's PTY. It is
// host.DataPlane, and it is the only path by which a byte a person typed
// reaches the shell.
//
// Every refusal here is a DROP with a log line, never a torn-down connection:
// a coordinator holding a handle this generation no longer has is the ordinary
// case across a restart, not an attack, and the frame's own bytes are never
// interpreted on the way past (AD-6).
func (s *Service) SessionData(ctx context.Context, f proto.SessionFrame) {
	hex := proto.SessionHex(f.Session)
	s.mu.Lock()
	hs, ok := s.sessions[hex]
	s.mu.Unlock()
	if !ok {
		s.log.Warn("session data frame dropped: no such session", "session", hex, "bytes", len(f.Payload))
		return
	}
	sink, _ := host.ConnectionFrom(ctx).(Sink)
	if err := hs.write(sink, f); err != nil {
		s.log.Warn("session write refused", "session", hex, "subscriber", proto.SessionHex(f.Subscriber),
			"epoch", uint64(f.Epoch), "bytes", len(f.Payload), "err", err)
	}
}

// resolvedCwd records where the shell actually started. An empty request is
// answered with the ANSWER rather than with the blank the caller sent, because
// a launch record repeating the request would be a record of the request.
func resolvedCwd(requested string, proc Process) string {
	if c, ok := proc.(interface{ Cwd() string }); ok {
		return c.Cwd()
	}
	return requested
}

// processGroup is the group the helper will signal. creack/pty starts the
// shell with Setsid, so the child leads its own group and pgid == pid by
// construction; the syscall is the cross-check and the construction is the
// authority, which is why a failure falls back rather than failing the spawn.
func processGroup(proc Process) int {
	if g, ok := proc.(interface{ ProcessGroup() int }); ok {
		return g.ProcessGroup()
	}
	return proc.Pid()
}

func randomID() ([16]byte, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return b, err
}
