// Package lifecyclecodec frames lifecycle envelopes on the wire: a 4-byte
// big-endian length prefix holding the JSON byte count, then the JSON
// (docs/lifecycle-protocol.md §6). It is the framing half of every lifecycle
// adapter — the local descriptor channel, the future forwarded-port channel
// and the relay share it, so the wire contract lives here once.
//
// The codec frames; it does not authenticate and does not interpret. A frame
// that maps to an envelope is delivered even when the kernel will reject it
// (a wrong capability, a quarantined event) — the kernel is the validator.
// A frame that cannot map — an implausible or oversize length prefix, a JSON
// body that does not parse, an unknown event kind, a malformed capability or
// fence — is garbage, and the decoder scans past it to the next frame
// boundary, reporting every skipped region through the GapSink so the kernel
// can enforce the desync budgets in one place (its NotifyGap).
//
// The two directions are not symmetric in one field. The bearer capability
// travels INBOUND only: the shell authenticates itself to the kernel with it
// on every frame, and the kernel — the only sender on the other direction,
// talking to a shell that already holds the value — sends none back. See
// Encode.
//
// Scan accounting, per region: every byte the scanner consumes before the
// resync point counts toward the byte budget; a garbage frame — a full
// 4-byte prefix of a plausible size whose body then failed (truncated at
// EOF, unparseable, or an oversize hello) — counts toward the frame budget.
// An implausible prefix is just bytes: the scanner advances one byte at a
// time, so a valid frame starting anywhere inside the garbage is found.
// The region is reported when the scanner resyncs, when the stream ends, or
// when a budget is exhausted.
package lifecyclecodec

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/shady2k/nocx/internal/lifecycle"
)

// ErrScanBudgetExhausted reports that the decoder consumed the scan budgets
// (bytes and frames) without finding a frame boundary. The kernel has
// revoked the domain through the final gap report; the adapter drains the
// transport afterwards.
var ErrScanBudgetExhausted = errors.New("lifecyclecodec: scan budget exhausted")

// ErrFrameTooLarge reports an Encode call whose frame exceeds max_frame.
// Decoding never returns it: an oversize prefix is garbage that the decoder
// scans past (and it is rejected before any allocation).
var ErrFrameTooLarge = errors.New("lifecyclecodec: frame exceeds max_frame")

// errFraming marks a frame attempt that failed to parse. It is internal: the
// decoder charges the attempt to the scan region and recovers.
var errFraming = errors.New("lifecyclecodec: framing anomaly")

// Config tunes the framing and scan bounds. Zero fields fall back to the
// protocol constants (docs/lifecycle-protocol.md §6) — the same numbers the
// kernel enforces through NotifyGap.
type Config struct {
	MaxFrame   int // default lifecycle.MaxFrameBytes (64 KiB)
	MaxHello   int // default lifecycle.MaxHelloBytes (1 KiB)
	ScanBytes  int // default lifecycle.ScanBudgetBytes (64 KiB)
	ScanFrames int // default lifecycle.ScanBudgetFrames (128)
}

func (c Config) withDefaults() Config {
	if c.MaxFrame <= 0 {
		c.MaxFrame = lifecycle.MaxFrameBytes
	}
	if c.MaxHello <= 0 {
		c.MaxHello = lifecycle.MaxHelloBytes
	}
	if c.ScanBytes <= 0 {
		c.ScanBytes = lifecycle.ScanBudgetBytes
	}
	if c.ScanFrames <= 0 {
		c.ScanFrames = lifecycle.ScanBudgetFrames
	}
	return c
}

// GapSink receives every garbage region the decoder skips: the number of
// garbage bytes and the number of garbage frame boundaries inside it. The
// adapter forwards each call to Kernel.NotifyGap. A nil sink disables
// reporting.
type GapSink func(bytes, frames int)

// Decoder reads length-delimited envelopes from a stream and recovers from
// framing corruption by scanning forward for the next frame boundary
// (docs/lifecycle-protocol.md §6, "Desynchronization"). It is safe for use
// by one goroutine.
type Decoder struct {
	r            io.Reader
	cfg          Config
	gap          GapSink
	pending      []byte // bytes already read from the stream, not yet consumed
	inScan       bool
	regionBytes  int
	regionFrames int
}

