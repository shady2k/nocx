package diagnostics

// The open's own record: where it is, how long each stage took, and who ended
// what (nocx-3an1s.1.8).
//
// # Why this exists
//
// On 2026-11-05 a saved SSH open on the owner's machine neither completed nor
// failed: the log said the tool socket was prepared, then `lifecycle channel
// lost cause=closed` 30 ms later, then nothing. There was no line saying what
// the open was waiting on, and `cause=closed` did not say who had closed it.
// Separately, "The terminal is busy" appeared while a server had been running
// for days, and the shipped log held no line about the refusal at all.
//
// So there are two records here, both bounded and both in memory only:
//
//   - one per open, carrying every stage it entered and left with its
//     duration, its trace id and its outcome — and a line at a bounded cadence
//     for as long as it waits;
//   - one per control-plane refusal, carrying the method, the admission
//     scope, and WHO held that scope and for how long.
//
// # What it must never carry
//
// This record feeds Copy diagnostics, which a person pastes into an issue,
// a chat or a mail. It therefore holds no host, user, address, path,
// fingerprint or typed input. A destination is a per-run label ("connection
// 1"); the only other identifiers are the W3C trace id, the wire request tag
// (connection number + method + JSON-RPC id, none of which name anything
// outside this process) and durations.
//
// # Cost
//
// Nothing here is on the data plane (AD-1): no PTY byte and no screen frame
// passes through this package (AD-6). A stage transition takes one mutex, one
// append and one log line. A pending open costs one timer and a line every
// 30 s after the first 5 s. The rings are fixed size.

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// The stages an open passes through. They are the vocabulary the owner asked
// for, spelled once, so the log, the record and the text cannot disagree.
const (
	StageDial = "dial"
	StageAuth = "authentication"
	// StageProbe is the selection dial: one authenticated connection to the
	// far host to ask what platform it is and whether an artifact exists for
	// it. It is the first thing a far open does inside the dial, and it was
	// invisible until nocx-b3j8r: the gap it hides is a handshake plus one
	// exec.
	StageProbe = "helper-probe"
	// StageInstall is the far helper's install and completeness check. It was
	// recorded under StageAuth before nocx-b3j8r, so a record read
	// "authentication 22 s" over work that had nothing to do with signing in.
	StageInstall    = "helper-install"
	StageHold       = "connection-hold"
	StagePublish    = "integration-publish"
	StageToolSocket = "tool-socket"
	StageSpawn      = "spawn"
	StageLifecycle  = "lifecycle-channel"
	StageAdoption   = "adoption"
)

// Bounds. Every one of these is a number a reviewer can check against the
// text; none of them grows with uptime.
const (
	liveOpenCap       = 32
	recentOpenCap     = 32
	refusalCap        = 16
	labelCap          = 64
	openStagesCap     = 64
	goroutineStackCap = 512 * 1024
	goroutineTopN     = 8
	// goroutineSummaryCap is where the stack read stops being cheap enough to
	// do inline on the control plane's read loop.
	goroutineSummaryCap = 1500

	defaultFirstPendingAfter = 5 * time.Second
	defaultPendingEvery      = 30 * time.Second
)

// OpenInfo is what the transport knows when an open begins. Key is used ONLY
// to keep a label stable per destination for the life of the process; it is
// never rendered. Kind is "ssh" or "local".
type OpenInfo struct {
	Kind string
	Key  string
}

// StageRecord is one entered-and-left stage.
type StageRecord struct {
	Name  string
	Start time.Time
	Took  time.Duration
	Err   bool
	// Done is false only while the stage is the one the open is parked in.
	// Took alone cannot say that — a stage that took 0 ms is done.
	Done bool
}

// LifecycleLoss is one authenticated channel's ending, after the fact. The
// open record already carries a closer when the loss happened DURING an open;
// this ring is what answers for a channel that ended later, and what makes
// `cause=closed` answerable in the text as well as in the log.
type LifecycleLoss struct {
	At       time.Time
	Lane     string
	Cause    string
	ClosedBy string
}

// Refusal is one refused control request.
type Refusal struct {
	At      time.Time
	Method  string
	Scope   string
	Held    time.Duration // age of the oldest holder at refusal
	Holders []Holder
}

// Holder is one request holding an admission at the moment it was asked.
type Holder struct {
	Method  string
	Request string
	Trace   string
	Since   time.Time
	Held    time.Duration
}

