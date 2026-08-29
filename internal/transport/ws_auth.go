package transport

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Authorization for the local WebSocket.
//
// Behind /session, `open` creates a PTY. Binding loopback on an ephemeral port
// is friction for an attacker, not authorization: any page in any browser on
// this machine can scan loopback and connect. Two independent controls sit in
// front of the upgrade, and both reject BEFORE it, so a rejected caller never
// gets a socket at all:
//
//  1. A capability token minted per launch, carried in Sec-WebSocket-Protocol.
//  2. An Origin/Host policy, injected per runtime mode.
//
// The token is the control that actually authorizes. The Origin/Host policy is
// defence in depth against the browser-driven case: a page cannot forge Origin,
// so even a leaked token does not let a foreign page drive the socket.

// tokenProtocolPrefix namespaces the capability inside the subprotocol list.
//
// The token travels in Sec-WebSocket-Protocol because the browser WebSocket API
// cannot set Authorization; a query parameter would leak into URLs, proxy logs
// and devtools; and a first-frame handshake would authenticate after the
// upgrade, which is too late — the socket would already exist.
const tokenProtocolPrefix = "nocx.token."

// tokenProtocol renders a token as the subprotocol a client must offer.
func tokenProtocol(token string) string { return tokenProtocolPrefix + token }

// tokenBytes is the raw entropy behind a token. 32 bytes is well past what an
// attacker could search against a loopback listener.
const tokenBytes = 32

// OriginPolicy decides whether a pre-upgrade request may proceed, given the
// Origin header (empty when the client is not a browser) and the Host it
// addressed us by.
//
// It is an interface rather than a constant because the answer differs by
// runtime mode, and getting it wrong is expensive in a specific way: a rejected
// legitimate client fails identically to a bad token — no upgrade, no socket —
// so a wrong production Origin looks exactly like a token bug.
type OriginPolicy interface {
	Allow(origin, host string) bool
}

// wailsOriginScheme and the wailsOriginHost* constants are what the shipped
// webview actually sends, captured from real runs rather than guessed:
//
//	macOS (WKWebView):  origin=wails://wails.localhost:34115  host=127.0.0.1:49308
//	Linux (WebKitGTK):  origin=wails://wails                  host=127.0.0.1:42723
//	Linux (Wails v3):   origin=wails://localhost              host=127.0.0.1:42665
//
// The macOS form was read off the CI e2e job on macos-latest, where `wails dev`
// runs the real WKWebView alongside Playwright's browser. The first Linux form
// was captured from a v2 packaged build on WebKitGTK, which uses a bare "wails"
// hostname without the ".localhost" suffix; the v3 form, which uses
// "localhost" itself, was captured from a v3 packaged build. The port is the
// dev asset server's and is absent in a packaged build, which is why matching
// ignores it — pinning the full string would pass in CI and reject the app in
// release, the exact failure this policy was warned about.
const (
	wailsOriginScheme    = "wails"
	wailsOriginHost      = "wails.localhost" // macOS WKWebView
	wailsOriginHostLinux = "wails"           // Linux WebKitGTK (v2)
	wailsOriginHostV3    = "localhost"       // Linux WebKitGTK (Wails v3)
)

// LoopbackOriginPolicy is the development policy. Two shapes are legitimate:
// the app's own webview, which sends the wails:// scheme, and an ordinary
// http(s) loopback URL — the browser dev path and the port-forwarded
// verification loop.
//
// An absent Origin is allowed. Browsers always send one on a WebSocket
// handshake, so absence means the caller is not a page — and a non-browser
// caller still has to present the token. Rejecting absence would only break
// native clients and tests while stopping nobody.
type LoopbackOriginPolicy struct{}

