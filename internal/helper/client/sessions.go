package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/pty"
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
	// Cause is proto.SessionExitCause's wire spelling, carried as a plain
	// string for the reason every other field on this boundary is: nothing
	// above internal/helper/client sees a proto type. Empty is the ordinary
	// case (nocx-y6fh7 item 6, round 3).
	Cause string `json:"cause,omitempty"`
}

// Error lets the existing session.ExitOutcome mapping consume the helper's
// process status through the same ExitCode vocabulary as os/exec.ExitError.
// Signal remains diagnostic data on ExitStatus; the product's existing
// outcome is still the reported Code, including -1 for signal termination.
func (e *ExitStatus) Error() string {
	return fmt.Sprintf("helper session exited with code %d", e.Code)
}

func (e *ExitStatus) ExitCode() int { return e.Code }

// ExitCause exposes Cause through the same optional-interface seam
// session.ExitOutcome already probes ExitCode() through, so a keepalive-lost
// connection reads as Interrupted rather than an anonymous Exited (nocx-y6fh7
// item 6, round 3).
func (e *ExitStatus) ExitCause() string { return e.Cause }

// RemoteLaunch is the SSH branch of the wire's launch union
// (proto.SSHLaunchRecord, nocx-50w7p.4): a session whose process is a shell
// channel on a connection this machine's helper dialed.
//
// # Why it is a second field rather than a second Launch type
//
// The wire carries the union as `launch.kind` plus one populated branch, and
// this boundary projects it as TWO GO VALUES: `Launch` for a process on this
// machine and `RemoteLaunch` for one that is not, distinguished by which is
// non-nil. That is a projection and not a second encoding — nothing above this
// boundary switches on a string tag, and the two branches are two different
// sets of facts rather than two spellings of one.
//
// It carries no pid, no pgid and no cwd, by CONSTRUCTION and not by omission:
// the process is on another machine, its pid belongs to that machine's
// namespace, and the directory it started in is that machine's answer. The
// wire's ssh branch has no such keys at all, so nothing here can invent one.
type RemoteLaunch struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	IdentityRef string `json:"identityRef"`
	Shell       string `json:"shell"`
	Cwd         string `json:"cwd"`
	Cols        uint16 `json:"cols"`
	Rows        uint16 `json:"rows"`
	WindowBytes int64  `json:"windowBytes"`
}

type SessionEntry struct {
	HostSessionID HostSessionID `json:"hostSessionId"`
	Workspace     string        `json:"workspace"`
	StartedAt     string        `json:"startedAt"`
	// Launch is the LOCAL branch of the wire's launch union: the record a
	// helper-hosted local pane has always had, unchanged.
	//
	// IT IS ABSENT — never a record of zeros — when RemoteLaunch is present, and
	// that absence is load-bearing (nocx-s8mfn). The contract this DTO feeds
	// (contracts/sessions.inventory.schema.json) carries the union and requires
	// exactly one branch, because a reader that found a `launch` record beside a
	// remote one would be reading a pid of 0: the kernel's scheduler rather than
	// the process the helper spawned, on a machine the session is not on. The
	// projection is therefore "one value the other nil" — what the wire's own
	// oneOf says — rather than a filled-in local record with a zero in it.
	Launch *LaunchRecord `json:"launch,omitempty"`
	// RemoteLaunch is the SSH branch, present exactly when this session's
	// process is a remote shell channel. When it is present, Launch is nil:
	// there is no process on this machine to describe, and the wire sends no
	// local record for one.
	RemoteLaunch    *RemoteLaunch `json:"remoteLaunch,omitempty"`
	Observed        *Observation  `json:"observed"`
	Window          WindowSpan    `json:"window"`
	LifecycleWindow WindowSpan    `json:"lifecycleWindow"`
	Writer          *string       `json:"writer"`
	WriterEpoch     uint64        `json:"writerEpoch"`
	Exit            *ExitStatus   `json:"exit"`
}

// IsRemote reports whether this session's process is on a remote host.
func (e SessionEntry) IsRemote() bool { return e.RemoteLaunch != nil }

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

