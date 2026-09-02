package transport

// The live frame's shape on the wire (nocx-u3vxd): a row is its TEXT. The
// cell grid was measured at 878 KB for one `top` screen whose text is ~9 KB,
// and no consumer on either side ever read a cell attribute — so the wire
// refuses a cells row rather than accepting two shapes for one row.

import (
	"encoding/json"
	"strings"
	"testing"
)

func liveFrameParams(t *testing.T, cols int, rows ...map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"requestId": "req-1",
		"outcome":   "frame",
		"rows":      rows,
		"cursor":    map[string]any{"line": 0, "col": 0},
		"identity":  map[string]any{"buffer": map[string]any{"kind": "normal"}, "cols": cols, "rows": len(rows), "generation": 1},
		"range":     map[string]any{"start": 0, "end": len(rows)},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func TestValidateReadScreenResolved_FrameRowShape(t *testing.T) {
	textRow := func(s string) map[string]any { return map[string]any{"kind": "text", "text": s} }

	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "text rows are the frame's shape",
			raw:  liveFrameParams(t, 4, textRow("hi  "), textRow("bye ")),
			want: "",
		},
		{
			name: "a row narrower than the screen is still a row",
			raw:  liveFrameParams(t, 80, textRow("hi")),
			want: "",
		},
		{
			name: "a cells row is refused",
			raw: liveFrameParams(t, 2, map[string]any{
				"kind":  "cells",
				"cells": []any{map[string]any{"char": "h", "attrs": map[string]any{}}},
			}),
			want: "a live frame row must be text",
		},
		{
			name: "a row wider than its columns can hold is refused",
			raw:  liveFrameParams(t, 4, textRow(strings.Repeat("x", 4*maxRowRunesPerCol+1))),
			want: "a live frame row is wider than its columns can hold",
		},
		{
			name: "the range must still span exactly the rows",
			raw: func() json.RawMessage {
				var m map[string]any
				if err := json.Unmarshal(liveFrameParams(t, 4, textRow("hi  ")), &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				m["range"] = map[string]any{"start": 0, "end": 7}
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				return raw
			}(),
			want: "range must be non-negative and span exactly the frame's rows",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateReadScreenResolvedRaw(tc.raw); got != tc.want {
				t.Fatalf("validateReadScreenResolvedRaw = %q, want %q", got, tc.want)
			}
		})
	}
}
