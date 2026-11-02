package log

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE LEVEL RULES HAVE ONE OWNER (nocx-vh2hr.1.12).
//
// internal/app and the window process both read NOCX_LOG_LEVEL. A spelling
// one of them understands and the other does not would make the same variable
// mean two things, so the mapping is asserted here once.
func TestConfiguredLevel_UsesTheBuildDefaultAndNamesAnUnknownSpelling(t *testing.T) {
	defaultLevel := DefaultLevel()
	for _, tc := range []struct {
		raw     string
		want    slog.Level
		unknown string
	}{
		{"", defaultLevel, ""},
		{"debug", slog.LevelDebug, ""},
		{"INFO", slog.LevelInfo, ""},
		{" warn ", slog.LevelWarn, ""},
		{"warning", slog.LevelWarn, ""},
		{"error", slog.LevelError, ""},
		{"loud", defaultLevel, "loud"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, unknown := ConfiguredLevel(tc.raw)
			if got != tc.want {
				t.Fatalf("ConfiguredLevel(%q) = %s, want %s", tc.raw, got, tc.want)
			}
			if unknown != tc.unknown {
				t.Fatalf("ConfiguredLevel(%q) unknown = %q, want %q", tc.raw, unknown, tc.unknown)
			}
		})
	}
}

// HandlerOptions carries the source on every record, which is what makes a
// line name the code that wrote it rather than the adapter in front of it.
func TestHandlerOptions_AddsTheSource(t *testing.T) {
	if !HandlerOptions(slog.LevelInfo).AddSource {
		t.Fatal("HandlerOptions does not add the source")
	}
}

// OpenFile is the one way a log file is created: its directory exists, it is
// owner-only, and a second open appends rather than truncating — the window's
// several instances share one file, so a truncate would erase the first
// window's account of a failure.
func TestOpenFile_AppendsWithoutTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "nocx-window.log")
	first, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, writeErr := first.WriteString("first line\n"); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	second, err := OpenFile(path)
	if err != nil {
		t.Fatalf("second OpenFile: %v", err)
	}
	if _, writeErr := second.WriteString("second line\n"); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := second.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	body, err := os.ReadFile(path) //nolint:gosec // this test's own temp directory
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "first line\nsecond line\n" {
		t.Fatalf("log file = %q, want both lines", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("log file mode = %v; it must not be group/world readable", info.Mode().Perm())
	}
	if !strings.HasSuffix(path, "nocx-window.log") {
		t.Fatal("unreachable: the fixture path changed")
	}
}
