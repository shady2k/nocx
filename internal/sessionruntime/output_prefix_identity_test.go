package sessionruntime

import (
	"errors"
	"reflect"
	"testing"
)

// A repeated output mark splits one ingest while the alternate screen is
// active. Returning to byte-identical primary rows must still invalidate a
// target minted before that observed buffer switch (ADR-0067, nocx-vekk3).
func TestAlternateOutputPrefixRefreshInvalidatesAPrimaryTarget(t *testing.T) {
	s, _, _ := realRuntime(t)
	ctrl := sessionControl(t, s)
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("mark primary output: %v", err)
	}
	before := s.Snapshot()
	if before.Identity.AltScreen {
		t.Fatal("the target must name the primary screen")
	}
	minted := before.Identity

	// One ingest, no resize, capture or intervening snapshot. The already
	// sighted C marker splits the feed without creating a new output anchor.
	if err := s.Ingest([]byte("\x1b[?1049h" + outputMarkerFixed + "\x1b[?1049l")); err != nil {
		t.Fatalf("visit alternate output and restore primary in one ingest: %v", err)
	}
	after := s.Snapshot()
	if after.Identity.AltScreen || !reflect.DeepEqual(after.Rows, before.Rows) || after.Cursor != before.Cursor {
		t.Fatal("the visit must restore the same primary rows and cursor")
	}
	if got, want := after.Identity.BufferInstance, before.Identity.BufferInstance+2; got != want {
		t.Errorf("buffer identity = %d, want %d after the alternate and primary observations", got, want)
	}

	// The helper's TestCheckTokenRefusesOnAltScreenSwitchRegardlessOfRows
	// covers the token's refusal once identity changes. This check proves
	// the runtime still supplies that changed identity at the commit point.
	incomparable := errors.New("incomparable")
	id := admitted(t, s, ctrl, IntentKindPaste, []byte("must not reach the old target"))
	encoded, err := s.Commit(Intent{ID: id}, func(now Snapshot) error {
		if now.Identity != minted {
			return incomparable
		}
		return nil
	})
	if !errors.Is(err, incomparable) || s.IntentState(id) != IntentStateRefused || len(encoded) != 0 {
		t.Errorf("stale target commit: err=%v state=%v bytes=%q, want incomparable/refused/no bytes", err, s.IntentState(id), encoded)
	}
}
