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
		Generation: 7, EffectID: 19, StreamOffset: 12345,
		Kind: EffectNotification, Title: []byte("Tests failed"), Body: []byte("2 failed"),
	}
	got, err := DecodeEffectFrame(EncodeEffectFrame(want))
	if err != nil {
		t.Fatalf("DecodeEffectFrame: %v", err)
	}
	if got.Session != want.Session || got.Subscriber != want.Subscriber || got.Generation != want.Generation || got.EffectID != want.EffectID || got.StreamOffset != want.StreamOffset || got.Kind != want.Kind || !bytes.Equal(got.Title, want.Title) || !bytes.Equal(got.Body, want.Body) || got.EpisodeID != want.EpisodeID {
		t.Fatalf("effect frame = %+v, want %+v", got, want)
	}
}

func TestPromptBoundaryEffectFrameRoundTripsEmptyPayload(t *testing.T) {
	want := EffectFrame{Session: [16]byte{1}, Subscriber: [16]byte{2}, Generation: 4, EffectID: 8, StreamOffset: 8192, Kind: EffectPromptBoundary}
	got, err := DecodeEffectFrame(EncodeEffectFrame(want))
	if err != nil {
		t.Fatalf("DecodeEffectFrame: %v", err)
	}
	if got.Session != want.Session || got.Subscriber != want.Subscriber || got.Generation != want.Generation || got.EffectID != want.EffectID || got.StreamOffset != want.StreamOffset || got.Kind != EffectPromptBoundary || len(got.Title) != 0 || len(got.Body) != 0 || got.EpisodeID != "" {
		t.Fatalf("prompt-boundary frame = %+v", got)
	}
}

func TestRecoveryEffectFrameRoundTripsEpisodeID(t *testing.T) {
	want := EffectFrame{
		Session: [16]byte{1}, Subscriber: [16]byte{2}, Generation: 4, EffectID: 9,
		Kind: EffectRecovery, EpisodeID: "rec-0123456789abcdef0123456789abcdef",
	}
	got, err := DecodeEffectFrame(EncodeEffectFrame(want))
	if err != nil {
		t.Fatalf("DecodeEffectFrame: %v", err)
	}
	if got.Session != want.Session || got.Subscriber != want.Subscriber || got.Generation != want.Generation || got.EffectID != want.EffectID || got.Kind != want.Kind || got.StreamOffset != 0 || got.EpisodeID != want.EpisodeID || len(got.Title) != 0 || len(got.Body) != 0 {
		t.Fatalf("recovery frame = %+v, want %+v", got, want)
	}
}

func TestEffectFrameABIIncludesExactStreamOffset(t *testing.T) {
	got := EncodeEffectFrame(EffectFrame{
		Session: [16]byte{1}, Subscriber: [16]byte{2},
		Generation: 0x030405060708090a, EffectID: 0x0b0c0d0e0f101112,
		StreamOffset: 0x131415161718191a, Kind: EffectPromptBoundary,
	})
	want, err := hex.DecodeString("0100000000000000000000000000000002000000000000000000000000000000030405060708090a0b0c0d0e0f101112131415161718191a06000000000000")
	if err != nil {
		t.Fatalf("decode golden vector: %v", err)
	}
	if len(got) != EffectFrameHeaderLen || !bytes.Equal(got, want) {
		t.Fatalf("effect frame wire = %x, want %x", got, want)
	}
}

func TestEffectFrameABIIncludesEpisodeIDLength(t *testing.T) {
	episodeID := "rec-0123456789abcdef0123456789abcdef"
	wire := EncodeEffectFrame(EffectFrame{Kind: EffectRecovery, EpisodeID: episodeID})
	if len(wire) != EffectFrameHeaderLen+len(episodeID) {
		t.Fatalf("frame length = %d, want %d", len(wire), EffectFrameHeaderLen+len(episodeID))
	}
	if got := int(binary.BigEndian.Uint16(wire[61:63])); got != len(episodeID) {
		t.Fatalf("episode length = %d, want %d", got, len(episodeID))
	}
	if string(wire[EffectFrameHeaderLen:]) != episodeID {
		t.Fatalf("episode payload = %q, want %q", wire[EffectFrameHeaderLen:], episodeID)
	}
}

func TestHelperProtocolVersionBumpedForRecoveryEpisode(t *testing.T) {
	if Version != "19" {
		t.Fatalf("helper protocol version = %q, want 19", Version)
	}
}

func TestEncodeEffectFrameRejectsInvalidRecoveryShape(t *testing.T) {
	tests := []struct {
		name  string
		frame EffectFrame
	}{
		{name: "missing episode id", frame: EffectFrame{Kind: EffectRecovery}},
		{name: "recovery title", frame: EffectFrame{Kind: EffectRecovery, EpisodeID: "rec-0123456789abcdef0123456789abcdef", Title: []byte("not empty")}},
		{name: "recovery body", frame: EffectFrame{Kind: EffectRecovery, EpisodeID: "rec-0123456789abcdef0123456789abcdef", Body: []byte("not empty")}},
		{name: "episode id on non-recovery", frame: EffectFrame{Kind: EffectNotification, EpisodeID: "rec-0123456789abcdef0123456789abcdef"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if got := recover(); got != ErrInvalidRecoveryEffect {
					t.Fatalf("EncodeEffectFrame panic = %v, want %v", got, ErrInvalidRecoveryEffect)
				}
			}()
			EncodeEffectFrame(tt.frame)
			t.Fatal("EncodeEffectFrame accepted an invalid recovery shape")
		})
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

func TestEffectFrameRejectsEpisodeLengthPastPayload(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	wire[56] = byte(EffectRecovery)
	binary.BigEndian.PutUint16(wire[61:63], 36)
	if _, err := DecodeEffectFrame(wire); err != ErrEffectFrameTitleLength {
		t.Fatalf("invalid episode length error = %v, want %v", err, ErrEffectFrameTitleLength)
	}
}

func TestEffectFrameRejectsInvalidRecoveryID(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen+4)
	wire[56] = byte(EffectRecovery)
	binary.BigEndian.PutUint16(wire[61:63], 4)
	copy(wire[EffectFrameHeaderLen:], "bad!")
	if _, err := DecodeEffectFrame(wire); err != ErrInvalidRecoveryEffect {
		t.Fatalf("invalid recovery error = %v, want %v", err, ErrInvalidRecoveryEffect)
	}
}

func TestEffectFrameRejectsShortHeader(t *testing.T) {
	if _, err := DecodeEffectFrame(make([]byte, EffectFrameHeaderLen-1)); err != ErrEffectFrameTooShort {
		t.Fatalf("short frame error = %v, want %v", err, ErrEffectFrameTooShort)
	}
}
