/* Flat-ABI shim over libghostty-vt, for a wasm32-freestanding module driven
 * from Go under wazero.
 *
 * Why a shim at all: libghostty-vt's C API is pointer-and-sized-struct shaped
 * (GHOSTTY_INIT_SIZED, out-params, tagged unions). The C compiler already owns
 * that ABI, so this file keeps it entirely inside the module and exposes a
 * surface a host can drive without knowing a single struct layout: scalar
 * functions, plus pointers into this module's linear memory for the three
 * things that are genuinely variable-length — the input buffer, the output
 * buffer, and whatever the terminal currently holds (a title, a hyperlink,
 * a reply).
 *
 * Everything the session-runtime design needs is reachable through here; see
 * REPORT.md for which of those the wasm build was measured to carry.
 *
 *   lifecycle   vt_new vt_free vt_reset vt_resize vt_set_scrollback_lines
 *   input       vt_inbuf vt_write_n vt_write_until_ground
 *   replies     vt_reply_ptr vt_reply_len vt_reply_clear
 *   effects     vt_bell_count vt_unknown_count vt_unknown_tag
 *               vt_clip_count vt_clip_location vt_clip_len
 *   state       vt_title_ptr/len vt_pwd_ptr/len vt_mode vt_cols vt_rows
 *               vt_cursor_x/y vt_cursor_pending_wrap vt_active_screen
 *               vt_cursor_visible vt_scrollback_rows vt_kitty_graphics
 *   cells       vt_cell_select vt_cell_has_text vt_cell_width
 *               vt_cell_grapheme_ptr/len vt_cell_fg_kind vt_cell_fg_palette
 *               vt_cell_fg_rgb vt_cell_bg_* vt_cell_ul_* vt_cell_attrs
 *               vt_cell_underline vt_cell_hyperlink_ptr/len
 *   rows        vt_row_wrap (bit 0 wrap, bit 1 continuation)
 *   render      vt_rs_new vt_rs_update vt_rs_dirty vt_rs_dirty_rows
 *               vt_rs_clean vt_rs_free
 *   key         vt_key_encode (into the output buffer)
 *   kitty       vt_kitty_image_present
 *   build       vt_build_info
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include <ghostty/vt.h>

#define EXPORT __attribute__((used, visibility("default")))

/* Buffer sizes are build-time knobs because they are the shim's own
 * contribution to every instance's linear memory: this module is instantiated
 * once per session, so 2 MiB of scratch is 2 MiB per session. The defaults are
 * generous enough for any single PTY read; build.sh can shrink them and the
 * measurement in REPORT.md is what decides the size. */
#ifndef INBUF_KB
#define INBUF_KB 1024
#endif
#ifndef OUTBUF_KB
#define OUTBUF_KB 1024
#endif

#define INBUF_CAP (INBUF_KB * 1024u)
#define OUTBUF_CAP (OUTBUF_KB * 1024u)
#define REPLY_CAP (64u << 10)
#define TAG_MAX 64
#define CLIP_MAX 16
#define CP_MAX 64

static GhosttyTerminal T = NULL;
static GhosttyRenderState RS = NULL;

static uint8_t INBUF[INBUF_CAP];
static uint8_t OUTBUF[OUTBUF_CAP];
static uint8_t REPLYBUF[REPLY_CAP];
static size_t REPLY_LEN = 0;

/* Scratch for the cell the host last selected. The style is cached because
 * reading it is a separate call and the host asks for a dozen fields of it. */
static GhosttyGridRef CELL;
static GhosttyCell CELLV = 0;
static bool CELL_OK = false;
static GhosttyStyle STYLE;
static uint8_t UTF8[256];
static size_t UTF8_LEN = 0;

static size_t BELLS = 0;
static int32_t TAGS[TAG_MAX];
static size_t TAGS_N = 0;
static int32_t CLIP_LOC[CLIP_MAX];
static size_t CLIP_LEN[CLIP_MAX];
static size_t CLIP_N = 0;

