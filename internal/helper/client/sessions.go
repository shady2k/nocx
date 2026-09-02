package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// HostSessionID is the coordinator's view of a helper-owned session identity.
// It deliberately duplicates the wire shape instead of exposing proto types
// to callers above the helper client boundary.
type HostSessionID struct {
	Generation string `json:"generation"`
	Session    string `json:"session"`
}

type LaunchRecord struct {
	Shell       string `json:"shell"`
	Cwd         string `json:"cwd"`
	Pid         int    `json:"pid"`
	Pgid        int    `json:"pgid"`
	Cols        uint16 `json:"cols"`
	Rows        uint16 `json:"rows"`
	WindowBytes int64  `json:"windowBytes"`
}

type Observation struct {
	Source            string   `json:"source"`
	Cwd               string   `json:"cwd,omitempty"`
	Argv              []string `json:"argv"`
	ForegroundPgid    int      `json:"foregroundPgid,omitempty"`
	ForegroundCommand string   `json:"foregroundCommand,omitempty"`
	// StartTime is the KERNEL's start time for the process, RFC 3339 with
	// nanoseconds — not SessionEntry.StartedAt, which is when the helper
	// spawned it. The pair is the pid-reuse guard and the two are allowed to
	// disagree; see proto.Observation.StartTime.
	StartTime string `json:"startTime,omitempty"`
	Ppid      int    `json:"ppid,omitempty"`
	// State is proto.ProcessState's closed vocabulary, carried as a string
	// for the same reason Unavailable is: this boundary exposes no proto
	// types above it.
	State       string   `json:"state,omitempty"`
	Unavailable []string `json:"unavailable"`
}

type WindowSpan struct {
	Base    uint64 `json:"base"`
	Written uint64 `json:"written"`
}

type ExitStatus struct {
	Code   int    `json:"code"`
	Signal int    `json:"signal,omitempty"`
	At     string `json:"at"`
}

// Error lets the existing session.ExitOutcome mapping consume the helper's
// process status through the same ExitCode vocabulary as os/exec.ExitError.
// Signal remains diagnostic data on ExitStatus; the product's existing
// outcome is still the reported Code, including -1 for signal termination.
func (e *ExitStatus) Error() string {
	return fmt.Sprintf("helper session exited with code %d", e.Code)
}

func (e *ExitStatus) ExitCode() int { return e.Code }

type SessionEntry struct {
	HostSessionID   HostSessionID `json:"hostSessionId"`
	Workspace       string        `json:"workspace"`
	StartedAt       string        `json:"startedAt"`
	Launch          LaunchRecord  `json:"launch"`
	Observed        *Observation  `json:"observed"`
	Window          WindowSpan    `json:"window"`
	LifecycleWindow WindowSpan    `json:"lifecycleWindow"`
	Writer          *string       `json:"writer"`
	WriterEpoch     uint64        `json:"writerEpoch"`
	Exit            *ExitStatus   `json:"exit"`
}

// Sessions asks one helper generation for the sessions it currently holds.
// An empty answer is an answer and is returned as a non-nil empty slice.
func (c *Client) Sessions(ctx context.Context) ([]SessionEntry, error) {
	var result proto.SessionsResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpSessions, proto.SessionsParams{}, &result); err != nil {
		return nil, err
	}
	entries := make([]SessionEntry, 0, len(result.Sessions))
	for _, entry := range result.Sessions {
		entries = append(entries, mapSessionEntry(entry))
	}
	return entries, nil
}

// CloseSession deliberately ends one helper-hosted session. The helper owns
// the PTY, so closing this client connection is not a substitute: the daemon
// remains reachable and the session is removed only by this operation.
func (c *Client) CloseSession(ctx context.Context, id HostSessionID) error {
	return c.Call(ctx, proto.ServiceSession, proto.OpCloseSession, proto.CloseSessionParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(id.Generation),
			Session:    id.Session,
		},
	}, nil)
}

// Signal sends one signal to the helper-owned process group.
func (c *Client) Signal(ctx context.Context, id HostSessionID, sig int) error {
	return c.Call(ctx, proto.ServiceSession, proto.OpSignal, proto.SignalParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(id.Generation),
			Session:    id.Session,
		},
		Signal: sig,
	}, nil)
}

