package transport

// The unified control-plane registration: every JSON-RPC control method is
// declared once, at server construction, as a methodSpec pairing the method
// with the submission that runs it. handleControlFrame has no branch and no
// switch: it looks the method up and calls registration.Submission.TrySubmit
// — the submission decides whether the work runs now (immediate), on a worker
// goroutine under the lane (admission-backed), or never (refused, which the
// caller is answered with the declaration's own outcome).
//
// The ingress-critical set is closed and VALIDATED at construction. A handler
// that wrongly claims immediate recreates the original bug: a blocking
// handler on the read loop freezes every tab on the socket. The methods below
// are the complete set that must never queue — the reason is concrete for the
// resolvers: RequestUnlock and RequestConnectionPassword block until their
// resolution arrives over the same socket the read loop consumes, so a
// resolution queued behind a full lane would deadlock the ask. ack is ring
// trimming, bounded bookkeeping whose delay would close the AD-10 credit
// window. Nothing else may pair an immediate disposition with a handler.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ingressCriticalMethods is the closed set of methods that run inline on the
// read loop via control.ImmediateSubmission, and take no credit. buildMethodSpecs
// enforces the set in both directions: a method in it must be registered
// immediate, and no other method may be. The set is deliberate and closed — a
// handler that wrongly claims immediate recreates the original bug (a blocking
// handler on the read loop freezes every tab). It grows only for a handler
// whose non-blocking shape is proven and whose latency the read loop can
// afford, and its members are ADR-8eej3e66 §7's rescue traffic: a resolution,
// a cancellation, a liveness probe, the credit advertisement itself and the
// diagnostics a person reads when everything else is stuck.
//
// agent.laneInteractivity is a mutex-guarded state update whose consumer —
// the run lease, a pending requestor under the ask stream — waits on the
// transition, so a report queued behind other work would delay the
// awaiting-takeover transition. transport.ping is a liveness probe; its
// handler only reads the clock and enqueues a response.
// ingressCritical reports whether a method is answered inline on the read
// loop. It is the one question the dispatch path asks of the set (whether a
// turn deadline can even apply: an inline answer never queues), and it keeps
// the map itself private to this file.
func ingressCritical(method string) bool {
	_, ok := ingressCriticalMethods[method]
	return ok
}

var ingressCriticalMethods = map[string]struct{}{
	"ack":                          {},
	"vault.unlockResolved":         {},
	"connections.passwordResolved": {},
	// The broker's resolutions (nocx-e2j1z): a pending requestor — a tool
	// under the ask stream — blocks on the answer, so an answer queued
	// behind a full lane would deadlock the run. Same disposition as the
	// two existing resolvers, for the same reason.
	"agent.runResolved": {},
	// The client host's resolution (nocx-uo1k6, design D3): the asking task
	// — a dialog, a URL open, a banner, a window raise — blocks on the
	// answer while holding a lane permit, so an answer queued behind a full
	// lane would deadlock the ask against the very work the client is
	// unblocking. Same disposition as every other resolver, for the reason
	// every other resolver has it.
	"host.resolved": {},
	// The lane interactivity report (ADR-0020 decision 3): the run lease
	// waits on the awaiting-takeover transition it feeds, so it must never
	// queue behind other work. Handler: a mutex update, microseconds.
	"agent.laneInteractivity": {},
	// transport.credit is the credit advertisement itself (ADR-8eej3e66
	// §7). It is the first frame a client sends and must be answered before
	// the client may send anything ordinary, so it takes no credit: a window
	// whose own advertisement waited for the window is the deadlock the
	// advertisement exists to prevent.
	"transport.credit": {},
	"transport.ping":   {},
	"rpc.cancel":       {},
	// app.diagnostics (nocx-3an1s.1.8): the moment a person needs it is the
	// moment everything else is stuck, so a wait or a credit would make it
	// unavailable at exactly the wrong time. Its handler is bounded by construction — a
	// mutex-guarded snapshot of fixed-size rings, one stat, and a goroutine
	// COUNT; the stack summary is read only while the process is small enough
	// for it to be a millisecond (see GoroutineSummary). It runs on a person's
	// click, never per frame.
	"app.diagnostics": {},
}