/* ---------------------------------------------------------------- effects */

static void on_write_pty(GhosttyTerminal t, void *ud, const uint8_t *data,
                         size_t len) {
  (void)t;
  (void)ud;
  /* Same hazard as a native binding: the parser is blocked here and the bytes
   * are borrowed, so this must be a copy and never a carrier write. */
  if (REPLY_LEN + len > REPLY_CAP) len = REPLY_CAP - REPLY_LEN;
  for (size_t i = 0; i < len; i++) REPLYBUF[REPLY_LEN + i] = data[i];
  REPLY_LEN += len;
}

static void on_bell(GhosttyTerminal t, void *ud) {
  (void)t;
  (void)ud;
  BELLS++;
}

static void on_clipboard_write(GhosttyTerminal t, void *ud,
                               const GhosttyClipboardWrite *w) {
  (void)t;
  (void)ud;
  if (CLIP_N >= CLIP_MAX) return;
  CLIP_LOC[CLIP_N] = (int32_t)w->location;
  CLIP_LEN[CLIP_N] = (size_t)w->contents_len;
  CLIP_N++;
}

static void on_unknown(GhosttyTerminal t, void *ud,
                       const GhosttyTerminalUnknownSequence *s) {
  (void)t;
  (void)ud;
  if (TAGS_N >= TAG_MAX) return;
  TAGS[TAGS_N++] = (int32_t)s->tag;
}

static GhosttyResult install(void) {
  GhosttyResult r;
  size_t unknown_max_bytes = 256;

  r = ghostty_terminal_set(T, GHOSTTY_TERMINAL_OPT_WRITE_PTY,
                           (const void *)on_write_pty);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(T, GHOSTTY_TERMINAL_OPT_BELL,
                           (const void *)on_bell);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(T, GHOSTTY_TERMINAL_OPT_CLIPBOARD_WRITE,
                           (const void *)on_clipboard_write);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(T, GHOSTTY_TERMINAL_OPT_UNKNOWN_SEQUENCE,
                           (const void *)on_unknown);
  if (r != GHOSTTY_SUCCESS) return r;
  /* The callback alone retains nothing; capture needs a nonzero byte limit. */
  return ghostty_terminal_set(T, GHOSTTY_TERMINAL_OPT_UNKNOWN_MAX_BYTES,
                              &unknown_max_bytes);
}

/* -------------------------------------------------------------- lifecycle */

EXPORT uint8_t *vt_inbuf(void) { return INBUF; }
EXPORT uint8_t *vt_outbuf(void) { return OUTBUF; }

EXPORT int32_t vt_new(uint32_t cols, uint32_t rows) {
  if (T) {
    ghostty_terminal_free(T);
    T = NULL;
  }
  CELL_OK = false;
  GhosttyResult r = ghostty_terminal_new(NULL, &T, (uint16_t)cols,
                                         (uint16_t)rows);
  if (r != GHOSTTY_SUCCESS) return (int32_t)r;
  return (int32_t)install();
}

EXPORT void vt_free(void) {
  if (RS) {
    ghostty_render_state_free(RS);
    RS = NULL;
  }
  if (T) {
    ghostty_terminal_free(T);
    T = NULL;
  }
}

EXPORT void vt_reset(void) { ghostty_terminal_reset(T); }

EXPORT int32_t vt_resize(uint32_t cols, uint32_t rows) {
  return (int32_t)ghostty_terminal_resize(T, (uint16_t)cols, (uint16_t)rows,
                                          10, 20);
}

EXPORT int32_t vt_set_scrollback_lines(uint32_t n) {
  size_t v = (size_t)n;
  return (int32_t)ghostty_terminal_set(
      T, GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_LINES, &v);
}

/* ------------------------------------------------------------------ input */

/* Refuses an oversized write rather than truncating it: a silently clipped
 * escape sequence is a corrupted screen, and the host can always chunk. */
