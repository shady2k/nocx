// Package host is the remote-helper's serving half: it reads one TypeHello
// over the wire, and on a version match writes the sentinel line and a
// TypeHelloOK echo, then serves requests until stdin reaches EOF. It hosts
// a closed set of named operations grouped into services (D2); registering
// a service is the whole extension point.
//
// stdout is the wire and nothing else (D22). Every diagnostic goes to the
// logger the host was constructed with — the caller builds it over stderr —
// so one stray write to stdout corrupts a frame and surfaces far away.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// ExitVersionMismatch is the helper's exit code for a hello carrying the
// wrong protocol version. The helper writes nothing to stdout first (D5).
const ExitVersionMismatch = 42

// ErrVersionMismatch reports a hello whose Version disagrees with
// proto.Version. main maps it to ExitVersionMismatch.
var ErrVersionMismatch = errors.New("host: helper version mismatch")

// Host serves one helper connection: hello, sentinel, then requests until
// the input stream ends. It is safe for use by one goroutine per request,
// plus the goroutine running Serve.
type Host struct {
	in          io.Reader
	out         io.Writer
	contentHash string
	instanceID  string
	log         *slog.Logger

	// mu guards out, services, requests and streamSeq. One mutex is enough:
	// the critical sections are tiny, and the writer mutex is what keeps
	// concurrent responses from interleaving mid-frame.
	mu       sync.Mutex
	services []Service
	requests map[uint64]pendingRequest

	// streamSeq mints the stream ids chunked responses are keyed by (D14):
	// the sentinel and its chunks may interleave with other responses, and
	// the stream id is what routes them.
	streamSeq uint64

	// inflight counts the request goroutines `frame` dispatched. D13 opens
	// the interval — a handler runs while the read loop moves on — and this
	// is what closes it: Serve does not return while one is still running.
	// Not guarded by mu: Add happens on the read loop before the goroutine
	// starts, which is the ordering sync.WaitGroup requires.
	inflight sync.WaitGroup
}

// pendingRequest is the per-request state a TypeCancel reaches: the
// request's own stop function plus the service and op it is serving, which
// is what lets a cancel decide whether the operation refuses cancellation
// (D11).
type pendingRequest struct {
	stop    context.CancelFunc
	service string
	op      string
}

// New builds a host over in and out. out is the wire: sentinel line, then
// frames, nothing else. The logger is the only place diagnostics go.
func New(in io.Reader, out io.Writer, contentHash, instanceID string, log *slog.Logger) *Host {
	return &Host{
		in:          in,
		out:         out,
		contentHash: contentHash,
		instanceID:  instanceID,
		requests:    make(map[uint64]pendingRequest),
		log:         log,
	}
}

