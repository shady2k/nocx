// Command nativecmp is the CGo side of the comparison: the same corpus replay,
// the same chunked-feed measurement and the same instance-count measurement as
// the wasm commands, against the same ghostty commit built natively.
//
// It exists because "the wasm build is slower" is only a claim until both are
// measured on the same input by the same-looking driver. Everything that could
// differ for a reason other than the target is made to match: the same four
// effect callbacks are installed (so the wasm shim's callback copying is paid
// on both sides), the same cell access path is used, and the corpus is replayed
// with byte-identical partitioning.
//
// This module is a scratch build target: a CGo package cannot live inside the
// measured module without breaking the `CGO_ENABLED=0 go build ./...` that is
// half the point of the wasm route, so it is generated into .nativecmp/ by
// native.sh and is gitignored.
package main

/*
#cgo CFLAGS: -I${SRCDIR}/../.vendor/ghostty/zig-out-native/include -DGHOSTTY_STATIC
#cgo LDFLAGS: ${SRCDIR}/../.vendor/ghostty/zig-out-native/lib/libghostty-vt.a -lm -lpthread
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <ghostty/vt.h>

// The effect callbacks are installed from C because cgo cannot take the
// address of a C function that has no external linkage. They do nothing but
// count, which is what the wasm shim's do (it copies replies into a buffer).
static size_t cb_write_calls = 0;
static size_t cb_write_bytes = 0;
static size_t cb_bells = 0;
static size_t cb_unknown = 0;
static size_t cb_clips = 0;

static void on_write(GhosttyTerminal t, void *ud, const uint8_t *d, size_t n) {
  (void)t; (void)ud; cb_write_calls++; cb_write_bytes += n;
}
static void on_bell(GhosttyTerminal t, void *ud) { (void)t; (void)ud; cb_bells++; }
static void on_clip(GhosttyTerminal t, void *ud, const GhosttyClipboardWrite *w) {
  (void)t; (void)ud; (void)w; cb_clips++;
}
static void on_unknown(GhosttyTerminal t, void *ud,
                       const GhosttyTerminalUnknownSequence *s) {
  (void)t; (void)ud; (void)s; cb_unknown++;
}

static GhosttyResult install_effects(GhosttyTerminal t) {
  size_t unknown_max_bytes = 256;
  GhosttyResult r;
  if ((r = ghostty_terminal_set(t, GHOSTTY_TERMINAL_OPT_WRITE_PTY, (const void*)on_write)) != GHOSTTY_SUCCESS) return r;
  if ((r = ghostty_terminal_set(t, GHOSTTY_TERMINAL_OPT_BELL, (const void*)on_bell)) != GHOSTTY_SUCCESS) return r;
  if ((r = ghostty_terminal_set(t, GHOSTTY_TERMINAL_OPT_CLIPBOARD_WRITE, (const void*)on_clip)) != GHOSTTY_SUCCESS) return r;
  if ((r = ghostty_terminal_set(t, GHOSTTY_TERMINAL_OPT_UNKNOWN_SEQUENCE, (const void*)on_unknown)) != GHOSTTY_SUCCESS) return r;
  return ghostty_terminal_set(t, GHOSTTY_TERMINAL_OPT_UNKNOWN_MAX_BYTES, &unknown_max_bytes);
}

static size_t effects_write_calls(void) { return cb_write_calls; }
static void effects_reset(void) { cb_write_calls = 0; cb_write_bytes = 0; cb_bells = 0; cb_unknown = 0; cb_clips = 0; }

// Cell grapheme cluster as UTF-8, plus its width class, at (x,y).
static int cell_facts(GhosttyTerminal t, uint16_t x, uint16_t y,
                      uint8_t *utf8, size_t utf8_len, size_t *out_len,
                      int *out_wide, int *out_has_text) {
  GhosttyPoint pt = {0};
  pt.tag = GHOSTTY_POINT_TAG_ACTIVE;
  pt.value.coordinate.x = x;
  pt.value.coordinate.y = y;
  GhosttyGridRef ref = GHOSTTY_INIT_SIZED(GhosttyGridRef);
  GhosttyResult r = ghostty_terminal_grid_ref(t, pt, &ref);
  if (r != GHOSTTY_SUCCESS) return (int)r;
  GhosttyCell cell = 0;
  if ((r = ghostty_grid_ref_cell(&ref, &cell)) != GHOSTTY_SUCCESS) return (int)r;
  bool has = false;
  GhosttyCellWide wide = GHOSTTY_CELL_WIDE_NARROW;
  ghostty_cell_get(cell, GHOSTTY_CELL_DATA_HAS_TEXT, &has);
  ghostty_cell_get(cell, GHOSTTY_CELL_DATA_WIDE, &wide);
  *out_has_text = has ? 1 : 0;
  *out_wide = (int)wide;
  *out_len = 0;
  uint32_t cps[64];
  size_t n = 64;
  r = ghostty_grid_ref_graphemes(&ref, cps, 64, &n);
  if (r != GHOSTTY_SUCCESS) return 0;
  size_t w = 0;
  for (size_t i = 0; i < n; i++) {
    uint32_t cp = cps[i];
    if (cp < 0x80) { if (w + 1 > utf8_len) break; utf8[w++] = (uint8_t)cp; }
    else if (cp < 0x800) { if (w + 2 > utf8_len) break; utf8[w++] = (uint8_t)(0xC0 | (cp >> 6)); utf8[w++] = (uint8_t)(0x80 | (cp & 0x3F)); }
    else if (cp < 0x10000) { if (w + 3 > utf8_len) break; utf8[w++] = (uint8_t)(0xE0 | (cp >> 12)); utf8[w++] = (uint8_t)(0x80 | ((cp >> 6) & 0x3F)); utf8[w++] = (uint8_t)(0x80 | (cp & 0x3F)); }
    else { if (w + 4 > utf8_len) break; utf8[w++] = (uint8_t)(0xF0 | (cp >> 18)); utf8[w++] = (uint8_t)(0x80 | ((cp >> 12) & 0x3F)); utf8[w++] = (uint8_t)(0x80 | ((cp >> 6) & 0x3F)); utf8[w++] = (uint8_t)(0x80 | (cp & 0x3F)); }
  }
  *out_len = w;
  return 0;
}

static int row_wrap_bits(GhosttyTerminal t, uint16_t y) {
  GhosttyPoint pt = {0};
  pt.tag = GHOSTTY_POINT_TAG_ACTIVE;
  pt.value.coordinate.x = 0;
  pt.value.coordinate.y = y;
  GhosttyGridRef ref = GHOSTTY_INIT_SIZED(GhosttyGridRef);
  if (ghostty_terminal_grid_ref(t, pt, &ref) != GHOSTTY_SUCCESS) return -1;
  GhosttyRow row = 0;
  if (ghostty_grid_ref_row(&ref, &row) != GHOSTTY_SUCCESS) return -1;
  bool w = false, c = false;
  if (ghostty_row_get(row, GHOSTTY_ROW_DATA_WRAP, &w) != GHOSTTY_SUCCESS) return -1;
  if (ghostty_row_get(row, GHOSTTY_ROW_DATA_WRAP_CONTINUATION, &c) != GHOSTTY_SUCCESS) return -1;
  return (w ? 1 : 0) | (c ? 2 : 0);
}
*/
import "C"

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
	"unsafe"
)