EXPORT int32_t vt_write_n(uint32_t n) {
  if (!T) return -1;
  if ((size_t)n > sizeof(INBUF)) return -2;
  ghostty_terminal_vt_write(T, INBUF, (size_t)n);
  return 0;
}

EXPORT uint32_t vt_inbuf_cap(void) { return (uint32_t)sizeof(INBUF); }
EXPORT uint32_t vt_outbuf_cap(void) { return (uint32_t)sizeof(OUTBUF); }

/* Feed until the parser is back in the ground state, so a caller can tell a
 * completed sequence from a truncated one. Returns the number of BYTES
 * consumed (compare against n), or -1 when the library errored. */
EXPORT int32_t vt_write_until_ground(uint32_t n) {
  if (!T) return -1;
  if ((size_t)n > sizeof(INBUF)) n = sizeof(INBUF);
  size_t consumed = 0;
  GhosttyResult r = ghostty_terminal_vt_write_until_ground(T, INBUF, (size_t)n,
                                                           &consumed);
  if (r != GHOSTTY_SUCCESS) return -1;
  return (int32_t)consumed;
}

EXPORT int32_t vt_vt_ground(void) {
  bool ground = false;
  GhosttyResult r =
      ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_VT_GROUND, &ground);
  if (r != GHOSTTY_SUCCESS) return -1;
  return ground ? 1 : 0;
}

/* ---------------------------------------------------------------- replies */

EXPORT uint8_t *vt_reply_ptr(void) { return REPLYBUF; }
EXPORT uint32_t vt_reply_len(void) { return (uint32_t)REPLY_LEN; }
EXPORT void vt_reply_clear(void) { REPLY_LEN = 0; }

/* ---------------------------------------------------------------- effects */

EXPORT uint32_t vt_bell_count(void) { return (uint32_t)BELLS; }
EXPORT void vt_clear_effects(void) {
  BELLS = 0;
  TAGS_N = 0;
  CLIP_N = 0;
}

EXPORT uint32_t vt_unknown_count(void) { return (uint32_t)TAGS_N; }
EXPORT int32_t vt_unknown_tag(uint32_t i) {
  return i < TAGS_N ? TAGS[i] : -1;
}

EXPORT uint32_t vt_clip_count(void) { return (uint32_t)CLIP_N; }
EXPORT int32_t vt_clip_location(uint32_t i) {
  return i < CLIP_N ? CLIP_LOC[i] : -1;
}
EXPORT uint32_t vt_clip_len(uint32_t i) {
  return i < CLIP_N ? (uint32_t)CLIP_LEN[i] : 0;
}

/* Title and cwd are borrowed from the terminal, which lives in this same
 * linear memory: the host reads them in place rather than through a copy. */
static const uint8_t *title = NULL;
static size_t title_len = 0;

static void refresh_title(void) {
  GhosttyString s = {0};
  title = NULL;
  title_len = 0;
  if (!T) return;
  if (ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_TITLE, &s) !=
      GHOSTTY_SUCCESS)
    return;
  title = s.ptr;
  title_len = s.len;
}

EXPORT uint32_t vt_title_len(void) {
  refresh_title();
  return (uint32_t)title_len;
}
EXPORT uint32_t vt_title_ptr(void) {
  refresh_title();
  return (uint32_t)(uintptr_t)title;
}

static const uint8_t *pwd = NULL;
static size_t pwd_len = 0;

EXPORT uint32_t vt_pwd_len(void) {
  GhosttyString s = {0};
  pwd = NULL;
  pwd_len = 0;
  if (!T) return 0;
  if (ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_PWD, &s) != GHOSTTY_SUCCESS)
    return 0;
  pwd = s.ptr;
  pwd_len = s.len;
  return (uint32_t)pwd_len;
}
EXPORT uint32_t vt_pwd_ptr(void) { return (uint32_t)(uintptr_t)pwd; }

/* ------------------------------------------------------------------ state */

