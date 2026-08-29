// Package uistate owns the state the app must remember WITHOUT being asked:
// window geometry, the sidebar's collapse/view/width, and which tab is in
// front. It is deliberately not the settings registry — see
// docs/decisions/0048-ui-state-is-a-document-not-a-setting.md, which draws the
// line: a setting is something a user deliberately chooses; UI state is a side
// effect of using the app.
//
// The store is internal/storage's DocumentStore (ADR-0011 §1 already assigned
// this data to it by name), one JSON document, atomically written and
// human-recoverable. Nothing here is exported by backup (ADR-0027) and nothing
// here appears on a Settings page: it is per machine, and it was never decided.
package uistate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/storage"
)

// DocumentName is the file this module owns inside Paths.ConfigDir().
const DocumentName = "uistate.json"

// schemaVersion is this module's current document version. Each module owns
// its own monotonic number; there is no app-wide one (ADR-0011 §6).
const schemaVersion storage.SchemaVersion = 1

// module is the storage.Module for the UI-state document. It carries no
// migrations yet because version 1 is the first shape there has ever been.
var module = storage.Module{
	Name:    "uistate",
	Current: schemaVersion,
}

// Window is the recorded window geometry.
//
// Every field has a meaningful zero, which is what makes an absent document an
// ordinary state rather than an error path (ADR-0048 §4):
//
//   - Width or Height of 0 means "no size was ever recorded" — use the default.
//   - Displays of "" means "no position that can be trusted" — the window is
//     centred rather than placed. That covers both a never-saved window and a
//     save made while the platform could not enumerate its displays.
//
// Width/Height/X/Y always describe the NORMAL window, never the maximised or
// full-screen one: those are states, restored by asking the platform to enter
// them, so that leaving the state lands where the user last left the window
// (ADR-0048 §6.4).
type Window struct {
	Width      int  `json:"width"`
	Height     int  `json:"height"`
	X          int  `json:"x"`
	Y          int  `json:"y"`
	Maximised  bool `json:"maximised"`
	FullScreen bool `json:"fullScreen"`
	// Displays fingerprints the attached displays at the moment the position
	// was recorded. See Fingerprint: it is identity, not containment, and the
	// string is human-readable on purpose so somebody reading the file can see
	// why their window moved.
	Displays string `json:"displays"`
}

// Sidebar is the app-shell panel's remembered state. Width is a WHOLE number
// of CSS pixels: nothing about a panel edge is meaningful to seven decimal
// places, and a fractional one is what put 206.3828125 px on a Settings page.
type Sidebar struct {
	Collapsed    bool   `json:"collapsed"`
	ActiveViewID string `json:"activeViewId"`
	Width        int    `json:"width"`
}

// Layout is the renderer's half of the document: everything the webview knows
// and the Go side cannot. It is the whole of what crosses the wire
// (ADR-0048 §7) — window geometry deliberately does not, in either direction,
// because the renderer can neither know it nor act on it.
type Layout struct {
	Sidebar   Sidebar `json:"sidebar"`
	ActiveTab string  `json:"activeTab"`
}

// Document is the on-disk shape.
type Document struct {
	SchemaVersion int     `json:"schemaVersion"`
	Window        Window  `json:"window"`
	Sidebar       Sidebar `json:"sidebar"`
	ActiveTab     string  `json:"activeTab"`
}

// Window defaults. The first two are the sizes main.go hardcoded before this
// package existed; the minima are the window's declared MinWidth/MinHeight and
// are enforced here as well so a hand-edited document cannot produce a window
// smaller than the shell can lay out.
const (
	DefaultWindowWidth  = 1024
	DefaultWindowHeight = 768
	MinWindowWidth      = 640
	MinWindowHeight     = 480
)

// Sidebar width bounds, in CSS pixels. These mirror
// frontend/src/sidebar-width.ts, which is the existing owner of the policy —
// the minimum is the Git dense row's floor, the maximum is the width at which
// the panel plus the activity bar would own more than half of a 1280px window.
// Move the numbers in both places.
const (
	DefaultSidebarWidth = 240
	MinSidebarWidth     = 200
	MaxSidebarWidth     = 640
)

// Display is what the platform can tell us about one attached display.
//
// It carries no origin, because Wails v2's runtime.ScreenGetAll does not
// report one. That absence is the reason the missing-display rule below is
// phrased as identity rather than containment — see Fingerprint.
type Display struct {
	Primary bool
	Width   int
	Height  int
}

