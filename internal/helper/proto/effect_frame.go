package proto

import (
	"encoding/binary"
	"errors"
)

// EffectFrame carries one non-visual runtime effect to one subscriber. It is
// separate from ScreenDataFrame: a screen snapshot never owns side effects.
// Layout: session[16], subscriber[16], generation[8], effect-id[8], kind[1], body.
const EffectFrameHeaderLen = 49

var (
	ErrEffectFrameTooShort = errors.New("proto: effect frame shorter than its header")
	ErrUnknownEffectKind   = errors.New("proto: unknown effect kind")
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
)

func (k EffectKind) valid() bool { return k >= EffectBell && k <= EffectCwdReport }

type EffectFrame struct {
	Session    [16]byte
	Subscriber [16]byte
	Generation uint64
	EffectID   uint64
	Kind       EffectKind
	Body       []byte
}

func EncodeEffectFrame(f EffectFrame) []byte {
	if !f.Kind.valid() {
		panic(ErrUnknownEffectKind)
	}
	b := make([]byte, EffectFrameHeaderLen+len(f.Body))
	copy(b[:16], f.Session[:])
	copy(b[16:32], f.Subscriber[:])
	binary.BigEndian.PutUint64(b[32:40], f.Generation)
	binary.BigEndian.PutUint64(b[40:48], f.EffectID)
	b[48] = byte(f.Kind)
	copy(b[EffectFrameHeaderLen:], f.Body)
	return b
}

func DecodeEffectFrame(b []byte) (EffectFrame, error) {
	if len(b) < EffectFrameHeaderLen {
		return EffectFrame{}, ErrEffectFrameTooShort
	}
	k := EffectKind(b[48])
	if !k.valid() {
		return EffectFrame{}, ErrUnknownEffectKind
	}
	var f EffectFrame
	copy(f.Session[:], b[:16])
	copy(f.Subscriber[:], b[16:32])
	f.Generation = binary.BigEndian.Uint64(b[32:40])
	f.EffectID = binary.BigEndian.Uint64(b[40:48])
	f.Kind = k
	f.Body = append([]byte(nil), b[EffectFrameHeaderLen:]...)
	return f, nil
}
