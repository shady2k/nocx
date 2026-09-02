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

// AttachedSession is the coordinator-side data-plane view of one helper
// session. Its identity is the helper-minted session and subscriber pair.
type AttachedSession struct {
	client        *Client
	generation    proto.GenerationID
	session       [16]byte
	subscriber    [16]byte
	attachment    proto.AttachmentID
	epoch         proto.LeaseEpoch
	data          chan []byte
	lifecycleData chan []byte
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
		data: make(chan []byte, 64), lifecycleData: make(chan []byte, 64),
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
	copyPayload := append([]byte(nil), payload...)
	select {
	case a.data <- copyPayload:
	case <-a.done:
	}
}

func (a *AttachedSession) deliverLifecycle(payload []byte) {
	copyPayload := append([]byte(nil), payload...)
	select {
	case a.lifecycleData <- copyPayload:
	case <-a.done:
	}
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
	select {
	case data := <-a.data:
		n := copy(p, data)
		if n < len(data) {
			go func() {
				select {
				case a.data <- data[n:]:
				case <-a.done:
				}
			}()
		}
		if n > 0 {
			a.mu.Lock()
			a.offset += proto.StreamOffset(n)
			offset := a.offset
			a.mu.Unlock()
			if err := a.client.Call(context.Background(), proto.ServiceSession, proto.OpAck,
				proto.AckParams{
					Subscriber: proto.SubscriberID(hex.EncodeToString(a.subscriber[:])),
					Session:    proto.HostSessionID{Generation: a.generation, Session: proto.SessionHex(a.session)},
					Offset:     offset,
				}, nil); err != nil {
				a.finish()
				return n, err
			}
		}
		return n, nil
	case <-a.done:
		return 0, io.EOF
	}
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
	select {
	case data := <-l.session.lifecycleData:
		n := copy(p, data)
		if n < len(data) {
			go func() {
				select {
				case l.session.lifecycleData <- data[n:]:
				case <-l.session.done:
				}
			}()
		}
		if n > 0 {
			l.session.mu.Lock()
			l.session.lifecycleOffset += proto.StreamOffset(n)
			offset := l.session.lifecycleOffset
			ptyOffset := l.session.offset
			l.session.mu.Unlock()
			if err := l.session.client.Call(context.Background(), proto.ServiceSession, proto.OpAck,
				proto.AckParams{
					Subscriber: proto.SubscriberID(hex.EncodeToString(l.session.subscriber[:])),
					Session:    proto.HostSessionID{Generation: l.session.generation, Session: proto.SessionHex(l.session.session)},
					Offset:     ptyOffset, LifecycleOffset: &offset,
				}, nil); err != nil {
				l.session.finish()
				return n, err
			}
		}
		return n, nil
	case <-l.closed:
		return 0, io.EOF
	case <-l.session.done:
		return 0, io.EOF
	}
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
