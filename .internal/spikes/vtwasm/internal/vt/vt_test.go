package vt_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"nocx.internal/spikes/vtwasm/internal/vt"
)

// The module is a build artifact and is not committed (see .gitignore), so a
// checkout without it reports why rather than failing: `./build.sh` produces it
// in about 45 s, and the same script is what pins the Zig and ghostty versions
// these assertions depend on.
func open(t *testing.T) *vt.VT {
	t.Helper()
	const path = "../../.build/vt.wasm"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no wasm module at %s; run ./build.sh to produce it", path)
	}
	ctx := context.Background()
	v, err := vt.Open(ctx, path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = v.Close(ctx) })
	return v
}

// The assertions below are the ABI contract: they are what "the shim carries
// the design's needs" means in executable form, and each one fails if the flat
// boundary loses a fact rather than moving it.
func TestCellStyleCrossesTheABI(t *testing.T) {
	ctx := context.Background()
	v := open(t)
	if r := v.New(ctx, 20, 3); r != 0 {
		t.Fatalf("vt_new = %d", r)
	}
	v.Write(ctx, []byte("\x1b[1;31mA\x1b[0m\x1b[38;2;1;2;3mB\x1b[0m\x1b[48;5;42mC\x1b[0m\x1b[4:2mD\x1b[0m"))

	a := v.Cell(ctx, 0, 0)
	if a.Grapheme != "A" || !a.HasText {
		t.Fatalf("cell 0: %+v", a)
	}
	if a.Attrs&vt.AttrBold == 0 {
		t.Errorf("bold did not cross the ABI: attrs=%#x", a.Attrs)
	}
	if a.FG.Kind != vt.ColorPalette || a.FG.Palette != 1 {
		t.Errorf("palette foreground not kept apart: %+v", a.FG)
	}

	b := v.Cell(ctx, 1, 0)
	if b.FG.Kind != vt.ColorRGB || b.FG.RGB != 0x010203 {
		t.Errorf("rgb foreground not kept apart: %+v", b.FG)
	}
	if b.FG.Kind == a.FG.Kind {
		t.Errorf("palette and rgb foregrounds collapsed to one kind %d", b.FG.Kind)
	}

	c := v.Cell(ctx, 2, 0)
	if c.BG.Kind != vt.ColorPalette || c.BG.Palette != 42 {
		t.Errorf("palette background not kept apart: %+v", c.BG)
	}

	d := v.Cell(ctx, 3, 0)
	if d.Underline != 2 {
		t.Errorf("curly underline did not cross the ABI: %d", d.Underline)
	}
}

func TestWideCellAndHyperlinkCrossTheABI(t *testing.T) {
	ctx := context.Background()
	v := open(t)
	if r := v.New(ctx, 20, 3); r != 0 {
		t.Fatalf("vt_new = %d", r)
	}
	v.Write(ctx, []byte("\x1b]8;;https://example.com\x1b\\L\x1b]8;;\x1b\\ \u4e2d"))

	link := v.Cell(ctx, 0, 0)
	if link.Grapheme != "L" || link.Hyperlink != "https://example.com" {
		t.Errorf("hyperlink did not cross the ABI: %+v", link)
	}
	wide := v.Cell(ctx, 2, 0)
	if wide.Grapheme != "\u4e2d" || wide.Width != vt.WidthWide {
		t.Errorf("wide grapheme: %+v", wide)
	}
	// The cell after a wide cluster is its spacer, and a renderer must be able
	// to tell that from a blank cell.
	tail := v.Cell(ctx, 3, 0)
	if tail.Width != vt.WidthSpacerTail || tail.HasText {
		t.Errorf("spacer tail after a wide cell: %+v", tail)
	}
}

func TestRepliesAndRenderStateCrossTheABI(t *testing.T) {
	ctx := context.Background()
	v := open(t)
	if r := v.New(ctx, 20, 3); r != 0 {
		t.Fatalf("vt_new = %d", r)
	}
	v.Write(ctx, []byte("hello\r\nworld"))
	v.Write(ctx, []byte("\x1b[6n"))
	if rep := string(v.Reply(ctx)); !strings.HasPrefix(rep, "\x1b[") || !strings.HasSuffix(rep, "R") {
		t.Errorf("cursor report did not cross the ABI: %q", rep)
	}
	if again := v.Reply(ctx); len(again) != 0 {
		t.Errorf("replies are not consumed once: %q", again)
	}

	if r := v.RSOpen(ctx); r != 0 {
		t.Fatalf("rs_new = %d", r)
	}
	v.RSUpdate(ctx)
	if got := v.RSDirty(ctx); got != vt.DirtyFull {
		t.Errorf("first dirty = %d, want FULL", got)
	}
	if rows := v.RSDirtyRows(ctx); len(rows) != 3 {
		t.Errorf("dirty rows on a fresh state = %v, want all three", rows)
	}
	v.RSClean(ctx)
	v.RSUpdate(ctx)
	if got := v.RSDirty(ctx); got != vt.DirtyFalse {
		t.Errorf("dirty after clean = %d, want FALSE", got)
	}
	v.Write(ctx, []byte("\r\nthird"))
	v.RSUpdate(ctx)
	if got := v.RSDirty(ctx); got != vt.DirtyPartial {
		t.Errorf("dirty after one line = %d, want PARTIAL", got)
	}
	if rows := v.RSDirtyRows(ctx); len(rows) == 0 {
		t.Error("a changed line reported no dirty rows")
	}
}

