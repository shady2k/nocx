// Command ghosttyvt runs the same emulator qualification probes as cmd/xvt,
// against libghostty-vt through the throwaway CGo binding, and writes one JSON
// observation per line to stdout in the identical schema.
package main

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"time"

	"nocx.internal/spikes/emulator/ghostty"
	"nocx.internal/spikes/emulator/obs"
)

// bitmap to the width vocabulary the x/vt driver also emits: the column
// footprint a renderer must advance by.
func widthOf(w ghostty.CellWidth) int {
	switch w {
	case ghostty.WidthWide:
		return 2
	case ghostty.WidthSpacerTail, ghostty.WidthSpacerHead:
		return 0
	default:
		return 1
	}
}

// cellRow renders row y as one token per column: %q of the cell's grapheme and
// its width, or "." for a cell with no text.
func cellRow(t *ghostty.Terminal, y int) string {
	var sb strings.Builder
	for x := range t.Cols() {
		content, w, has := t.Cell(x, y)
		if !has || content == "" {
			sb.WriteString(". ")
			continue
		}
		fmt.Fprintf(&sb, "%q/%d ", content, widthOf(w))
	}
	return strings.TrimRight(sb.String(), " ")
}

func rowText(t *ghostty.Terminal, y int) string {
	var sb strings.Builder
	for x := range t.Cols() {
		content, _, has := t.Cell(x, y)
		if !has || content == "" {
			sb.WriteByte(' ')
			continue
		}
		sb.WriteString(content)
	}
	return strings.TrimRight(sb.String(), " ")
}

func screen(t *ghostty.Terminal) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for y := range t.Rows() {
		if y > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%q", rowText(t, y))
	}
	sb.WriteByte(']')
	return sb.String()
}

func main() {
	p1()
	p2()
	p3()
	p4()
	p5()
	p6()
	p7()
	p8()
	p9()
	api()
}

func p1() {
	const p = "1_grapheme_split"
	cases := []struct{ name, s string }{
		{"zwj_family", "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466"},
		{"skin_tone", "\U0001F44D\U0001F3FD"},
		{"flag_ru", "\U0001F1F7\U0001F1FA"},
		{"heart_vs16", "\u2764\uFE0F"},
	}
	for _, c := range cases {
		b := []byte(c.s)

		t := ghostty.New(20, 3)
		t.Write(b)
		whole := cellRow(t, 0)
		wx, wy := t.Cursor()
		obs.Emit(p, c.name+"/whole", "bytes", len(b))
		obs.Emit(p, c.name+"/whole", "row0", whole)
		obs.Emit(p, c.name+"/whole", "cursor", fmt.Sprintf("%d,%d", wx, wy))
		t.Free()

		ok, firstBad := 0, -1
		for k := 1; k < len(b); k++ {
			t2 := ghostty.New(20, 3)
			t2.Write(b[:k])
			t2.Write(b[k:])
			row := cellRow(t2, 0)
			cx, cy := t2.Cursor()
			match := row == whole && cx == wx && cy == wy
			if match {
				ok++
			} else if firstBad < 0 {
				firstBad = k
			}
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "row0", row)
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "cursor", fmt.Sprintf("%d,%d", cx, cy))
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "matches_whole", match)
			t2.Free()
		}
		obs.Emit(p, c.name+"/summary", "offsets_identical", fmt.Sprintf("%d/%d", ok, len(b)-1))
		obs.Emit(p, c.name+"/summary", "first_divergent_offset", firstBad)
	}
}

func p2() {
	const p = "2_combining_mark"
	t := ghostty.New(20, 3)
	t.Write([]byte("a\u0301"))
	obs.Emit(p, "one_write", "row0", cellRow(t, 0))
	obs.Emit(p, "one_write", "cells_occupied", occupied(t, 0))
	cx, _ := t.Cursor()
	obs.Emit(p, "one_write", "cursor_x", cx)
	t.Free()

	t2 := ghostty.New(20, 3)
	t2.Write([]byte("a"))
	t2.Write([]byte("\u0301"))
	obs.Emit(p, "split_write", "row0", cellRow(t2, 0))
	obs.Emit(p, "split_write", "cells_occupied", occupied(t2, 0))
	cx2, _ := t2.Cursor()
	obs.Emit(p, "split_write", "cursor_x", cx2)
	t2.Free()
}

func occupied(t *ghostty.Terminal, y int) int {
	n := 0
	for x := range t.Cols() {
		if content, _, has := t.Cell(x, y); has && content != "" {
			n++
		}
	}
	return n
}

