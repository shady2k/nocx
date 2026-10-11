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
		Generation: 7, EffectID: 19,
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
	want := EffectFrame{Session: [16]byte{1}, Subscriber: [16]byte{2}, Generation: 4, EffectID: 8, StreamOffset: 8192, ScreenRevision: 7, Kind: EffectPromptBoundary}
	got, err := DecodeEffectFrame(EncodeEffectFrame(want))
	if err != nil {
		t.Fatalf("DecodeEffectFrame: %v", err)
	}
	if got.Session != want.Session || got.Subscriber != want.Subscriber || got.Generation != want.Generation || got.EffectID != want.EffectID || got.StreamOffset != want.StreamOffset || got.ScreenRevision != want.ScreenRevision || got.Kind != EffectPromptBoundary || len(got.Title) != 0 || len(got.Body) != 0 || got.EpisodeID != "" {
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

func TestEffectFrameRoundTripsStreamOffsetAndEpisodeIDWithoutMixingKinds(t *testing.T) {
	episodeID := "rec-0123456789abcdef0123456789abcdef"
	cases := []struct {
		name string
		want EffectFrame
	}{
		{
			name: "prompt boundary carries stream offset",
			want: EffectFrame{Generation: 4, EffectID: 8, StreamOffset: 0x131415161718191a, ScreenRevision: 9, Kind: EffectPromptBoundary},
		},
		{
			name: "recovery carries episode id",
			want: EffectFrame{Generation: 4, EffectID: 9, Kind: EffectRecovery, EpisodeID: episodeID},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeEffectFrame(EncodeEffectFrame(tt.want))
			if err != nil {
				t.Fatalf("DecodeEffectFrame: %v", err)
			}
			if got.StreamOffset != tt.want.StreamOffset || got.ScreenRevision != tt.want.ScreenRevision || got.EpisodeID != tt.want.EpisodeID || got.Kind != tt.want.Kind {
				t.Fatalf("decoded frame = %+v, want offset %d, revision %d, episode %q, kind %d", got, tt.want.StreamOffset, tt.want.ScreenRevision, tt.want.EpisodeID, tt.want.Kind)
			}
		})
	}
}

func TestEffectFrameRejectsStreamOffsetCombinedWithRecoveryEpisode(t *testing.T) {
	frame := EffectFrame{
		Kind: EffectRecovery, StreamOffset: 17,
		EpisodeID: "rec-0123456789abcdef0123456789abcdef",
	}
	defer func() {
		if got := recover(); got != ErrInvalidStreamOffset {
			t.Fatalf("EncodeEffectFrame panic = %v, want %v", got, ErrInvalidStreamOffset)
		}
	}()
	EncodeEffectFrame(frame)
	t.Fatal("EncodeEffectFrame accepted a recovery episode with a prompt stream offset")
}

func TestEffectFrameABIIncludesExactStreamOffset(t *testing.T) {
	got := EncodeEffectFrame(EffectFrame{
		Session: [16]byte{1}, Subscriber: [16]byte{2},
		Generation: 0x030405060708090a, EffectID: 0x0b0c0d0e0f101112,
		StreamOffset: 0x131415161718191a, ScreenRevision: 0x1b1c1d1e1f202122, Kind: EffectPromptBoundary,
	})
	want, err := hex.DecodeString("0100000000000000000000000000000002000000000000000000000000000000030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20212206000000000000")
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
	if got := int(binary.BigEndian.Uint16(wire[69:71])); got != len(episodeID) {
		t.Fatalf("episode length = %d, want %d", got, len(episodeID))
	}
	if string(wire[EffectFrameHeaderLen:]) != episodeID {
		t.Fatalf("episode payload = %q, want %q", wire[EffectFrameHeaderLen:], episodeID)
	}
}

func TestHelperProtocolVersionBumpedForRecoveryEpisode(t *testing.T) {
	if Version != "20" {
		t.Fatalf("helper protocol version = %q, want 20", Version)
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
	wire[64] = 255
	if _, err := DecodeEffectFrame(wire); err == nil {
		t.Fatal("unknown effect kind was accepted")
	}
}

func TestEffectFrameRejectsTitleLengthPastPayload(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	wire[64] = byte(EffectNotification)
	binary.BigEndian.PutUint32(wire[65:69], 1)
	if _, err := DecodeEffectFrame(wire); err != ErrEffectFrameTitleLength {
		t.Fatalf("invalid title length error = %v, want %v", err, ErrEffectFrameTitleLength)
	}
}

func TestEffectFrameRejectsEpisodeLengthPastPayload(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	wire[64] = byte(EffectRecovery)
	binary.BigEndian.PutUint16(wire[69:71], 36)
	if _, err := DecodeEffectFrame(wire); err != ErrEffectFrameTitleLength {
		t.Fatalf("invalid episode length error = %v, want %v", err, ErrEffectFrameTitleLength)
	}
}

func TestEffectFrameRejectsInvalidRecoveryID(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen+4)
	wire[64] = byte(EffectRecovery)
	binary.BigEndian.PutUint16(wire[69:71], 4)
	copy(wire[EffectFrameHeaderLen:], "bad!")
	if _, err := DecodeEffectFrame(wire); err != ErrInvalidRecoveryEffect {
		t.Fatalf("invalid recovery error = %v, want %v", err, ErrInvalidRecoveryEffect)
	}
}

func TestEffectFrameRejectsPromptBoundaryWithoutScreenRevision(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	wire[64] = byte(EffectPromptBoundary)
	if _, err := DecodeEffectFrame(wire); err != ErrInvalidScreenRevision {
		t.Fatalf("missing revision error = %v, want %v", err, ErrInvalidScreenRevision)
	}
}

func TestEffectFrameRejectsScreenRevisionOnOtherKinds(t *testing.T) {
	wire := make([]byte, EffectFrameHeaderLen)
	binary.BigEndian.PutUint64(wire[56:64], 1)
	wire[64] = byte(EffectBell)
	if _, err := DecodeEffectFrame(wire); err != ErrInvalidScreenRevision {
		t.Fatalf("unexpected revision error = %v, want %v", err, ErrInvalidScreenRevision)
	}
}

func TestEffectFrameDecodeRejectsStreamOffsetCombinedWithRecoveryEpisode(t *testing.T) {
	wire := EncodeEffectFrame(EffectFrame{
		Kind: EffectRecovery, EpisodeID: "rec-0123456789abcdef0123456789abcdef",
	})
	binary.BigEndian.PutUint64(wire[48:56], 17)
	if _, err := DecodeEffectFrame(wire); err != ErrInvalidStreamOffset {
		t.Fatalf("DecodeEffectFrame error = %v, want %v", err, ErrInvalidStreamOffset)
	}
}

func TestEffectFrameRejectsShortHeader(t *testing.T) {
	if _, err := DecodeEffectFrame(make([]byte, EffectFrameHeaderLen-1)); err != ErrEffectFrameTooShort {
		t.Fatalf("short frame error = %v, want %v", err, ErrEffectFrameTooShort)
	}
}