// ErrLifecycleAdoptUnsupported is a helper generation that does not know the
// adopt-lifecycle op. It is a fact about the GENERATION, not a failure of the
// session: two generations are resident at once by design, and a session held
// by an older one can be taken back — its output is live and its ledger is
// restored — with no way to re-establish its lifecycle channel. The caller
// turns it into the statement the product shows, never into silence.
var ErrLifecycleAdoptUnsupported = errors.New("helper: this generation does not hand back a session lifecycle launch")

// AdoptLifecycle asks the helper for the lifecycle identity it spawned a
// session's shell with, so a REPLACING coordinator can take over the domain
// that shell is still speaking on (nocx-k6p18.31).
//
// Three answers, and they are three different situations:
//
//	(launch, nil)   adopt it; the shell has been speaking to nobody.
//	(nil, nil)      this session has no lifecycle channel. A conventional
//	                pane, correct as it stands, and nothing to state.
//	(nil, err)      the question could not be answered. ErrLifecycleAdopt-
//	                Unsupported for an older generation; anything else is
//	                the connection. Either way the pane is degraded and the
//	                product says so.
func (c *Client) AdoptLifecycle(ctx context.Context, id HostSessionID) (*proto.LifecycleLaunch, error) {
	var result proto.AdoptLifecycleResult
	err := c.Call(ctx, proto.ServiceSession, proto.OpAdoptLifecycle, proto.AdoptLifecycleParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(id.Generation),
			Session:    id.Session,
		},
	}, &result)
	if err != nil {
		var refusal *RefusalError
		if errors.As(err, &refusal) && refusal.Code == proto.ErrCodeUnknownOp {
			return nil, ErrLifecycleAdoptUnsupported
		}
		return nil, err
	}
	return result.Lifecycle, nil
}

// Signal sends one signal to a helper-owned process group. A zero pgid means
// the session's own group; a named one is the addressee a stop ladder resolved
// once and keeps (proto.SignalParams.Pgid).
func (c *Client) Signal(ctx context.Context, id HostSessionID, pgid, sig int) error {
	return c.Call(ctx, proto.ServiceSession, proto.OpSignal, proto.SignalParams{
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(id.Generation),
			Session:    id.Session,
		},
		Signal: sig,
		Pgid:   pgid,
	}, nil)
}

