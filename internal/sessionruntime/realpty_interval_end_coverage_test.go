package sessionruntime

import (
	"fmt"
	"strings"
	"testing"
)

// THE COVERAGE INVARIANT AT THE HELPER SEAM (nocx-rb4ca).
//
// The e2e's durable-rows assertion (e2e/transcript-scroll-budget.spec.ts, the
// zero-retention case) fails when a row of a command's output ends up in NO
// interval: not streamed below an end marker, and not on the closing screen
// that marker carries. The interval's own arithmetic is stated on the port
// (rowstream.go): a row belongs to the interval whose end marker follows it,
// and the marker's endRow is the absolute index one past that interval's last
// departed row, with the screen the boundary sat on appended as the closing
// rows. So the invariant every consumer of the stream relies on is:
//
//	endRow + len(closing) >= the number of rows the command printed
//
// at the marker that ends the command's own interval — and no row may sit
// below an end marker and above the next one's coverage with the two
// overlapping.
//
// These tests drive the REAL path: a real shell on a real PTY writing the
// fence sequence internal/shellintegration/scripts/nocx.bash writes
// (__nocx_lc_send complete is sent BEFORE the fence reaches the pty, so both
// arrival orders are ordinary), with the real emulator behind the port. The
// chunk shapes the second test feeds are the shapes a 4 KiB pty read makes
// (internal/helper/session/owner.go's pageSize): the marker inside a chunk
// with rows after it, a chunk that ends exactly at the marker, and a read
// boundary inside a multi-byte grapheme of the command's own output.

// intervalCoverage is what the stream says the intervals cover, read off the
// recorded emissions: the total the last end marker accounts for, and whether
// the row batches are contiguous (a hole would leave an index no batch and no
// closing screen names).
type intervalCoverage struct {
	ends      int
	covered   uint64
	gaps      []string
	lastNoFen bool
}

// coverageOf walks one recorded stream and answers what its end markers
// account for. A batch whose FromRow is not exactly where the stream stood —
// allowing for a counted loss, which spends the indices it names — is a gap.
func coverageOf(t *testing.T, rs *recordingRowStream) intervalCoverage {
	t.Helper()
	var out intervalCoverage
	stood := uint64(0)
	for _, e := range rs.snapshot() {
		switch e.kind {
		case "rows":
			if e.from != stood+e.lost {
				out.gaps = append(out.gaps, fmt.Sprintf(
					"a batch starts at %d where the stream stood at %d", e.from, stood))
			}
			stood = e.from + uint64(len(e.rows))
		case "end":
			out.ends++
			out.covered = e.endRow + uint64(len(e.closing))
			out.lastNoFen = e.noFence
			if e.endRow > stood {
				out.gaps = append(out.gaps, fmt.Sprintf(
					"an end marker names row %d but the stream had reached %d", e.endRow, stood))
			}
		}
	}
	return out
}

// numberRowsProgram is the e2e's own command shape on a real pty: rows a
// screenful past the pane, then the render fence the shell writes when the
// command finishes, then a sentinel the test waits on.
func numberRowsProgram(rows int) string {
	return rawPreamble + fmt.Sprintf(`
i=1
while [ $i -le %d ]; do printf 'row-%%04d\n' "$i"; i=$((i+1)); done
printf '\033]1337;NOCX_FENCE;%s\007'
printf 'COMMAND-DONE\n'
`, rows, fenceNonceHex)
}

