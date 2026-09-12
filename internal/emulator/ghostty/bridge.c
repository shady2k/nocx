#include "bridge.h"

/* ------------------------------------------------------------------ effects */

/*
 * The program's own replies: a device status report, a mode query, an in-band
 * size report. The bytes are BORROWED — they live only for the duration of
 * this call, which is the library's parser writing to the PTY — so the Go side
 * copies them before it returns.
 */
static void cb_write_pty(GhosttyTerminal terminal, void *userdata,
                         const uint8_t *data, size_t len) {
  (void)terminal;
  /* The library's callback type is const-qualified and cgo's generated header
     for the exported function is not, so the qualifier is dropped here rather
     than in a second declaration of the same function. */
  nocxGoWritePty((uintptr_t)userdata, (uint8_t *)data, len);
}

/*
 * Device attributes (CSI c, CSI > c, CSI = c). The library FORMATS the reply
 * from what this callback reports, and without a callback it ignores the query
 * entirely and the program hears nothing at all.
 *
 * The identity is not invented here. It is what Ghostty itself answers —
 * src/termio/stream_handler.zig:796, "we quack as a VT220. We don't quack as a
 * 420 because we don't support DCS sequences" — minus the clipboard feature it
 * adds when it serves OSC 52 reads, which nocx does not (design §6.2 gives
 * clipboard delivery to the runtime, not to the terminal). So: level 2
 * conformance with colour, a VT220 device type, and a firmware version of 0
 * because an emulator's has no meaning.
 */
static bool cb_device_attributes(GhosttyTerminal terminal, void *userdata,
                                 GhosttyDeviceAttributes *out) {
  (void)terminal;
  (void)userdata;
  out->primary.conformance_level = GHOSTTY_DA_CONFORMANCE_VT220;
  out->primary.features[0] = GHOSTTY_DA_FEATURE_ANSI_COLOR;
  out->primary.num_features = 1;
  out->secondary.device_type = GHOSTTY_DA_DEVICE_TYPE_VT220;
  out->secondary.firmware_version = 0;
  out->secondary.rom_cartridge = 0;
  out->tertiary.unit_id = 0;
  return true;
}

/*
 * XTWINOPS size queries (CSI 14/16/18 t) and the in-band size reports of mode
 * 2048. Every number comes from the terminal itself, which the adapter keeps
 * sized through ghostty_terminal_resize, so this is the terminal answering
 * about its own geometry rather than the adapter inventing one. A terminal
 * whose geometry is zero — which ghostty_terminal_new cannot produce and
 * resize refuses — reports nothing rather than reporting zeroes.
 */
static bool cb_size(GhosttyTerminal terminal, void *userdata,
                    GhosttySizeReportSize *out) {
  (void)userdata;
  uint16_t cols = 0;
  uint16_t rows = 0;
  uint32_t width_px = 0;
  uint32_t height_px = 0;
  if (ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_COLS, &cols) !=
      GHOSTTY_SUCCESS)
    return false;
  if (ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_ROWS, &rows) !=
      GHOSTTY_SUCCESS)
    return false;
  if (cols == 0 || rows == 0) return false;
  if (ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_WIDTH_PX,
                           &width_px) != GHOSTTY_SUCCESS)
    return false;
  if (ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_HEIGHT_PX,
                           &height_px) != GHOSTTY_SUCCESS)
    return false;
  out->columns = cols;
  out->rows = rows;
  out->cell_width = width_px / cols;
  out->cell_height = height_px / rows;
  return true;
}

/*
 * The option value for an effect IS the function pointer, cast to const void*;
 * the value for GHOSTTY_TERMINAL_OPT_USERDATA IS the userdata pointer itself.
 * Passing the address of a stack local holding either one makes the terminal
 * call a stack address on the first effect.
 */
GhosttyResult nocxInstall(GhosttyTerminal terminal, uintptr_t handle) {
  GhosttyResult r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_USERDATA,
                                         (const void *)handle);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_WRITE_PTY,
                           (const void *)cb_write_pty);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_DEVICE_ATTRIBUTES,
                           (const void *)cb_device_attributes);
  if (r != GHOSTTY_SUCCESS) return r;
  return ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_SIZE,
                              (const void *)cb_size);
}

/* -------------------------------------------------------------------- reads */

/*
 * A grid reference at (x, y) of the ACTIVE AREA — the grid the cursor moves in,
 * not the scrollback and not a viewport somebody has scrolled. The reference is
 * a snapshot and dies at the next mutating call, so every read copies what it
 * needs before returning.
 */
GhosttyResult nocxGridRefAt(GhosttyTerminal terminal, uint16_t x, uint16_t y,
                            GhosttyGridRef *out) {
  GhosttyPoint pt = {0};
  pt.tag = GHOSTTY_POINT_TAG_ACTIVE;
  pt.value.coordinate.x = x;
  pt.value.coordinate.y = y;
  *out = GHOSTTY_INIT_SIZED(GhosttyGridRef);
  return ghostty_terminal_grid_ref(terminal, pt, out);
}

static void color_facts(GhosttyStyleColor color, GhosttyStyleColorTag *tag,
                        GhosttyColorPaletteIndex *palette,
                        GhosttyColorRgb *rgb) {
  *tag = color.tag;
  *palette = 0;
  rgb->r = 0;
  rgb->g = 0;
  rgb->b = 0;
  if (color.tag == GHOSTTY_STYLE_COLOR_PALETTE) {
    *palette = color.value.palette;
  } else if (color.tag == GHOSTTY_STYLE_COLOR_RGB) {
    *rgb = color.value.rgb;
  }
}

GhosttyResult nocxStyleAt(const GhosttyGridRef *ref, nocxStyleFacts *out) {
  GhosttyStyle style = GHOSTTY_INIT_SIZED(GhosttyStyle);
  GhosttyResult r = ghostty_grid_ref_style(ref, &style);
  if (r != GHOSTTY_SUCCESS) return r;
  color_facts(style.fg_color, &out->fg_tag, &out->fg_palette, &out->fg_rgb);
  color_facts(style.bg_color, &out->bg_tag, &out->bg_palette, &out->bg_rgb);
  color_facts(style.underline_color, &out->ul_tag, &out->ul_palette,
              &out->ul_rgb);
  out->underline = (GhosttySgrUnderline)style.underline;
  out->bold = style.bold;
  out->italic = style.italic;
  out->faint = style.faint;
  out->blink = style.blink;
  out->inverse = style.inverse;
  out->invisible = style.invisible;
  out->strikethrough = style.strikethrough;
  out->overline = style.overline;
  return GHOSTTY_SUCCESS;
}

/* -------------------------------------------------------------------- input */

GhosttyResult nocxKeyEncode(GhosttyKeyEncoder encoder, GhosttyKey key,
                            GhosttyMods mods, GhosttyKeyAction action,
                            const char *utf8, size_t utf8_len, char *out,
                            size_t out_len, size_t *out_written) {
  GhosttyKeyEvent event = NULL;
  GhosttyResult r = ghostty_key_event_new(NULL, &event);
  if (r != GHOSTTY_SUCCESS) return r;
  ghostty_key_event_set_key(event, key);
  ghostty_key_event_set_mods(event, mods);
  ghostty_key_event_set_action(event, action);
  /* Borrowed until the event is freed below, which is why both happen here
     rather than across two calls from Go. */
  if (utf8 != NULL && utf8_len > 0)
    ghostty_key_event_set_utf8(event, utf8, utf8_len);
  r = ghostty_key_encoder_encode(encoder, event, out, out_len, out_written);
  ghostty_key_event_free(event);
  return r;
}
