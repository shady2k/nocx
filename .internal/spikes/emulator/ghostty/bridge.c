#include "bridge.h"

static void cb_write_pty(GhosttyTerminal terminal, void *userdata,
                         const uint8_t *data, size_t len) {
  (void)terminal;
  nocxGoWritePty((uintptr_t)userdata, data, len);
}

static void cb_bell(GhosttyTerminal terminal, void *userdata) {
  (void)terminal;
  nocxGoBell((uintptr_t)userdata);
}

static void cb_title_changed(GhosttyTerminal terminal, void *userdata) {
  (void)terminal;
  nocxGoTitleChanged((uintptr_t)userdata);
}

static void cb_pwd_changed(GhosttyTerminal terminal, void *userdata) {
  (void)terminal;
  nocxGoPwdChanged((uintptr_t)userdata);
}

static void cb_clipboard_write(GhosttyTerminal terminal, void *userdata,
                               const GhosttyClipboardWrite *write) {
  (void)terminal;
  nocxGoClipboardWrite((uintptr_t)userdata, (int)write->location,
                       write->contents_len);
}

/*
 * Only the tag is read. The payload is a tagged union of borrowed strings and
 * this spike deliberately does not bind it, so the probe answers "was this
 * sequence reported as unsupported, and as which kind" and nothing more.
 */
static void cb_unknown_sequence(GhosttyTerminal terminal, void *userdata,
                                const GhosttyTerminalUnknownSequence *seq) {
  (void)terminal;
  nocxGoUnknownSequence((uintptr_t)userdata, (int)seq->tag);
}

/*
 * The option value for an effect IS the function pointer, cast to
 * const void*; the value for GHOSTTY_TERMINAL_OPT_USERDATA IS the userdata
 * pointer. Passing the address of a stack local holding either one makes the
 * terminal call a stack address on the first effect.
 */
GhosttyResult nocxInstallEffects(GhosttyTerminal terminal, uintptr_t handle) {
  GhosttyResult r;

  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_WRITE_PTY,
                           (const void *)cb_write_pty);
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
  r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_UNKNOWN_SEQUENCE,
                           (const void *)cb_unknown_sequence);
  if (r != GHOSTTY_SUCCESS) return r;
  /* The callback alone retains nothing: capture must also be enabled with a
     nonzero byte limit, per the header. */
  {
    size_t unknown_max_bytes = 256;
    r = ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_UNKNOWN_MAX_BYTES,
                             &unknown_max_bytes);
    if (r != GHOSTTY_SUCCESS) return r;
  }
  return ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_USERDATA,
                              (const void *)handle);
}

GhosttyResult nocxTerminalTitle(GhosttyTerminal terminal, GhosttyString *out) {
  return ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_TITLE, out);
}

GhosttyResult nocxTerminalPwd(GhosttyTerminal terminal, GhosttyString *out) {
  return ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_PWD, out);
}

GhosttyResult nocxTerminalMode(GhosttyTerminal terminal, uint16_t value, int ansi,
                               int *out_set) {
  GhosttyTerminalModeConfig cfg = {0};
  cfg.mode = ghostty_mode_new(value, ansi != 0);
  GhosttyResult r =
      ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_MODE, &cfg);
  if (r != GHOSTTY_SUCCESS) return r;
  *out_set = cfg.value ? 1 : 0;
  return GHOSTTY_SUCCESS;
}

GhosttyResult nocxGridRefAt(GhosttyTerminal terminal, uint16_t x, uint16_t y,
                            GhosttyGridRef *out) {
  GhosttyPoint pt = {0};
  pt.tag = GHOSTTY_POINT_TAG_ACTIVE;
  pt.value.coordinate.x = x;
  pt.value.coordinate.y = y;
  *out = GHOSTTY_INIT_SIZED(GhosttyGridRef);
  return ghostty_terminal_grid_ref(terminal, pt, out);
}

GhosttyResult nocxCellFacts(GhosttyCell cell, uint32_t *out_cp, int *out_wide,
                            int *out_has_text) {
  uint32_t cp = 0;
  bool has_text = false;
  GhosttyCellWide wide = GHOSTTY_CELL_WIDE_NARROW;
  GhosttyResult r = ghostty_cell_get(cell, GHOSTTY_CELL_DATA_CODEPOINT, &cp);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_cell_get(cell, GHOSTTY_CELL_DATA_HAS_TEXT, &has_text);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_cell_get(cell, GHOSTTY_CELL_DATA_WIDE, &wide);
  if (r != GHOSTTY_SUCCESS) return r;
  *out_cp = cp;
  *out_has_text = has_text ? 1 : 0;
  *out_wide = (int)wide;
  return GHOSTTY_SUCCESS;
}