/* Each of these uses the LOCAL TYPE the header declares for that data kind.
 * A generic uint16_t out-param would be wrong in both directions: a 4-byte
 * enum written through a 2-byte local is a stack overwrite, and a bool read as
 * 0x0100 reports set when it is false. The header's "Output type:" line is the
 * authority for each. */
static uint16_t u16(GhosttyTerminalData d) {
  uint16_t v = 0;
  if (!T) return 0;
  ghostty_terminal_get(T, d, &v);
  return v;
}

static bool boolean(GhosttyTerminalData d) {
  bool v = false;
  if (!T) return false;
  if (ghostty_terminal_get(T, d, &v) != GHOSTTY_SUCCESS) return false;
  return v;
}

EXPORT uint32_t vt_cols(void) { return u16(GHOSTTY_TERMINAL_DATA_COLS); }
EXPORT uint32_t vt_rows(void) { return u16(GHOSTTY_TERMINAL_DATA_ROWS); }
EXPORT uint32_t vt_cursor_x(void) { return u16(GHOSTTY_TERMINAL_DATA_CURSOR_X); }
EXPORT uint32_t vt_cursor_y(void) { return u16(GHOSTTY_TERMINAL_DATA_CURSOR_Y); }
EXPORT uint32_t vt_cursor_pending_wrap(void) {
  return boolean(GHOSTTY_TERMINAL_DATA_CURSOR_PENDING_WRAP) ? 1 : 0;
}
EXPORT uint32_t vt_cursor_visible(void) {
  return boolean(GHOSTTY_TERMINAL_DATA_CURSOR_VISIBLE) ? 1 : 0;
}
EXPORT uint32_t vt_active_screen(void) {
  GhosttyTerminalScreen v = GHOSTTY_TERMINAL_SCREEN_PRIMARY;
  if (!T) return 0;
  if (ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_ACTIVE_SCREEN, &v) !=
      GHOSTTY_SUCCESS)
    return 0;
  return (uint32_t)v;
}
EXPORT uint32_t vt_kitty_keyboard_flags(void) {
  GhosttyKittyKeyFlags v = 0;
  if (!T) return 0;
  if (ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_KITTY_KEYBOARD_FLAGS, &v) !=
      GHOSTTY_SUCCESS)
    return 0;
  return (uint32_t)v;
}
EXPORT uint32_t vt_scrollback_rows(void) {
  size_t v = 0;
  if (!T) return 0;
  ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS, &v);
  return (uint32_t)v;
}

/* bit 0 = the mode is set, bit 1 = this terminal knows the mode. */
EXPORT int32_t vt_mode(uint32_t value, uint32_t ansi) {
  GhosttyTerminalModeConfig cfg = {0};
  cfg.mode = ghostty_mode_new((uint16_t)value, ansi != 0);
  GhosttyResult r =
      ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_MODE, &cfg);
  if (r != GHOSTTY_SUCCESS) return -1;
  return (cfg.value ? 1 : 0) | 2;
}

/* ------------------------------------------------------------------- cells */

static GhosttyResult ref_at(uint32_t x, uint32_t y) {
  GhosttyPoint pt = {0};
  pt.tag = GHOSTTY_POINT_TAG_ACTIVE;
  pt.value.coordinate.x = (uint16_t)x;
  pt.value.coordinate.y = (uint16_t)y;
  CELL = GHOSTTY_INIT_SIZED(GhosttyGridRef);
  return ghostty_terminal_grid_ref(T, pt, &CELL);
}

static size_t utf8_encode(uint32_t cp, uint8_t *out) {
  if (cp < 0x80) {
    out[0] = (uint8_t)cp;
    return 1;
  }
  if (cp < 0x800) {
    out[0] = (uint8_t)(0xC0 | (cp >> 6));
    out[1] = (uint8_t)(0x80 | (cp & 0x3F));
    return 2;
  }
  if (cp < 0x10000) {
    out[0] = (uint8_t)(0xE0 | (cp >> 12));
    out[1] = (uint8_t)(0x80 | ((cp >> 6) & 0x3F));
    out[2] = (uint8_t)(0x80 | (cp & 0x3F));
    return 3;
  }
  out[0] = (uint8_t)(0xF0 | (cp >> 18));
  out[1] = (uint8_t)(0x80 | ((cp >> 12) & 0x3F));
  out[2] = (uint8_t)(0x80 | ((cp >> 6) & 0x3F));
  out[3] = (uint8_t)(0x80 | (cp & 0x3F));
  return 4;
}