// TestAnIntervalEndCoversEveryRowTheCommandPrinted drives the whole path on a
// real PTY, in BOTH orders the two carriers can arrive in — which is not a
// race but the ordinary shape twice over: nocx.bash sends the authenticated
// completion BEFORE it writes the fence to the pty
// (internal/shellintegration/scripts/nocx.bash), so the completion lands
// first whenever the pty read loop is behind the shell, and the fence's
// sighting lands first when it is not.
func TestAnIntervalEndCoversEveryRowTheCommandPrinted(t *testing.T) {
	const rows = 5000
	nonce := fenceNonceFromString(t, fenceNonceHex)
	for _, order := range []string{"sighting-first", "completion-first"} {
		t.Run(order, func(t *testing.T) {
			rs := &recordingRowStream{}
			// The e2e's pane shape: 27 rows tall, so the last 27 rows are
			// still on the screen when the fence sits on them.
			p := startProgramRows(t, numberRowsProgram(rows), harnessGeometry(100, 27), rs)
			p.s.SetRowStream(rs)
			if err := p.s.ApplyScrollback(0); err != nil {
				t.Fatalf("apply the zero scrollback budget: %v", err)
			}
			if order == "completion-first" {
				// The completion can arrive while the command's own output is
				// still in flight: the shell sends it before it writes the
				// fence, and the pty read loop is a separate path.
				p.s.Completed(p.s.Incarnation(), nonce, 0)
			}
			p.wait("COMMAND-DONE")
			p.s.Completed(p.s.Incarnation(), nonce, 0)

			got := coverageOf(t, rs)
			if len(got.gaps) > 0 {
				t.Errorf("the row stream has holes: %v", got.gaps)
			}
			if got.ends == 0 {
				t.Fatal("the command's interval never ended: no end marker was emitted")
			}
			if got.covered < rows {
				t.Errorf("the interval end accounts for %d rows of the %d the command printed: "+
					"rows %d..%d sit below every end marker and outside every closing screen",
					got.covered, rows, got.covered+1, rows)
			}
			if got.lastNoFen {
				t.Errorf("the interval settled without its fence: the block this marker closes is " +
					"declared incomplete even though the fence reached the pty")
			}
		})
	}
}

// TestAnIntervalEndCoversEveryRowWhateverSplitsTheReads feeds the command's
// own bytes in the chunk shapes a pty read makes, with the fence at every
// possible split point: inside a chunk with rows after it, exactly at a
// chunk's end, and with the read boundary inside a multi-byte grapheme of the
// output. The end marker must still carry the row the command ended at.
func TestAnIntervalEndCoversEveryRowWhateverSplitsTheReads(t *testing.T) {
	const rows = 200
	var body strings.Builder
	for i := range rows {
		fmt.Fprintf(&body, "row-%04d \u6f22\U0001F642 tail\r\n", i)
	}
	text := body.String()
	// The shape nocx.bash writes: the fence, then the next prompt's own bytes
	// in the same read.
	fence := fenceSeq(fenceNonceHex) + "\x1b]133;D\x07\x1b]133;A\x07$ "
	full := text + fence

	splits := []struct {
		name string
		at   []int
	}{
		{"the fence alone in its own read", []int{len(text), len(text) + len(fence)}},
		{"the fence with the prompt's bytes behind it", []int{len(text)}},
		{"a chunk ending exactly at the marker", []int{len(text) + len(fenceSeq(fenceNonceHex))}},
		{"a read boundary inside a multi-byte grapheme", []int{len(text)/3 + 2}},
		{"every boundary of the fence", nil},
	}
	for _, shape := range splits {
		t.Run(shape.name, func(t *testing.T) {
			cuts := shape.at
			if cuts == nil {
				for cut := 1; cut <= len(fence); cut++ {
					cuts = append(cuts, len(text)+cut)
				}
			}
			for _, cut := range cuts {
				rs := &recordingRowStream{}
				s := obsSession(t, harnessGeometry(100, 27))
				s.SetRowStream(rs)
				if err := s.ApplyScrollback(0); err != nil {
					t.Fatalf("apply the zero scrollback budget: %v", err)
				}
				for off := 0; off < len(full); {
					end := min(off+4096, len(full))
					if off < cut && cut < end {
						end = cut
					}
					if err := s.Ingest([]byte(full[off:end])); err != nil {
						t.Fatalf("ingest: %v", err)
					}
					off = end
				}
				s.Completed(s.Incarnation(), fenceNonceFromString(t, fenceNonceHex), 0)
				got := coverageOf(t, rs)
				if len(got.gaps) > 0 {
					t.Fatalf("cut at %d: the row stream has holes: %v", cut, got.gaps)
				}
				if got.covered < rows {
					t.Fatalf("cut at %d: the interval end accounts for %d rows of the %d the command "+
						"printed: rows %d..%d sit below every end marker and outside every closing screen",
						cut, got.covered, rows, got.covered+1, rows)
				}
			}
		})
	}
}
