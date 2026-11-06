// Package catalog is the one declaration site for every operation nocx can
// run, and the only place an operation's lifetime is written.
//
// ADR-8eej3e66: settings have one declaration site and registration follows
// from it; an operation's lifetime is the same kind of fact, so it is declared
// once here rather than restated at each call site, in a handler, or in a
// second timeout somebody adds later. Everything else is a projection:
//
//   - internal/transport's methodSpec holds a typed Method handle from this
//     table, so a JSON-RPC method with no entry cannot be registered;
//   - contracts/operations.json is generated from Declarations() and is
//     validated by contracts/operations.schema.json;
//   - contracts/openrpc.json carries each declaration as x-nocx-operation.
//
// The renderer's copy of the deadlines and sentences is NOT generated yet.
// It belongs to nocx-3an1s.2.2, the stage that arms renderer timers: a
// generated TypeScript module nothing imports is a dead export, and this stage
// has no user for it. Go and contracts/operations.json are the consumers.
//
// The package imports nothing from internal/transport. Startup recovery and
// other work that precedes buildMethodSpecs can read the same declarations.
//
// Stage 2 of the migration declares and records. It does not enforce: a
// deadline on expiry is logged, not acted on, so nothing a person can do
// behaves differently because of this package yet. The behaviour changes
// belong to the stages that migrate each domain.
package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ID names one operation: a JSON-RPC method's wire name, or the name of an
// internal operation the backend starts by itself.
type ID string

// Kind says which half of the catalogue an entry belongs to.
type Kind string

const (
	KindMethod   Kind = "method"
	KindInternal Kind = "internal"
)

// Owner is what an invocation dies with.
type Owner string

const (
	OwnerRequest  Owner = "request"
	OwnerSession  Owner = "session"
	OwnerBinding  Owner = "binding"
	OwnerServer   Owner = "server"
	OwnerCheckout Owner = "checkout"
)

// Basis records where a declared deadline came from. Stage 2 is required to
// declare deadlines from present behaviour, not from taste, so every deadline
// says whether it was read off the code or measured.
type Basis string

const (
	// BasisRead is a deadline read off present behaviour: an existing
	// constant, a gate's wait bound, or an explicit read of the code path
	// that says no bound exists today.
	BasisRead Basis = "read"
	// BasisMeasured is a deadline derived from a measurement of present
	// behaviour. The Note names the measurement.
	BasisMeasured Basis = "measured"
)

// Deadline is one finite bound, in milliseconds, with its provenance. Zero is
// never a declaration: the validation refuses it, because a zero timeout in Go
// is the spelling of "wait for ever" and this catalogue exists to make that
// spelling unwritable.
type Deadline struct {
	Millis int64  `json:"millis"`
	Basis  Basis  `json:"basis"`
	Note   string `json:"note"`
}

func (d Deadline) Duration() time.Duration { return time.Duration(d.Millis) * time.Millisecond }

func (d Deadline) valid() bool {
	return d.Millis > 0 && (d.Basis == BasisRead || d.Basis == BasisMeasured) && strings.TrimSpace(d.Note) != ""
}

// BoundKind is the closed set of answers for silence, answer and recovery.
type BoundKind string

const (
	BoundNone     BoundKind = "none"
	BoundDeadline BoundKind = "deadline"
)

// Bound is a bound that may legitimately be absent: an operation with nothing
// to say has no progress bound, one that asks nobody has no answer bound, and
// a read that commits nothing has no recovery budget. The absent case is
// explicit (BoundNone), never a zero duration, so "none" and "for ever" cannot
// be confused.
type Bound struct {
	Kind   BoundKind `json:"kind"`
	Millis int64     `json:"millis"`
	Basis  Basis     `json:"basis"`
	Note   string    `json:"note"`
}

func (b Bound) Deadline() (Deadline, bool) {
	if b.Kind != BoundDeadline {
		return Deadline{}, false
	}
	return Deadline{Millis: b.Millis, Basis: b.Basis, Note: b.Note}, true
}