func mapSessionEntry(in proto.SessionEntry) SessionEntry {
	out := SessionEntry{
		HostSessionID: HostSessionID{Generation: string(in.Session.Generation), Session: in.Session.Session},
		Workspace:     string(in.Workspace),
		StartedAt:     in.StartedAt,
		Launch: LaunchRecord{
			Shell: in.Launch.Shell, Cwd: in.Launch.Cwd, Pid: in.Launch.Pid,
			Pgid: in.Launch.Pgid, Cols: in.Launch.Cols, Rows: in.Launch.Rows,
			WindowBytes: in.Launch.WindowBytes,
		},
		Window:          WindowSpan{Base: uint64(in.Window.Base), Written: uint64(in.Window.Written)},
		LifecycleWindow: WindowSpan{Base: uint64(in.LifecycleWindow.Base), Written: uint64(in.LifecycleWindow.Written)},
		WriterEpoch:     uint64(in.WriterEpoch),
	}
	if in.Writer != nil {
		writer := string(*in.Writer)
		out.Writer = &writer
	}
	if in.Observed != nil {
		out.Observed = &Observation{
			Source:            in.Observed.Source,
			Cwd:               in.Observed.Cwd,
			Argv:              make([]string, 0, len(in.Observed.Argv)),
			ForegroundPgid:    in.Observed.ForegroundPgid,
			ForegroundCommand: in.Observed.ForegroundCommand,
			StartTime:         in.Observed.StartTime,
			Ppid:              in.Observed.Ppid,
			State:             string(in.Observed.State),
			Unavailable:       make([]string, 0, len(in.Observed.Unavailable)),
		}
		out.Observed.Argv = append(out.Observed.Argv, in.Observed.Argv...)
		for _, diagnostic := range in.Observed.Unavailable {
			out.Observed.Unavailable = append(out.Observed.Unavailable, string(diagnostic))
		}
	}
	if in.Exit != nil {
		out.Exit = &ExitStatus{Code: in.Exit.Code, Signal: in.Exit.Signal, At: in.Exit.At}
	}
	return out
}

var ErrAttachmentClosed = errors.New("helper session attachment is closed")

// streamBytes widens a byte count to a stream offset. Every caller's count
// comes from copy, which never returns a negative value, and the check makes
// that readable rather than merely true.
func streamBytes(n int) proto.StreamOffset {
	if n < 0 {
		return 0
	}
	return proto.StreamOffset(n)
}

// AttachedSession is the coordinator-side data-plane view of one helper
// session. Its identity is the helper-minted session and subscriber pair.
type AttachedSession struct {
	client        *Client
	generation    proto.GenerationID
	session       [16]byte
	subscriber    [16]byte
	attachment    proto.AttachmentID
	epoch         proto.LeaseEpoch
	data          *stream
	lifecycleData *stream
	done          chan struct{}
	once          sync.Once
	mu            sync.Mutex
	// exitMu guards the immutable helper status. The notification records a
	// snapshot before finish closes done, and WaitErr keeps returning it after
	// close so the session layer can classify the complete interval.
	exitMu          sync.Mutex
	exit            *ExitStatus
	offset          proto.StreamOffset
	lifecycleOffset proto.StreamOffset
	// pendingReset and pendingLifecycleReset count the live resets that have
	// been RECEIVED but not yet REACHED by the reader, and they exist because
	// the helper moves its own cursor the moment it sends one. The interval
	// they mark has both ends: it opens when the notification arrives on the
	// connection's read loop and closes when the reader consumes the reset in
	// stream order. Inside it the cursor is frozen — the bytes still queued
	// ahead of the hole are handed over, because they are output the user has
	// not seen, but nothing is acked for them: their position is one the
	// helper has already abandoned, so an ack carrying it could only be
	// refused as behind.
	pendingReset          int
	pendingLifecycleReset int
	// holeObs is the coordinator's observer for a hole in this session's
	// output — see OnOutputHole. Guarded by mu; fired outside it.
	holeObs func(lost uint64, reason string)
}

