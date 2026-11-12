package sessionruntime

import (
	"fmt"
	"strings"
	"testing"
)

// ROWS SCROLLED BY LEAVING THE ALTERNATE SCREEN ARE NOT LOST AFTER A COMMIT
// MADE WHILE IT WAS UP (nocx-2v80t.6).
//
// A commit while the alternate screen holds the pane leaves the primary's
// departure count unmeasured — its depth cannot be read from there — and the
// feed that leaves the alternate screen and prints more than a screen used to
// re-baseline over its own scroll: those rows were neither streamed nor
// counted as withheld. Measured before the fix: 21 of 60 rows stored. The
// same bytes with no commit store all 60, which is the pair.
func TestLeavingTheAlternateScreenAfterACommitKeepsEveryRowItScrolls(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "no commit while the alternate screen is up"
		if commit {
			name = "a commit while the alternate screen is up"
		}
		t.Run(name, func(t *testing.T) {
			s, rs := streamSession(t, harnessGeometry(150, 40))
			if err := s.Ingest([]byte("$ run-a-program\r\n" + outputMarkerFixed)); err != nil {
				t.Fatalf("ingest the echo and the output mark: %v", err)
			}
			if err := s.Ingest([]byte("\x1b[?1049h" + "full-screen program\r\n")); err != nil {
				t.Fatalf("enter the alternate screen: %v", err)
			}
			if commit {
				if _, err := s.CommitGeometry(harnessGeometry(80, 24)); err != nil {
					t.Fatalf("commit: %v", err)
				}
			}
			var want []string
			var out strings.Builder
			out.WriteString("\x1b[?1049l")
			for i := 1; i <= 60; i++ {
				m := fmt.Sprintf("OUT-%03d", i)
				want = append(want, m)
				out.WriteString(m + "\r\n")
			}
			if err := s.Ingest([]byte(out.String())); err != nil {
				t.Fatalf("leave the alternate screen and print in one feed: %v", err)
			}
			nonce := obsNonce(0x61)
			s.Completed(s.Incarnation(), nonce, 0)
			if err := s.Ingest([]byte(fenceFor(0x61))); err != nil {
				t.Fatalf("ingest the fence: %v", err)
			}
			var stored []string
			for _, e := range rs.snapshot() {
				if e.kind == "rows" {
					for _, r := range e.rows {
						stored = append(stored, streamRowText(r))
					}
				}
			}
			stored = append(stored, closingTexts(t, rs, nonce)...)
			if strings.Join(stored, "|") != strings.Join(want, "|") {
				t.Fatalf("the block stored %d rows, want exactly its %d output rows:\n%s", len(stored), len(want), strings.Join(stored, "\n"))
			}
		})
	}
}

// The other half of the same schedule: output the command printed BEFORE the
// alternate screen, on a full primary screen, is pushed into the history by
// a shrink while the alternate screen is up. Those rows left the screen too,
// and are the block's.
func TestAShrinkWhileTheAlternateScreenIsUpKeepsTheRowsItPushedOff(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(150, 40))
	var want []string
	var out strings.Builder
	out.WriteString("$ make-then-open-a-program\r\n" + outputMarkerFixed)
	for i := 1; i <= 50; i++ {
		m := fmt.Sprintf("OUT-%03d", i)
		want = append(want, m)
		out.WriteString(m + "\r\n")
	}
	if err := s.Ingest([]byte(out.String())); err != nil {
		t.Fatalf("ingest the command and its output: %v", err)
	}
	if err := s.Ingest([]byte("\x1b[?1049h" + "full-screen program\r\n")); err != nil {
		t.Fatalf("enter the alternate screen: %v", err)
	}
	if _, err := s.CommitGeometry(harnessGeometry(150, 24)); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := s.Ingest([]byte("\x1b[?1049l")); err != nil {
		t.Fatalf("leave the alternate screen: %v", err)
	}
	nonce := obsNonce(0x62)
	s.Completed(s.Incarnation(), nonce, 0)
	if err := s.Ingest([]byte(fenceFor(0x62))); err != nil {
		t.Fatalf("ingest the fence: %v", err)
	}
	var stored []string
	for _, e := range rs.snapshot() {
		if e.kind == "rows" {
			for _, r := range e.rows {
				stored = append(stored, streamRowText(r))
			}
		}
	}
	stored = append(stored, closingTexts(t, rs, nonce)...)
	if strings.Join(stored, "|") != strings.Join(want, "|") {
		t.Fatalf("the block stored %d rows, want exactly its %d output rows:\n%s", len(stored), len(want), strings.Join(stored, "\n"))
	}
}