// Terminal is one native terminal.
type Terminal struct{ t C.GhosttyTerminal }

func New(cols, rows int) *Terminal {
	var t C.GhosttyTerminal
	if r := C.ghostty_terminal_new(nil, &t, C.uint16_t(cols), C.uint16_t(rows)); r != 0 {
		panic(fmt.Sprintf("ghostty_terminal_new = %d", int(r)))
	}
	if r := C.install_effects(t); r != 0 {
		panic(fmt.Sprintf("install_effects = %d", int(r)))
	}
	return &Terminal{t: t}
}

func (t *Terminal) Free() { C.ghostty_terminal_free(t.t) }
func (t *Terminal) Write(b []byte) {
	C.ghostty_terminal_vt_write(t.t, (*C.uint8_t)(unsafe.Pointer(&b[0])), C.size_t(len(b)))
	runtime.KeepAlive(b)
}

func (t *Terminal) Resize(cols, rows int) {
	C.ghostty_terminal_resize(t.t, C.uint16_t(cols), C.uint16_t(rows), 10, 20)
}

func (t *Terminal) ScrollbackRows() int {
	var v C.size_t
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS, unsafe.Pointer(&v))
	return int(v)
}

func (t *Terminal) SetScrollbackLines(n int) {
	v := C.size_t(n)
	C.ghostty_terminal_set(t.t, C.GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_LINES, unsafe.Pointer(&v))
}

