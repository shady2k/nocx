// Command geom replays a recorded capture through one emulator column by
// column, and scores the three candidate screens against xterm.js.
//
// It exists because ADR-0041 chose x/vt on geometry, that judgement was made
// on a corpus that was never committed, and the corpus it decides between is
// three emulators fed identical bytes. So the tool is deliberately small and
// has no opinion: `replay` writes down every column of the final screen, and
// `score` compares those write-downs. Nothing normalises at emit time — each
// emulator's own answer is preserved, and the single normalisation a
// comparison needs happens once, in score.
//
//	geom replay -emu xvt|ghostty -capture c.jsonl -out out.json [-parts N|-bytewise]
//	geom score  -capture htop -dir results/geometry
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
	"nocx.internal/spikes/emulator/ghostty"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "replay":
		err = runReplay(os.Args[2:])
	case "score":
		err = runScore(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "geom:", err)
		os.Exit(1)
	}
}

const usage = `usage:
  geom replay -emu xvt|ghostty -capture FILE -out FILE [-parts N | -bytewise]
  geom score  -capture NAME -dir DIR`

// Cell is one column of the final screen, as the emulator itself reports it:
// the text it holds and its column footprint (2 wide, 1 narrow, 0 a column
// that is not the first of anything). No normalisation happens here.
type Cell struct {
	T string `json:"t"`
	W int    `json:"w"`
}

// Dump is one emulator's answer for one capture and one feed shape.
type Dump struct {
	Capture      string   `json:"capture"`
	Emulator     string   `json:"emulator"`
	Mode         string   `json:"mode"`
	Cols         int      `json:"cols"`
	Rows         int      `json:"rows"`
	CursorX      int      `json:"cursorX"`
	CursorY      int      `json:"cursorY"`
	Alt          bool     `json:"alt,omitempty"`
	Chunks       int      `json:"chunks"`
	Bytes        int      `json:"bytes"`
	Replacements int      `json:"replacementChars"`
	Cells        [][]Cell `json:"cells"`
}

type header struct {
	Agent string   `json:"agent"`
	Argv  []string `json:"argv"`
	Cols  int      `json:"cols"`
	Rows  int      `json:"rows"`
}

type chunk struct {
	AtMs   int64  `json:"atMs"`
	Offset int    `json:"offset"`
	Data   string `json:"data"`
}

// readCapture reads a capture and checks the one thing a replay depends on:
// each chunk's offset is where the chunks before it ended. That invariant is
// also what detects a capture whose byte stream the recorder corrupted —
// encoding/json replaces an invalid UTF-8 byte with U+FFFD, which is three
// bytes where there was one — so a broken file is refused rather than replayed
// as though it were whole. Replacement characters are counted as well, because
// the LAST chunk has no following offset to contradict it.
func readCapture(path string) (header, []chunk, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return header{}, nil, nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	var h header
	if !sc.Scan() {
		return h, nil, nil, fmt.Errorf("%s: empty file", path)
	}
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		return h, nil, nil, fmt.Errorf("%s: header: %w", path, err)
	}
	var chunks []chunk
	var stream []byte
	expect := 0
	for sc.Scan() {
		var c chunk
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			return h, nil, nil, fmt.Errorf("%s: chunk %d: %w", path, len(chunks), err)
		}
		if c.Offset != expect {
			return h, nil, nil, fmt.Errorf("%s: chunk %d starts at %d, expected %d — the byte stream has a hole",
				path, len(chunks), c.Offset, expect)
		}
		expect += len(c.Data)
		chunks = append(chunks, c)
		stream = append(stream, c.Data...)
	}
	if err := sc.Err(); err != nil {
		return h, nil, nil, err
	}
	if h.Cols <= 0 || h.Rows <= 0 {
		return h, nil, nil, fmt.Errorf("%s: geometry %dx%d", path, h.Cols, h.Rows)
	}
	return h, chunks, stream, nil
}

func countReplacements(b []byte) int {
	return strings.Count(string(b), "\uFFFD")
}