// methodSpec declares one control method at server construction: the
// submission that runs it and the per-connection handler builder. The
// builder receives the connection's own wsConn, its connState and the
// Responder the handler is constructed with, and returns the handler
// closure — handlers are constructed types holding their capability and
// Responder, never the *WSServer, so a handler cannot reach a store it
// was not constructed with. The wsConn is identity (subscriber slots,
// client ids); writes go through the injected r, which connMethods wraps
// in the sealed-vault normalizer for every method (ADR-0032,
// vault_sealed.go).
type methodSpec struct {
	method string
	// op is the catalogue handle this registration runs. It is a typed handle,
	// not the string above, so a method with no declaration cannot be
	// registered: reg() is the only constructor and it takes the handle, and
	// only internal/operation/catalog can mint one (ADR-8eej3e66 §1).
	op         catalog.Method
	submission control.Submission
	build      func(w *wsConn, state *connState, r Responder) handlerFunc
	// key optionally extracts the invocation's runtime resource keys from
	// validated params — the names a declaration cannot know, such as which
	// session or which connection-held collection this call is about. Keyed
	// methods validate before admission, so both the validator and extractor
	// must do bounded, nonblocking ingress work.
	//
	// The declared keys that need no param (store:content,
	// collection:workspace-layout) are added from the catalogue by
	// orderKeysFor; this function answers only for nameless resources.
	key func(json.RawMessage) []string
	// available reports whether the method can answer at all — the domain
	// it belongs to is wired. Optional: nil means "always available", which
	// is true of most methods. Where it is NOT nil it is consulted BEFORE
	// the validator, because a method that does not exist for this build
	// has nothing to say about the shape of params sent to it. The owner
	// decided this explicitly: "метода нет" is the right answer.
	available func() bool
	// unavailableMessage is what an unavailable method answers with. Each
	// domain already had its own sentence ("vault not available", "endpoints
	// not available") and callers read them, so the gate keeps the domain's
	// words rather than flattening every domain to one string.
	unavailableMessage string
	// validate is the method's params validator, and it is REQUIRED:
	// buildMethodSpecs refuses a registration without one, so a control
	// method that nobody validated cannot exist in a built server. See
	// paramsValidator.
	validate paramsValidator
}

// paramsValidator checks one method's raw params BEFORE its handler runs.
// It returns "" when the params are acceptable, and otherwise the message
// the caller receives as -32602.
//
// WHY THIS IS A FIELD AND NOT A HABIT. Validation that lives inside each
// handler is validation that can be forgotten, and it was: endpoints.probe
// shipped checking only that its base URL was non-empty while dialling that
// URL, putting its model in a request body and its key in an HTTP header.
// Nothing in the code said anything was missing, because nothing required
// anything to be there. Making the validator a required field of the
// registration turns "somebody forgot" into "the server does not build",
// which is the same enforcement this file already applies to the
// ingress-critical set.
//
// Declare one of two things at every registration, and there is no third:
//
//   - params(fn) — the method takes params and fn checks every reachable
//     field: presence, bounds, and shape.
//   - noParams — the method takes NONE. This is an assertion, not an
//     exemption: the middleware enforces that params really are absent,
//     null or an empty object, and refuses anything else.
type paramsValidator func(raw json.RawMessage) string

// params declares a method's params validator.
func params(fn func(raw json.RawMessage) string) paramsValidator { return fn }

// noParams declares that a method takes no params, and enforces it. A method
// registered with this cannot be reached with a payload — a caller that
// sends one is refused rather than quietly ignored, so a renderer that
// starts sending something has to be met by a deliberate change here.
func noParams() paramsValidator {
	return func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return ""
		}
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" || trimmed == "null" || trimmed == "{}" {
			return ""
		}
		return "this method takes no params"
	}
}

// maxGenericStringRunes bounds any single string in a params object checked
// by genericObject. Generous enough for every legitimate control-plane
// string this repo sends (paths, ids, passphrases, commit messages) and far
// below anything that could be used to make the server do work by volume.
const maxGenericStringRunes = 64_000

