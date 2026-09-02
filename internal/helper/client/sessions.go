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
	Unavailable       []string `json:"unavailable"`
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
	data          chan inbound
	lifecycleData chan inbound
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
	// this value as the item is consumed, and payload then carries the
	// statement of the hole rather than stream bytes — so it is NOT acked.
	resetTo *proto.StreamOffset
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
		data: make(chan inbound, 64), lifecycleData: make(chan inbound, 64),
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
	a.enqueue(a.data, inbound{payload: append([]byte(nil), payload...)})
}

func (a *AttachedSession) deliverLifecycle(payload []byte) {
	a.enqueue(a.lifecycleData, inbound{payload: append([]byte(nil), payload...)})
}

// enqueue hands one item to the reader. The room-first attempt is not an
// optimisation: `done` closes on the PROCESS event, and the helper goes on
// sending the window's remaining bytes after it — its window has one closer,
// the pump, and the fd ending is the only event that means no more bytes will
// arrive. A plain select against a closed done drops those bytes half the time
// with the queue half empty.
func (a *AttachedSession) enqueue(q chan inbound, item inbound) {
	select {
	case q <- item:
		return
	default:
	}
	select {
	case q <- item:
	case <-a.done:
	}
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
func (a *AttachedSession) take(q chan inbound, closed <-chan struct{}) (inbound, bool) {
	select {
	case item := <-q:
		return item, true
	default:
	}
	select {
	case item := <-q:
		return item, true
	case <-closed:
	case <-a.done:
	}
	select {
	case item := <-q:
		return item, true
	default:
		return inbound{}, false
	}
}

// applyReset takes AD-9's live reset, in stream order. The helper reclaimed
// its bounded window past this reader's cursor, told it so, and moved that
// subscriber's sent and acked cursors to the base; this is the coordinator
// growing the ear for it. Two things are applied and both are required: the
// cursor moves to the base, which is what makes the next ack acceptable
// instead of behind, and the hole is STATED to the reader between the bytes it
// sits between, which is what keeps the renderer from splicing two
// non-adjacent stretches of output together and calling it a screen.
//
// The statement is in-band because on this side of the boundary the byte
// stream is the only thing that reaches the renderer: the coordinator's own
// mid-stream reset (contracts, ipc.ts) is answered at attach and has no
// notification shape, so a control-plane reset here would be a wire break in
// three packages. What that costs is one line of nocx's own text in a
// recording; what the alternative costs is a soft degrade visible only in a
// log, which is the shape AGENTS.md names.
func (a *AttachedSession) applyReset(r proto.SessionReset) {
	from := r.Resume.From
	lost := uint64(0)
	if r.Resume.Gap != nil && r.Resume.Gap.End > r.Resume.Gap.Start {
		lost = uint64(r.Resume.Gap.End - r.Resume.Gap.Start)
	}
	if r.Stream == streamLifecycle {
		// No notice on this carrier: the lifecycle stream is a framed
		// protocol, not a terminal, and text spliced into it would be a
		// second defect rather than a statement of the first. Its own
		// resynchronisation belongs to the channel that frames it.
		a.mu.Lock()
		a.pendingLifecycleReset++
		a.mu.Unlock()
		a.enqueue(a.lifecycleData, inbound{resetTo: &from})
		return
	}
	a.mu.Lock()
	a.pendingReset++
	a.mu.Unlock()
	a.enqueue(a.data, inbound{resetTo: &from, payload: resetNotice(lost)})
}

// streamLifecycle is the SessionReset.Stream value naming the lifecycle
// carrier; empty names the PTY output stream.
const streamLifecycle = "lifecycle"

func resetNotice(lost uint64) []byte {
	if lost == 0 {
		return []byte("\r\n[nocx] output was dropped by the host's window; the stream continues here\r\n")
	}
	return fmt.Appendf(nil, "\r\n[nocx] the host's window dropped %d bytes of output; the stream continues here\r\n", lost)
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
	}
	n := copy(p, item.payload)
	if n < len(item.payload) {
		rest := inbound{payload: item.payload[n:]}
		go func() {
			select {
			case a.data <- rest:
			case <-a.done:
			}
		}()
	}
	// A reset's own payload states the hole; it is not stream bytes, so
	// it advances no cursor. Neither do bytes read while a reset is still
	// queued behind them — see pendingReset.
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
		rest := inbound{payload: item.payload[n:]}
		go func() {
			select {
			case l.session.lifecycleData <- rest:
			case <-l.session.done:
			}
		}()
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
