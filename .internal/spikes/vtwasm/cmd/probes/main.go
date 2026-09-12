// Command probes runs the qualification spike's nine behavioural probes plus
// its API section against the wasm build, and writes one JSON observation per
// line in the same schema as .internal/spikes/emulator/results/ghostty.jsonl.
//
// The point is not to re-measure the library — it is the same ghostty commit
// either way. It is to answer whether the SAME answers survive the flat wasm
// ABI and the freestanding target: every difference between this file and the
// committed native one is a finding about the shim or the target.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"nocx.internal/spikes/vtwasm/internal/obs"
	"nocx.internal/spikes/vtwasm/internal/vt"
)

var (
	wasmPath = flag.String("wasm", ".build/vt.wasm", "path to the wasm module")
	ctx      = context.Background()
)

// widthOf maps the library's width vocabulary to the column footprint a
// renderer must advance by — the same mapping the native driver emits.
func widthOf(w int) int {
	switch w {
	case vt.WidthWide:
		return 2
	case vt.WidthSpacerTail, vt.WidthSpacerHead:
		return 0
	default:
		return 1
	}
}

// cellRow renders row y as one token per column: %q of the cell's grapheme and
// its width, or "." for a cell holding no text.
func cellRow(v *vt.VT, y int) string {
	var sb strings.Builder
	for x := range v.Cols(ctx) {
		c := v.Cell(ctx, x, y)
		if !c.HasText || c.Grapheme == "" {
			sb.WriteString(". ")
			continue
		}
		fmt.Fprintf(&sb, "%q/%d ", c.Grapheme, widthOf(c.Width))
	}
	return strings.TrimRight(sb.String(), " ")
}

func rowText(v *vt.VT, y int) string {
	var sb strings.Builder
	for x := range v.Cols(ctx) {
		c := v.Cell(ctx, x, y)
		if !c.HasText || c.Grapheme == "" {
			sb.WriteByte(' ')
			continue
		}
		sb.WriteString(c.Grapheme)
	}
	return strings.TrimRight(sb.String(), " ")
}

func screen(v *vt.VT) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for y := range v.Rows(ctx) {
		if y > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%q", rowText(v, y))
	}
	sb.WriteByte(']')
	return sb.String()
}

// open creates a terminal and fails loudly rather than probing a stub.
func open(v *vt.VT, cols, rows int) {
	if r := v.New(ctx, cols, rows); r != 0 {
		panic(fmt.Sprintf("vt_new(%d,%d) = %d", cols, rows, r))
	}
}

func main() {
	flag.Parse()
	mod, err := vt.Compile(ctx, *wasmPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probes:", err)
		os.Exit(1)
	}
	defer mod.Close(ctx)
	v, err := mod.Instantiate(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probes:", err)
		os.Exit(1)
	}
	defer v.Close(ctx)

	p1(v)
	p2(v)
	p3(v)
	p4(v)
	p5(v)
	p6(v)
	p7(mod)
	p8(v)
	p9(v)
	p11(v)
	api(v)
}

func p1(v *vt.VT) {
	const p = "1_grapheme_split"
	cases := []struct{ name, s string }{
		{"zwj_family", "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466"},
		{"skin_tone", "\U0001F44D\U0001F3FD"},
		{"flag_ru", "\U0001F1F7\U0001F1FA"},
		{"heart_vs16", "\u2764\uFE0F"},
	}
	for _, c := range cases {
		b := []byte(c.s)

		open(v, 20, 3)
		v.Write(ctx, b)
		whole := cellRow(v, 0)
		wx, wy := v.Cursor(ctx)
		obs.Emit(p, c.name+"/whole", "bytes", len(b))
		obs.Emit(p, c.name+"/whole", "row0", whole)
		obs.Emit(p, c.name+"/whole", "cursor", fmt.Sprintf("%d,%d", wx, wy))

		ok, firstBad := 0, -1
		for k := 1; k < len(b); k++ {
			open(v, 20, 3)
			v.Write(ctx, b[:k])
			v.Write(ctx, b[k:])
			row := cellRow(v, 0)
			cx, cy := v.Cursor(ctx)
			match := row == whole && cx == wx && cy == wy
			if match {
				ok++
			} else if firstBad < 0 {
				firstBad = k
			}
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "row0", row)
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "cursor", fmt.Sprintf("%d,%d", cx, cy))
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "matches_whole", match)
		}
		obs.Emit(p, c.name+"/summary", "offsets_identical", fmt.Sprintf("%d/%d", ok, len(b)-1))
		obs.Emit(p, c.name+"/summary", "first_divergent_offset", firstBad)
	}
}

