package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// One host session: a PTY, its process group, its bounded output window, its
// readers, and the one write capability among them (D3).
//
// # The interval this object draws, and both of its ends
//
// A session exists in the inventory FROM the moment its PTY exists — never
// before, so a spawn that failed leaves nothing to find — UNTIL close-session
// ends it (nocx-k6p18.7). A coordinator dying, a connection being replaced, an
// attachment being dropped and the PROCESS ITSELF exiting are all inside that
// interval: an exited session stays in the inventory carrying its status,
// because the coordinator that will want that status may not exist yet.

// Process is the PTY the helper owns: the seam between this package and
// internal/pty. It is an interface so the service can be tested without a real
// shell, and it is deliberately the SHAPE internal/pty.LocalPty already has —
// this package does not get to invent a second PTY vocabulary.
type Process interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error
	// Done closes when the process has ended and WaitErr has been recorded.
	Done() <-chan struct{}
	// WaitErr is what waiting on the process returned, and whether it has
	// been captured yet. Written before Done fires.
	WaitErr() (error, bool)
	Pid() int
	Shell() string
	// ForegroundProcessGroup is the group the terminal is currently giving
	// input to — the OS's answer to "what is running in here right now".
	ForegroundProcessGroup() (int, error)
}

// LifecycleProcess is implemented by a spawner when a session requested the
// authenticated descriptor channel. The helper moves this stream only.
type LifecycleProcess interface {
	Lifecycle() io.ReadWriteCloser
}

// ProcessGroupSignaller is the optional process-group control seam. The
// helper's local PTY implements it; tests and platforms without POSIX process
// groups can omit it and receive a named refusal.
type ProcessGroupSignaller interface {
	SignalProcessGroup(pgid int, sig syscall.Signal) error
}

// SpawnRequest is what the helper decided to launch, after the wire's params
// have been clamped and resolved. The SHELL is not in it: the composition root
// chooses it (see LocalSpawner), because no caller over the wire may name a
// command — host.Register refuses argv-shaped params, and this is the same
// rule one layer in.
type SpawnRequest struct {
	// SessionID is minted by the helper before launch so the in-memory shell
	// integration can identify the session without an installed script.
	SessionID string
	Cwd       string
	Env       map[string]string
	Cols      uint16
	Rows      uint16
	Lifecycle *proto.LifecycleLaunch
}

// Spawner starts a shell under a PTY. One implementation reaches internal/pty;
// a test's reaches nothing.
type Spawner interface {
	Spawn(req SpawnRequest) (Process, error)
}

// Inspector is the OS-evidence seam (D10), and it draws two distinctions
// rather than one.
//
// NIL means nobody could be asked at all — a pid the kernel no longer has, or
// a platform with no implementation. Never an empty record, because an empty
// record decodes as "we looked and there is nothing".
//
// A non-nil observation naming a diagnostic in Unavailable means it WAS asked
// and could not answer that one. That case used to look like the first, and it
// is the whole of nocx-k6p18.10: a reader that cannot tell them apart falls
// back to the launch record and shows where a shell started as though it were
// where the shell is (see inspect.go).
type Inspector interface {
	Observe(pid, foregroundPgid int) *proto.Observation
}

// Sink is the connection this helper is currently speaking on. It is a
// per-connection object, bound and released as connections come and go, and
// nothing durable holds a reference to it: an attachment dies with its
// connection, a host session does not (D2).
type Sink interface {
	SendSessionData(proto.SessionFrame) error
	SendLifecycleData(proto.SessionFrame) error
	SendNotification(proto.Notification) error
}

// push. It is AD-10's own constant and the same value internal/transport uses,
// restated here rather than imported because the helper may not depend on the
// coordinator's transport — a survival component that must stay compatible
// across generations has no business importing the WebSocket layer (D3).
//
// The floor invariant D8 asks for is enforced against it rather than merely
// documented: Limits refuses a MinWindowBytes at or below two of these, so a
// window's capacity always binds LATER than the credit window. With a window
// no larger than the allowance the pump could never fall behind the base, the
// reset path would be unreachable, and the credit accounting would silently
// become the only bound — a window that looks configured and is not.
const creditLimit = 64 * 1024