// HelperStatus is what the coordinator can honestly say about this machine's
// helper. ConnectedFor is the age of the current connection, not the age of
// the daemon process: the daemon may outlive the connection, and claiming a
// process uptime we do not measure would be a guess.
type HelperStatus struct {
	Known        bool
	Connected    bool
	ConnectedFor time.Duration
	Failure      string // a bounded code, never a path or an address
}

// Facts is everything the text needs that does not come from the record.
type Facts struct {
	ServerStarted time.Time
	Helper        HelperStatus
	LogBytes      int64
	LogKnown      bool
	Admissions    []AdmissionView
}

// AdmissionView is one admission scope at the moment of asking.
type AdmissionView struct {
	Scope   string
	Holders []Holder
}

type openTraceKey struct{}

// Recorder is the bounded, in-memory record of this process's opens and
// refusals. One per server; safe for concurrent use.
type Recorder struct {
	mu sync.Mutex

	nextID uint64

	labels    map[string]string
	nextLabel int

	live   map[uint64]*Trace
	order  []uint64 // live ids, oldest first
	recent []*Trace

	refusals []Refusal
	losses   []LifecycleLoss

	now func() time.Time
}

// NewRecorder builds the process's record.
func NewRecorder() *Recorder {
	return &Recorder{
		labels: make(map[string]string),
		live:   make(map[uint64]*Trace),
		now:    time.Now,
	}
}

// WithRecorder installs the record on a request context. The dispatcher calls
// it once per frame, which is why no layer below it has to be handed a
// recorder: the stages and the refusals already have a context.
func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderKey, r)
}

type recorderKeyType struct{}

var recorderKey = recorderKeyType{}

func recorderFrom(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(recorderKey).(*Recorder)
	return r
}

// BeginOpen starts one open's record on the recorder the dispatcher installed
// on ctx, or does nothing when there is none (a backend open, a unit test).
// The nil trace makes every Stage a no-op and Finish free.
func BeginOpen(ctx context.Context, info OpenInfo) (context.Context, *Trace) {
	r := recorderFrom(ctx)
	if r == nil {
		return ctx, nil
	}
	return r.Begin(ctx, info)
}

// Begin starts one open's record and returns the context that carries it.
// The returned context also carries a trace id when the caller had none, so
// every stage line of one open is joinable even in a test that never crossed
// the wire.
func (r *Recorder) Begin(ctx context.Context, info OpenInfo) (context.Context, *Trace) {
	if r == nil {
		return ctx, nil
	}
	lg := log.From(ctx)
	if !log.SpanFrom(ctx).Valid() {
		ctx, _ = log.StartSpan(ctx)
		lg = log.From(ctx)
	}
	span := log.SpanFrom(ctx)

	now := r.now()
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	label := r.labelLocked(info.Key, info.Kind)
	t := &Trace{
		rec: r, id: id, label: label, kind: info.Kind,
		traceID: span.TraceID, requestID: requestIDFrom(ctx),
		started: now, current: "", currentSince: now,
		stop: make(chan struct{}),
	}
	t.ctx = ctx
	r.live[id] = t
	r.order = append(r.order, id)
	r.evictLiveLocked()
	r.mu.Unlock()

	ctx = context.WithValue(ctx, openTraceKey{}, t)
	lg.Info("open begin", "connection", label, "kind", info.Kind)
	go t.reportPending(defaultFirstPendingAfter, defaultPendingEvery)
	return ctx, t
}

// requestIDFrom is the wire request tag the dispatcher bound. It is a string
// like "11/open#5": a connection ordinal, a method and a JSON-RPC id, none of
// which names anything outside this process.
func requestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if tag, ok := ctx.Value(requestTagKey{}).(string); ok {
		return tag
	}
	return ""
}

type requestTagKey struct{}

// WithRequestTag carries the wire request tag in the context for the record.
// The dispatcher owns the one spelling of that tag (it is also `request_id`),
// so it passes the value rather than letting this package derive a second one.
func WithRequestTag(ctx context.Context, tag string) context.Context {
	return context.WithValue(ctx, requestTagKey{}, tag)
}

func (r *Recorder) labelLocked(key, kind string) string {
	if key == "" {
		if kind == "ssh" {
			return "connection ?"
		}
		return "local"
	}
	if label, ok := r.labels[key]; ok {
		return label
	}
	if len(r.labels) >= labelCap {
		return fmt.Sprintf("connection +%d", len(r.labels))
	}
	r.nextLabel++
	var label string
	if kind == "ssh" {
		label = fmt.Sprintf("connection %d", r.nextLabel)
	} else {
		label = fmt.Sprintf("local %d", r.nextLabel)
	}
	r.labels[key] = label
	return label
}

