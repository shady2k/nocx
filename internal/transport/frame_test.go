package transport

import (
	"crypto/rand"
	"fmt"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	var sid [16]byte
	_, _ = rand.Read(sid[:])
	payload := []byte("hello world\n")

	original := Frame{
		Version:   FrameVersion,
		MsgType:   MsgTypeData,
		SessionID: sid,
		Payload:   payload,
	}

	encoded := original.Encode()
	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}

	if decoded.Version != original.Version {
		t.Errorf("version mismatch: %d != %d", decoded.Version, original.Version)
	}
	if decoded.MsgType != original.MsgType {
		t.Errorf("msgType mismatch: %d != %d", decoded.MsgType, original.MsgType)
	}
	if decoded.SessionID != original.SessionID {
		t.Errorf("sessionId mismatch")
	}
	if string(decoded.Payload) != string(original.Payload) {
		t.Errorf("payload mismatch: %q != %q", decoded.Payload, original.Payload)
	}
	if len(decoded.Payload) != len(original.Payload) {
		t.Errorf("payload length mismatch: %d != %d", len(decoded.Payload), len(original.Payload))
	}
}

func TestDecodeFrameTooShort(t *testing.T) {
	_, err := DecodeFrame(make([]byte, 10))
	if err == nil {
		t.Fatal("expected error for short frame")
	}
	if err != ErrFrameTooShort {
		t.Fatalf("expected ErrFrameTooShort, got %v", err)
	}
}

func TestDecodeFrameBadVersion(t *testing.T) {
	buf := make([]byte, FrameHeaderSize)
	buf[0] = 0x99
	_, err := DecodeFrame(buf)
	if err == nil {
		t.Fatal("expected error for bad version")
	}
	if err != ErrUnknownVersion {
		t.Fatalf("expected ErrUnknownVersion, got %v", err)
	}
}

func TestDecodeFrameBadMsgType(t *testing.T) {
	// Note: DecodeFrame checks version but not msg-type.
	// The caller is responsible for msg-type validation.
	// This test verifies the frame round-trips even with unknown msg-type.
	var sid [16]byte
	_, _ = rand.Read(sid[:])

	original := Frame{
		Version:   FrameVersion,
		MsgType:   0xFF,
		SessionID: sid,
		Payload:   []byte("test"),
	}

	encoded := original.Encode()
	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if decoded.MsgType != 0xFF {
		t.Errorf("expected unknown msg-type to survive round-trip")
	}
}

func TestDecodeFrameEmptyPayload(t *testing.T) {
	var sid [16]byte
	_, _ = rand.Read(sid[:])

	f := Frame{
		Version:   FrameVersion,
		MsgType:   MsgTypeData,
		SessionID: sid,
		Payload:   nil,
	}

	encoded := f.Encode()
	if len(encoded) != FrameHeaderSize {
		t.Fatalf("expected encoded length %d, got %d", FrameHeaderSize, len(encoded))
	}

	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(decoded.Payload) != 0 {
		t.Errorf("expected empty payload, got %d bytes", len(decoded.Payload))
	}
}

func TestDecodeFrameAtMinimumSize(t *testing.T) {
	buf := make([]byte, FrameHeaderSize)
	buf[0] = FrameVersion

	f, err := DecodeFrame(buf)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(f.Payload) != 0 {
		t.Errorf("expected empty payload for minimum-size frame")
	}
}

// Golden vector shared with frontend/src/frame.test.ts.
// Any unilateral change to the wire layout that breaks parity between Go and
// TypeScript codecs must fail a test on this side, on the other side, or both.
func TestGoldenVector(t *testing.T) {
	// "0123456789abcdef0011223344556677" in raw bytes.
	sid := [16]byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
	}
	payload := []byte("hi\n")

	const want = "01010123456789abcdef001122334455667768690a"

	f := Frame{
		Version:   FrameVersion,
		MsgType:   MsgTypeData,
		SessionID: sid,
		Payload:   payload,
	}

	encoded := f.Encode()
	if len(encoded) != len(want)/2 {
		t.Fatalf("encoded length %d, want %d", len(encoded), len(want)/2)
	}
	got := fmt.Sprintf("%x", encoded)
	if got != want {
		t.Errorf("golden vector mismatch:\n got  %s\n want %s", got, want)
	}

	// Decode back.
	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame(golden): %v", err)
	}
	if decoded.Version != f.Version {
		t.Errorf("version mismatch: %d != %d", decoded.Version, f.Version)
	}
	if decoded.MsgType != f.MsgType {
		t.Errorf("msgType mismatch: %d != %d", decoded.MsgType, f.MsgType)
	}
	if decoded.SessionID != f.SessionID {
		t.Errorf("sessionId mismatch")
	}
	if string(decoded.Payload) != string(f.Payload) {
		t.Errorf("payload mismatch: %q != %q", decoded.Payload, f.Payload)
	}
}

func TestMsgTypeConstants(t *testing.T) {
	if MsgTypeData != 0x01 {
		t.Errorf("MsgTypeData must be 0x01 per wire contract")
	}
	if MsgTypeMetadata != 0x02 {
		t.Errorf("MsgTypeMetadata must be 0x02 per wire contract")
	}
	if FrameHeaderSize != 18 {
		t.Errorf("FrameHeaderSize must be 18")
	}
}
