// Package vt drives the libghostty-vt flat-ABI wasm module from Go under
// wazero, with no CGo anywhere in the process.
//
// The module is one artifact for every target nocx builds: wasm32-freestanding
// is architecture independent, and it links no libc, so nothing about the host
// that loads it can change what it does. The ABI it exposes is scalars plus
// pointers into its own linear memory (see shim.c); this package is the only
// place that knows those offsets, so nothing above it deals in addresses.
package vt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Cell width classes, mirroring GhosttyCellWide.
const (
	WidthNarrow     = 0
	WidthWide       = 1
	WidthSpacerTail = 2
	WidthSpacerHead = 3
)

// Style colour tags, mirroring GhosttyStyleColorTag.
const (
	ColorNone    = 0
	ColorPalette = 1
	ColorRGB     = 2
)

// Attribute bits returned by Cell.Attrs.
const (
	AttrBold          = 1 << 0
	AttrItalic        = 1 << 1
	AttrFaint         = 1 << 2
	AttrBlink         = 1 << 3
	AttrInverse       = 1 << 4
	AttrInvisible     = 1 << 5
	AttrStrikethrough = 1 << 6
	AttrOverline      = 1 << 7
)

// Render-state dirty classes, mirroring GhosttyRenderStateDirty.
const (
	DirtyFalse   = 0
	DirtyPartial = 1
	DirtyFull    = 2
)

// Build info selectors, mirroring GhosttyBuildInfo.
const (
	BuildInfoKittyGraphics = 2
	BuildInfoTmuxControl   = 3
	BuildInfoVersionString = 5
)

// Color is one style colour, kept in the three shapes the design requires be
// distinguishable: unset, a palette index, or a direct RGB triple.
type Color struct {
	Kind    int
	Palette int
	RGB     uint32
}

// Cell is everything the card format asks for about one grid position.
type Cell struct {
	Grapheme  string
	Width     int
	HasText   bool
	FG        Color
	BG        Color
	UL        Color
	Attrs     uint32
	Underline int
	Hyperlink string
}

// ClipboardWrite is one OSC 52 clipboard effect.
type ClipboardWrite struct {
	Location int
	Length   int
}

// VT is one wasm instance holding one terminal. Every terminal gets its own
// instance with its own linear memory, which is exactly what the per-session
// memory measurement is about.
type VT struct {
	module *Module
	mod    api.Module
	mem    api.Memory
	fn     map[string]api.Function

	// writeFn and inbufCap are cached at Instantiate; see the comment there.
	// Both are consulted once per PTY chunk, so neither may cost a call into
	// the guest: the throughput measurement would be reporting this package's
	// overhead rather than the library's.
	writeFn  api.Function
	inbufCap uint32

	inbuf  uint32
	outbuf uint32
	// ownsModule is set by Open, which has no other owner to hand the shared
	// runtime to. Instances from Module.Instantiate must not close it: one
	// session ending may not end another's.
	ownsModule bool
	// closed makes Close idempotent, so a caller that closes deliberately (a
	// probe stopping a runaway write) can still defer Close.
	closed bool
}

// Module is the compiled wasm module. Compiling it once and instantiating it
// many times is the shape a runtime wants: the code pages are shared, and each
// session pays only for its own instance and its own linear memory. That is
// what the per-session memory measurement is about.
type Module struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	mu       sync.Mutex
	count    int
}

// Compile reads and compiles the module. Nothing is instantiated yet, and no
// network is used.
func Compile(ctx context.Context, wasmPath string) (*Module, error) {
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read wasm: %w", err)
	}
	r := wazero.NewRuntime(ctx)
	compiled, err := r.CompileModule(ctx, wasm)
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("compile wasm: %w", err)
	}
	// The module may import host functions (ghostty logs through one). Rather
	// than hardcode a signature that upstream is free to change, take the
	// declaration from the module itself and satisfy it with a no-op that
	// reads the wasm stack — so a changed arity is a non-event, not a
	// load-time failure.
	builder := r.NewHostModuleBuilder("env")
	for _, imp := range compiled.ImportedFunctions() {
		modName, name, ok := imp.Import()
		if !ok || modName != "env" {
			continue
		}
		params, results := imp.ParamTypes(), imp.ResultTypes()
		builder.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
				for i := range results {
					stack[len(params)+i] = 0
				}
			}), params, results).
			Export(name)
	}
	if _, err := builder.Instantiate(ctx); err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("instantiate host module: %w", err)
	}
	return &Module{runtime: r, compiled: compiled}, nil
}

