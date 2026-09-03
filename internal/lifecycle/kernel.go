package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Options configure a Kernel. Zero fields fall back to the defaults: the real
// clock, crypto/rand, and DefaultBudgets.
type Options struct {
	Now     func() time.Time
	Rand    io.Reader
	Budgets Budgets
}

// Kernel is the pure authenticated lifecycle model. It is safe for concurrent
// use: each public method serializes on an internal mutex, applies its
// transition, and hands the outbound envelopes it produced (accept,
// refresh_request) back to the caller UNSENT — the caller owns delivery
// ordering (ADR-0024 decision 9: an accept must not reach the shell before
// the renderer has acknowledged the published fact). Deliver sends one
// such envelope to its transport's port, outside the lock. Invalid events
// mutate nothing and return a sentinel error (errors.go).
type Kernel struct {
	mu       sync.Mutex
	now      func() time.Time
	rand     io.Reader
	budgets  Budgets
	registry *DomainRegistry
	lanes    map[LaneID]*laneState
	attempts map[AttemptID]*ExecutionAttempt
	ports    map[TransportID]Port
}

// requestIDRe bounds a shell-minted domain-request id: [A-Za-z0-9._-]{1,64}.
// The id is the shell's nonce, echoed by the grant; the shape keeps it out
// of any quoting the shell side does and makes a malformed id a rejection
// rather than a string compared by accident.
var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// agentNameRe bounds the agent an enrolment names: it is a driver key and a
// word a person reads in a refusal, so [A-Za-z0-9._-] and nothing else.
var agentNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,` + strconv.Itoa(maxAgentNameLen) + `}$`)

// Bounds on a domain request's carried ssh options (nocx-c6z0). They are not
// a shape — an ssh option argument is an arbitrary path, host list or config
// string, and constraining its CHARACTERS would refuse lines OpenSSH accepts.
// Quoting is what makes them safe (the composer shell-quotes each one); these
// two only keep a malformed frame from composing a line no shell will take.
// `ssh -o A -o B -o C -i k -J h -F c` is nine tokens, so 64 is generous, and
// the longest real argument is a path.
const (
	maxDomainRequestOpts   = 64
	maxDomainRequestOptLen = 4096
)

// New builds a Kernel with the given options. A nil Now uses time.Now; a nil
// Rand uses crypto/rand.Reader.
func New(opts Options) *Kernel {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		opts.Rand = rand.Reader
	}
	opts.Budgets = opts.Budgets.withDefaults()
	return &Kernel{
		now:      opts.Now,
		rand:     opts.Rand,
		budgets:  opts.Budgets,
		registry: NewDomainRegistry(),
		lanes:    make(map[LaneID]*laneState),
		attempts: make(map[AttemptID]*ExecutionAttempt),
		ports:    make(map[TransportID]Port),
	}
}

// BindTransport registers a transport and its outbound port. A transport binds
// once and is never unbound.
func (k *Kernel) BindTransport(t TransportID, port Port) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if t == "" || port == nil {
		return ErrInvalidArgument
	}
	if _, ok := k.ports[t]; ok {
		return ErrInvalidArgument
	}
	k.ports[t] = port
	return nil
}

// UnbindTransport removes a transport registration after a post-bind
// construction failure. It is intentionally narrow: callers cannot unbind a
// live transport through normal lifecycle operation.
func (k *Kernel) UnbindTransport(t TransportID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.ports[t]; !ok {
		return ErrUnknownTransport
	}
	delete(k.ports, t)
	return nil
}

// RequestDomain mints a Pending domain bound to the transport: a fresh id, a
// fresh epoch and a fresh capability. The adapter substitutes the capability
// into the integration script and waits for the shell's hello; nothing is
// live until the handshake completes (decision 3). parent must be the top of
// the lane's stack; a top-level domain requires an empty lane.
func (k *Kernel) RequestDomain(lane LaneID, parent *DomainID, t TransportID) (DomainHandle, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.ports[t]; !ok {
		return DomainHandle{}, ErrUnknownTransport
	}
	ls := k.getLane(lane)
	if k.overHandshakeBudget(ls) {
		return DomainHandle{}, ErrHandshakeRateLimited
	}
	var parentID *DomainID
	if parent != nil {
		pd, ok := k.registry.Lookup(*parent)
		if !ok {
			return DomainHandle{}, ErrUnknownParent
		}
		if pd.State != DomainEstablished && pd.State != DomainSuspended {
			return DomainHandle{}, ErrParentNotLive
		}
		if pd.Lane != lane {
			return DomainHandle{}, ErrWrongLane
		}
		if ls.top() != pd.ID {
			return DomainHandle{}, ErrParentNotTop
		}
		pid := pd.ID
		parentID = &pid
	} else if ls.top() != "" {
		return DomainHandle{}, ErrLaneBusy
	}
	// Both secrets are minted BEFORE anything is registered: a mint that
	// cannot get randomness must leave no domain behind, not a domain
	// holding a value nobody can present (nocx-s16k8).
	capability, err := k.randomCapability()
	if err != nil {
		return DomainHandle{}, err
	}
	recovery, err := k.randomFence()
	if err != nil {
		return DomainHandle{}, err
	}
	domHex, err := k.randomHex(8)
	if err != nil {
		return DomainHandle{}, err
	}
	d := &Domain{
		ID:         DomainID("dom-" + domHex),
		Epoch:      k.registry.nextEpoch(),
		Parent:     parentID,
		Lane:       lane,
		Transport:  t,
		State:      DomainPending,
		capability: capability,
		recovery:   recovery,
	}
	k.registry.Register(d)
	// The lane mirrors the domain's recovery fence: the domain dies on
	// transport loss, and the lost lane must still be able to publish the
	// expected recovery fence to the renderer (decision 8). A fresh
	// establishment overwrites it — a new epoch, a new nonce, and a late
	// ack from the old episode can no longer match.
	ls.recoveryNonce = d.recovery
	return DomainHandle{Domain: d.ID, Epoch: d.Epoch, Capability: d.capability, Recovery: d.recovery}, nil
}

// Ingest delivers one authenticated envelope from a transport. Validation
// order (decision 7): protocol version, domain liveness, transport binding,
// epoch, capability — authentication terminates before any domain or sequence
// state is consulted — then lane match, then the monotonic sequence rule, then
// the legal transition. Invalid events mutate nothing, produce no outbound,
// and return a sentinel error.
//
// The outbound envelopes the transition produced (accept, refresh_request)
// are returned UNSENT: the caller owns their delivery ordering (decision 9).
func (k *Kernel) Ingest(t TransportID, env Envelope) ([]Outbound, error) {
	k.mu.Lock()
	out, err := k.ingestLocked(t, env)
	k.mu.Unlock()
	return out, err
}

func (k *Kernel) ingestLocked(t TransportID, env Envelope) ([]Outbound, error) {
	if _, ok := k.ports[t]; !ok {
		return nil, ErrUnknownTransport
	}
	if env.Version != ProtocolVersion {
		return nil, ErrBadVersion
	}
	if !env.Event.validInbound() {
		return nil, ErrIllegalEvent
	}
	d, ok := k.registry.Lookup(env.Domain)
	if !ok {
		k.recordAuthFailure(env.Lane)
		return nil, ErrUnknownDomain
	}
	if d.Transport != t {
		k.recordAuthFailure(d.Lane)
		return nil, ErrWrongTransport
	}
	if d.Epoch != env.Epoch {
		k.recordAuthFailure(d.Lane)
		return nil, ErrStaleEpoch
	}
	if d.capability == (Capability{}) || d.capability != env.Capability {
		// The zero test is not belt-and-braces, it is the difference between
		// authenticating and not: `d.capability != env.Capability` compares
		// EQUAL when both are zero, so a domain holding zeros would
		// authenticate any candidate who sent thirty-two zero bytes.
		//
		// randomCapability no longer produces a zero capability — a failed
		// random read is ErrNoRandomness and no domain is minted at all
		// (nocx-s16k8). This guard is not therefore redundant, and removing
		// it would be a mistake: it is the SECOND of two independent
		// defences, and it is the one that does not care how the zero got
		// there. A future path that writes the field, a deserialized domain,
		// a mint that regresses — a domain with no capability authenticates
		// nobody, whatever put it in that state.
		k.recordAuthFailure(d.Lane)
		return nil, ErrBadCapability
	}
	if d.Lane != env.Lane {
		return nil, ErrWrongLane
	}
	// Authenticated. Sequence state may mutate only after this point.
	if env.Sequence <= d.lastSeq {
		return nil, ErrSequenceReplay
	}
	ls := k.lanes[d.Lane]
	if d.State == DomainDesynchronized {
		k.checkDesyncBudget(d, ls) // time can elapse while nothing is scanned
	}
	var out []Outbound
	var err error
	switch env.Event.Kind {
	case KindHello:
		out, err = k.applyHello(d, ls, env)
	case KindStart:
		out, err = k.applyStart(d, ls, env)
	case KindComplete:
		out, err = k.applyComplete(d, ls, env)
	case KindPromptReady:
		out, err = k.applyPromptReady(d, ls, env)
	case KindSnapshot:
		out, err = k.applySnapshot(d, ls, env)
	case KindDomainSuspended:
		out, err = k.applySuspend(d, ls, env)
	case KindDomainActivated:
		out, err = k.applyActivate(d, ls, env)
	case KindDomainClosed:
		out, err = k.applyClose(d, ls, env)
	case KindDomainRequest:
		out, err = k.applyDomainRequest(d, ls, env)
	case KindAgentEnrol:
		out, err = k.applyAgentEnrol(d, ls, env)
	case KindAgentWithdraw:
		out, err = k.applyAgentWithdraw(d, ls, env)
	case KindAgentReport:
		out, err = k.applyAgentReport(d, ls, env)
	default:
		return nil, ErrIllegalEvent
	}
	if err == nil {
		// The counter advances exactly when an event is accepted — never
		// before authentication, never on a rejected frame (decision 7).
		d.lastSeq = env.Sequence
	}
	return out, err
}

// NotifyGap reports framing corruption on a transport: the adapter scanned
// garbageBytes of garbage spanning garbageFrames frame boundaries. The domain
// enters Desynchronized (or the episode's budgets accumulate), nocx requests an
// authenticated snapshot, and only a snapshot answering it restores authority
// (decision 7). Budget exhaustion revokes the domain.
func (k *Kernel) NotifyGap(t TransportID, dID DomainID, garbageBytes, garbageFrames int) ([]Outbound, error) {
	k.mu.Lock()
	out, err := k.notifyGapLocked(t, dID, garbageBytes, garbageFrames)
	k.mu.Unlock()
	return out, err
}

func (k *Kernel) notifyGapLocked(t TransportID, dID DomainID, garbageBytes, garbageFrames int) ([]Outbound, error) {
	if _, ok := k.ports[t]; !ok {
		return nil, ErrUnknownTransport
	}
	if garbageBytes < 0 || garbageFrames < 0 {
		return nil, ErrInvalidArgument
	}
	d, ok := k.registry.Lookup(dID)
	if !ok {
		return nil, ErrUnknownDomain
	}
	if d.Transport != t {
		return nil, ErrWrongTransport
	}
	ls := k.lanes[d.Lane]
	var out []Outbound
	switch d.State {
	case DomainEstablished:
		if d.desyncEpisodes+1 > k.budgets.MaxDesyncEpisodes {
			k.revoke(d, ls)
			return out, nil
		}
		d.desyncEpisodes++
		d.State = DomainDesynchronized
		d.desyncSince = k.now()
		d.desyncBytes = garbageBytes
		d.desyncFrames = garbageFrames
		ridHex, ridErr := k.randomHex(8)
		if ridErr != nil {
			return nil, ridErr
		}
		rid := RequestID("req-" + ridHex)
		d.refreshRequest = &rid
		if ls.top() == d.ID {
			k.setLifecycle(ls, LifecycleDesynchronized, d.ID, "")
		}
		out = append(out, k.refreshOutbound(d, rid))
	case DomainDesynchronized:
		d.desyncBytes += garbageBytes
		d.desyncFrames += garbageFrames
		k.checkDesyncBudget(d, ls)
	default:
		return nil, ErrDomainNotLive
	}
	return out, nil
}

// TransportLost notifies that a transport died. Every domain bound to it is
// lost (decision 8), the cascade takes their descendants down too, open
// attempts become unknown and never successful, and each affected lane falls
// to LifecycleLost. A new session gets fresh epochs — never resumed ones.
func (k *Kernel) TransportLost(t TransportID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.ports[t]; !ok {
		return ErrUnknownTransport
	}
	dead := make(map[DomainID]bool)
	for _, d := range k.registry.DomainsOnTransport(t) {
		dead[d.ID] = true
	}
	// Cascade: a domain cannot outlive its parent chain.
	changed := true
	for changed {
		changed = false
		for _, d := range k.registry.All() {
			if !dead[d.ID] && d.Parent != nil && dead[*d.Parent] {
				dead[d.ID] = true
				changed = true
			}
		}
	}
	affected := make(map[LaneID]bool)
	for _, d := range k.registry.All() {
		if !dead[d.ID] {
			continue
		}
		d.State = DomainLost
		d.refreshRequest = nil
		k.unknownOpenAttempts(d.ID)
		if ls, ok := k.lanes[d.Lane]; ok {
			affected[d.Lane] = true
			k.removeFromStack(ls, d.ID)
		}
	}
	for lane := range affected {
		if ls, ok := k.lanes[lane]; ok {
			k.setLifecycle(ls, LifecycleLost, "", "")
		}
	}
	return nil
}

// EstablishmentTimeout rolls back a domain whose accept was never delivered
// (decision 9): an accept-pending domain is revoked — Closed, its open
// attempts unknown, the lane back to Native — and the caller publishes the
// resulting safe state. A domain whose accept WAS delivered (the
// acknowledgement raced the timeout) is left live: the shell has its accept
// and must not be revoked under it. A domain that never helloed (Pending) is
// not touched either — the transport's own hello bound (TransportLost) owns
// that case.
func (k *Kernel) EstablishmentTimeout(domain DomainID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	d, ok := k.registry.Lookup(domain)
	if !ok {
		return ErrUnknownDomain
	}
	if d.State != DomainEstablished || !d.acceptPending {
		return nil // delivered (race lost), or not awaiting an accept
	}
	ls := k.lanes[d.Lane]
	k.revoke(d, ls)
	return nil
}

// RecoverLane completes a restoration acknowledgement — decision 8's
// composite ACK, once the renderer has both matched the shell's one-shot
// recovery fence on the pty and applied the conventional presentation. A
// Lost lane falls to Native: the session becomes a usable conventional
// terminal. The domain stays permanently Lost, and any future integration
// is a fresh epoch — never a resumption. Idempotent: an already-Native lane
// is a no-op success. It can never revoke a live domain: a lane with a
// live domain is refused (the ack permits only Lost → Native).
func (k *Kernel) RecoverLane(lane LaneID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	ls, ok := k.lanes[lane]
	if !ok {
		return ErrUnknownLane
	}
	switch ls.lifecycle {
	case LifecycleLost:
		k.setLifecycle(ls, LifecycleNative, "", "")
		return nil
	case LifecycleNative:
		return nil // idempotent: the recovery already landed
	default:
		return ErrNotLost
	}
}

// SubmitAttempt synchronously creates an app-originated attempt — id,
// app-owned command text, cwd, host, start time — before the bytes that could
// cause the shell's own start are written to the pty (decision 5). It requires
// a live, active, non-desynchronized domain at a ready prompt.
func (k *Kernel) SubmitAttempt(domain DomainID, command, cwd, host, submitID string) (ExecutionAttempt, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	d, ok := k.registry.Lookup(domain)
	if !ok {
		return ExecutionAttempt{}, ErrUnknownDomain
	}
	ls := k.lanes[d.Lane]
	if err := k.requireActive(d, ls); err != nil {
		return ExecutionAttempt{}, err
	}
	if ls.lifecycle != LifecyclePromptReady {
		return ExecutionAttempt{}, ErrNotPromptReady
	}
	if len(command) > k.budgets.MaxCommandBytes {
		return ExecutionAttempt{}, ErrOversizeCommand
	}
	if open := k.openAttemptFor(d.ID); open != nil {
		return ExecutionAttempt{}, ErrAttemptOpen
	}
	aid, err := k.newAttemptID()
	if err != nil {
		return ExecutionAttempt{}, err
	}
	att := k.createAttempt(d, aid, OriginApp, false, command, cwd, host, k.now())
	att.SubmitID = submitID
	k.setLifecycle(ls, LifecycleRunning, d.ID, att.ID)
	return *att, nil
}

// AbandonAttempt marks an open attempt unknown — the explicit abandonment path
// (native-mode escape, decision 5). Nothing may mark it successful and nothing
// may assign it an exit code it did not report.
func (k *Kernel) AbandonAttempt(id AttemptID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	att, ok := k.attempts[id]
	if !ok || att.State != AttemptOpen {
		return ErrAttemptNotOpen
	}
	att.State = AttemptUnknown
	return nil
}

// State returns the read model of one lane.
func (k *Kernel) State(lane LaneID) (LaneSnapshot, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	ls, ok := k.lanes[lane]
	if !ok {
		return LaneSnapshot{}, ErrUnknownLane
	}
	snap := LaneSnapshot{
		Lane:          lane,
		Lifecycle:     ls.lifecycle,
		Domain:        ls.lifecycleDomain,
		Attempt:       ls.lifecycleAttempt,
		Stack:         append([]DomainID(nil), ls.stack...),
		RecoveryNonce: ls.recoveryNonce,
	}
	for _, att := range k.attempts {
		if att.State != AttemptOpen {
			continue
		}
		if dl, ok := k.registry.Lookup(att.Domain); ok && dl.Lane == lane {
			snap.OpenAttempts = append(snap.OpenAttempts, att.ID)
		}
	}
	sort.Slice(snap.OpenAttempts, func(i, j int) bool { return snap.OpenAttempts[i] < snap.OpenAttempts[j] })
	return snap, nil
}

// Attempt returns a copy of the attempt, if it exists.
func (k *Kernel) Attempt(id AttemptID) (ExecutionAttempt, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	att, ok := k.attempts[id]
	if !ok {
		return ExecutionAttempt{}, false
	}
	return *att, true
}

// OpenAttempt returns the single open attempt of a domain, if any. At most one
// attempt is open per domain at a time.
func (k *Kernel) OpenAttempt(domain DomainID) (ExecutionAttempt, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if att := k.openAttemptFor(domain); att != nil {
		return *att, true
	}
	return ExecutionAttempt{}, false
}

// Domain returns the read model of one domain.
func (k *Kernel) Domain(id DomainID) (Domain, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	d, ok := k.registry.Lookup(id)
	if !ok {
		return Domain{}, false
	}
	return *d, true
}

// --- transitions -----------------------------------------------------------

func (k *Kernel) applyHello(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	switch d.State {
	case DomainPending:
		if d.Parent == nil {
			if ls.top() != "" {
				k.recordAuthFailure(d.Lane)
				return nil, ErrLaneBusy
			}
		} else {
			pd, ok := k.registry.Lookup(*d.Parent)
			if !ok {
				return nil, ErrUnknownParent
			}
			if pd.State != DomainSuspended {
				k.recordAuthFailure(d.Lane)
				return nil, ErrParentActive
			}
			if ls.top() != pd.ID {
				return nil, ErrParentNotTop
			}
		}
		// What the far shell says is installed on its host, kept before the
		// state moves so the fact is available from the moment the domain is
		// established rather than a frame later.
		d.BundleGeneration = env.Event.Hello.Generation
		d.State = DomainEstablished
		// The accept is minted but NOT delivered — the caller owns delivery
		// ordering (decision 9), and the domain is not live until the
		// accept reaches the shell (Deliver clears acceptPending). Events
		// are rejected meanwhile (requireActive → ErrDomainPending), and
		// EstablishmentTimeout rolls the domain back if the accept never
		// goes out.
		d.acceptPending = true
		ls.stack = append(ls.stack, d.ID)
		k.setLifecycle(ls, LifecyclePromptReady, d.ID, "")
		return []Outbound{k.acceptOutbound(d)}, nil
	case DomainEstablished, DomainSuspended, DomainDesynchronized:
		// Reconnect within the epoch: accepted, counter never resets,
		// authority unchanged. A fresh accept lets the shell gate its
		// prompt suppression on the reply.
		return []Outbound{k.acceptOutbound(d)}, nil
	default:
		return nil, ErrDomainNotLive
	}
}

func (k *Kernel) applyStart(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if err := k.requireActive(d, ls); err != nil {
		return nil, err
	}
	if len(env.Event.Start.Command) > k.budgets.MaxCommandBytes {
		return nil, ErrOversizeCommand
	}
	open := k.openAttemptFor(d.ID)
	// Lifecycle gate (decision 5): a start attaches to the single pending
	// app attempt, or opens a shell-originated attempt — and only at a ready
	// prompt. Running with a just-completed attempt (awaiting prompt_ready)
	// or any other lane state is a violation.
	switch {
	case open != nil && !open.Started:
		if env.Event.Start.AttemptID != nil && *env.Event.Start.AttemptID != open.ID {
			// The shell names the attempt in its own namespace: it never
			// learns the app-minted id (protocol §8 — no outbound envelope
			// carries one), so its id is the only name it can report in a
			// snapshot. Record it as a per-attempt alias and attach. The app
			// id stays authoritative — the attempt keeps its id, command
			// text, cwd, host and secrets (decision 5) — and the alias is
			// learnable only here, downstream of requireActive above, never
			// from a snapshot (ADR-0024 constraint a). A second top-level
			// attempt over a pending one is still impossible: the attach
			// window admits exactly one start, because Started flips and the
			// next start hits ErrAttemptOpen (constraint d).
			// No collision check here (the default branch's
			// ErrAttemptIDExists guard does not apply): this id becomes an
			// alias on an existing record, never a map key, and an honest
			// shell mints s-<dom>-<counter> — which cannot equal any
			// existing attempt id: app-minted ids are att-<hex>, foreign
			// shells carry their own domain prefix, and this shell's
			// counter never re-mints a value. The default branch keeps its
			// check because there the id becomes the attempt's identity (a
			// map key), so a shell naming an id that is not its own must be
			// rejected rather than allowed to overwrite the record.
			open.shellID = *env.Event.Start.AttemptID
		}
		open.Started = true // attach: id, command text, cwd, host and secrets stay app-owned
		k.setLifecycle(ls, LifecycleRunning, d.ID, open.ID)
		return nil, nil
	case open != nil:
		return nil, ErrAttemptOpen // start while an attempt runs
	default:
		if ls.lifecycle != LifecyclePromptReady {
			return nil, ErrNotPromptReady // no prompt yet: completion pending, or lane lost/native
		}
		if env.Event.Start.AttemptID != nil {
			// The id becomes this attempt's identity (a map key): a global
			// id must stay unique. The domain-scoped mint makes an honest
			// collision impossible; this check is the guard for a shell that
			// names an id that is not its own.
			if _, exists := k.attempts[*env.Event.Start.AttemptID]; exists {
				return nil, ErrAttemptIDExists
			}
		}
		var id AttemptID
		if env.Event.Start.AttemptID != nil {
			id = *env.Event.Start.AttemptID
		} else {
			var idErr error
			if id, idErr = k.newAttemptID(); idErr != nil {
				return nil, idErr
			}
		}
		att := k.createAttempt(d, id, OriginShell, true, env.Event.Start.Command, "", "", k.now())
		k.setLifecycle(ls, LifecycleRunning, d.ID, att.ID)
		return nil, nil
	}
}

func (k *Kernel) applyComplete(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if err := k.requireActive(d, ls); err != nil {
		return nil, err
	}
	c := env.Event.Complete
	if c.Fence == (FenceNonce{}) {
		return nil, ErrFenceMissing
	}
	att := k.openAttemptFor(d.ID)
	if c.AttemptID != nil {
		named, ok := k.attempts[*c.AttemptID]
		if !ok {
			return nil, ErrAttemptNotOpen
		}
		if named.Domain != d.ID {
			return nil, ErrAttemptDomainMismatch
		}
		if att != nil && named.ID != att.ID {
			return nil, ErrAttemptMismatch
		}
		att = named
	}
	if att == nil {
		return nil, ErrAttemptNotOpen
	}
	if !att.Started {
		return nil, ErrAttemptNotStarted
	}
	if att.State != AttemptOpen {
		return nil, ErrAttemptNotOpen // exit status is set exactly once
	}
	now := k.now()
	att.State = AttemptCompleted
	att.ExitCode = c.ExitCode
	att.CompletedAt = &now
	att.Fence = c.Fence
	return nil, nil
}

func (k *Kernel) applyPromptReady(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if err := k.requireActive(d, ls); err != nil {
		return nil, err
	}
	if open := k.openAttemptFor(d.ID); open != nil {
		return nil, ErrPromptOverAttempt
	}
	k.setLifecycle(ls, LifecyclePromptReady, d.ID, "")
	return nil, nil
}

func (k *Kernel) applySuspend(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if err := k.requireActive(d, ls); err != nil {
		return nil, err
	}
	d.State = DomainSuspended
	k.setLifecycle(ls, LifecycleNative, "", "")
	return nil, nil
}

func (k *Kernel) applyActivate(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if d.State != DomainSuspended {
		return nil, ErrNotSuspended
	}
	if ls.top() != d.ID {
		return nil, ErrDomainNotTop // a live child is above; close it first
	}
	d.State = DomainEstablished
	if open := k.openAttemptFor(d.ID); open != nil {
		k.setLifecycle(ls, LifecycleRunning, d.ID, open.ID)
	} else {
		k.setLifecycle(ls, LifecyclePromptReady, d.ID, "")
	}
	return nil, nil
}

func (k *Kernel) applyClose(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if d.State != DomainEstablished && d.State != DomainSuspended {
		return nil, ErrDomainNotLive
	}
	if ls.top() != d.ID {
		return nil, ErrDomainNotTop
	}
	ls.stack = ls.stack[:len(ls.stack)-1]
	d.State = DomainClosed
	k.unknownOpenAttempts(d.ID) // the shell is gone; no completion will come
	k.setLifecycle(ls, LifecycleNative, "", "")
	return nil, nil
}

func (k *Kernel) applyDomainRequest(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if err := k.requireActive(d, ls); err != nil {
		return nil, err
	}
	req := env.Event.DomainRequest
	if req.RequestID == "" {
		return nil, ErrRequestIDShape
	}
	// The request id is the shell's own nonce; the grant echoes it so a
	// stale grant can never answer a newer request. The shape bound keeps
	// it out of any quoting the shell side does: [A-Za-z0-9._-]{1,64}.
	if !requestIDRe.MatchString(string(req.RequestID)) {
		return nil, ErrRequestIDShape
	}
	switch req.Env {
	case EnvSudo, EnvSu, EnvSSH:
		// Known kinds only; an unknown env is a malformed request, never a
		// silent conventional fallback (the refusal must be explicit).
	default:
		return nil, ErrBadRequest
	}
	if req.Env == EnvSSH && req.Host == "" {
		return nil, ErrBadRequest // no destination: nothing to compose a line for
	}
	if req.Port < 0 || req.Port > 65535 {
		return nil, ErrBadRequest
	}
	// The options are spliced into a command line the PARENT SHELL then
	// evaluates, so they are bounded here even though the frame is
	// capability-authenticated and the shell is the only thing that can have
	// sent it. The composer shell-quotes each one, which is what makes them
	// safe; these bounds are what keep a malformed frame from composing a
	// line no shell will accept. Every real ssh invocation is far inside
	// them, and an option this refuses refuses the whole request — the
	// parent then runs its command conventionally, which is the honest
	// fallback and never a silently altered command (nocx-c6z0).
	if len(req.Opts) > maxDomainRequestOpts {
		return nil, ErrBadRequest
	}
	for _, o := range req.Opts {
		if o == "" || len(o) > maxDomainRequestOptLen {
			return nil, ErrBadRequest
		}
	}
	// The kernel validates and mints nothing here: the grant's child is
	// minted by the publisher seam (kernel.RequestDomain — the kernel
	// stays the sole minter), which also picks the child's transport and
	// builds the bootstrap. This outbound is the request echo; the seam
	// enriches it (or delivers it as the empty-bootstrap refusal).
	return []Outbound{k.grantOutbound(d, req)}, nil
}

// applyAgentEnrol validates the enrolment act and echoes it for the seam that
// actually opens the grid.
//
// The kernel keeps NOTHING here, and that is the design rather than an
// omission. Enrolment is not lifecycle state: it may not open, complete or
// alter an execution attempt (ADR-0024 decision 1), and a kernel that held it
// would be a second place where "is this pane being watched" is answered. The
// kernel's job is the one thing only it can do — say that this frame really
// came from this domain in this epoch — after which the fact belongs to
// whoever owns grids, which is not this package and cannot be, since the
// kernel is a pure model with no transport and no pane.
func (k *Kernel) applyAgentEnrol(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if err := k.requireActive(d, ls); err != nil {
		return nil, err
	}
	req := env.Event.AgentEnrol
	if req.RequestID == "" || !requestIDRe.MatchString(string(req.RequestID)) {
		return nil, ErrRequestIDShape
	}
	if !agentNameRe.MatchString(req.Agent) {
		return nil, ErrBadRequest
	}
	if req.Cols <= 0 || req.Rows <= 0 || req.Cols > maxPaneDimension || req.Rows > maxPaneDimension {
		return nil, ErrBadRequest
	}
	return []Outbound{k.outbound(d, Event{
		Kind:          KindAgentEnrolled,
		AgentEnrolled: &AgentEnrolled{RequestID: req.RequestID, Agent: req.Agent},
	})}, nil
}

// applyAgentWithdraw closes the caller's end of the interval. It is answered
// rather than silently absorbed: an interval whose close is fire-and-forget is
// an interval with one end, which is the shape AGENTS.md names.
func (k *Kernel) applyAgentWithdraw(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if err := k.requireActive(d, ls); err != nil {
		return nil, err
	}
	req := env.Event.AgentWithdraw
	if req.RequestID == "" || !requestIDRe.MatchString(string(req.RequestID)) {
		return nil, ErrRequestIDShape
	}
	return []Outbound{k.outbound(d, Event{
		Kind:           KindAgentWithdrawn,
		AgentWithdrawn: &AgentWithdrawn{RequestID: req.RequestID},
	})}, nil
}

// applyAgentReport validates a participant's declaration and echoes it for the
// seam that records it.
//
// The kernel keeps nothing here, exactly as it keeps nothing for an
// enrolment, and for the same reason: a declaration is not lifecycle state —
// it may not open, complete or alter an execution attempt (ADR-0024 decision
// 1) — and a kernel that held it would be a second place where "what did this
// participant produce" is answered. What only the kernel can do is say the
// frame really came from this domain in this epoch; after that the fact
// belongs to whoever owns the wave record, which is not this package.
//
// The summary is bounded HERE and not at the seam, because the bound is a
// property of the frame: a report longer than this could not be sent at all,
// and refusing it where the frame is parsed is what stops the rest of the
// stack from having to have an opinion about length.
func (k *Kernel) applyAgentReport(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if err := k.requireActive(d, ls); err != nil {
		return nil, err
	}
	req := env.Event.AgentReport
	if req.RequestID == "" || !requestIDRe.MatchString(string(req.RequestID)) {
		return nil, ErrRequestIDShape
	}
	if len(req.Summary) > MaxReportSummaryBytes {
		return nil, ErrBadRequest
	}
	return []Outbound{k.outbound(d, Event{
		Kind:          KindAgentReported,
		AgentReported: &AgentReported{RequestID: req.RequestID},
	})}, nil
}

// outbound addresses one kernel→shell envelope to the domain that asked, by
// the tuple the adapter routes by: lane, domain and epoch. It is the ONE
// place that builds an outbound envelope, so that what follows is decided
// once rather than four times.
//
// # The outbound direction carries no capability, deliberately
//
// It used to carry it on every frame, and that undid what the capability is
// for (nocx-aqz7o). ADR-0024 makes the per-epoch capability mandatory rather
// than belt-and-braces for one named actor: a DESCENDANT of the shell, which
// inherits the lifecycle descriptor because bash's redirection is not
// close-on-exec and exec.Cmd.ExtraFiles clears FD_CLOEXEC. The transport
// stops whatever can only write to the terminal; the capability was supposed
// to stop that descendant. Writing the capability back onto the same
// descriptor, in cleartext, on the very first frame of the handshake, handed
// it to the actor it exists to exclude — a passive read, needing no ptrace
// and no /proc, by exactly the actor the ADR names and does not exclude.
//
// Nothing needed it. The kernel is the only sender on this direction; the
// shell already holds the capability it was given at bootstrap, so an echo
// authenticates nobody to anybody. Its one production reader was the accept
// check in the integration scripts, which used it as an "is this frame
// mine" test — and could not have been authenticating the peer, because the
// shell's own hello carries the capability to that peer one frame earlier.
// The scripts now make that test on the domain and epoch, which are the
// non-secret halves of the same tuple and are already in the shell's
// environment.
//
// What this does NOT close: a domain_grant's bootstrap payload is the child
// shell's rcfile text, and for a local nested environment that text contains
// the CHILD's capability. It rides this same descriptor, and it is inherent
// to the design — the parent has to receive something to hand its child.
// That is a separate exposure with a separate answer, and it is not this one.
func (k *Kernel) outbound(d *Domain, evt Event) Outbound {
	return Outbound{
		Transport: d.Transport,
		Envelope: Envelope{
			Version: ProtocolVersion, Lane: d.Lane, Domain: d.ID,
			Epoch: d.Epoch,
			Event: evt,
		},
	}
}

func (k *Kernel) grantOutbound(d *Domain, req *DomainRequest) Outbound {
	return k.outbound(d, Event{Kind: KindDomainGrant, DomainGrant: &DomainGrant{
		RequestID: req.RequestID,
		Env:       req.Env,
		Host:      req.Host,
		User:      req.User,
		Port:      req.Port,
		Opts:      req.Opts,
	}})
}

func (k *Kernel) applySnapshot(d *Domain, ls *laneState, env Envelope) ([]Outbound, error) {
	if d.State != DomainDesynchronized {
		return nil, ErrSnapshotUnexpected
	}
	s := env.Event.Snapshot
	if d.refreshRequest == nil || *d.refreshRequest != s.RequestID {
		return nil, ErrSnapshotMismatch
	}
	if s.NextSequence <= env.Sequence {
		return nil, ErrSnapshotSequence
	}
	if s.ActiveAttemptID != nil && s.LastCompleted != nil && *s.ActiveAttemptID == s.LastCompleted.AttemptID {
		return nil, ErrSnapshotConflict
	}
	// Resolution phase: map the shell-named ids to THIS domain's attempts —
	// exact id first (a shell-originated attempt's id IS the kernel's
	// record), then the per-attempt alias recorded from an authenticated
	// start. A snapshot may name an alias the kernel already learned, never
	// create one (constraint a), and aliases are per-domain: the alias scan
	// is scoped to this domain, so a child's or sibling's alias never
	// resolves here (constraint c).
	var active, completed *ExecutionAttempt
	var activeOwned, completedOwned bool
	if s.ActiveAttemptID != nil {
		active, activeOwned = k.resolveAttempt(d, *s.ActiveAttemptID)
	}
	if s.LastCompleted != nil {
		completed, completedOwned = k.resolveAttempt(d, s.LastCompleted.AttemptID)
	}
	// Validation phase: every reference in the snapshot must agree with the
	// kernel's records before anything mutates (decision 7: invalid events
	// mutate nothing). A named id that exists under a DIFFERENT domain is a
	// contradiction, not an unknown id — resolveAttempt's bool is the single
	// owner of that "does this attempt belong to the domain" question, and
	// both call sites consume it. An unknown active attempt is fine — it
	// will be created from the snapshot (its start was lost in the gap) —
	// and an unknown last-completed attempt is fine — there is nothing open
	// to reconcile against it.
	if active != nil {
		if !activeOwned {
			return nil, ErrSnapshotConflict
		}
		if active.State != AttemptOpen {
			return nil, ErrSnapshotConflict // shell claims running; we have it terminal
		}
	}
	if completed != nil && !completedOwned {
		return nil, ErrSnapshotConflict
	}
	// Apply phase: all validation passed, so every mutation below is final.
	if s.ActiveAttemptID != nil && active == nil {
		active = k.createAttempt(d, *s.ActiveAttemptID, OriginShell, true, "", "", "", k.now())
	}
	if s.LastCompleted != nil && completed != nil && completed.State == AttemptOpen {
		now := k.now()
		completed.State = AttemptCompleted
		completed.ExitCode = s.LastCompleted.ExitCode
		completed.CompletedAt = &now
	}
	// Already terminal: nothing to reconcile.
	for _, att := range k.attempts {
		if att.Domain == d.ID && att.State == AttemptOpen && att != active {
			att.State = AttemptUnknown // open, but the shell is not running it
		}
	}
	d.State = DomainEstablished
	d.refreshRequest = nil
	d.desyncBytes, d.desyncFrames = 0, 0
	if ls.top() == d.ID {
		if s.ActiveAttemptID != nil {
			k.setLifecycle(ls, LifecycleRunning, d.ID, active.ID)
		} else {
			k.setLifecycle(ls, LifecyclePromptReady, d.ID, "")
		}
	}
	return nil, nil
}

// resolveAttempt maps a shell-named attempt id to a kernel attempt of this
// domain: exact id first — a shell-originated attempt's id IS the kernel's
// record — then the per-attempt alias recorded from an authenticated start
// (constraint a: a snapshot may name an alias the kernel already learned,
// never create one). Aliases are per-domain (constraint c): the alias scan
// is scoped to this domain, so a child's or sibling's alias never resolves
// here. The bool reports whether the found attempt belongs to the domain: a
// named id that exists under a different domain is a contradiction for the
// caller, not an unknown id.
func (k *Kernel) resolveAttempt(d *Domain, id AttemptID) (*ExecutionAttempt, bool) {
	if att, ok := k.attempts[id]; ok {
		return att, att.Domain == d.ID
	}
	for _, att := range k.attempts {
		if att.Domain == d.ID && att.shellID == id {
			return att, true
		}
	}
	return nil, false
}

// --- helpers ---------------------------------------------------------------

// Outbound is one kernel-originated envelope awaiting delivery — accept or
// refresh_request, the only two kinds the kernel sends. It is addressed to
// the transport the domain is bound to; the caller delivers it with Deliver,
// owning the ordering (decision 9). The transport port itself stays private
// to the kernel; the caller never touches it.
type Outbound struct {
	Transport TransportID
	Envelope  Envelope
}

// Deliver sends one outbound envelope to its transport's port. The port is
// looked up under the lock and the send runs OUTSIDE it — the "send outside
// the kernel mutex" invariant the old internal flush preserved. An accept is
// refused unless the domain is still Established: an accept for a revoked,
// lost or never-helloed domain would let a shell suppress its prompt against
// a dead domain (decision 9). On a successful accept send the domain's
// acceptPending clears — the domain is live past ACCEPT (decision 3), and
// only then may lifecycle events be accepted. Send failures are best-effort
// (safe direction: the shell times out its handshake).
func (k *Kernel) Deliver(out Outbound) error {
	k.mu.Lock()
	port, ok := k.ports[out.Transport]
	var d *Domain
	if ok {
		d, _ = k.registry.Lookup(out.Envelope.Domain)
	}
	if out.Envelope.Event.Kind == KindAccept && (d == nil || d.State != DomainEstablished) {
		k.mu.Unlock()
		return ErrDomainNotLive
	}
	k.mu.Unlock()
	if !ok || port == nil {
		return ErrUnknownTransport
	}
	err := port.Send(out.Envelope)
	if err == nil && out.Envelope.Event.Kind == KindAccept && d != nil {
		k.mu.Lock()
		if d.State == DomainEstablished && d.acceptPending {
			d.acceptPending = false
		}
		k.mu.Unlock()
	}
	return err
}

func (k *Kernel) acceptOutbound(d *Domain) Outbound {
	return k.outbound(d, Event{Kind: KindAccept, Accept: &Accept{}})
}

func (k *Kernel) refreshOutbound(d *Domain, rid RequestID) Outbound {
	return k.outbound(d, Event{Kind: KindRefreshRequest, RefreshRequest: &RefreshRequest{RequestID: rid}})
}

// requireActive enforces that the domain is live, established (not
// desynchronized), the top of its lane stack, and past ACCEPT (decision 9:
// a domain whose accept was minted but never delivered is not live).
func (k *Kernel) requireActive(d *Domain, ls *laneState) error {
	switch d.State {
	case DomainEstablished:
		if d.acceptPending {
			return ErrDomainPending // not past accept (decision 3/9)
		}
		if ls.top() != d.ID {
			return ErrDomainNotTop
		}
		return nil
	case DomainDesynchronized:
		return ErrDomainDesynchronized
	case DomainSuspended:
		return ErrDomainInactive
	case DomainPending:
		return ErrDomainPending
	default:
		return ErrDomainNotLive
	}
}

func (k *Kernel) createAttempt(d *Domain, id AttemptID, origin AttemptOrigin, started bool, command, cwd, host string, at time.Time) *ExecutionAttempt {
	att := &ExecutionAttempt{
		ID: id, Domain: d.ID, Lane: d.Lane,
		Command: command, Cwd: cwd, Host: host,
		StartedAt: at, Origin: origin, Started: started, State: AttemptOpen,
	}
	k.attempts[id] = att
	return att
}

func (k *Kernel) openAttemptFor(domain DomainID) *ExecutionAttempt {
	for _, att := range k.attempts {
		if att.Domain == domain && att.State == AttemptOpen {
			return att
		}
	}
	return nil
}

func (k *Kernel) unknownOpenAttempts(domain DomainID) {
	for _, att := range k.attempts {
		if att.Domain == domain && att.State == AttemptOpen {
			att.State = AttemptUnknown
		}
	}
}

func (k *Kernel) removeFromStack(ls *laneState, id DomainID) {
	for i, cur := range ls.stack {
		if cur == id {
			ls.stack = append(ls.stack[:i], ls.stack[i+1:]...)
			return
		}
	}
}

func (k *Kernel) revoke(d *Domain, ls *laneState) {
	d.State = DomainClosed
	d.refreshRequest = nil
	k.unknownOpenAttempts(d.ID)
	k.removeFromStack(ls, d.ID)
	k.setLifecycle(ls, LifecycleNative, "", "")
}

func (k *Kernel) checkDesyncBudget(d *Domain, ls *laneState) {
	if d.desyncBytes > k.budgets.ScanBytes ||
		d.desyncFrames > k.budgets.ScanFrames ||
		k.now().Sub(d.desyncSince) >= k.budgets.ScanDuration {
		k.revoke(d, ls)
	}
}

func (k *Kernel) getLane(lane LaneID) *laneState {
	if ls, ok := k.lanes[lane]; ok {
		return ls
	}
	ls := &laneState{lane: lane, lifecycle: LifecycleNative}
	k.lanes[lane] = ls
	return ls
}

func (k *Kernel) setLifecycle(ls *laneState, st LifecycleState, d DomainID, att AttemptID) {
	ls.lifecycle = st
	ls.lifecycleDomain = d
	ls.lifecycleAttempt = att
	if st == LifecycleNative {
		// THE RECOVERY FENCE'S AUTHORITY INTERVAL CLOSES HERE, and it is a
		// different interval from its confidentiality (carrier design §5.3).
		//
		// Authority opens once the bootstrap has succeeded and the backend
		// has registered the fence for a domain generation, and closes at
		// the FIRST of: the fence being sent once on channel loss and
		// acknowledged, teardown with no recovery needed, or a generation
		// replacement. Every one of those three ends with the lane back at
		// Native — RecoverLane completing the composite ACK, a clean
		// domain_closed, a revoke — which is why the close lives here
		// rather than in three call sites that would drift apart.
		//
		// The third event, a generation replacement, closes it from the
		// other direction: RequestDomain overwrites the nonce, so a late
		// ack from the previous episode can no longer match.
		//
		// The lane keeps the expected value until then and must: it is what
		// validates the acknowledgement, and dropping it at the moment the
		// fence goes out would leave nothing to check the answer against.
		//
		// Closing authority is NOT closing confidentiality, and conflating
		// them would promise more than we can hold. The domain's own record
		// still carries its fence, and the shell's copy is a variable in a
		// process on another host that we may no longer be able to address.
		// This clears one backend copy — the one whose PURPOSE has ended —
		// and claims nothing about the others.
		ls.recoveryNonce = FenceNonce{}
	}
}

func (k *Kernel) overHandshakeBudget(ls *laneState) bool {
	if ls == nil {
		return false
	}
	now := k.now()
	keep := ls.helloFailures[:0]
	for _, t := range ls.helloFailures {
		if now.Sub(t) < k.budgets.HandshakeWindow {
			keep = append(keep, t)
		}
	}
	ls.helloFailures = keep
	return len(keep) >= k.budgets.HandshakeFailures
}

// randomFence mints the per-domain one-shot recovery fence: 32 random bytes,
// distinct from the capability, handed to the shell in the bootstrap while
// the channel is alive. Never reused: a fresh domain is a fresh nonce.
//
// A failed read is an error here too, and not because a zero fence is
// dangerous the way a zero capability is — the read model treats zero as "no
// recovery nonce" and disables the promise, which is the safe direction. It
// is an error because the fence and the capability come from the same source
// in the same breath: if this read failed, that one did, and one rule for
// "the machine has no randomness" is easier to hold than two (nocx-s16k8).
func (k *Kernel) randomFence() (FenceNonce, error) {
	var f FenceNonce
	if _, err := io.ReadFull(k.rand, f[:]); err != nil {
		return FenceNonce{}, fmt.Errorf("%w: recovery fence: %v", ErrNoRandomness, err)
	}
	return f, nil
}

// recordAuthFailure charges one failed handshake to the lane (decision 3's
// rate limit). Unknown lanes are skipped: garbage lane ids are the adapter's
// connection-level concern.
func (k *Kernel) recordAuthFailure(lane LaneID) {
	if ls, ok := k.lanes[lane]; ok {
		ls.helloFailures = append(ls.helloFailures, k.now())
	}
}

// randomHex mints an IDENTIFIER — a domain id, a refresh request id, an
// attempt id. None of these is an authenticator, so a zero value does not let
// anybody in. It does something else that is still a defect: every id becomes
// the SAME id, and every place that tells two things apart by id — the
// attempt registry, the refresh request the shell echoes back, the domain a
// frame names — stops telling them apart. That is the capability's failure
// mode one layer down, so it gets the capability's answer (nocx-s16k8).
func (k *Kernel) randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(k.rand, b); err != nil {
		return "", fmt.Errorf("%w: identifier: %v", ErrNoRandomness, err)
	}
	return hex.EncodeToString(b), nil
}

// randomCapability mints the per-epoch bearer. A failed random read is an
// ERROR and no capability is returned (nocx-s16k8).
//
// It used to be tolerated, leaving a zero capability, on the argument that
// the caller has no useful answer to "the machine has no randomness". But a
// zero capability is not a degraded authenticator, it is an authenticator
// every candidate can produce: the check is an equality against the expected
// value, and two zero values compare equal. ingestLocked refusing a zero
// capability closed that hole and still does — but a domain minted from a
// failed read was still registered, still offered to a shell, and still
// waiting for a handshake that could never authenticate. Failing here is
// what makes the failure a refusal instead of a session that hangs.
func (k *Kernel) randomCapability() (Capability, error) {
	var c Capability
	if _, err := io.ReadFull(k.rand, c[:]); err != nil {
		return Capability{}, fmt.Errorf("%w: per-epoch capability: %v", ErrNoRandomness, err)
	}
	return c, nil
}

func (k *Kernel) newAttemptID() (AttemptID, error) {
	h, err := k.randomHex(8)
	if err != nil {
		return "", err
	}
	return AttemptID("att-" + h), nil
}

// AdoptDomain installs a domain this kernel did not mint: the shell on the
// far side of a helper-owned session is still speaking, still holds the
// capability and epoch it was given at spawn, and has no idea that the
// coordinator it was talking to was replaced (nocx-k6p18.31).
//
// # This is not a Lost domain being revived, and the distinction is the whole
// # of the authority argument
//
// §12 says a Lost domain stays Lost and any future integration is a fresh
// epoch. That rule is about a kernel that WATCHED a transport die and revoked
// authority: it exists so that the renderer, having fallen back to a native
// prompt, can never be told the old authority is good again. Nothing of the
// sort happened here. The shell's end of the lifecycle channel is a
// socketpair descriptor handed over at spawn, and the HELPER holds the parent
// end — so when the coordinator process went away, the domain did not die,
// the registry that could address it did. Adoption re-establishes the
// coordinator↔helper leg and nothing else; the shell notices nothing, because
// there is nothing for it to notice.
//
// # What still stops a stale or hostile writer
//
// Exactly what stopped it before, unchanged: the per-frame bearer. Ingest
// authenticates domain, transport, epoch and capability before it consults
// any state, and adoption alters none of that — a descendant of the shell
// that inherited the descriptor cannot produce the capability, because the
// only place it could have READ one is the channel itself and the outbound
// direction no longer writes it there (see outbound; it did until
// nocx-aqz7o, and while it did this sentence was false). A frame from any
// OTHER domain still names an id this kernel does not hold.
// What adoption adds is one more holder of the capability: the replacing
// coordinator, which is the same trust class that minted it in the first
// place and which already owns the session's keyboard and its whole output
// stream. The one new exposure adoption WOULD create is replay of the
// helper's retained lifecycle window, and it is closed where the window is
// read rather than here: the re-attachment resumes at the window's head, so
// no frame the previous coordinator already consumed is ever re-delivered.
//
// The domain is installed Established and the lane PromptReady — the state
// the shell is actually in, since it has its accept and speaks only from a
// prompt. An attempt that was open when the coordinator was replaced is not
// adopted with it: it belonged to the dead kernel, and its outcome comes back
// from the bytes (nocx-k6p18.6), which is the existing answer to that half.
func (k *Kernel) AdoptDomain(lane LaneID, id DomainID, epoch uint64, capability Capability, recovery FenceNonce, t TransportID) (DomainHandle, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.ports[t]; !ok {
		return DomainHandle{}, ErrUnknownTransport
	}
	// A zero capability authenticates thirty-two zero bytes, which is the
	// same trap ingest's own zero test guards; refused here so no such
	// domain is ever registered.
	if lane == "" || id == "" || epoch == 0 || capability == (Capability{}) {
		return DomainHandle{}, ErrInvalidArgument
	}
	if _, exists := k.registry.Lookup(id); exists {
		return DomainHandle{}, ErrDomainExists
	}
	ls := k.getLane(lane)
	if ls.top() != "" {
		return DomainHandle{}, ErrLaneBusy
	}
	d := &Domain{
		ID:         id,
		Epoch:      epoch,
		Lane:       lane,
		Transport:  t,
		State:      DomainEstablished,
		capability: capability,
		recovery:   recovery,
	}
	k.registry.Register(d)
	// The adopted epoch came from another process's counter. Lifting ours
	// past it keeps `nextEpoch` the monotonic, never-reused value decision 8
	// asks for across the two processes, not merely within this one.
	k.registry.adoptEpoch(epoch)
	ls.recoveryNonce = recovery
	ls.stack = append(ls.stack, d.ID)
	k.setLifecycle(ls, LifecyclePromptReady, d.ID, "")
	return DomainHandle{Domain: d.ID, Epoch: d.Epoch, Capability: d.capability, Recovery: d.recovery}, nil
}
