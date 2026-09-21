package content_test

// CaptureViews — the metadata rows a settled record's views live at
// (nocx-2v80t.2.5). The property under test: a view row is an ADDRESS and a
// provenance chain, and nothing else — no chunks, no body, the record's own
// retention state riding along — plus the store's ordinary identity rules
// (replay is a no-op, a known id naming another artifact is a conflict) and
// the two refusals a chain implies: no entry to hang on, a record that is
// not stored.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func aView(entryID, recordID string, derived string, media content.MediaType) content.CaptureView {
	cols, rows := 80, 24
	return content.CaptureView{
		EntryID: entryID, RecordID: recordID,
		ID: recordID + "-view", MediaType: media,
		DerivedFrom: derived, TerminalCols: &cols, TerminalRows: &rows,
	}
}

func storeRecordForViews(t *testing.T, led content.LedgerRepository, entryID, recordID string) {
	t.Helper()
	in := aCapture(entryID, recordID)
	in.MediaType = content.MediaJSON
	if _, err := led.CaptureOutput(context.Background(), in); err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}
}

func TestCaptureViews_StoreTheProvenanceWithoutABody(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "ls -la")
	recordID := "00000000-0000-7000-8000-0000000000b1"
	storeRecordForViews(t, led, entryID, recordID)

	vt := aView(entryID, recordID, recordID, content.MediaVT)
	text := aView(entryID, recordID, vt.ID, content.MediaText)
	text.ID = recordID + "-text"
	if err := led.CaptureViews(ctx, []content.CaptureView{vt, text}); err != nil {
		t.Fatalf("CaptureViews: %v", err)
	}

	for _, v := range []content.CaptureView{vt, text} {
		art, err := led.Artifact(ctx, v.ID)
		if err != nil || art == nil {
			t.Fatalf("Artifact(%s) = %v, %v — the view row is missing", v.ID, art, err)
		}
		if art.MediaType != v.MediaType {
			t.Fatalf("view %s media type = %q, want %q", v.ID, art.MediaType, v.MediaType)
		}
		if art.DerivedFrom == nil || *art.DerivedFrom != v.DerivedFrom {
			t.Fatalf("view %s derivedFrom = %v, want %q", v.ID, art.DerivedFrom, v.DerivedFrom)
		}
		if len(art.Chunks) != 0 || art.ByteLen != 0 {
			t.Fatalf("view %s stored a body (%d bytes) — a view is an address, not a copy",
				v.ID, art.ByteLen)
		}
		if art.ExecutionID == nil {
			t.Fatalf("view %s names no execution — provenance was lost", v.ID)
		}
	}
}

func TestCaptureViews_IsIdempotentOnTheViewID(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "ls -la")
	recordID := "00000000-0000-7000-8000-0000000000b2"
	storeRecordForViews(t, led, entryID, recordID)

	vt := aView(entryID, recordID, recordID, content.MediaVT)
	if err := led.CaptureViews(ctx, []content.CaptureView{vt}); err != nil {
		t.Fatalf("first CaptureViews: %v", err)
	}
	// The retried ask: same ids, nothing to change, no error.
	if err := led.CaptureViews(ctx, []content.CaptureView{vt}); err != nil {
		t.Fatalf("replayed CaptureViews: %v", err)
	}
}

func TestCaptureViews_RefuseARecordThatIsNotStored(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "ls -la")

	ghost := "00000000-0000-7000-8000-0000000000b3"
	vt := aView(entryID, ghost, ghost, content.MediaVT)
	if err := led.CaptureViews(ctx, []content.CaptureView{vt}); err == nil {
		t.Fatal("a view beside a record nobody stored was accepted — the chain has no far end")
	}
}

func TestCaptureViews_ConflictWhenAKnownIDNamesAnotherView(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "ls -la")
	otherEntry := recordOne(t, led, "ls -la /tmp")
	recordID := "00000000-0000-7000-8000-0000000000b4"
	storeRecordForViews(t, led, entryID, recordID)

	vt := aView(entryID, recordID, recordID, content.MediaVT)
	if err := led.CaptureViews(ctx, []content.CaptureView{vt}); err != nil {
		t.Fatalf("CaptureViews: %v", err)
	}
	// The same view id naming a different chain: the id is the identity,
	// and a known id answering for another derivation is a conflict, never
	// an overwrite.
	hijack := vt
	hijack.DerivedFrom = "00000000-0000-7000-8000-0000000000ff"
	if err := led.CaptureViews(ctx, []content.CaptureView{hijack}); err == nil {
		t.Fatal("a known view id was rewritten without a conflict")
	}
	_ = otherEntry
}

func TestCaptureViews_RefuseAnUnknownEntry(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)

	orphan := aView("no-such-entry", "00000000-0000-7000-8000-0000000000b5", "00000000-0000-7000-8000-0000000000b5", content.MediaVT)
	if err := led.CaptureViews(ctx, []content.CaptureView{orphan}); err == nil {
		t.Fatal("a view hung on an entry nobody recorded was accepted")
	}
}
