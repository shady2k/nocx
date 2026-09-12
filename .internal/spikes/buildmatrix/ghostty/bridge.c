#include <string.h>

#include "bridge.h"

/*
 * The callback ghostty calls is `(terminal, userdata, data, len)`; the Go
 * export is `(handle, data, len)` and reads the handle out of userdata. The
 * adapter is what makes those two the same call -- installing the export's
 * address directly would shift every argument by one.
 */
static void bmWritePtyCb(GhosttyTerminal terminal, void *userdata,
                         const uint8_t *data, size_t len) {
  (void)terminal;
  bmGoWritePty((uintptr_t)userdata, (uint8_t *)data, len);
}

GhosttyResult bmInstallWritePty(GhosttyTerminal terminal, uintptr_t handle) {
  GhosttyResult r = ghostty_terminal_set(
      terminal, GHOSTTY_TERMINAL_OPT_USERDATA, (void *)handle);
  if (r != GHOSTTY_SUCCESS) return r;
  return ghostty_terminal_set(terminal, GHOSTTY_TERMINAL_OPT_WRITE_PTY,
                              (const void *)&bmWritePtyCb);
}

GhosttyResult bmTitle(GhosttyTerminal terminal, char *buf, size_t buf_len,
                      size_t *out_len) {
  GhosttyString s = {0};
  GhosttyResult r =
      ghostty_terminal_get(terminal, GHOSTTY_TERMINAL_DATA_TITLE, &s);
  if (r != GHOSTTY_SUCCESS) return r;
  *out_len = s.len;
  if (s.len == 0) return GHOSTTY_SUCCESS;
  memcpy(buf, s.ptr, s.len < buf_len ? s.len : buf_len);
  return GHOSTTY_SUCCESS;
}