func occupied(v *vt.VT, y int) int {
	n := 0
	for x := range v.Cols(ctx) {
		if c := v.Cell(ctx, x, y); c.HasText && c.Grapheme != "" {
			n++
		}
	}
	return n
}

func p2(v *vt.VT) {
	const p = "2_combining_mark"
	open(v, 20, 3)
	v.Write(ctx, []byte("a\u0301"))
	obs.Emit(p, "one_write", "row0", cellRow(v, 0))
	obs.Emit(p, "one_write", "cells_occupied", occupied(v, 0))
	cx, _ := v.Cursor(ctx)
	obs.Emit(p, "one_write", "cursor_x", cx)

	open(v, 20, 3)
	v.Write(ctx, []byte("a"))
	v.Write(ctx, []byte("\u0301"))
	obs.Emit(p, "split_write", "row0", cellRow(v, 0))
	obs.Emit(p, "split_write", "cells_occupied", occupied(v, 0))
	cx2, _ := v.Cursor(ctx)
	obs.Emit(p, "split_write", "cursor_x", cx2)
}

func p3(v *vt.VT) {
	const p = "3_modified_keys"
	cases := []struct {
		name string
		key  int
		mods int
	}{
		{"ctrl_left", v.KeyArrowLeft(ctx), v.ModCtrl(ctx)},
		{"shift_up", v.KeyArrowUp(ctx), v.ModShift(ctx)},
		{"f5_shift", v.KeyF5(ctx), v.ModShift(ctx)},
		{"f5_plain", v.KeyF5(ctx), 0},
		{"f5_ctrl", v.KeyF5(ctx), v.ModCtrl(ctx)},
		{"ctrl_shift_left", v.KeyArrowLeft(ctx), v.ModCtrl(ctx) | v.ModShift(ctx)},
		{"left_plain", v.KeyArrowLeft(ctx), 0},
	}
	for _, c := range cases {
		out := v.KeyEncode(ctx, c.key, c.mods, 0, false)
		obs.Emit(p, c.name, "len", len(out))
		obs.Emit(p, c.name, "hex", obs.Hex(out))
		obs.Emit(p, c.name, "esc", obs.Esc(out))
	}
	for _, c := range cases {
		out := v.KeyEncode(ctx, c.key, c.mods, v.KittyKeyAll(ctx), false)
		obs.Emit(p, c.name+"/kitty", "esc", obs.Esc(out))
	}
}

func p4(v *vt.VT) {
	const p = "4_function_keys"
	cases := []struct {
		name string
		key  int
		mods int
	}{
		{"f13", v.KeyF13(ctx), 0},
		{"f13_shift", v.KeyF13(ctx), v.ModShift(ctx)},
		{"f12", v.KeyF12(ctx), 0},
		{"f1", v.KeyF1(ctx), 0},
	}
	for _, c := range cases {
		out := v.KeyEncode(ctx, c.key, c.mods, 0, false)
		obs.Emit(p, c.name, "len", len(out))
		obs.Emit(p, c.name, "hex", obs.Hex(out))
		obs.Emit(p, c.name, "esc", obs.Esc(out))
		obs.Emit(p, c.name, "contains_u+fffd", strings.Contains(string(out), "\uFFFD"))
	}
}

func p5(v *vt.VT) {
	const p = "5_insert_mode"
	open(v, 20, 3)
	v.Write(ctx, []byte("abcd"))
	v.Write(ctx, []byte("\x1b[1;1H"))
	obs.Emit(p, "before_irm", "row0", cellRow(v, 0))

	v.Write(ctx, []byte("\x1b[4h"))
	set, ok := v.Mode(ctx, 4, true)
	obs.Emit(p, "after_set_irm", "mode_query_ok", ok)
	obs.Emit(p, "after_set_irm", "mode_4_set", set)

	v.Write(ctx, []byte("XY"))
	obs.Emit(p, "after_print", "row0", cellRow(v, 0))
	obs.Emit(p, "after_print", "row_text", rowText(v, 0))
	cx, _ := v.Cursor(ctx)
	obs.Emit(p, "after_print", "cursor_x", cx)

	v.Write(ctx, []byte("\x1b[4$p"))
	rep := v.Reply(ctx)
	obs.Emit(p, "decrqm", "esc", obs.Esc(rep))
	obs.Emit(p, "decrqm", "hex", obs.Hex(rep))
}