// Instantiate creates one instance: its own linear memory, its own terminal.
// Each instance takes its own module name, because a wazero runtime keys
// instantiated modules by name and a runtime that holds many sessions needs
// many live instances of one compiled module.
func (m *Module) Instantiate(ctx context.Context) (*VT, error) {
	m.mu.Lock()
	m.count++
	name := fmt.Sprintf("vt-%d", m.count)
	m.mu.Unlock()
	cfg := wazero.NewModuleConfig().WithName(name).WithStartFunctions()
	mod, err := m.runtime.InstantiateModule(ctx, m.compiled, cfg)
	if err != nil {
		return nil, fmt.Errorf("instantiate module: %w", err)
	}
	mem := mod.Memory()
	if mem == nil {
		_ = mod.Close(ctx)
		return nil, errors.New("module exports no memory")
	}
	v := &VT{module: m, mod: mod, mem: mem, fn: map[string]api.Function{}}
	for name := range mod.ExportedFunctionDefinitions() {
		if f := mod.ExportedFunction(name); f != nil {
			v.fn[name] = f
		}
	}
	for _, need := range []string{"vt_new", "vt_inbuf", "vt_write_n"} {
		if v.fn[need] == nil {
			// A refused instantiation must not leave the instance behind: a
			// runtime that cannot create a session has to be able to say so
			// without leaking the memory it just mapped.
			_ = mod.Close(ctx)
			return nil, fmt.Errorf("module does not export %s", need)
		}
	}
	v.inbuf = uint32(v.call(ctx, "vt_inbuf"))
	v.outbuf = uint32(v.call(ctx, "vt_outbuf"))
	// Cached because both are consulted once per ingested chunk, thousands of
	// times a second: a map lookup and a guest call per chunk are costs the
	// throughput measurement should not be carrying. The capacity is a property
	// of the artifact, so one read at instantiation is all it can ever need.
	v.writeFn = v.fn["vt_write_n"]
	v.inbufCap = uint32(v.call(ctx, "vt_inbuf_cap"))
	return v, nil
}

// Close tears down the runtime, releasing every instance's linear memory.
func (m *Module) Close(ctx context.Context) error { return m.runtime.Close(ctx) }

// Open compiles and instantiates in one step, for callers that want one
// terminal and no sharing.
func Open(ctx context.Context, wasmPath string) (*VT, error) {
	m, err := Compile(ctx, wasmPath)
	if err != nil {
		return nil, err
	}
	v, err := m.Instantiate(ctx)
	if err != nil {
		_ = m.Close(ctx)
		return nil, err
	}
	v.ownsModule = true
	return v, nil
}

// Close releases this instance and its linear memory. Sibling instances of the
// same module keep running: only Module.Close tears down the shared runtime.
// A VT from Open owns its runtime and closes that too, because there is
// nothing else that could.
func (v *VT) Close(ctx context.Context) error {
	if v.closed {
		return nil
	}
	v.closed = true
	err := v.mod.Close(ctx)
	if v.ownsModule {
		if cerr := v.module.Close(ctx); err == nil {
			err = cerr
		}
	}
	return err
}

// Module returns the module this instance came from.
func (v *VT) Module() *Module { return v.module }

// Instances reports how many instances of this module have been created.
func (m *Module) Instances() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// MemorySize reports the instance's linear memory in bytes. This is the
// resident cost of one instance: it is page-granular and never shrinks.
func (v *VT) MemorySize() uint32 { return v.mem.Size() }

func (v *VT) call(ctx context.Context, name string, args ...uint64) uint64 {
	f, ok := v.fn[name]
	if !ok {
		panic("vt: module does not export " + name)
	}
	res, err := f.Call(ctx, args...)
	if err != nil {
		panic("vt: " + name + ": " + err.Error())
	}
	if len(res) == 0 {
		return 0
	}
	return res[0]
}

func i32(u uint64) int32 { return int32(uint32(u)) }

// ---------------------------------------------------------------- lifecycle

// New creates (or replaces) the terminal in this instance.
func (v *VT) New(ctx context.Context, cols, rows int) int32 {
	r := i32(v.call(ctx, "vt_new", uint64(cols), uint64(rows)))
	// Pointers are stable for the module's lifetime, but re-read them after a
	// call that can allocate, because a growing memory is exactly the case the
	// header warns embedders about.
	v.inbuf = uint32(v.call(ctx, "vt_inbuf"))
	v.outbuf = uint32(v.call(ctx, "vt_outbuf"))
	return r
}

// Free releases the terminal; the instance itself stays usable.
func (v *VT) Free(ctx context.Context) { v.call(ctx, "vt_free") }