/* Selects a cell and materialises everything about it the design's card
 * format asks for: the grapheme cluster, its column footprint, whether it
 * holds text, the full style, and the hyperlink. Returns 0 on success. */
EXPORT int32_t vt_cell_select(uint32_t x, uint32_t y) {
  CELL_OK = false;
  UTF8_LEN = 0;
  if (!T) return -1;
  if (ref_at(x, y) != GHOSTTY_SUCCESS) return -1;

  if (ghostty_grid_ref_cell(&CELL, &CELLV) != GHOSTTY_SUCCESS) return -1;

  uint32_t cps[CP_MAX];
  size_t ncp = CP_MAX;
  GhosttyResult r = ghostty_grid_ref_graphemes(&CELL, cps, CP_MAX, &ncp);
  if (r == GHOSTTY_OUT_OF_SPACE) return -2; /* impossible at CP_MAX, reported not truncated */
  if (r != GHOSTTY_SUCCESS) ncp = 0;
  for (size_t i = 0; i < ncp && UTF8_LEN + 4 < sizeof(UTF8); i++)
    UTF8_LEN += utf8_encode(cps[i], &UTF8[UTF8_LEN]);

  STYLE = GHOSTTY_INIT_SIZED(GhosttyStyle);
  if (ghostty_grid_ref_style(&CELL, &STYLE) != GHOSTTY_SUCCESS)
    STYLE = GHOSTTY_INIT_SIZED(GhosttyStyle);

  CELL_OK = true;
  return 0;
}

EXPORT uint32_t vt_cell_grapheme_ptr(void) { return (uint32_t)(uintptr_t)UTF8; }
EXPORT uint32_t vt_cell_grapheme_len(void) { return (uint32_t)UTF8_LEN; }

EXPORT int32_t vt_cell_has_text(void) {
  if (!CELL_OK) return 0;
  bool v = false;
  if (ghostty_cell_get(CELLV, GHOSTTY_CELL_DATA_HAS_TEXT, &v) != GHOSTTY_SUCCESS)
    return 0;
  return v ? 1 : 0;
}

EXPORT int32_t vt_cell_width(void) {
  if (!CELL_OK) return -1;
  GhosttyCellWide w = GHOSTTY_CELL_WIDE_NARROW;
  if (ghostty_cell_get(CELLV, GHOSTTY_CELL_DATA_WIDE, &w) != GHOSTTY_SUCCESS)
    return -1;
  return (int32_t)w;
}

/* 0 = default, 1 = palette index, 2 = direct RGB. */
static int32_t style_color_kind(GhosttyStyleColor c) {
  if (c.tag == GHOSTTY_STYLE_COLOR_PALETTE) return 1;
  if (c.tag == GHOSTTY_STYLE_COLOR_RGB) return 2;
  return 0;
}

static uint32_t rgb_of(GhosttyColorRgb c) {
  return ((uint32_t)c.r << 16) | ((uint32_t)c.g << 8) | (uint32_t)c.b;
}

EXPORT int32_t vt_cell_fg_kind(void) {
  return CELL_OK ? style_color_kind(STYLE.fg_color) : -1;
}
EXPORT int32_t vt_cell_fg_palette(void) {
  return CELL_OK && STYLE.fg_color.tag == GHOSTTY_STYLE_COLOR_PALETTE
             ? (int32_t)STYLE.fg_color.value.palette
             : -1;
}
EXPORT uint32_t vt_cell_fg_rgb(void) {
  return CELL_OK && STYLE.fg_color.tag == GHOSTTY_STYLE_COLOR_RGB
             ? rgb_of(STYLE.fg_color.value.rgb)
             : 0;
}

