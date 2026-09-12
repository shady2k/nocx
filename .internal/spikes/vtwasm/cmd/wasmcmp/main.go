// Command wasmcmp is the wasm side of the comparison. Its three subcommands
// mirror nativecmp's one for one — same corpus, same chunk sizes, same
// instance counts, same JSON schema — so the two drivers' outputs can be
// diffed rather than described.
//
//	wasmcmp corpus      -dir ../emulator/corpus        > results/corpus-wasm.jsonl
//	wasmcmp throughput  -capture bash -json            > results/throughput-wasm.jsonl
//	wasmcmp instances   -n 50
package main

import (
	"context"
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

	"nocx.internal/spikes/vtwasm/internal/corpus"
	"nocx.internal/spikes/vtwasm/internal/vt"
)

var ctx = context.Background()

// grid adapts one wasm terminal to corpus.Grid.
type grid struct {
	v *vt.VT
	c *context.Context
}

func (g grid) Cols() int { return g.v.Cols(*g.c) }
func (g grid) Rows() int { return g.v.Rows(*g.c) }
func (g grid) Cell(x, y int) (string, int, bool) {
	cell := g.v.Cell(*g.c, x, y)
	return cell.Grapheme, cell.Width, cell.HasText
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wasmcmp corpus|throughput|instances [flags]")
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
		err = runScroll(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wasmcmp:", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- corpus

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
	wasm := fs.String("wasm", ".build/vt.wasm", "wasm module")
	bytewiseMax := fs.Int("bytewise-max", 65536, "replay bytewise when the capture is at most this many bytes; 0 disables")
	if err := fs.Parse(args); err != nil {
		return err
	}
	caps, err := corpus.LoadAll(*dir)
	if err != nil {
		return err
	}
	v, err := vt.Open(ctx, *wasm)
	if err != nil {
		return err
	}
	defer v.Close(ctx)

	enc := json.NewEncoder(os.Stdout)
	for _, c := range caps {
		shapes := []struct {
			name string
			offs []int
		}{{"whole", c.Partitions(1, false)}, {"recorded", c.Offsets}}
		for _, p := range []int{2, 3, 5, 8, 16, 32} {
			shapes = append(shapes, struct {
				name string
				offs []int
			}{fmt.Sprintf("parts%d", p), c.Partitions(p, false)})
		}
		if *bytewiseMax > 0 && len(c.Stream) <= *bytewiseMax {
			shapes = append(shapes, struct {
				name string
				offs []int
			}{"bytewise", c.Partitions(0, true)})
		}
		for _, s := range shapes {
			if r := v.New(ctx, c.Cols, c.Rows); r != 0 {
				return fmt.Errorf("%s: vt_new = %d", c.Name, r)
			}
			start := 0
			for _, end := range s.offs {
				if end > len(c.Stream) {
					end = len(c.Stream)
				}
				if end > start {
					v.Write(ctx, c.Stream[start:end])
				}
				start = end
			}
			if start < len(c.Stream) {
				v.Write(ctx, c.Stream[start:])
			}
			rows := corpus.Render(grid{v: v, c: &ctx})
			sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
			if err := enc.Encode(replayResult{
				Capture:   c.Name,
				Partition: s.name,
				Cols:      v.Cols(ctx),
				Rows:      v.Rows(ctx),
				Zoomed:    v.ScrollbackRows(ctx),
				Digest:    hex.EncodeToString(sum[:]),
				Grid:      rows,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ------------------------------------------------------------ throughput

func runThroughput(args []string) error {
	fs := flag.NewFlagSet("throughput", flag.ContinueOnError)
	dir := fs.String("dir", "../emulator/corpus", "corpus directory")
	captureName := fs.String("capture", "bash", "capture name")
	wasm := fs.String("wasm", ".build/vt.wasm", "wasm module")
	reps := fs.Int("reps", 3, "repetitions; the median is reported")
	xfeed := fs.Int("xfeed", 1, "times the capture is re-fed inside one timed run (a longer continuous stream)")
	jsonOut := fs.Bool("json", false, "emit one JSON line per measurement")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := corpus.Load(*dir + "/" + *captureName + ".jsonl")
	if err != nil {
		return err
	}
	m, err := vt.Compile(ctx, *wasm)
	if err != nil {
		return err
	}
	defer m.Close(ctx)
	v, err := m.Instantiate(ctx)
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
			if r := v.New(ctx, c.Cols, c.Rows); r != 0 {
				return fmt.Errorf("vt_new = %d", r)
			}
			calls = 0
			start := time.Now()
			for range *xfeed {
				for off := 0; off < len(c.Stream); off += ch {
					end := off + ch
					if end > len(c.Stream) {
						end = len(c.Stream)
					}
					v.Write(ctx, c.Stream[off:end])
					calls++
				}
			}
			samples = append(samples, time.Since(start).Nanoseconds())
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
			}{"wasm", r})
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
	wasm := fs.String("wasm", ".build/vt.wasm", "wasm module")
	count := fs.Int("n", 10, "number of live instances")
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
	rssBefore := rssBytes()

	m, err := vt.Compile(ctx, *wasm)
	if err != nil {
		return err
	}
	defer m.Close(ctx)
	runtime.GC()
	rssCompiled := rssBytes()

	const unit = "the quick brown fox jumps over the lazy dog 0123456789\r\n"
	filler := strings.Repeat(unit, (*fill+len(unit)-1)/len(unit))
	instances := make([]*vt.VT, 0, *count)
	var linearBytes uint32
	for range *count {
		v, err := m.Instantiate(ctx)
		if err != nil {
			return err
		}
		if r := v.New(ctx, *cols, *rows); r != 0 {
			return fmt.Errorf("vt_new = %d", r)
		}
		v.SetScrollbackLines(ctx, *scroll)
		v.Write(ctx, []byte(filler[:*fill]))
		instances = append(instances, v)
		linearBytes += v.MemorySize()
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	rssAfter := rssBytes()

	fmt.Printf("wasm instances=%d cols=%d rows=%d scrollback=%d fill=%d\n", *count, *cols, *rows, *scroll, *fill)
	fmt.Printf("  rss_before_bytes=%d\n", rssBefore)
	fmt.Printf("  instance_linear_bytes_total=%d\n", linearBytes)
	fmt.Printf("  instance_linear_bytes_each=%.0f\n", float64(linearBytes)/float64(*count))
	fmt.Printf("  rss_after_compile_bytes=%d\n", rssCompiled)
	fmt.Printf("  rss_after_instances_bytes=%d\n", rssAfter)
	fmt.Printf("  rss_delta_bytes=%d\n", rssAfter-rssCompiled)
	fmt.Printf("  rss_per_instance_bytes=%.0f\n", float64(rssAfter-rssCompiled)/float64(*count))
	fmt.Printf("  go_heap_delta_bytes=%d\n", int64(after.HeapAlloc)-int64(before.HeapAlloc))
	fmt.Printf("  liveness=%d\n", len(instances))
	_ = instances
	return nil
}

// runScroll probes one specific disagreement between the two targets: the
// scrollback row count after a clamped REP with a capped scrollback. Every
// other REP case agrees; this one did not, so it gets its own mode rather than
// living in a paragraph.
func runScroll(args []string) error {
	fs := flag.NewFlagSet("scroll", flag.ContinueOnError)
	wasm := fs.String("wasm", ".build/vt.wasm", "wasm module")
	capsCSV := fs.String("caps", "-1,10,100,1000,10000", "comma-separated scrollback caps to sweep")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := vt.Compile(ctx, *wasm)
	if err != nil {
		return err
	}
	defer m.Close(ctx)
	v, err := m.Instantiate(ctx)
	if err != nil {
		return err
	}
	for _, cap := range parseInts(*capsCSV) {
		v.New(ctx, 80, 24)
		if cap >= 0 {
			v.SetScrollbackLines(ctx, cap)
		}
		v.Write(ctx, []byte("A"))
		v.Write(ctx, []byte("\x1b[1000000b"))
		cx, cy := v.Cursor(ctx)
		fmt.Printf("cap=%-6d scrollback_rows=%-5d cursor=%d,%d linear=%d\n",
			cap, v.ScrollbackRows(ctx), cx, cy, v.MemorySize())
	}
	for _, n := range []int{1000, 10000, 65535, 65536} {
		v.New(ctx, 80, 24)
		v.Write(ctx, []byte("A"))
		v.Write(ctx, []byte(fmt.Sprintf("\x1b[%db", n)))
		fmt.Printf("rep=%-6d scrollback_rows=%-5d\n", n, v.ScrollbackRows(ctx))
	}
	return nil
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