func (LoopbackOriginPolicy) Allow(origin, host string) bool {
	if !hostIsLoopback(host) {
		return false
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if isWailsWebviewOrigin(u) {
		return true
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return hostIsLoopback(u.Host)
}

// isWailsWebviewOrigin reports whether an Origin is the app's own webview.
//
// Scheme and hostname must both match; the port is ignored because it exists
// only under `wails dev`. Three hostnames are accepted because the webview
// differs by platform and Wails generation: macOS WKWebView sends
// "wails.localhost", Linux WebKitGTK sent "wails" under v2 and sends
// "localhost" under v3. A browser page cannot forge any of them — no page is
// served from a wails:// origin — so accepting them costs nothing against the
// threat the Origin check exists for, which is a foreign page driving the
// socket.
func isWailsWebviewOrigin(u *url.URL) bool {
	if u.Scheme != wailsOriginScheme {
		return false
	}
	h := u.Hostname()
	if h == "" { // wails://wails.localhost parses the host into Opaque on some forms
		h = strings.TrimPrefix(u.Opaque, "//")
	}
	return h == wailsOriginHost || h == wailsOriginHostLinux || h == wailsOriginHostV3
}

// PinnedOriginPolicy is the production policy: exactly the origins the shipped
// webview sends, and nothing else.
//
// An empty allowlist denies everything. That is deliberate — the alternative,
// treating "not configured yet" as "allow anything", would reinstate the hole
// this type exists to close, and it would do so silently.
type PinnedOriginPolicy struct {
	Origins []string
}

func (p PinnedOriginPolicy) Allow(origin, host string) bool {
	if !hostIsLoopback(host) {
		return false
	}
	for _, allowed := range p.Origins {
		if allowed != "" && origin == allowed {
			return true
		}
	}
	return false
}

// hostIsLoopback reports whether a Host header addresses this machine's
// loopback interface. This is what closes DNS rebinding: the request reaches
// our listener either way, so only the Host it was addressed by reveals that
// the client resolved an attacker-controlled name to 127.0.0.1.
func hostIsLoopback(host string) bool {
	if host == "" {
		return false
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// WithTokenSource replaces the entropy source used to mint the token.
// Injected so the fail-closed path is testable; production uses crypto/rand.
func WithTokenSource(r io.Reader) WSServerOption {
	return func(s *WSServer) { s.tokenSource = r }
}

// WithOriginPolicy sets the Origin/Host policy. Defaults to the loopback
// policy, which is correct for development and for the port-forwarded
// verification loop.
func WithOriginPolicy(p OriginPolicy) WSServerOption {
	return func(s *WSServer) { s.origins = p }
}

// Token returns the capability minted for this launch. Valid only after Start.
func (s *WSServer) Token() string { return s.token }

// mintToken generates the per-launch capability. It fails closed: if the
// entropy source errors, Start aborts rather than serving an empty token that
// would compare equal to an empty client offer.
func (s *WSServer) mintToken() error {
	src := s.tokenSource
	if src == nil {
		src = rand.Reader
	}
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(src, buf); err != nil {
		return fmt.Errorf("ws token: %w", err)
	}
	// Unpadded base64url: '=', '+' and '/' are not valid in a subprotocol
	// name, and a padded value would be silently mangled by the header.
	s.token = base64.RawURLEncoding.EncodeToString(buf)
	return nil
}

// authorize runs both controls before the upgrade. It writes the response and
// returns false when the request must not proceed.
func (s *WSServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	policy := s.origins
	if policy == nil {
		policy = LoopbackOriginPolicy{}
	}
	if !policy.Allow(r.Header.Get("Origin"), r.Host) {
		// Origin and Host ARE logged, despite being attacker-controlled. The
		// first version of this omitted them on the grounds that untrusted text
		// does not belong in logs, and that cost an afternoon: 36 rejections per
		// CI run with no way to tell a genuine attack from our own client being
		// refused, which is the failure this policy is most likely to produce.
		// A rejection nobody can diagnose is worse than one that quotes what it
		// rejected. slog quotes and escapes the values, so the log-injection
		// concern is handled by the encoder rather than by discarding evidence.
		// Neither header carries a secret — the token travels in a different one
		// and is never logged.
		s.log.Warn("ws upgrade rejected",
			"reason", "origin_or_host",
			"origin", r.Header.Get("Origin"),
			"host", r.Host,
			"path", r.URL.Path)
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if !s.tokenOffered(r) {
		// The token itself is never logged; how many subprotocols were offered
		// distinguishes "client sent nothing" from "client sent a wrong value",
		// which are different bugs.
		s.log.Warn("ws upgrade rejected",
			"reason", "token",
			"origin", r.Header.Get("Origin"),
			"host", r.Host,
			"protocols_offered", len(r.Header.Values("Sec-WebSocket-Protocol")))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// tokenOffered reports whether the client offered the capability among its
// subprotocols. The comparison is constant-time so the token cannot be
// recovered by timing the handshake.
func (s *WSServer) tokenOffered(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	want := []byte(s.token)
	var ok bool
	for _, raw := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			got, found := strings.CutPrefix(p, tokenProtocolPrefix)
			if !found {
				continue
			}
			// No early return: keep scanning so the work does not depend on
			// where a match sits in the offered list.
			if subtle.ConstantTimeCompare([]byte(got), want) == 1 {
				ok = true
			}
		}
	}
	return ok
}
