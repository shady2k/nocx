package turnbind

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestTurnFrameRoundTripLeavesRequestUntouched(t *testing.T) {
	token := strings.Repeat("ab", 32)
	encoded, err := EncodeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != TokenMagic+token+"\n" {
		t.Fatalf("frame=%q", encoded)
	}
	reader := bytes.NewBuffer(append(encoded, []byte("request")...))
	got, err := ReadToken(reader)
	if err != nil || got != token {
		t.Fatalf("token=%q err=%v", got, err)
	}
	remaining, _ := io.ReadAll(reader)
	if string(remaining) != "request" {
		t.Fatalf("remaining=%q", remaining)
	}
}

func TestTurnFrameRejectsRawPaneAndPartialFrames(t *testing.T) {
	for _, wire := range []string{strings.Repeat("ab", 32) + "\n", TokenMagic + "abcd"} {
		if _, err := ReadToken(strings.NewReader(wire)); err == nil {
			t.Fatalf("accepted %q", wire)
		}
	}
}
