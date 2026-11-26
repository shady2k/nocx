package discovery

import (
	"strings"
	"testing"
)

// splitFrame and its sentinel are the exec seam's framing rule, so they stay
// in this package even though the table parsers moved to internal/portsprobe:
// what a probe prints around its table is the transport's contract with the
// helper, and the parsers only ever see the body.
func TestSplitFrame(t *testing.T) {
	body := "LISTEN 0 4096 127.0.0.1:53 0.0.0.0:*\n"
	framed := "NOCX-PD/1\n" + body + "NOCX-PD/1\n"

	got, leading, trailing := splitFrame([]byte(framed))
	if !leading || !trailing {
		t.Fatalf("valid frame: leading=%v trailing=%v, want both true", leading, trailing)
	}
	if string(got) != strings.TrimSuffix(body, "\n") {
		t.Errorf("body = %q, want %q", got, strings.TrimSuffix(body, "\n"))
	}

	// Empty body (no listeners): sentinel-only output is a valid empty
	// sample.
	empty := "NOCX-PD/1\nNOCX-PD/1\n"
	if _, leading, trailing := splitFrame([]byte(empty)); !leading || !trailing {
		t.Errorf("empty frame: leading=%v trailing=%v, want both true", leading, trailing)
	}

	// Missing leading sentinel — the framing violation: rejected whole.
	if _, leading, _ := splitFrame([]byte("Welcome to example.com\nNOCX-PD/1\n")); leading {
		t.Error("unframed output passed the leading check")
	}

	// Missing trailing sentinel — truncated output: leading ok, trailing not.
	truncated := "NOCX-PD/1\nLISTEN 0 4096 127.0.0.1:53 0.0.0.0:*\n"
	if _, leading, trailing := splitFrame([]byte(truncated)); !leading || trailing {
		t.Errorf("truncated frame: leading=%v trailing=%v, want true/false", leading, trailing)
	}

	// Empty output.
	if _, leading, _ := splitFrame(nil); leading {
		t.Error("empty output passed the leading check")
	}
}