func (r *Recorder) evictLiveLocked() {
	for len(r.order) > liveOpenCap {
		oldest := r.order[0]
		r.order = r.order[1:]
		if t, ok := r.live[oldest]; ok {
			t.finishLocked(errEvicted)
			delete(r.live, oldest)
		}
	}
}

var errEvicted = fmt.Errorf("record evicted")

// Trace is one open's record. Nil-safe: every method no-ops on a nil trace,
// so a caller in a build with no recorder costs a nil check.
// stageFrame is one open stage's parent: its name and when it began, so a
// nested stage's leave restores the parent's ORIGINAL start and the parent's
// duration includes the child's.
type stageFrame struct {
	name  string
	start time.Time
}

type Trace struct {
	rec *Recorder
	id  uint64

	mu           sync.Mutex
	label        string
	kind         string
	traceID      string
	requestID    string
	started      time.Time
	current      string
	currentSince time.Time
	stack        []stageFrame
	stages       []*StageRecord
	finished     bool
	outcome      string
	detail       string
	publish      string
	integration  string
	closedBy     string
	stop         chan struct{}
	stopOnce     sync.Once
	// ctx is the context this open began in, after the trace was made: every
	// stage line is written through it, so it carries the request id and the
	// trace id without any call site passing them.
	ctx context.Context
}

func (t *Trace) reportPending(first, every time.Duration) {
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-timer.C:
		}
		t.mu.Lock()
		if t.finished {
			t.mu.Unlock()
			return
		}
		stage := t.current
		if stage == "" {
			stage = "start"
		}
		elapsed := t.rec.now().Sub(t.started)
		stageFor := t.rec.now().Sub(t.currentSince)
		t.mu.Unlock()
		// SAID AT INFO, in the shipped build, because this is the line a
		// person's own log will be read for when a pane never opens.
		log.From(t.ctx).Info("open pending",
			"connection", t.label, "kind", t.kind,
			"trace_id", t.traceID, "request_id", t.requestID,
			"stage", stage, "elapsed_ms", elapsed.Milliseconds(),
			"stage_ms", stageFor.Milliseconds())
		timer.Reset(every)
	}
}

// Stage enters one stage and returns the function that leaves it. The
// returned function takes the stage's error: nil is success. A stage line
// never prints the error's text — that can name a host or a path, and these
// lines are shipped; the failure record is where the error belongs.
func Stage(ctx context.Context, name string) func(error) {
	t, _ := ctx.Value(openTraceKey{}).(*Trace)
	if t == nil {
		return func(error) {}
	}
	return t.enter(name)
}

func (t *Trace) enter(name string) func(error) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return func(error) {}
	}
	now := t.rec.now()
	if t.current != "" {
		// A nested stage: the parent stays on the stack and is finalised by
		// its own leave, so its duration includes the child's — the honest
		// reading of "the dial took 1.2 s".
		t.stack = append(t.stack, stageFrame{name: t.current, start: t.currentSince})
	}
	t.stages = appendBounded(t.stages, &StageRecord{Name: name, Start: now})
	t.current = name
	t.currentSince = now
	t.mu.Unlock()

	log.From(t.ctx).Info("open stage", "connection", t.label, "kind", t.kind,
		"trace_id", t.traceID, "request_id", t.requestID,
		"stage", name, "phase", "enter")
	return func(err error) { t.leave(name, err) }
}

func (t *Trace) leave(name string, err error) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	now := t.rec.now()
	took := now.Sub(t.currentSince)
	for i := len(t.stages) - 1; i >= 0; i-- {
		if rec := t.stages[i]; rec.Name == name && !rec.Done {
			rec.Took = now.Sub(rec.Start)
			rec.Err = err != nil
			rec.Done = true
			took = rec.Took
			break
		}
	}
	if len(t.stack) > 0 {
		parent := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		if t.current == name {
			t.current = parent.name
			t.currentSince = parent.start
		}
	} else if t.current == name {
		t.current = ""
		t.currentSince = now
	}
	t.mu.Unlock()

	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	log.From(t.ctx).Info("open stage", "connection", t.label, "kind", t.kind,
		"trace_id", t.traceID, "request_id", t.requestID,
		"stage", name, "phase", "done", "duration_ms", took.Milliseconds(), "outcome", outcome)
}