// Reset performs RIS.
func (v *VT) Reset(ctx context.Context) { v.call(ctx, "vt_reset") }

// Resize changes the geometry.
func (v *VT) Resize(ctx context.Context, cols, rows int) int32 {
	return i32(v.call(ctx, "vt_resize", uint64(cols), uint64(rows)))
}

// SetScrollbackLines caps the scrollback.
func (v *VT) SetScrollbackLines(ctx context.Context, n int) int32 {
	return i32(v.call(ctx, "vt_set_scrollback_lines", uint64(n)))
}

// ------------------------------------------------------------------- input

// Write feeds bytes to the VT parser. It panics on a refused or failed call,
// which is right for every ordinary caller: the module's ABI is fixed and a
// failure there is a bug in this package, not a condition to handle.
func (v *VT) Write(ctx context.Context, b []byte) {
	if err := v.WriteErr(ctx, b); err != nil {
		panic("vt: " + err.Error())
	}
}

// WriteErr is Write for callers that must survive an interrupted call — a
// bounded probe that closes its instance to stop a runaway write, for
// instance. It chunks to the module's input buffer, so a caller never has to
// know the buffer size, and the module refuses an oversized write rather than
// truncating it.
func (v *VT) WriteErr(ctx context.Context, b []byte) error {
	capacity := int(v.inbufCap)
	for len(b) > 0 {
		n := len(b)
		if n > capacity {
			n = capacity
		}
		if !v.mem.Write(v.inbuf, b[:n]) {
			return fmt.Errorf("write out of range")
		}
		res, err := v.writeFn.Call(ctx, uint64(n))
		if err != nil {
			return fmt.Errorf("vt_write_n(%d): %w", n, err)
		}
		if r := i32(res[0]); r != 0 {
			return fmt.Errorf("vt_write_n(%d) = %d (buffer cap %d)", n, r, capacity)
		}
		b = b[n:]
	}
	return nil
}

// InbufCap reports the module's input buffer size in bytes.
func (v *VT) InbufCap(ctx context.Context) uint32 { return uint32(v.call(ctx, "vt_inbuf_cap")) }

// OutbufCap reports the module's output/scratch buffer size in bytes. Anything
// the module hands back through that buffer (an encoded key, a dirty-row list,
// a hyperlink) is bounded by it.
func (v *VT) OutbufCap(ctx context.Context) uint32 { return uint32(v.call(ctx, "vt_outbuf_cap")) }

// WriteUntilGround feeds bytes and returns how many were consumed before the
// parser reached the ground state, or -1 on error.
func (v *VT) WriteUntilGround(ctx context.Context, b []byte) int32 {
	if len(b) == 0 {
		return 0
	}
	if !v.mem.Write(v.inbuf, b) {
		panic("vt: write out of range")
	}
	return i32(v.call(ctx, "vt_write_until_ground", uint64(len(b))))
}

// Ground reports whether the parser is at a stateless point.
func (v *VT) Ground(ctx context.Context) bool {
	return i32(v.call(ctx, "vt_vt_ground")) == 1
}

// ---------------------------------------------------------------- replies

// Reply returns the bytes the terminal asked to be written back to the PTY
// since the previous call, and clears them.
func (v *VT) Reply(ctx context.Context) []byte {
	n := uint32(v.call(ctx, "vt_reply_len"))
	ptr := uint32(v.call(ctx, "vt_reply_ptr"))
	out := v.read(ptr, n)
	v.call(ctx, "vt_reply_clear")
	return out
}

// ------------------------------------------------------------------ effects

// BellCount returns the number of BELs seen since the last ClearEffects.
func (v *VT) BellCount(ctx context.Context) int {
	return int(i32(v.call(ctx, "vt_bell_count")))
}

// UnknownTags returns the tags of sequences the library reported unsupported.
func (v *VT) UnknownTags(ctx context.Context) []int {
	n := int(i32(v.call(ctx, "vt_unknown_count")))
	out := make([]int, 0, n)
	for i := range n {
		out = append(out, int(i32(v.call(ctx, "vt_unknown_tag", uint64(i)))))
	}
	return out
}

// ClipboardWrites returns the OSC 52 effects seen since the last ClearEffects.
func (v *VT) ClipboardWrites(ctx context.Context) []ClipboardWrite {
	n := int(i32(v.call(ctx, "vt_clip_count")))
	out := make([]ClipboardWrite, 0, n)
	for i := range n {
		out = append(out, ClipboardWrite{
			Location: int(i32(v.call(ctx, "vt_clip_location", uint64(i)))),
			Length:   int(i32(v.call(ctx, "vt_clip_len", uint64(i)))),
		})
	}
	return out
}

