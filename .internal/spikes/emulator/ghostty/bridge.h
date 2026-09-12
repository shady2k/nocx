/*
 * Throwaway bridge between libghostty-vt's C ABI and the Go driver.
 *
 * It exists only because cgo cannot take the address of a C function that has
 * no external linkage, so the effect callbacks are installed from C rather
 * than passed from Go.
 */
#ifndef NOCX_SPIKE_GHOSTTY_BRIDGE_H
#define NOCX_SPIKE_GHOSTTY_BRIDGE_H

#include <stddef.h>
#include <stdint.h>

#include <ghostty/vt.h>

/* Implemented on the Go side with //export. */
extern void nocxGoWritePty(uintptr_t handle, const uint8_t *data, size_t len);
extern void nocxGoBell(uintptr_t handle);
extern void nocxGoTitleChanged(uintptr_t handle);
extern void nocxGoPwdChanged(uintptr_t handle);
extern void nocxGoClipboardWrite(uintptr_t handle, int location, size_t len);
extern void nocxGoUnknownSequence(uintptr_t handle, int tag);

GhosttyResult nocxInstallEffects(GhosttyTerminal terminal, uintptr_t handle);
GhosttyResult nocxTerminalTitle(GhosttyTerminal terminal, GhosttyString *out);
GhosttyResult nocxTerminalPwd(GhosttyTerminal terminal, GhosttyString *out);
GhosttyResult nocxTerminalMode(GhosttyTerminal terminal, uint16_t value, int ansi,
                               int *out_set);
GhosttyResult nocxGridRefAt(GhosttyTerminal terminal, uint16_t x, uint16_t y,
                            GhosttyGridRef *out);
GhosttyResult nocxCellFacts(GhosttyCell cell, uint32_t *out_cp, int *out_wide,
                            int *out_has_text);
GhosttyResult nocxRowFacts(GhosttyRow row, int *out_wrap, int *out_continuation);
GhosttyResult nocxCellGraphemes(const GhosttyGridRef *ref, uint32_t *buf,
                                size_t buf_len, size_t *out_len);

/*
 * Render-state lifecycle, so a caller can hold one state across several
 * terminal updates and observe what the incremental API actually reports.
 */
GhosttyResult nocxRenderStateNew(GhosttyRenderState *out_state);
GhosttyResult nocxRenderStateUpdate(GhosttyRenderState state,
                                    GhosttyTerminal terminal);
GhosttyResult nocxRenderStateDirty(GhosttyRenderState state, int *out_dirty);
GhosttyResult nocxRenderStateDirtyRows(GhosttyRenderState state,
                                       uint16_t *rows, size_t max_rows,
                                       size_t *out_n);
GhosttyResult nocxRenderStateClean(GhosttyRenderState state);
void nocxRenderStateFree(GhosttyRenderState state);

/* Compile-time capabilities of the library that was linked, plus whether the
   linked build exposes Kitty image storage for this terminal. */
GhosttyResult nocxBuildInfoBool(GhosttyBuildInfo which, int *out);
GhosttyResult nocxKittyGraphicsPresent(GhosttyTerminal terminal, int *out);

/* Whether an image with this id exists in the terminal's Kitty image
   storage: 1 when found, 0 when not. This is what decides whether a Kitty
   graphics APC was decoded, as opposed to merely not being reported as
   unknown. */
GhosttyResult nocxKittyImagePresent(GhosttyTerminal terminal, uint32_t image_id,
                                    int *out);

#endif /* NOCX_SPIKE_GHOSTTY_BRIDGE_H */