// maxGenericDepth bounds nesting in a params object checked by genericObject.
// A control request is a flat-ish record; deep nesting is how a small payload
// becomes expensive to walk.
const maxGenericDepth = 16

func walkGeneric(v any, depth int) string {
	if depth > maxGenericDepth {
		return fmt.Sprintf("params nest deeper than %d levels", maxGenericDepth)
	}
	switch t := v.(type) {
	case string:
		if utf8.RuneCountInString(t) > maxGenericStringRunes {
			return fmt.Sprintf("a params string exceeds %d characters", maxGenericStringRunes)
		}
	case map[string]any:
		for _, item := range t {
			if msg := walkGeneric(item, depth+1); msg != "" {
				return msg
			}
		}
	case []any:
		for _, item := range t {
			if msg := walkGeneric(item, depth+1); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// maxParamsBytes bounds any method's params payload. A control-plane frame
// is a request, never a data channel — the data plane is binary and separate
// (AD-1) — so a params object larger than this is a hostile or broken
// caller, and the bound applies before any per-method validator so a
// validator never has to parse something enormous to refuse it.
const maxParamsBytes = 8 << 20

// validateParams applies the universal bound and then the method's own
// validator. This is the ONE place params are checked, and every control
// request goes through it (connMethods wraps every handler with it), so a
// handler cannot be reached with params nobody looked at.
func validateParams(v paramsValidator, raw json.RawMessage) string {
	if len(raw) > maxParamsBytes {
		return fmt.Sprintf("params exceed %d bytes", maxParamsBytes)
	}
	return v(raw)
}

// controlMethod is one connection's answer to one control method: the shared
// submission and the connection-scoped handler closure.
type controlMethod struct {
	op         catalog.Method
	submission control.Submission
	handle     func(ctx context.Context, req jsonrpcRequest)
	key        func(json.RawMessage) ([]string, *RPCError)
}

// buildMethodSpecs validates and indexes the registration set. It rejects
// duplicate methods and any registration that pairs an ingress-critical
// disposition with a method outside the closed set (or a non-immediate
// disposition with a method inside it). The validation happens once, at
// construction, so a wrong claim fails the server build rather than freezing
// a socket at runtime.
func buildMethodSpecs(specs []methodSpec) (map[string]methodSpec, error) {
	m := make(map[string]methodSpec, len(specs))
	for _, spec := range specs {
		if _, dup := m[spec.method]; dup {
			return nil, fmt.Errorf("transport: duplicate registration for control method %q", spec.method)
		}
		if spec.validate == nil {
			// The architectural prohibition: a control method with no
			// declared params validator does not get to exist. Declare
			// params(fn) or noParams() — the latter is an assertion the
			// middleware enforces, not a way out.
			return nil, fmt.Errorf(
				"transport: control method %q registered without a params validator — "+
					"declare params(fn) or noParams(); validation is a field of the "+
					"registration, not a habit inside the handler", spec.method)
		}
		if !spec.op.Valid() {
			return nil, fmt.Errorf(
				"transport: control method %q registered without a catalogue handle — declare the operation in "+
					"internal/operation/catalog and pass its handle to reg(); a method with no declaration does not exist",
				spec.method)
		}
		if want := string(spec.op.ID()); want != spec.method {
			return nil, fmt.Errorf(
				"transport: control method %q is registered with the handle for %q", spec.method, want)
		}
		_, immediate := spec.submission.(control.ImmediateSubmission)
		if immediate {
			if _, critical := ingressCriticalMethods[spec.method]; !critical {
				return nil, fmt.Errorf(
					"transport: method %q registered with an immediate submission — "+
						"the ingress-critical set is closed (%d methods); a handler that wrongly "+
						"claims it freezes the read loop", spec.method, len(ingressCriticalMethods))
			}
		} else if _, critical := ingressCriticalMethods[spec.method]; critical {
			return nil, fmt.Errorf(
				"transport: ingress-critical method %q registered without an immediate "+
					"submission — its resolution must never queue behind the lane", spec.method)
		}
		m[spec.method] = spec
	}
	return m, nil
}

// connMethods materialises the per-connection handler set from the server's
// validated specs. It is called once per connection, after the connState
// exists; the handlers capture the connection's Responder, never the server.
//
// Every handler is constructed with the sealed-vault normalizer (ADR-0032,
// vault_sealed.go): a failure that is a sealed-vault failure is rewritten to
// the canonical sealed shape, so the renderer's dispatcher raises the unlock
// and re-sends the request — one wrapper, every method, including the ones
// not written yet. The normalizer is a pure rewrite (no blocking, no ask),
// so the ingress-critical set is untouched: a resolution still never waits
// behind the lane, and nothing new can block the read loop.
func connMethods(specs map[string]methodSpec, w *wsConn, state *connState) map[string]controlMethod {
	m := make(map[string]controlMethod, len(specs))
	for name, spec := range specs {
		norm := newSealedNormalizer(w)
		next := spec.build(w, state, norm)
		method := controlMethod{
			op:         spec.op,
			submission: spec.submission,
			handle:     validated(spec, next, w),
		}
		if spec.key != nil {
			// Validate once, before key allocation/admission rather than in
			// the worker. Ownership remains a handler-time decision.
			method.handle = next
			method.key = func(raw json.RawMessage) ([]string, *RPCError) {
				if refusal := paramsRefusal(spec, raw); refusal != nil {
					return nil, refusal
				}
				return spec.key(raw), nil
			}
		}
		m[name] = method
	}
	return m
}

// validated applies the shared params/availability gate on the worker for
// ordinary methods. Keyed methods apply the same gate before admission, so a
// rejected request never allocates ordering state. Both paths are wired by
// connMethods: no handler is reachable without validation.
func validated(spec methodSpec, next handlerFunc, r Responder) handlerFunc {
	return func(ctx context.Context, req jsonrpcRequest) {
		if refusal := paramsRefusal(spec, req.Params); refusal != nil {
			_ = r.TryError(req.ID, *refusal)
			return
		}
		next(ctx, req)
	}
}

func paramsRefusal(spec methodSpec, raw json.RawMessage) *RPCError {
	// Availability first: an unwired method has nothing to say about params.
	if spec.available != nil && !spec.available() {
		msg := spec.unavailableMessage
		if msg == "" {
			msg = "Method not found"
		}
		return &RPCError{Code: -32601, Message: msg}
	}
	if msg := validateParams(spec.validate, raw); msg != "" {
		return &RPCError{Code: -32602, Message: "Invalid params: " + msg}
	}
	return nil
}

// handlerFunc is the per-connection form of a control handler: the request
// context and the decoded request. It is what a methodSpec's builder returns.
type handlerFunc func(ctx context.Context, req jsonrpcRequest)

// reg declares one control method: the submission that runs it and the
// per-connection handler builder. The builder receives the connection's
// wsConn (identity), its state, and the Responder to write through — the
// sealed-vault normalizer for every method (connMethods decides).
func reg(sub control.Submission, op catalog.Method, v paramsValidator, build func(w *wsConn, state *connState, r Responder) handlerFunc) methodSpec {
	return methodSpec{method: string(op.ID()), op: op, submission: sub, build: build, validate: v}
}

// whenAvailable declares that a method answers only while its domain is
// wired, and is consulted before the validator. Compose it onto a spec:
//
//	whenAvailable(regResponder(sub, catalog.OpVaultUnseal, params(fn), build), wired)
func whenAvailable(spec methodSpec, available func() bool, unavailable string) methodSpec {
	spec.available = available
	spec.unavailableMessage = unavailable
	return spec
}

// regResponder declares a method whose handler needs only the connection's
// Responder — the common case. Handlers that need connection identity
// (subscriber registration, capture tab id, tunnel ownership) use reg with
// the *wsConn directly.
func regResponder(sub control.Submission, op catalog.Method, v paramsValidator, build func(r Responder) handlerFunc) methodSpec {
	return reg(sub, op, v, func(_ *wsConn, _ *connState, r Responder) handlerFunc { return build(r) })
}