// ClearEffects resets the bell, unknown-sequence and clipboard accumulators.
func (v *VT) ClearEffects(ctx context.Context) { v.call(ctx, "vt_clear_effects") }

// Title returns the terminal title, read in place from linear memory.
func (v *VT) Title(ctx context.Context) string { return v.str(ctx, "vt_title_len", "vt_title_ptr") }

// Pwd returns the OSC 7 working directory, read in place.
func (v *VT) Pwd(ctx context.Context) string { return v.str(ctx, "vt_pwd_len", "vt_pwd_ptr") }

// -------------------------------------------------------------------- state

func (v *VT) Cols(ctx context.Context) int { return int(i32(v.call(ctx, "vt_cols"))) }
func (v *VT) Rows(ctx context.Context) int { return int(i32(v.call(ctx, "vt_rows"))) }
func (v *VT) ScrollbackRows(ctx context.Context) int {
	return int(i32(v.call(ctx, "vt_scrollback_rows")))
}

func (v *VT) Cursor(ctx context.Context) (x, y int) {
	return int(i32(v.call(ctx, "vt_cursor_x"))), int(i32(v.call(ctx, "vt_cursor_y")))
}

func (v *VT) CursorPendingWrap(ctx context.Context) bool {
	return i32(v.call(ctx, "vt_cursor_pending_wrap")) == 1
}

func (v *VT) CursorVisible(ctx context.Context) bool {
	return i32(v.call(ctx, "vt_cursor_visible")) == 1
}

func (v *VT) ActiveScreen(ctx context.Context) int { return int(i32(v.call(ctx, "vt_active_screen"))) }

func (v *VT) KittyKeyboardFlags(ctx context.Context) int {
	return int(i32(v.call(ctx, "vt_kitty_keyboard_flags")))
}

// Mode reports a mode's value and whether this terminal knows the mode. It is
// the C-side query, not the DECRQM reply a program would receive.
func (v *VT) Mode(ctx context.Context, value int, ansi bool) (set, ok bool) {
	a := uint64(0)
	if ansi {
		a = 1
	}
	r := i32(v.call(ctx, "vt_mode", uint64(value), a))
	return r&1 != 0, r&2 != 0
}

// -------------------------------------------------------------------- cells

// Cell reads everything about one grid position, including its full style.
func (v *VT) Cell(ctx context.Context, x, y int) Cell {
	if i32(v.call(ctx, "vt_cell_select", uint64(x), uint64(y))) != 0 {
		return Cell{}
	}
	c := Cell{
		Grapheme: v.str(ctx, "vt_cell_grapheme_len", "vt_cell_grapheme_ptr"),
		Width:    int(i32(v.call(ctx, "vt_cell_width"))),
		HasText:  i32(v.call(ctx, "vt_cell_has_text")) == 1,
		Attrs:    uint32(v.call(ctx, "vt_cell_attrs")),
		FG: Color{
			Kind:    int(i32(v.call(ctx, "vt_cell_fg_kind"))),
			Palette: int(i32(v.call(ctx, "vt_cell_fg_palette"))),
			RGB:     uint32(v.call(ctx, "vt_cell_fg_rgb")),
		},
		BG: Color{
			Kind:    int(i32(v.call(ctx, "vt_cell_bg_kind"))),
			Palette: int(i32(v.call(ctx, "vt_cell_bg_palette"))),
			RGB:     uint32(v.call(ctx, "vt_cell_bg_rgb")),
		},
		UL: Color{
			Kind: int(i32(v.call(ctx, "vt_cell_underline_kind"))),
			RGB:  uint32(v.call(ctx, "vt_cell_underline_rgb")),
		},
		Underline: int(i32(v.call(ctx, "vt_cell_underline"))),
	}
	if n := uint32(v.call(ctx, "vt_cell_hyperlink_len")); n > 0 {
		c.Hyperlink = string(v.read(uint32(v.call(ctx, "vt_cell_hyperlink_ptr")), n))
	}
	return c
}

// RowWrap reports whether row y soft-wraps and whether it continues the row
// above — the per-line state the card serialiser's isWrapped path needs.
func (v *VT) RowWrap(ctx context.Context, y int) (wrap, continuation bool) {
	r := i32(v.call(ctx, "vt_row_wrap", uint64(y)))
	if r < 0 {
		return false, false
	}
	return r&1 != 0, r&2 != 0
}

// --------------------------------------------------------------- render state

// RSOpen creates the incremental render state.
func (v *VT) RSOpen(ctx context.Context) int32 { return i32(v.call(ctx, "vt_rs_new")) }

