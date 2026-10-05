package proto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestOutputStartRowFrameRoundTrip(t *testing.T) {
	f := OutputStartRowFrame{FromRow: 0x0102030405060708}
	f.Session[0], f.Session[15] = 1, 2
	f.Subscriber[0], f.Subscriber[15] = 3, 4
	gotBytes, err := EncodeOutputStartRowFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotBytes) != OutputStartRowFrameHeaderLen || !bytes.Equal(gotBytes[32:], []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("encoded = %x", gotBytes)
	}
	got, err := DecodeOutputStartRowFrame(gotBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got != f {
		t.Fatalf("decoded = %+v, want %+v", got, f)
	}
}

func TestOutputStartRowFrameRefusesNonExactSize(t *testing.T) {
	for _, payload := range [][]byte{make([]byte, OutputStartRowFrameHeaderLen-1), make([]byte, OutputStartRowFrameHeaderLen+1)} {
		if _, err := DecodeOutputStartRowFrame(payload); !errors.Is(err, ErrOutputStartRowFrameSize) {
			t.Fatalf("decode len %d error = %v", len(payload), err)
		}
	}
	b, _ := EncodeOutputStartRowFrame(OutputStartRowFrame{FromRow: 9})
	if got := binary.BigEndian.Uint64(b[32:40]); got != 9 {
		t.Fatalf("row index = %d", got)
	}
}