func (b Bound) valid() bool {
	switch b.Kind {
	case BoundNone:
		return b.Millis == 0 && b.Basis == "" && b.Note == ""
	case BoundDeadline:
		return (Deadline{Millis: b.Millis, Basis: b.Basis, Note: b.Note}).valid()
	default:
		return false
	}
}

// ResourceKind is the closed set of keys an operation may order on.
type ResourceKind string

const (
	ResourceSession    ResourceKind = "session"
	ResourceBinding    ResourceKind = "binding"
	ResourceCheckout   ResourceKind = "checkout"
	ResourceStore      ResourceKind = "store"
	ResourceCollection ResourceKind = "collection"
	ResourceBarrier    ResourceKind = "barrier"
)

// ResourceMode says how a resource is held. Key resources are per-instance
// FIFO keys; barriers are shared or exclusive.
type ResourceMode string

const (
	ModeKey       ResourceMode = "key"
	ModeShared    ResourceMode = "shared"
	ModeExclusive ResourceMode = "exclusive"
)

// Resource is one key or barrier an operation orders on.
type Resource struct {
	Kind ResourceKind `json:"kind"`
	Name string       `json:"name"`
	Mode ResourceMode `json:"mode"`
}

func (r Resource) String() string {
	if r.Kind == ResourceStore || r.Kind == ResourceBarrier {
		return string(r.Kind) + ":" + r.Name + ":" + string(r.Mode)
	}
	return string(r.Kind) + ":" + string(r.Mode)
}

func (r Resource) valid() bool {
	switch r.Kind {
	case ResourceSession, ResourceBinding, ResourceCheckout:
		return r.Name == "" && r.Mode == ModeKey
	case ResourceCollection:
		return r.Mode == ModeKey
	case ResourceStore:
		return strings.TrimSpace(r.Name) != "" && (r.Mode == ModeKey || r.Mode == ModeShared || r.Mode == ModeExclusive)
	case ResourceBarrier:
		return strings.TrimSpace(r.Name) != "" && (r.Mode == ModeShared || r.Mode == ModeExclusive)
	default:
		return false
	}
}

// Domain is a failure domain: what fails together and therefore needs its own
// bulkhead.
type Domain string

const (
	DomainConfig     Domain = "config"
	DomainVault      Domain = "vault"
	DomainContent    Domain = "content"
	DomainSession    Domain = "session"
	DomainGit        Domain = "git"
	DomainFilesystem Domain = "filesystem"
	DomainAPI        Domain = "api"
	DomainSSH        Domain = "ssh"
	DomainHelper     Domain = "helper"
	DomainModel      Domain = "model"
	DomainNetwork    Domain = "network"
	DomainNotify     Domain = "notify"
	DomainLoopback   Domain = "loopback"
	DomainServer     Domain = "server"
)

var domains = map[Domain]bool{
	DomainConfig: true, DomainVault: true, DomainContent: true, DomainSession: true,
	DomainGit: true, DomainFilesystem: true, DomainAPI: true, DomainSSH: true,
	DomainHelper: true, DomainModel: true, DomainNetwork: true, DomainNotify: true,
	DomainLoopback: true, DomainServer: true,
}

// Phase is one ordered step of an operation, with the resources and failure
// domains it draws on. A phase may hold nothing: an operation whose first step
// is pure computation still has a phase, and a later phase acquires what it
// needs.
type Phase struct {
	Name         string     `json:"name"`
	Resources    []Resource `json:"resources"`
	Dependencies []Domain   `json:"dependencies"`
}

// Merge is what a later invocation of the same operation may do.
type Merge string

const (
	MergeNone        Merge = "none"
	MergeLatestValue Merge = "latest-value"
	MergeSameKey     Merge = "same-key"
)

// Expired is the outcome at the deadline.
type Expired string

const (
	ExpiredNotStarted     Expired = "not-started"
	ExpiredOutcomeUnknown Expired = "outcome-unknown"
	ExpiredRetryRead      Expired = "retry-read"
)