func parseCPR(b []byte) (int, int) {
	s := string(b)
	i := strings.Index(s, "\x1b[")
	if i < 0 {
		return -1, -1
	}
	s = s[i+2:]
	j := strings.IndexByte(s, 'R')
	if j < 0 {
		return -1, -1
	}
	var row, col int
	if _, err := fmt.Sscanf(s[:j], "%d;%d", &row, &col); err != nil {
		return -1, -1
	}
	return row, col
}

func p6(v *vt.VT) {
	const p = "6_cursor_report_origin_mode"
	open(v, 80, 24)
	v.Write(ctx, []byte("\x1b[5;10r"))
	v.Write(ctx, []byte("\x1b[1;1H"))
	x, y := v.Cursor(ctx)
	obs.Emit(p, "decom_off", "api_cursor", fmt.Sprintf("%d,%d", x, y))
	v.Write(ctx, []byte("\x1b[6n"))
	obs.Emit(p, "decom_off", "cpr_esc", obs.Esc(v.Reply(ctx)))

	open(v, 80, 24)
	v.Write(ctx, []byte("\x1b[5;10r"))
	v.Write(ctx, []byte("\x1b[?6h"))
	v.Write(ctx, []byte("\x1b[1;1H"))
	x2, y2 := v.Cursor(ctx)
	obs.Emit(p, "decom_on", "api_cursor", fmt.Sprintf("%d,%d", x2, y2))
	v.Write(ctx, []byte("\x1b[6n"))
	rep := v.Reply(ctx)
	obs.Emit(p, "decom_on", "cpr_esc", obs.Esc(rep))
	obs.Emit(p, "decom_on", "cpr_hex", obs.Hex(rep))
	row, col := parseCPR(rep)
	obs.Emit(p, "decom_on", "cpr_row", row)
	obs.Emit(p, "decom_on", "cpr_col", col)

	open(v, 80, 24)
	v.Write(ctx, []byte("\x1b[5;10r"))
	v.Write(ctx, []byte("\x1b[5;1H"))
	x3, y3 := v.Cursor(ctx)
	obs.Emit(p, "decom_off_at_row5", "api_cursor", fmt.Sprintf("%d,%d", x3, y3))
	v.Write(ctx, []byte("\x1b[6n"))
	obs.Emit(p, "decom_off_at_row5", "cpr_esc", obs.Esc(v.Reply(ctx)))
}

func p7(mod *vt.Module) {
	const p = "7_bounded_rep"
	for _, c := range []struct {
		name   string
		n      int
		budget time.Duration
	}{
		{"rep_100000", 100000, 0},
		{"rep_1000000", 1000000, 0},
		{"rep_10000000", 10000000, 0},
		{"rep_1000000000_budget30s", 1000000000, 30 * time.Second},
		{"rep_1000000_scrollback100", 1000000, 0},
		{"rep_65535", 65535, 0},
		{"rep_70000", 70000, 0},
	} {
		runREP(mod, p, c.name, c.n, c.budget)
	}
}