// Fingerprint reduces the attached displays to a stable, human-readable
// string: the count, then each display's logical size in canonical order, with
// the primary marked "p".
//
//	3:1920x1080,1920x1080,2560x1440p
//
// It is canonicalised rather than taken in the platform's order because that
// order is not guaranteed stable across launches, and an unstable fingerprint
// would report "the displays changed" every time and never restore a position.
//
// WHY A FINGERPRINT AND NOT A BOUNDS CHECK. The failure being prevented is a
// window restored to coordinates that are nowhere — geometry saved with a
// second monitor attached, reopened without it. The natural rule is "is the
// saved rectangle still on a screen", and it is unimplementable here:
// ScreenGetAll returns no screen origins, so the desktop's union rectangle
// cannot be computed and any containment test would silently always pass.
// Identity is what the API can actually answer.
//
// An empty list returns "", which every caller reads as "unknown" and treats
// as a mismatch: when we cannot tell, we open somewhere visible.
func Fingerprint(displays []Display) string {
	if len(displays) == 0 {
		return ""
	}
	parts := make([]string, 0, len(displays))
	for _, d := range displays {
		suffix := ""
		if d.Primary {
			suffix = "p"
		}
		parts = append(parts, fmt.Sprintf("%dx%d%s", d.Width, d.Height, suffix))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%d:%s", len(displays), strings.Join(parts, ","))
}

// Placement is what to apply to the window at start.
type Placement struct {
	Width, Height int
	X, Y          int
	// UsePosition is false when the position must not be applied — the
	// display set differs from the one the position was recorded on, or none
	// was ever recorded. The caller then leaves the window where the platform
	// puts it, or centres it.
	UsePosition bool
	Maximise    bool
	FullScreen  bool
}

// Restore decides where the window opens, from what was saved and what is
// attached now. It is pure — no Wails, no clock, no I/O — which is the only
// reason the mismatch and the clamp can be tested at all. The Wails side reads
// the probe, calls this, and applies the answer.
func Restore(saved Window, displays []Display) Placement {
	p := Placement{
		Width:      saved.Width,
		Height:     saved.Height,
		X:          saved.X,
		Y:          saved.Y,
		Maximise:   saved.Maximised,
		FullScreen: saved.FullScreen,
	}

	if p.Width <= 0 || p.Height <= 0 {
		p.Width, p.Height = DefaultWindowWidth, DefaultWindowHeight
	}

	// The size clamp runs on EVERY path, including a matching fingerprint: a
	// size saved on a larger display, or a hand-edited absurd one, must not
	// produce a window bigger than the screen it opens on.
	maxW, maxH := primarySize(displays)
	if maxW > 0 && p.Width > maxW {
		p.Width = maxW
	}
	if maxH > 0 && p.Height > maxH {
		p.Height = maxH
	}
	if p.Width < MinWindowWidth {
		p.Width = MinWindowWidth
	}
	if p.Height < MinWindowHeight {
		p.Height = MinWindowHeight
	}

	// Identity, not containment. A mismatch keeps the size and drops only the
	// position; the saved position stays in the document, so plugging the
	// monitor back in restores the old arrangement.
	p.UsePosition = saved.Displays != "" && saved.Displays == Fingerprint(displays)
	if !p.UsePosition {
		p.X, p.Y = 0, 0
	}
	return p
}

// primarySize reports the primary display's logical size, or the largest
// display's when none is flagged primary. Zero means unknown.
func primarySize(displays []Display) (int, int) {
	for _, d := range displays {
		if d.Primary && d.Width > 0 && d.Height > 0 {
			return d.Width, d.Height
		}
	}
	w, h := 0, 0
	for _, d := range displays {
		if d.Width*d.Height > w*h {
			w, h = d.Width, d.Height
		}
	}
	return w, h
}

// Observe folds a live sample into the recorded window state.
//
// The whole of its job is the maximised/full-screen case: the platform reports
// the maximised size as the window size, and recording that would restore a
// window that merely LOOKS maximised and unmaximises to the wrong place. So
// while the window is in one of those states only the flags move, and the
// normal geometry underneath is carried forward untouched.
func Observe(prev, live Window) Window {
	next := live
	if !live.Maximised && !live.FullScreen {
		return next
	}
	if prev.Width > 0 && prev.Height > 0 {
		next.Width, next.Height = prev.Width, prev.Height
		next.X, next.Y = prev.X, prev.Y
		if prev.Displays != "" {
			next.Displays = prev.Displays
		}
	}
	return next
}

// ClampSidebarWidth applies the panel's width policy: a whole number of CSS
// pixels inside the declared bounds. A value that is not usable at all — zero,
// negative — yields the default rather than the minimum, because "absent" and
// "the user dragged it as narrow as it goes" are different facts.
func ClampSidebarWidth(width int) int {
	switch {
	case width <= 0:
		return DefaultSidebarWidth
	case width < MinSidebarWidth:
		return MinSidebarWidth
	case width > MaxSidebarWidth:
		return MaxSidebarWidth
	default:
		return width
	}
}

// sanitise repairs a document field by field. It never rejects the whole
// document, because one unknown sidebar view id must not also throw away
// window geometry that was perfectly good (ADR-0048 §4).
func sanitise(d Document) Document {
	d.SchemaVersion = int(schemaVersion)
	d.Sidebar.Width = ClampSidebarWidth(d.Sidebar.Width)
	if d.Window.Width < 0 || d.Window.Height < 0 {
		d.Window.Width, d.Window.Height = 0, 0
	}
	return d
}

// defaultDocument is what an absent, unreadable or unparseable document
// yields. It is a value that works, not a sentinel.
func defaultDocument() Document {
	return Document{
		SchemaVersion: int(schemaVersion),
		Sidebar:       Sidebar{Width: DefaultSidebarWidth},
	}
}
