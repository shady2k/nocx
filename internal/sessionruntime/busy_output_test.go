package sessionruntime

import (
	"strings"
	"testing"
)

// BenchmarkBusyOutput measures the active-output runtime at 120 columns by 40 rows
// and 800 bytes per ingest, without PTY or socket work.
func BenchmarkBusyOutput(b *testing.B) {
	s, _, stop, err := newIngestWindowSession()
	if err != nil {
		b.Fatal(err)
	}
	defer stop()
	payload := []byte(strings.Repeat("x", 78) + "\r\n")
	payload = []byte(strings.Repeat(string(payload), 10))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Ingest(payload); err != nil {
			b.Fatal(err)
		}
	}
}