// runREP measures one REP case on an instance of its own.
//
// The instance is disposable because of what the budget is for: a pathological
// implementation must not be able to hang the run. On expiry this closes the
// instance — which is how wazero interrupts a call in flight — and then waits a
// bounded grace period for the writer to come back. If it does, the case is
// recorded as incomplete and the run continues on a fresh instance; if it does
// not, the driver stops, because continuing would mean letting a case that may
// still be writing share memory with the ones after it. The wait is never
// unbounded either way.
func runREP(mod *vt.Module, p, name string, n int, budget time.Duration) {
	v, err := mod.Instantiate(ctx)
	if err != nil {
		panic(fmt.Sprintf("instantiate for %s: %v", name, err))
	}
	defer v.Close(ctx)

	open(v, 80, 24)
	if name == "rep_1000000_scrollback100" {
		v.SetScrollbackLines(ctx, 100)
	}
	v.Write(ctx, []byte("A"))

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	memBefore := v.MemorySize()

	// The writer reports the call's own error as well as its duration, so an
	// interrupted call is a value rather than a panic out of a goroutine.
	type outcome struct {
		took time.Duration
		err  error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		err := v.WriteErr(ctx, []byte("\x1b["+itoa(n)+"b"))
		done <- outcome{time.Since(start), err}
	}()

	var elapsed time.Duration
	if budget > 0 {
		select {
		case o := <-done:
			elapsed = o.took
		case <-time.After(budget):
			// The instance is closed here, so this is the last moment at which
			// anything can be read out of it. Emit what is known — the count,
			// the elapsed time and the fact that it did not finish — and
			// return: reading memory or scrollback now would be a read of a
			// closed module, and the case's terminal state is exactly what the
			// budget exists to stop waiting for.
			elapsed = time.Since(start)
			_ = v.Close(ctx)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				obs.Emit(p, name, "interrupt_failed", true)
				fmt.Fprintf(os.Stderr,
					"probes: %s: closed the instance but the writer had not returned 10s later; stopping so no later case shares this terminal\n", name)
				os.Exit(3)
			}
			obs.Emit(p, name, "rep_count", n)
			obs.Emit(p, name, "completed", false)
			obs.Emit(p, name, "budget_expired", true)
			obs.Emit(p, name, "wall_ns", elapsed.Nanoseconds())
			obs.Emit(p, name, "wall_ms", fmt.Sprintf("%.1f", float64(elapsed.Nanoseconds())/1e6))
			obs.Emit(p, name, "state_read", false)
			return
		}
	} else {
		o := <-done
		elapsed = o.took
	}
	memAfter := v.MemorySize()
	runtime.ReadMemStats(&m1)

	obs.Emit(p, name, "rep_count", n)
	obs.Emit(p, name, "completed", true)
	obs.Emit(p, name, "wall_ns", elapsed.Nanoseconds())
	obs.Emit(p, name, "wall_ms", fmt.Sprintf("%.1f", float64(elapsed.Nanoseconds())/1e6))
	obs.Emit(p, name, "total_alloc_bytes", m1.TotalAlloc-m0.TotalAlloc)
	obs.Emit(p, name, "heap_alloc_before_bytes", m0.HeapAlloc)
	obs.Emit(p, name, "heap_alloc_after_bytes", m1.HeapAlloc)
	obs.Emit(p, name, "mallocs", m1.Mallocs-m0.Mallocs)
	// wasm-specific: the module's own linear memory is the number a per-session
	// instance pays, and only the host can see it.
	obs.Emit(p, name, "wasm_mem_before_bytes", memBefore)
	obs.Emit(p, name, "wasm_mem_after_bytes", memAfter)
	obs.Emit(p, name, "scrollback_rows", v.ScrollbackRows(ctx))
	cx, cy := v.Cursor(ctx)
	obs.Emit(p, name, "cursor", fmt.Sprintf("%d,%d", cx, cy))
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func p8(v *vt.VT) {
	const p = "8_resize"
	open(v, 20, 24)
	for i := 1; i <= 24; i++ {
		line := fmt.Sprintf("L%02d", i)
		if i < 24 {
			line += "\r\n"
		}
		v.Write(ctx, []byte(line))
	}
	cx, cy := v.Cursor(ctx)
	obs.Emit(p, "shrink_height/before", "cursor", fmt.Sprintf("%d,%d", cx, cy))
	obs.Emit(p, "shrink_height/before", "rows", screen(v))
	v.Resize(ctx, 20, 10)
	cx2, cy2 := v.Cursor(ctx)
	obs.Emit(p, "shrink_height/after", "cursor", fmt.Sprintf("%d,%d", cx2, cy2))
	obs.Emit(p, "shrink_height/after", "height", v.Rows(ctx))
	obs.Emit(p, "shrink_height/after", "rows", screen(v))
	obs.Emit(p, "shrink_height/after", "scrollback_rows", v.ScrollbackRows(ctx))

	open(v, 20, 10)
	v.Write(ctx, []byte("0123456789"+"0123456789"+"0123456789"+"0123456789"+"ABCDE"))
	cx3, cy3 := v.Cursor(ctx)
	obs.Emit(p, "reflow_width/before", "width", v.Cols(ctx))
	obs.Emit(p, "reflow_width/before", "cursor", fmt.Sprintf("%d,%d", cx3, cy3))
	obs.Emit(p, "reflow_width/before", "rows", screen(v))
	v.Resize(ctx, 10, 10)
	cx4, cy4 := v.Cursor(ctx)
	obs.Emit(p, "reflow_width/after", "width", v.Cols(ctx))
	obs.Emit(p, "reflow_width/after", "cursor", fmt.Sprintf("%d,%d", cx4, cy4))
	obs.Emit(p, "reflow_width/after", "rows", screen(v))

	open(v, 20, 5)
	v.Write(ctx, []byte("hello\r\nworld"))
	v.Resize(ctx, 20, 10)
	obs.Emit(p, "grow_height/after", "rows", screen(v))
}