func (t *Terminal) Cursor() (int, int) {
	var x, y C.uint16_t
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_CURSOR_X, unsafe.Pointer(&x))
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_CURSOR_Y, unsafe.Pointer(&y))
	return int(x), int(y)
}

func (t *Terminal) Cols() int {
	var v C.uint16_t
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_COLS, unsafe.Pointer(&v))
	return int(v)
}

func (t *Terminal) Rows() int {
	var v C.uint16_t
	C.ghostty_terminal_get(t.t, C.GHOSTTY_TERMINAL_DATA_ROWS, unsafe.Pointer(&v))
	return int(v)
}

// Cell returns the grapheme, width class, has-text flag and the wrap bits of
// the row it is on — the same facts the wasm shim's flat ABI carries.
func (t *Terminal) Cell(x, y int) (string, int, bool) {
	buf := make([]byte, 256)
	var n C.size_t
	var wide, has C.int
	if r := C.cell_facts(t.t, C.uint16_t(x), C.uint16_t(y), (*C.uint8_t)(unsafe.Pointer(&buf[0])), 256, &n, &wide, &has); r != 0 {
		return "", 0, false
	}
	return string(buf[:n]), int(wide), has != 0
}

func (t *Terminal) RowWrap(y int) (bool, bool) {
	r := C.row_wrap_bits(t.t, C.uint16_t(y))
	return r&1 != 0, r&2 != 0
}

func widthOf(w int) int {
	switch w {
	case 1:
		return 2
	case 2, 3:
		return 0
	default:
		return 1
	}
}

// ---------------------------------------------------------------- corpus

type chunk struct {
	Data string `json:"data"`
}

type capture struct {
	Name    string
	Cols    int
	Rows    int
	Stream  []byte
	Offsets []int // recorded write boundaries, as the recorder saw them
}

func loadCapture(path string) (*capture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var meta struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := json.Unmarshal(lines[0], &meta); err != nil {
		return nil, fmt.Errorf("%s: meta: %w", path, err)
	}
	c := &capture{Name: strings.TrimSuffix(base(path), ".jsonl"), Cols: meta.Cols, Rows: meta.Rows}
	for _, ln := range lines[1:] {
		var ch chunk
		if err := json.Unmarshal(ln, &ch); err != nil {
			return nil, fmt.Errorf("%s: chunk: %w", path, err)
		}
		c.Stream = append(c.Stream, ch.Data...)
		c.Offsets = append(c.Offsets, len(c.Stream))
	}
	return c, nil
}

