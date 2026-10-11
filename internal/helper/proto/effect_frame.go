package proto

import (
	"encoding/binary"
	"errors"
)

// EffectFrame carries one non-visual runtime effect to one subscriber. It is
// separate from ScreenDataFrame: a screen snapshot never owns side effects.
// Layout: session[16], subscriber[16], generation[8], effect-id[8],
// stream-offset[8], screen-revision[8], kind[1], title-length[4],
// episode-id-length[2], title, episode-id, body. The helper protocol version
// fences this shape.
const EffectFrameHeaderLen = 71

var (
	ErrEffectFrameTooShort       = errors.New("proto: effect frame shorter than its header")
	ErrEffectFrameTitleLength    = errors.New("proto: effect frame title length exceeds payload")
	ErrEffectFrameTitleTooLong   = errors.New("proto: effect frame title exceeds wire length")
	ErrEffectFrameEpisodeTooLong = errors.New("proto: effect frame episode id exceeds wire length")
	ErrUnknownEffectKind         = errors.New("proto: unknown effect kind")
	ErrInvalidRecoveryEffect     = errors.New("proto: invalid recovery effect")
	ErrInvalidScreenRevision     = errors.New("proto: invalid prompt-boundary screen revision")
	ErrInvalidStreamOffset       = errors.New("proto: stream offset is invalid for effect kind")
)

// EffectKind is the closed non-visual effect vocabulary shared with
// internal/sessionruntime.EffectKind. Values intentionally match that enum.
type EffectKind uint8

const (
	EffectBell EffectKind = iota + 1
	EffectNotification
	EffectClipboard
	EffectTitle
	EffectCwdReport
	EffectPromptBoundary
	EffectRecovery
)

func (k EffectKind) valid() bool { return k >= EffectBell && k <= EffectRecovery }

type EffectFrame struct {
	Session    [16]byte
	Subscriber [16]byte
	Generation uint64
	EffectID   uint64
	// StreamOffset is the exclusive raw PTY byte position for a prompt
	// boundary and shares SessionFrame's cumulative output-window coordinate;
	// other effect kinds leave it zero.
	StreamOffset uint64
	// ScreenRevision is present only for prompt boundaries. It names the
	// full-frame lower bound that the coordinator must offer before delivering
	// the corresponding effect; it is neither an acknowledgement nor authority.
	ScreenRevision uint64
	Kind           EffectKind
	Title          []byte
	Body           []byte
	// EpisodeID is present only for EffectRecovery and is non-secret.
	EpisodeID string
}

func EncodeEffectFrame(f EffectFrame) []byte {
	if len(f.Title) > 1<<31-1 {
		panic(ErrEffectFrameTitleTooLong)
	}
	if len(f.EpisodeID) > 1<<16-1 {
		panic(ErrEffectFrameEpisodeTooLong)
	}
	if !f.Kind.valid() {
		panic(ErrUnknownEffectKind)
	}
	if (f.Kind == EffectPromptBoundary && f.ScreenRevision == 0) || (f.Kind != EffectPromptBoundary && f.ScreenRevision != 0) {
		panic(ErrInvalidScreenRevision)
	}
	if f.Kind != EffectPromptBoundary && f.StreamOffset != 0 {
		panic(ErrInvalidStreamOffset)
	}
	if (f.Kind == EffectRecovery && (!validEpisodeID(f.EpisodeID) || len(f.Title) != 0 || len(f.Body) != 0)) || (f.Kind != EffectRecovery && f.EpisodeID != "") {
		panic(ErrInvalidRecoveryEffect)
	}
	b := make([]byte, EffectFrameHeaderLen+len(f.Title)+len(f.EpisodeID)+len(f.Body))
	copy(b[:16], f.Session[:])
	copy(b[16:32], f.Subscriber[:])
	binary.BigEndian.PutUint64(b[32:40], f.Generation)
	binary.BigEndian.PutUint64(b[40:48], f.EffectID)
	binary.BigEndian.PutUint64(b[48:56], f.StreamOffset)
	binary.BigEndian.PutUint64(b[56:64], f.ScreenRevision)
	b[64] = byte(f.Kind)
	titleLen := len(f.Title)
	b[65] = byte((titleLen >> 24) & 0xff)
	b[66] = byte((titleLen >> 16) & 0xff)
	b[67] = byte((titleLen >> 8) & 0xff)
	b[68] = byte(titleLen & 0xff)
	episodeLen := len(f.EpisodeID)
	b[69] = byte((episodeLen >> 8) & 0xff)
	b[70] = byte(episodeLen & 0xff)
	copy(b[EffectFrameHeaderLen:], f.Title)
	copy(b[EffectFrameHeaderLen+len(f.Title):], f.EpisodeID)
	copy(b[EffectFrameHeaderLen+len(f.Title)+len(f.EpisodeID):], f.Body)
	return b
}

func DecodeEffectFrame(b []byte) (EffectFrame, error) {
	if len(b) < EffectFrameHeaderLen {
		return EffectFrame{}, ErrEffectFrameTooShort
	}
	k := EffectKind(b[64])
	if !k.valid() {
		return EffectFrame{}, ErrUnknownEffectKind
	}
	var f EffectFrame
	copy(f.Session[:], b[:16])
	copy(f.Subscriber[:], b[16:32])
	f.Generation = binary.BigEndian.Uint64(b[32:40])
	f.EffectID = binary.BigEndian.Uint64(b[40:48])
	f.StreamOffset = binary.BigEndian.Uint64(b[48:56])
	f.ScreenRevision = binary.BigEndian.Uint64(b[56:64])
	f.Kind = k
	titleLen := binary.BigEndian.Uint32(b[65:69])
	// int is at least 32 bits in Go. This guard makes the conversion safe on
	// 32-bit hosts too, and the payload bound rejects truncated titles.
	if titleLen > 1<<31-1 {
		return EffectFrame{}, ErrEffectFrameTitleLength
	}
	titleSize := int(titleLen)
	payloadLen := len(b) - EffectFrameHeaderLen
	if titleSize > payloadLen {
		return EffectFrame{}, ErrEffectFrameTitleLength
	}
	episodeSize := int(binary.BigEndian.Uint16(b[69:71]))
	if episodeSize > payloadLen-titleSize {
		return EffectFrame{}, ErrEffectFrameTitleLength
	}
	titleEnd := EffectFrameHeaderLen + titleSize
	episodeEnd := titleEnd + episodeSize
	f.Title = append([]byte(nil), b[EffectFrameHeaderLen:titleEnd]...)
	f.EpisodeID = string(b[titleEnd:episodeEnd])
	f.Body = append([]byte(nil), b[episodeEnd:]...)
	if f.Kind != EffectPromptBoundary && f.StreamOffset != 0 {
		return EffectFrame{}, ErrInvalidStreamOffset
	}
	if (f.Kind == EffectRecovery && (!validEpisodeID(f.EpisodeID) || len(f.Title) != 0 || len(f.Body) != 0)) || (f.Kind != EffectRecovery && f.EpisodeID != "") {
		return EffectFrame{}, ErrInvalidRecoveryEffect
	}
	if (f.Kind == EffectPromptBoundary && f.ScreenRevision == 0) || (f.Kind != EffectPromptBoundary && f.ScreenRevision != 0) {
		return EffectFrame{}, ErrInvalidScreenRevision
	}
	return f, nil
}

func validEpisodeID(id string) bool {
	if len(id) != 36 || id[:4] != "rec-" {
		return false
	}
	for _, c := range id[4:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