// inbound is one item in an attachment's delivery order: bytes the wire
// carried, or the live reset that sits between them. It is one queue and not
// two because AD-9's reset is ordered WITH RESPECT TO the data — proto's own
// SessionReset says the reader "sees exactly which bytes the hole sits
// between" — and a reset applied out of band would move the cursor out from
// under bytes still waiting to be read.
type inbound struct {
	// payload is what Read hands to its caller.
	payload []byte
	// resetTo, when non-nil, is where the stream resumes. The cursor takes
	// this value as the item is consumed, and the item carries no stream
	// bytes of its own — so nothing is acked for it.
	resetTo *proto.StreamOffset
	// hole is what the helper says was lost, in its own Gap shape: the range
	// of this session's output that the host's window reclaimed before this
	// reader could receive it. Carried on the item rather than reported when
	// the notification arrives, because WHERE the hole sits is the whole of
	// what a reader needs from it, and that is only known in stream order.
	hole *proto.Gap
}

// Spawn creates a helper-owned shell and returns its inventory entry.
func (c *Client) Spawn(ctx context.Context, params proto.SpawnParams) (SessionEntry, error) {
	var result proto.SpawnResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpSpawn, params, &result); err != nil {
		return SessionEntry{}, err
	}
	return mapSessionEntry(result.Entry), nil
}

// Attach subscribes this coordinator to a helper-owned session and returns its
// raw PTY data channel. Registration happens before the request so data sent
// immediately after the helper accepts the subscriber cannot be lost.
func (c *Client) Attach(ctx context.Context, params proto.AttachParams) (*AttachedSession, error) {
	session, err := proto.SessionBytes(params.Session.Session)
	if err != nil {
		return nil, err
	}
	subscriberBytes, err := hex.DecodeString(string(params.Subscriber))
	if err != nil || len(subscriberBytes) != 16 {
		return nil, errors.New("helper session subscriber id must be 32 hex characters")
	}
	var subscriber [16]byte
	copy(subscriber[:], subscriberBytes)
	a := &AttachedSession{
		client: c, generation: params.Session.Generation,
		session: session, subscriber: subscriber,
		data: newStream(), lifecycleData: newStream(),
		done:   make(chan struct{}),
		offset: params.Offset, lifecycleOffset: params.LifecycleOffset,
	}
	c.mu.Lock()
	if _, exists := c.attachments[subscriber]; exists {
		c.mu.Unlock()
		return nil, errors.New("helper session subscriber already attached")
	}
	c.attachments[subscriber] = a
	c.mu.Unlock()

	var result proto.AttachResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpAttach, params, &result); err != nil {
		c.mu.Lock()
		delete(c.attachments, subscriber)
		c.mu.Unlock()
		a.finish()
		return nil, err
	}
	a.mu.Lock()
	a.attachment = result.Attachment
	a.epoch = result.Write.Epoch
	a.offset = result.Resume.From
	a.lifecycleOffset = result.LifecycleResume.From
	a.mu.Unlock()
	return a, nil
}

func (a *AttachedSession) deliver(payload []byte) {
	a.data.push(inbound{payload: append([]byte(nil), payload...)})
}

func (a *AttachedSession) deliverLifecycle(payload []byte) {
	a.lifecycleData.push(inbound{payload: append([]byte(nil), payload...)})
}

// stream is one attachment's inbound queue for one carrier, and its whole
// purpose is that PUSHING NEVER BLOCKS. The connection has a single read loop:
// it delivers the response every Call is waiting for, and it also hands PTY
// and lifecycle payloads to their attachment. While that hand-off could block,
// the read loop's progress depended on a consumer — and a consumer that acked
// synchronously was waiting on the read loop, so neither could move and the
// whole client wedged: every session on that helper, plus resize, detach and
// inventory. A bigger buffer only moves that deadlock.
//
// What bounds the queue is not this side but the helper's credit limit: it
// sends at most creditLimit unacked bytes, and only bytes that have been READ
// are ever acked, so an undrained carrier stalls the producer after one credit
// window rather than growing here without end. That is AD-10's backpressure
// applied where it belongs — at the source — instead of at a channel in the
// middle of the connection's only reader.
type stream struct {
	mu      sync.Mutex
	items   []inbound
	changed chan struct{}
}

func newStream() *stream { return &stream{changed: make(chan struct{})} }

// wait is taken BEFORE a pop, so an item pushed between the pop and the park
// is not slept through. Same generation-channel shape as the coordinator's own
// output ring.
func (s *stream) wait() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *stream) push(item inbound) {
	s.mu.Lock()
	s.items = append(s.items, item)
	s.signalLocked()
	s.mu.Unlock()
}