// NewDecoder builds a decoder over r. Zero Config fields fall back to the
// protocol defaults; gap, when non-nil, receives every skipped region.
func NewDecoder(r io.Reader, cfg Config, gap GapSink) *Decoder {
	return &Decoder{r: r, cfg: cfg.withDefaults(), gap: gap}
}

// ReadFrame returns the next frame's envelope. It may scan past garbage to
// get there; every region it skips is reported through the gap sink.
//
// Errors: io.EOF when the stream ended at a frame boundary;
// ErrScanBudgetExhausted when the scan budgets ran out.
func (d *Decoder) ReadFrame() (lifecycle.Envelope, error) {
	if !d.inScan {
		env, err := d.tryFrame()
		if err == nil || errors.Is(err, io.EOF) {
			return env, err
		}
		if !errors.Is(err, errFraming) {
			return lifecycle.Envelope{}, err
		}
		d.inScan = true // the failed attempt opens the region; tryFrame left
		// its bytes in pending, where the scanner accounts for them
	}
	return d.scan()
}

// tryFrame attempts one frame at the head of pending, topping up from the
// stream as needed. On success it consumes the frame from pending; on a
// framing anomaly it leaves pending untouched (the scanner accounts for the
// failed construct byte by byte) and returns errFraming.
func (d *Decoder) tryFrame() (lifecycle.Envelope, error) {
	if len(d.pending) < 4 {
		if err := d.topUp(4); err != nil {
			if errors.Is(err, io.EOF) {
				return lifecycle.Envelope{}, io.EOF // clean end at a boundary
			}
			return lifecycle.Envelope{}, err
		}
	}
	n := int(binary.BigEndian.Uint32(d.pending[:4]))
	if n == 0 || n > d.cfg.MaxFrame {
		// Rejected before any body allocation: a frame that claims more
		// than max_frame is not trusted to exist.
		return lifecycle.Envelope{}, errFraming
	}
	if len(d.pending) < 4+n {
		if err := d.topUp(4 + n); err != nil {
			if errors.Is(err, io.EOF) {
				return lifecycle.Envelope{}, errFraming // truncated frame
			}
			return lifecycle.Envelope{}, err
		}
	}
	env, ok := d.parse(d.pending[4:4+n], n)
	if !ok {
		return lifecycle.Envelope{}, errFraming
	}
	d.pending = d.pending[4+n:]
	return env, nil
}

// scan is the byte-at-a-time recovery loop. It consumes garbage until a
// frame resyncs, a budget gives out, or the stream ends, charging the region
// as it goes and reporting it once, at the end.
func (d *Decoder) scan() (lifecycle.Envelope, error) {
	for {
		if len(d.pending) < 4 {
			if err := d.topUp(4); err != nil {
				if errors.Is(err, io.EOF) {
					d.regionBytes += len(d.pending)
					d.endRegion()
					return lifecycle.Envelope{}, io.EOF
				}
				return lifecycle.Envelope{}, err
			}
		}
		n := int(binary.BigEndian.Uint32(d.pending[:4]))
		if n == 0 || n > d.cfg.MaxFrame {
			// This position is not a frame boundary: one garbage byte.
			d.pending = d.pending[1:]
			d.regionBytes++
			if d.overBudget() {
				d.endRegion()
				return lifecycle.Envelope{}, ErrScanBudgetExhausted
			}
			continue
		}
		if len(d.pending) < 4+n {
			if err := d.topUp(4 + n); err != nil {
				if errors.Is(err, io.EOF) {
					d.regionBytes += len(d.pending)
					d.regionFrames++ // a claimed frame whose body never arrived
					d.endRegion()
					return lifecycle.Envelope{}, io.EOF
				}
				return lifecycle.Envelope{}, err
			}
		}
		env, ok := d.parse(d.pending[4:4+n], n)
		if !ok {
			// One garbage frame: consume it whole and keep scanning.
			d.pending = d.pending[4+n:]
			d.regionBytes += 4 + n
			d.regionFrames++
			if d.overBudget() {
				d.endRegion()
				return lifecycle.Envelope{}, ErrScanBudgetExhausted
			}
			continue
		}
		// Resynced: the bytes before this frame are the region.
		d.endRegion()
		d.pending = d.pending[4+n:]
		return env, nil
	}
}

