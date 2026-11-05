package transport

// app.diagnostics — the record Copy diagnostics pastes (nocx-3an1s.1.8).
//
// # Why it rides the IMMEDIATE submission
//
// The moment a person needs this method is the moment the control plane is
// saturated: on 2026-11-05 killing the helper left the server answering
// "Control plane busy" for every pane, and a diagnostics method that queued
// behind the same permits would have been refused at exactly that moment.
// `app.about` already rides the bounded lane and inherits that problem; this
// one deliberately does not. It runs inline on the read loop, takes no gate,
// no lane and no queue, and does only bounded work: two small maps under a
// mutex, a stat and one runtime stack read.
//
// # What it says
//
// The text is rendered by internal/diagnostics, which owns the vocabulary:
// recent and pending opens with every stage and duration, recent refusals with
// their scope and holders, every admission's current holders, the server's
// uptime, the helper's connection, the log's size and the goroutine count.
// Nothing in it names a host, a user, an address, a path or a fingerprint: a
// destination is a per-run label, the log is a size, and a failure is a
// bounded code.

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/diagnostics"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/transport/control"
)

// DiagnosticsProvider is the facts only the composition root knows: whether
// this machine's helper is connected and for how long, and the log file's
// size. Injected rather than reached for, the same rule app.about's build
// descriptor follows.
type DiagnosticsProvider interface {
	HelperStatus() diagnostics.HelperStatus
	LogSize() (int64, bool)
}

// WithDiagnostics wires the record and the facts the diagnostics method
// answers with. Without it the method still exists and says the record is
// unavailable — a surface that appears only in some builds is how a feature
// ships unreachable.
func WithDiagnostics(rec *diagnostics.Recorder, provider DiagnosticsProvider) WSServerOption {
	return func(s *WSServer) {
		s.diagnostics = rec
		s.diagnosticsProvider = provider
	}
}

type diagnosticsResult struct {
	Text string `json:"text"`
}

type diagnosticsHandlers struct {
	rec      *diagnostics.Recorder
	provider DiagnosticsProvider
	started  time.Time
	admits   []control.Admission
	r        Responder
}

func (h diagnosticsHandlers) handle(_ context.Context, req jsonrpcRequest) {
	if h.rec == nil {
		_ = h.r.TryResult(req.ID, mustMarshal(diagnosticsResult{
			Text: "nocx diagnostics\nrecord: unavailable in this build\n",
		}))
		return
	}
	now := time.Now()
	facts := diagnostics.Facts{ServerStarted: h.started}
	if h.provider != nil {
		facts.Helper = h.provider.HelperStatus()
		if n, ok := h.provider.LogSize(); ok {
			facts.LogBytes, facts.LogKnown = n, true
		}
	}
	facts.Admissions = admissionViews(h.admits, now)
	text := diagnostics.Render(h.rec.Snapshot(), facts)
	_ = h.r.TryResult(req.ID, mustMarshal(diagnosticsResult{Text: text}))
}

// admissionViews is every leaf admission's holders at THIS moment, in the
// order the composition root built them. A submission is unwrapped to its
// parts by control.Parts, so the scope names are the admission names a refusal
// reports.
func admissionViews(admits []control.Admission, now time.Time) []diagnostics.AdmissionView {
	seen := make(map[string]struct{}, len(admits))
	out := make([]diagnostics.AdmissionView, 0, len(admits))
	for _, a := range admits {
		for _, part := range control.Parts(a) {
			name := part.Name()
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			view := diagnostics.AdmissionView{Scope: name}
			for _, h := range control.HoldersOf(part) {
				view.Holders = append(view.Holders, diagnostics.Holder{
					Method: h.Method, Request: h.Request, Trace: h.Trace,
					Since: h.Since, Held: h.Held(now),
				})
			}
			out = append(out, view)
		}
	}
	return out
}

// noteRefusal is the ONE place a control-plane refusal is logged and recorded
// (nocx-3an1s.1.8). Two callers reach it: the dispatcher for a submission
// refusal, and capability's operations through the sink the dispatcher put on
// the request context for a gate or lane refusal.
//
// It is at WARN, in the shipped build, because the owner's 300 MB log held no
// line about the refusals that were on screen: a request refused at a gate
// answered with an RPC error and nothing else. `control.saturated` remains the
// renderer's toast; this is the server's own record of the same fact.
func (s *WSServer) noteRefusal(ctx context.Context, request, method, disposition string, rej control.Rejection) {
	now := time.Now()
	holders := make([]diagnostics.Holder, 0, len(rej.Holders))
	oldest := time.Duration(0)
	for _, h := range rej.Holders {
		held := h.Held(now)
		if held > oldest {
			oldest = held
		}
		holders = append(holders, diagnostics.Holder{
			Method: h.Method, Request: h.Request, Trace: h.Trace,
			Since: h.Since, Held: held,
		})
	}
	if s.diagnostics != nil {
		s.diagnostics.RecordRefusal(now, method, rej.Scope, holders)
	}
	// THE EXISTING DIAGNOSTIC, EXTENDED (nocx-3an1s.1.8). method, methodClass,
	// scope, disposition and retryAfterMs were already the line a refusal was
	// supposed to produce; the holders and their age are what was missing.
	// `rej.Reason` is deliberately NOT here: it is free text, and the rule
	// ws_acceptance_test.go enforces is that a refusal's free text never
	// reaches the log.
	// Through log.From(ctx): the refusal line carries the request id and the
	// trace id of the frame it refused, the same as every other line of that
	// exchange, and the log-context ratchet's rule holds for a new call site.
	log.From(ctx).Warn("control action refused",
		"method", method,
		"methodClass", methodClassFor(method),
		"request", request,
		"scope", rej.Scope,
		"disposition", disposition,
		"retryAfterMs", rej.RetryAfter.Milliseconds(),
		"held_ms", oldest.Milliseconds(),
		"holders", holderText(holders))
}

func holderText(holders []diagnostics.Holder) string {
	if len(holders) == 0 {
		return "none"
	}
	out := ""
	for i, h := range holders {
		if i > 0 {
			out += "; "
		}
		out += h.Method + "(" + h.Request + ") " + h.Held.Round(time.Millisecond).String()
	}
	return out
}

// diagnosticsMethod registers the method. Its own group of one, so what it
// does NOT have — a submission queue, a gate, a wired domain — is visible
// rather than inferred from a neighbour.
func (s *WSServer) diagnosticsSpec() methodSpec {
	return regResponder(control.ImmediateSubmission{}, "app.diagnostics", noParams(), func(r Responder) handlerFunc {
		h := diagnosticsHandlers{
			rec: s.diagnostics, provider: s.diagnosticsProvider,
			started: s.diagStarted, admits: s.diagAdmits, r: r,
		}
		return func(ctx context.Context, req jsonrpcRequest) { h.handle(ctx, req) }
	})
}