// subscriber is one reader of one session's output. The cursor is the
// SUBSCRIBER's and outlives the connection that carried it (D2), which is why
// `ack` is keyed by subscriber and session and never by attachment.
type subscriber struct {
	id  proto.SubscriberID
	raw [16]byte

	// sent is where this reader's pump has pushed to; acked is what the
	// reader confirmed receiving. sent − max(acked, base) is what is in
	// flight, and creditLimit bounds it.
	//
	// acked is floored at the window's base on a reset, and that is not
	// bookkeeping: without it a reader that reset was still charged for bytes
	// it was never sent, so it sent none, so nothing was acked, so the window
	// stayed shut. internal/transport paid for that lesson in creditFloor and
	// it is the same lesson here.
	sent  proto.StreamOffset
	acked proto.StreamOffset

	// lifecycleSent/lifecycleAcked are the cursor pair for the independent
	// lifecycle window. They never share PTY offsets.
	lifecycleSent  proto.StreamOffset
	lifecycleAcked proto.StreamOffset

	// cursorMu guards the reader cursor, which the serving pump advances while
	// ack requests may advance the confirmed side concurrently.
	cursorMu sync.Mutex
	wake     *gate

	stop          context.CancelFunc
	done          chan struct{}
	lifecycleWake *gate
	lifecycleDone chan struct{}
	// sink is the connection this subscriber's pump writes to. It is captured
	// at attach: a pump must not start writing to a connection that replaced
	// the one it was attached on.
	sink       Sink
	attachment proto.AttachmentID
}

type attachment struct {
	id         proto.AttachmentID
	subscriber proto.SubscriberID
	sink       Sink
}

// hostSession is one PTY and everything the helper knows about it.
type hostSession struct {
	lifecycleWin    *window
	id              proto.HostSessionID
	raw             [16]byte
	workspace       proto.WorkspaceID
	startedAt       time.Time
	launch          proto.LaunchRecord
	proc            Process
	win             *window
	log             *slog.Logger
	lifecycleBudget int64

	mu          sync.Mutex
	subs        map[proto.SubscriberID]*subscriber
	attachments map[proto.AttachmentID]*attachment
	writer      *proto.SubscriberID
	writerAtt   proto.AttachmentID
	epoch       proto.LeaseEpoch
	exit        *proto.SessionExitStatus
	stopped     bool
}

// pump moves bytes from the PTY into the window and nowhere else. Its interval
// starts before each proc.Read and ends after that read's bytes reach win.write
// (or win.close on EOF); throughout it MUST NOT take s.mu. write holds s.mu
// from lease validation until proc.Write returns, so making pump contend on
// that mutex would deadlock a blocked PTY write before the pump can drain it.
// It never interprets a byte — it reads them to MOVE them (AD-6).
func (s *hostSession) pump() {
	buf := make([]byte, pageSize)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			s.win.write(buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Warn("session output ended", "session", s.id.Session, "err", err)
			}
			s.win.close()
			return
		}
	}
}

// lifecyclePump moves raw lifecycle bytes from the helper-owned shell channel
// into its bounded window. No decoder or policy exists on this host.
func (s *hostSession) lifecyclePump(stream io.ReadWriteCloser) {
	buf := make([]byte, pageSize)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			s.lifecycleWin.write(buf[:n])
		}
		if err != nil {
			s.lifecycleWin.close()
			return
		}
	}
}

