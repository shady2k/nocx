package assistant

// THE WIRE, VERBATIM.
//
// wireTap is the one owner of provider capture. It records the request body
// and the response bytes as they pass the HTTP boundary; it never records
// headers, so API keys and secret-valued headers cannot enter a dump.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// WireRecorder receives the bytes of one provider exchange. Implementations
// persist them under the turn entry. The recorder is deliberately best-effort:
// losing a diagnostic must never abort an otherwise valid model response.
type WireRecorder interface {
	RecordWire(ctx context.Context, runID, entryID, kind string, body []byte, truncated bool)
}

type wireIdentity struct {
	runID   string
	entryID string
}

type wireIdentityKey struct{}

// WithWireIdentity carries the backend-owned run and turn ids through the
// model framework into the HTTP RoundTripper. The context is the only route
// across that boundary; no package global is used.
func WithWireIdentity(ctx context.Context, runID, entryID string) context.Context {
	return context.WithValue(ctx, wireIdentityKey{}, wireIdentity{runID: runID, entryID: entryID})
}

type wireToolOffer struct {
	runID   string
	effects []content.Effect
	scopes  []content.GrantScope
}

type wireToolOfferKey struct{}

// WithWireToolOffer carries the structural authority context to the HTTP
// recorder. It contains no question, prompt, arguments or tool output.
func WithWireToolOffer(ctx context.Context, runID string, grant *content.Grant) context.Context {
	if grant == nil || runID == "" {
		return ctx
	}
	return context.WithValue(ctx, wireToolOfferKey{}, wireToolOffer{
		runID:   runID,
		effects: append([]content.Effect(nil), grant.Effects...),
		scopes:  append([]content.GrantScope(nil), grant.Scopes...),
	})
}

func wireToolOfferFrom(ctx context.Context) (wireToolOffer, bool) {
	v, ok := ctx.Value(wireToolOfferKey{}).(wireToolOffer)
	return v, ok && v.runID != ""
}

func wireIdentityFrom(ctx context.Context) (wireIdentity, bool) {
	v, ok := ctx.Value(wireIdentityKey{}).(wireIdentity)
	return v, ok && v.runID != "" && v.entryID != ""
}

// wireCaptureCap is the per-direction ceiling. It matches the ledger's
// MaxArtifactBytes: one provider request or response cannot consume more than
// one artifact's budget, while truncation remains visible in the dump.
const wireCaptureCap = 1 << 20

// wireTap is an http.RoundTripper that copies a request and its response into
// the optional developer log and product recorder, and derives the run's tool
// offer from the request it observed. It changes none of them.
type wireTap struct {
	inner    http.RoundTripper
	logPath  string
	recorder WireRecorder
	logger   log.Logger
	mu       sync.Mutex
	offered  map[string]struct{}
}

func newWireTapWith(inner http.RoundTripper, logPath string, recorder WireRecorder, logger log.Logger) http.RoundTripper {
	if logPath == "" && recorder == nil && logger == nil {
		return inner
	}
	return &wireTap{inner: inner, logPath: logPath, recorder: recorder, logger: logger, offered: make(map[string]struct{})}
}

func (w *wireTap) RoundTrip(req *http.Request) (*http.Response, error) {
	var body *wireRequestBody
	if req.Body != nil {
		body = &wireRequestBody{
			parent: w,
			ctx:    req.Context(),
			orig:   req.Body,
			label:  "REQUEST " + req.Method + " " + req.URL.String(),
		}
		req.Body = body
	}
	resp, err := w.inner.RoundTrip(req)
	if body == nil {
		w.write("REQUEST " + req.Method + " " + req.URL.String())
		w.record(req.Context(), "request", nil, false)
	} else {
		// A custom transport may return without consuming or closing the body.
		// Finish here so the record still describes the bytes it observed.
		body.finish(true)
	}
	if err != nil {
		w.write("TRANSPORT ERROR\n" + err.Error())
		return resp, err
	}
	w.write(fmt.Sprintf("RESPONSE %s", resp.Status))
	resp.Body = &wireResponseBody{
		parent: w,
		ctx:    req.Context(),
		orig:   resp.Body,
		buf:    make([]byte, 0, minInt(wireCaptureCap, 64*1024)),
	}
	return resp, nil
}

