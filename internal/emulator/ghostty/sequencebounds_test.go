package ghostty

import (
	"bytes"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// The library's own bound on an unterminated sequence (nocx-ygxjv.11).
//
// The session runtime's hostile-sequence schedules assert what a RUNTIME owes
// for an OSC or a DCS that never terminates: the part it keeps ITSELF is within
// MaxPendingSequence, its work is linear in the bytes it examined, and whatever
// it did not hand on is accounted for — held within that bound, or dropped with
// the loss counted and completeness reporting lost-ingest
// (internal/sessionruntime/contract_test.go, section 17).
//
// What those schedules cannot assert is what the EMULATOR holds, and that is
// ADR-0065's split rather than a gap: with the parser inside this library an
// unterminated sequence belongs to the LIBRARY, and the runtime's port carries
// no reading of it. So the library's half is measured where the emulator is
// chosen — here — because "the emulator's bound is the emulator's" is only a
// useful sentence if somebody knows the number. The pin these numbers belong to
// is doc.go's: ghostty e2e53f861482e080bf45054ba49ef471f9849937 (2026-09-11),
// Zig 0.16.0, ReleaseFast.
//
// # What is measured, and how
//
// The process's resident set is read from /proc/self/statm, the sequence is fed
// in 64 KiB chunks, and a reading is taken after each of three lengths: 1 MiB,
// 16 MiB and 64 MiB. Two numbers come out of those readings for each shape of
// sequence, and both are asserted — because a single "it is small" reading
// cannot tell a bound apart from a 64 MiB limit:
//
//   - the GROWTH from 1 MiB to 64 MiB, a ceiling on what the library retains
//     however long the sequence is;
//   - the MOVE from 16 MiB to 64 MiB, 48 MiB of further input in which an
//     unbounded buffer would have to grow by 48 MiB. This is the reading that
//     makes "bounded" a claim about the input length rather than about the size
//     of today's test.
//
// The reading is process-wide and this test lives in that process, so
// TestTheResidentReadingSeesWhatIsRetained is the control: an instrument that
// answered the same number whatever happened would pass every case below and
// fail that one.
//
// # The bounds, as the source at the pin states them
//
// | sequence under test                    | the library's bound                                                              |
// | -------------------------------------- | -------------------------------------------------------------------------------- |
// | `ESC ] 0 ;` + data, never terminated    | osc.zig `Parser.MAX_BUF = 2048`, a fixed array: a normal OSC is never allocated    |
// | `ESC ] 52 ; c ;` + data (an allocating capture) | osc.zig `Parser.MAX_ALLOCATING_BUF = 8 MiB`, past which the capture stops taking bytes |
// | `ESC P D` + data (an unrecognised hook) | dcs.zig `.ignore`: every byte after an unrecognised hook is discarded             |
// | `ESC P + q` + data (XTGETTCAP, a capture) | dcs.zig `Handler.max_bytes = 1 MiB`, past which the capture is dropped          |
//
// The first and third are the shapes the runtime's own schedules feed, so those
// schedules' "the runtime discarded nothing" is joined here by "and the library
// discarded what it could not keep". The second and fourth are the allocating
// paths of the same two sequences, and they are here because they are where the
// memory is: an OSC 52 clipboard write and an XTGETTCAP query are the two
// captures that grow at all, and both stop at a constant.

// settle brings the process to a floor before a reading. A plain runtime.GC
// is not enough: the Go runtime returns freed pages to the OS lazily, so a test
// that ran before this one can still be releasing tens of megabytes into the
// window being measured, and a reading that falls by 25 MiB while 64 MiB is
// being fed cannot show what the feed retained.
func settle() { debug.FreeOSMemory() }

// residentKiB is the process's resident set, in KiB, read from the kernel.
//
// /proc/self/statm's second field is the resident page count, and it is the
// reading that shows a CURRENT figure rather than a high-water mark:
// syscall.Rusage.Maxrss was tried first and could not see a retained 8 MiB,
// because the peak of the test binary is already above it.
func residentKiB(t *testing.T) int64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Skipf("the resident set is read from /proc/self/statm, which this platform does not have, so the library's bound cannot be measured on it: the measurement is a Linux reading of the process the pinned archive is linked into (doc.go)")
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		t.Fatalf("/proc/self/statm answered %q, want at least two fields", raw)
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		t.Fatalf("parsing the resident page count in %q: %v", raw, err)
	}
	return pages * int64(os.Getpagesize()) / 1024
}