func p3() {
	const p = "3_modified_keys"
	cases := []struct {
		name string
		key  ghostty.Key
		mods ghostty.Mods
	}{
		{"ctrl_left", ghostty.KeyLeft, ghostty.ModCtrl},
		{"shift_up", ghostty.KeyUp, ghostty.ModShift},
		{"f5_shift", ghostty.KeyF5, ghostty.ModShift},
		{"f5_plain", ghostty.KeyF5, 0},
		{"f5_ctrl", ghostty.KeyF5, ghostty.ModCtrl},
		{"ctrl_shift_left", ghostty.KeyLeft, ghostty.ModCtrl | ghostty.ModShift},
		{"left_plain", ghostty.KeyLeft, 0},
	}
	for _, c := range cases {
		out := ghostty.EncodeKey(c.key, c.mods, 0, nil)
		obs.Emit(p, c.name, "len", len(out))
		obs.Emit(p, c.name, "hex", obs.Hex(out))
		obs.Emit(p, c.name, "esc", obs.Esc(out))
	}
	// The same keys with the Kitty keyboard protocol enabled, for contrast:
	// the sequence a Kitty-aware program asks for.
	for _, c := range cases {
		out := ghostty.EncodeKey(c.key, c.mods, ghostty.KittyAll, nil)
		obs.Emit(p, c.name+"/kitty", "esc", obs.Esc(out))
	}
}

func p4() {
	const p = "4_function_keys"
	cases := []struct {
		name string
		key  ghostty.Key
		mods ghostty.Mods
	}{
		{"f13", ghostty.KeyF13, 0},
		{"f13_shift", ghostty.KeyF13, ghostty.ModShift},
		{"f12", ghostty.KeyF12, 0},
		{"f1", ghostty.KeyF1, 0},
	}
	for _, c := range cases {
		out := ghostty.EncodeKey(c.key, c.mods, 0, nil)
		obs.Emit(p, c.name, "len", len(out))
		obs.Emit(p, c.name, "hex", obs.Hex(out))
		obs.Emit(p, c.name, "esc", obs.Esc(out))
		obs.Emit(p, c.name, "contains_u+fffd", bytes.Contains(out, []byte("\uFFFD")))
	}
}

func p5() {
	const p = "5_insert_mode"
	t := ghostty.New(20, 3)
	defer t.Free()
	t.Write([]byte("abcd"))
	t.Write([]byte("\x1b[1;1H"))
	obs.Emit(p, "before_irm", "row0", cellRow(t, 0))

	t.Write([]byte("\x1b[4h")) // IRM on
	set, ok := t.Mode(4, true)
	obs.Emit(p, "after_set_irm", "mode_query_ok", ok)
	obs.Emit(p, "after_set_irm", "mode_4_set", set)

	t.Write([]byte("XY"))
	obs.Emit(p, "after_print", "row0", cellRow(t, 0))
	obs.Emit(p, "after_print", "row_text", rowText(t, 0))
	cx, _ := t.Cursor()
	obs.Emit(p, "after_print", "cursor_x", cx)

	t.Write([]byte("\x1b[4$p")) // DECRQM: query IRM
	rep := t.Reply()
	obs.Emit(p, "decrqm", "esc", obs.Esc(rep))
	obs.Emit(p, "decrqm", "hex", obs.Hex(rep))
}