// watchExit records the process's end. The status is captured from WaitErr,
// which internal/pty writes BEFORE closing Done, so observing Done is enough
// to see it — no additional synchronisation, and no polling.
//
// It does NOT close the window, and that is the correction of a defect this
// code shipped for an hour. The process ending and the OUTPUT ending are two
// events and the second is later: bytes the shell wrote are still in the
// kernel's PTY buffer when it exits, and closing the window on the process's
// death raced the pump's own write of them. What the race produced was a
// reader that attached after an exit, found a window that was both closed and
// empty, concluded the stream was over and stopped — losing the last thing the
// shell ever printed, which is the single most valuable line in the session.
//
// So the window has ONE closer, the pump, and it closes it when the fd ends —
// the only event that means "no more bytes will ever arrive".
func (s *hostSession) watchExit(now func() time.Time, notify func(proto.SessionExit)) {
	<-s.proc.Done()
	err, _ := s.proc.WaitErr()
	status := proto.SessionExitStatus{Code: 0, At: proto.FormatTime(now())}
	if err != nil {
		status.Code = -1
		var coder interface{ ExitCode() int }
		if errors.As(err, &coder) {
			status.Code = coder.ExitCode()
		}
		var sig interface{ Signal() int }
		if errors.As(err, &sig) {
			status.Signal = sig.Signal()
		}
	}
	s.mu.Lock()
	s.exit = &status
	s.mu.Unlock()

	// What the window actually cost, said once per session. D8 names the cost
	// deliberately because it is spent on somebody else's machine — the worst
	// case on a host is its live session count times the bound, on a VM that
	// may be small — and a bound nobody ever measures is a bound nobody can
	// tell was set wrong.
	base, written := s.win.span()
	s.log.Info("session exited", "session", s.id.Session,
		"code", status.Code, "signal", status.Signal,
		"produced", uint64(written), "retained", uint64(written-base),
		"windowResidentBytes", s.win.allocated(), "windowBytes", s.launch.WindowBytes)

	notify(proto.SessionExit{Session: s.id, Status: status})
}

// entry builds this session's inventory row. The launch record is copied
// unchanged and the observation is taken fresh: authority and evidence, and
// the observation is nil when nobody could be asked (D10).
func (s *hostSession) entry(inspector Inspector) proto.SessionEntry {
	s.mu.Lock()
	writer := s.writer
	epoch := s.epoch
	exit := s.exit
	s.mu.Unlock()

	base, written := s.win.span()
	var lifecycleWindow proto.WindowSpan
	if s.lifecycleWin != nil {
		lbase, lwritten := s.lifecycleWin.span()
		lifecycleWindow = proto.WindowSpan{Base: lbase, Written: lwritten}
	}
	e := proto.SessionEntry{
		Session:         s.id,
		Workspace:       s.workspace,
		StartedAt:       proto.FormatTime(s.startedAt),
		Launch:          s.launch,
		Window:          proto.WindowSpan{Base: base, Written: written},
		LifecycleWindow: lifecycleWindow,
		Writer:          writer,
		WriterEpoch:     0,
		Exit:            exit,
	}
	if writer != nil {
		e.WriterEpoch = epoch
	}
	if inspector != nil && exit == nil {
		fg, err := s.proc.ForegroundProcessGroup()
		if err != nil {
			fg = 0
		}
		e.Observed = inspector.Observe(s.launch.Pid, fg)
	}
	return e
}