GhosttyResult nocxRowFacts(GhosttyRow row, int *out_wrap, int *out_continuation) {
  bool wrap = false, cont = false;
  GhosttyResult r = ghostty_row_get(row, GHOSTTY_ROW_DATA_WRAP, &wrap);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_row_get(row, GHOSTTY_ROW_DATA_WRAP_CONTINUATION, &cont);
  if (r != GHOSTTY_SUCCESS) return r;
  *out_wrap = wrap ? 1 : 0;
  *out_continuation = cont ? 1 : 0;
  return GHOSTTY_SUCCESS;
}

GhosttyResult nocxCellGraphemes(const GhosttyGridRef *ref, uint32_t *buf,
                                size_t buf_len, size_t *out_len) {
  return ghostty_grid_ref_graphemes(ref, buf, buf_len, out_len);
}

GhosttyResult nocxRenderStateNew(GhosttyRenderState *out_state) {
  return ghostty_render_state_new(NULL, out_state);
}

GhosttyResult nocxRenderStateUpdate(GhosttyRenderState state,
                                    GhosttyTerminal terminal) {
  return ghostty_render_state_update(state, terminal);
}

GhosttyResult nocxRenderStateDirty(GhosttyRenderState state, int *out_dirty) {
  GhosttyRenderStateDirty dirty;
  GhosttyResult r =
      ghostty_render_state_get(state, GHOSTTY_RENDER_STATE_DATA_DIRTY, &dirty);
  if (r != GHOSTTY_SUCCESS) return r;
  *out_dirty = (int)dirty;
  return GHOSTTY_SUCCESS;
}

GhosttyResult nocxRenderStateDirtyRows(GhosttyRenderState state,
                                       uint16_t *rows, size_t max_rows,
                                       size_t *out_n) {
  *out_n = 0;

  GhosttyRenderStateRowIterator it = NULL;
  GhosttyResult r = ghostty_render_state_row_iterator_new(NULL, &it);
  if (r != GHOSTTY_SUCCESS) return r;
  r = ghostty_render_state_get(state, GHOSTTY_RENDER_STATE_DATA_ROW_ITERATOR,
                               &it);
  if (r != GHOSTTY_SUCCESS) {
    ghostty_render_state_row_iterator_free(it);
    return r;
  }

  uint16_t y = 0;
  size_t n = 0;
  while (n < max_rows && ghostty_render_state_row_iterator_next_dirty(it, &y)) {
    rows[n++] = y;
  }
  *out_n = n;

  ghostty_render_state_row_iterator_free(it);
  return GHOSTTY_SUCCESS;
}

GhosttyResult nocxRenderStateClean(GhosttyRenderState state) {
  return ghostty_render_state_clean(state);
}

void nocxRenderStateFree(GhosttyRenderState state) {
  ghostty_render_state_free(state);
}

GhosttyResult nocxBuildInfoBool(GhosttyBuildInfo which, int *out) {
  bool v = false;
  GhosttyResult r = ghostty_build_info(which, &v);
  *out = v ? 1 : 0;
  return r;
}

GhosttyResult nocxKittyGraphicsPresent(GhosttyTerminal terminal, int *out) {
  GhosttyKittyGraphics g = NULL;
  GhosttyResult r =
      ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_KITTY_GRAPHICS, &g);
  *out = (r == GHOSTTY_SUCCESS && g != NULL) ? 1 : 0;
  return r;
}

GhosttyResult nocxKittyImagePresent(GhosttyTerminal terminal, uint32_t image_id,
                                    int *out) {
  *out = 0;
  GhosttyKittyGraphics g = NULL;
  GhosttyResult r =
      ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_KITTY_GRAPHICS, &g);
  if (r != GHOSTTY_SUCCESS || g == NULL) return r;
  *out = ghostty_kitty_graphics_image(g, image_id) != NULL ? 1 : 0;
  return GHOSTTY_SUCCESS;
}
