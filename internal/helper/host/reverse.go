package host

// The REVERSE half of the connection: the requests this host SENDS to the
// coordinator, and the answers it routes back.
//
// TypeRequest and TypeResponse were reserved in both directions before either
// was used both ways (proto/frame.go), and until now only one direction was
// built: the coordinator asked, the helper answered. The ssh service needs the
// other one, because a helper that dials has questions only the coordinator
// can answer — what password to present, which key to sign with, whether a
// host key is known (proto/ssh_service.go).
//
// # Why the waits live on the Host and not in the service
//
// The connection is the Host: it owns the wire, the writer mutex, and the id
// space of the requests it has sent. A service that kept its own wait table
// would need the Host's writer to send and its reader to be told about the
// answer, which is exactly the shape of two owners for one connection. The
// service asks (Ask), and everything about how the question reaches the wire
// and how the answer comes back stays here.
//
// # One request per goroutine, and never from the read loop
//
// Ask BLOCKS until the answer, the caller's context ends, or the connection
// dies. It is therefore called from the goroutine serving a request — never
// from the decoder callback, which is the read loop itself and is what
// delivers the answer. A service handler runs on its own goroutine by
// construction (host.request), so the only way to get this wrong is for
// internal code to call Ask outside a handler.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/helper/proto"
	nocxlog "github.com/shady2k/nocx/internal/log"
)

// Ask sends one reverse request to the coordinator on this connection and
// waits for its answer. The answer is decoded into out, which may be nil when
// the caller only cares that the op succeeded.
//
// Every way this can fail is a DIFFERENT thing to the caller, and they are kept
// apart rather than collapsed into one error:
//
//   - the coordinator refused it — *proto.Refusal, carrying the code the
//     coordinator's handler named (a sealed vault, a request to sign a
//     challenge with a key that is not there);
//   - the caller gave up or the connection died — ctx.Err(), and the request
//     context is what carries the second: abandonCancellable cancels every
//     cancellable request when the transport goes, which is what unwedges a
//     probe blocked here so Serve can finish;
//   - the wire could not carry it — the write failed, with the cause.
//
// The wait is bounded by the context and by nothing else, deliberately: a
// helper that invented its own timeout would be a second answer to "how long
// may the coordinator take", and the caller's context is the one the product
// already reasons about.
func (h *Host) Ask(ctx context.Context, service, op string, params, out any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("host: reverse params: %w", err)
		}
		raw = b
	}
	id := h.mintReverseID()
	req := proto.Request{
		ID: id, Service: service, Op: op, Params: raw,
		Corr: reverseCorr(),
		// The exchange the REQUESTING coordinator is in becomes this ask's
		// parent, exactly as the coordinator's own requests stamp theirs
		// (client.Call): the helper's question and the answer's effect land in
		// the same trace as the probe that caused them (D26).
		Traceparent: nocxlog.SpanFrom(ctx).Traceparent(),
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("host: reverse request: %w", err)
	}
	// A REQUEST IS NOT CHUNKED — the chunking path is the response half (D14) —
	// so a payload above one frame is refused here rather than handed to
	// EncodeFrame, which panics on it. A public key, a signature and a
	// challenge are all far below this bound; the guard is here because a
	// panic in a helper holding somebody's session is not an acceptable way to
	// learn that one day it is not.
	if len(payload) > proto.MaxFrameBytes {
		return fmt.Errorf("host: reverse request for %s.%s exceeds one frame: %d bytes", service, op, len(payload))
	}

	ch := make(chan proto.Response, 1)
	h.mu.Lock()
	if h.reverse == nil {
		h.reverse = make(map[uint64]chan proto.Response)
	}
	h.reverse[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.reverse, id)
		h.mu.Unlock()
	}()

	if err := h.write(proto.TypeRequest, payload); err != nil {
		return fmt.Errorf("host: reverse request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return &proto.Refusal{Code: resp.Error.Code, Message: resp.Error.Message, Details: resp.Error.Details}
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("host: reverse result: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		// The caller gave up, or the transport died and its request context was
		// cancelled. Either way the answer has nowhere to go: the wait ends
		// here rather than holding the connection's shutdown open.
		return ctx.Err()
	}
}

// reverseResponse routes one inbound response to the Ask waiting for it.
//
// An id nobody is waiting for is logged and dropped rather than answered or
// acted on: the only way to produce one is for a caller to stop waiting — a
// cancelled probe, or a connection that ended — and the request it belonged to
// is already over.
func (h *Host) reverseResponse(payload []byte) {
	var resp proto.Response
	if err := json.Unmarshal(payload, &resp); err != nil {
		h.log.Warn("malformed response to a reverse request", "err", err)
		return
	}
	h.mu.Lock()
	ch, ok := h.reverse[resp.ID]
	if ok {
		delete(h.reverse, resp.ID)
	}
	h.mu.Unlock()
	if !ok {
		h.log.Warn("response names no reverse request this helper is waiting for", "id", resp.ID)
		return
	}
	// Buffered with room for one, and the entry is gone: this send cannot
	// block, whatever the waiting side does next.
	ch <- resp
}

// mintReverseID mints the id of one reverse request. It counts from 1 in its
// own space, which is what the frame type makes safe: a response is only ever
// read by the side that SENT the request it answers, so the coordinator's ids
// and the helper's never meet. The mutex is h.mu, shared with the maps it
// guards — one lock for the connection's small pieces of state, as everywhere
// else in this file.
func (h *Host) mintReverseID() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reverseSeq++
	return h.reverseSeq
}

// reverseCorr mints the correlation id of one reverse request, the same value
// the coordinator's own requests carry (D26: both sides of the hop write the
// same corr down). Its failure is not a failure of the request: a missing corr
// costs a log line its join, which is worth less than the request itself.
func reverseCorr() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// compile-time proof that a refusal is what Ask hands a service, and that it
// carries a code a service can put straight back on the wire as its own
// (host.RefusalCoder).
var _ proto.Coded = (*proto.Refusal)(nil)
