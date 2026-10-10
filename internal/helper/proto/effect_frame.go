package proto

import (
	"encoding/binary"
	"errors"
)

// EffectFrame carries one non-visual runtime effect to one subscriber. It is
// separate from ScreenDataFrame: a screen snapshot never owns side effects.
// Layout: session[16], subscriber[16], generation[8], effect-id[8],
// stream-offset[8], kind[1], title-length[4], episode-id-length[2], title,
// episode-id, body. The helper protocol version fences this shape.
const EffectFrameHeaderLen = 63

var (
	ErrEffectFrameTooShort       = errors.New("proto: effect frame shorter than its header")
	ErrEffectFrameTitleLength    = errors.New("proto: effect frame title length exceeds payload")
	ErrEffectFrameTitleTooLong   = errors.New("proto: effect frame title exceeds wire length")
	ErrEffectFrameEpisodeTooLong = errors.New("proto: effect frame episode id exceeds wire length")
	ErrUnknownEffectKind         = errors.New("proto: unknown effect kind")
	ErrInvalidRecoveryEffect     = errors.New("proto: invalid recovery effect")
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
	// StreamOffset is the exclusive raw PTY byte position for the effect. It
	// is non-zero for prompt boundaries and shares SessionFrame's cumulative
	// output-window coordinate; other effect kinds leave it zero.
	StreamOffset uint64
	Kind         EffectKind
	Title        []byte
	Body         []byte
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
	if (f.Kind == EffectRecovery && (!validEpisodeID(f.EpisodeID) || len(f.Title) != 0 || len(f.Body) != 0)) || (f.Kind != EffectRecovery && f.EpisodeID != "") {
		panic(ErrInvalidRecoveryEffect)
	}
	b := make([]byte, EffectFrameHeaderLen+len(f.Title)+len(f.EpisodeID)+len(f.Body))
	copy(b[:16], f.Session[:])
	copy(b[16:32], f.Subscriber[:])
	binary.BigEndian.PutUint64(b[32:40], f.Generation)
	binary.BigEndian.PutUint64(b[40:48], f.EffectID)
	binary.BigEndian.PutUint64(b[48:56], f.StreamOffset)
	b[56] = byte(f.Kind)
	titleLen := len(f.Title)
	b[57] = byte((titleLen >> 24) & 0xff)
	b[58] = byte((titleLen >> 16) & 0xff)
	b[59] = byte((titleLen >> 8) & 0xff)
	b[60] = byte(titleLen & 0xff)
	episodeLen := len(f.EpisodeID)
	b[61] = byte((episodeLen >> 8) & 0xff)
	b[62] = byte(episodeLen & 0xff)
	copy(b[EffectFrameHeaderLen:], f.Title)
	copy(b[EffectFrameHeaderLen+len(f.Title):], f.EpisodeID)
	copy(b[EffectFrameHeaderLen+len(f.Title)+len(f.EpisodeID):], f.Body)
	return b
}

func DecodeEffectFrame(b []byte) (EffectFrame, error) {
	if len(b) < EffectFrameHeaderLen {
		return EffectFrame{}, ErrEffectFrameTooShort
	}
	k := EffectKind(b[56])
	if !k.valid() {
		return EffectFrame{}, ErrUnknownEffectKind
	}
	var f EffectFrame
	copy(f.Session[:], b[:16])
	copy(f.Subscriber[:], b[16:32])
	f.Generation = binary.BigEndian.Uint64(b[32:40])
	f.EffectID = binary.BigEndian.Uint64(b[40:48])
	f.StreamOffset = binary.BigEndian.Uint64(b[48:56])
	f.Kind = k
	titleLen := binary.BigEndian.Uint32(b[57:61])
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
	episodeSize := int(binary.BigEndian.Uint16(b[61:63]))
	if episodeSize > payloadLen-titleSize {
		return EffectFrame{}, ErrEffectFrameTitleLength
	}
	titleEnd := EffectFrameHeaderLen + titleSize
	episodeEnd := titleEnd + episodeSize
	f.Title = append([]byte(nil), b[EffectFrameHeaderLen:titleEnd]...)
	f.EpisodeID = string(b[titleEnd:episodeEnd])
	f.Body = append([]byte(nil), b[episodeEnd:]...)
	if (f.Kind == EffectRecovery && (!validEpisodeID(f.EpisodeID) || len(f.Title) != 0 || len(f.Body) != 0)) || (f.Kind != EffectRecovery && f.EpisodeID != "") {
		return EffectFrame{}, ErrInvalidRecoveryEffect
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