// RSUpdate refreshes the state from the terminal and marks new damage.
func (v *VT) RSUpdate(ctx context.Context) int32 { return i32(v.call(ctx, "vt_rs_update")) }

// RSDirty reports the global dirty class.
func (v *VT) RSDirty(ctx context.Context) int { return int(i32(v.call(ctx, "vt_rs_dirty"))) }

// RSDirtyRows returns the viewport rows needing a redraw.
func (v *VT) RSDirtyRows(ctx context.Context) []int {
	n := int(i32(v.call(ctx, "vt_rs_dirty_rows")))
	raw := v.read(v.outbuf, uint32(n*2))
	out := make([]int, 0, n)
	for i := 0; i+1 < len(raw); i += 2 {
		out = append(out, int(uint16(raw[i])|uint16(raw[i+1])<<8))
	}
	return out
}

// RSClean marks the frame drawn.
func (v *VT) RSClean(ctx context.Context) int32 { return i32(v.call(ctx, "vt_rs_clean")) }

// RSFree releases the render state.
func (v *VT) RSFree(ctx context.Context) { v.call(ctx, "vt_rs_free") }

// ------------------------------------------------------------ key encoding

// KeyEncode encodes one key press. kittyFlags selects the Kitty keyboard
// protocol feature set (0 = legacy); fromTerminal derives the mode-dependent
// settings from the terminal's own state instead of the caller's defaults.
func (v *VT) KeyEncode(ctx context.Context, key, mods, kittyFlags int, fromTerminal bool) []byte {
	ft := uint64(0)
	if fromTerminal {
		ft = 1
	}
	n := i32(v.call(ctx, "vt_key_encode", uint64(int64(key)), uint64(mods), uint64(kittyFlags), ft))
	if n < 0 {
		return nil
	}
	return v.read(v.outbuf, uint32(n))
}

// ------------------------------------------------------------- ABI constants

// Key identities and modifier bits, read from the module so no enum value is
// transcribed into Go.
func (v *VT) KeyArrowLeft(ctx context.Context) int { return int(i32(v.call(ctx, "vt_key_arrow_left"))) }

func (v *VT) KeyArrowUp(ctx context.Context) int  { return int(i32(v.call(ctx, "vt_key_arrow_up"))) }
func (v *VT) KeyF1(ctx context.Context) int       { return int(i32(v.call(ctx, "vt_key_f1"))) }
func (v *VT) KeyF5(ctx context.Context) int       { return int(i32(v.call(ctx, "vt_key_f5"))) }
func (v *VT) KeyF12(ctx context.Context) int      { return int(i32(v.call(ctx, "vt_key_f12"))) }
func (v *VT) KeyF13(ctx context.Context) int      { return int(i32(v.call(ctx, "vt_key_f13"))) }
func (v *VT) ModShift(ctx context.Context) int    { return int(i32(v.call(ctx, "vt_mod_shift"))) }
func (v *VT) ModCtrl(ctx context.Context) int     { return int(i32(v.call(ctx, "vt_mod_ctrl"))) }
func (v *VT) ModAlt(ctx context.Context) int      { return int(i32(v.call(ctx, "vt_mod_alt"))) }
func (v *VT) KittyKeyAll(ctx context.Context) int { return int(i32(v.call(ctx, "vt_kitty_key_all"))) }

// ------------------------------------------------------------------ graphics

// KittyGraphics reports whether this build exposes kitty image storage.
// Negative means the feature was compiled out entirely, which is what the
// freestanding target does.
func (v *VT) KittyGraphics(ctx context.Context) int {
	return int(i32(v.call(ctx, "vt_kitty_graphics")))
}

// KittyImagePresent reports whether image id was decoded into storage.
func (v *VT) KittyImagePresent(ctx context.Context, id uint32) int {
	return int(i32(v.call(ctx, "vt_kitty_image_present", uint64(id))))
}

// BuildInfo reads a compile-time capability flag.
func (v *VT) BuildInfo(ctx context.Context, which int) int {
	return int(i32(v.call(ctx, "vt_build_info", uint64(which))))
}

// ------------------------------------------------------------------ helpers

func (v *VT) read(ptr, n uint32) []byte {
	if n == 0 {
		return nil
	}
	b, ok := v.mem.Read(ptr, n)
	if !ok {
		panic("vt: read out of range")
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func (v *VT) str(ctx context.Context, lenFn, ptrFn string) string {
	n := uint32(v.call(ctx, lenFn))
	if n == 0 {
		return ""
	}
	return string(v.read(uint32(v.call(ctx, ptrFn)), n))
}
