package paneview

import "github.com/shady2k/nocx/internal/sessionruntime"

// ScreenCaptureIdentity names the helper-owned active buffer instance, its
// viewport geometry and the content generation at the capture instant.
type ScreenCaptureIdentity struct {
	Kind       string
	Instance   uint64
	Cols       int
	Rows       int
	Generation uint64
}

// ScreenCapture is the complete absolute row slice read from one session
// runtime instant. Rows and metadata cannot be composed from separate reads.
type ScreenCapture struct {
	Floor         uint64
	Start         uint64
	End           uint64
	Total         uint64
	Rows          [][]Cell
	CursorX       int
	CursorY       uint64
	CursorVisible bool
	Identity      ScreenCaptureIdentity
	Revision      sessionruntime.Revision
	Completeness  sessionruntime.Completeness
}