EXPORT int32_t vt_cell_bg_kind(void) {
  return CELL_OK ? style_color_kind(STYLE.bg_color) : -1;
}
EXPORT int32_t vt_cell_bg_palette(void) {
  return CELL_OK && STYLE.bg_color.tag == GHOSTTY_STYLE_COLOR_PALETTE
             ? (int32_t)STYLE.bg_color.value.palette
             : -1;
}
EXPORT uint32_t vt_cell_bg_rgb(void) {
  return CELL_OK && STYLE.bg_color.tag == GHOSTTY_STYLE_COLOR_RGB
             ? rgb_of(STYLE.bg_color.value.rgb)
             : 0;
}

EXPORT int32_t vt_cell_underline_kind(void) {
  return CELL_OK ? style_color_kind(STYLE.underline_color) : -1;
}
EXPORT uint32_t vt_cell_underline_rgb(void) {
  return CELL_OK && STYLE.underline_color.tag == GHOSTTY_STYLE_COLOR_RGB
             ? rgb_of(STYLE.underline_color.value.rgb)
             : 0;
}

#define ATTR_BOLD (1u << 0)
#define ATTR_ITALIC (1u << 1)
#define ATTR_FAINT (1u << 2)
#define ATTR_BLINK (1u << 3)
#define ATTR_INVERSE (1u << 4)
#define ATTR_INVISIBLE (1u << 5)
#define ATTR_STRIKETHROUGH (1u << 6)
#define ATTR_OVERLINE (1u << 7)

EXPORT uint32_t vt_cell_attrs(void) {
  if (!CELL_OK) return 0;
  uint32_t a = 0;
  if (STYLE.bold) a |= ATTR_BOLD;
  if (STYLE.italic) a |= ATTR_ITALIC;
  if (STYLE.faint) a |= ATTR_FAINT;
  if (STYLE.blink) a |= ATTR_BLINK;
  if (STYLE.inverse) a |= ATTR_INVERSE;
  if (STYLE.invisible) a |= ATTR_INVISIBLE;
  if (STYLE.strikethrough) a |= ATTR_STRIKETHROUGH;
  if (STYLE.overline) a |= ATTR_OVERLINE;
  return a;
}

EXPORT int32_t vt_cell_underline(void) {
  return CELL_OK ? (int32_t)STYLE.underline : -1;
}

/* Hyperlink URI bytes for the selected cell, 0 when it has none. Copying is
 * the caller's business; the pointer is into this module's memory. */
static const uint8_t *link = NULL;
static size_t link_len = 0;

EXPORT uint32_t vt_cell_hyperlink_len(void) {
  link = NULL;
  link_len = 0;
  if (!CELL_OK) return 0;
  size_t n = 0;
  GhosttyResult r = ghostty_grid_ref_hyperlink_uri(&CELL, OUTBUF, 4096, &n);
  if (r != GHOSTTY_SUCCESS) return 0;
  link = OUTBUF;
  link_len = n;
  return (uint32_t)n;
}
EXPORT uint32_t vt_cell_hyperlink_ptr(void) {
  return (uint32_t)(uintptr_t)link;
}

/* -------------------------------------------------------------------- rows */

/* bit 0: the row soft-wraps. bit 1: it continues a soft-wrapped row above. */
EXPORT int32_t vt_row_wrap(uint32_t y) {
  if (!T) return -1;
  if (ref_at(0, y) != GHOSTTY_SUCCESS) return -1;
  GhosttyRow row = 0;
  if (ghostty_grid_ref_row(&CELL, &row) != GHOSTTY_SUCCESS) return -1;
  bool w = false, c = false;
  if (ghostty_row_get(row, GHOSTTY_ROW_DATA_WRAP, &w) != GHOSTTY_SUCCESS)
    return -1;
  if (ghostty_row_get(row, GHOSTTY_ROW_DATA_WRAP_CONTINUATION, &c) !=
      GHOSTTY_SUCCESS)
    return -1;
  return (w ? 1 : 0) | (c ? 2 : 0);
}