func mapSessionEntry(in proto.SessionEntry) SessionEntry {
	out := SessionEntry{
		HostSessionID: HostSessionID{Generation: string(in.Session.Generation), Session: in.Session.Session},
		Workspace:     string(in.Workspace),
		StartedAt:     in.StartedAt,
		Window:        WindowSpan{Base: uint64(in.Window.Base), Written: uint64(in.Window.Written)},
		LifecycleWindow: WindowSpan{
			Base: uint64(in.LifecycleWindow.Base), Written: uint64(in.LifecycleWindow.Written),
		},
		WriterEpoch: uint64(in.WriterEpoch),
	}
	// The union is projected as two Go values, and exactly one of them is
	// populated — the wire enforces that with oneOf, so this switch is a
	// translation rather than a guess. A record that somehow carried neither
	// branch leaves BOTH zero, which is the honest projection of "this helper
	// described no process": it is not a local session with pid 0.
	switch {
	case in.Launch.Local != nil:
		out.Launch = &LaunchRecord{
			Shell: in.Launch.Local.Shell, Cwd: in.Launch.Local.Cwd, Pid: in.Launch.Local.Pid,
			Pgid: in.Launch.Local.Pgid, Cols: in.Launch.Local.Cols, Rows: in.Launch.Local.Rows,
			WindowBytes: in.Launch.Local.WindowBytes,
		}
	case in.Launch.SSH != nil:
		out.RemoteLaunch = &RemoteLaunch{
			Host: in.Launch.SSH.Host, Port: in.Launch.SSH.Port, User: in.Launch.SSH.User,
			IdentityRef: in.Launch.SSH.IdentityRef, Shell: in.Launch.SSH.Shell,
			Cwd: in.Launch.SSH.Cwd, Cols: in.Launch.SSH.Cols, Rows: in.Launch.SSH.Rows,
			WindowBytes: in.Launch.SSH.WindowBytes,
		}
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
		out.Exit = &ExitStatus{Code: in.Exit.Code, Signal: in.Exit.Signal, At: in.Exit.At, Cause: string(in.Exit.Cause)}
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
	exitMu sync.Mutex
	exit   *ExitStatus
	// exitFinalOffset is set only by AdoptExitStatus (nocx-isjh4): the
	// window offset a RE-ADOPTED, already-exited session can never advance
	// past, because the shell that would have advanced it is already gone.
	// Guarded alongside exit; nil for every other session, including one
	// that exits normally while THIS attachment is live (that path ends
	// through sessionExited/finish directly and needs no target — there is
	// always more that COULD arrive until the process is observed to end).
	exitFinalOffset *proto.StreamOffset
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
	// livenessObs is the coordinator's observer for this session's own
	// keepalive prober (nocx-y6fh7 item 6) — see OnLiveness. Guarded by mu;
	// fired outside it, on the same terms holeObs already is.
	livenessObs func(responsive bool, roundTripMS int64)
	// screenObs is the coordinator's consumer for this session's published
	// screen: one full session.frame document per revision, reassembled by
	// the connection's assembler before it reaches here. screenLostObs is
	// told, by name, when the carrier dropped an assembly — the invariant's
	// "or has been told it lost it". Both guarded by mu; fired outside it.
	screenObs     func(revision uint64, payload []byte)
	screenLostObs func(reason string)
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

// SpawnSSH opens a session whose process is a shell channel on a FAR host —
// a remote pane (nocx-50w7p.4). The helper dials through its own ssh client
// and answers with the same inventory entry every other session answers with,
// whose RemoteLaunch is populated instead of Launch.
//
// It returns the helper's refusal unchanged, and those refusals are the
// caller's to switch on: `no_ssh_client` (this machine's helper was built
// without an ssh client), `unreachable` / `rejected` / `needs-interactive` /
// `host-key-unknown` / `host-key-changed` (the destination, in the vocabulary
// the probe already reports), `vault_sealed` (the material could not be read),
// `no_auth_channel` (no coordinator connection to ask), `channel_refused` (the
// server would not give the helper a session or would not start the command)
// and `bad_params` (the request itself). None of them is a spawn failure: a
// failed spawn is `spawn_failed`, and telling the two apart is what keeps a
// person looking at the host rather than at their own request.
func (c *Client) SpawnSSH(ctx context.Context, params proto.SSHSpawnParams) (SessionEntry, error) {
	var result proto.SpawnResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpSpawnSSH, params, &result); err != nil {
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
	// A RESET AT ATTACH IS A HOLE LIKE ANY OTHER, and until nocx-k6p18.30 it
	// was the one hole that reached nobody. applyReset carries a mid-stream
	// reset to the observer through the queue, in stream order; an attach that
	// comes back Reset states exactly the same fact about exactly the same
	// stream — the window's base has moved past where this reader asked to
	// resume — and it is the FIRST thing true of this attachment. A
	// coordinator taking a session back after being replaced meets it every
	// time the host out-produced its window while nobody was listening, which
	// is the case the epic exists for.
	//
	// It goes through the same queue and the same reportHole, at the FRONT, so
	// there is one path from "bytes were lost" to the recording's Gap and not
	// two. The front matters: the attachment was registered before the call,
	// so payloads may already be queued behind it, and this loss precedes
	// every one of them.
	if result.Resume.Reset {
		from := result.Resume.From
		a.mu.Lock()
		a.pendingReset++
		a.mu.Unlock()
		a.data.unpop(inbound{resetTo: &from, hole: result.Resume.Gap})
	}
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

// OnLiveness registers the coordinator's observer for this session's own
// keepalive prober, exactly as OnOutputHole registers one for a hole: nil
// means nobody is watching, and this is ordinary for a local pane, which the
// helper never probes (session_service.go's own note on
// EventSessionLiveness).
func (a *AttachedSession) OnLiveness(f func(responsive bool, roundTripMS int64)) {
	a.mu.Lock()
	a.livenessObs = f
	a.mu.Unlock()
}

// OnScreenFrame registers the coordinator's consumer for this session's
// published screen. Nil means nobody is watching: assembled frames are then
// dropped at this door, and the transport that wants them registers first
// (see OnScreenLost for what happens to the ones the carrier itself lost).
func (a *AttachedSession) OnScreenFrame(f func(revision uint64, payload []byte)) {
	a.mu.Lock()
	a.screenObs = f
	a.mu.Unlock()
}

// OnScreenLost registers the coordinator's observer for a screen the carrier
// dropped — a superseded assembly, a bound refused. The reason is the
// assembler's own named error, carried through untranslated.
func (a *AttachedSession) OnScreenLost(f func(reason string)) {
	a.mu.Lock()
	a.screenLostObs = f
	a.mu.Unlock()
}

// deliverScreen hands one whole reassembled document to the observer, with
// the revision it was read at.
func (a *AttachedSession) deliverScreen(assembled *proto.AssembledScreenFrame) {
	a.mu.Lock()
	obs := a.screenObs
	a.mu.Unlock()
	if obs == nil {
		return
	}
	obs(assembled.Revision, assembled.Payload)
}

// reportScreenLost tells the observer what the carrier dropped and why.
func (a *AttachedSession) reportScreenLost(reason string) {
	a.mu.Lock()
	obs := a.screenLostObs
	a.mu.Unlock()
	if obs != nil {
		obs(reason)
	}
}

// reportLiveness tells the observer what a liveness notification said.
func (a *AttachedSession) reportLiveness(responsive bool, roundTripMS int64) {
	a.mu.Lock()
	obs := a.livenessObs
	a.mu.Unlock()
	if obs != nil {
		obs(responsive, roundTripMS)
	}
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

// AdoptExitStatus carries a status the HELPER already reported — through the
// inventory, in the same round trip — onto an attachment that will never see
// the notification for it.
//
// WHY IT EXISTS. `notifyExit` fires once, to whoever is bound at that moment
// (session.go's watchExit). A coordinator that was replaced while a shell was
// still running, and comes back after it exited, is not that coordinator: it
// attaches to a session the helper still holds, reads whatever the window kept,
// and reaches EOF. Without this the exit reads as a LOSS — "was interrupted",
// with no status — which is exactly the "unknown" the epic set out to end, for
// a build whose real exit code the helper has been holding the whole time.
//
// It is a CARRY and never a derivation. The value comes from the same helper's
// own inventory row in the same conversation as the attach, so there is one
// number with one owner, not two answers to "how did it end". It is refused
// once a status is already recorded, so a notification that does arrive is
// never overwritten by a staler inventory read.
//
// finalOffset is the SAME inventory row's window frontier (SessionEntry.
// Window.Written) — the offset this stream can never advance past, because
// the shell that would have advanced it is already gone. "Reads whatever the
// window kept, and reaches EOF" above describes the intent, not what Read
// does on its own: take() (this file) returns only on new data or on
// a.done, and nothing closes a.done for this attachment without this call —
// so before this fix a coordinator that re-adopted an already-exited,
// unattached session hung its reader forever instead of reaching EOF, and
// neither monitorExit nor EndSession ever ran for it (nocx-isjh4, closer 2's
// other door; found in review, not by a wire change — SessionEntry already
// carries Window on the existing wire). checkFullyDrained below is what
// actually closes a.done, once this attachment's own read cursor reaches
// finalOffset — called here for the (rare) case nothing is left to read at
// all, and again after every Read that moves the cursor toward it.
func (a *AttachedSession) AdoptExitStatus(status ExitStatus, finalOffset proto.StreamOffset) {
	a.exitMu.Lock()
	already := a.exit != nil
	if !already {
		snapshot := status
		a.exit = &snapshot
		target := finalOffset
		a.exitFinalOffset = &target
	}
	a.exitMu.Unlock()
	if !already {
		a.checkFullyDrained()
	}
}

// checkFullyDrained ends this attachment's session (nocx-isjh4) once its own
// read cursor has reached the point AdoptExitStatus named as the offset an
// already-exited session's window will never advance past. A no-op when no
// exit was ever adopted here (exitFinalOffset nil) — a session whose shell
// exits while this coordinator holds it ends through sessionExited/finish
// directly, with no target to compare against, because "more could still
// arrive" is true right up to that notification.
//
// Finishing here reaches the SAME monitorExit path a live coordinator's own
// shell exit does (both close a.done through finish), so a re-adopted,
// already-exited session releases its helper session exactly as closer 2
// already does for the case where a coordinator was attached the whole time.
func (a *AttachedSession) checkFullyDrained() {
	a.exitMu.Lock()
	target := a.exitFinalOffset
	a.exitMu.Unlock()
	if target == nil {
		return
	}
	a.mu.Lock()
	reached := a.offset >= *target
	a.mu.Unlock()
	if reached {
		a.finish()
	}
}

func (a *AttachedSession) recordExit(status proto.SessionExitStatus) {
	snapshot := &ExitStatus{Code: status.Code, Signal: status.Signal, At: status.At, Cause: string(status.Cause)}
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
	// Checked on every Read, not only when it moves the cursor: a caller
	// that reads with a zero-length buffer or hits a reset-only item still
	// deserves the check, and checkFullyDrained is itself a no-op unless
	// AdoptExitStatus named a target (nocx-isjh4).
	a.checkFullyDrained()
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

// WriteGranted reports whether this attachment holds the session's one write
// capability. It is the same fact Write refuses on and not a second
// derivation of it: the epoch is minted from 1, so zero names no grant.
//
// It is readable because a caller may need to decide BEFORE writing anything.
// A coordinator taking a session back after being replaced is exactly that
// caller: the helper serves a second coordinator rather than refusing it (D12)
// and answers the write request by naming the holder, so "the session is live
// and somebody else owns its keyboard" is a state that has to be told apart
// from "the session is mine". Adopting a session this coordinator cannot write
// to would put a pane on screen whose keystrokes go nowhere — a surface
// advertising what it cannot deliver.
func (a *AttachedSession) WriteGranted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.epoch != 0
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
	// One write may be larger than one frame may carry (a big paste), and
	// EncodeFrame panics above MaxFrameBytes rather than corrupting the wire.
	// So the payload is cut into consecutive session frames, each leaving
	// room for the session frame's own header inside the envelope; they are
	// written under one writeMu hold, so no other writer's frame lands
	// between them and the PTY receives the bytes in order.
	overhead := len(proto.EncodeSessionFrame(proto.SessionFrame{
		Session: a.session, Subscriber: a.subscriber, Epoch: epoch,
	}))
	chunk := proto.MaxFrameBytes - overhead
	a.client.writeMu.Lock()
	defer a.client.writeMu.Unlock()
	written := 0
	for written < len(p) || (len(p) == 0 && written == 0) {
		end := written + chunk
		if end > len(p) {
			end = len(p)
		}
		frame := proto.EncodeSessionFrame(proto.SessionFrame{
			Session: a.session, Subscriber: a.subscriber, Epoch: epoch, Payload: p[written:end],
		})
		// The inner session frame is the ENVELOPE'S payload, never the lane's:
		// from the first write until this attachment is closed, every byte this
		// method puts on the lane is inside exactly one TypeSessionData frame —
		// the way attachedLifecycle.Write wraps its own in TypeLifecycleData, and
		// the way every other producer on this wire wraps its own.
		if _, err := a.client.conn.Stdin().Write(proto.EncodeFrame(proto.TypeSessionData, 0, 0, frame)); err != nil {
			return written, err
		}
		if len(p) == 0 {
			break
		}
		written = end
	}
	return len(p), nil
}

func (a *AttachedSession) Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error {
	return a.client.Call(ctx, proto.ServiceSession, proto.OpResize, proto.ResizeParams{
		Session: proto.HostSessionID{Generation: a.generation, Session: proto.SessionHex(a.session)},
		Cols:    cols, Rows: rows,
		XPixel: xpixel, YPixel: ypixel,
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

// EndSession releases this attachment AND asks the helper to close the
// session itself, which is what gives its reserved window budget back
// (nocx-isjh4). It is a SEPARATE verb from Close and must stay one:
// internal/session.realSession.Close calls it for a session the coordinator
// is done with for good — the pane it was the pipe of has left the layout, a
// shell exit has been persisted, or the user asked to close it directly —
// while a caller that merely lost a re-adopt race (another coordinator holds
// the write lease, or this coordinator's own adopt failed after a successful
// attach) calls plain Close: the session stays live under whoever already
// holds it, and ending it there would be the exact defect this method exists
// to avoid causing anywhere else (see internal/app/session_readopt.go).
//
// The local bookkeeping Close performs is repeated here rather than
// delegated to it, so this sends ONE round trip to the helper — closing a
// session already implies detaching every attachment on it — instead of a
// detach followed by a redundant close.
//
// THE ORDER IS THE POINT (nocx-xn63t.6.4). The round trip runs FIRST, and
// the local bookkeeping — including a.finish(), which is what closes Done()
// — runs only once it returns. Before this, finish() ran first: Done()
// closed, and only the next line sent CloseSession. A caller reacting to
// Done() by tearing down the shared client — helperRegistry.SessionEnded
// legitimately does exactly that once no git binding holds the client open,
// internal/app/helper_git.go, nocx-xn63t.6.3 — could then close the
// transport before the close-session request had even reached the wire.
// The far helper never heard it, and kept the session in its own inventory
// until its unclaimed-session TTL swept it, minutes past any caller's
// remaining patience (e2e/remote-coordinator-reclaim.spec.ts measured it
// against a 60s bound). CloseSession's own failure — a dead transport,
// ErrLost — must still run the local half so this attachment does not hang
// forever, which is why the error is captured rather than returned early.
func (a *AttachedSession) EndSession(ctx context.Context) error {
	id := a.hostID()
	err := a.client.CloseSession(ctx, id)
	a.client.mu.Lock()
	delete(a.client.attachments, a.subscriber)
	a.client.mu.Unlock()
	a.finish()
	return err
}

// ── the signal seam (nocx-ie23r.3) ───────────────────────────────────────────
//
// THE WHOLE SEAM, OR NONE OF IT. internal/session reaches a channel's signal
// methods by optional-method ASSERTION, so a channel that answers some of them
// is not degraded — it is wrong, and it is wrong silently: nothing fails to
// compile, and what the product says instead is "nothing is running in this
// pane" about a command that plainly is (nocx-92gfl.4, twice, through two
// different missing methods). internal/app's lifecyclePTY wrapper stated that
// rule for the local pty it wrapped; this is the same rule for the channel
// that replaced it.
//
// WHY IT LANDS HERE NOW. Before nocx-ie23r.3 no helper-hosted session was ever
// asked to stop a job: the run-lease ladder only ever ran against a local pty
// the coordinator itself had forked. Now every local pane is a helper session,
// so the ladder's three questions have to be answerable over the wire or the
// stop button stops working on this machine.
//
// The ADDRESSEE IS RESOLVED ONCE AND KEPT, which is the whole point of
// ForegroundJob being separate from SignalForeground (nocx-uvac6.11): a shell
// that starts another job between SIGINT and SIGKILL must not be hit by the
// second, so the caller names a group and then signals that group.

// ErrNoForegroundJob is a session whose foreground group this generation
// cannot name: the OS could not be asked, or the helper's observation carries
// no foreground group. It is typed rather than silent so a caller can tell
// "there is nothing running" from "nobody could look" — the second is a
// diagnosis and the first is an answer.
var ErrNoForegroundJob = errors.New("helper: this session's foreground process group is not known")

// signalTimeout bounds one signal-seam call. It is a REQUEST bound and not a
// policy: the ladder's own timing is the transport's, and what this stops is a
// stop button that hangs on a helper that has gone quiet.
const signalTimeout = 5 * time.Second

func (a *AttachedSession) hostID() HostSessionID {
	return HostSessionID{Generation: string(a.generation), Session: proto.SessionHex(a.session)}
}

// ForegroundJob names the process group in front of this session's terminal,
// as the HELPER's own observation of the host reports it. It is evidence read
// from the operating system on the machine the shell is actually on, which is
// the only place the question can be answered — the coordinator may be on a
// different machine entirely.
func (a *AttachedSession) ForegroundJob() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), signalTimeout)
	defer cancel()
	entries, err := a.client.Sessions(ctx)
	if err != nil {
		return 0, err
	}
	id := a.hostID()
	for i := range entries {
		if entries[i].HostSessionID != id {
			continue
		}
		// The pid comes from the branch that HAS one: a remote session's
		// process is on another machine and its local record is absent, so a
		// session with no local branch compares against no shell group (0) —
		// which is the honest answer, and the observation is nil for such a
		// session anyway (the helper has no pid here to inspect).
		launchPID := 0
		if entries[i].Launch != nil {
			launchPID = entries[i].Launch.Pid
		}
		return classifyForegroundObservation(entries[i].Observed, launchPID)
	}
	// The helper answered and does not hold this session. Said as its own
	// sentence rather than as ErrNoForegroundJob: "there is no job in front"
	// and "there is no session" are different facts, and answering both the
	// same way would let a stop button report a quiet pane about one that has
	// ended.
	return 0, fmt.Errorf("helper: this generation no longer holds session %s", a.hostID().Session)
}

// classifyForegroundObservation turns the helper's evidence into the answer
// the run-lease ladder is written against. It is a function of its own because
// it is a DECISION and the rest of ForegroundJob is transport — and because
// what broke was this, not the wire (nocx-nekvj).
//
// THE THREE ANSWERS ARE THREE DIFFERENT FACTS and collapsing any two is how
// the stop button lies:
//
//   - a group that is NOT the shell's own is a job, and it is returned to be
//     signalled;
//   - the SHELL'S OWN group in front is protected. It is not an absence: under
//     ADR-0024 nocx runs commands with job control off, so this is the state a
//     running command produces for its whole life, and the ladder answers it
//     by writing the terminal's interrupt rather than by signalling a group
//     that contains the shell;
//   - no group at all means nobody could look, which is a diagnosis. It must
//     not read as ErrNoForeground, or a caller reports a quiet pane about a
//     command that is plainly running.
//
// This mirrors pty.LocalPty.ForegroundJob exactly, and deliberately: the local
// pty reads the raw group and compares it against the shell it forked. Before
// nocx-ie23r.3 that was the only implementation, and every local pane now goes
// through this one instead. Two implementations of one predicate is the
// regression with a delay fuse AGENTS.md warns about, so this one is written
// to give the same answers rather than its own.
func classifyForegroundObservation(obs *Observation, launchPID int) (int, error) {
	if obs == nil || obs.ForegroundPgid <= 0 {
		return 0, ErrNoForegroundJob
	}
	if launchPID > 0 && obs.ForegroundPgid == launchPID {
		return 0, pty.ErrProtectedForeground
	}
	return obs.ForegroundPgid, nil
}

// SignalProcessGroup signals the exact group a previous ForegroundJob named.
func (a *AttachedSession) SignalProcessGroup(pgid int, sig syscall.Signal) error {
	ctx, cancel := context.WithTimeout(context.Background(), signalTimeout)
	defer cancel()
	return a.client.Signal(ctx, a.hostID(), pgid, int(sig))
}

// SignalForeground is the ONE-SHOT form: whatever is in front right now.
//
// It is composed from the two above rather than being a third question asked
// of the helper, so there is one answer to "which group is in front" and one
// answer to "signal this group". A helper that resolved the foreground itself
// on this call would be a second derivation, and the two would disagree in
// exactly the window the ladder cares about.
func (a *AttachedSession) SignalForeground(sig syscall.Signal) error {
	pgid, err := a.ForegroundJob()
	if err != nil {
		return err
	}
	return a.SignalProcessGroup(pgid, sig)
}

// LifecycleComplete carries one already-authenticated completion DOWN to the
// helper session it names (owner decision 2026-09-19). The kernel has
// validated everything there is to validate; this is the carrier and not a
// second gate, and the answer that matters is the absence of an error.
func (c *Client) LifecycleComplete(ctx context.Context, params proto.LifecycleCompleteParams) error {
	return c.Call(ctx, proto.ServiceSession, proto.OpLifecycleComplete, params, nil)
}
