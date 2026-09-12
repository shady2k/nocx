/*
 * The C half of the adapter: everything cgo cannot express.
 *
 * Six things live here, and each is here for a reason rather than for style:
 *
 *   - THE CALLBACKS. cgo cannot take the address of a C function that a Go
 *     package defines, so the effect callbacks are C functions with external
 *     linkage and are installed from C.
 *
 *   - THE POINT. GhosttyPoint is a tagged union, and cgo represents a C union
 *     as a byte array, so a point cannot be built from Go.
 *
 *   - THE STYLE. GhosttyStyleColor is a tagged union for the same reason, so
 *     the union is flattened here into scalars Go can read.
 *
 *   - THE KEY TEXT. ghostty_key_event_set_utf8 BORROWS the bytes for as long
 *     as the event lives, and a Go string handed to C is only valid for the
 *     duration of one call. Setting the text and encoding inside one call is
 *     what keeps it valid; doing it from Go would be a use after free.
 *
 *   - THE EVENTS. A key event and a mouse event are built, used and freed in
 *     one C call for the same reason: the caller would otherwise have to free
 *     the handle on every path out of the function that configured it.
 *
 *   - THE MODE NUMBER. A packed GhosttyMode is built by ghostty_mode_new, a
 *     static inline in modes.h, so the packing stays on this side.
 */
#ifndef NOCX_EMULATOR_GHOSTTY_BRIDGE_H
#define NOCX_EMULATOR_GHOSTTY_BRIDGE_H

#include <stddef.h>
#include <stdint.h>

#include <ghostty/vt.h>

/* Implemented on the Go side with //export. The parameter is non-const because
   cgo's generated header for an exported function is, and the two declarations
   must agree. */
extern void nocxGoWritePty(uintptr_t handle, uint8_t *data, size_t len);

/* The effect callbacks, on the same terms: the C half reads the borrowed value
   the callback carries — the terminal's own title or pwd, the notification's
   text, the clipboard write's payload — and hands the bytes to Go, which
   copies them before the borrowed memory dies with the callback. One function
   per kind rather than a kind argument, because the kind is a port value and
   the port's numbering must not be mirrored in C. */
extern void nocxGoBell(uintptr_t handle);
extern void nocxGoTitle(uintptr_t handle, uint8_t *data, size_t len);
extern void nocxGoPwd(uintptr_t handle, uint8_t *data, size_t len);
extern void nocxGoClipboard(uintptr_t handle, uint8_t *data, size_t len);
extern void nocxGoNotification(uintptr_t handle, uint8_t *data, size_t len);

/*
 * A style with its union materialised: cgo represents GhosttyStyleColorValue
 * as a byte array, so the tagged union is resolved here and the three shapes
 * arrive as the C types that carry them. Every field keeps its upstream type
 * rather than being widened to an integer, so the Go side converts by NAME
 * against the header's own constants instead of against numbers it copied.
 *
 * A palette index is meaningful only when its tag says palette, and an RGB
 * value only when its tag says RGB; the other field is zero, and the port's
 * conversion reads it through the tag.
 */
typedef struct {
  GhosttyStyleColorTag fg_tag;
  GhosttyColorPaletteIndex fg_palette;
  GhosttyColorRgb fg_rgb;
  GhosttyStyleColorTag bg_tag;
  GhosttyColorPaletteIndex bg_palette;
  GhosttyColorRgb bg_rgb;
  GhosttyStyleColorTag ul_tag;
  GhosttyColorPaletteIndex ul_palette;
  GhosttyColorRgb ul_rgb;
  GhosttySgrUnderline underline;
  bool bold;
  bool italic;
  bool faint;
  bool blink;
  bool inverse;
  bool invisible;
  bool strikethrough;
  bool overline;
} nocxStyleFacts;

GhosttyResult nocxInstall(GhosttyTerminal terminal, uintptr_t handle);
GhosttyResult nocxGridRefAt(GhosttyTerminal terminal, uint16_t x, uint16_t y,
                            GhosttyGridRef *out);
GhosttyResult nocxStyleAt(const GhosttyGridRef *ref, nocxStyleFacts *out);
GhosttyResult nocxKeyEncode(GhosttyKeyEncoder encoder, GhosttyKey key,
                            GhosttyMods mods, GhosttyKeyAction action,
                            const char *utf8, size_t utf8_len, char *out,
                            size_t out_len, size_t *out_written);

/*
 * Encodes one mouse event. The event is built and freed here for the same
 * reason the key event is: the encoder reads it during one call, and a caller
 * that built it from Go would have to free it on every path out of this
 * function, including the ones that return early.
 *
 * has_button is separate from the button value because "no button" is a state
 * of the event rather than a button identity: a motion with nothing held clears
 * the button instead of naming UNKNOWN.
 */
GhosttyResult nocxMouseEncode(GhosttyMouseEncoder encoder,
                              GhosttyMouseAction action,
                              GhosttyMouseButton button, bool has_button,
                              GhosttyMods mods, float x, float y, char *out,
                              size_t out_len, size_t *out_written);

/*
 * Reads one of the program's own mode bits. The mode is PACKED — value plus
 * the ANSI flag in bit 15 — and building it here is what keeps the packing out
 * of Go: ghostty_mode_new is a static inline in modes.h, and the shape of a
 * packed mode is upstream's business rather than the adapter's.
 */
GhosttyResult nocxModeValue(GhosttyTerminal terminal, uint16_t mode,
                            bool *out);

#endif /* NOCX_EMULATOR_GHOSTTY_BRIDGE_H */