func p6() {
	const p = "6_cursor_report_origin_mode"
	t := ghostty.New(80, 24)
	t.Write([]byte("\x1b[5;10r"))
	t.Write([]byte("\x1b[1;1H"))
	x, y := t.Cursor()
	obs.Emit(p, "decom_off", "api_cursor", fmt.Sprintf("%d,%d", x, y))
	t.Write([]byte("\x1b[6n"))
	obs.Emit(p, "decom_off", "cpr_esc", obs.Esc(t.Reply()))
	t.Free()

	t2 := ghostty.New(80, 24)
	t2.Write([]byte("\x1b[5;10r"))
	t2.Write([]byte("\x1b[?6h"))
	t2.Write([]byte("\x1b[1;1H"))
	x2, y2 := t2.Cursor()
	obs.Emit(p, "decom_on", "api_cursor", fmt.Sprintf("%d,%d", x2, y2))
	t2.Write([]byte("\x1b[6n"))
	rep := t2.Reply()
	obs.Emit(p, "decom_on", "cpr_esc", obs.Esc(rep))
	obs.Emit(p, "decom_on", "cpr_hex", obs.Hex(rep))
	row, col := parseCPR(rep)
	obs.Emit(p, "decom_on", "cpr_row", row)
	obs.Emit(p, "decom_on", "cpr_col", col)
	t2.Free()

	t3 := ghostty.New(80, 24)
	t3.Write([]byte("\x1b[5;10r"))
	t3.Write([]byte("\x1b[5;1H"))
	x3, y3 := t3.Cursor()
	obs.Emit(p, "decom_off_at_row5", "api_cursor", fmt.Sprintf("%d,%d", x3, y3))
	t3.Write([]byte("\x1b[6n"))
	obs.Emit(p, "decom_off_at_row5", "cpr_esc", obs.Esc(t3.Reply()))
	t3.Free()
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

func p7() {
	const p = "7_bounded_rep"
	for _, n := range []int{100000, 1000000, 10000000} {
		runREP(p, "rep_"+itoa(n), n, 0)
	}
	runREP(p, "rep_1000000000_budget30s", 1000000000, 30*time.Second)
	runREP(p, "rep_1000000_scrollback100", 1000000, 0)
	// The pair that shows whether REP is clamped: equal final state means the
	// count was capped, different state means it was honoured.
	runREP(p, "rep_65535", 65535, 0)
	runREP(p, "rep_70000", 70000, 0)
}

func runREP(p, name string, n int, budget time.Duration) {
	t := ghostty.New(80, 24)
	defer t.Free()
	if name == "rep_1000000_scrollback100" {
		t.SetScrollbackLines(100)
	}
	t.Write([]byte("A"))

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)

	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		t.Write([]byte("\x1b[" + itoa(n) + "b"))
		done <- time.Since(start)
	}()

	var elapsed time.Duration
	completed := true
	if budget > 0 {
		select {
		case elapsed = <-done:
		case <-time.After(budget):
			completed = false
			elapsed = time.Since(start)
		}
	} else {
		elapsed = <-done
	}

	runtime.ReadMemStats(&m1)
	obs.Emit(p, name, "rep_count", n)
	obs.Emit(p, name, "completed", completed)
	obs.Emit(p, name, "wall_ns", elapsed.Nanoseconds())
	obs.Emit(p, name, "wall_ms", fmt.Sprintf("%.1f", float64(elapsed.Nanoseconds())/1e6))
	obs.Emit(p, name, "total_alloc_bytes", m1.TotalAlloc-m0.TotalAlloc)
	obs.Emit(p, name, "heap_alloc_before_bytes", m0.HeapAlloc)
	obs.Emit(p, name, "heap_alloc_after_bytes", m1.HeapAlloc)
	obs.Emit(p, name, "mallocs", m1.Mallocs-m0.Mallocs)
	obs.Emit(p, name, "scrollback_rows", t.ScrollbackRows())
	if completed {
		cx, cy := t.Cursor()
		obs.Emit(p, name, "cursor", fmt.Sprintf("%d,%d", cx, cy))
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func p8() {
	const p = "8_resize"
	t := ghostty.New(20, 24)
	for i := 1; i <= 24; i++ {
		line := fmt.Sprintf("L%02d", i)
		if i < 24 {
			line += "\r\n"
		}
		t.Write([]byte(line))
	}
	cx, cy := t.Cursor()
	obs.Emit(p, "shrink_height/before", "cursor", fmt.Sprintf("%d,%d", cx, cy))
	obs.Emit(p, "shrink_height/before", "rows", screen(t))
	t.Resize(20, 10)
	cx2, cy2 := t.Cursor()
	obs.Emit(p, "shrink_height/after", "cursor", fmt.Sprintf("%d,%d", cx2, cy2))
	obs.Emit(p, "shrink_height/after", "height", t.Rows())
	obs.Emit(p, "shrink_height/after", "rows", screen(t))
	obs.Emit(p, "shrink_height/after", "scrollback_rows", t.ScrollbackRows())
	t.Free()

	t2 := ghostty.New(20, 10)
	t2.Write([]byte("0123456789" + "0123456789" + "0123456789" + "0123456789" + "ABCDE"))
	cx3, cy3 := t2.Cursor()
	obs.Emit(p, "reflow_width/before", "width", t2.Cols())
	obs.Emit(p, "reflow_width/before", "cursor", fmt.Sprintf("%d,%d", cx3, cy3))
	obs.Emit(p, "reflow_width/before", "rows", screen(t2))
	t2.Resize(10, 10)
	cx4, cy4 := t2.Cursor()
	obs.Emit(p, "reflow_width/after", "width", t2.Cols())
	obs.Emit(p, "reflow_width/after", "cursor", fmt.Sprintf("%d,%d", cx4, cy4))
	obs.Emit(p, "reflow_width/after", "rows", screen(t2))
	t2.Free()

	t3 := ghostty.New(20, 5)
	t3.Write([]byte("hello\r\nworld"))
	t3.Resize(20, 10)
	obs.Emit(p, "grow_height/after", "rows", screen(t3))
	t3.Free()
}

func p9() {
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
		t := ghostty.New(40, 3)
		t.Write([]byte(c.seq))
		t.Write([]byte("Z"))
		title := t.Title()
		_, titles, _, _ := t.Events()
		obs.Emit(p, c.name, "title_events", fmt.Sprintf("%q", titles))
		obs.Emit(p, c.name, "title_hex", fmt.Sprintf("%x", title))
		obs.Emit(p, c.name, "screen_row0", fmt.Sprintf("%q", rowText(t, 0)))
		cx, _ := t.Cursor()
		obs.Emit(p, c.name, "cursor_x", cx)
		t.Free()
	}

	// Effects beyond the title, on the same protocol family.
	t := ghostty.New(40, 3)
	t.Write([]byte("\x1b]0;T\x07"))             // title
	t.Write([]byte("\x1b]7;file://h/tmp\x07"))  // cwd
	t.Write([]byte("\a"))                       // bell
	t.Write([]byte("\x1b]52;c;SGVsbG8=\x1b\\")) // clipboard write (OSC 52)
	t.Write([]byte("\x1b]8;;https://example.com\x1b\\L\x1b]8;;\x1b\\"))
	bells, titles, pwds, clips := t.Events()
	obs.Emit(p, "effects", "bells", bells)
	obs.Emit(p, "effects", "titles", fmt.Sprintf("%q", titles))
	obs.Emit(p, "effects", "pwds", fmt.Sprintf("%q", pwds))
	obs.Emit(p, "effects", "clipboard_writes", fmt.Sprint(clips))
	obs.Emit(p, "effects", "title_after", t.Title())
	obs.Emit(p, "effects", "pwd_after", t.Pwd())
	t.Free()
}