func p9(v *vt.VT) {
	const p = "9_c1_0x9c_in_osc"
	cases := []struct {
		name string
		seq  string
	}{
		{"title_bel", "\x1b]0;X\u2733Y\a"},
		{"title_st", "\x1b]0;X\u2733Y\x1b\\"},
		{"title_bare_9c", "\x1b]0;X\x9cY\a"},
		{"title_ascii", "\x1b]0;X-Y\a"},
	}
	for _, c := range cases {
		open(v, 40, 3)
		v.Write(ctx, []byte(c.seq))
		v.Write(ctx, []byte("Z"))
		title := v.Title(ctx)
		obs.Emit(p, c.name, "title_events", fmt.Sprintf("%q", []string{title}))
		obs.Emit(p, c.name, "title_hex", fmt.Sprintf("%x", title))
		obs.Emit(p, c.name, "screen_row0", fmt.Sprintf("%q", rowText(v, 0)))
		cx, _ := v.Cursor(ctx)
		obs.Emit(p, c.name, "cursor_x", cx)
	}

	open(v, 40, 3)
	v.Write(ctx, []byte("\x1b]0;T\x07"))
	v.Write(ctx, []byte("\x1b]7;file://h/tmp\x07"))
	v.Write(ctx, []byte("\a"))
	v.Write(ctx, []byte("\x1b]52;c;SGVsbG8=\x1b\\"))
	v.Write(ctx, []byte("\x1b]8;;https://example.com\x1b\\L\x1b]8;;\x1b\\"))
	obs.Emit(p, "effects", "bells", v.BellCount(ctx))
	obs.Emit(p, "effects", "titles", fmt.Sprintf("%q", []string{v.Title(ctx)}))
	obs.Emit(p, "effects", "pwds", fmt.Sprintf("%q", []string{v.Pwd(ctx)}))
	obs.Emit(p, "effects", "clipboard_writes", fmt.Sprint(v.ClipboardWrites(ctx)))
	obs.Emit(p, "effects", "title_after", v.Title(ctx))
	obs.Emit(p, "effects", "pwd_after", v.Pwd(ctx))
}

func p11(v *vt.VT) {
	const p = "11_graphics"
	sixel := "\x1bPq\"1;1;2;2#0;2;0;0;0#0~~\x1b\\"
	kitty := "\x1b_Ga=T,f=24,s=1,v=1,i=42;AAAA\x1b\\"

	open(v, 20, 3)
	v.ClearEffects(ctx)
	before := screen(v)
	v.Write(ctx, []byte(sixel))
	obs.Emit(p, "sixel", "unknown_sequence_tags", fmt.Sprint(v.UnknownTags(ctx)))
	obs.Emit(p, "sixel", "screen_unchanged", screen(v) == before)
	cx, cy := v.Cursor(ctx)
	obs.Emit(p, "sixel", "cursor", fmt.Sprintf("%d,%d", cx, cy))

	v.Write(ctx, []byte(kitty))
	obs.Emit(p, "kitty", "unknown_sequence_tags", fmt.Sprint(v.UnknownTags(ctx)))
	obs.Emit(p, "kitty", "screen_unchanged", screen(v) == before)
	obs.Emit(p, "kitty", "kitty_graphics_compiled", v.BuildInfo(ctx, vt.BuildInfoKittyGraphics) == 1)
	obs.Emit(p, "kitty", "storage_present", v.KittyGraphics(ctx) == 1)
	obs.Emit(p, "kitty", "image_42_decoded", v.KittyImagePresent(ctx, 42) == 1)
	obs.Emit(p, "kitty", "reply_esc", obs.Esc(v.Reply(ctx)))

	v.Write(ctx, []byte("\x1b_private-command;payload\x1b\\"))
	obs.Emit(p, "control_unknown_apc", "unknown_sequence_tags", fmt.Sprint(v.UnknownTags(ctx)))
	obs.Emit(p, "control_unknown_apc", "screen_unchanged", screen(v) == before)
}