func appendBounded(list []*StageRecord, rec *StageRecord) []*StageRecord {
	if len(list) >= openStagesCap {
		copy(list, list[1:])
		list[len(list)-1] = rec
		return list
	}
	return append(list, rec)
}

// findOpenLocked is the record whose leave has not run: the most recent one
// with this name and no duration.
func (t *Trace) findOpenLocked(name string) *StageRecord {
	for i := len(t.stages) - 1; i >= 0; i-- {
		if rec := t.stages[i]; rec.Name == name && !rec.Done {
			return rec
		}
	}
	return nil
}

// NotePublish records the integration publish's outcome on the open. Bounded
// vocabulary: "ok", "failed:<reason>".
func NotePublish(ctx context.Context, value string) {
	if t, ok := ctx.Value(openTraceKey{}).(*Trace); ok && t != nil {
		t.mu.Lock()
		t.publish = value
		t.mu.Unlock()
	}
}

// NoteIntegration records the integration axis' answer.
func NoteIntegration(ctx context.Context, value string) {
	if t, ok := ctx.Value(openTraceKey{}).(*Trace); ok && t != nil {
		t.mu.Lock()
		t.integration = value
		t.mu.Unlock()
	}
}

// NoteLifecycleClosed records WHO closed the lifecycle channel during this
// open. It is the fact `cause=closed` never carried (nocx-3an1s.1.8): a
// bounded code such as "open-rollback:spawn" or "session-ended".
func NoteLifecycleClosed(ctx context.Context, closer string) {
	if t, ok := ctx.Value(openTraceKey{}).(*Trace); ok && t != nil {
		t.mu.Lock()
		t.closedBy = closer
		t.mu.Unlock()
	}
}

// Finish ends the open's record: says its outcome at info, stops the pending
// reporter, and moves it to the bounded recent list.
func (t *Trace) Finish(err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.finished = true
	t.stopOnce.Do(func() { close(t.stop) })
	// A stage still open at the end is part of the record: it is the stage the
	// open died in, and its duration is the time spent there.
	if t.current != "" {
		if rec := t.findOpenLocked(t.current); rec != nil {
			rec.Took = t.rec.now().Sub(rec.Start)
			rec.Done = true
		}
		t.current = ""
	}
	if err != nil {
		t.outcome = "failed"
	} else {
		t.outcome = "opened"
	}
	now := t.rec.now()
	elapsed := now.Sub(t.started)
	stages := len(t.stages)
	last := t.lastStageLocked()
	lg := log.From(t.ctx)
	conn, kind := t.label, t.kind
	traceID, requestID := t.traceID, t.requestID
	publish, integration, closedBy, detail := t.publish, t.integration, t.closedBy, t.detail
	t.mu.Unlock()

	msg := "open finished"
	if err != nil {
		msg = "open failed"
	}
	lg.Info(msg, "connection", conn, "kind", kind, "trace_id", traceID,
		"request_id", requestID, "duration_ms", elapsed.Milliseconds(),
		"stages", stages, "last_stage", last, "publish", publish,
		"integration", integration, "lifecycle_closed_by", closedBy, "detail", detail)

	t.rec.mu.Lock()
	if _, ok := t.rec.live[t.id]; ok {
		delete(t.rec.live, t.id)
		t.rec.dropOrderLocked(t.id)
		t.rec.recent = append(t.rec.recent, t)
		if len(t.rec.recent) > recentOpenCap {
			t.rec.recent = t.rec.recent[len(t.rec.recent)-recentOpenCap:]
		}
	}
	t.rec.mu.Unlock()
}

func (t *Trace) finishLocked(err error) {
	t.finished = true
	t.outcome = "failed"
	t.detail = fmt.Sprint(err)
	t.stopOnce.Do(func() { close(t.stop) })
}

func (t *Trace) stageValuesLocked() []StageRecord {
	out := make([]StageRecord, 0, len(t.stages))
	for _, rec := range t.stages {
		out = append(out, *rec)
	}
	return out
}

func (t *Trace) lastStageLocked() string {
	if t.current != "" {
		return t.current
	}
	if len(t.stages) > 0 {
		return t.stages[len(t.stages)-1].Name
	}
	return ""
}

func (r *Recorder) dropOrderLocked(id uint64) {
	for i, v := range r.order {
		if v == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			return
		}
	}
}