// attach makes one subscriber a reader from one offset, and answers where it
// actually stands and whether it may write. The three are separate fields for
// three separate concerns; conflating any two of them is the defect the ABI
// exists to prevent.
func (s *hostSession) attach(p proto.AttachParams, sink Sink, mintAttachment func() proto.AttachmentID, log *slog.Logger) (proto.AttachResult, error) {
	raw, err := proto.SessionBytes(string(p.Subscriber))
	lifecycleResume := proto.Resume{Resumed: true, From: p.LifecycleOffset}
	if s.lifecycleWin != nil {
		lbase, lwritten := s.lifecycleWin.span()
		lifecycleResume = proto.ResumeAt(lbase, lwritten, p.LifecycleOffset)
	}
	if err != nil {
		return proto.AttachResult{}, ErrBadSubscriber
	}

	base, written := s.win.span()
	resume := proto.ResumeAt(base, written, p.Offset)
	att := mintAttachment()
	ctx, stop := context.WithCancel(context.Background())

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		stop()
		return proto.AttachResult{}, ErrNoSuchSession
	}

	// One pump per subscriber: a second attach by the same subscriber
	// REPLACES the first, because a subscriber is one reader and two pumps on
	// one cursor would interleave frames into a stream that must stay ordered.
	// Remove the old attachment record while holding the same mutex as the
	// replacement, so a stale detach can never find the new subscriber.
	var old *subscriber
	oldWriter := false
	if previous, ok := s.subs[p.Subscriber]; ok {
		old = previous
		oldWriter = s.writer != nil && *s.writer == p.Subscriber && s.writerAtt == previous.attachment
		delete(s.subs, p.Subscriber)
		delete(s.attachments, previous.attachment)
		if oldWriter {
			s.writer = nil
			s.writerAtt = ""
		}
	}

	sub := &subscriber{
		id: p.Subscriber, raw: raw,
		sent: resume.From, acked: resume.From,
		lifecycleSent: lifecycleResume.From, lifecycleAcked: lifecycleResume.From,
		wake: newGate(), lifecycleWake: newGate(),
		stop: stop, done: make(chan struct{}), lifecycleDone: make(chan struct{}),
		sink: sink, attachment: att,
	}
	s.subs[p.Subscriber] = sub
	s.attachments[att] = &attachment{id: att, subscriber: p.Subscriber, sink: sink}
	if oldWriter && !p.RequestWrite {
		s.writer = &p.Subscriber
		s.writerAtt = att
	}

	grant := proto.WriteGrant{}
	if p.RequestWrite {
		if s.writer != nil && *s.writer != p.Subscriber {
			holder := *s.writer
			grant.Holder = &holder
		} else {
			s.epoch++
			s.writer = &p.Subscriber
			s.writerAtt = att
			grant.Granted = true
			grant.Epoch = s.epoch
		}
	}
	s.mu.Unlock()
	s.stopSubscriber(old)
	go s.serve(ctx, sub, log)
	if s.lifecycleWin != nil {
		go s.serveLifecycle(ctx, sub, log)
	} else {
		close(sub.lifecycleDone)
	}
	return proto.AttachResult{Attachment: att, Resume: resume, LifecycleResume: lifecycleResume, Write: grant}, nil
}

// serve is one subscriber's pump: it reads the window at the subscriber's own
// cursor and writes data frames to the connection it attached on. Several run
// at once, none blocks another, and none blocks the PTY — the window is
// capacity-reclaimed, so a stalled reader costs itself a reset and costs the
// producer nothing (D7, D8).
func (s *hostSession) serve(ctx context.Context, sub *subscriber, log *slog.Logger) {
	defer close(sub.done)
	for {
		if ctx.Err() != nil {
			return
		}

		// Take the wakeups BEFORE reading, so a write or an ack that lands
		// between the read and the park is not lost.
		dataChanged := s.win.changed()
		acked := sub.wake.wait()
		sub.cursorMu.Lock()
		sent, confirmed := sub.sent, sub.acked
		sub.cursorMu.Unlock()

		data, resume := s.win.read(sent)
		if resume.Reset {
			// The live reset: stated, never only logged. The reader clears and
			// resumes at the base, and its credit floor moves with it — a
			// reader charged for bytes it will never be sent can never reopen
			// its window.
			if err := sub.sink.SendNotification(proto.Notification{
				Service: proto.ServiceSession,
				Event:   proto.EventSessionReset,
				Params:  proto.SessionReset{Subscriber: sub.id, Session: s.id, Resume: resume},
			}); err != nil {
				log.Warn("session reset not delivered", "session", s.id.Session, "err", err)
				return
			}
			sub.cursorMu.Lock()
			sub.sent = resume.From
			sub.acked = resume.From
			sub.cursorMu.Unlock()
			continue
		}

		if len(data) > 0 {
			// The credit floor is the reader's OWN ack cursor, and nothing
			// else. internal/transport's creditFloor also takes the ring's
			// base, on the sound argument that bytes the ring no longer holds
			// cannot be in flight.
			if sent > confirmed && sent-confirmed >= creditLimit {
				select {
				case <-acked:
				case <-dataChanged:
				case <-ctx.Done():
					return
				}
				continue
			}
			if err := sub.sink.SendSessionData(proto.SessionFrame{
				Session: s.raw, Subscriber: sub.raw, Payload: data,
			}); err != nil {
				// The wire died. The attachment goes; the session, the window
				// and the process do not (D2).
				log.Warn("session data not delivered", "session", s.id.Session, "err", err)
				return
			}
			sub.cursorMu.Lock()
			sub.sent += proto.StreamOffset(len(data))
			sub.cursorMu.Unlock()
			continue
		}

		// Caught up. If the window is closed and nothing is left, the stream
		// is over and so is this pump.
		if s.win.isClosed() {
			_, written := s.win.span()
			sub.cursorMu.Lock()
			done := sub.sent >= written
			sub.cursorMu.Unlock()
			if done {
				return
			}
		}
		select {
		case <-dataChanged:
		case <-acked:
		case <-ctx.Done():
			return
		}
	}
}