// A window row that continues the one before it is consumed as ITSELF when it
// departs, so no entry outlives the row it named: output whose first row
// reads exactly like that continuation is output (codex's review of
// nocx-2v80t.6, finding 3).
func TestAContinuationTheWindowNamedLeavesNoEntryToSwallowOutput(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	line := "$ " + strings.Repeat("x", 78) + strings.Repeat("continuation-text ", 2)
	if err := s.Ingest([]byte(line + "\r\n" + outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the echo and the output mark: %v", err)
	}
	tail := strings.TrimRight(line[80:], " ")
	want := []string{tail}
	var out strings.Builder
	out.WriteString(tail + "\r\n")
	for i := 1; i <= 40; i++ {
		m := fmt.Sprintf("OUT-%03d", i)
		want = append(want, m)
		out.WriteString(m + "\r\n")
	}
	if err := s.Ingest([]byte(out.String())); err != nil {
		t.Fatalf("ingest the output: %v", err)
	}
	nonce := obsNonce(0x63)
	s.Completed(s.Incarnation(), nonce, 0)
	if err := s.Ingest([]byte(fenceFor(0x63))); err != nil {
		t.Fatalf("ingest the fence: %v", err)
	}
	got := storedRowsFor(t, rs, nonce)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("the block stored %d rows, want its %d output rows, the first reading like the echo's continuation:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
}

// A pane that GROWS while the alternate screen is up pulls rows the primary
// already reported back out of its history. When they leave again they are
// not the next command's: the next block holds only its own output (codex's
// review of nocx-2v80t.6, finding 4).
func TestAGrowthWhileTheAlternateScreenIsUpDoesNotReportRowsTwice(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	obsFeed(t, s, 0, 60)
	obsSeal(t, s, obsNonce(1))
	if err := s.Ingest([]byte("$ run-a-program\r\n" + outputMarkerFixed + "\x1b[?1049h" + "full-screen program\r\n")); err != nil {
		t.Fatalf("enter the alternate screen: %v", err)
	}
	if _, err := s.CommitGeometry(harnessGeometry(80, 40)); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var want []string
	var out strings.Builder
	out.WriteString("\x1b[?1049l")
	for i := 1; i <= 60; i++ {
		m := fmt.Sprintf("OUT-%03d", i)
		want = append(want, m)
		out.WriteString(m + "\r\n")
	}
	if err := s.Ingest([]byte(out.String())); err != nil {
		t.Fatalf("leave the alternate screen and print: %v", err)
	}
	nonce := obsNonce(0x64)
	s.Completed(s.Incarnation(), nonce, 0)
	if err := s.Ingest([]byte(fenceFor(0x64))); err != nil {
		t.Fatalf("ingest the fence: %v", err)
	}
	// The block before is every row streamed up to its end marker; this one
	// is what streamed after it plus its own closing screen.
	var mine []string
	after := false
	for _, e := range rs.snapshot() {
		switch {
		case e.kind == "end" && e.nonce == obsNonce(1):
			after = true
		case e.kind == "rows" && after:
			for _, r := range e.rows {
				mine = append(mine, streamRowText(r))
			}
		}
	}
	mine = append(mine, closingTexts(t, rs, nonce)...)
	if strings.Join(mine, "|") != strings.Join(want, "|") {
		t.Fatalf("the block stored %d rows, want exactly its %d output rows:\n%s", len(mine), len(want), strings.Join(mine, "\n"))
	}
}

// A full-screen command has no primary-screen output. A width commit made
// on the alternate screen must not turn the restored primary's earlier blocks
// into its closing rows, even when the fence beats the commit back (nocx-l3evv).
func TestAlternateScreenWidthCommitKeepsPriorBlocksOutOfClosingRows(t *testing.T) {
	for _, size := range []struct{ cols, rows int }{{100, 24}, {60, 24}, {80, 40}, {80, 20}} {
		for _, backBeforeFence := range []bool{false, true} {
			for _, fenceFirst := range []bool{false, true} {
				for _, priorLines := range []int{3, 30} {
					name := fmt.Sprintf("%dx%d/backBeforeFence=%v/fenceFirst=%v/prior=%d", size.cols, size.rows, backBeforeFence, fenceFirst, priorLines)
					t.Run(name, func(t *testing.T) {
						s, rs := streamSession(t, harnessGeometry(80, 24))
						ingest := func(text string) {
							t.Helper()
							if err := s.Ingest([]byte(text)); err != nil {
								t.Fatal(err)
							}
						}
						commit := func(cols, rows int) {
							t.Helper()
							if _, err := s.CommitGeometry(harnessGeometry(cols, rows)); err != nil {
								t.Fatal(err)
							}
						}
						seal := func(k byte) FenceNonce {
							t.Helper()
							nonce := obsNonce(k)
							if fenceFirst {
								ingest(fenceFor(k))
							}
							s.Completed(s.Incarnation(), nonce, 0)
							if !fenceFirst {
								ingest(fenceFor(k))
							}
							return nonce
						}
						var prior strings.Builder
						prior.WriteString("$ agent\r\n" + outputMarkerFixed)
						for i := 0; i < priorLines; i++ {
							fmt.Fprintf(&prior, "PRIOR-AGENT-%02d %s\r\n", i, strings.Repeat("=", 70))
						}
						ingest(prior.String())
						seal(1)
						ingest("\x1b[J$ ls\r\n" + outputMarkerFixed + "PRIOR-LS-1  PRIOR-LS-2\r\nPRIOR-LS-3\r\n")
						seal(2)
						for run := byte(3); run < 6; run++ {
							ingest("\x1b[J$ htop\r\n" + outputMarkerFixed + "\x1b[?1049h\x1b[H\x1b[2JTasks: 1 running\r\n")
							commit(size.cols, size.rows)
							ingest("\x1b[H\x1b[2JTasks: 1 running (redraw)\r\n")
							ingest("\x1b[?1049l\r\x1b[?1l\x1b>")
							if backBeforeFence {
								commit(80, 24)
							}
							nonce := seal(run)
							if !backBeforeFence {
								commit(80, 24)
							}
							if got := storedRowsFor(t, rs, nonce); len(got) != 0 {
								t.Errorf("full-screen run %d stored %d rows from the restored primary, want none:\n%s", run-2, len(got), strings.Join(got, "\n"))
							}
						}
					})
				}
			}
		}
	}
}

// Reflow must not bless output written over the mark's prefix as old content.
// The rewrite is visible on primary before the geometry commit, including
// when the commit itself is deferred until the alternate screen returns.
func TestGeometryCommitDoesNotRepairARewrittenOutputPrefix(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		for _, capture := range []string{"none", "before-rewrite", "after-rewrite"} {
			captured := capture != "none"
			name := fmt.Sprintf("alternate=%v/capture=%s", alternate, capture)
			t.Run(name, func(t *testing.T) {
				s, rs := streamSession(t, harnessGeometry(80, 24))
				if err := s.Ingest([]byte("prev-1\r\nprev-2\r\nprev-3\r\n$ cmd\r\n" + outputMarkerFixed)); err != nil {
					t.Fatal(err)
				}
				forged := obsNonce(0x79)
				if capture == "before-rewrite" {
					if err := s.SightFence(forged, []byte("unverified")); err != nil {
						t.Fatal(err)
					}
				}
				// No ED2: the pin survives and only the stands check detects
				// that the program rewrote the prefix in place.
				if err := s.Ingest([]byte("\x1b[2A\x1b[2KUP\r\n\x1b[2KOUT\r\n")); err != nil {
					t.Fatal(err)
				}
				if capture == "after-rewrite" {
					if err := s.SightFence(forged, []byte("unverified")); err != nil {
						t.Fatal(err)
					}
				}
				if alternate {
					if err := s.Ingest([]byte("\x1b[?1049h")); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.CommitGeometry(harnessGeometry(100, 24)); err != nil {
					t.Fatal(err)
				}
				if alternate {
					if err := s.Ingest([]byte("\x1b[?1049l")); err != nil {
						t.Fatal(err)
					}
				}
				if captured {
					if err := s.ExpireRendezvous(forged); err != nil {
						t.Fatal(err)
					}
				}
				nonce := obsNonce(0x78)
				s.Completed(s.Incarnation(), nonce, 0)
				if err := s.Ingest([]byte(fenceFor(0x78))); err != nil {
					t.Fatal(err)
				}
				if got := storedRowsFor(t, rs, nonce); strings.Join(got, "|") != "UP|OUT" {
					t.Fatalf("stored %q, want exactly UP, OUT: earlier rows were not rewritten", got)
				}
			})
		}
	}
}

// A cursor above rewritten output must not make the prefix's shorter visible
// suffix look unchanged and allow reflow to bless the rows below that cursor.
func TestGeometryCommitChecksThePrefixBelowTheCursor(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	if err := s.Ingest([]byte("$ cmd\r\n$ cmd\r\n$ cmd\r\n" + outputMarkerFixed)); err != nil {
		t.Fatal(err)
	}
	if err := s.Ingest([]byte("\x1b[1A\x1b[2KUP\x1b[1A")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitGeometry(harnessGeometry(100, 24)); err != nil {
		t.Fatal(err)
	}
	if err := s.Ingest([]byte("\x1b[2B")); err != nil {
		t.Fatal(err)
	}
	nonce := obsNonce(0x7a)
	s.Completed(s.Incarnation(), nonce, 0)
	if err := s.Ingest([]byte(fenceFor(0x7a))); err != nil {
		t.Fatal(err)
	}
	if got := storedRowsFor(t, rs, nonce); strings.Join(got, "|") != "UP" {
		t.Fatalf("stored %q, want exactly UP: output below the parked cursor was rewritten", got)
	}
}