// feedUnterminated hands the terminal n further bytes of a sequence that will
// not terminate it, in chunks, and answers the resident set afterwards. The
// chunk buffer is allocated once and reused: what the reading must see is what
// the TERMINAL retains, and a per-chunk allocation of the test's own would be
// an allocation of the same size as the thing being measured.
func feedUnterminated(t *testing.T, term emulator.Terminal, n int) int64 {
	t.Helper()
	const chunkSize = 64 << 10
	chunk := bytes.Repeat([]byte("A"), chunkSize)
	for fed := 0; fed < n; fed += len(chunk) {
		if _, err := term.Ingest(chunk); err != nil {
			t.Fatalf("ingesting bytes %d..%d of an unterminated sequence: %v", fed, fed+len(chunk), err)
		}
	}
	settle()
	return residentKiB(t)
}

// TestTheResidentReadingSeesWhatIsRetained is the control the measurement below
// depends on: the reading has to move when memory is retained. Without it, four
// cases passing would be equally consistent with a library that bounds nothing
// and an instrument that measures nothing.
func TestTheResidentReadingSeesWhatIsRetained(t *testing.T) {
	settle()
	before := residentKiB(t)

	const size = 32 << 20
	retained := make([]byte, size)
	for i := range retained {
		retained[i] = byte(i)
	}
	settle()
	after := residentKiB(t)

	if got := after - before; got < size/1024/2 {
		t.Errorf("the resident set moved by %d KiB after %d KiB was allocated and touched, want at least %d: the instrument does not see retained memory, so the measurement below proves nothing",
			got, size>>10, size/1024/2)
	}
	runtime.KeepAlive(retained)
}

// TestTheLibraryBoundsWhatItHoldsForAnUnterminatedSequence feeds the pinned
// parser 1 MiB, 16 MiB and 64 MiB of four sequences that never terminate and
// holds what it retains to the bound the source states.
func TestTheLibraryBoundsWhatItHoldsForAnUnterminatedSequence(t *testing.T) {
	const (
		small  = 1 << 20
		middle = 16 << 20
		large  = 64 << 20
		// plateauKiB bounds the move between the 16 MiB and the 64 MiB point.
		// The measured moves are tens of KiB for all four shapes; 48 MiB of
		// input is what a buffer that grew with its input would have to show
		// here, and this is the ceiling that separates the two.
		plateauKiB = 1 << 10
	)

	cases := []struct {
		name   string
		prefix string
		// ceilingKiB is what the library may retain: the constant the source
		// states, plus room for the allocator's own rounding and the pages the
		// rest of the test binary faults in while the sequence is fed.
		ceilingKiB int64
		where      string
	}{
		{
			name:       "an OSC title that never terminates",
			prefix:     "\x1b]0;",
			ceilingKiB: 4 << 10,
			where:      "osc.zig Parser.MAX_BUF = 2048, a fixed array in the parser",
		},
		{
			name:       "an OSC that captures into allocated storage",
			prefix:     "\x1b]52;c;",
			ceilingKiB: 12 << 10,
			where:      "osc.zig Parser.MAX_ALLOCATING_BUF = 8 MiB",
		},
		{
			name:       "the DCS the runtime's schedule feeds",
			prefix:     "\x1bPD",
			ceilingKiB: 4 << 10,
			where:      "dcs.zig .ignore: an unrecognised hook discards what follows it",
		},
		{
			name:       "a DCS that captures",
			prefix:     "\x1bP+q",
			ceilingKiB: 4 << 10,
			where:      "dcs.zig Handler.max_bytes = 1 MiB",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			term, err := New(emulator.Geometry{Cols: 80, Rows: 24, CellWidthPx: 10, CellHeightPx: 20})
			if err != nil {
				t.Fatalf("building the terminal: %v", err)
			}
			defer term.Close()

			// The hook is fed once, and the baseline is taken before a byte of
			// payload is, so what the three readings measure is the growth the
			// PAYLOAD causes rather than where the process happens to start.
			if _, err := term.Ingest([]byte(c.prefix)); err != nil {
				t.Fatalf("ingesting the sequence's introducer %q: %v", c.prefix, err)
			}
			settle()
			base := residentKiB(t)

			at1 := feedUnterminated(t, term, small) - base
			at16 := feedUnterminated(t, term, middle-small) - base
			at64 := feedUnterminated(t, term, large-middle) - base
			t.Logf("resident set above the start of the sequence: +%d KiB at 1 MiB fed, +%d KiB at 16 MiB, +%d KiB at 64 MiB (bound: %s)",
				at1, at16, at64, c.where)

			if growth := at64 - at1; growth > c.ceilingKiB {
				t.Errorf("the library retained %d KiB more after 64 MiB of an unterminated sequence than after 1 MiB, want at most %d KiB (%s): the sequence's length is not bounded by a constant",
					growth, c.ceilingKiB, c.where)
			}
			if move := at64 - at16; move > plateauKiB {
				t.Errorf("the resident set moved by %d KiB between 16 MiB and 64 MiB fed, want at most %d: 48 MiB of further input made the library retain more, so what it holds grows with the sequence rather than being bounded",
					move, plateauKiB)
			}
		})
	}
}