// unpop returns the unread remainder of a partially copied item to the FRONT
// of the queue. It replaces a goroutine that pushed the remainder back onto a
// channel, which could — and on a busy stream would — put it behind a payload
// that arrived later: bytes delivered out of order to a terminal.
func (s *stream) unpop(item inbound) {
	s.mu.Lock()
	s.items = append([]inbound{item}, s.items...)
	s.signalLocked()
	s.mu.Unlock()
}

func (s *stream) pop() (inbound, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) == 0 {
		return inbound{}, false
	}
	item := s.items[0]
	s.items = s.items[1:]
	return item, true
}

func (s *stream) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// take is the reader's end of the same rule, and the one that names the
// stream's closing event. The process ending and the OUTPUT ending are two
// events and the second is later (internal/helper/session/session.go's
// watchExit says so, and refuses to make the mistake again); a select over the
// queue and a closed done picks uniformly, so with k chunks buffered the odds
// of reading them all were 2^-k and what went missing was the last line the
// shell ever printed.
//
// Draining first is the shape taken over "let EOF come from the stream
// ending", because on this wire there is no end-of-stream marker to wait for:
// the helper's per-subscriber pump simply returns when the window is closed
// and drained, and a coordinator that waited for a frame that never comes
// would hang the tab open forever instead of losing a line. So the closing
// event is stated here rather than received: the queue is empty AND the
// session has ended.
func (a *AttachedSession) take(q *stream, closed <-chan struct{}) (inbound, bool) {
	for {
		wake := q.wait()
		if item, ok := q.pop(); ok {
			return item, true
		}
		select {
		case <-wake:
		case <-closed:
			item, ok := q.pop()
			return item, ok
		case <-a.done:
			item, ok := q.pop()
			return item, ok
		}
	}
}

// applyReset takes AD-9's live reset, in stream order. The helper reclaimed
// its bounded window past this reader's cursor, told it so, and moved that
// subscriber's sent and acked cursors to the base; this is the coordinator
// growing the ear for it. Two things are applied and both are required: the
// cursor moves to the base, which is what makes the next ack acceptable
// instead of behind, and the hole is STATED between the bytes it sits
// between, which is what keeps a reader from splicing two non-adjacent
// stretches of output together and calling it a screen.
//
// The statement is NOT in the byte stream, and an earlier version of this fix
// put it there — a line of nocx's own text between the two stretches. It read
// well and it was wrong twice over: session.output's contract says in its own
// words that "the backend hands back what the session printed and never
// interprets it (AD-6)", and a recording replays those bytes for ever, so a
// person scrolling back a week later would read a sentence the shell never
// printed and have no way to tell. The hole already has an owner in this
// codebase — internal/content's Gap, with its start, its end and its reason —
// and the coordinator reaches it through OnOutputHole.
func (a *AttachedSession) applyReset(r proto.SessionReset) {
	from := r.Resume.From
	if r.Stream == streamLifecycle {
		// No hole is reported on this carrier: the lifecycle stream is a
		// framed protocol, not a terminal, and there is no recording of it
		// for a gap to be a gap in. Its own resynchronisation belongs to the
		// channel that frames it.
		a.mu.Lock()
		a.pendingLifecycleReset++
		a.mu.Unlock()
		a.lifecycleData.push(inbound{resetTo: &from})
		return
	}
	a.mu.Lock()
	a.pendingReset++
	a.mu.Unlock()
	a.data.push(inbound{resetTo: &from, hole: r.Resume.Gap})
}

// streamLifecycle is the SessionReset.Stream value naming the lifecycle
// carrier; empty names the PTY output stream.
const streamLifecycle = "lifecycle"

// OnOutputHole installs the observer for a stretch of this session's output
// that never reached this coordinator, and it is the seam the durable record
// of that hole is written from (internal/transport's recorder, which owns the
// content store this package must not depend on).
//
// IT FIRES IN STREAM ORDER, from the reader's own goroutine, as the reset is
// consumed: after the last byte before the hole has been handed over and
// before the first byte after it. That is the whole reason it is a callback
// on the read path rather than a channel — the coordinator's ring is written
// by the same goroutine that reads here, so a hole reported from anywhere
// else would land at an offset that is not the hole's.
//
// `lost` is the helper's own count and never a second derivation of it: the
// helper computed it from this subscriber's cursor when it reclaimed the
// window, and two derivations of one number agree everywhere anybody looks.
// `reason` is the helper's word for the cause (proto.GapReasonWindow today),
// carried through untranslated so a generation naming a cause this one has
// never heard of is still reported as itself.
func (a *AttachedSession) OnOutputHole(f func(lost uint64, reason string)) {
	a.mu.Lock()
	a.holeObs = f
	a.mu.Unlock()
}

