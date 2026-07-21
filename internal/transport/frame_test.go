package transport

import (
	"crypto/rand"
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