// Row says when Operations shows the operation.
type Row string

const (
	RowNever    Row = "never"
	RowPastWait Row = "past-wait"
	RowAlways   Row = "always"
)

// Declaration is one operation's complete lifetime. Every field is required;
// the validation refuses a zero value rather than reading it as "unlimited".
type Declaration struct {
	ID       ID       `json:"id"`
	Kind     Kind     `json:"kind"`
	Owner    Owner    `json:"owner"`
	Phases   []Phase  `json:"phases"`
	Turn     Deadline `json:"turn"`
	Max      Deadline `json:"max"`
	Silence  Bound    `json:"silence"`
	Answer   Bound    `json:"answer"`
	Recovery Bound    `json:"recovery"`
	Merge    Merge    `json:"merge"`
	Preempts []ID     `json:"preempts"`
	Waiting  string   `json:"waiting"`
	Row      Row      `json:"row"`
	Expired  Expired  `json:"expired"`
}

// Resources is the union of every phase's resources, in declaration order,
// without duplicates. It is derived, never stored, so the phase sets stay the
// one source of truth.
func (d Declaration) Resources() []Resource {
	seen := map[string]bool{}
	var out []Resource
	for _, p := range d.Phases {
		for _, r := range p.Resources {
			key := r.String()
			if !seen[key] {
				seen[key] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// Dependencies is the union of every phase's failure domains.
func (d Declaration) Dependencies() []Domain {
	seen := map[Domain]bool{}
	var out []Domain
	for _, p := range d.Phases {
		for _, dep := range p.Dependencies {
			if !seen[dep] {
				seen[dep] = true
				out = append(out, dep)
			}
		}
	}
	return out
}

// Method is the typed handle internal/transport's methodSpec holds. Its only
// constructor is the table below, through the generated handle variables, so a
// registration cannot name a method the catalogue does not declare.
type Method struct {
	id ID
}

// ID returns the operation's id.
func (m Method) ID() ID { return m.id }

// Valid reports whether the handle came from the table.
func (m Method) Valid() bool { _, ok := declarationIndex[m.id]; return ok }

// Declaration returns the operation's declaration. It panics on a zero or
// unknown handle: there is no useful fallback, and a registration must fail
// loudly rather than run unbounded.
func (m Method) Declaration() Declaration {
	d, ok := declarationIndex[m.id]
	if !ok {
		panic("operation/catalog: handle for undeclared operation " + string(m.id))
	}
	return d
}

func methodHandle(id ID) Method {
	if _, ok := declarationIndex[id]; !ok {
		panic("operation/catalog: handle for undeclared operation " + string(id))
	}
	return Method{id: id}
}

// Declarations returns the complete table, sorted by id. The returned slice is
// a copy; a caller cannot mutate the catalogue.
func Declarations() []Declaration {
	out := make([]Declaration, len(declarationTable))
	copy(out, declarationTable)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var declarationTable = func() []Declaration {
	out := make([]Declaration, 0, len(methodDeclarations)+len(internalDeclarations))
	out = append(out, methodDeclarations...)
	out = append(out, internalDeclarations...)
	return out
}()

var declarationIndex = func() map[ID]Declaration {
	m := make(map[ID]Declaration, len(declarationTable))
	for _, d := range declarationTable {
		if _, dup := m[d.ID]; dup {
			panic("operation/catalog: duplicate declaration for " + string(d.ID))
		}
		m[d.ID] = d
	}
	return m
}()

func init() {
	if errs := Validate(declarationTable); len(errs) != 0 {
		panic(fmt.Sprintf("operation/catalog: %d invalid declaration(s): %v", len(errs), errs[0]))
	}
}

// Validate checks every declaration field and the table-level rules: ids are
// unique, a preempts target exists, a preemption is between two operations
// that order on at least one common resource, and a merge is only declared for
// a read (an expiry that says retry-read). It returns every problem rather
// than the first: a planted violation should name itself.
func Validate(table []Declaration) []error {
	var errs []error
	add := func(d Declaration, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", d.ID, fmt.Sprintf(format, args...)))
	}
	byID := make(map[ID]Declaration, len(table))
	for _, d := range table {
		if _, dup := byID[d.ID]; dup {
			add(d, "duplicate declaration")
			continue
		}
		byID[d.ID] = d
	}
	for _, d := range table {
		if strings.TrimSpace(string(d.ID)) == "" {
			add(d, "empty id")
		}
		if d.Kind != KindMethod && d.Kind != KindInternal {
			add(d, "kind %q is not method or internal", d.Kind)
		}
		switch d.Owner {
		case OwnerRequest, OwnerSession, OwnerBinding, OwnerServer, OwnerCheckout:
		default:
			add(d, "owner %q is not a known owner", d.Owner)
		}
		if len(d.Phases) == 0 {
			add(d, "no phases; an operation still declares the step it runs")
		}
		for i, p := range d.Phases {
			if strings.TrimSpace(p.Name) == "" {
				add(d, "phase %d has no name", i)
			}
			seen := map[string]bool{}
			for _, r := range p.Resources {
				if !r.valid() {
					add(d, "phase %q has invalid resource %+v", p.Name, r)
				}
				if seen[r.String()] {
					add(d, "phase %q repeats resource %s", p.Name, r)
				}
				seen[r.String()] = true
			}
			for _, dep := range p.Dependencies {
				if !domains[dep] {
					add(d, "phase %q has unknown failure domain %q", p.Name, dep)
				}
			}
		}
		if !d.Turn.valid() {
			add(d, "turn is not a finite, sourced deadline: %+v", d.Turn)
		}
		if !d.Max.valid() {
			add(d, "max is not a finite, sourced deadline: %+v", d.Max)
		}
		if !d.Silence.valid() {
			add(d, "silence is not none or a valid deadline: %+v", d.Silence)
		} else if sd, ok := d.Silence.Deadline(); ok && sd.Millis >= d.Max.Millis {
			add(d, "silence (%dms) must be shorter than max (%dms)", sd.Millis, d.Max.Millis)
		}
		if !d.Answer.valid() {
			add(d, "answer is not none or a valid deadline: %+v", d.Answer)
		}
		if !d.Recovery.valid() {
			add(d, "recovery is not none or a valid deadline: %+v", d.Recovery)
		}
		switch d.Merge {
		case MergeNone:
		case MergeLatestValue:
			// A later value replaces this one (resize). It is allowed for a
			// state replacement that cannot commit an external effect; an
			// effectful mutation whose outcome is unknown must not merge.
			if d.Expired == ExpiredOutcomeUnknown {
				add(d, "merge latest-value on an effectful mutation whose expiry is outcome-unknown")
			}
		case MergeSameKey:
			if d.Expired != ExpiredRetryRead {
				add(d, "merge same-key is only for a read, but expired is %q", d.Expired)
			}
		default:
			add(d, "merge %q is not none, latest-value or same-key", d.Merge)
		}
		switch d.Expired {
		case ExpiredNotStarted, ExpiredOutcomeUnknown, ExpiredRetryRead:
		default:
			add(d, "expired %q is not not-started, outcome-unknown or retry-read", d.Expired)
		}
		switch d.Row {
		case RowNever, RowPastWait, RowAlways:
		default:
			add(d, "row %q is not never, past-wait or always", d.Row)
		}
		if strings.TrimSpace(d.Waiting) == "" {
			add(d, "waiting sentence is empty")
		}
		for _, target := range d.Preempts {
			t, ok := byID[target]
			if !ok {
				add(d, "preempts unknown operation %q", target)
				continue
			}
			if !sharesResource(d, t) {
				add(d, "preempts %q, but they share no resource key", target)
			}
		}
	}
	return errs
}

func sharesResource(a, b Declaration) bool {
	set := map[string]bool{}
	for _, r := range a.Resources() {
		set[r.String()] = true
	}
	for _, r := range b.Resources() {
		if set[r.String()] {
			return true
		}
	}
	return false
}

// ── Invocation, scope and settlement ───────────────────────────────────────

// Origin is who asked. It travels with the invocation, not the declaration:
// one read method serves a person's refresh and a background poll.
type Origin string

const (
	OriginPerson     Origin = "person"
	OriginBackground Origin = "background"
	OriginSystem     Origin = "system"
)

// Invocation is what a declaration cannot carry: who asked and what the work
// dies with. The owner defaults to the declaration's.
type Invocation struct {
	Origin Origin
	Owner  Owner
}

// Outcome is the single settled answer a scope records.
type Outcome string

const (
	OutcomeCompleted      Outcome = "completed"
	OutcomeCancelled      Outcome = "cancelled"
	OutcomeExpired        Outcome = "expired"
	OutcomeRefused        Outcome = "refused"
	OutcomeNotStarted     Outcome = "not-started"
	OutcomeOutcomeUnknown Outcome = "outcome-unknown"
)

// Scope is the opaque lifetime a handle hands out. It carries the declaration's
// deadlines and the invocation's cancellation, and it owns the one settlement
// record for the invocation.
//
// Stage 2 records settlement; it does not gate the wire. The first of
// completion, cancellation and expiry wins the record, and every later attempt
// is dropped silently: a later attempt may come from a timer goroutine the
// caller cannot wait for, and nothing may be written after the operation has
// settled. A later stage arms the response path on the same record, which is
// why the race is exercised under -race now.
type Scope struct {
	op          Method
	decl        Declaration
	origin      Origin
	owner       Owner
	started     time.Time
	maxDeadline time.Time
	ctx         context.Context
	strict      bool
	settled     atomic.Bool
	outcome     atomic.Value // Outcome
	expiry      *time.Timer
	cancelReg   func() bool
	mu          sync.Mutex
	closed      bool
}

// Begin starts an invocation's scope. It never fails: a missing declaration is
// a programming error the handle itself prevents, so a zero handle panics.
func Begin(ctx context.Context, op Method, inv Invocation) *Scope {
	return begin(ctx, op, inv, false)
}

// BeginInternal starts a backend-initiated operation. Its origin is background
// unless the declaration says otherwise, and its owner is the declaration's.
func BeginInternal(ctx context.Context, op Method) *Scope {
	d := op.Declaration()
	return begin(ctx, op, Invocation{Origin: OriginBackground, Owner: d.Owner}, false)
}

func begin(ctx context.Context, op Method, inv Invocation, strict bool) *Scope {
	d := op.Declaration()
	if inv.Origin == "" {
		inv.Origin = OriginPerson
	}
	if inv.Owner == "" {
		inv.Owner = d.Owner
	}
	s := &Scope{
		op: op, decl: d, origin: inv.Origin, owner: inv.Owner,
		started: time.Now(), ctx: ctx, strict: strict,
	}
	// MAX IS THE ONLY DEADLINE STAGE 2 CAN OBSERVE. Turn is the wait for
	// resources and capacity; nothing queues on a catalogue resource yet, so a
	// turn timer would fire on every ordinary request and report a wait that
	// never happened. The stage that first makes an operation wait starts it.
	//
	// The timer and the cancellation registration are installed under the
	// mutex: either can fire before begin returns, and a settlement that wins
	// that race must not be followed by a timer nobody will stop.
	maxLifetime := d.Max.Duration()
	// A child operation's deadline is the minimum of its own and its parent's
	// remainder (ADR-8eej3e66 §3). Stage 2 applies it to the recorded expiry
	// only; nothing is cancelled.
	if parent, ok := ScopeFrom(ctx); ok {
		if remaining := time.Until(parent.maxDeadline); remaining > 0 && remaining < maxLifetime {
			maxLifetime = remaining
		}
	}
	s.maxDeadline = s.started.Add(maxLifetime)
	s.mu.Lock()
	if !s.settled.Load() {
		s.expiry = time.AfterFunc(maxLifetime, s.expire)
		if ctx != nil {
			s.cancelReg = context.AfterFunc(ctx, func() { s.Settle(OutcomeCancelled) })
		}
	} else {
		s.closed = true
	}
	s.mu.Unlock()
	return s
}

// ScopeFrom returns the scope an invocation put in the context, if any.
func ScopeFrom(ctx context.Context) (*Scope, bool) {
	if ctx == nil {
		return nil, false
	}
	s, ok := ctx.Value(scopeKey{}).(*Scope)
	return s, ok && s != nil
}

type scopeKey struct{}

// WithScope binds a scope to a context.
func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ID, Origin and Owner report what the scope is.
func (s *Scope) ID() ID                   { return s.decl.ID }
func (s *Scope) Origin() Origin           { return s.origin }
func (s *Scope) Owner() Owner             { return s.owner }
func (s *Scope) Declaration() Declaration { return s.decl }
func (s *Scope) Started() time.Time       { return s.started }

// Outcome returns the settled outcome and whether the scope has settled.
func (s *Scope) Outcome() (Outcome, bool) {
	v := s.outcome.Load()
	if v == nil {
		return "", false
	}
	outcome, ok := v.(Outcome)
	if !ok {
		// atomic.Value only ever receives an Outcome from Settle; a wrong
		// type is a programming error, not a value to invent.
		return "", false
	}
	return outcome, true
}

// Settle records the first outcome and drops every later one. It is the one
// settlement authority of section 4.
//
// NOTHING IS WRITTEN AFTER THE FIRST SETTLEMENT. A second settlement can arrive
// from a goroutine the caller cannot wait for — the max timer, or the
// context.AfterFunc callback that settles a cancellation — and a warning there
// races whoever reads the log after the operation returned (CI fdfc8d519).
// Routine outcomes write nothing whether they win or lose: a completion, a
// cancellation and a refusal are ordinary control-plane traffic. Only an
// expiry that WINS the settlement writes, and that is by definition an
// operation still running. A strict (test-built) scope panics on the second
// settlement instead, so the defect stays visible in a test.
func (s *Scope) Settle(outcome Outcome) bool {
	if !s.settled.CompareAndSwap(false, true) {
		if s.strict {
			prev, _ := s.Outcome()
			panic(fmt.Sprintf("operation/catalog: %s settled twice: %q then %q", s.decl.ID, prev, outcome))
		}
		return false
	}
	s.outcome.Store(outcome)
	s.stopTimers()
	if outcome == OutcomeExpired {
		log.From(s.logContext()).Warn("operation deadline expired",
			"operation", string(s.decl.ID), "owner", string(s.owner), "origin", string(s.origin),
			"max_ms", s.decl.Max.Millis, "elapsed_ms", time.Since(s.started).Milliseconds())
	}
	return true
}

// Complete settles a successful invocation.
func (s *Scope) Complete() bool { return s.Settle(OutcomeCompleted) }

// Refuse settles an invocation the submission refused before it started.
func (s *Scope) Refuse() bool { return s.Settle(OutcomeRefused) }

// Cancel settles an invocation its sender abandoned.
func (s *Scope) Cancel() bool { return s.Settle(OutcomeCancelled) }

func (s *Scope) expire() {
	if s.settled.Load() {
		return
	}
	// An expiry before any effect is the declaration's own not-started answer;
	// after one it is the declaration's expired outcome. Stage 2 has no effect
	// tracking, so it records the declared expired outcome and says so in the
	// log line, which is the whole of what this stage does with a deadline.
	s.Settle(OutcomeExpired)
}

func (s *Scope) logContext() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

func (s *Scope) stopTimers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.expiry != nil {
		s.expiry.Stop()
	}
	if s.cancelReg != nil {
		s.cancelReg()
	}
}

// Close releases a scope nobody settled (a handler that returned without
// answering). It settles as completed only for the record; the wire behaviour
// is unchanged.
func (s *Scope) Close() { s.Settle(OutcomeCompleted) }