func (s *hostSession) serveLifecycle(ctx context.Context, sub *subscriber, log *slog.Logger) {
	defer close(sub.lifecycleDone)
	for {
		if ctx.Err() != nil {
			return
		}
		changed := s.lifecycleWin.changed()
		acked := sub.lifecycleWake.wait()
		sub.cursorMu.Lock()
		sent, confirmed := sub.lifecycleSent, sub.lifecycleAcked
		sub.cursorMu.Unlock()
		data, resume := s.lifecycleWin.read(sent)
		if resume.Reset {
			_ = sub.sink.SendNotification(proto.Notification{
				Service: proto.ServiceSession, Event: proto.EventSessionReset,
				Params: proto.SessionReset{
					Subscriber: sub.id, Session: s.id,
					Resume: resume, Stream: "lifecycle",
				},
			})
			sub.cursorMu.Lock()
			sub.lifecycleSent, sub.lifecycleAcked = resume.From, resume.From
			sub.cursorMu.Unlock()
			continue
		}
		if len(data) > 0 {
			if sent > confirmed && sent-confirmed >= creditLimit {
				select {
				case <-acked:
				case <-changed:
				case <-ctx.Done():
					return
				}
				continue
			}
			if err := sub.sink.SendLifecycleData(proto.SessionFrame{
				Session: s.raw, Subscriber: sub.raw, Payload: data,
			}); err != nil {
				log.Warn("lifecycle data not delivered", "session", s.id.Session, "err", err)
				return
			}
			sub.cursorMu.Lock()
			sub.lifecycleSent += proto.StreamOffset(len(data))
			sub.cursorMu.Unlock()
			continue
		}
		if s.lifecycleWin.isClosed() {
			_, written := s.lifecycleWin.span()
			sub.cursorMu.Lock()
			done := sub.lifecycleSent >= written
			sub.cursorMu.Unlock()
			if done {
				return
			}
		}
		select {
		case <-changed:
		case <-acked:
		case <-ctx.Done():
			return
		}
	}
}

// ack advances one subscriber's confirmed offset. Validated exactly as the
// coordinator's ring validates its own, and for the same two reasons: an
// offset ahead of what was produced would free a reader to be sent bytes that
// do not exist, and one behind the cursor is a stale report.
func (s *hostSession) ack(sink Sink, id proto.SubscriberID, offset proto.StreamOffset) error {
	_, written := s.win.span()
	if offset > written {
		return ErrAckAhead
	}
	s.mu.Lock()
	sub, ok := s.subs[id]
	if !ok || sub.sink != sink {
		s.mu.Unlock()
		return ErrNotAttached
	}
	sub.cursorMu.Lock()
	if offset < sub.acked {
		sub.cursorMu.Unlock()
		s.mu.Unlock()
		return ErrAckBehind
	}
	sub.acked = offset
	sub.cursorMu.Unlock()
	s.mu.Unlock()
	sub.wake.signal()
	return nil
}

func (s *hostSession) ackLifecycle(sink Sink, id proto.SubscriberID, offset proto.StreamOffset) error {
	if s.lifecycleWin == nil {
		if offset != 0 {
			return ErrAckAhead
		}
		return ErrNotAttached
	}
	_, written := s.lifecycleWin.span()
	if offset > written {
		return ErrAckAhead
	}
	s.mu.Lock()
	sub, ok := s.subs[id]
	if !ok || sub.sink != sink {
		s.mu.Unlock()
		return ErrNotAttached
	}
	sub.cursorMu.Lock()
	if offset < sub.lifecycleAcked {
		sub.cursorMu.Unlock()
		s.mu.Unlock()
		return ErrAckBehind
	}
	sub.lifecycleAcked = offset
	sub.cursorMu.Unlock()
	s.mu.Unlock()
	sub.lifecycleWake.signal()
	return nil
}

