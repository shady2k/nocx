package lifecyclecodec

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/shady2k/nocx/internal/lifecycle"
)

// ErrInvalidFrame reports an empty raw frame. Raw operations do not scan or
// recover: their caller owns an already established frame boundary.
var ErrInvalidFrame = errors.New("lifecyclecodec: empty frame")

// ReadRawFrame reads exactly one frame, without buffering any following bytes.
// The size is checked before allocating its body. Transport deadlines and
// cancellation belong to r.
func ReadRawFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 {
		return nil, ErrInvalidFrame
	}
	if n > lifecycle.MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	body := make([]byte, int(n))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// WriteRawFrame writes a complete frame, initially in one write so another
// lifecycle writer cannot land between its header and body. Short writes resume
// at the unwritten suffix; callers must serialize any concurrent writers.
// The returned count includes the four-byte header.
func WriteRawFrame(w io.Writer, body []byte) (int, error) {
	if len(body) == 0 {
		return 0, ErrInvalidFrame
	}
	if len(body) > lifecycle.MaxFrameBytes {
		return 0, ErrFrameTooLarge
	}
	frame := make([]byte, 4+len(body))
	// #nosec G115 -- checked against MaxFrameBytes above.
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	written := 0
	for written < len(frame) {
		n, err := w.Write(frame[written:])
		if n < 0 || n > len(frame)-written {
			return written, io.ErrShortWrite
		}
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}