// Register adds a service to the host. D3 is enforced here, not just audited:
// every op of the service must declare its params type, and an op whose params
// carry a free-form string list is refused at registration — a rule the
// registration path cannot get past is stronger than a rule a test must
// remember to check.
//
// The name `session` was RESERVED here until nocx-k6p18.3 and this call
// panicked on it (D15). That reservation is now cashed in by
// internal/helper/session, and what survives it is the reason it existed: one
// owner per name. A duplicate registration is refused, whatever the name,
// because serviceByName answers with the first match — so a second service
// under one name is not a conflict anybody would see, it is a service that
// silently never runs.
func (h *Host) Register(s Service) {
	for _, existing := range h.Services() {
		if existing.Name() == s.Name() {
			panic(fmt.Sprintf("host: a service named %q is already registered", s.Name()))
		}
	}
	for _, op := range s.Ops() {
		schema := s.ParamsSchema(op)
		if schema == nil {
			panic(fmt.Sprintf("host: %s.%s declares no params schema", s.Name(), op))
		}
		for _, field := range schema.Fields() {
			if field.IsFreeFormStringList() {
				panic(fmt.Sprintf("host: %s.%s takes %q: no operation may accept argv (D3)", s.Name(), op, field.Name))
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.services = append(h.services, s)
}

// Services returns the registered services. The dispatcher finds a service
// by name through this accessor — there is no second lookup table beside
// it — and the D3 audit walks the same list.
func (h *Host) Services() []Service {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Service(nil), h.services...)
}

// Serve runs the connection: it reads one TypeHello, refuses a version
// mismatch without writing a byte, otherwise writes the sentinel line and a
// TypeHelloOK echo, then serves frames until the input reaches EOF, at which
// point it returns nil.
func (h *Host) Serve(ctx context.Context) error {
	// A returning Serve means the connection is over, and cmd/nocx-helper's
	// main exits on it — so it must also mean no handler is still working.
	// Otherwise a transport that dies mid-mutation takes the process down
	// with the git it spawned still writing into .git, which is the
	// half-written repository D12 exists to prevent (nocx-t76b9). A mutation
	// is never cancelled (D11), so this waits for it rather than bounding it.
	//
	// Waiting for EVERYTHING would be the wrong end of the same stick: a read
	// still in flight when the transport dies has nobody left to answer, and
	// would hold the process open for as long as it felt like running. So the
	// cancellable half is abandoned first and only the refusing half is
	// waited for. Deferred in this order because defers run last-first.
	defer h.inflight.Wait()
	defer h.abandonCancellable()

	var helloErr error
	helloDone := false
	dec := proto.NewDecoder(func(ty proto.FrameType, seq, ack uint32, payload []byte) {
		if !helloDone {
			helloDone = true
			helloErr = h.hello(ty, payload)
			return
		}
		h.frame(ctx, ty, payload)
	}, func(n int) {
		h.log.Warn("decoder resync", "garbage", n)
	})

	buf := make([]byte, 32*1024)
	for {
		n, err := h.in.Read(buf)
		if n > 0 {
			if ferr := dec.Feed(buf[:n]); ferr != nil {
				return ferr
			}
			if helloErr != nil {
				return helloErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// hello handles the first frame of the connection. A mismatch must write
// nothing to stdout (D5): the sentinel is only written after the version
// check passes.
func (h *Host) hello(ty proto.FrameType, payload []byte) error {
	if ty != proto.TypeHello {
		return fmt.Errorf("host: first frame is type %d, want %d", ty, proto.TypeHello)
	}
	var hello proto.Hello
	if err := json.Unmarshal(payload, &hello); err != nil {
		return fmt.Errorf("host: malformed hello: %w", err)
	}
	if hello.Version != proto.Version {
		return fmt.Errorf("host: version %q, want %q: %w", hello.Version, proto.Version, ErrVersionMismatch)
	}
	h.mu.Lock()
	_, err := fmt.Fprintf(h.out, "nocx-helper %s ready\n", proto.Version)
	h.mu.Unlock()
	if err != nil {
		return fmt.Errorf("host: sentinel: %w", err)
	}
	ok := proto.HelloOK{Version: proto.Version, Nonce: hello.Nonce, ContentHash: h.contentHash, InstanceID: h.instanceID}
	raw, err := json.Marshal(ok)
	if err != nil {
		return fmt.Errorf("host: helloOK: %w", err)
	}
	h.writeFrame(proto.TypeHelloOK, raw)
	return nil
}

// frame handles every frame after the hello.
func (h *Host) frame(ctx context.Context, ty proto.FrameType, payload []byte) {
	switch ty {
	case proto.TypeRequest:
		var req proto.Request
		if err := json.Unmarshal(payload, &req); err != nil {
			h.log.Warn("malformed request", "err", err)
			return
		}
		h.inflight.Add(1)
		go func() {
			defer h.inflight.Done()
			h.request(ctx, req)
		}()
	case proto.TypeCancel:
		h.cancel(payload)
	case proto.TypeKeepAlive:
		// nothing to answer; keepalives keep the transport warm
	case proto.TypeSessionData:
		h.sessionData(payload)
	default:
		h.log.Warn("unexpected frame", "type", ty)
	}
}

// sessionData handles an inbound data-plane frame: it is routed to the session
// service when this generation has one, and DROPPED when it does not.
//
// The drop path is not a leftover. Generations are immutable and coexist for
// months, and a build serving only the git service — which is every build
// before nocx-k6p18.3, and any later one composed without the session service
// — will still be sent these frames by a newer coordinator. An unknown type
// byte is garbage to the decoder, which then resyncs forward one byte at a
// time through whatever follows: a live PTY stream, in the case that matters.
// Recognising the frame turns that into one dropped write. This is AD-1's own
// move for its reserved metadata msg-type: logged and dropped, never a spawn
// and never a torn-down connection.
//
// The bytes are counted here, never read: the helper moves PTY bytes and does
// not interpret them (AD-6).
func (h *Host) sessionData(payload []byte) {
	f, err := proto.DecodeSessionFrame(payload)
	if err != nil {
		h.log.Warn("malformed session data frame", "err", err, "bytes", len(payload))
		return
	}
	if svc := h.serviceByName(proto.ServiceSession); svc != nil {
		if plane, ok := svc.(DataPlane); ok {
			plane.SessionData(f)
			return
		}
	}
	h.log.Warn("session data frame dropped: no session service in this generation",
		"session", fmt.Sprintf("%x", f.Session), "subscriber", fmt.Sprintf("%x", f.Subscriber),
		"epoch", uint64(f.Epoch), "bytes", len(f.Payload))
}

// SendSessionData writes one data-plane frame to the wire: the helper's own
// output, on its way to one subscriber. It is the outbound half of DataPlane
// and the reason this method is on the host rather than in the service — the
// wire and its writer mutex are the host's, and a second writer would
// interleave mid-frame.
//
// The error is returned rather than only logged, because its caller acts on
// it: a per-subscriber pump whose wire has died drops its attachment, and the
// session, the window and the process survive it.
func (h *Host) SendSessionData(f proto.SessionFrame) error {
	return h.write(proto.TypeSessionData, proto.EncodeSessionFrame(f))
}

// SendNotification writes one unsolicited fact: a live reset, an exit. It
// rides as a TypeNotify frame on the same wire as the data frames, so it is
// ORDERED with respect to them — which is what lets a reader see exactly which
// bytes a hole sits between.
func (h *Host) SendNotification(n proto.Notification) error {
	raw, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("host: marshal notification: %w", err)
	}
	return h.write(proto.TypeNotify, raw)
}

// request serves one request on its own goroutine, so a blocking handler
// never stalls the read loop or another request (D13). The per-request
// context is stored by id so a TypeCancel can reach it.
func (h *Host) request(ctx context.Context, req proto.Request) {
	reqCtx, stop := context.WithCancel(ctx)
	h.mu.Lock()
	h.requests[req.ID] = pendingRequest{stop: stop, service: req.Service, op: req.Op}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.requests, req.ID)
		h.mu.Unlock()
		stop()
	}()

	h.log.Info("request", "id", req.ID, "service", req.Service, "op", req.Op, "corr", req.Corr) // D26

	resp := proto.Response{ID: req.ID}
	svc := h.serviceByName(req.Service)
	if svc == nil {
		resp.Error = &proto.Error{Code: proto.ErrCodeUnknownService, Message: "no service named " + req.Service}
		h.respond(resp)
		return
	}
	schema := svc.ParamsSchema(req.Op)
	if schema == nil {
		resp.Error = &proto.Error{Code: proto.ErrCodeUnknownOp, Message: "no op " + req.Op + " on service " + req.Service}
		h.respond(resp)
		return
	}
	if _, err := schema.Decode(req.Params); err != nil {
		resp.Error = &proto.Error{Code: proto.ErrCodeBadParams, Message: err.Error()}
		h.respond(resp)
		return
	}
	result, err := svc.Call(reqCtx, req.Op, req.Params)
	if err != nil {
		code, details := refusal(svc, err)
		resp.Error = &proto.Error{Code: code, Message: err.Error(), Details: details}
		h.respond(resp)
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp.Error = &proto.Error{Code: proto.ErrCodeInternal, Message: "result: " + err.Error()}
		h.respond(resp)
		return
	}
	resp.Result = raw
	h.respond(resp)
}

// abandonCancellable cancels every in-flight request whose service allows
// cancellation, which is what a dead transport means for a read: the answer
// has nowhere to go. A mutation whose service refuses cancellation (D11) is
// left alone and runs to completion — that is what Serve then waits for.
func (h *Host) abandonCancellable() {
	h.mu.Lock()
	pending := make([]pendingRequest, 0, len(h.requests))
	for _, entry := range h.requests {
		pending = append(pending, entry)
	}
	h.mu.Unlock()
	for _, entry := range pending {
		if svc := h.serviceByName(entry.service); svc != nil {
			if policy, ok := svc.(CancelPolicy); ok && policy.RefusesCancel(entry.op) {
				continue
			}
		}
		entry.stop()
	}
}

// refusal codes a service error for the wire: the service's own
// machine-readable code and structured details when it declares them
// (RefusalCoder), internal otherwise. One call for both halves, so a
// coder cannot produce a code and details that disagree.
func refusal(svc Service, err error) (string, json.RawMessage) {
	coder, ok := svc.(RefusalCoder)
	if !ok {
		return proto.ErrCodeInternal, nil
	}
	code, details := coder.Refusal(err)
	if code == "" {
		return proto.ErrCodeInternal, nil
	}
	return code, details
}

// cancel reaches the per-request context of the named request. Whether the
// cancellation is honoured is the handler's decision; mutations are never
// cancelled (D11), which the git service enforces when it lands: a cancel
// naming an operation the service declares refuses cancellation is
// answered with ErrCodeCancelRefused — a refusal is a fact the caller can
// act on, a no-op looks like success — and the operation runs to
// completion. Everything else is cancelled.
func (h *Host) cancel(payload []byte) {
	var c struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		h.log.Warn("malformed cancel", "err", err)
		return
	}
	h.mu.Lock()
	entry, ok := h.requests[c.ID]
	h.mu.Unlock()
	if !ok {
		return
	}
	if svc := h.serviceByName(entry.service); svc != nil {
		if policy, ok := svc.(CancelPolicy); ok && policy.RefusesCancel(entry.op) {
			h.respond(proto.Response{ID: c.ID, Error: &proto.Error{
				Code:    proto.ErrCodeCancelRefused,
				Message: "operation refuses cancellation",
			}})
			return
		}
	}
	entry.stop()
}

// serviceByName finds a service by name through Services(), the same
// accessor the D3 audit reads — the lookup and the audit cannot drift.
func (h *Host) serviceByName(name string) Service {
	for _, s := range h.Services() {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// respond sends one response. A response whose frame would exceed the
// payload bound is chunked (D14): a ChunkedResult sentinel followed by
// TypeChunk frames, which the receiver reassembles by concatenation.
func (h *Host) respond(resp proto.Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		h.log.Error("marshal response", "err", err)
		return
	}
	if len(raw) > proto.MaxFrameBytes && resp.Error == nil {
		h.respondChunked(resp.ID, resp.Result)
		return
	}
	h.writeFrame(proto.TypeResponse, raw)
}

// respondChunked sends one response as a ChunkedResult sentinel followed by
// TypeChunk frames: the result bytes, split into pieces that each fit
// proto.MaxFrameBytes once wrapped in a Chunk envelope. The receiver
// concatenates the pieces in stream-id order to recover the original
// result. The
// sentinel and its chunks may interleave with other responses on the wire —
// the stream id routes them, which is what keeps chunking compatible with
// one goroutine per request (D13).
func (h *Host) respondChunked(id uint64, result json.RawMessage) {
	h.mu.Lock()
	h.streamSeq++
	streamID := h.streamSeq
	h.mu.Unlock()

	chunkSize := chunkPayloadCapacity(proto.MaxFrameBytes)
	if chunkSize < 1 {
		chunkSize = 1
	}
	chunks := splitChunks(result, chunkSize)
	sentinelResult, err := json.Marshal(proto.ChunkedResult{
		ChunkedStreamID: streamID,
		TotalBytes:      len(result),
		ChunkCount:      len(chunks),
	})
	if err != nil {
		h.log.Error("marshal chunked sentinel", "err", err)
		return
	}
	sentinel := proto.Response{ID: id, Result: sentinelResult}
	raw, err := json.Marshal(sentinel)
	if err != nil {
		h.log.Error("marshal chunked response", "err", err)
		return
	}
	h.writeFrame(proto.TypeResponse, raw)
	for _, piece := range chunks {
		raw, err = json.Marshal(proto.Chunk{ChunkedStreamID: streamID, Bytes: piece})
		if err != nil {
			h.log.Error("marshal chunk", "err", err)
			return
		}
		h.writeFrame(proto.TypeChunk, raw)
	}
}

// chunkPayloadCapacity is the largest raw result piece whose Chunk envelope
// — the stream id and the base64 bytes, in JSON — still fits within the
// given payload bound. The 64 bytes cover the envelope's fixed overhead,
// and 3/4 is base64's expansion; both are deliberately conservative.
func chunkPayloadCapacity(maxPayload int) int {
	return (maxPayload - 64) * 3 / 4
}

// splitChunks divides b into pieces of at most size bytes, preserving order.
func splitChunks(b []byte, size int) [][]byte {
	if len(b) == 0 {
		return nil
	}
	var out [][]byte
	for len(b) > size {
		out = append(out, b[:size])
		b = b[size:]
	}
	out = append(out, b)
	return out
}

// writeFrame writes one frame and logs a failure. It is the response path,
// where there is nothing else to do with the error: the request it answers is
// already finished.
func (h *Host) writeFrame(ty proto.FrameType, raw []byte) {
	if err := h.write(ty, raw); err != nil {
		h.log.Error("write frame", "type", ty, "err", err)
	}
}

// write puts one frame on the wire under the writer mutex, so concurrent
// writers cannot interleave mid-frame, and RETURNS the failure. The session
// service's pumps act on it — a dead wire drops an attachment — which is why
// this half exists beside writeFrame rather than instead of it.
func (h *Host) write(ty proto.FrameType, raw []byte) error {
	frame := proto.EncodeFrame(ty, 0, 0, raw)
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.out.Write(frame)
	return err
}
