package hostsvc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// TestRefusalCodesAndDetails pins the plan's closed list, one row per code.
// It is a unit test on purpose: this box may run as root, where a directory's
// permission bits refuse nothing, so the half of scenario 14 that needs a real
// EACCES is covered both here (by the error the provider produces) and by the
// end-to-end test (skipped only where the kernel overrules the mode bits).
func TestRefusalCodesAndDetails(t *testing.T) {
	svc := newTestService(t, local.New())
	for _, tc := range []struct {
		name string
		err  error
		code string
		want map[string]any
	}{
		{name: "cancelled request", err: context.Canceled, code: ErrCodeTimedOut},
		{name: "expired deadline", err: context.DeadlineExceeded, code: ErrCodeTimedOut},
		{name: "not found", err: &filesystem.ErrNotFound{Path: "/x"}, code: ErrCodeNotFound},
		{name: "permission denied", err: &filesystem.ErrPermission{Path: "/x"}, code: ErrCodePermissionDenied},
		{name: "not a directory", err: &filesystem.ErrNotDir{Path: "/x"}, code: ErrCodeNotDir},
		{name: "not regular", err: &filesystem.ErrNotRegular{Path: "/x", Kind: filesystem.KindDir}, code: ErrCodeNotRegular, want: map[string]any{"kind": "dir"}},
		{name: "invalid path", err: &filesystem.ErrInvalidPath{Path: "x", Reason: "path is not absolute"}, code: ErrCodeInvalidPath, want: map[string]any{"reason": "path is not absolute"}},
		{name: "invalid page", err: &filesystem.ErrInvalidPage{Offset: -1, Limit: 0}, code: ErrCodeInvalidPage},
		{name: "entry cap", err: &filesystem.ErrTooLarge{ObservedCount: 3, Limit: 2}, code: ErrCodeTooLarge, want: map[string]any{"observed": 3.0, "limit": 2.0}},
		{name: "listing size cap", err: &filesystem.ErrTooLargeSize{ObservedBytes: 9, Limit: 8}, code: "too-large-size", want: map[string]any{"observed": 9.0, "limit": 8.0}},
		{name: "entry cap is not the size cap", err: &filesystem.ErrTooLarge{ObservedCount: 9, Limit: 8}, code: ErrCodeTooLarge, want: map[string]any{"observed": 9.0, "limit": 8.0}},
		{name: "elapsed cap", err: &filesystem.ErrTimedOut{Timeout: time.Second}, code: ErrCodeTimedOut},
		{name: "raw EACCES", err: &os.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}, code: ErrCodePermissionDenied},
		{name: "raw ENOENT", err: &os.PathError{Op: "open", Path: "/x", Err: syscall.ENOENT}, code: ErrCodeNotFound},
		{name: "raw ENOTDIR", err: &os.PathError{Op: "open", Path: "/x", Err: syscall.ENOTDIR}, code: ErrCodeNotDir},
		{name: "unclassified", err: errors.New("boom"), code: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, details := svc.Refusal(tc.err)
			if code != tc.code {
				t.Fatalf("code = %q, want %q", code, tc.code)
			}
			if tc.want == nil {
				return
			}
			var got map[string]any
			if err := json.Unmarshal(details, &got); err != nil {
				t.Fatalf("details: %v", err)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("details[%q] = %v, want %v (details %s)", k, got[k], v, details)
				}
			}
		})
	}
}

// TestKindNameSpellsWhatTheObjectIs: filesystem.Kind flattens every special
// object into `other`, which is the right wire vocabulary for a row and the
// wrong one for a refusal a person reads.
func TestKindNameSpellsWhatTheObjectIs(t *testing.T) {
	for _, tc := range []struct {
		mode os.FileMode
		want string
	}{
		{mode: os.ModeDir | 0o750, want: "dir"},
		{mode: os.ModeNamedPipe | 0o600, want: "fifo"},
		{mode: os.ModeSocket | 0o600, want: "socket"},
		{mode: os.ModeDevice | os.ModeCharDevice | 0o600, want: "device"},
		{mode: os.ModeIrregular, want: "other"},
		{mode: 0o600, want: "other"}, // a regular file never reaches a refusal for being regular
	} {
		if got := kindName(tc.mode); got != tc.want {
			t.Errorf("kindName(%v) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}
