package proto

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEffectFrameRoundTripsIdentityTitleAndBody(t *testing.T) {
	want := EffectFrame{
		Session: [16]byte{1, 2}, Subscriber: [16]byte{3, 4},
		Generation: 7, EffectID: 19, Kind: EffectNotification, Title: []byte("Tests failed"), Body: []byte("2 failed"),
	}
	got, err := DecodeEffectFrame(EncodeEffectFrame(want))
	if err != nil {
		t.Fatalf("DecodeEffectFrame: %v", err)
	}
	if got.Session != want.Session || got.Subscriber != want.Subscriber || got.Generation != want.Generation || got.EffectID != want.EffectID || got.Kind != want.Kind || !bytes.Equal(got.Title, want.Title) || !bytes.Equal(got.Body, want.Body) {
		t.Fatalf("effect frame = %+v, want %+v", got, want)
	}
}

func TestEffectFrameRefusesUnknownKind(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	wire[48] = 255
	if _, err := DecodeEffectFrame(wire); err == nil {
		t.Fatal("unknown effect kind was accepted")
	}
}

func TestEffectFrameRejectsTitleLengthPastPayload(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	wire[48] = byte(EffectNotification)
	binary.BigEndian.PutUint32(wire[49:53], 1)
	if _, err := DecodeEffectFrame(wire); err != ErrEffectFrameTitleLength {
		t.Fatalf("invalid title length error = %v, want %v", err, ErrEffectFrameTitleLength)
	}
}

func TestEffectFrameRejectsShortHeader(t *testing.T) {
	if _, err := DecodeEffectFrame(make([]byte, EffectFrameHeaderLen-1)); err != ErrEffectFrameTooShort {
		t.Fatalf("short frame error = %v, want %v", err, ErrEffectFrameTooShort)
	}
}