// RecordRefusal appends one refusal to the bounded ring.
func (r *Recorder) RecordRefusal(now time.Time, method, scope string, holders []Holder) {
	if r == nil {
		return
	}
	held := time.Duration(0)
	for _, h := range holders {
		if h.Since.IsZero() {
			continue
		}
		if age := now.Sub(h.Since); age > held {
			held = age
		}
	}
	for i := range holders {
		if !holders[i].Since.IsZero() {
			holders[i].Held = now.Sub(holders[i].Since)
		}
	}
	r.mu.Lock()
	r.refusals = append(r.refusals, Refusal{At: now, Method: method, Scope: scope, Held: held, Holders: holders})
	if len(r.refusals) > refusalCap {
		r.refusals = r.refusals[len(r.refusals)-refusalCap:]
	}
	r.mu.Unlock()
}

// NoteLifecycleLoss appends one channel's ending to the bounded ring. The
// app's loss sink calls it; lane, cause and closer are all bounded codes.
func (r *Recorder) NoteLifecycleLoss(lane, cause, closedBy string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.losses = append(r.losses, LifecycleLoss{At: time.Now(), Lane: lane, Cause: cause, ClosedBy: closedBy})
	if len(r.losses) > refusalCap {
		r.losses = r.losses[len(r.losses)-refusalCap:]
	}
	r.mu.Unlock()
}

// Snapshot is the record at one moment.
type Snapshot struct {
	Generated time.Time
	Live      []OpenView
	Recent    []OpenView
	Refusals  []Refusal
	Losses    []LifecycleLoss
}

// OpenView is one open as the text reads it.
type OpenView struct {
	Label        string
	Kind         string
	Trace        string
	Request      string
	Started      time.Time
	Elapsed      time.Duration
	Current      string
	CurrentSince time.Duration
	Stages       []StageRecord
	Outcome      string
	Detail       string
	Publish      string
	Integration  string
	ClosedBy     string
}

// Snapshot copies the bounded record.
func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{Generated: time.Now()}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{Generated: r.now()}
	ids := append([]uint64(nil), r.order...)
	for _, id := range ids {
		if t, ok := r.live[id]; ok {
			s.Live = append(s.Live, t.viewLocked())
		}
	}
	for _, t := range r.recent {
		s.Recent = append(s.Recent, t.viewLocked())
	}
	s.Refusals = append([]Refusal(nil), r.refusals...)
	s.Losses = append([]LifecycleLoss(nil), r.losses...)
	return s
}

func (t *Trace) viewLocked() OpenView {
	t.mu.Lock()
	defer t.mu.Unlock()
	return OpenView{
		Label: t.label, Kind: t.kind, Trace: t.traceID, Request: t.requestID,
		Started: t.started, Elapsed: t.rec.now().Sub(t.started),
		Current: t.current, CurrentSince: t.rec.now().Sub(t.currentSince),
		Stages:  t.stageValuesLocked(),
		Outcome: t.outcome, Detail: t.detail, Publish: t.publish,
		Integration: t.integration, ClosedBy: t.closedBy,
	}
}

// GoroutineSummary is a cheap, bounded answer to "what is this process doing":
// how many goroutines there are and the most common top frames. It reads the
// runtime's stack dump once, under a byte cap, and never blocks on anything
// else.
func GoroutineSummary() (count int, top []string) {
	count = runtime.NumGoroutine()
	// THE STACK READ IS OPTIONAL, AND SAID SO (the brief's "if cheap"). A
	// process this large takes real milliseconds to dump; Copy diagnostics
	// runs on the read loop, so past this bound it answers the count and
	// leaves the frames to a profile.
	if count > goroutineSummaryCap {
		return count, nil
	}
	buf := make([]byte, goroutineStackCap)
	n := runtime.Stack(buf, true)
	text := string(buf[:n])
	tally := make(map[string]int)
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[0], "goroutine ") {
			continue
		}
		fn := strings.TrimSpace(lines[1])
		if i := strings.Index(fn, "("); i >= 0 {
			fn = fn[:i]
		}
		if fn != "" {
			tally[fn]++
		}
	}
	top = make([]string, 0, goroutineTopN)
	for fn, n := range tally {
		top = append(top, fmt.Sprintf("%s x%d", shortFn(fn), n))
	}
	sort.Slice(top, func(i, j int) bool { return top[i] > top[j] })
	if len(top) > goroutineTopN {
		top = top[:goroutineTopN]
	}
	return count, top
}

func shortFn(fn string) string {
	slash := strings.LastIndex(fn, "/")
	if slash >= 0 {
		fn = fn[slash+1:]
	}
	if len(fn) > 80 {
		fn = fn[:80]
	}
	return fn
}