/* --------------------------------------------------------- render state */

EXPORT int32_t vt_rs_new(void) {
  if (RS) {
    ghostty_render_state_free(RS);
    RS = NULL;
  }
  return (int32_t)ghostty_render_state_new(NULL, &RS);
}

EXPORT int32_t vt_rs_update(void) {
  return RS ? (int32_t)ghostty_render_state_update(RS, T) : -1;
}

EXPORT int32_t vt_rs_dirty(void) {
  if (!RS) return -1;
  GhosttyRenderStateDirty d;
  if (ghostty_render_state_get(RS, GHOSTTY_RENDER_STATE_DATA_DIRTY, &d) !=
      GHOSTTY_SUCCESS)
    return -1;
  return (int32_t)d;
}

/* Writes the dirty viewport rows into OUTBUF as u16 and returns the count.
 * OUTBUF is compared as a byte buffer by the host. */
EXPORT uint32_t vt_rs_dirty_rows(void) {
  if (!RS) return 0;
  GhosttyRenderStateRowIterator it = NULL;
  if (ghostty_render_state_row_iterator_new(NULL, &it) != GHOSTTY_SUCCESS)
    return 0;
  if (ghostty_render_state_get(RS, GHOSTTY_RENDER_STATE_DATA_ROW_ITERATOR,
                               &it) != GHOSTTY_SUCCESS) {
    ghostty_render_state_row_iterator_free(it);
    return 0;
  }
  uint16_t *rows = (uint16_t *)OUTBUF;
  size_t max = OUTBUF_CAP / sizeof(uint16_t);
  size_t n = 0;
  uint16_t y = 0;
  while (n < max && ghostty_render_state_row_iterator_next_dirty(it, &y))
    rows[n++] = y;
  ghostty_render_state_row_iterator_free(it);
  return (uint32_t)n;
}

EXPORT int32_t vt_rs_clean(void) {
  return RS ? (int32_t)ghostty_render_state_clean(RS) : -1;
}

EXPORT void vt_rs_free(void) {
  if (RS) {
    ghostty_render_state_free(RS);
    RS = NULL;
  }
}

/* ------------------------------------------------------------ key encoding */

/* Encodes one key press into OUTBUF and returns its length, or negative on
 * error. `from_terminal` derives the protocol and mode settings from the
 * terminal's own state (application cursor keys, Kitty flags, modifyOtherKeys)
 * rather than from the caller's guess. */
EXPORT int32_t vt_key_encode(int32_t key, uint32_t mods, uint32_t kitty_flags,
                             uint32_t from_terminal) {
  GhosttyKeyEncoder enc = NULL;
  if (ghostty_key_encoder_new(NULL, &enc) != GHOSTTY_SUCCESS) return -10;
  if (kitty_flags != 0) {
    uint8_t f = (uint8_t)kitty_flags;
    ghostty_key_encoder_setopt(enc, GHOSTTY_KEY_ENCODER_OPT_KITTY_FLAGS, &f);
  }
  if (from_terminal && T)
    ghostty_key_encoder_setopt_from_terminal(enc, T);

  GhosttyKeyEvent ev = NULL;
  if (ghostty_key_event_new(NULL, &ev) != GHOSTTY_SUCCESS) {
    ghostty_key_encoder_free(enc);
    return -11;
  }
  ghostty_key_event_set_action(ev, GHOSTTY_KEY_ACTION_PRESS);
  ghostty_key_event_set_key(ev, (GhosttyKey)key);
  ghostty_key_event_set_mods(ev, (GhosttyMods)mods);

  size_t n = 0;
  GhosttyResult r =
      ghostty_key_encoder_encode(enc, ev, (char *)OUTBUF, OUTBUF_CAP, &n);
  ghostty_key_event_free(ev);
  ghostty_key_encoder_free(enc);
  if (r != GHOSTTY_SUCCESS) return -(int32_t)r;
  return (int32_t)n;
}

