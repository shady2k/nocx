// Package clienthost implements the native-host capabilities of a
// coordinator that has no window (nocx-uo1k6, design D3).
//
// Before the daemon split the backend WAS the Wails process, so the
// composition root injected Wails-backed implementations of
// transport.DialogService, transport.UrlOpener and notify.AttentionHost
// directly. The coordinator is now a process of its own with zero or more
// attached clients, and those three seams keep their meaning by being
// implemented HERE, against one narrow port: ask an attached client to
// perform the effect and report what it said.
//
// AD-3 read against this, because D3 gives the shell a job it did not have:
// the shell ends up IMPLEMENTING capabilities the coordinator asks for and
// owning no decision about them. Which pane a click focuses, which URL may
// be opened, whether a second picker may stack — every one of those stays on
// this side of the wire.
//
// Nothing here decides WHICH client serves an ask: ADR-0026 §16 decided that
// (broadcast, first-consumer-wins) and the broker implements it. The adapters
// call one method and map its outcome.
package clienthost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/transport"
)

// Requester is the coordinator's channel to an attached client: one ask, one
// answer, or an honest error. It is the whole of what this package depends
// on — the transport satisfies it, and a test satisfies it without a socket.
type Requester interface {
	RequestHost(ctx context.Context, ask transport.HostAsk) (transport.HostAnswer, error)
}

// Dialogs is transport.DialogService backed by an attached client.
//
// It deliberately does NOT implement transport.UploadPicker. That picker
// answers with a source ticket the renderer could not have authored (design
// R2), and the mint has to happen where the path is; with the backend in
// another process the picked path would have to cross the client to reach
// the mint, which is exactly the property R2 exists to deny. So
// dialog.openFileForUpload keeps reporting itself unavailable — the honest
// degrade — until that is designed rather than quietly weakened.
type Dialogs struct {
	req Requester
}

// NewDialogs builds the client-backed picker.
func NewDialogs(req Requester) *Dialogs { return &Dialogs{req: req} }

// OpenFile asks a client for the native file picker. The DialogService
// contract is unchanged and is now literally true of the far side too: the
// call returns when the person acts, "" means they cancelled, and nothing
// here can dismiss a picker that is already on screen — so the transport's
// capacity-one waiting gate is still what stops a second one stacking.
func (d *Dialogs) OpenFile(ctx context.Context) (string, error) {
	return d.pick(ctx, transport.HostCapOpenFile)
}

// OpenDirectory asks a client for the native directory picker. The file
// picker's sibling in every respect the coordinator can see.
func (d *Dialogs) OpenDirectory(ctx context.Context) (string, error) {
	return d.pick(ctx, transport.HostCapOpenDirectory)
}

func (d *Dialogs) pick(ctx context.Context, cap transport.HostCapability) (string, error) {
	answer, err := d.req.RequestHost(ctx, transport.HostAsk{Capability: cap})
	if err != nil {
		return "", err
	}
	if answer.Cancelled {
		// A dismissal is an OUTCOME, not a failure: the caller reads "" the
		// way it always has, and the transport turns it into a result
		// rather than an error (ws_dialog.go).
		return "", nil
	}
	return answer.Path, nil
}

// URLs is transport.UrlOpener backed by an attached client.
type URLs struct {
	req Requester
}

// NewURLs builds the client-backed browser opener.
func NewURLs(req Requester) *URLs { return &URLs{req: req} }

// OpenURL asks a client to open the URL in its platform browser. The
// transport has already refused anything that is not an http(s) URL with a
// host; this adapter adds no second gate, because a second answer to
// "may this be opened" is the duplication AGENTS.md names.
func (u *URLs) OpenURL(ctx context.Context, url string) error {
	_, err := u.req.RequestHost(ctx, transport.HostAsk{
		Capability: transport.HostCapOpenURL,
		URL:        url,
	})
	return err
}

// Attention is notify.AttentionHost backed by an attached client, plus the
// other half of a banner: what happens when a person clicks one.
type Attention struct {
	req   Requester
	log   *slog.Logger
	focus func(sessionID string)
}

// NewAttention builds the client-backed attention surface. focus is the
// composition root's session-focus push — the renderer owns session→tab and
// the backend cannot do it at all (nocx-wyp3p) — and may be nil on a host
// that has no such push, in which case a click only raises a window.
func NewAttention(req Requester, logger *slog.Logger, focus func(sessionID string)) *Attention {
	if logger == nil {
		logger = slog.Default()
	}
	return &Attention{req: req, log: logger, focus: focus}
}

// Banner asks a client to present one desktop banner. Only the event's
// presentation fields and its addressing session id cross the wire — the
// same three the Wails adapter reads (internal/notify/wailsadapter).
func (a *Attention) Banner(ctx context.Context, ev notify.Event) error {
	_, err := a.req.RequestHost(ctx, transport.HostAsk{
		Capability: transport.HostCapBanner,
		Title:      ev.Title,
		Body:       ev.Body,
		SessionID:  ev.SessionID,
	})
	return unavailableIfNoHost(err)
}

// Badge asks a client to set its dock badge; 0 clears it.
func (a *Attention) Badge(ctx context.Context, count int) error {
	_, err := a.req.RequestHost(ctx, transport.HostAsk{
		Capability: transport.HostCapBadge,
		Count:      count,
	})
	return unavailableIfNoHost(err)
}

// Bounce asks a client for the attention bounce.
func (a *Attention) Bounce(ctx context.Context) error {
	_, err := a.req.RequestHost(ctx, transport.HostAsk{Capability: transport.HostCapBounce})
	return unavailableIfNoHost(err)
}

// Activated is transport.AttentionActivation: a person clicked a banner a
// client presented. Two halves, and neither is the client's to decide.
//
// The WINDOW is raised by asking a client, because only a shell has a window
// manager. The PANE is focused by the coordinator's own session-focus push,
// because the renderer owns session→tab and the destination is resolved from
// the session's current subscriber — which may be a different connection
// from the one that showed the banner. A click on a session no pane holds
// moves the window and nothing else, which is the honest outcome rather than
// an error (unchanged from before the split).
func (a *Attention) Activated(ctx context.Context, sessionID string) {
	if _, err := a.req.RequestHost(ctx, transport.HostAsk{Capability: transport.HostCapFocusWindow}); err != nil {
		// Said out loud and not fatal: the pane focus below is still worth
		// attempting, and a click that moves the tab without raising the
		// window is better than one that does nothing.
		a.log.Warn("notification click could not raise a window", "sessionId", sessionID, "error", err)
	}
	if a.focus == nil {
		a.log.Error("notification click cannot be honored: no session-focus path is wired", "sessionId", sessionID)
		return
	}
	a.focus(sessionID)
}

// unavailableIfNoHost reports a missing client as notify.ErrUnavailable AS
// WELL AS the capability's own error.
//
// Both words are true and both are load-bearing. "No UI host attached" is
// what a caller asked about; "unavailable on this host" is what the notify
// pipeline's one exemption from the failure feed is written against — a
// channel that does not exist right now is not a channel that lost a
// message. Wrapping both keeps that exemption working without a second
// spelling of absence (AD-8), and errors.Is answers either question.
func unavailableIfNoHost(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, transport.ErrNoUIHost) {
		return fmt.Errorf("%w: %w", notify.ErrUnavailable, err)
	}
	return err
}
