package lifecyclecodec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
)

func TestRawFramesDoNotReadAhead(t *testing.T) {
	var wire bytes.Buffer
	for _, body := range []string{`{"first":1}`, `{"second":2}`} {
		if _, err := WriteRawFrame(&wire, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := ReadRawFrame(&wire)
	if err != nil || string(first) != `{"first":1}` {
		t.Fatal("first frame changed")
	}
	if wire.Len() != 4+len(`{"second":2}`) {
		t.Fatal("first read consumed the following frame")
	}
	second, err := ReadRawFrame(&wire)
	if err != nil || string(second) != `{"second":2}` {
		t.Fatal("second frame changed")
	}
}

type rawShortWriter struct {
	out       bytes.Buffer
	limit     int
	failAfter int
}

func (w *rawShortWriter) Write(p []byte) (int, error) {
	if w.failAfter > 0 && w.out.Len() >= w.failAfter {
		return 0, io.ErrClosedPipe
	}
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	return w.out.Write(p)
}

func TestRawWriterResumesShortWritesWithoutReplay(t *testing.T) {
	body := []byte(`{"exact":"魚"}`)
	for _, limit := range []int{1, 2, 3, 4, 5, 100} {
		w := &rawShortWriter{limit: limit}
		n, err := WriteRawFrame(w, body)
		if err != nil || n != len(body)+4 {
			t.Fatalf("short write = %d, %v", n, err)
		}
		got, err := ReadRawFrame(&w.out)
		if err != nil || !bytes.Equal(got, body) || w.out.Len() != 0 {
			t.Fatal("short write replayed or skipped bytes")
		}
	}
	w := &rawShortWriter{limit: 2, failAfter: 6}
	if n, err := WriteRawFrame(w, body); n != 6 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("partial failure = %d, %v", n, err)
	}
	if !bytes.Equal(w.out.Bytes()[4:], body[:2]) {
		t.Fatal("partial failure replayed data")
	}
}

func TestRawFramingBoundsAndTruncation(t *testing.T) {
	var huge [4]byte
	binary.BigEndian.PutUint32(huge[:], lifecycle.MaxFrameBytes+1)
	for _, tc := range []struct {
		wire []byte
		want error
	}{
		{nil, io.EOF},
		{[]byte{0}, io.ErrUnexpectedEOF},
		{[]byte{0, 0, 0, 0}, ErrInvalidFrame},
		{huge[:], ErrFrameTooLarge},
		{[]byte{0, 0, 0, 2, '{'}, io.ErrUnexpectedEOF},
	} {
		if _, err := ReadRawFrame(bytes.NewReader(tc.wire)); !errors.Is(err, tc.want) {
			t.Fatalf("read = %v, want %v", err, tc.want)
		}
	}
	for _, body := range [][]byte{nil, make([]byte, lifecycle.MaxFrameBytes+1)} {
		var out bytes.Buffer
		if _, err := WriteRawFrame(&out, body); err == nil || out.Len() != 0 {
			t.Fatal("invalid body wrote a prefix")
		}
	}
	body := bytes.Repeat([]byte{'x'}, lifecycle.MaxFrameBytes)
	var out bytes.Buffer
	if _, err := WriteRawFrame(&out, body); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRawFrame(&out); err != nil || !bytes.Equal(got, body) {
		t.Fatal("exact maximum rejected")
	}
}

func TestRawWriterZeroProgressIsAnError(t *testing.T) {
	if n, err := WriteRawFrame(&rawShortWriter{}, []byte("{}")); n != 0 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero progress = %d, %v", n, err)
	}
}