func api() {
	const p = "api"

	// Incremental render state, held across updates: the API a diff encoder
	// would drive.
	t := ghostty.New(20, 3)
	t.Write([]byte("hello\r\nworld"))
	rs := ghostty.NewRenderState()
	rs.Update(t)
	obs.Emit(p, "incremental", "dirty_first_update", rs.Dirty())
	obs.Emit(p, "incremental", "dirty_rows_first_update", fmt.Sprint(rs.DirtyRows()))
	rs.Clean()
	rs.Update(t)
	obs.Emit(p, "incremental", "dirty_after_clean", rs.Dirty())
	obs.Emit(p, "incremental", "dirty_rows_after_clean", fmt.Sprint(rs.DirtyRows()))
	t.Write([]byte("\r\nthird"))
	rs.Update(t)
	obs.Emit(p, "incremental", "dirty_after_one_line", rs.Dirty())
	obs.Emit(p, "incremental", "dirty_rows_after_one_line", fmt.Sprint(rs.DirtyRows()))
	rs.Clean()
	rs.Free()
	t.Free()

	// Compile-time capabilities of the linked library.
	obs.Emit(p, "graphics", "kitty_graphics_compiled", ghostty.BuildInfoKittyGraphics())
	obs.Emit(p, "graphics", "tmux_control_mode_compiled", ghostty.BuildInfoTmuxControlMode())

	// Soft-wrap continuation per line.
	t2 := ghostty.New(10, 4)
	t2.Write([]byte("0123456789ABCDEFGHIJ"))
	w0, c0 := t2.RowWrap(0)
	w1, c1 := t2.RowWrap(1)
	w2, c2 := t2.RowWrap(2)
	obs.Emit(p, "softwrap", "row0", fmt.Sprintf("%q", rowText(t2, 0)))
	obs.Emit(p, "softwrap", "row1", fmt.Sprintf("%q", rowText(t2, 1)))
	obs.Emit(p, "softwrap", "row0_wrap", w0)
	obs.Emit(p, "softwrap", "row0_continuation", c0)
	obs.Emit(p, "softwrap", "row1_wrap", w1)
	obs.Emit(p, "softwrap", "row1_continuation", c1)
	obs.Emit(p, "softwrap", "row2_wrap", w2)
	obs.Emit(p, "softwrap", "row2_continuation", c2)
	cx, cy := t2.Cursor()
	obs.Emit(p, "softwrap", "cursor", fmt.Sprintf("%d,%d", cx, cy))

	// The same wrapped line across a width change.
	t2.Resize(20, 4)
	obs.Emit(p, "softwrap", "after_grow_row0", fmt.Sprintf("%q", rowText(t2, 0)))
	obs.Emit(p, "softwrap", "after_grow_row1", fmt.Sprintf("%q", rowText(t2, 1)))
	w0b, c0b := t2.RowWrap(0)
	w1b, c1b := t2.RowWrap(1)
	obs.Emit(p, "softwrap", "after_grow_row0_wrap", w0b)
	obs.Emit(p, "softwrap", "after_grow_row0_continuation", c0b)
	obs.Emit(p, "softwrap", "after_grow_row1_wrap", w1b)
	obs.Emit(p, "softwrap", "after_grow_row1_continuation", c1b)
	t2.Free()
}
