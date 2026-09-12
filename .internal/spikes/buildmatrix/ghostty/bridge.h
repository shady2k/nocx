/*
 * The two calls cgo cannot make by itself.
 *
 * A Go export is not addressable from Go, and the option value for an effect
 * IS the function pointer cast to `const void *` -- not the address of a
 * variable holding it. The spike that first bound this API segfaulted the
 * other way round, so the convention is copied here rather than re-derived.
 */
#ifndef BM_BRIDGE_H
#define BM_BRIDGE_H

#include <stddef.h>
#include <stdint.h>

#include <ghostty/vt.h>

/*
 * Defined on the Go side with //export.
 *
 * The parameter is non-const because cgo generates the definition from the Go
 * signature as `uint8_t *data`, and this header is included into the same
 * translation unit that defines it (_cgo_export.c includes the exporting
 * file's preamble). The adapter below casts the qualifier away, which is sound
 * here: the Go side copies the bytes out and never writes through the pointer.
 */
extern void bmGoWritePty(uintptr_t handle, uint8_t *data, size_t length);

GhosttyResult bmInstallWritePty(GhosttyTerminal terminal, uintptr_t handle);

/*
 * Copies the borrowed title out of the library's own storage. `out_len`
 * receives the FULL length, so a buffer that was too small is reported by the
 * caller rather than silently truncated here.
 */
GhosttyResult bmTitle(GhosttyTerminal terminal, char *buf, size_t buf_len,
                      size_t *out_len);

#endif /* BM_BRIDGE_H */
