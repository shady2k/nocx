// Command xvt runs the emulator qualification probes against
// github.com/charmbracelet/x/vt at the revision nocx pins (ADR-0041), and
// writes one JSON observation per line to stdout.
package main

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"nocx.internal/spikes/emulator/obs"
)

const replyWait = 250 * time.Millisecond

// drainer continuously reads the emulator's reply stream, the way a real
// terminal always has a reader. ADR-0041 records that x/vt blocks forever on
// its reply pipe if nothing drains it, so this is also the integration
// requirement being exercised.
type drainer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newEmu(w, h int) (*vt.Emulator, *drainer) {
	e := vt.NewEmulator(w, h)
	d := &drainer{}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := e.Read(b)
			if n > 0 {
				d.mu.Lock()
				d.buf.Write(b[:n])
				d.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return e, d
}

// write feeds bytes to the emulator and discards the count and error. x/vt's
// Write returns (len(p), nil) unless its reply pipe has been closed, and every
// write here is bounded and local; the reply side is what carries meaning, and
// the drainer captures that.
func write(e *vt.Emulator, b []byte) {
	_, _ = e.Write(b)
}

// take returns everything the emulator has replied with so far, waiting up to
// replyWait for the first byte.
func (d *drainer) take() []byte {
	deadline := time.Now().Add(replyWait)
	for {
		d.mu.Lock()
		if d.buf.Len() > 0 {
			out := append([]byte(nil), d.buf.Bytes()...)
			d.buf.Reset()
			d.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			d.mu.Lock()
			out = append(out, d.buf.Bytes()...)
			d.buf.Reset()
			d.mu.Unlock()
			return out
		}
		d.mu.Unlock()
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// cellRow renders row y as one token per column: %q of the cell's grapheme
// and its width, or "." for an empty cell.
func cellRow(e *vt.Emulator, y int) string {
	var sb strings.Builder
	for x := range e.Width() {
		c := e.CellAt(x, y)
		if c == nil || c.Content == "" || c.Content == " " {
			sb.WriteString(". ")
			continue
		}
		fmt.Fprintf(&sb, "%q/%d ", c.Content, c.Width)
	}
	return strings.TrimRight(sb.String(), " ")
}

// rowText renders row y as plain text with trailing blanks trimmed.
func rowText(e *vt.Emulator, y int) string {
	var sb strings.Builder
	for x := range e.Width() {
		c := e.CellAt(x, y)
		if c == nil || c.Content == "" {
			sb.WriteByte(' ')
			continue
		}
		sb.WriteString(c.Content)
	}
	return strings.TrimRight(sb.String(), " ")
}

// screen renders every row as a %q-quoted slice, so that a row holding a
// space and an empty row cannot be confused in the emitted line.
func screen(e *vt.Emulator) string {
	out := make([]string, e.Height())
	for y := range out {
		out[y] = rowText(e, y)
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for y, r := range out {
		if y > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%q", r)
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
	p10()
	p11()
	api()
}

// logSink collects the emulator's own log lines, which is how x/vt reports a
// sequence it has no handler for.
type logSink struct{ lines *[]string }

func (s logSink) Printf(format string, v ...any) {
	*s.lines = append(*s.lines, fmt.Sprintf(format, v...))
}

// p11 measures whether the graphics protocols are supported by executing them
// against the emulator, rather than by reading the source for their absence.
func p11() {
	const p = "11_graphics"
	// A minimal sixel DCS (ESC P q ... ESC \) and a Kitty graphics APC
	// (ESC _ G ... ESC \) transmitting a 1x1 RGB image as three bytes.
	sixel := "\x1bPq\"1;1;2;2#0;2;0;0;0#0~~\x1b\\"
	kitty := "\x1b_Ga=T,f=24,s=1,v=1,i=42;AAAA\x1b\\"

	// (a) No handler registered: what does the emulator do with them?
	e, d := newEmu(20, 3)
	var lines []string
	e.SetLogger(logSink{&lines})
	before := screen(e)
	write(e, []byte(sixel))
	write(e, []byte(kitty))
	d.take()
	obs.Emit(p, "no_handler", "log_lines", fmt.Sprintf("%q", lines))
	obs.Emit(p, "no_handler", "screen_unchanged", screen(e) == before)
	obs.Emit(p, "no_handler", "screen_row0", fmt.Sprintf("%q", rowText(e, 0)))
	cx, cy := e.CursorPosition().X, e.CursorPosition().Y
	obs.Emit(p, "no_handler", "cursor", fmt.Sprintf("%d,%d", cx, cy))
	obs.Emit(p, "no_handler", "reply_bytes", obs.Esc(d.take()))

	// (b) The consumer seam: a registered handler receives them instead.
	e2, d2 := newEmu(20, 3)
	var dcsData, apcData [][]byte
	e2.RegisterDcsHandler('q', func(_ ansi.Params, data []byte) bool {
		dcsData = append(dcsData, append([]byte(nil), data...))
		return true
	})
	e2.RegisterApcHandler(func(data []byte) bool {
		apcData = append(apcData, append([]byte(nil), data...))
		return true
	})
	write(e2, []byte(sixel))
	write(e2, []byte(kitty))
	d2.take()
	obs.Emit(p, "with_handler", "dcs_deliveries", len(dcsData))
	obs.Emit(p, "with_handler", "apc_deliveries", len(apcData))
	obs.Emit(p, "with_handler", "dcs_bytes", obs.Esc(bytes.Join(dcsData, nil)))
	obs.Emit(p, "with_handler", "apc_bytes", obs.Esc(bytes.Join(apcData, nil)))
}

// p10 measures the integration obligation ADR-0041 records: x/vt answers the
// program through an io.Pipe, so a reply-producing write returns only when
// something reads the other end.
func p10() {
	const p = "10_reply_requires_reader"

	// No reader at all.
	raw := vt.NewEmulator(80, 24)
	done := make(chan struct{})
	go func() {
		write(raw, []byte("\x1b[6n"))
		close(done)
	}()
	select {
	case <-done:
		obs.Emit(p, "no_reader", "write_returned_within_2s", true)
	case <-time.After(2 * time.Second):
		obs.Emit(p, "no_reader", "write_returned_within_2s", false)
	}

	// With a reader.
	e, d := newEmu(80, 24)
	start := time.Now()
	write(e, []byte("\x1b[6n"))
	el := time.Since(start)
	obs.Emit(p, "with_reader", "write_ns", el.Nanoseconds())
	obs.Emit(p, "with_reader", "reply_esc", obs.Esc(d.take()))
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
		e, d := newEmu(20, 3)
		write(e, b)
		d.take()
		whole := cellRow(e, 0)
		wholeCur := e.CursorPosition()
		obs.Emit(p, c.name+"/whole", "bytes", len(b))
		obs.Emit(p, c.name+"/whole", "row0", whole)
		obs.Emit(p, c.name+"/whole", "cursor", fmt.Sprintf("%d,%d", wholeCur.X, wholeCur.Y))

		ok, firstBad := 0, -1
		for k := 1; k < len(b); k++ {
			e2, d2 := newEmu(20, 3)
			write(e2, b[:k])
			write(e2, b[k:])
			d2.take()
			row := cellRow(e2, 0)
			cur := e2.CursorPosition()
			match := row == whole && cur == wholeCur
			if match {
				ok++
			} else if firstBad < 0 {
				firstBad = k
			}
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "row0", row)
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "cursor", fmt.Sprintf("%d,%d", cur.X, cur.Y))
			obs.Emit(p, fmt.Sprintf("%s/split@%d", c.name, k), "matches_whole", match)
		}
		obs.Emit(p, c.name+"/summary", "offsets_identical", fmt.Sprintf("%d/%d", ok, len(b)-1))
		obs.Emit(p, c.name+"/summary", "first_divergent_offset", firstBad)
	}
}

func p2() {
	const p = "2_combining_mark"
	// U+0301 COMBINING ACUTE ACCENT on an ASCII base.
	e, d := newEmu(20, 3)
	write(e, []byte("a\u0301"))
	d.take()
	obs.Emit(p, "one_write", "row0", cellRow(e, 0))
	obs.Emit(p, "one_write", "cells_occupied", occupied(e, 0))
	obs.Emit(p, "one_write", "cursor_x", e.CursorPosition().X)

	e2, d2 := newEmu(20, 3)
	write(e2, []byte("a"))
	write(e2, []byte("\u0301"))
	d2.take()
	obs.Emit(p, "split_write", "row0", cellRow(e2, 0))
	obs.Emit(p, "split_write", "cells_occupied", occupied(e2, 0))
	obs.Emit(p, "split_write", "cursor_x", e2.CursorPosition().X)
}

// occupied counts cells holding something other than a blank: the columns a
// row's content actually claims, plus any zero-width cell (a combining mark
// or the tail of a split cluster) that was stored beside its base.
func occupied(e *vt.Emulator, y int) int {
	n := 0
	for x := range e.Width() {
		if c := e.CellAt(x, y); c != nil && c.Content != "" && c.Content != " " {
			n++
		}
	}
	return n
}

func p3() {
	const p = "3_modified_keys"
	cases := []struct {
		name string
		ev   uv.KeyEvent
	}{
		{"ctrl_left", vt.KeyPressEvent{Code: vt.KeyLeft, Mod: vt.ModCtrl}},
		{"shift_up", vt.KeyPressEvent{Code: vt.KeyUp, Mod: vt.ModShift}},
		{"f5_shift", vt.KeyPressEvent{Code: vt.KeyF5, Mod: vt.ModShift}},
		{"f5_plain", vt.KeyPressEvent{Code: vt.KeyF5}},
		{"f5_ctrl", vt.KeyPressEvent{Code: vt.KeyF5, Mod: vt.ModCtrl}},
		{"ctrl_shift_left", vt.KeyPressEvent{Code: vt.KeyLeft, Mod: vt.ModCtrl | vt.ModShift}},
		{"left_plain", vt.KeyPressEvent{Code: vt.KeyLeft}},
	}
	for _, c := range cases {
		e, d := newEmu(80, 24)
		e.SendKey(c.ev)
		out := d.take()
		obs.Emit(p, c.name, "len", len(out))
		obs.Emit(p, c.name, "hex", obs.Hex(out))
		obs.Emit(p, c.name, "esc", obs.Esc(out))
	}
}

func p4() {
	const p = "4_function_keys"
	cases := []struct {
		name string
		ev   uv.KeyEvent
	}{
		{"f13", vt.KeyPressEvent{Code: vt.KeyF13}},
		{"f13_shift", vt.KeyPressEvent{Code: vt.KeyF13, Mod: vt.ModShift}},
		{"f12", vt.KeyPressEvent{Code: vt.KeyF12}},
		{"f1", vt.KeyPressEvent{Code: vt.KeyF1}},
	}
	for _, c := range cases {
		e, d := newEmu(80, 24)
		e.SendKey(c.ev)
		out := d.take()
		obs.Emit(p, c.name, "len", len(out))
		obs.Emit(p, c.name, "hex", obs.Hex(out))
		obs.Emit(p, c.name, "esc", obs.Esc(out))
		obs.Emit(p, c.name, "contains_u+fffd", bytes.Contains(out, []byte("\uFFFD")))
	}
}

func p5() {
	const p = "5_insert_mode"
	e, d := newEmu(20, 3)
	var ansiModes, decModes []int
	e.SetCallbacks(vt.Callbacks{
		EnableMode:  func(m ansi.Mode) { ansiModes = append(ansiModes, m.Mode()) },
		DisableMode: func(m ansi.Mode) { decModes = append(decModes, m.Mode()) },
	})
	write(e, []byte("abcd"))
	write(e, []byte("\x1b[1;1H")) // CUP to home
	obs.Emit(p, "before_irm", "row0", cellRow(e, 0))

	write(e, []byte("\x1b[4h")) // IRM on
	enabled := append([]int(nil), ansiModes...)
	obs.Emit(p, "after_set_irm", "enable_mode_callbacks", fmt.Sprint(enabled))
	obs.Emit(p, "after_set_irm", "mode_4_recorded", contains(enabled, 4))

	write(e, []byte("XY"))
	obs.Emit(p, "after_print", "row0", cellRow(e, 0))
	obs.Emit(p, "after_print", "row_text", rowText(e, 0))
	obs.Emit(p, "after_print", "cursor_x", e.CursorPosition().X)

	write(e, []byte("\x1b[4$p")) // DECRQM: query IRM
	rep := d.take()
	obs.Emit(p, "decrqm", "esc", obs.Esc(rep))
	obs.Emit(p, "decrqm", "hex", obs.Hex(rep))
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func p6() {
	const p = "6_cursor_report_origin_mode"
	// Control: no DECOM.
	e, d := newEmu(80, 24)
	write(e, []byte("\x1b[5;10r")) // DECSTBM rows 5..10
	write(e, []byte("\x1b[1;1H"))  // CUP 1;1
	pos := e.CursorPosition()
	obs.Emit(p, "decom_off", "api_cursor", fmt.Sprintf("%d,%d", pos.X, pos.Y))
	write(e, []byte("\x1b[6n")) // DSR 6
	obs.Emit(p, "decom_off", "cpr_esc", obs.Esc(d.take()))

	// DECOM on.
	e2, d2 := newEmu(80, 24)
	write(e2, []byte("\x1b[5;10r")) // DECSTBM rows 5..10
	write(e2, []byte("\x1b[?6h"))   // DECOM on
	write(e2, []byte("\x1b[1;1H"))  // CUP 1;1
	pos2 := e2.CursorPosition()
	obs.Emit(p, "decom_on", "api_cursor", fmt.Sprintf("%d,%d", pos2.X, pos2.Y))
	write(e2, []byte("\x1b[6n"))
	rep := d2.take()
	obs.Emit(p, "decom_on", "cpr_esc", obs.Esc(rep))
	obs.Emit(p, "decom_on", "cpr_hex", obs.Hex(rep))
	row, col := parseCPR(rep)
	obs.Emit(p, "decom_on", "cpr_row", row)
	obs.Emit(p, "decom_on", "cpr_col", col)

	// DECOM off, cursor parked at the top of the scrolling region by an
	// origin-relative address, for the same absolute position.
	e3, d3 := newEmu(80, 24)
	write(e3, []byte("\x1b[5;10r"))
	write(e3, []byte("\x1b[5;1H"))
	pos3 := e3.CursorPosition()
	obs.Emit(p, "decom_off_at_row5", "api_cursor", fmt.Sprintf("%d,%d", pos3.X, pos3.Y))
	write(e3, []byte("\x1b[6n"))
	obs.Emit(p, "decom_off_at_row5", "cpr_esc", obs.Esc(d3.take()))
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
	// The probe as specified, bounded so a pathological implementation
	// reports itself rather than hanging the spike.
	runREP(p, "rep_1000000000_budget30s", 1000000000, 30*time.Second)
	// Same, with the scrollback the emulator keeps by default made small,
	// to separate the print loop from scrollback eviction.
	runREP(p, "rep_1000000_scrollback100", 1000000, 0)
	// The pair that shows whether REP is clamped: equal final state means the
	// count was capped, different state means it was honoured.
	runREP(p, "rep_65535", 65535, 0)
	runREP(p, "rep_70000", 70000, 0)
}

func runREP(p, name string, n int, budget time.Duration) {
	e, d := newEmu(80, 24)
	write(e, []byte("A"))
	d.take()

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)

	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		write(e, []byte("\x1b["+itoa(n)+"b"))
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
	obs.Emit(p, name, "scrollback_len", e.ScrollbackLen())
	if completed {
		obs.Emit(p, name, "cursor", fmt.Sprintf("%d,%d", e.CursorPosition().X, e.CursorPosition().Y))
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func p8() {
	const p = "8_resize"
	// (a) Height shrink while the cursor sits on the last row.
	e, d := newEmu(20, 24)
	for i := 1; i <= 24; i++ {
		line := fmt.Sprintf("L%02d", i)
		if i < 24 {
			line += "\r\n"
		}
		write(e, []byte(line))
	}
	d.take()
	obs.Emit(p, "shrink_height/before", "cursor", fmt.Sprintf("%d,%d", e.CursorPosition().X, e.CursorPosition().Y))
	obs.Emit(p, "shrink_height/before", "rows", fmt.Sprint(screen(e)))
	e.Resize(20, 10)
	d.take()
	obs.Emit(p, "shrink_height/after", "cursor", fmt.Sprintf("%d,%d", e.CursorPosition().X, e.CursorPosition().Y))
	obs.Emit(p, "shrink_height/after", "height", e.Height())
	obs.Emit(p, "shrink_height/after", "rows", fmt.Sprint(screen(e)))
	obs.Emit(p, "shrink_height/after", "scrollback_len", e.ScrollbackLen())

	// (b) Width change with a wrapped logical line.
	e2, d2 := newEmu(20, 10)
	text := "0123456789" + "0123456789" + "0123456789" + "0123456789" + "ABCDE"
	write(e2, []byte(text))
	d2.take()
	obs.Emit(p, "reflow_width/before", "width", e2.Width())
	obs.Emit(p, "reflow_width/before", "cursor", fmt.Sprintf("%d,%d", e2.CursorPosition().X, e2.CursorPosition().Y))
	obs.Emit(p, "reflow_width/before", "rows", fmt.Sprint(screen(e2)))
	e2.Resize(10, 10)
	d2.take()
	obs.Emit(p, "reflow_width/after", "width", e2.Width())
	obs.Emit(p, "reflow_width/after", "cursor", fmt.Sprintf("%d,%d", e2.CursorPosition().X, e2.CursorPosition().Y))
	obs.Emit(p, "reflow_width/after", "rows", fmt.Sprint(screen(e2)))

	// (c) Grow.
	e3, d3 := newEmu(20, 5)
	write(e3, []byte("hello\r\nworld"))
	d3.take()
	e3.Resize(20, 10)
	d3.take()
	obs.Emit(p, "grow_height/after", "rows", fmt.Sprint(screen(e3)))
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
		e, d := newEmu(40, 3)
		var got []string
		e.SetCallbacks(vt.Callbacks{Title: func(s string) { got = append(got, s) }})
		write(e, []byte(c.seq))
		write(e, []byte("Z")) // lands on the grid only if the string ended early
		d.take()
		obs.Emit(p, c.name, "title_events", fmt.Sprintf("%q", got))
		obs.Emit(p, c.name, "title_hex", fmt.Sprintf("%x", strings.Join(got, "")))
		obs.Emit(p, c.name, "screen_row0", fmt.Sprintf("%q", rowText(e, 0)))
		obs.Emit(p, c.name, "cursor_x", e.CursorPosition().X)
	}
}

func api() {
	const p = "api"
	// Incremental render state: what the emulator actually exposes.
	e, d := newEmu(20, 3)
	write(e, []byte("hello\r\nworld"))
	d.take()
	t := e.Touched()
	obs.Emit(p, "incremental", "touched_rows", len(t))
	var detail []string
	for y, ld := range t {
		if ld == nil {
			detail = append(detail, fmt.Sprintf("%d:nil", y))
			continue
		}
		detail = append(detail, fmt.Sprintf("%d:{first:%d last:%d}", y, ld.FirstCell, ld.LastCell))
	}
	obs.Emit(p, "incremental", "touched_detail", fmt.Sprint(detail))

	// Soft-wrap continuation per line.
	e2, d2 := newEmu(10, 4)
	write(e2, []byte("0123456789ABCDEFGHIJ"))
	d2.take()
	obs.Emit(p, "softwrap", "row0", fmt.Sprintf("%q", rowText(e2, 0)))
	obs.Emit(p, "softwrap", "row1", fmt.Sprintf("%q", rowText(e2, 1)))
	obs.Emit(p, "softwrap", "cursor", fmt.Sprintf("%d,%d", e2.CursorPosition().X, e2.CursorPosition().Y))
	obs.Emit(p, "softwrap", "touched_detail", fmt.Sprint(touchedDetail(e2)))
	obs.Emit(p, "softwrap", "scrollback_len", e2.ScrollbackLen())

	// Reflow across a resize after the wrap happened.
	e2.Resize(20, 4)
	obs.Emit(p, "softwrap", "after_grow_row0", fmt.Sprintf("%q", rowText(e2, 0)))
	obs.Emit(p, "softwrap", "after_grow_row1", fmt.Sprintf("%q", rowText(e2, 1)))
}

func touchedDetail(e *vt.Emulator) []string {
	var detail []string
	for y, ld := range e.Touched() {
		if ld == nil {
			detail = append(detail, fmt.Sprintf("%d:nil", y))
			continue
		}
		detail = append(detail, fmt.Sprintf("%d:{first:%d last:%d}", y, ld.FirstCell, ld.LastCell))
	}
	return detail
}