// parse maps one frame body to an envelope. It reports failure for JSON that
// does not parse, a hello over the hello bound, or a body that cannot map to
// an envelope (unknown event kind, malformed capability or fence).
func (d *Decoder) parse(body []byte, n int) (lifecycle.Envelope, bool) {
	var w wireEnvelope
	if err := json.Unmarshal(body, &w); err != nil {
		return lifecycle.Envelope{}, false
	}
	if w.Evt == string(lifecycle.KindHello) && n > d.cfg.MaxHello {
		return lifecycle.Envelope{}, false
	}
	env, err := decodeEnvelope(&w)
	if err != nil {
		return lifecycle.Envelope{}, false
	}
	return env, true
}

// topUp reads from the stream until pending holds at least n bytes or the
// stream ends. A partial read that also reports EOF is kept; the next call
// sees the EOF.
func (d *Decoder) topUp(n int) error {
	for len(d.pending) < n {
		var chunk [4096]byte
		m, err := d.r.Read(chunk[:])
		d.pending = append(d.pending, chunk[:m]...)
		if err != nil {
			if errors.Is(err, io.EOF) && m > 0 {
				continue
			}
			return err
		}
	}
	return nil
}

func (d *Decoder) overBudget() bool {
	return d.regionBytes > d.cfg.ScanBytes || d.regionFrames > d.cfg.ScanFrames
}

// endRegion reports the current region, if it contains anything, and closes
// it. An empty region is never reported: a zero gap must not desynchronize a
// domain.
func (d *Decoder) endRegion() {
	if d.gap != nil && (d.regionBytes > 0 || d.regionFrames > 0) {
		d.gap(d.regionBytes, d.regionFrames)
	}
	d.regionBytes, d.regionFrames = 0, 0
	d.inScan = false
}

// wireEnvelope is the flat wire shape: the addressing tuple plus the event
// kind and the event payload fields, all at one level, per the envelope
// tables in docs/lifecycle-protocol.md §2 and §3. Payload fields are
// pointers so absence is distinguishable from a zero value; the kernel
// judges legality, the codec only maps.
type wireEnvelope struct {
	Version uint8  `json:"v"`
	Lane    string `json:"lane"`
	Domain  string `json:"dom"`
	Epoch   uint64 `json:"epoch"`
	Seq     uint64 `json:"seq"`
	// Cap is the bearer capability, 64 lowercase hex chars — and it is
	// present on the INBOUND half only. An outbound frame omits it
	// entirely: the kernel is the only sender on that direction and the
	// shell already holds the capability it was given at bootstrap, so
	// echoing it back authenticates nothing while writing the secret onto
	// a descriptor every descendant of the shell inherits (nocx-aqz7o).
	// Absent therefore decodes to the zero capability, which the kernel
	// refuses outright (internal/lifecycle/kernel.go's zero test) — the
	// codec still does not authenticate, it only stops carrying.
	Cap string `json:"cap,omitempty"`
	Evt string `json:"evt"`

	// Event payload fields (§3).
	Shell *string `json:"shell,omitempty"`
	// Gen is the hello's bundle generation — what the far shell says it was
	// brought up from. Optional: a shell launched from no bundle names none,
	// and every shell built before the field existed sends nothing.
	Gen           *string           `json:"gen,omitempty"`
	MaxFrame      *int              `json:"max_frame,omitempty"`
	Attempt       *string           `json:"attempt,omitempty"`
	Command       *string           `json:"command,omitempty"`
	ExitCode      *int              `json:"exit_code,omitempty"`
	Fence         *string           `json:"fence,omitempty"` // 64 hex chars
	Request       *string           `json:"request,omitempty"`
	ShellState    *string           `json:"shell_state,omitempty"`
	ActiveAttempt *string           `json:"active_attempt,omitempty"`
	LastCompleted *wireCompletedRef `json:"last_completed,omitempty"`
	NextSeq       *uint64           `json:"next_seq,omitempty"`

	// Domain request/grant payload fields (§3). Env is the nested
	// environment kind; host/user/port the ssh destination; grant_domain/
	// grant_epoch the child's identity. Bootstrap is deliberately LAST: the
	// shell extracts it by substring (everything between "bootstrap":" and
	// the closing "}), and a fixed trailing position makes that extraction
	// robust against the content's own escaped quotes.
	Env  *string `json:"env,omitempty"`
	Host *string `json:"host,omitempty"`
	User *string `json:"user,omitempty"`
	Port *int    `json:"port,omitempty"`
	// Opts are the ssh options the user typed, in order, with their
	// arguments — the rest of what the composer rebuilds the line from
	// (nocx-c6z0). Before Bootstrap for the reason stated above: Bootstrap
	// stays last, and a field added after it would break the shell's
	// substring extraction.
	Opts        []string `json:"opts,omitempty"`
	GrantDomain *string  `json:"grant_domain,omitempty"`
	GrantEpoch  *uint64  `json:"grant_epoch,omitempty"`
	Bootstrap   *string  `json:"bootstrap,omitempty"`
	// Agent enrolment (protocol doc §15). Agent names what is about to run;
	// Enrolled and Reason are the answer. Enrolled is a VALUE rather than a
	// pointer on purpose: a missing field decodes to false, which is the
	// refusal, so a truncated or hostile frame cannot read as consent.
	Agent    *string `json:"agent,omitempty"`
	Cols     *int    `json:"cols,omitempty"`
	Rows     *int    `json:"rows,omitempty"`
	Enrolled bool    `json:"enrolled,omitempty"`
	Reason   *string `json:"reason,omitempty"`
}

