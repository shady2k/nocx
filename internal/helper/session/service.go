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
	"io"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// LifecycleDataPlane receives opaque lifecycle bytes from the coordinator.
// The helper only routes them to the shell-owned carrier.
type LifecycleDataPlane interface {
	LifecycleData(context.Context, proto.SessionFrame)
}

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
	ErrBadKey        = errors.New("session: the idempotency key is longer than the protocol allows")
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
	// keys are the live idempotency claims (L7), one entry per key that a
	// spawn is holding or has resolved. It is guarded by the same mutex as
	// sessions BECAUSE the two are one fact: `keys[k].session != ""` implies
	// `sessions[keys[k].session]` exists, and the only way to keep that true
	// is to change both under one lock.
	keys   map[string]*keyClaim
	budget int64
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
		keys:       make(map[string]*keyClaim),
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
	// Every claim goes with the rows it named: a key outliving the inventory
	// it points into would answer a retry with a session that no longer
	// exists. In-flight claims are released by their own spawn, which either
	// resolves into the new map or removes itself from it.
	s.keys = make(map[string]*keyClaim)
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
		proto.OpAdoptLifecycle,
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
	case proto.OpAdoptLifecycle:
		return host.SchemaFor(proto.AdoptLifecycleParams{})
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
		errors.Is(err, ErrAckAhead), errors.Is(err, ErrAckBehind), errors.Is(err, ErrBadKey):
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
		return s.spawn(ctx, p)
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
		if p.LifecycleOffset != nil {
			if err := hs.ackLifecycle(sink, p.Subscriber, *p.LifecycleOffset); err != nil {
				return nil, err
			}
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
	case proto.OpAdoptLifecycle:
		var p proto.AdoptLifecycleParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		hs, err := s.find(p.Session)
		if err != nil {
			return nil, err
		}
		return proto.AdoptLifecycleResult{Lifecycle: hs.lifecycleLaunch}, nil
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
//
// Before all four, and only when the caller minted one, comes the idempotency
// claim (L7) — see claimKey. It is FIRST because the whole point of it is to
// precede the first irreversible effect, and the budget reservation in step 1
// is already one: a repeat that reserved a second window before discovering it
// was a repeat would refuse itself at the budget on a helper with one session
// left in it.
func (s *Service) spawn(ctx context.Context, p proto.SpawnParams) (proto.SpawnResult, error) {
	if len(p.IdempotencyKey) > proto.MaxIdempotencyKey {
		return proto.SpawnResult{}, fmt.Errorf("%w: %d characters, the limit is %d",
			ErrBadKey, len(p.IdempotencyKey), proto.MaxIdempotencyKey)
	}
	claim, existing, err := s.claimKey(ctx, p.IdempotencyKey)
	if err != nil {
		return proto.SpawnResult{}, err
	}
	if existing != nil {
		return proto.SpawnResult{Entry: existing.entry(s.inspector)}, nil
	}
	// From here the claim is HELD: every return below either resolves it onto
	// a registered session or releases it, and there is no path between them.
	spawned := false
	defer func() {
		if !spawned {
			s.releaseKey(claim)
		}
	}()

	bound := s.clamp(p.WindowBytes)
	reserved := bound
	if p.Lifecycle != nil {
		reserved += bound
	}

	s.mu.Lock()
	if s.budget+reserved > s.limits.BudgetBytes {
		s.mu.Unlock()
		return proto.SpawnResult{}, fmt.Errorf("%w: %d bytes committed of %d", ErrBudget, s.budget, s.limits.BudgetBytes)
	}
	s.budget += reserved
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
		s.budget -= reserved
		s.mu.Unlock()
		return proto.SpawnResult{}, fmt.Errorf("%w: %v", ErrSpawn, err)
	}

	proc, err := s.spawner.Spawn(SpawnRequest{
		SessionID: proto.SessionHex(raw),
		Cwd:       p.Cwd,
		Env:       p.Env,
		Cols:      cols,
		Rows:      rows,
		Lifecycle: p.Lifecycle,
	})
	if err != nil {
		s.mu.Lock()
		s.budget -= reserved
		s.mu.Unlock()
		return proto.SpawnResult{}, fmt.Errorf("%w: %v", ErrSpawn, err)
	}
	lifecycleWin := (*window)(nil)
	lifecycleBudget := int64(0)
	var lifecycleCarrier io.ReadWriteCloser
	if lp, ok := proc.(LifecycleProcess); ok {
		lifecycleCarrier = lp.Lifecycle()
		if lifecycleCarrier != nil {
			lifecycleWin = newWindow(bound)
			lifecycleBudget = bound
		}
	}
	if p.Lifecycle != nil && lifecycleWin == nil {
		s.mu.Lock()
		s.budget -= bound
		s.mu.Unlock()
	}
	hs := &hostSession{
		id:              proto.HostSessionID{Generation: s.generation, Session: proto.SessionHex(raw)},
		raw:             raw,
		workspace:       p.Workspace,
		key:             p.IdempotencyKey,
		startedAt:       s.now(),
		proc:            proc,
		win:             newWindow(bound),
		lifecycleWin:    lifecycleWin,
		lifecycleBudget: lifecycleBudget,
		// Retained for the life of the session, and only when there is a
		// window behind it: a launch kept for a shell that never got a
		// channel would be an identity a replacing coordinator could adopt
		// and then hear nothing on (nocx-k6p18.31).
		lifecycleLaunch: adoptableLaunch(p.Lifecycle, lifecycleWin),
		log:             s.log,
		subs:            make(map[proto.SubscriberID]*subscriber),
		attachments:     make(map[proto.AttachmentID]*attachment),
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
	// The claim resolves onto the row in the SAME critical section that adds
	// it, so no reader can ever see a resolved claim naming a session the
	// inventory does not hold.
	s.resolveKeyLocked(claim, hs.id.Session)
	spawned = true
	s.mu.Unlock()
	go hs.pump()
	if lifecycleCarrier != nil {
		go hs.lifecyclePump(lifecycleCarrier)
	}
	go hs.watchExit(s.now, s.notifyExit)

	s.log.Info("session spawned", "session", hs.id.Session, "generation", string(s.generation),
		"shell", hs.launch.Shell, "pid", hs.launch.Pid, "pgid", hs.launch.Pgid, "windowBytes", bound)
	return proto.SpawnResult{Entry: hs.entry(s.inspector)}, nil
}

// keyClaim is one live idempotency claim (L7). It is a pointer held by the
// spawn that took it, so a claim that has been replaced in the map — released
// and re-taken by a later spawn — can never be resolved or released by the
// first one: identity is the check, never the key string.
type keyClaim struct {
	// key is empty for a caller that minted none. A claim with no key is a
	// claim on nothing: it is never in the map, and resolving or releasing it
	// does nothing, which is what keeps the keyless path free of branches.
	key string
	// done closes when the spawn holding this claim finished, either way. A
	// repeat that arrives mid-flight waits on it rather than forking.
	done chan struct{}
	// session is the row this claim resolved onto, set under s.mu exactly
	// once. Empty means the claim is still in flight.
	session string
}

// claimKey takes the caller's idempotency claim, or answers with the session
// that already holds it.
//
// THE INTERVAL, BOTH ENDS NAMED: a key names its session from BEFORE the fork
// — the claim is registered here, under the same mutex the inventory is
// guarded by, and the spawner is not called until it is held — until the row
// it named LEAVES THE INVENTORY, which is closeSession or Close and nothing
// else. Between those two moments exactly one session in this generation
// answers to that key, and a spawn repeated with it returns that session's
// entry rather than forking a second shell. A spawn that fails releases its
// claim at the failure, so the key is reusable immediately and a pane is never
// wedged by an attempt that produced nothing.
//
// The end is the ROW and not the process, deliberately: a session whose shell
// has exited keeps its row and its exit status until somebody closes it (D5
// makes that row the answer reconciliation reads), so the key must go on
// naming it. Forking a fresh shell over a row whose exit status nobody has
// read yet would be the coordinator losing the thing it came back for.
//
// Three answers: a claim to hold (existing nil), an existing session to return
// (existing non-nil), or the caller's context ending while another spawn holds
// the same key.
func (s *Service) claimKey(ctx context.Context, key string) (claim *keyClaim, existing *hostSession, err error) {
	if key == "" {
		// No claim was minted, so there is nothing to hold and nothing to
		// promise: two keyless spawns are two sessions, as they always were.
		return &keyClaim{done: make(chan struct{})}, nil, nil
	}
	for {
		s.mu.Lock()
		held, ok := s.keys[key]
		if !ok {
			c := &keyClaim{key: key, done: make(chan struct{})}
			s.keys[key] = c
			s.mu.Unlock()
			return c, nil, nil
		}
		if held.session != "" {
			// Resolved. The invariant that both maps are written under this
			// one mutex is what makes the lookup total rather than hopeful.
			hs, live := s.sessions[held.session]
			s.mu.Unlock()
			if live {
				return nil, hs, nil
			}
			// Unreachable while the invariant holds; treated as a released
			// claim rather than trusted, because a claim pointing at nothing
			// must never be an answer.
			s.mu.Lock()
			if s.keys[key] == held {
				delete(s.keys, key)
			}
			s.mu.Unlock()
			continue
		}
		// In flight. Waiting is what makes the claim precede the fork for a
		// CONCURRENT repeat too — the case the whole mechanism exists for,
		// since a coordinator's retry can race its own first attempt over a
		// second connection.
		s.mu.Unlock()
		select {
		case <-held.done:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// resolveKeyLocked binds a held claim to the row that was just registered. The
// caller holds s.mu.
func (s *Service) resolveKeyLocked(c *keyClaim, session string) {
	if c.key != "" {
		c.session = session
	}
	close(c.done)
}

// releaseKey gives a claim back after a spawn that produced no row. It is
// idempotent by construction — the deferred release runs only when the spawn
// did not resolve — and it removes the map entry only if this claim is still
// the one in it.
func (s *Service) releaseKey(c *keyClaim) {
	if c.key != "" {
		s.mu.Lock()
		if s.keys[c.key] == c {
			delete(s.keys, c.key)
		}
		s.mu.Unlock()
	}
	close(c.done)
}

// closeSession ends the PTY first, then removes its inventory row and releases
// the reserved window budget. The row is present until this operation starts
// and absent after it returns; a disconnect alone never reaches this path.
//
// It is also the closing end of the idempotency interval: the claim goes with
// the row, in the same critical section, so the next spawn carrying that key
// forks a new session instead of being handed one that no longer exists.
func (s *Service) closeSession(p proto.CloseSessionParams) error {
	hs, err := s.find(p.Session)
	if err != nil {
		return err
	}
	hs.stop()

	s.mu.Lock()
	if current, ok := s.sessions[p.Session.Session]; ok && current == hs {
		delete(s.sessions, p.Session.Session)
		if hs.key != "" {
			if claim, held := s.keys[hs.key]; held && claim.session == p.Session.Session {
				delete(s.keys, hs.key)
			}
		}
		s.budget -= hs.launch.WindowBytes + hs.lifecycleBudget
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

// LifecycleData routes opaque lifecycle bytes to the shell-owned carrier.
func (s *Service) LifecycleData(ctx context.Context, f proto.SessionFrame) {
	hex := proto.SessionHex(f.Session)
	s.mu.Lock()
	hs, ok := s.sessions[hex]
	s.mu.Unlock()
	if !ok {
		s.log.Warn("lifecycle data frame dropped: no such session", "session", hex, "bytes", len(f.Payload))
		return
	}
	sink, _ := host.ConnectionFrom(ctx).(Sink)
	if err := hs.writeLifecycle(sink, f); err != nil {
		s.log.Warn("lifecycle write refused", "session", hex, "subscriber", proto.SessionHex(f.Subscriber),
			"bytes", len(f.Payload), "err", err)
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

// adoptableLaunch is the guard on what may be handed back for adoption: the
// launch, but only for a session that actually got a lifecycle window.
//
// The two can differ. A spawn may carry a launch and still produce no
// channel — the spawner declines shell integration for a shell it does not
// support, and the process then satisfies no LifecycleProcess. Handing the
// launch back for such a session would let a replacing coordinator adopt a
// domain nothing will ever speak on, and the product would show a pane
// reporting itself integrated while no command in it ever produces a block:
// the exact silent degrade this op exists to end, reintroduced one layer up.
func adoptableLaunch(launch *proto.LifecycleLaunch, win *window) *proto.LifecycleLaunch {
	if launch == nil || win == nil {
		return nil
	}
	return launch
}
