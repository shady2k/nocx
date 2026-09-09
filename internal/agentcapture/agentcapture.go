// Package agentcapture owns the capture FORMAT: the JSONL a real PTY's bytes
// are recorded into, and the replay that turns it back into the frame a driver
// classifies.
//
// # Why it is a package and not the command it came from
//
// cmd/agent-capture recorded the corpus that made the claude driver right, and
// for a while it was the only thing that read the format, so the format lived
// inside it. Calibration (nocx-etejh) needs the same file: a labelled set IS a
// capture plus a mark per label. A second reader written beside the first
// would be two owners of one format, which is the shape AGENTS.md names — they
// agree on every file anybody tried and disagree on the one nobody did. So the
// format moved here and the command reads it through this package, which is
// also what lets the calibration set be inspected with the command a person
// already has:
//
//	go run ./cmd/agent-capture replay -at 0,1,2 <set>/capture.jsonl
//
// # Replay goes through panegrid, not through a bare emulator
//
// The frame a driver classifies in production comes out of a panegrid Store
// fed from byte zero. Replaying through anything else would answer about a
// screen the product never produces, and the difference is exactly the column
// geometry ADR-0041 pins the emulator for.
package agentcapture

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// Header is the capture's first line: what was recorded, at what geometry.
type Header struct {
	Agent   string   `json:"agent"`
	Argv    []string `json:"argv"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
	Started string   `json:"started"`
	Script  []string `json:"script"`
}

// Chunk is one read off the PTY: when it arrived, where it sits in the stream,
// and the bytes. Offset is redundant with the lengths before it and is stored
// anyway, because it is what lets a truncated or hand-edited file be detected
// rather than replayed as though it were whole.
type Chunk struct {
	AtMs   int64  `json:"atMs"`
	Offset int    `json:"offset"`
	Data   string `json:"data"`
}

// Moment is one replayed mark: the screen as it stood there, and how much of
// the stream had been consumed to get it.
type Moment struct {
	AtMs   int64
	Frame  panegrid.Frame
	Chunks int
	Offset int
}

// Write stores a capture atomically enough for a file a person may be reading:
// it is written whole, and a failure part-way leaves the previous file alone.
//
// Mode 0600, because a capture holds whatever was on the screen — the same
// reason the app directory's documents are written that way (ADR-0011).
func Write(path string, header Header, chunks []Chunk) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the caller explicitly supplies the capture path
	if err != nil {
		return fmt.Errorf("cannot create capture %q: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(header); err != nil {
		_ = file.Close()
		return fmt.Errorf("write capture header: %w", err)
	}
	for _, chunk := range chunks {
		if err := encoder.Encode(chunk); err != nil {
			_ = file.Close()
			return fmt.Errorf("write capture chunk: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close capture %q: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("place capture %q: %w", path, err)
	}
	return nil
}

// Read parses a capture and validates the two invariants a replay depends on:
// time does not run backwards, and the stream has no hole in it. Both are
// refusals rather than repairs — a capture with a gap replays a screen that
// never existed, which is worse than no capture at all.
func Read(path string) (Header, []Chunk, error) {
	file, err := os.Open(path) //nolint:gosec // the caller explicitly supplies the capture path
	if err != nil {
		return Header{}, nil, fmt.Errorf("cannot open capture %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Header{}, nil, fmt.Errorf("read capture header: %w", err)
		}
		return Header{}, nil, errors.New("capture is empty; expected a JSONL header")
	}
	var header Header
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return Header{}, nil, fmt.Errorf("decode capture header: %w", err)
	}
	if header.Agent == "" || len(header.Argv) == 0 {
		return Header{}, nil, errors.New("capture header has no agent or argv")
	}
	if header.Cols <= 0 || header.Rows <= 0 {
		return Header{}, nil, fmt.Errorf("capture header has invalid geometry %dx%d", header.Cols, header.Rows)
	}

	chunks := make([]Chunk, 0)
	expectedOffset := 0
	var previousAt int64
	for lineNumber := 2; scanner.Scan(); lineNumber++ {
		var chunk Chunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			return Header{}, nil, fmt.Errorf("decode capture chunk on line %d: %w", lineNumber, err)
		}
		if chunk.AtMs < 0 || chunk.AtMs < previousAt {
			return Header{}, nil, fmt.Errorf("capture chunk on line %d has out-of-order atMs %d", lineNumber, chunk.AtMs)
		}
		if chunk.Offset != expectedOffset {
			return Header{}, nil, fmt.Errorf("capture chunk on line %d starts at offset %d, expected %d", lineNumber, chunk.Offset, expectedOffset)
		}
		chunks = append(chunks, chunk)
		expectedOffset += len(chunk.Data)
		previousAt = chunk.AtMs
	}
	if err := scanner.Err(); err != nil {
		return Header{}, nil, fmt.Errorf("read capture: %w", err)
	}
	return header, chunks, nil
}

// ChunksThrough counts the chunks that had arrived by mark, continuing from
// already. The boundary is inclusive: a chunk stamped exactly at the mark is
// part of the screen at that mark, which is what makes a mark a moment rather
// than a moment-minus-epsilon.
func ChunksThrough(chunks []Chunk, mark int64, already int) int {
	for already < len(chunks) && chunks[already].AtMs <= mark {
		already++
	}
	return already
}

// EndOffset is where the consumed prefix of the stream ends.
func EndOffset(chunks []Chunk, consumed int) int {
	if consumed == 0 {
		return 0
	}
	return chunks[consumed-1].Offset + len(chunks[consumed-1].Data)
}

// Frames replays a capture and answers the screen at each mark. Marks must be
// non-decreasing, because the emulator is fed forward once: a capture cannot
// be rewound, only replayed from byte zero, and asking for an earlier mark
// after a later one would silently answer the later screen.
func Frames(lg log.Logger, header Header, chunks []Chunk, marks []int64) ([]Moment, error) {
	r, err := NewReplayer(lg, header)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	out := make([]Moment, 0, len(marks))
	consumed := 0
	var previous int64
	for i, mark := range marks {
		if i > 0 && mark < previous {
			return nil, fmt.Errorf("agentcapture: marks must be non-decreasing; %d follows %d", mark, previous)
		}
		previous = mark
		from := consumed
		consumed = ChunksThrough(chunks, mark, consumed)
		if err := r.Feed(chunks[from:consumed]); err != nil {
			return nil, fmt.Errorf("agentcapture: feed capture at %dms: %w", mark, err)
		}
		f, err := r.Frame()
		if err != nil {
			return nil, fmt.Errorf("agentcapture: frame at %dms: %w", mark, err)
		}
		out = append(out, Moment{AtMs: mark, Frame: f, Chunks: consumed, Offset: EndOffset(chunks, consumed)})
	}
	return out, nil
}

// Replayer is one emulator fed forward through a capture. Callers that want
// every mark in one pass use Frames; this is for a caller that also wants to
// paint something MORE onto the replayed screen.
type Replayer struct {
	store *panegrid.Store
	pane  string
}

// replayPane is the pane id a replay enrols under. A Replayer owns its Store,
// so the name never collides with anything.
const replayPane = "capture"

// NewReplayer enrols a grid at the capture's geometry. Close releases it.
func NewReplayer(lg log.Logger, header Header) (*Replayer, error) {
	if header.Cols <= 0 || header.Rows <= 0 {
		return nil, fmt.Errorf("agentcapture: capture geometry is %dx%d", header.Cols, header.Rows)
	}
	store := panegrid.New(lg)
	if err := store.Enrol(replayPane, header.Cols, header.Rows); err != nil {
		return nil, fmt.Errorf("agentcapture: enrol replay grid: %w", err)
	}
	return &Replayer{store: store, pane: replayPane}, nil
}

// Feed hands the next chunks to the emulator.
func (r *Replayer) Feed(chunks []Chunk) error {
	for _, c := range chunks {
		r.store.Feed(r.pane, []byte(c.Data))
	}
	return nil
}

// Frame is the screen as it stands.
func (r *Replayer) Frame() (panegrid.Frame, error) { return r.store.Frame(r.pane) }

// Close withdraws the grid. A Replayer that is not closed leaks one emulator
// and the goroutine draining its replies.
func (r *Replayer) Close() { r.store.Withdraw(r.pane) }

// Paint encodes a frame as the bytes that reproduce it, so that a frame can be
// stored in a capture and read back by replaying to its mark.
//
// # Why a frame is stored as BYTES rather than as itself
//
// A calibration set is evidence, and the thing that reads evidence is the
// replay a person already has. Storing the frame as a serialised struct would
// need a second reader, a second version and a second way to be wrong, and
// would not be inspectable with `agent-capture replay`. Storing the bytes that
// PRODUCE the frame keeps one format for both.
//
// # What survives, and why that is everything a rule can read
//
// A Frame is text, width and cursor — panegrid answers nothing else, on
// purpose, because both powers the AD-6 amendment grants are positional. So
// colour and attributes are not lost here; they were never in the frame. Each
// row is written from its first column so a double-width grapheme lands where
// it stood, the alternate screen is restored because a rule may read it, and
// the cursor is parked LAST because it is the one marker an agent cannot
// forge and everything before it moves the cursor.
func Paint(f panegrid.Frame) []byte {
	var b strings.Builder
	if f.AltScreen {
		b.WriteString("\x1b[?1049h")
	} else {
		b.WriteString("\x1b[?1049l")
	}
	b.WriteString("\x1b[2J")
	for y, line := range f.Lines {
		text := rowBytes(line)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s", y+1, text)
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH", f.CursorY+1, f.CursorX+1)
	return []byte(b.String())
}

// rowBytes renders one row, trimmed of the trailing blanks an erased screen
// already has. Trimming is safe precisely because the paint begins with an
// erase: what is not written is blank, and what is blank was not written.
func rowBytes(line []panegrid.Cell) string {
	last := -1
	for x, c := range line {
		if c.Width == 0 {
			continue
		}
		if c.Text != "" && c.Text != " " {
			last = x
		}
	}
	if last < 0 {
		return ""
	}
	var b strings.Builder
	for x, c := range line {
		if x > last {
			break
		}
		if c.Width == 0 {
			// A continuation cell: the grapheme before it already advanced
			// the cursor across this column.
			continue
		}
		if c.Text == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(c.Text)
	}
	return b.String()
}