// partsOf turns a stream and a feed shape into the slices to write in order.
// parts=1 is the whole stream in one write; bytewise is one byte per write.
func partsOf(stream []byte, parts int, bytewise bool) [][]byte {
	if bytewise {
		parts = len(stream)
	}
	if parts < 1 {
		parts = 1
	}
	if parts > len(stream) {
		parts = max(len(stream), 1)
	}
	if parts == 1 {
		return [][]byte{stream}
	}
	out := make([][]byte, 0, parts)
	start := 0
	for i := 1; i <= parts; i++ {
		end := len(stream) * i / parts
		out = append(out, stream[start:end])
		start = end
	}
	return out
}

func modeOf(parts int, bytewise bool) string {
	if bytewise {
		return "bytewise"
	}
	if parts <= 1 {
		return "whole"
	}
	return "split:" + strconv.Itoa(parts)
}

func runReplay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	emu := fs.String("emu", "", "xvt or ghostty")
	capture := fs.String("capture", "", "capture JSONL")
	out := fs.String("out", "", "dump JSON")
	parts := fs.Int("parts", 1, "feed the stream in this many writes")
	bytewise := fs.Bool("bytewise", false, "feed the stream one byte per write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *emu == "" || *capture == "" || *out == "" {
		return errors.New(usage)
	}
	h, chunks, stream, err := readCapture(*capture)
	if err != nil {
		return err
	}
	if n := countReplacements(stream); n > 0 {
		// The same refusal geometry.mjs makes, for the same reason: the capture
		// format carries bytes as a JSON string, so a byte that is not valid
		// UTF-8 was already replaced with U+FFFD by the recorder, and a dump of
		// that stream would be a measurement of bytes the program never wrote.
		return fmt.Errorf("%s: %d replacement characters in the decoded stream", *capture, n)
	}
	shape := partsOf(stream, *parts, *bytewise)
	var d Dump
	switch *emu {
	case "xvt":
		d = replayXVT(h, chunks, stream, shape)
	case "ghostty":
		d = replayGhostty(h, chunks, stream, shape)
	default:
		return fmt.Errorf("unknown emulator %q", *emu)
	}
	d.Capture = captureName(*capture)
	d.Mode = modeOf(*parts, *bytewise)
	d.Replacements = countReplacements(stream)
	return writeJSON(*out, d)
}

// captureName is the file's base name without its extension: the identity the
// three dumps of one capture share.
func captureName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func writeJSON(path string, v any) error {
	return writeJSONIndent(path, v, "")
}

// writeJSONIndent writes v as JSON with an optional indent. The dumps stay
// compact — they are machine output, 173 files of cells, and are not committed
// — while the SCORES are written indented and committed, because the report's
// tables are derived from them and a reviewer should be able to check a number
// without re-running the measurement. Two spaces and a trailing newline is
// also what prettier writes, so regenerating a score leaves the tree clean.
func writeJSONIndent(path string, v any, indent string) error {
	b, err := json.MarshalIndent(v, "", indent)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, b, 0o644)
}

// drainer reads x/vt's reply pipe for as long as the emulator lives. x/vt
// answers a program through an io.Pipe and a reply-producing write does not
// return until somebody reads, so a replay with no reader deadlocks —
// ADR-0041 records it and the spike's probe 10 measured it.
type drainer struct {
	mu  sync.Mutex
	buf []byte
}