func TestSoftWrapContinuationCrossesTheABI(t *testing.T) {
	ctx := context.Background()
	v := open(t)
	if r := v.New(ctx, 10, 4); r != 0 {
		t.Fatalf("vt_new = %d", r)
	}
	v.Write(ctx, []byte("0123456789ABCDEFGHIJ"))
	wrap0, cont0 := v.RowWrap(ctx, 0)
	wrap1, cont1 := v.RowWrap(ctx, 1)
	if !wrap0 || cont0 {
		t.Errorf("row 0 wrap/continuation = %v/%v, want true/false", wrap0, cont0)
	}
	if wrap1 || !cont1 {
		t.Errorf("row 1 wrap/continuation = %v/%v, want false/true", wrap1, cont1)
	}
}

func TestKeyEncodingIsDrivenFromTerminalState(t *testing.T) {
	ctx := context.Background()
	v := open(t)
	if r := v.New(ctx, 80, 24); r != 0 {
		t.Fatalf("vt_new = %d", r)
	}

	// Legacy: a plain arrow is CSI D.
	if got := string(v.KeyEncode(ctx, v.KeyArrowLeft(ctx), 0, 0, false)); got != "\x1b[D" {
		t.Errorf("legacy left = %q", got)
	}
	// The same key with the terminal in application-cursor mode must encode
	// differently, which is the whole point of passing the terminal in.
	v.Write(ctx, []byte("\x1b[?1h"))
	if got := string(v.KeyEncode(ctx, v.KeyArrowLeft(ctx), 0, 0, true)); got != "\x1bOD" {
		t.Errorf("application left (from terminal state) = %q", got)
	}
	if got := string(v.KeyEncode(ctx, v.KeyArrowLeft(ctx), 0, 0, false)); got != "\x1b[D" {
		t.Errorf("legacy left while in application mode = %q", got)
	}
	// And a Kitty-aware program gets the event-suffixed form.
	v.Write(ctx, []byte("\x1b[?1l"))
	kitty := string(v.KeyEncode(ctx, v.KeyArrowLeft(ctx), v.ModCtrl(ctx), v.KittyKeyAll(ctx), false))
	if kitty != "\x1b[1;5:1D" {
		t.Errorf("kitty ctrl-left = %q", kitty)
	}
}

// A runtime holds one instance per session, so the lifecycle has to be
// per-instance: ending one session may not end another's.
func TestClosingOneInstanceLeavesSiblingsUsable(t *testing.T) {
	ctx := context.Background()
	const path = "../../.build/vt.wasm"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no wasm module at %s; run ./build.sh to produce it", path)
	}
	m, err := vt.Compile(ctx, path)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer m.Close(ctx)

	a, err := m.Instantiate(ctx)
	if err != nil {
		t.Fatalf("instantiate a: %v", err)
	}
	b, err := m.Instantiate(ctx)
	if err != nil {
		t.Fatalf("instantiate b: %v", err)
	}
	if a == b {
		t.Fatal("two instances are the same object")
	}
	for _, v := range []*vt.VT{a, b} {
		if r := v.New(ctx, 20, 3); r != 0 {
			t.Fatalf("vt_new = %d", r)
		}
	}

	if err := a.Close(ctx); err != nil {
		t.Fatalf("close a: %v", err)
	}
	// b must still parse, read cells and answer a query.
	b.Write(ctx, []byte("still here"))
	if got := b.Cell(ctx, 0, 0).Grapheme; got != "s" {
		t.Errorf("sibling instance stopped working after another closed: cell = %q", got)
	}
	b.Write(ctx, []byte("\x1b[6n"))
	if rep := string(b.Reply(ctx)); rep == "" {
		t.Error("sibling instance produced no reply after another closed")
	}
	if m.Instances() != 2 {
		t.Errorf("instances = %d, want 2", m.Instances())
	}
}

// Close is idempotent because a caller may close deliberately and still defer
// a close: the bounded REP probe stops a runaway write by closing the instance,
// and the deferred call must then be harmless.
func TestCloseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	const path = "../../.build/vt.wasm"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no wasm module at %s; run ./build.sh to produce it", path)
	}
	v, err := vt.Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if r := v.New(ctx, 20, 3); r != 0 {
		t.Fatalf("vt_new = %d", r)
	}
	if err := v.Close(ctx); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := v.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
