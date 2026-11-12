package content

import (
	"context"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// BenchmarkBlockRowsRetention measures small active deliveries on either side
// of the default cap. Use -benchtime=1000x: the fresh case stays below the cap,
// while the prefilled case exercises eviction on every append.
func BenchmarkBlockRowsRetention(b *testing.B) {
	for _, initialRows := range []int{0, 3000} {
		b.Run(fmt.Sprintf("initialRows=%d", initialRows), func(b *testing.B) {
			s, entry := newBlockRowsEvictionStore(b)
			ctx := context.Background()
			row := emulator.Row{Cells: make([]emulator.Cell, 80)}
			for i := range row.Cells {
				row.Cells[i] = emulator.Cell{Grapheme: "x", Width: emulator.WidthNarrow, HasText: true}
			}
			in := AppendBlockRows{EntryID: entry, ArtifactID: evictionArtifact, Rows: []emulator.Row{row}}
			for range initialRows {
				if err := s.AppendBlockRows(ctx, in); err != nil {
					b.Fatal(err)
				}
				in.FromRow++
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := s.AppendBlockRows(ctx, in); err != nil {
					b.Fatal(err)
				}
				in.FromRow++
			}
		})
	}
}
