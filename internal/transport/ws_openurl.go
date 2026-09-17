package transport

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"
)

// UrlOpener opens a URL in the platform's default browser on behalf of the
// renderer. It is a control-plane capability (AD-1): the renderer has no
// path to the Wails runtime, so "open on its hosting" (brief, nocx-hc0m) is
// reached through this method over the same WebSocket as everything else.
//
// The service is often absent — the dev-web harness has no Wails at all —
// and absence is reported as a JSON-RPC -32601 error, which the renderer
// surfaces as a toast rather than as a silent no-op.
//
// Unlike files.reveal (D4), this capability is NOT local-only, and the
// difference is deliberate and written down: files.reveal operates on a
// PATH, and a path from an SSH binding refers to a file on the remote host
// that the local file manager cannot reveal — the attestation is what tells
// the transport the path is not local. Opening a URL has no path semantics:
// the string is a web address, equally valid whether the repository it was
// derived from lives on this machine or on the helper's. The only producer
// today is the git panel, which git.open already refuses for SSH sessions
// (D3), so in practice the URL is local-derived anyway — but the capability
// itself carries no local-only meaning, and the helper must be
// able to reuse it without inheriting a guard this method does not need.
type UrlOpener interface {
	// OpenURL opens the URL in the default browser. The transport has
	// already refused anything that is not an http(s) URL; the service
	// itself may still fail (no browser, no runtime) and its error is
	// returned as-is.
	OpenURL(ctx context.Context, url string) error
}

// urlOpenerHolder is the transport's mutable url-opener seam: the mutex and
// the opener it guards. The opener is assigned post-construction
// (SetUrlOpener, below) while a handler may be reading it, so the handler
// holds the holder — pointing at the WSServer's own url-opener state, one
// mutex and one opener shared between the setter and the readers — and reads
// the CURRENT opener per call.
type urlOpenerHolder struct {
	mu  *sync.RWMutex
	svc *UrlOpener
}

// get returns the current url opener, or nil when none is wired.
func (h *urlOpenerHolder) get() UrlOpener {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return *h.svc
}

// urlOpener is set post-construction: the Wails context it needs only exists
// inside WailsApp.startup (main.go), which runs after the transport is
// built. The handler may be reading it while startup assigns it, so the
// field is mutex-guarded, exactly like the dialog service.
func (s *WSServer) SetUrlOpener(uo UrlOpener) {
	s.urlMu.Lock()
	defer s.urlMu.Unlock()
	s.urlOpener = uo
}

type shellOpenUrlParams struct {
	URL string `json:"url"`
}

// openUrlHandlers answers shell.openUrl. It holds the url opener holder and
// its Responder; nothing else.
type openUrlHandlers struct {
	opener *urlOpenerHolder
	r      Responder
}

// handleShellOpenUrl opens one URL in the system browser. The URL is
// validated here, at the seam: only http(s) URLs with a host cross into the
// browser — a scheme the shell would happily open (file:, javascript:) is
// not a URL this panel may ever send a user to, and the renderer's
// conversion module only ever emits https for a recognised host. The result
// is the empty object, exactly like files.reveal.
func (h openUrlHandlers) handleShellOpenUrl(ctx context.Context, req jsonrpcRequest) {
	var params shellOpenUrlParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.URL == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: url required"})
		return
	}
	u, err := url.Parse(params.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: only http(s) URLs can be opened"})
		return
	}
	uo := h.opener.get()
	if uo == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "shell.openUrl not available"})
		return
	}
	if err := uo.OpenURL(ctx, u.String()); err != nil {
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "shell.openUrl: ", err))
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}