func base(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// screen renders the whole grid as the canonical comparable form: one string
// per row of grapheme/width tokens, exactly as the geometry driver's columns
// but joined per row so a diff shows which row moved.
func screen(t *Terminal) []string {
	rows := make([]string, 0, t.Rows())
	for y := range t.Rows() {
		var sb strings.Builder
		for x := range t.Cols() {
			g, w, has := t.Cell(x, y)
			if !has || g == "" {
				sb.WriteString(". ")
				continue
			}
			fmt.Fprintf(&sb, "%q/%d ", g, widthOf(w))
		}
		rows = append(rows, strings.TrimRight(sb.String(), " "))
	}
	return rows
}

// partitions returns the write-boundary offsets for a feed shape.
func partitions(c *capture, parts int, bytewise bool) []int {
	if bytewise {
		offs := make([]int, 0, len(c.Stream))
		for i := range c.Stream {
			offs = append(offs, i+1)
		}
		return offs
	}
	if parts <= 1 {
		return []int{len(c.Stream)}
	}
	offs := make([]int, 0, parts)
	for i := 1; i <= parts; i++ {
		offs = append(offs, len(c.Stream)*i/parts)
	}
	return offs
}

// replay builds a terminal and feeds it the stream at the given write
// boundaries. The caller owns the terminal and frees it; freeing here would
// make the caller's screen read a use-after-free.
func replay(c *capture, offs []int) *Terminal {
	t := New(c.Cols, c.Rows)
	start := 0
	for _, end := range offs {
		if end > len(c.Stream) {
			end = len(c.Stream)
		}
		if end > start {
			t.Write(c.Stream[start:end])
		}
		start = end
	}
	if start < len(c.Stream) {
		t.Write(c.Stream[start:])
	}
	return t
}

type replayResult struct {
	Capture   string   `json:"capture"`
	Partition string   `json:"partition"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
	Zoomed    int      `json:"scrollback_rows"`
	Digest    string   `json:"digest"`
	Grid      []string `json:"grid"`
}

func runCorpus(args []string) error {
	fs := flag.NewFlagSet("corpus", flag.ContinueOnError)
	dir := fs.String("dir", "../emulator/corpus", "corpus directory")
	bytewiseMax := fs.Int("bytewise-max", 65536, "replay bytewise when the capture is at most this many bytes; 0 disables")
	if err := fs.Parse(args); err != nil {
		return err
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") && e.Name() != "wizard.meta.json" {
			files = append(files, *dir+"/"+e.Name())
		}
	}
	sort.Strings(files)

	enc := json.NewEncoder(os.Stdout)
	for _, f := range files {
		c, err := loadCapture(f)
		if err != nil {
			return err
		}
		shapes := []struct {
			name string
			offs []int
		}{{"whole", []int{len(c.Stream)}}, {"recorded", c.Offsets}}
		for _, p := range []int{2, 3, 5, 8, 16, 32} {
			shapes = append(shapes, struct {
				name string
				offs []int
			}{fmt.Sprintf("parts%d", p), partitions(c, p, false)})
		}
		if *bytewiseMax > 0 && len(c.Stream) <= *bytewiseMax {
			shapes = append(shapes, struct {
				name string
				offs []int
			}{"bytewise", partitions(c, 0, true)})
		}
		for _, s := range shapes {
			t := replay(c, s.offs)
			rows := screen(t)
			sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
			if err := enc.Encode(replayResult{
				Capture:   c.Name,
				Partition: s.name,
				Cols:      t.Cols(),
				Rows:      t.Rows(),
				Zoomed:    t.ScrollbackRows(),
				Digest:    hex.EncodeToString(sum[:]),
				Grid:      rows,
			}); err != nil {
				return err
			}
			t.Free()
		}
	}
	return nil
}

// ------------------------------------------------------------ throughput

// feedChunked feeds the stream in fixed-size chunks and returns the wall time
// and call count. This is the measurement that matters: the runtime ingests a
// PTY continuously, so per-call overhead is paid thousands of times per
// second, and a single 1.8 MB feed never pays it the same way.
func feedChunked(t *Terminal, stream []byte, chunk, xfeed int) (time.Duration, int) {
	calls := 0
	start := time.Now()
	for range xfeed {
		for off := 0; off < len(stream); off += chunk {
			end := off + chunk
			if end > len(stream) {
				end = len(stream)
			}
			t.Write(stream[off:end])
			calls++
		}
	}
	return time.Since(start), calls
}

func runThroughput(args []string) error {
	fs := flag.NewFlagSet("throughput", flag.ContinueOnError)
	dir := fs.String("dir", "../emulator/corpus", "corpus directory")
	captureName := fs.String("capture", "bash", "capture name")
	reps := fs.Int("reps", 3, "repetitions; the median is reported")
	xfeed := fs.Int("xfeed", 1, "times the capture is re-fed inside one timed run (a longer continuous stream)")
	jsonOut := fs.Bool("json", false, "emit one JSON line per measurement")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := loadCapture(*dir + "/" + *captureName + ".jsonl")
	if err != nil {
		return err
	}
	chunks := []int{64, 256, 1024, 4096, 16384, 65536, 1 << 20, len(c.Stream)}

	type row struct {
		Chunk   int     `json:"chunk"`
		Calls   int     `json:"calls"`
		Bytes   int     `json:"bytes"`
		MedianN int64   `json:"median_ns"`
		BytesPS float64 `json:"bytes_per_s"`
		NsPerCa float64 `json:"ns_per_call"`
	}
	var rows []row
	for _, ch := range chunks {
		var samples []int64
		var calls int
		for range *reps {
			t := New(c.Cols, c.Rows)
			d, n := feedChunked(t, c.Stream, ch, *xfeed)
			calls = n
			samples = append(samples, d.Nanoseconds())
			t.Free()
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		med := samples[len(samples)/2]
		rows = append(rows, row{
			Chunk: ch, Calls: calls, Bytes: len(c.Stream) * *xfeed, MedianN: med,
			BytesPS: float64(len(c.Stream)**xfeed) / (float64(med) / 1e9),
			NsPerCa: float64(med) / float64(calls),
		})
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		for _, r := range rows {
			_ = enc.Encode(struct {
				Driver string `json:"driver"`
				row
			}{"native", r})
		}
		return nil
	}
	fmt.Printf("capture=%s bytes=%d xfeed=%d total_bytes=%d cols=%d rows=%d reps=%d\n", c.Name, len(c.Stream), *xfeed, len(c.Stream)**xfeed, c.Cols, c.Rows, *reps)
	fmt.Printf("%10s %8s %14s %16s %12s\n", "chunk", "calls", "median_ms", "bytes/s", "ns/call")
	for _, r := range rows {
		fmt.Printf("%10d %8d %14.3f %16.0f %12.1f\n", r.Chunk, r.Calls,
			float64(r.MedianN)/1e6, r.BytesPS, r.NsPerCa)
	}
	return nil
}

// -------------------------------------------------------------- instances

func runInstances(args []string) error {
	fs := flag.NewFlagSet("instances", flag.ContinueOnError)
	count := fs.Int("n", 10, "number of live terminals")
	cols := fs.Int("cols", 120, "columns per terminal")
	rows := fs.Int("rows", 40, "rows per terminal")
	scroll := fs.Int("scrollback", 10000, "scrollback lines per terminal")
	fill := fs.Int("fill", 2000, "bytes of filler written into each terminal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	terms := make([]*Terminal, 0, *count)
	unit := []byte("the quick brown fox jumps over the lazy dog 0123456789\r\n")
	filler := bytes.Repeat(unit, (*fill+len(unit)-1)/len(unit))
	for range *count {
		t := New(*cols, *rows)
		t.SetScrollbackLines(*scroll)
		t.Write(filler[:*fill])
		terms = append(terms, t)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	fmt.Printf("native instances=%d cols=%d rows=%d scrollback=%d fill=%d\n", *count, *cols, *rows, *scroll, *fill)
	fmt.Printf("  go_heap_delta_bytes=%d\n", int64(after.HeapAlloc)-int64(before.HeapAlloc))
	fmt.Printf("  go_total_alloc_delta_bytes=%d\n", int64(after.TotalAlloc)-int64(before.TotalAlloc))
	fmt.Printf("  rss_bytes=%d\n", rssBytes())
	return nil
}

// runScroll is the wasm driver's scroll mode with the same cases, so the one
// scrollback disagreement can be re-measured with a driver of the same shape
// on both sides.
func runScroll(args []string) {
	fs := flag.NewFlagSet("scroll", flag.ContinueOnError)
	capsCSV := fs.String("caps", "-1,10,100,1000,10000", "comma-separated scrollback caps to sweep")
	if err := fs.Parse(args); err != nil {
		return
	}
	for _, cap := range parseInts(*capsCSV) {
		t := New(80, 24)
		if cap >= 0 {
			t.SetScrollbackLines(cap)
		}
		t.Write([]byte("A"))
		t.Write([]byte("\x1b[1000000b"))
		cx, cy := t.Cursor()
		fmt.Printf("cap=%-6d scrollback_rows=%-5d cursor=%d,%d\n", cap, t.ScrollbackRows(), cx, cy)
		t.Free()
	}
	for _, n := range []int{1000, 10000, 65535, 65536} {
		t := New(80, 24)
		t.Write([]byte("A"))
		t.Write([]byte(fmt.Sprintf("\x1b[%db", n)))
		fmt.Printf("rep=%-6d scrollback_rows=%-5d\n", n, t.ScrollbackRows())
		t.Free()
	}
}

func parseInts(csv string) []int {
	var out []int
	for _, f := range strings.Split(csv, ",") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(f), "%d", &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: nativecmp corpus|throughput|instances [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "corpus":
		err = runCorpus(os.Args[2:])
	case "throughput":
		err = runThroughput(os.Args[2:])
	case "instances":
		err = runInstances(os.Args[2:])
	case "scroll":
		runScroll(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nativecmp:", err)
		os.Exit(1)
	}
}

func rssBytes() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return -1
	}
	var size, resident int64
	if _, err := fmt.Sscanf(string(b), "%d %d", &size, &resident); err != nil {
		return -1
	}
	return resident * int64(os.Getpagesize())
}
