package sessionruntime

import (
	"fmt"
	"strings"
	"testing"
)

func TestReadScreenCaptureReturnsOneAbsoluteBackendOwnedRange(t *testing.T) {
	s, _, _ := realRuntime(t)
	var output strings.Builder
	for row := range 30 {
		fmt.Fprintf(&output, "row-%02d\r\n", row)
	}
	if err := s.Ingest([]byte(output.String())); err != nil {
		t.Fatalf("ingest screen rows: %v", err)
	}

	capture, err := s.ReadScreenCapture(nil)
	if err != nil {
		t.Fatalf("read backend capture: %v", err)
	}
	if capture.Start != 0 || capture.End != 31 || capture.Floor != 0 || capture.Total != 31 {
		t.Fatalf("capture range = floor %d, [%d,%d), total %d; want floor 0, [0,31), total 31",
			capture.Floor, capture.Start, capture.End, capture.Total)
	}
	if got := len(capture.Rows); got != 31 {
		t.Fatalf("capture returned %d rows, want 31", got)
	}
	if got := capture.Rows[0].Cells[0].Grapheme; got != "r" {
		t.Fatalf("first absolute row starts with %q, want r", got)
	}
	if capture.Identity.BufferInstance == 0 || capture.Generation == 0 || capture.Revision == 0 {
		t.Fatalf("capture lacks backend identity/revision: %+v", capture)
	}
	if capture.Identity.AltScreen {
		t.Fatal("buffer identity is alternate, want primary/normal")
	}
}

func TestReadScreenCaptureRefusesRowsAboveJSONSafeIntegerCeiling(t *testing.T) {
	s, _, _ := realRuntime(t)
	s.screenDepartedRows = MaxScreenRowOffset
	if _, err := s.ReadScreenCapture(nil); err == nil || !strings.Contains(err.Error(), "JSON safe-integer ceiling") {
		t.Fatalf("capture at the safe-integer ceiling returned err=%v, want refusal before row offsets become inexact", err)
	}
}

func TestReadScreenCaptureChangesIdentityAcrossAlternateBufferSessions(t *testing.T) {
	s, _, _ := realRuntime(t)
	if err := s.Ingest([]byte("primary")); err != nil {
		t.Fatalf("write primary buffer: %v", err)
	}
	primary, captureErr := s.ReadScreenCapture(nil)
	if captureErr != nil {
		t.Fatalf("capture primary buffer: %v", captureErr)
	}
	if err := s.Ingest([]byte("\x1b[?1049hALT")); err != nil {
		t.Fatalf("enter alternate buffer: %v", err)
	}
	alternate, alternateErr := s.ReadScreenCapture(nil)
	if alternateErr != nil {
		t.Fatalf("capture alternate buffer: %v", alternateErr)
	}
	if !alternate.Identity.AltScreen || alternate.Identity.BufferInstance == primary.Identity.BufferInstance {
		t.Fatalf("alternate identity = %+v, primary = %+v", alternate.Identity, primary.Identity)
	}
	if alternate.Floor != 0 || alternate.Start != 0 || alternate.Total != alternate.End || len(alternate.Rows) != alternate.Identity.Rows {
		t.Fatalf("alternate capture includes primary history: floor %d, [%d,%d), rows %d", alternate.Floor, alternate.Start, alternate.End, alternate.Total)
	}
	if err := s.Ingest([]byte("\x1b[?1049l")); err != nil {
		t.Fatalf("leave alternate buffer: %v", err)
	}
	restored, err := s.ReadScreenCapture(nil)
	if err != nil {
		t.Fatalf("capture restored primary buffer: %v", err)
	}
	if restored.Identity.AltScreen || restored.Identity.BufferInstance == primary.Identity.BufferInstance {
		t.Fatalf("restored primary identity = %+v, original = %+v; buffer identity must not be reused", restored.Identity, primary.Identity)
	}
}

func TestReadScreenCaptureRefusesARangeOutsideRetainedRows(t *testing.T) {
	s, _, _ := realRuntime(t)
	if err := s.Ingest([]byte("hello")); err != nil {
		t.Fatalf("ingest screen: %v", err)
	}
	_, err := s.ReadScreenCapture(&ScreenRange{Start: 500, End: 501})
	if err == nil {
		t.Fatal("an absolute range beyond the captured screen was silently accepted")
	}
}