func (w *wireTap) logToolOffer(ctx context.Context, body []byte) {
	if w.logger == nil {
		return
	}
	offer, ok := wireToolOfferFrom(ctx)
	if !ok {
		return
	}

	var envelope struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		w.logger.Warn("agent ask: tool offer could not be parsed", "run", offer.runID, "parseError", true)
		return
	}
	names := make([]string, 0, len(envelope.Tools))
	for _, tool := range envelope.Tools {
		if tool.Function.Name != "" {
			names = append(names, tool.Function.Name)
		}
	}
	w.mu.Lock()
	if _, seen := w.offered[offer.runID]; seen {
		w.mu.Unlock()
		return
	}
	w.offered[offer.runID] = struct{}{}
	w.mu.Unlock()
	w.logger.Info("agent ask: tools offered", "run", offer.runID, "count", len(names),
		"tools", names, "effects", offer.effects, "scopes", offer.scopes)
}

func (w *wireTap) record(ctx context.Context, kind string, body []byte, early bool) {
	if w.recorder == nil {
		return
	}
	id, ok := wireIdentityFrom(ctx)
	if !ok {
		return
	}
	truncated := early || len(body) > wireCaptureCap
	if len(body) > wireCaptureCap {
		body = body[:wireCaptureCap]
	}
	w.recorder.RecordWire(ctx, id.runID, id.entryID, kind, body, truncated)
}

func (w *wireTap) write(record string) {
	if w.logPath == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// #nosec G304 — the path is the operator's own, named in an environment
	// variable of this process; no untrusted input reaches it.
	f, err := os.OpenFile(w.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}

	defer func() { _ = f.Close() }()
	stamp := time.Now().Format("15:04:05.000")
	_, _ = fmt.Fprintf(f, "\n===== %s %s\n%s\n", stamp, strings.SplitN(record, "\n", 2)[0], record)
}

type wireRequestBody struct {
	parent   *wireTap
	ctx      context.Context
	orig     io.ReadCloser
	label    string
	buf      []byte
	overflow bool
	done     bool
	mu       sync.Mutex
}

func (b *wireRequestBody) Read(p []byte) (int, error) {
	n, err := b.orig.Read(p)
	if n > 0 {
		b.mu.Lock()
		keep := n
		if keep > wireCaptureCap-len(b.buf) {
			keep = wireCaptureCap - len(b.buf)
			b.overflow = true
		}
		if keep > 0 {
			b.buf = append(b.buf, p[:keep]...)
		}
		b.mu.Unlock()
	}
	if err == io.EOF {
		b.finish(false)
	}
	return n, err
}

func (b *wireRequestBody) Close() error {
	err := b.orig.Close()
	b.finish(true)
	return err
}

func (b *wireRequestBody) finish(closedEarly bool) {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.done = true
	body := append([]byte(nil), b.buf...)
	truncated := closedEarly || b.overflow
	b.mu.Unlock()
	b.parent.write(b.label + "\n" + string(body))
	b.parent.record(b.ctx, "request", body, truncated)
	b.parent.logToolOffer(b.ctx, body)
}

type wireResponseBody struct {
	parent   *wireTap
	ctx      context.Context
	orig     io.ReadCloser
	buf      []byte
	overflow bool
	done     bool
	mu       sync.Mutex
}

func (b *wireResponseBody) Read(p []byte) (int, error) {
	n, err := b.orig.Read(p)
	if n > 0 {
		b.mu.Lock()
		keep := n
		if keep > wireCaptureCap-len(b.buf) {
			keep = wireCaptureCap - len(b.buf)
			b.overflow = true
		}
		if keep > 0 {
			b.buf = append(b.buf, p[:keep]...)
		}
		b.mu.Unlock()
	}
	if err == io.EOF {
		b.finish(false)
	}
	return n, err
}

func (b *wireResponseBody) Close() error {
	err := b.orig.Close()
	b.finish(true)
	return err
}

func (b *wireResponseBody) finish(closedEarly bool) {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.done = true
	body := append([]byte(nil), b.buf...)
	truncated := closedEarly || b.overflow
	b.mu.Unlock()
	b.parent.write("RESPONSE BODY\n" + string(body))
	b.parent.record(b.ctx, "response", body, truncated)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
