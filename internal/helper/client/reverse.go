package client

// The REVERSE half of the connection: the requests the HELPER sends to this
// coordinator, and the handlers that answer them.
//
// TypeRequest and TypeResponse were reserved in both directions before either
// was used both ways. Until the ssh service only one direction was built — this
// client asked, the helper answered — and a request arriving the other way was
// logged as an unexpected frame and dropped. Dropping it is the worst possible
// answer: the helper is not asking a question it can proceed without, it is
// BLOCKED on the answer, so the drop turns a missing handler into a hang.
//
// # What may be asked, and by whom
//
// The set is CLOSED and it is registered by name at the composition root
// (internal/app), never by a helper and never by a package that happens to
// want one: a reverse handler is a coordinator capability, and the reason this
// structure exists rather than a callback on the client is that the list has
// to be readable in one place. An op nobody registered is refused with the
// same codes the host uses for its own dispatch (unknown_service, unknown_op),
// so a helper built against a newer generation learns what happened instead of
// waiting forever.
//
// # Why the answer is served on its own goroutine
//
// A handler may block — reading a vault raises an unlock, and a person takes as
// long as they take — and the read pump is what delivers the NEXT reverse
// request. Answering inline would let one question from one helper stall every
// other request in flight on the same connection, which is the defect
// host.request's own per-request goroutine exists to avoid (D13) on the other
// side of this wire.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/helper/proto"
	nocxlog "github.com/shady2k/nocx/internal/log"
)

// ReverseHandler answers one reverse request: params is the raw JSON the
// helper sent and the returned value is marshalled into the Response's result.
//
// A handler that refuses something the caller acts on differently returns an
// error implementing proto.Coded (or a *proto.Refusal), and its code crosses
// the wire. Anything else is `internal` — see the refusal path below.
type ReverseHandler func(ctx context.Context, params json.RawMessage) (any, error)

// ReverseRegistry is the closed set of services and ops this coordinator
// answers when a helper asks.
//
// It is handed to a client through Config.Reverse, which is why it is a value
// the composition root builds ONCE and shares: the local daemon connection is
// one connection for every pane, and two registries would be two answers to
// "may this helper ask for this".
type ReverseRegistry struct {
	mu       sync.RWMutex
	handlers map[reverseKey]ReverseHandler
	services map[string]struct{}
}

type reverseKey struct{ service, op string }

// NewReverseRegistry builds an empty registry: a client without one answers
// every reverse request with unknown_service, which is the honest answer for a
// build that registered nothing.
func NewReverseRegistry() *ReverseRegistry {
	return &ReverseRegistry{
		handlers: make(map[reverseKey]ReverseHandler),
		services: make(map[string]struct{}),
	}
}

// Register adds one op. A duplicate is a panic rather than a silent
// replacement: two handlers under one name is not a conflict anybody would
// see, it is a handler that silently never runs — the same reason host.Register
// refuses a duplicate service name.
func (r *ReverseRegistry) Register(service, op string, h ReverseHandler) {
	if h == nil {
		panic(fmt.Sprintf("helper client: reverse handler for %s.%s is nil", service, op))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[reverseKey{service, op}]; exists {
		panic(fmt.Sprintf("helper client: a reverse handler for %s.%s is already registered", service, op))
	}
	r.handlers[reverseKey{service, op}] = h
	r.services[service] = struct{}{}
}

// lookup answers the handler for one op, and whether the SERVICE is known at
// all — the two facts the refusal path needs to tell "no such service" from
// "no such op on this service".
func (r *ReverseRegistry) lookup(service, op string) (ReverseHandler, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[reverseKey{service, op}]
	if ok {
		return h, true
	}
	_, known := r.services[service]
	return nil, known
}

// serveReverse answers one reverse request: it looks the op up, runs the
// handler and writes the response frame. It runs on its own goroutine — see
// the file header — and it never returns an error, because there is nobody
// left to hand one to: the answer is the frame.
func (c *Client) serveReverse(payload []byte) {
	var req proto.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		c.log.Warn("malformed reverse request", "err", err)
		return
	}
	ctx := c.reverseCtx
	go c.answerReverse(ctx, req)
}

// answerReverse runs one reverse handler and answers it.
func (c *Client) answerReverse(ctx context.Context, req proto.Request) {
	lg := nocxlog.NewSlogAdapter(c.log).WithContext(ctx).
		With("id", req.ID, "corr", req.Corr, "op", req.Service+"."+req.Op)
	lg.Info("reverse request", "service", req.Service, "op", req.Op, "corr", req.Corr) // D26

	resp := proto.Response{ID: req.ID}
	refuse := func(code, message string) {
		lg.Warn("coordinator refused a reverse request", "code", code, "message", message)
		resp.Error = &proto.Error{Code: code, Message: message}
		c.writeReverse(resp)
	}

	handler, known := c.cfg.Reverse.lookup(req.Service, req.Op)
	if handler == nil {
		if known {
			refuse(proto.ErrCodeUnknownOp, "no op "+req.Op+" on reverse service "+req.Service)
			return
		}
		refuse(proto.ErrCodeUnknownService, "no reverse service named "+req.Service)
		return
	}

	result, err := handler(ctx, req.Params)
	if err != nil {
		var coded proto.Coded
		if errors.As(err, &coded) {
			code, details := coded.WireCode()
			if code != "" {
				lg.Warn("reverse handler refused", "code", code, "error", err)
				resp.Error = &proto.Error{Code: code, Message: err.Error(), Details: details}
				c.writeReverse(resp)
				return
			}
		}
		refuse(proto.ErrCodeInternal, err.Error())
		return
	}

	raw, err := json.Marshal(result)
	if err != nil {
		refuse(proto.ErrCodeInternal, "result: "+err.Error())
		return
	}
	lg.Debug("reverse request ok")
	resp.Result = raw
	c.writeReverse(resp)
}

// writeReverse puts one response frame on the wire. A payload that will not
// fit in one frame is answered as an internal refusal instead: the chunking
// path is the host's (D14) and a response is not chunked in this direction, so
// the alternative to refusing is EncodeFrame panicking in the coordinator —
// which is to say, on a helper's question.
func (c *Client) writeReverse(resp proto.Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		c.log.Error("marshal reverse response", "err", err)
		return
	}
	if len(raw) > proto.MaxFrameBytes {
		fallback, ferr := json.Marshal(proto.Response{ID: resp.ID, Error: &proto.Error{
			Code:    proto.ErrCodeInternal,
			Message: fmt.Sprintf("coordinator: the answer to this request exceeds one frame (%d bytes)", len(raw)),
		}})
		if ferr != nil {
			c.log.Error("marshal reverse refusal", "err", ferr)
			return
		}
		raw = fallback
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, werr := c.conn.Stdin().Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, raw)); werr != nil {
		c.log.Warn("write reverse response", "err", werr)
	}
}