// reportHole tells the observer what the reset said was lost. A reset whose
// gap the helper did not name states no bounds, and inventing them here —
// from the distance between the cursor and the resume point — would be the
// second derivation OnOutputHole exists to avoid, so it is logged and not
// guessed. proto.Resume's contract is that the gap is present exactly when
// the reset is, so this is a defensive branch rather than a live one.
func (a *AttachedSession) reportHole(gap *proto.Gap) {
	a.mu.Lock()
	obs := a.holeObs
	a.mu.Unlock()
	if obs == nil {
		return
	}
	if gap == nil || gap.End <= gap.Start {
		a.client.log.Warn("session reset named no gap; the hole cannot be recorded",
			"session", proto.SessionHex(a.session))
		return
	}
	obs(uint64(gap.End-gap.Start), gap.Reason)
}

func (a *AttachedSession) recordExit(status proto.SessionExitStatus) {
	snapshot := &ExitStatus{Code: status.Code, Signal: status.Signal, At: status.At}
	a.exitMu.Lock()
	if a.exit == nil {
		a.exit = snapshot
	}
	a.exitMu.Unlock()
}

// WaitErr exposes the helper's recorded process status through the optional
// session.Channel seam. The returned snapshot is copied so callers cannot
// mutate the status that remains valid until the attachment is closed.
func (a *AttachedSession) WaitErr() (error, bool) {
	a.exitMu.Lock()
	defer a.exitMu.Unlock()
	if a.exit == nil {
		return nil, false
	}
	snapshot := *a.exit
	return &snapshot, true
}

func (a *AttachedSession) finish() { a.once.Do(func() { close(a.done) }) }

func (a *AttachedSession) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	item, ok := a.take(a.data, nil)
	if !ok {
		return 0, io.EOF
	}
	if item.resetTo != nil {
		a.mu.Lock()
		a.offset = *item.resetTo
		if a.pendingReset > 0 {
			a.pendingReset--
		}
		a.mu.Unlock()
		// Here, and not when the notification arrived: this is the point in
		// the byte stream the hole sits at, and the observer's whole job is
		// to say where.
		a.reportHole(item.hole)
	}
	n := copy(p, item.payload)
	if n < len(item.payload) {
		a.data.unpop(inbound{payload: item.payload[n:]})
	}
	// A reset carries no stream bytes of its own, so it advances no cursor.
	// Neither do bytes read while a reset is still queued behind them — see
	// pendingReset.
	if n > 0 && item.resetTo == nil {
		advance := streamBytes(n)
		a.mu.Lock()
		frozen := a.pendingReset > 0
		if !frozen {
			a.offset += advance
		}
		offset := a.offset
		a.mu.Unlock()
		if !frozen {
			if err := a.client.Call(context.Background(), proto.ServiceSession, proto.OpAck,
				proto.AckParams{
					Subscriber: proto.SubscriberID(hex.EncodeToString(a.subscriber[:])),
					Session:    proto.HostSessionID{Generation: a.generation, Session: proto.SessionHex(a.session)},
					Offset:     offset,
				}, nil); err != nil {
				// A refused ack is not the end of a session, and treating
				// it as one is how a live shell was reported as ENDED. It
				// is expected after a reset: the helper moves its own
				// cursor the moment it sends the notification, so every
				// ack for bytes already in flight is behind by the time it
				// lands — a race the coordinator cannot win and does not
				// need to. The session ends when the process exits or the
				// transport dies, and both have their own owner.
				a.client.log.Warn("session ack refused", "err", err,
					"session", proto.SessionHex(a.session), "offset", uint64(offset))
			}
		}
	}
	return n, nil
}

type attachedLifecycle struct {
	session *AttachedSession
	closed  chan struct{}
	once    sync.Once
}

