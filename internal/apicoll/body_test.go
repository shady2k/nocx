package apicoll

import "testing"

func TestBodyTransmitsTextCoversEveryBodyKind(t *testing.T) {
	for _, tc := range []struct {
		kind      string
		transmits bool
	}{
		{kind: BodyNone, transmits: false},
		{kind: BodyRaw, transmits: true},
		{kind: BodyJSON, transmits: true},
		{kind: BodyForm, transmits: true},
		{kind: BodyFile, transmits: false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if got := (Body{Kind: tc.kind, Text: "payload"}).TransmitsText(); got != tc.transmits {
				t.Fatalf("TransmitsText() = %t, want %t", got, tc.transmits)
			}
		})
	}
}
