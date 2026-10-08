package proto

import (
	"encoding/binary"
	"errors"
)

// EffectFrame carries one non-visual runtime effect to one subscriber. It is
// separate from ScreenDataFrame: a screen snapshot never owns side effects.
// Layout: session[16], subscriber[16], generation[8], effect-id[8],
// stream-offset[8], kind[1], title-length[4], title, body. The helper
// protocol version fences this shape.
const EffectFrameHeaderLen = 61

var (
	ErrEffectFrameTooShort     = errors.New("proto: effect frame shorter than its header")
	ErrEffectFrameTitleLength  = errors.New("proto: effect frame title length exceeds payload")
	ErrEffectFrameTitleTooLong = errors.New("proto: effect frame title exceeds wire length")
	ErrUnknownEffectKind       = errors.New("proto: unknown effect kind")
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
)

func (k EffectKind) valid() bool { return k >= EffectBell && k <= EffectPromptBoundary }

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
}

func EncodeEffectFrame(f EffectFrame) []byte {
	if len(f.Title) > 1<<31-1 {
		panic(ErrEffectFrameTitleTooLong)
	}
	if !f.Kind.valid() {
		panic(ErrUnknownEffectKind)
	}
	b := make([]byte, EffectFrameHeaderLen+len(f.Title)+len(f.Body))
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
	copy(b[EffectFrameHeaderLen:], f.Title)
	copy(b[EffectFrameHeaderLen+len(f.Title):], f.Body)
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
	if titleSize > len(b)-EffectFrameHeaderLen {
		return EffectFrame{}, ErrEffectFrameTitleLength
	}
	titleEnd := EffectFrameHeaderLen + titleSize
	f.Title = append([]byte(nil), b[EffectFrameHeaderLen:titleEnd]...)
	f.Body = append([]byte(nil), b[titleEnd:]...)
	return f, nil
}
