package transport

// The unified control-plane registration: every JSON-RPC control method is
// declared once, at server construction, as a methodSpec pairing the method
// with the submission that runs it. handleControlFrame has no branch and no
// switch: it looks the method up and calls registration.Submission.TrySubmit
// — the submission decides whether the work runs now (immediate), on a worker
// goroutine under the lane (admission-backed), or never (refused, which the
// caller answers with the saturation error / notification).
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

	"github.com/shady2k/nocx/internal/transport/control"
)

// ingressCriticalMethods is the closed set of methods that run inline on the
// read loop via control.ImmediateSubmission. buildMethodSpecs enforces the
// set in both directions: a method in it must be registered immediate, and no
// other method may be. The set is deliberate and closed — a handler that
// wrongly claims immediate recreates the original bug (a blocking handler on
// the read loop freezes every tab). It grows only for a handler whose
// non-blocking shape is proven and whose latency the read loop can afford:
// agent.laneInteractivity is a mutex-guarded state update whose consumer —
// the run lease, a pending requestor under the ask stream — waits on the
// transition, so a report queued behind a full lane would delay the
// awaiting-takeover transition and a lease that has not seen it would keep
// enforcing its bounds on a TUI the human now owns.
var ingressCriticalMethods = map[string]struct{}{
	"ack":                          {},
	"vault.unlockResolved":         {},
	"connections.passwordResolved": {},
	// The broker's resolutions (nocx-e2j1z): a pending requestor — a tool
	// under the ask stream — blocks on the answer, so an answer queued
	// behind a full lane would deadlock the run. Same disposition as the
	// two existing resolvers, for the same reason.
	"agent.readScreenResolved": {},
	"agent.runResolved":        {},
	// The lane interactivity report (ADR-0020 decision 3): the run lease
	// waits on the awaiting-takeover transition it feeds, so it must never
	// queue behind the lane either. Handler: a mutex update, microseconds.
	"agent.laneInteractivity": {},
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
	method     string
	submission control.Submission
	build      func(w *wsConn, state *connState, r Responder) handlerFunc
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

// genericObject is the FLOOR every method gets until it declares a validator
// that knows its own fields: params must be absent or a JSON object, no
// string in it may exceed maxGenericStringRunes, and nesting is bounded.
//
// This is real validation, not a placeholder — a method wearing it cannot be
// reached with a bare scalar, an unbounded string or a pathological nesting.
// It is nonetheless WEAKER than a validator that knows the method's fields
// exist, are required, and mean something, and the `why` string names the
// bead that will replace it. Count them with:
//
//	grep -c 'genericObject(' internal/transport/*.go
//
// That count is a ratchet: it may only shrink.
func genericObject(why string) paramsValidator {
	_ = why
	return func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return ""
		}
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" || trimmed == "null" {
			return ""
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return "params must be a JSON object or array"
		}
		// JSON-RPC 2.0 §4.2: params, when present, is a structured value —
		// an object (named) or an array (positional). Both are legitimate
		// here and both are walked; a bare scalar is not params at all.
		// (groups.apply sends an array, which is how this floor learned it
		// had been written from one method's habits rather than the spec.)
		switch v.(type) {
		case map[string]any, []any:
		default:
			return "params must be a JSON object or array"
		}
		return walkGeneric(v, 0)
	}
}

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
	submission control.Submission
	handle     func(ctx context.Context, req jsonrpcRequest)
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
		m[name] = controlMethod{
			submission: spec.submission,
			handle:     validated(spec, next, w),
		}
	}
	return m
}

// validated is the params middleware: every control handler is wrapped in it,
// here, at the one place a connection's handler set is built. A handler
// therefore cannot be reached with params nobody checked — not because the
// author remembered, but because there is no path around this function.
//
// A refusal is -32602 with the validator's message, answered before the
// handler is entered, so a rejected request never touches a store, a vault or
// a socket.
func validated(spec methodSpec, next handlerFunc, r Responder) handlerFunc {
	return func(ctx context.Context, req jsonrpcRequest) {
		// Availability first. A method whose domain is not wired answers
		// "method not found" whatever it was sent: the caller's next move is
		// to stop calling it, not to fix its arguments.
		if spec.available != nil && !spec.available() {
			msg := spec.unavailableMessage
			if msg == "" {
				msg = "Method not found"
			}
			_ = r.TryError(req.ID, RPCError{Code: -32601, Message: msg})
			return
		}
		if msg := validateParams(spec.validate, req.Params); msg != "" {
			_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
			return
		}
		next(ctx, req)
	}
}

// handlerFunc is the per-connection form of a control handler: the request
// context and the decoded request. It is what a methodSpec's builder returns.
type handlerFunc func(ctx context.Context, req jsonrpcRequest)

// reg declares one control method: the submission that runs it and the
// per-connection handler builder. The builder receives the connection's
// wsConn (identity), its state, and the Responder to write through — the
// sealed-vault normalizer for every method (connMethods decides).
func reg(sub control.Submission, method string, v paramsValidator, build func(w *wsConn, state *connState, r Responder) handlerFunc) methodSpec {
	return methodSpec{method: method, submission: sub, build: build, validate: v}
}

// whenAvailable declares that a method answers only while its domain is
// wired, and is consulted before the validator. Compose it onto a spec:
//
//	whenAvailable(regResponder(sub, "vault.unseal", params(fn), build), wired)
func whenAvailable(spec methodSpec, available func() bool, unavailable string) methodSpec {
	spec.available = available
	spec.unavailableMessage = unavailable
	return spec
}

// regResponder declares a method whose handler needs only the connection's
// Responder — the common case. Handlers that need connection identity
// (subscriber registration, capture tab id, tunnel ownership) use reg with
// the *wsConn directly.
func regResponder(sub control.Submission, method string, v paramsValidator, build func(r Responder) handlerFunc) methodSpec {
	return reg(sub, method, v, func(_ *wsConn, _ *connState, r Responder) handlerFunc { return build(r) })
}

// methodClassFor maps a refused method to its coarse server-side class for
// the control.saturated notification. The class is server vocabulary (the
// schema's "never the raw method name"): the first dot-segment, with the
// session-plane methods mapped to "session".
func methodClassFor(method string) string {
	for i := range len(method) {
		if method[i] == '.' {
			prefix := method[:i]
			if class, ok := coarseMethodClasses[prefix]; ok {
				return class
			}
			return prefix
		}
	}
	return "session"
}

// coarseMethodClasses maps the known method-name prefixes to their coarse
// classes. Unknown prefixes pass through as their own segment; the classes
// exist so the renderer groups refusals by product area, never by raw method.
var coarseMethodClasses = map[string]string{
	"profiles":    "config",
	"groups":      "config",
	"endpoints":   "config",
	"settings":    "config",
	"uistate":     "config",
	"secrets":     "secrets",
	"vault":       "vault",
	"git":         "git",
	"files":       "fs",
	"fs":          "fs",
	"history":     "history",
	"export":      "export",
	"shell":       "shell",
	"sshConfig":   "ssh",
	"tunnel":      "tunnel",
	"ports":       "ports",
	"dialog":      "dialog",
	"connections": "connections",
	"sessions":    "session",
}