// detach drops one attachment and reports whether it was holding the write
// capability, because that is the fact the next caller acts on.
func (s *hostSession) detach(sink Sink, att proto.AttachmentID) (bool, bool) {
	s.mu.Lock()
	entry, ok := s.attachments[att]
	if !ok || entry.sink != sink {
		s.mu.Unlock()
		return false, false
	}
	delete(s.attachments, att)
	released := false
	if s.writerAtt == att && s.writer != nil {
		s.writer = nil
		s.writerAtt = ""
		released = true
	}
	sub := s.subs[entry.subscriber]
	delete(s.subs, entry.subscriber)
	s.mu.Unlock()

	s.stopSubscriber(sub)
	return released, true
}

// stopSubscriber ends one pump and waits for it, so a replaced pump can never
// write a frame after its replacement has written one.
func (s *hostSession) stopSubscriber(sub *subscriber) {
	if sub == nil {
		return
	}
	sub.stop()
	sub.wake.signal()
	sub.lifecycleWake.signal()
	<-sub.done
	<-sub.lifecycleDone
}

// So do the attachments of every other connection, which is what makes this
// safe to call while other coordinators are still connected (D12).
//
// The write capability goes with it when its holder attached on that
// connection — that is what "the write capability is released when its
// connection closes, so a replacing coordinator can take it without
// arbitration" means — and stays put when the holder attached on another.
//
// A nil sink releases everything, which is only shutdown (stop).
func (s *hostSession) releaseConnection(sink Sink) {
	s.mu.Lock()
	subs := make([]*subscriber, 0, len(s.subs))
	for id, sub := range s.subs {
		if sink != nil && sub.sink != sink {
			continue
		}
		subs = append(subs, sub)
		delete(s.subs, id)
	}
	gone := make(map[proto.SubscriberID]bool, len(subs))
	for _, sub := range subs {
		gone[sub.id] = true
	}
	for id, att := range s.attachments {
		if gone[att.subscriber] {
			delete(s.attachments, id)
		}
	}
	if s.writer != nil && gone[*s.writer] {
		s.writer = nil
		s.writerAtt = ""
	}
	s.mu.Unlock()

	for _, sub := range subs {
		s.stopSubscriber(sub)
	}
}

// write applies one inbound data frame to the PTY, if and only if it comes
// from the current holder of the write capability at the current lease epoch
// and from the connection that owns that attachment. It holds s.mu from
// validation through the return of s.proc.Write; lease transitions wait for
// that interval to end, and pump MUST NOT acquire s.mu during its own
// proc.Read-to-window-write interval.
func (s *hostSession) write(sink Sink, f proto.SessionFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.writer == nil {
		return ErrNoWriter
	}
	attachment, ok := s.attachments[s.writerAtt]
	if !ok || attachment.sink != sink || attachment.subscriber != *s.writer {
		return ErrNotTheWriter
	}
	held, err := proto.SessionBytes(string(*s.writer))
	if err != nil || held != f.Subscriber {
		return ErrNotTheWriter
	}
	if f.Epoch != s.epoch {
		return ErrStaleLease
	}
	_, werr := s.proc.Write(f.Payload)
	return werr
}

func (s *hostSession) writeLifecycle(sink Sink, f proto.SessionFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[proto.SubscriberID(proto.SessionHex(f.Subscriber))]
	if !ok || sub.sink != sink {
		return ErrNotAttached
	}
	lp, ok := s.proc.(LifecycleProcess)
	if !ok {
		return ErrNotAttached
	}
	carrier := lp.Lifecycle()
	if carrier == nil {
		return ErrNotAttached
	}
	_, err := carrier.Write(f.Payload)
	return err
}

// stop ends the session's own goroutines and closes the PTY. It is what the
// helper does on shutdown; ending a session on a caller's request is
// close-session and is nocx-k6p18.7's.
func (s *hostSession) stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()
	s.releaseConnection(nil)
	_ = s.proc.Close()
	s.win.close()
}