func replayXVT(h header, chunks []chunk, stream []byte, shape [][]byte) Dump {
	e := vt.NewEmulator(h.Cols, h.Rows)
	d := &drainer{}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := e.Read(b)
			if n > 0 {
				d.mu.Lock()
				d.buf = append(d.buf, b[:n]...)
				d.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { _ = e.Close() }()
	for _, p := range shape {
		if _, err := e.Write(p); err != nil {
			panic(fmt.Sprintf("x/vt write: %v", err))
		}
	}
	// A reply-producing sequence blocks its writer until the drainer takes the
	// bytes; give the last one a moment to be produced before reading the
	// screen, rather than reading a screen the emulator has not finished.
	time.Sleep(20 * time.Millisecond)
	dump := Dump{
		Cols: h.Cols, Rows: h.Rows, Chunks: len(chunks), Bytes: len(stream),
		Alt: e.IsAltScreen(),
	}
	pos := e.CursorPosition()
	dump.CursorX, dump.CursorY = pos.X, pos.Y
	dump.Cells = make([][]Cell, h.Rows)
	for y := range h.Rows {
		row := make([]Cell, h.Cols)
		for x := range h.Cols {
			c := e.CellAt(x, y)
			if c == nil {
				// A nil cell is an untouched column, not a continuation: the
				// zero value would be {T:"", W:0} and read as one.
				row[x] = Cell{T: "", W: 1}
				continue
			}
			// Verbatim, including a printed space: the comparison in score is
			// the single place that decides a blank and a space are the same
			// thing, and a dump that had already normalised could not be read
			// back to see which one the emulator held.
			row[x] = Cell{T: c.Content, W: c.Width}
		}
		dump.Cells[y] = row
	}
	return dump
}

func replayGhostty(h header, chunks []chunk, stream []byte, shape [][]byte) Dump {
	t := ghostty.New(h.Cols, h.Rows)
	defer t.Free()
	for _, p := range shape {
		t.Write(p)
	}
	dump := Dump{Cols: h.Cols, Rows: h.Rows, Chunks: len(chunks), Bytes: len(stream)}
	cx, cy := t.Cursor()
	dump.CursorX, dump.CursorY = cx, cy
	dump.Cells = make([][]Cell, h.Rows)
	for y := range h.Rows {
		row := make([]Cell, h.Cols)
		for x := range h.Cols {
			content, w, has := t.Cell(x, y)
			switch {
			case !has && (w == ghostty.WidthSpacerTail || w == ghostty.WidthSpacerHead):
				row[x] = Cell{T: "", W: 0}
			case !has:
				row[x] = Cell{T: "", W: 1}
			default:
				row[x] = Cell{T: content, W: ghosttyWidth(w)}
			}
		}
		dump.Cells[y] = row
	}
	return dump
}

func ghosttyWidth(w ghostty.CellWidth) int {
	switch w {
	case ghostty.WidthWide:
		return 2
	case ghostty.WidthSpacerTail, ghostty.WidthSpacerHead:
		return 0
	default:
		return 1
	}
}

// ---------------------------------------------------------------- scoring

// Score is one capture's result: what each candidate did against xterm.js.
type Score struct {
	Capture    string                    `json:"capture"`
	Cols       int                       `json:"cols"`
	Rows       int                       `json:"rows"`
	NonBlank   int                       `json:"xtermNonBlankColumns"`
	Cursor     map[string][2]int         `json:"cursor"`
	Modes      map[string][]ModeResult   `json:"modes"`
	Candidates map[string]CandidateScore `json:"candidates"`
	// Disagreements is the three-way record: every column where either
	// candidate differs from xterm.js, with what all three put there.
	Disagreements []Disagreement `json:"disagreements"`
}

// ModeResult is one feed shape compared with the same emulator's whole-file
// screen: whether the final screen is identical, and where it first is not.
type ModeResult struct {
	Mode      string `json:"mode"`
	TextSame  bool   `json:"textSame"`
	GeomSame  bool   `json:"geometrySame"`
	FirstText string `json:"firstTextDiff,omitempty"`
	FirstGeom string `json:"firstGeometryDiff,omitempty"`
	TextDiffs int    `json:"textDiffColumns"`
	GeomDiffs int    `json:"geometryDiffColumns"`
}

// CandidateScore is one candidate against xterm.js, on the two questions the
// brief separates. `text` is the characters the final screen holds: a row
// counts when its concatenated text is identical, which is the definition the
// brief gives. `geometry` is where those characters fall: a column counts when
// it holds the same class of content.
type CandidateScore struct {
	RowsIdentical    int `json:"rowsIdentical"`
	RowsTotal        int `json:"rowsTotal"`
	GeomColsAgree    int `json:"geometryColumnsAgree"`
	GeomColsTotal    int `json:"geometryColumnsTotal"`
	ContentGeomAgree int `json:"contentGeometryAgree"`
	ContentTotal     int `json:"contentColumns"`
	Disagreeing      int `json:"disagreeingColumns"`
}

// Disagreement is one column where a candidate differs from xterm.js, with
// what all three put there — the record the decision is made from.
type Disagreement struct {
	Row       int      `json:"row"`
	Col       int      `json:"col"`
	XVT       []string `json:"xvtKinds"`
	Ghostty   []string `json:"ghosttyKinds"`
	Xterm     Cell     `json:"xterm"`
	XVTCell   Cell     `json:"xvt"`
	GhostCell Cell     `json:"ghostty"`
}

// classOf is the ONE normalisation a geometry comparison needs, applied to all
// three emulators' raw cells equally: a column is the first column of a
// cluster (narrow or wide), the continuation of one, a cell holding a
// zero-width cluster of its own, or blank. ADR-0041 scored x/vt 100/100 on
// geometry while its continuation cells hold a space and xterm.js's hold
// nothing, so comparing raw cell contents would manufacture misses that are
// representation, not geometry.
func classOf(c Cell) string {
	switch {
	case c.W == 0 && c.T == "":
		return "cont"
	case c.W == 0:
		return "zero"
	case c.W >= 2:
		return "wide"
	case strings.TrimSpace(c.T) == "":
		return "blank"
	default:
		return "narrow"
	}
}

// tokenOf is what one column HOLDS — the brief's definition of geometry: the
// characters in that cell, with the second half of a wide cluster counting as
// a continuation. A continuation contributes nothing (the wide cluster to its
// left already stands for both columns) and a column with no visible
// characters contributes a space, so "a b" and "ab" cannot compare equal. Two
// emulators that put the same characters in the same columns agree; ADR-0041
// needed the same normalisation to score x/vt 100/100, since x/vt's wide tails
// hold a space where xterm.js's hold nothing.
//
// The same function renders a row's text for the `text` metric, which is the
// different question of whether the characters survived at all.
// geomKey is what the geometry metric compares at one column: the class of
// cell it is (blank, narrow, wide, continuation, or a zero-width cell holding
// its own cluster) AND the characters that cell holds. Both are needed — the
// class catches a footprint difference at a column whose characters are the
// same (x/vt gives U+2764 U+FE0F two columns where xterm.js gives it one), and
// the characters catch a cluster-assembly difference at a column whose class
// is the same (x/vt holds the whole ZWJ family in one 2-wide cell where
// xterm.js holds its first member).
func geomKey(c Cell) string { return classOf(c) + ":" + tokenOf(c) }

func tokenOf(c Cell) string {
	if c.W == 0 {
		// A zero-width cell holding a combining mark or a split cluster's tail
		// contributes its character; x/vt's wide-tail cells hold a space and
		// contribute nothing, exactly as xterm.js's empty ones do.
		if strings.TrimSpace(c.T) == "" {
			return ""
		}
		return c.T
	}
	if strings.TrimSpace(c.T) == "" {
		return " "
	}
	return c.T
}

func rowText(row []Cell) string {
	var sb strings.Builder
	for _, c := range row {
		sb.WriteString(tokenOf(c))
	}
	return strings.TrimRight(sb.String(), " ")
}

// badColumns is where one candidate differs from the reference, keyed by
// column and by the kind of difference. The cells themselves are read from the
// candidate's whole dump when the three-way record is built, so a column where
// a candidate AGREES still reports what it put there.
type badColumns struct {
	kinds map[[2]int][]string
}

func newBadColumns() badColumns {
	return badColumns{kinds: map[[2]int][]string{}}
}

func (b badColumns) add(row, col int, kind string) {
	key := [2]int{row, col}
	for _, k := range b.kinds[key] {
		if k == kind {
			return
		}
	}
	b.kinds[key] = append(b.kinds[key], kind)
}

func (b badColumns) sortedKeys() [][2]int {
	keys := make([][2]int, 0, len(b.kinds))
	for k := range b.kinds {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	return keys
}

func compare(ref, got Dump) (CandidateScore, badColumns) {
	s := CandidateScore{}
	bad := newBadColumns()
	limit := min(len(ref.Cells), len(got.Cells))
	for y := range limit {
		refRow, gotRow := ref.Cells[y], got.Cells[y]
		n := min(len(refRow), len(gotRow))
		s.RowsTotal++
		if rowText(refRow) == rowText(gotRow) {
			s.RowsIdentical++
		} else {
			// A text difference is a row-level fact; the column recorded is
			// the first one whose contribution differs, so the three-way
			// record can point at it.
			for x := range n {
				if tokenOf(refRow[x]) != tokenOf(gotRow[x]) {
					bad.add(y, x, "text")
					break
				}
			}
		}
		for x := range n {
			rc, gc := refRow[x], gotRow[x]
			s.GeomColsTotal++
			geomSame := geomKey(rc) == geomKey(gc)
			if geomSame {
				s.GeomColsAgree++
			} else {
				bad.add(y, x, "geometry")
			}
			if classOf(rc) != "blank" {
				s.ContentTotal++
				if geomSame {
					s.ContentGeomAgree++
				}
			}
		}
	}
	s.Disagreeing = len(bad.kinds)
	return s, bad
}

func compareModes(whole, other Dump, mode string) ModeResult {
	r := ModeResult{Mode: mode}
	s, bad := compare(whole, other)
	r.TextDiffs = s.RowsTotal - s.RowsIdentical
	r.GeomDiffs = s.GeomColsTotal - s.GeomColsAgree
	r.TextSame = r.TextDiffs == 0
	r.GeomSame = r.GeomDiffs == 0
	for _, k := range bad.sortedKeys() {
		loc := fmt.Sprintf("row %d col %d %q/%d vs %q/%d",
			k[0], k[1], whole.Cells[k[0]][k[1]].T, whole.Cells[k[0]][k[1]].W,
			other.Cells[k[0]][k[1]].T, other.Cells[k[0]][k[1]].W)
		for _, kind := range bad.kinds[k] {
			switch {
			case kind == "text" && r.FirstText == "":
				r.FirstText = loc
			case kind == "geometry" && r.FirstGeom == "":
				r.FirstGeom = loc
			}
		}
	}
	return r
}

func runScore(args []string) error {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	capture := fs.String("capture", "", "capture name, e.g. htop")
	dir := fs.String("dir", "results/geometry", "directory of dumps")
	out := fs.String("out", "", "score JSON (default <dir>/<capture>.score.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *capture == "" {
		return errors.New(usage)
	}
	load := func(emu, mode string) (Dump, error) {
		var d Dump
		path := filepath.Join(*dir, *capture+"."+emu+"."+mode+".json")
		b, err := os.ReadFile(path)
		if err != nil {
			return d, err
		}
		return d, json.Unmarshal(b, &d)
	}
	xtermWhole, err := load("xterm", "whole")
	if err != nil {
		return err
	}
	if xtermWhole.Replacements > 0 {
		return fmt.Errorf("%s: the xterm dump was taken from a stream with %d replacement characters",
			*capture, xtermWhole.Replacements)
	}
	score := Score{
		Capture: *capture, Cols: xtermWhole.Cols, Rows: xtermWhole.Rows,
		Cursor:     map[string][2]int{"xterm": {xtermWhole.CursorX, xtermWhole.CursorY}},
		Modes:      map[string][]ModeResult{},
		Candidates: map[string]CandidateScore{},
		// An empty array, not null: a reader of the JSON should not have to
		// treat "no disagreements" and "not measured" the same.
		Disagreements: []Disagreement{},
	}
	for _, row := range xtermWhole.Cells {
		for _, c := range row {
			if classOf(c) != "blank" {
				score.NonBlank++
			}
		}
	}
	modes, err := modesIn(*dir, *capture)
	if err != nil {
		return err
	}
	candidateBad := map[string]badColumns{}
	wholeDumps := map[string]Dump{}
	for _, emu := range []string{"xvt", "ghostty"} {
		whole, err := load(emu, "whole")
		if err != nil {
			return err
		}
		if whole.Replacements > 0 {
			return fmt.Errorf("%s: the %s dump was taken from a stream with %d replacement characters",
				*capture, emu, whole.Replacements)
		}
		wholeDumps[emu] = whole
		score.Cursor[emu] = [2]int{whole.CursorX, whole.CursorY}
		cs, bad := compare(xtermWhole, whole)
		score.Candidates[emu] = cs
		candidateBad[emu] = bad
		var results []ModeResult
		for _, mode := range modes {
			d, err := load(emu, mode)
			if err != nil {
				return err
			}
			results = append(results, compareModes(whole, d, mode))
		}
		score.Modes[emu] = results
	}
	// The reference against itself in another feed shape: xterm.js's own
	// answer to whether chunking matters, without which a green result for the
	// candidates would have nothing to be compared with.
	var xtResults []ModeResult
	for _, mode := range modes {
		d, err := load("xterm", mode)
		if err != nil {
			return err
		}
		xtResults = append(xtResults, compareModes(xtermWhole, d, mode))
	}
	score.Modes["xterm"] = xtResults

	keys := map[[2]int]bool{}
	for _, bad := range candidateBad {
		for k := range bad.kinds {
			keys[k] = true
		}
	}
	var ordered [][2]int
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i][0] != ordered[j][0] {
			return ordered[i][0] < ordered[j][0]
		}
		return ordered[i][1] < ordered[j][1]
	})
	for _, k := range ordered {
		cellOf := func(emu string) Cell {
			d := wholeDumps[emu]
			if k[0] < len(d.Cells) && k[1] < len(d.Cells[k[0]]) {
				return d.Cells[k[0]][k[1]]
			}
			return Cell{}
		}
		kindsOf := func(emu string) []string {
			if ks := candidateBad[emu].kinds[k]; ks != nil {
				return ks
			}
			return []string{}
		}
		score.Disagreements = append(score.Disagreements, Disagreement{
			Row: k[0], Col: k[1],
			XVT:       kindsOf("xvt"),
			Ghostty:   kindsOf("ghostty"),
			Xterm:     xtermWhole.Cells[k[0]][k[1]],
			XVTCell:   cellOf("xvt"),
			GhostCell: cellOf("ghostty"),
		})
	}
	if *out == "" {
		*out = filepath.Join(*dir, *capture+".score.json")
	}
	return writeJSONIndent(*out, score, "  ")
}

// modesIn finds the feed shapes that were dumped for one capture, so a score
// covers exactly what exists rather than a list written twice.
func modesIn(dir, capture string) ([]string, error) {
	entries, err := filepath.Glob(filepath.Join(dir, capture+".xterm.*.json"))
	if err != nil {
		return nil, err
	}
	var modes []string
	for _, e := range entries {
		name := strings.TrimSuffix(filepath.Base(e), ".json")
		mode := strings.TrimPrefix(name, capture+".xterm.")
		if mode != "whole" {
			modes = append(modes, mode)
		}
	}
	sort.Slice(modes, func(i, j int) bool { return modeLess(modes[i], modes[j]) })
	return modes, nil
}

func modeLess(a, b string) bool {
	na, ea := strconv.Atoi(strings.TrimPrefix(a, "split:"))
	nb, eb := strconv.Atoi(strings.TrimPrefix(b, "split:"))
	switch {
	case ea != nil:
		return true
	case eb != nil:
		return false
	default:
		return na < nb
	}
}
