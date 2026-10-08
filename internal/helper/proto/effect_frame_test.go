package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestEffectFrameRoundTripsIdentityTitleAndBody(t *testing.T) {
	want := EffectFrame{
		Session: [16]byte{1, 2}, Subscriber: [16]byte{3, 4},
		Generation: 7, EffectID: 19, StreamOffset: 12345, Kind: EffectNotification, Title: []byte("Tests failed"), Body: []byte("2 failed"),
	}
	got, err := DecodeEffectFrame(EncodeEffectFrame(want))
	if err != nil {
		t.Fatalf("DecodeEffectFrame: %v", err)
	}
	if got.Session != want.Session || got.Subscriber != want.Subscriber || got.Generation != want.Generation || got.EffectID != want.EffectID || got.StreamOffset != want.StreamOffset || got.Kind != want.Kind || !bytes.Equal(got.Title, want.Title) || !bytes.Equal(got.Body, want.Body) {
		t.Fatalf("effect frame = %+v, want %+v", got, want)
	}
}

func TestPromptBoundaryEffectFrameRoundTripsEmptyPayload(t *testing.T) {
	want := EffectFrame{Session: [16]byte{1}, Subscriber: [16]byte{2}, Generation: 4, EffectID: 8, StreamOffset: 8192, Kind: EffectPromptBoundary}
	got, err := DecodeEffectFrame(EncodeEffectFrame(want))
	if err != nil {
		t.Fatalf("DecodeEffectFrame: %v", err)
	}
	if got.Session != want.Session || got.Subscriber != want.Subscriber || got.Generation != want.Generation || got.EffectID != want.EffectID || got.StreamOffset != want.StreamOffset || got.Kind != EffectPromptBoundary || len(got.Title) != 0 || len(got.Body) != 0 {
		t.Fatalf("prompt-boundary frame = %+v", got)
	}
}

func TestEffectFrameABIIncludesExactStreamOffset(t *testing.T) {
	got := EncodeEffectFrame(EffectFrame{
		Session: [16]byte{1}, Subscriber: [16]byte{2},
		Generation: 0x030405060708090a, EffectID: 0x0b0c0d0e0f101112,
		StreamOffset: 0x131415161718191a, Kind: EffectPromptBoundary,
	})
	want, err := hex.DecodeString("0100000000000000000000000000000002000000000000000000000000000000030405060708090a0b0c0d0e0f101112131415161718191a0600000000")
	if err != nil {
		t.Fatalf("decode golden vector: %v", err)
	}
	if len(got) != EffectFrameHeaderLen || !bytes.Equal(got, want) {
		t.Fatalf("effect frame wire = %x, want %x", got, want)
	}
}

func TestHelperProtocolVersionBumpedForEffectOffset(t *testing.T) {
	if Version != "18" {
		t.Fatalf("helper protocol version = %q, want 18", Version)
	}
}

func TestEffectFrameRefusesUnknownKind(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	wire[56] = 255
	if _, err := DecodeEffectFrame(wire); err == nil {
		t.Fatal("unknown effect kind was accepted")
	}
}

func TestEffectFrameRejectsTitleLengthPastPayload(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	wire[56] = byte(EffectNotification)
	binary.BigEndian.PutUint32(wire[57:61], 1)
	if _, err := DecodeEffectFrame(wire); err != ErrEffectFrameTitleLength {
		t.Fatalf("invalid title length error = %v, want %v", err, ErrEffectFrameTitleLength)
	}
}

func TestEffectFrameRejectsShortHeader(t *testing.T) {
	if _, err := DecodeEffectFrame(make([]byte, EffectFrameHeaderLen-1)); err != ErrEffectFrameTooShort {
		t.Fatalf("short frame error = %v, want %v", err, ErrEffectFrameTooShort)
	}
}