// wireCompletedRef is the snapshot's last_completed payload.
type wireCompletedRef struct {
	Attempt  string `json:"attempt"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

// decodeEnvelope maps the wire shape to a lifecycle.Envelope. It fails only
// when the wire cannot be represented: a malformed capability or fence, or
// an unknown event kind. Every other judgement is the kernel's.
//
// An ABSENT cap is representable and decodes to the zero capability: that is
// what an outbound frame looks like since nocx-aqz7o, and it is also the one
// value the kernel's authentication refuses unconditionally. A cap that is
// PRESENT and malformed is still garbage — the codec maps what it can and
// scans past what it cannot.
func decodeEnvelope(w *wireEnvelope) (lifecycle.Envelope, error) {
	var capability lifecycle.Capability
	if w.Cap != "" {
		capBytes, err := hex.DecodeString(w.Cap)
		if err != nil || len(capBytes) != len(capability) {
			return lifecycle.Envelope{}, errFraming
		}
		copy(capability[:], capBytes)
	}
	env := lifecycle.Envelope{
		Version:  w.Version,
		Lane:     lifecycle.LaneID(w.Lane),
		Domain:   lifecycle.DomainID(w.Domain),
		Epoch:    w.Epoch,
		Sequence: w.Seq,
		Event:    lifecycle.Event{Kind: lifecycle.EventKind(w.Evt)},
	}
	env.Capability = capability
	switch env.Event.Kind {
	case lifecycle.KindHello:
		env.Event.Hello = &lifecycle.Hello{Shell: str(w.Shell), Generation: str(w.Gen)}
	case lifecycle.KindAccept:
		env.Event.Accept = &lifecycle.Accept{}
	case lifecycle.KindStart:
		env.Event.Start = &lifecycle.Start{AttemptID: attemptIDPtr(w.Attempt), Command: str(w.Command)}
	case lifecycle.KindComplete:
		var fence lifecycle.FenceNonce
		if w.Fence != nil {
			fb, err := hex.DecodeString(*w.Fence)
			if err != nil || len(fb) != len(fence) {
				return lifecycle.Envelope{}, errFraming
			}
			copy(fence[:], fb)
		}
		env.Event.Complete = &lifecycle.Complete{
			AttemptID: attemptIDPtr(w.Attempt),
			ExitCode:  w.ExitCode,
			Fence:     fence,
		}
	case lifecycle.KindPromptReady:
		env.Event.PromptReady = &lifecycle.PromptReady{}
	case lifecycle.KindRefreshRequest:
		env.Event.RefreshRequest = &lifecycle.RefreshRequest{RequestID: lifecycle.RequestID(str(w.Request))}
	case lifecycle.KindSnapshot:
		env.Event.Snapshot = &lifecycle.Snapshot{
			RequestID:       lifecycle.RequestID(str(w.Request)),
			ShellState:      lifecycle.ShellState(str(w.ShellState)),
			ActiveAttemptID: attemptIDPtr(w.ActiveAttempt),
			LastCompleted:   decodeCompletedRef(w.LastCompleted),
			NextSequence:    derefU64(w.NextSeq),
		}
	case lifecycle.KindDomainEstablished:
		env.Event.DomainEstablished = &lifecycle.DomainEstablishedEvent{}
	case lifecycle.KindDomainActivated:
		env.Event.DomainActivated = &lifecycle.DomainActivatedEvent{}
	case lifecycle.KindDomainSuspended:
		env.Event.DomainSuspended = &lifecycle.DomainSuspendedEvent{}
	case lifecycle.KindDomainClosed:
		env.Event.DomainClosed = &lifecycle.DomainClosedEvent{}
	case lifecycle.KindDomainRequest:
		env.Event.DomainRequest = &lifecycle.DomainRequest{
			RequestID: lifecycle.RequestID(str(w.Request)),
			Env:       str(w.Env),
			Host:      str(w.Host),
			User:      str(w.User),
			Port:      derefInt(w.Port),
			Opts:      w.Opts,
		}
	case lifecycle.KindDomainGrant:
		env.Event.DomainGrant = &lifecycle.DomainGrant{
			RequestID: lifecycle.RequestID(str(w.Request)),
			Env:       str(w.Env),
			Host:      str(w.Host),
			User:      str(w.User),
			Port:      derefInt(w.Port),
			Opts:      w.Opts,
			Domain:    lifecycle.DomainID(str(w.GrantDomain)),
			Epoch:     derefU64(w.GrantEpoch),
			Bootstrap: str(w.Bootstrap),
		}
	case lifecycle.KindAgentEnrol:
		env.Event.AgentEnrol = &lifecycle.AgentEnrol{
			RequestID: lifecycle.RequestID(str(w.Request)),
			Agent:     str(w.Agent),
			Cols:      derefInt(w.Cols),
			Rows:      derefInt(w.Rows),
		}
	case lifecycle.KindAgentEnrolled:
		env.Event.AgentEnrolled = &lifecycle.AgentEnrolled{
			RequestID: lifecycle.RequestID(str(w.Request)),
			Agent:     str(w.Agent),
			Enrolled:  w.Enrolled,
			Reason:    str(w.Reason),
		}
	case lifecycle.KindAgentWithdraw:
		env.Event.AgentWithdraw = &lifecycle.AgentWithdraw{
			RequestID: lifecycle.RequestID(str(w.Request)),
		}
	case lifecycle.KindAgentWithdrawn:
		env.Event.AgentWithdrawn = &lifecycle.AgentWithdrawn{
			RequestID: lifecycle.RequestID(str(w.Request)),
		}
	default:
		return lifecycle.Envelope{}, errFraming
	}
	return env, nil
}

// Encode writes env as one length-delimited JSON frame. It is the outbound
// half: the kernel's accept, refresh_request and domain_grant travel this
// way. A frame over max_frame is refused before anything is written.
//
// The capability is written only when the envelope carries one. Every
// envelope the KERNEL builds carries none (nocx-aqz7o), so every frame this
// writes on the kernel→shell direction is free of the bearer — which is what
// makes ADR-0024's claim true, that a descendant which inherited the
// descriptor cannot produce the capability. It was not true while the accept
// echoed it back in cleartext onto that same descriptor. The condition is on
// the VALUE and not on the event kind because the value is the thing that
// must not be written; a caller that hands Encode a capability is speaking
// the inbound half (the shell's own hello and events, and the tests and
// helpers that stand in for them), and that half still needs it.
func Encode(w io.Writer, env lifecycle.Envelope) (int, error) {
	we := wireEnvelope{
		Version: env.Version,
		Lane:    string(env.Lane),
		Domain:  string(env.Domain),
		Epoch:   env.Epoch,
		Seq:     env.Sequence,
		Evt:     string(env.Event.Kind),
	}
	if env.Capability != (lifecycle.Capability{}) {
		we.Cap = hex.EncodeToString(env.Capability[:])
	}
	switch env.Event.Kind {
	case lifecycle.KindHello:
		if p := env.Event.Hello; p != nil {
			we.Shell = new(p.Shell)
			if p.Generation != "" {
				we.Gen = new(p.Generation)
			}
		}
	case lifecycle.KindStart:
		if p := env.Event.Start; p != nil {
			if p.AttemptID != nil {
				we.Attempt = new(string(*p.AttemptID))
			}
			we.Command = new(p.Command)
		}
	case lifecycle.KindComplete:
		if p := env.Event.Complete; p != nil {
			if p.AttemptID != nil {
				we.Attempt = new(string(*p.AttemptID))
			}
			we.ExitCode = p.ExitCode
			we.Fence = new(hex.EncodeToString(p.Fence[:]))
		}
	case lifecycle.KindRefreshRequest:
		if p := env.Event.RefreshRequest; p != nil {
			we.Request = new(string(p.RequestID))
		}
	case lifecycle.KindSnapshot:
		if p := env.Event.Snapshot; p != nil {
			we.Request = new(string(p.RequestID))
			we.ShellState = new(string(p.ShellState))
			if p.ActiveAttemptID != nil {
				we.ActiveAttempt = new(string(*p.ActiveAttemptID))
			}
			we.LastCompleted = encodeCompletedRef(p.LastCompleted)
			we.NextSeq = new(p.NextSequence)
		}
	case lifecycle.KindDomainRequest:
		if p := env.Event.DomainRequest; p != nil {
			we.Request = new(string(p.RequestID))
			we.Env = new(p.Env)
			if p.Host != "" {
				we.Host = new(p.Host)
			}
			if p.User != "" {
				we.User = new(p.User)
			}
			if p.Port != 0 {
				we.Port = new(p.Port)
			}
			we.Opts = p.Opts
		}
	case lifecycle.KindDomainGrant:
		if p := env.Event.DomainGrant; p != nil {
			we.Request = new(string(p.RequestID))
			if p.Env != "" {
				we.Env = new(p.Env)
			}
			if p.Host != "" {
				we.Host = new(p.Host)
			}
			if p.User != "" {
				we.User = new(p.User)
			}
			if p.Port != 0 {
				we.Port = new(p.Port)
			}
			we.Opts = p.Opts
			we.GrantDomain = new(string(p.Domain))
			we.GrantEpoch = new(p.Epoch)
			if p.Bootstrap != "" {
				we.Bootstrap = new(p.Bootstrap)
			}
		}
	case lifecycle.KindAgentEnrol:
		if p := env.Event.AgentEnrol; p != nil {
			we.Request = new(string(p.RequestID))
			we.Agent = new(p.Agent)
			we.Cols = new(p.Cols)
			we.Rows = new(p.Rows)
		}
	case lifecycle.KindAgentEnrolled:
		if p := env.Event.AgentEnrolled; p != nil {
			we.Request = new(string(p.RequestID))
			we.Agent = new(p.Agent)
			we.Enrolled = p.Enrolled
			if p.Reason != "" {
				we.Reason = new(p.Reason)
			}
		}
	case lifecycle.KindAgentWithdraw:
		if p := env.Event.AgentWithdraw; p != nil {
			we.Request = new(string(p.RequestID))
		}
	case lifecycle.KindAgentWithdrawn:
		if p := env.Event.AgentWithdrawn; p != nil {
			we.Request = new(string(p.RequestID))
		}
	}
	body, err := json.Marshal(&we)
	if err != nil {
		return 0, err
	}
	if len(body) > lifecycle.MaxFrameBytes {
		return 0, ErrFrameTooLarge
	}
	var hdr [4]byte
	// #nosec G115 -- len(body) is checked against MaxFrameBytes (64 KiB)
	// above, far below the uint32 ceiling; the frame length is the JSON
	// byte count by contract.
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, werr := w.Write(hdr[:]); werr != nil {
		return 0, werr
	}
	n, err := w.Write(body)
	return 4 + n, err
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func attemptIDPtr(p *string) *lifecycle.AttemptID {
	if p == nil {
		return nil
	}
	id := lifecycle.AttemptID(*p)
	return &id
}

// derefU64 reads an optional wire integer. Named for what it does rather than
// for its type so that the deadcode ratchet's report is legible when a caller
// chain moves (nocx-u7uh.5).
func derefU64(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

func decodeCompletedRef(p *wireCompletedRef) *lifecycle.CompletedRef {
	if p == nil {
		return nil
	}
	return &lifecycle.CompletedRef{AttemptID: lifecycle.AttemptID(p.Attempt), ExitCode: p.ExitCode}
}

func encodeCompletedRef(p *lifecycle.CompletedRef) *wireCompletedRef {
	if p == nil {
		return nil
	}
	return &wireCompletedRef{Attempt: string(p.AttemptID), ExitCode: p.ExitCode}
}
