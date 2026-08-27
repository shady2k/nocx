package proto

import (
	"bytes"
	"testing"
)

func TestFrameRoundTripPreservesBinaryPayload(t *testing.T) {
	payload := []byte{0x00, 0x0A, 0x0D, 0x0A, 0xFF, '{', '}'}
	var gotType FrameType
	var gotSeq, gotAck uint32
	var gotPayload []byte
	d := NewDecoder(func(ty FrameType, seq, ack uint32, p []byte) {
		gotType, gotSeq, gotAck, gotPayload = ty, seq, ack, append([]byte(nil), p...)
	}, func(int) { t.Error("unexpected gap") })
	if err := d.Feed(EncodeFrame(TypeRequest, 7, 3, payload)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if gotType != TypeRequest || gotSeq != 7 || gotAck != 3 || !bytes.Equal(gotPayload, payload) {
		t.Fatalf("round trip lost data: %v %d %d %v", gotType, gotSeq, gotAck, gotPayload)
	}
}

func TestDecoderResyncsPastGarbageAndReportsIt(t *testing.T) {
	var frames int
	var gapped int
	d := NewDecoder(func(FrameType, uint32, uint32, []byte) { frames++ },
		func(n int) { gapped += n })
	garbage := []byte("bash: nocx-helper: command not found\n")
	if err := d.Feed(append(garbage, EncodeFrame(TypeHelloOK, 0, 0, []byte(`{}`))...)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if frames != 1 {
		t.Fatalf("want the valid frame delivered, got %d", frames)
	}
	if gapped == 0 {
		t.Fatal("want the skipped region reported")
	}
}

// TestFrameSplitAcrossThreeFeedCalls is the acceptance criterion the easy
// path skips: a payload arriving in pieces at arbitrary offsets still decodes
// as one frame. The cuts land mid-header and three bytes into the payload, so
// every part of the header and body crosses a Feed boundary.
func TestFrameSplitAcrossThreeFeedCalls(t *testing.T) {
	payload := []byte(`{"id":7,"service":"git","op":"status"}`)
	frame := EncodeFrame(TypeRequest, 0xA0A0, 0x0505, payload)
	cuts := []int{5, HeaderLen + 3}
	var gotType FrameType
	var gotSeq, gotAck uint32
	var got []byte
	d := NewDecoder(func(ty FrameType, seq, ack uint32, p []byte) {
		gotType, gotSeq, gotAck, got = ty, seq, ack, append(got, p...)
	}, func(int) { t.Error("unexpected gap") })
	prev := 0
	for _, c := range cuts {
		if err := d.Feed(frame[prev:c]); err != nil {
			t.Fatalf("feed %d:%d: %v", prev, c, err)
		}
		prev = c
	}
	if err := d.Feed(frame[prev:]); err != nil {
		t.Fatalf("final feed: %v", err)
	}
	if gotType != TypeRequest || gotSeq != 0xA0A0 || gotAck != 0x0505 || !bytes.Equal(got, payload) {
		t.Fatalf("split frame lost data: %v %d %d %q", gotType, gotSeq, gotAck, got)
	}
}

// TestOversizeLengthPrefixIsGarbage pins the acceptance criterion that a
// length prefix above MaxFrameBytes is resynced past rather than allocated:
// the decoder never trusts the claimed size, advances one byte at a time,
// and still finds the valid frame that follows the bogus header.
func TestOversizeLengthPrefixIsGarbage(t *testing.T) {
	var frames int
	var gapped int
	d := NewDecoder(func(FrameType, uint32, uint32, []byte) { frames++ },
		func(n int) { gapped += n })
	oversize := make([]byte, HeaderLen)
	oversize[0] = byte(TypeRequest)
	oversize[9] = 0x20 // 1<<29, far above MaxFrameBytes
	valid := EncodeFrame(TypeResponse, 1, 2, []byte(`{"id":1}`))
	if err := d.Feed(append(oversize, valid...)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if frames != 1 {
		t.Fatalf("want the valid frame delivered, got %d", frames)
	}
	if gapped != HeaderLen {
		t.Fatalf("want %d garbage bytes, got %d", HeaderLen, gapped)
	}
}

// TestUnknownTypeByteIsScannedOneByteAtATime pins the closed-set rule:
// 0x08 is not a FrameType (TypeChunk is 7, TypeKeepAlive is 9), so its
// claimed length prefix must not be trusted. The decoder advances one byte
// at a time, so a valid frame whose header begins right after the bogus one
// is still found — skipping the whole claimed 13+100 bytes would eat it.
func TestUnknownTypeByteIsScannedOneByteAtATime(t *testing.T) {
	var frames int
	var gapped int
	d := NewDecoder(func(FrameType, uint32, uint32, []byte) { frames++ },
		func(n int) { gapped += n })
	bogus := make([]byte, HeaderLen)
	bogus[0] = 0x08 // unknown type
	bogus[9] = 0x00
	bogus[10] = 0x00
	bogus[11] = 0x00
	bogus[12] = 0x64 // claims a 100-byte payload that does not exist
	valid := EncodeFrame(TypeHelloOK, 0, 0, []byte(`{}`))
	if err := d.Feed(append(bogus, valid...)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if frames != 1 {
		t.Fatalf("want the valid frame delivered, got %d", frames)
	}
	if gapped != HeaderLen {
		t.Fatalf("want %d garbage bytes (the bogus header, byte by byte), got %d", HeaderLen, gapped)
	}
}

// TestEncodeFrameRefusesOversizePayload pins the acceptance criterion that
// EncodeFrame refuses a payload above MaxFrameBytes. The signature has no
// error return, so the refusal is a panic: emitting a truncated or oversize
// frame would corrupt the wire silently.
func TestEncodeFrameRefusesOversizePayload(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("want EncodeFrame to panic on an oversize payload")
		}
	}()
	EncodeFrame(TypeResponse, 0, 0, make([]byte, MaxFrameBytes+1))
}

// TestPayloadSurvivesSubsequentFeed is the decoder's copy discipline: the
// payload handed to onFrame must not alias the decoder's internal buffer, or
// a later Feed's append can write into that same backing array. The callback
// deliberately retains the view WITHOUT copying — the plan's verbatim test
// copies on the callback side and would not catch this.
func TestPayloadSurvivesSubsequentFeed(t *testing.T) {
	first := []byte(`{"id":7,"service":"git","op":"status"}`)
	var retainedType FrameType
	var retained []byte
	d := NewDecoder(func(ty FrameType, seq, ack uint32, p []byte) {
		if retained == nil {
			retainedType, retained = ty, p
		}
	}, func(int) { t.Error("unexpected gap") })
	if err := d.Feed(EncodeFrame(TypeRequest, 1, 1, first)); err != nil {
		t.Fatalf("first feed: %v", err)
	}
	if err := d.Feed(EncodeFrame(TypeResponse, 2, 2, []byte(`{"id":1,"result":{"ok":true}}`))); err != nil {
		t.Fatalf("second feed: %v", err)
	}
	if retainedType != TypeRequest || !bytes.Equal(retained, first) {
		t.Fatalf("payload corrupted by a subsequent feed: %v %q", retainedType, retained)
	}
}

// TestGarbageRegionAccountingSpansFeedCalls pins the decoder-state rule: the
// garbage-byte count of the current resync region lives on the Decoder, not
// in a Feed local. Split the garbage across two Feed calls and the second
// Feed must still report the full region, including the bytes the first Feed
// already scanned.
func TestGarbageRegionAccountingSpansFeedCalls(t *testing.T) {
	garbage := []byte("bash: nocx-helper: command not found\n")
	var frames int
	var gapped int
	d := NewDecoder(func(FrameType, uint32, uint32, []byte) { frames++ },
		func(n int) { gapped += n })
	mid := len(garbage) / 2
	if err := d.Feed(garbage[:mid]); err != nil {
		t.Fatalf("first feed: %v", err)
	}
	rest := append(garbage[mid:], EncodeFrame(TypeHelloOK, 0, 0, []byte(`{}`))...)
	if err := d.Feed(rest); err != nil {
		t.Fatalf("second feed: %v", err)
	}
	if frames != 1 {
		t.Fatalf("want the valid frame delivered, got %d", frames)
	}
	if gapped != len(garbage) {
		t.Fatalf("want %d gapped bytes across both feeds, got %d", len(garbage), gapped)
	}
}