func (l *attachedLifecycle) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	item, ok := l.session.take(l.session.lifecycleData, l.closed)
	if !ok {
		return 0, io.EOF
	}
	if item.resetTo != nil {
		l.session.mu.Lock()
		l.session.lifecycleOffset = *item.resetTo
		if l.session.pendingLifecycleReset > 0 {
			l.session.pendingLifecycleReset--
		}
		l.session.mu.Unlock()
	}
	n := copy(p, item.payload)
	if n < len(item.payload) {
		l.session.lifecycleData.unpop(inbound{payload: item.payload[n:]})
	}
	if n > 0 {
		advance := streamBytes(n)
		l.session.mu.Lock()
		frozen := l.session.pendingLifecycleReset > 0
		if !frozen {
			l.session.lifecycleOffset += advance
		}
		offset := l.session.lifecycleOffset
		ptyOffset := l.session.offset
		l.session.mu.Unlock()
		if frozen {
			return n, nil
		}
		if err := l.session.client.Call(context.Background(), proto.ServiceSession, proto.OpAck,
			proto.AckParams{
				Subscriber: proto.SubscriberID(hex.EncodeToString(l.session.subscriber[:])),
				Session:    proto.HostSessionID{Generation: l.session.generation, Session: proto.SessionHex(l.session.session)},
				Offset:     ptyOffset, LifecycleOffset: &offset,
			}, nil); err != nil {
			// Not fatal, for the reason the PTY reader's ack is not.
			l.session.client.log.Warn("lifecycle ack refused", "err", err,
				"session", proto.SessionHex(l.session.session), "offset", uint64(offset))
		}
	}
	return n, nil
}

func (l *attachedLifecycle) Write(p []byte) (int, error) {
	select {
	case <-l.closed:
		return 0, ErrAttachmentClosed
	case <-l.session.done:
		return 0, ErrAttachmentClosed
	default:
	}
	frame := proto.EncodeSessionFrame(proto.SessionFrame{
		Session: l.session.session, Subscriber: l.session.subscriber, Payload: p,
	})
	l.session.client.writeMu.Lock()
	defer l.session.client.writeMu.Unlock()
	if _, err := l.session.client.conn.Stdin().Write(proto.EncodeFrame(proto.TypeLifecycleData, 0, 0, frame)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (l *attachedLifecycle) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (a *AttachedSession) Lifecycle() io.ReadWriteCloser {
	return &attachedLifecycle{session: a, closed: make(chan struct{})}
}

func (a *AttachedSession) Write(p []byte) (int, error) {
	select {
	case <-a.done:
		return 0, ErrAttachmentClosed
	default:
	}
	a.mu.Lock()
	epoch := a.epoch
	a.mu.Unlock()
	if epoch == 0 {
		return 0, errors.New("helper session attachment has no write lease")
	}
	frame := proto.EncodeSessionFrame(proto.SessionFrame{
		Session: a.session, Subscriber: a.subscriber, Epoch: epoch, Payload: p,
	})
	a.client.writeMu.Lock()
	defer a.client.writeMu.Unlock()
	// The inner session frame is the ENVELOPE'S payload, never the lane's:
	// from the first write until this attachment is closed, every byte this
	// method puts on the lane is inside exactly one TypeSessionData frame —
	// the way attachedLifecycle.Write wraps its own in TypeLifecycleData, and
	// the way every other producer on this wire wraps its own.
	if _, err := a.client.conn.Stdin().Write(proto.EncodeFrame(proto.TypeSessionData, 0, 0, frame)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (a *AttachedSession) Resize(ctx context.Context, cols, rows, _, _ uint16) error {
	return a.client.Call(ctx, proto.ServiceSession, proto.OpResize, proto.ResizeParams{
		Session: proto.HostSessionID{Generation: a.generation, Session: proto.SessionHex(a.session)},
		Cols:    cols, Rows: rows,
	}, nil)
}

func (a *AttachedSession) Done() <-chan struct{} { return a.done }

func (a *AttachedSession) Close() error {
	a.client.mu.Lock()
	delete(a.client.attachments, a.subscriber)
	a.client.mu.Unlock()
	a.mu.Lock()
	attachment := a.attachment
	a.mu.Unlock()
	a.finish()
	return a.client.Call(context.Background(), proto.ServiceSession, proto.OpDetach,
		proto.DetachParams{Attachment: attachment}, nil)
}