/* ---------------------------------------------------------------- graphics */

/* Kitty graphics is force-disabled upstream on freestanding targets
 * (build_options.zig: kittyGraphics() returns false for .freestanding,
 * whatever the feature flag says), so the C API's kitty symbols are not in the
 * wasm archive at all. build.sh greps the archive and defines NOCX_HAVE_KITTY
 * accordingly, so this compiles against what was actually built rather than
 * against what the header promises. vt_build_info(GHOSTTY_BUILD_INFO_KITTY_
 * GRAPHICS) is the runtime authority for the same fact. */
#if NOCX_HAVE_KITTY
EXPORT int32_t vt_kitty_graphics(void) {
  if (!T) return 0;
  GhosttyKittyGraphics g = NULL;
  if (ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_KITTY_GRAPHICS, &g) !=
      GHOSTTY_SUCCESS)
    return 0;
  return g != NULL ? 1 : 0;
}

EXPORT int32_t vt_kitty_image_present(uint32_t id) {
  if (!T) return 0;
  GhosttyKittyGraphics g = NULL;
  if (ghostty_terminal_get(T, GHOSTTY_TERMINAL_DATA_KITTY_GRAPHICS, &g) !=
      GHOSTTY_SUCCESS)
    return 0;
  return ghostty_kitty_graphics_image(g, id) != NULL ? 1 : 0;
}
#else
EXPORT int32_t vt_kitty_graphics(void) { return -2; }
EXPORT int32_t vt_kitty_image_present(uint32_t id) {
  (void)id;
  return -2;
}
#endif

/* --------------------------------------------------------- ABI constants */

/* Enum and macro values the host needs, exported rather than transcribed.
 * GhosttyKey is a long sequential enum and the header is upstream's to
 * reorder; a Go file with 1001 in it is a bug waiting for the next bump. */
EXPORT int32_t vt_key_arrow_left(void) { return (int32_t)GHOSTTY_KEY_ARROW_LEFT; }
EXPORT int32_t vt_key_arrow_up(void) { return (int32_t)GHOSTTY_KEY_ARROW_UP; }
EXPORT int32_t vt_key_f1(void) { return (int32_t)GHOSTTY_KEY_F1; }
EXPORT int32_t vt_key_f5(void) { return (int32_t)GHOSTTY_KEY_F5; }
EXPORT int32_t vt_key_f12(void) { return (int32_t)GHOSTTY_KEY_F12; }
EXPORT int32_t vt_key_f13(void) { return (int32_t)GHOSTTY_KEY_F13; }
EXPORT uint32_t vt_mod_shift(void) { return (uint32_t)GHOSTTY_MODS_SHIFT; }
EXPORT uint32_t vt_mod_ctrl(void) { return (uint32_t)GHOSTTY_MODS_CTRL; }
EXPORT uint32_t vt_mod_alt(void) { return (uint32_t)GHOSTTY_MODS_ALT; }
EXPORT uint32_t vt_kitty_key_all(void) { return (uint32_t)GHOSTTY_KITTY_KEY_ALL; }
EXPORT uint32_t vt_color_palette_tag(void) { return (uint32_t)GHOSTTY_STYLE_COLOR_PALETTE; }
EXPORT uint32_t vt_color_rgb_tag(void) { return (uint32_t)GHOSTTY_STYLE_COLOR_RGB; }

EXPORT int32_t vt_build_info(uint32_t which) {
  bool v = false;
  if (ghostty_build_info((GhosttyBuildInfo)which, &v) != GHOSTTY_SUCCESS)
    return -1;
  return v ? 1 : 0;
}

EXPORT uint32_t vt_build_version(void) {
  GhosttyString s = {0};
  if (ghostty_build_info(GHOSTTY_BUILD_INFO_VERSION_STRING, &s) !=
      GHOSTTY_SUCCESS)
    return 0;
  return (uint32_t)s.len;
}