func api(v *vt.VT) {
	const p = "api"

	open(v, 20, 3)
	v.Write(ctx, []byte("hello\r\nworld"))
	obs.Emit(p, "incremental", "rs_new", v.RSOpen(ctx))
	v.RSUpdate(ctx)
	obs.Emit(p, "incremental", "dirty_first_update", v.RSDirty(ctx))
	obs.Emit(p, "incremental", "dirty_rows_first_update", fmt.Sprint(v.RSDirtyRows(ctx)))
	v.RSClean(ctx)
	v.RSUpdate(ctx)
	obs.Emit(p, "incremental", "dirty_after_clean", v.RSDirty(ctx))
	obs.Emit(p, "incremental", "dirty_rows_after_clean", fmt.Sprint(v.RSDirtyRows(ctx)))
	v.Write(ctx, []byte("\r\nthird"))
	v.RSUpdate(ctx)
	obs.Emit(p, "incremental", "dirty_after_one_line", v.RSDirty(ctx))
	obs.Emit(p, "incremental", "dirty_rows_after_one_line", fmt.Sprint(v.RSDirtyRows(ctx)))
	v.RSClean(ctx)
	v.RSFree(ctx)

	obs.Emit(p, "graphics", "kitty_graphics_compiled", v.BuildInfo(ctx, vt.BuildInfoKittyGraphics) == 1)
	obs.Emit(p, "graphics", "tmux_control_mode_compiled", v.BuildInfo(ctx, vt.BuildInfoTmuxControl) == 1)

	open(v, 10, 4)
	v.Write(ctx, []byte("0123456789ABCDEFGHIJ"))
	w0, c0 := v.RowWrap(ctx, 0)
	w1, c1 := v.RowWrap(ctx, 1)
	w2, c2 := v.RowWrap(ctx, 2)
	obs.Emit(p, "softwrap", "row0", fmt.Sprintf("%q", rowText(v, 0)))
	obs.Emit(p, "softwrap", "row1", fmt.Sprintf("%q", rowText(v, 1)))
	obs.Emit(p, "softwrap", "row0_wrap", w0)
	obs.Emit(p, "softwrap", "row0_continuation", c0)
	obs.Emit(p, "softwrap", "row1_wrap", w1)
	obs.Emit(p, "softwrap", "row1_continuation", c1)
	obs.Emit(p, "softwrap", "row2_wrap", w2)
	obs.Emit(p, "softwrap", "row2_continuation", c2)
	cx, cy := v.Cursor(ctx)
	obs.Emit(p, "softwrap", "cursor", fmt.Sprintf("%d,%d", cx, cy))

	v.Resize(ctx, 20, 4)
	obs.Emit(p, "softwrap", "after_grow_row0", fmt.Sprintf("%q", rowText(v, 0)))
	obs.Emit(p, "softwrap", "after_grow_row1", fmt.Sprintf("%q", rowText(v, 1)))
	w0b, c0b := v.RowWrap(ctx, 0)
	w1b, c1b := v.RowWrap(ctx, 1)
	obs.Emit(p, "softwrap", "after_grow_row0_wrap", w0b)
	obs.Emit(p, "softwrap", "after_grow_row0_continuation", c0b)
	obs.Emit(p, "softwrap", "after_grow_row1_wrap", w1b)
	obs.Emit(p, "softwrap", "after_grow_row1_continuation", c1b)
}
