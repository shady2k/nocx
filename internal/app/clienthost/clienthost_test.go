package clienthost

// The adapters map one Requester outcome onto the seam each capability
// already had. What has to be true, per capability and with the failure
// paired to the success in every case:
//
//   - a client's answer reaches the caller in the caller's own vocabulary —
//     a path for a picker, nothing for an effect;
//   - a dismissal is a RESULT, not an error, because that is what
//     transport.DialogService has always meant by "";
//   - a missing client is the capability's own typed error, and for the
//     attention surface it is ALSO notify.ErrUnavailable, which is the word
//     the pipeline's one exemption from the failure feed is written against;
//   - a click raises a window and focuses a pane, and a failure of either
//     half does not cost the other.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/transport"
)

// fakeRequester is the client on the far side of the wire, canned.
type fakeRequester struct {
	asks   []transport.HostAsk
	answer transport.HostAnswer
	err    error
	// errFor overrides err for one capability, so a test can fail the
	// window raise while the rest succeeds.
	errFor map[transport.HostCapability]error
}

func (f *fakeRequester) RequestHost(_ context.Context, ask transport.HostAsk) (transport.HostAnswer, error) {
	f.asks = append(f.asks, ask)
	if err, ok := f.errFor[ask.Capability]; ok {
		return transport.HostAnswer{}, err
	}
	if f.err != nil {
		return transport.HostAnswer{}, f.err
	}
	return f.answer, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDialogs_OpenFileReturnsTheChosenPath(t *testing.T) {
	req := &fakeRequester{answer: transport.HostAnswer{Path: "/home/dev/key"}}
	path, err := NewDialogs(req).OpenFile(context.Background())
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if path != "/home/dev/key" {
		t.Fatalf("path = %q, want /home/dev/key", path)
	}
	if len(req.asks) != 1 || req.asks[0].Capability != transport.HostCapOpenFile {
		t.Fatalf("asks = %+v, want one dialog.file", req.asks)
	}
}

func TestDialogs_OpenDirectoryAsksForTheDirectoryPicker(t *testing.T) {
	req := &fakeRequester{answer: transport.HostAnswer{Path: "/home/dev/projects"}}
	path, err := NewDialogs(req).OpenDirectory(context.Background())
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	if path != "/home/dev/projects" {
		t.Fatalf("path = %q, want /home/dev/projects", path)
	}
	if req.asks[0].Capability != transport.HostCapOpenDirectory {
		t.Fatalf("capability = %q, want dialog.directory", req.asks[0].Capability)
	}
}

// A dismissal is "" and no error — the contract every caller of
// transport.DialogService already reads, and the reason the transport turns
// it into a result rather than an error.
func TestDialogs_CancelledPickerIsAnEmptyPathAndNoError(t *testing.T) {
	req := &fakeRequester{answer: transport.HostAnswer{Cancelled: true}}
	for name, call := range map[string]func(context.Context) (string, error){
		"file":      NewDialogs(req).OpenFile,
		"directory": NewDialogs(req).OpenDirectory,
	} {
		path, err := call(context.Background())
		if err != nil {
			t.Errorf("%s: a dismissal returned %v, want no error", name, err)
		}
		if path != "" {
			t.Errorf("%s: path = %q on a dismissal, want empty", name, path)
		}
	}
}

func TestDialogs_NoUIHostIsTheDialogError(t *testing.T) {
	req := &fakeRequester{err: transport.ErrNoDialogHost}
	if _, err := NewDialogs(req).OpenFile(context.Background()); !errors.Is(err, transport.ErrNoDialogHost) {
		t.Fatalf("OpenFile error = %v, want ErrNoDialogHost", err)
	}
	if _, err := NewDialogs(req).OpenDirectory(context.Background()); !errors.Is(err, transport.ErrNoDialogHost) {
		t.Fatalf("OpenDirectory error = %v, want ErrNoDialogHost", err)
	}
}

// The failure path of the picker's one external call: the client answered
// that it could not open one. Surfaced as-is, not swallowed into "".
func TestDialogs_ClientFailureSurfaces(t *testing.T) {
	req := &fakeRequester{err: errors.New("no display")}
	path, err := NewDialogs(req).OpenFile(context.Background())
	if err == nil {
		t.Fatal("a failing picker returned no error")
	}
	if path != "" {
		t.Errorf("path = %q on a failure, want empty", path)
	}
}

func TestURLs_OpenURLCarriesTheURL(t *testing.T) {
	req := &fakeRequester{}
	if err := NewURLs(req).OpenURL(context.Background(), "https://example.com/x"); err != nil {
		t.Fatalf("OpenURL: %v", err)
	}
	if req.asks[0].Capability != transport.HostCapOpenURL || req.asks[0].URL != "https://example.com/x" {
		t.Fatalf("ask = %+v, want shell.openUrl carrying the URL", req.asks[0])
	}
}

func TestURLs_NoUIHostIsTheURLError(t *testing.T) {
	req := &fakeRequester{err: transport.ErrNoURLHost}
	if err := NewURLs(req).OpenURL(context.Background(), "https://example.com"); !errors.Is(err, transport.ErrNoURLHost) {
		t.Fatalf("error = %v, want ErrNoURLHost", err)
	}
}

func TestURLs_ClientFailureSurfaces(t *testing.T) {
	req := &fakeRequester{err: errors.New("no browser")}
	err := NewURLs(req).OpenURL(context.Background(), "https://example.com")
	if err == nil || errors.Is(err, notify.ErrUnavailable) {
		t.Fatalf("error = %v, want the client's own failure", err)
	}
}

func TestAttention_BannerCarriesOnlyThePresentationFields(t *testing.T) {
	req := &fakeRequester{}
	a := NewAttention(req, quietLogger(), nil)
	err := a.Banner(context.Background(), notify.Event{
		Title:     "done",
		Body:      "the build finished",
		SessionID: "s-1",
	})
	if err != nil {
		t.Fatalf("Banner: %v", err)
	}
	ask := req.asks[0]
	if ask.Capability != transport.HostCapBanner {
		t.Fatalf("capability = %q, want attention.banner", ask.Capability)
	}
	if ask.Title != "done" || ask.Body != "the build finished" || ask.SessionID != "s-1" {
		t.Fatalf("ask = %+v, want the event's presentation fields", ask)
	}
}

func TestAttention_BadgeAndBounceAsk(t *testing.T) {
	req := &fakeRequester{}
	a := NewAttention(req, quietLogger(), nil)
	if err := a.Badge(context.Background(), 4); err != nil {
		t.Fatalf("Badge: %v", err)
	}
	if req.asks[0].Capability != transport.HostCapBadge || req.asks[0].Count != 4 {
		t.Fatalf("ask = %+v, want attention.badge with count 4", req.asks[0])
	}
	if err := a.Bounce(context.Background()); err != nil {
		t.Fatalf("Bounce: %v", err)
	}
	if req.asks[1].Capability != transport.HostCapBounce {
		t.Fatalf("capability = %q, want attention.bounce", req.asks[1].Capability)
	}
}

// A missing client is BOTH facts at once, and both are load-bearing: the
// caller asked about the UI host, and the notify pipeline's one exemption
// from the failure feed is written against notify.ErrUnavailable. A second
// spelling of absence is what AD-8 forbids, so the error carries both words
// rather than one of them being invented somewhere else.
func TestAttention_NoUIHostIsAlsoUnavailable(t *testing.T) {
	req := &fakeRequester{err: transport.ErrNoAttentionHost}
	a := NewAttention(req, quietLogger(), nil)
	for name, err := range map[string]error{
		"banner": a.Banner(context.Background(), notify.Event{}),
		"badge":  a.Badge(context.Background(), 1),
		"bounce": a.Bounce(context.Background()),
	} {
		if !errors.Is(err, transport.ErrNoAttentionHost) {
			t.Errorf("%s: error = %v, want ErrNoAttentionHost", name, err)
		}
		if !errors.Is(err, notify.ErrUnavailable) {
			t.Errorf("%s: error = %v, want it to be notify.ErrUnavailable too", name, err)
		}
	}
}

// And a client that ANSWERS "I have no such surface" is the same absence,
// which is the case the notification centre was actually breaking on
// (nocx-bu8fl): every browser-hosted client is attached and has no OS banner.
// The transport resolves that answer to the capability's own ErrNoUIHost with
// the client's sentence appended, so the wrapping has to survive one more
// layer than the missing-client case above.
func TestAttention_AClientWithNoSurfaceIsAlsoUnavailable(t *testing.T) {
	req := &fakeRequester{err: fmt.Errorf("%w: this client has no native host", transport.ErrNoAttentionHost)}
	a := NewAttention(req, quietLogger(), nil)
	for name, err := range map[string]error{
		"banner": a.Banner(context.Background(), notify.Event{}),
		"badge":  a.Badge(context.Background(), 1),
		"bounce": a.Bounce(context.Background()),
	} {
		if !errors.Is(err, notify.ErrUnavailable) {
			t.Errorf("%s: error = %v, want it to be notify.ErrUnavailable", name, err)
		}
		if !strings.Contains(err.Error(), "this client has no native host") {
			t.Errorf("%s: error = %v, want it to keep the client's sentence", name, err)
		}
	}
}

// The paired failure: a client that IS attached and failed is a failed
// delivery, not an unavailable surface. Conflating the two would exempt a
// lost banner from the failure feed.
func TestAttention_ClientFailureIsNotUnavailable(t *testing.T) {
	req := &fakeRequester{err: errors.New("no D-Bus session")}
	a := NewAttention(req, quietLogger(), nil)
	err := a.Banner(context.Background(), notify.Event{})
	if err == nil {
		t.Fatal("a failed banner returned no error")
	}
	if errors.Is(err, notify.ErrUnavailable) {
		t.Fatalf("error = %v, want a failed delivery rather than an unavailable surface", err)
	}
}

// A click is two halves and neither is the client's to decide: the window is
// raised by asking a client, the pane is focused by the coordinator's own
// session push.
func TestAttention_ActivatedRaisesAWindowAndFocusesThePane(t *testing.T) {
	req := &fakeRequester{}
	var focused []string
	a := NewAttention(req, quietLogger(), func(sid string) { focused = append(focused, sid) })
	a.Activated(context.Background(), "s-9")

	if len(req.asks) != 1 || req.asks[0].Capability != transport.HostCapFocusWindow {
		t.Fatalf("asks = %+v, want one window.focus", req.asks)
	}
	if len(focused) != 1 || focused[0] != "s-9" {
		t.Fatalf("focused = %v, want [s-9]", focused)
	}
}

// The failure path of the raise: a click that moves the tab without raising
// the window is better than one that does nothing, so the pane focus is still
// attempted.
func TestAttention_ActivatedStillFocusesWhenTheRaiseFails(t *testing.T) {
	req := &fakeRequester{errFor: map[transport.HostCapability]error{
		transport.HostCapFocusWindow: transport.ErrNoWindowHost,
	}}
	var focused []string
	a := NewAttention(req, quietLogger(), func(sid string) { focused = append(focused, sid) })
	a.Activated(context.Background(), "s-9")
	if len(focused) != 1 || focused[0] != "s-9" {
		t.Fatalf("focused = %v, want [s-9] despite the failed raise", focused)
	}
}

// And the other missing half: with no session-focus path wired the click is
// recorded rather than panicking, which is the state a host without one is in.
func TestAttention_ActivatedWithNoFocusPathDoesNotPanic(t *testing.T) {
	req := &fakeRequester{}
	a := NewAttention(req, quietLogger(), nil)
	a.Activated(context.Background(), "s-9")
	if len(req.asks) != 1 {
		t.Fatalf("asks = %+v, want the window raise to have happened anyway", req.asks)
	}
}

// The adapters satisfy the seams they replace. A compile-time assertion,
// because the whole point of this package is that the transport and the
// notify router keep asking exactly what they always asked.
var (
	_ transport.DialogService       = (*Dialogs)(nil)
	_ transport.UrlOpener           = (*URLs)(nil)
	_ notify.AttentionHost          = (*Attention)(nil)
	_ transport.AttentionActivation = (*Attention)(nil)
)

// And it deliberately does NOT satisfy the upload picker: minting a source
// ticket needs the path where the mint is, and with the backend in another
// process the path would have to cross the client to get there — which is the
// property design R2 exists to deny. dialog.openFileForUpload keeps reporting
// itself unavailable until that is designed rather than quietly weakened.
func TestDialogs_IsNotAnUploadPicker(t *testing.T) {
	var ds transport.DialogService = NewDialogs(&fakeRequester{})
	if _, ok := ds.(transport.UploadPicker); ok {
		t.Fatal("the client-backed picker claims to mint upload tickets; see design R2")
	}
}
