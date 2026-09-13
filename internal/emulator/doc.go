// Package emulator is the terminal a session runtime drives: the port, and
// every type that crosses it.
//
// # Why this package exists under this name
//
// ADR-0065 makes libghostty-vt the emulator the session runtime is built on,
// and its third point is the shape of this package: nocx defines its own cells,
// styles, effects, inputs and errors; borrowed data is copied before it expires;
// terminal access is explicitly serialised; and no Ghostty enum value or memory
// layout reaches the wire protocol or a durable document.
//
// So this is a port with an adapter beside it, not a vendored API. The adapter
// is [github.com/shady2k/nocx/internal/emulator/ghostty] and it is the only
// package allowed to name an upstream type. Nothing here imports it, names it,
// or depends on it in any way — and because that is a property of the SOURCE
// rather than of this comment, the adapter carries a test that fails the day an
// exported identifier here acquires a type that came from upstream or from C.
//
// The name is deliberate. docs/architecture.md's module map already spends
// "terminal" on the frontend module, and AGENTS.md forbids one name for two
// things, so the backend's port is the emulator.
//
// # What the port carries, and what it does not
//
// The runtime's needs, in the order ADR-0065 and the session-runtime design
// state them: create and destroy with owned handles; ingest; cells with
// grapheme boundaries and authoritative widths; style with colour kept apart as
// default, palette and RGB; per-line soft-wrap continuation; the program's own
// replies handed to the caller; key, mouse, paste and focus encoding driven
// from the terminal's state; the non-visual effects the program's output asked
// for; resize; and the alternate screen.
//
// Deliberately absent, and not by oversight:
//
//   - The cursor. The frame format needs it, but the runtime that consumes the
//     frame does not exist yet, and a field nobody reads is a field nobody has
//     agreed to.
//   - What the effects are FOR. Bell, title, cwd and clipboard writes cross the
//     port as [Effect] values because no read of the screen can find them, but
//     design §6.2's delivery — permissions, the replay rule, who may write the
//     clipboard — is the runtime's, and an effect is the request rather than
//     the delivery. The port carries no notion of a user having agreed to one.
//   - Hyperlinks, selection, search, Kitty graphics, and the incremental
//     render state. Each is a real capability of the library; none is in this
//     bead's list, and the render-state path in particular is a different
//     reading of the terminal (a diff encoder's) rather than this one.
//
// # The dependency direction is the whole point
//
// A session runtime holds an [Terminal] and drives it. It never learns which
// implementation it holds, and the implementation is replaceable per ADR-0065's
// sixth point, which keeps the rollback target an older Ghostty rather than a
// second terminal semantics. There is no production caller for any of this yet:
// the wiring lands with nocx-ygxjv.2, which is also why this package declares
// rather more than it currently earns.
package emulator
