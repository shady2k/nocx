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
 * The non-visual effects. Each is a thin shim: it reads the borrowed value the
 * callback carries — from the callback's own argument, or from the terminal,
 * for the two whose callback carries nothing — and forwards the bytes to Go,
 * which copies them before the borrowed memory dies with this call.
 *
 * Installing one of these is what makes the effect exist at all. Without the
 * bell callback a BEL is consumed and nothing says so; without the title and
 * pwd callbacks the terminal still stores the values but no one is told they
 * changed; without the clipboard callback a clipboard write is refused (the
 * header's "returning without replying denies the write" is moot when no
 * callback is installed, since the library then has nobody to ask); without the
 * notification callback OSC 9 and OSC 777 are swallowed.
 *
 * The progress report (OSC 9;4) is deliberately NOT installed. It is a progress
 * hint about a long-running command — a bar a surface may draw — and not a
 * thing the program asked the terminal to DO, so it is not an effect and a
 * runtime that wants it must ask upstream for it separately.
 */
static void cb_bell(GhosttyTerminal terminal, void *userdata) {
  (void)terminal;
  nocxGoBell((uintptr_t)userdata);
}

/*
 * The title is read from the terminal rather than passed to the callback, so
 * it is read HERE, while the borrowed string is alive: GHOSTTY_TERMINAL_DATA_TITLE
 * is valid until the next mutating call, and a Go-side read after this
 * function returned would be reading through a pointer the library was free to
 * invalidate.
 */
static void cb_title_changed(GhosttyTerminal terminal, void *userdata) {
  GhosttyString title = {0};
  if (ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_TITLE, &title) !=
      GHOSTTY_SUCCESS)
    return;
  nocxGoTitle((uintptr_t)userdata, (uint8_t *)title.ptr, title.len);
}

static void cb_pwd_changed(GhosttyTerminal terminal, void *userdata) {
  GhosttyString pwd = {0};
  if (ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_PWD, &pwd) !=
      GHOSTTY_SUCCESS)
    return;
  nocxGoPwd((uintptr_t)userdata, (uint8_t *)pwd.ptr, pwd.len);
}

/*
 * A clipboard write carries its payload in the callback's argument: an array of
 * MIME representations of ONE logical value (the header's words), the first of
 * which is the payload the port carries. A write with no representations is a
 * request to CLEAR the destination, which travels as an effect with an empty
 * body rather than as no effect at all: the program asked for something.
 *
 * The reply is answered success, and it has to be answered here because the
 * header requires it within the callback. Success is the honest answer rather
 * than a convenience: nocx ACCEPTS the write — the effect is the payload being
 * handed to the runtime that will perform it (design §6.2) — and answering
 * denied would tell the program that a write nocx took was refused, which a
 * program that retries or reports an error would then act on. `remember` is
 * left false: a session grant is a permission decision, and this adapter has no
 * notion of one having been given.
 */
static void cb_clipboard_write(GhosttyTerminal terminal, void *userdata,
                               const GhosttyClipboardWrite *write) {
  (void)terminal;
  GhosttyString payload = {0};
  if (write->contents_len > 0) payload = write->contents[0].data;
  nocxGoClipboard((uintptr_t)userdata, (uint8_t *)payload.ptr, payload.len);
  if (write->reply == NULL) return;
  GhosttyClipboardWriteReply reply = {0};
  reply.size = sizeof(reply);
  reply.result = GHOSTTY_CLIPBOARD_WRITE_RESULT_SUCCESS;
  write->reply(write, &reply);
}

/*
 * The body is the message. OSC 9 carries only its text, which the library
 * reports as the body with an empty title, and OSC 777 carries a title AND a
 * body; the port carries the body for both, which is the thing a notification
 * says.
 */
static void cb_desktop_notification(
    GhosttyTerminal terminal, void *userdata,
    const GhosttyTerminalDesktopNotification *notification) {
  (void)terminal;
  nocxGoNotification((uintptr_t)userdata, (uint8_t *)notification->body.ptr,
                     notification->body.len);
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
  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_BELL,
                           (const void *)cb_bell);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_TITLE_CHANGED,
                           (const void *)cb_title_changed);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_PWD_CHANGED,
                           (const void *)cb_pwd_changed);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_CLIPBOARD_WRITE,
                           (const void *)cb_clipboard_write);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_DESKTOP_NOTIFICATION,
                           (const void *)cb_desktop_notification);
  if (r != GHOSTTY_SUCCESS) return r;
  return ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_SIZE,
                              (const void *)cb_size);
}

/* -------------------------------------------------------------------- modes */

GhosttyResult nocxModeValue(GhosttyTerminal terminal, uint16_t mode,
                            bool *out) {
  GhosttyTerminalModeConfig cfg = {0};
  cfg.mode = ghostty_mode_new(mode, false);
  GhosttyResult r = ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_MODE,
                                         &cfg);
  if (r != GHOSTTY_SUCCESS) return r;
  *out = cfg.value;
  return GHOSTTY_SUCCESS;
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

GhosttyResult nocxMouseEncode(GhosttyMouseEncoder encoder,
                              GhosttyMouseAction action,
                              GhosttyMouseButton button, bool has_button,
                              GhosttyMods mods, float x, float y, char *out,
                              size_t out_len, size_t *out_written) {
  GhosttyMouseEvent event = NULL;
  GhosttyResult r = ghostty_mouse_event_new(NULL, &event);
  if (r != GHOSTTY_SUCCESS) return r;
  ghostty_mouse_event_set_action(event, action);
  if (has_button)
    ghostty_mouse_event_set_button(event, button);
  else
    ghostty_mouse_event_clear_button(event);
  ghostty_mouse_event_set_mods(event, mods);
  GhosttyMousePosition position = {0};
  position.x = x;
  position.y = y;
  ghostty_mouse_event_set_position(event, position);
  r = ghostty_mouse_encoder_encode(encoder, event, out, out_len, out_written);
  ghostty_mouse_event_free(event);
  return r;
}
