//go:build linux

package sandbox

import (
	"context"
	"errors"
	"testing"

	landlock "github.com/landlock-lsm/go-landlock/landlock"
)

func TestStatusForABI(t *testing.T) {
	cases := []struct {
		name string
		abi  int
		err  error
		want Status
	}{
		{"probe error", 0, errors.New("probe failed"), Status{Available: false, Backend: BackendLandlock, Reason: ReasonLandlockUnavailable}},
		{"no usable version", 0, nil, Status{Available: false, Backend: BackendLandlock, Reason: ReasonLandlockUnavailable}},
		{"below floor", 2, nil, Status{Available: false, Backend: BackendLandlock, Reason: ReasonLandlockABITooOld, ABI: 2}},
		{"floor", 3, nil, Status{Available: true, Backend: BackendLandlock, ABI: 3}},
		{"current", 9, nil, Status{Available: true, Backend: BackendLandlock, ABI: 9}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statusForABI(tc.abi, tc.err)
			if got.Available != tc.want.Available || got.Backend != tc.want.Backend || got.Reason != tc.want.Reason || got.ABI != tc.want.ABI {
				t.Errorf("statusForABI(%d, %v) = %+v, want %+v", tc.abi, tc.err, got, tc.want)
			}
		})
	}
}

func TestServiceStatusUsesProbe(t *testing.T) {
	orig := landlockABIQuery
	defer func() { landlockABIQuery = orig }()

	svc := New(nil, t.TempDir())
	ctx := context.Background()

	landlockABIQuery = func() (int, error) { return 2, nil }
	if st := svc.Status(ctx); st.Reason != ReasonLandlockABITooOld {
		t.Errorf("abi=2: reason = %q, want %q", st.Reason, ReasonLandlockABITooOld)
	}

	landlockABIQuery = func() (int, error) { return 0, errors.New("ENOSYS") }
	if st := svc.Status(ctx); st.Reason != ReasonLandlockUnavailable {
		t.Errorf("probe error: reason = %q, want %q", st.Reason, ReasonLandlockUnavailable)
	}

	landlockABIQuery = func() (int, error) { return 9, nil }
	st := svc.Status(ctx)
	if !st.Available || st.ABI != 9 || st.Backend != BackendLandlock {
		t.Errorf("abi=9: got %+v, want available landlock ABI 9", st)
	}
}

func TestStrictConfigABI(t *testing.T) {
	cases := map[int]int{1: 1, 2: 2, 3: 3, 7: 7, 8: 8, 9: 8, 12: 8, 0: 0}
	for in, want := range cases {
		if got := strictConfigABI(in); got != want {
			t.Errorf("strictConfigABI(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestStrictConfigPresets(t *testing.T) {
	if strictConfig(8) != landlock.V8 {
		t.Error("strictConfig(8) != landlock.V8")
	}
	if strictConfig(9) != landlock.V8 {
		t.Error("strictConfig(9) must cap at landlock.V8")
	}
	if strictConfig(3) != landlock.V3 {
		t.Error("strictConfig(3) != landlock.V3")
	}
	if strictConfig(1) != landlock.V1 {
		t.Error("strictConfig(1) != landlock.V1")
	}
}
