import { ClipboardGetText, ClipboardSetText } from '../wailsjs/runtime/runtime'

// ── Clipboard access seam (AD-8: interface, AD-6: renderer never touches) ──

/**
 * Platform clipboard abstraction. The renderer reports facts and never calls
 * these methods — clipboard access lives above the renderer boundary (AD-6).
 */
export interface ClipboardAccess {
  readText(): Promise<string>
  writeText(text: string): Promise<void>
}

/**
 * Wails-backed clipboard — works in the packaged app where Wails injects
 * window.runtime. ClipboardSetText returns a boolean; false means the
 * platform rejected the write and must be surfaced, not swallowed.
 */
class WailsClipboard implements ClipboardAccess {
  async readText(): Promise<string> {
    return ClipboardGetText()
  }

  async writeText(text: string): Promise<void> {
    const ok = await ClipboardSetText(text)
    if (!ok) throw new Error('ClipboardSetText returned false')
  }
}

/**
 * Browser-backed clipboard — works in the plain-browser dev seam on :34115
 * when navigator.clipboard is available. The factory guards against absent
 * clipboard (non-secure context) at construction time.
 */
class BrowserClipboard implements ClipboardAccess {
  async readText(): Promise<string> {
    return navigator.clipboard.readText()
  }

  async writeText(text: string): Promise<void> {
    await navigator.clipboard.writeText(text)
  }
}

/**
 * Degraded clipboard — rejects every operation with a clear reason instead
 * of throwing at construction. A terminal must not die because it cannot
 * find a clipboard: paste, copy-on-select and OSC 52 writes all degrade to
 * logged warnings, and the rest of the app works normally.
 *
 * Reachable today by opening the dev seam over a LAN IP instead of
 * localhost — navigator.clipboard is undefined in a non-secure context.
 */
class NoopClipboard implements ClipboardAccess {
  readText(): Promise<string> {
    return Promise.reject(new Error('nocx: no clipboard backend available — cannot read clipboard'))
  }

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  writeText(_text: string): Promise<void> {
    return Promise.reject(
      new Error('nocx: no clipboard backend available — cannot write to clipboard'),
    )
  }
}

/**
 * Creates a clipboard access implementation. Returns a Wails-backed
 * clipboard when the Wails runtime is present, falling back to
 * navigator.clipboard. Returns a degraded implementation that reports its
 * own unavailability when neither backend is available, so the terminal
 * still opens — a missing clipboard must never kill the app.
 *
 * AD-8: this is called at the composition root (main.ts) and the result is
 * injected down; no consumer calls the factory itself.
 */
export function createClipboardAccess(): ClipboardAccess {
  // Wails runtime — packaged app.
  if (typeof window !== 'undefined' && (window as unknown as { runtime?: unknown }).runtime) {
    return new WailsClipboard()
  }

  // navigator.clipboard — plain-browser dev seam on :34115.
  if (typeof navigator !== 'undefined' && navigator.clipboard) {
    return new BrowserClipboard()
  }

  console.warn(
    'nocx: no clipboard backend available (no Wails runtime, no navigator.clipboard) — clipboard operations will fail',
  )
  return new NoopClipboard()
}

// ── OSC 52 decoder ──────────────────────────────────────────────────────

/**
 * Maximum decoded payload size (1 MiB) expressed as the base64 string length
 * that would produce it. 1 MiB = 1_048_576 bytes → ceil(1_048_576 / 3) * 4
 * = 1_398_104 base64 characters. Rejecting on the base64 length before atob
 * keeps a hostile stream from allocating megabytes per sequence on the
 * render thread.
 */
const OSC52_MAX_BASE64 = 1_398_104

/**
 * Decodes an OSC 52 payload into the clipboard text it carries, or null when
 * the payload is invalid, malformed, oversized, zero-byte, or is a read
 * request.
 *
 * Format: `target;base64` where target is `c` (clipboard), empty (defaults
 * to clipboard), or `p`/`q`/`s` (primary/secondary/select — ignored here).
 * The read form `c;?` is refused — no code path for reading the clipboard.
 *
 * xterm.js's parser already caps the raw OSC payload at 1e7 characters, so
 * the 1 MiB rule here is a policy cap on what a program may silently place
 * on the clipboard, not a memory guard.
 */
export function decodeOsc52(payload: string): string | null {
  const semi = payload.indexOf(';')
  if (semi === -1) return null

  const target = payload.slice(0, semi)
  const data = payload.slice(semi + 1)

  // Only clipboard target 'c' or empty (default).
  if (target !== '' && target !== 'c') return null

  // Refuse read form: the data field is exactly '?'.
  // No code path for OSC 52 read — not disabled, not configurable. Absent.
  if (data === '?') return null

  // Size cap on the base64 string before decoding, so a hostile stream
  // cannot allocate megabytes per sequence on the render thread.
  // xterm.js already caps the raw OSC payload at 1e7 characters, so
  // this is a policy cap, not a memory guard.
  if (data.length > OSC52_MAX_BASE64) return null

  let decoded: string
  try {
    // atob returns a binary string where each character is a byte (0–255).
    // Convert to bytes, then decode as UTF-8 so Unicode content survives
    // the round trip — a program base64-encodes the UTF-8 bytes, not the
    // UTF-16 string.
    const binary = atob(data)
    const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0))

    // A whitespace-only or empty payload decodes to zero bytes. A remote
    // program must never clear the user's clipboard silently.
    if (bytes.length === 0) return null

    decoded = new TextDecoder().decode(bytes)
  } catch {
    // atob throws on malformed base64 (e.g. wrong characters, wrong padding).
    return null
  }

  return decoded
}

// ── OSC 52 gate ────────────────────────────────────────────────────────

/**
 * Pure-state policy gate for OSC 52 clipboard writes. Starts denied — a
 * program's first write attempt is blocked and the banner offers the remedy.
 * No DOM, no side-effects, unit-testable without a browser.
 *
 * The grant lasts for one run of the application — persisting it needs real
 * config storage (nocx-jap). The grant covers the whole app, not a named
 * program — per-program binding needs kernel-sourced identity, never the
 * terminal title (nocx-3cc).
 */
export class ClipboardGate {
  private _granted = false
  private _suppressed = false

  /** True when the user has allowed OSC 52 writes for this run. */
  get granted(): boolean {
    return this._granted
  }

  /** True when the user has suppressed the banner without granting. */
  get suppressed(): boolean {
    return this._suppressed
  }

  /** Allow clipboard writes — called from the banner's Allow affordance. */
  allow(): void {
    this._granted = true
  }

  /** Suppress the banner without granting — called from Don't show again. */
  suppress(): void {
    this._suppressed = true
  }
}

// ── Selection policy ────────────────────────────────────────────────────

/**
 * Returns true when a selection should be copied to the clipboard.
 * A click or an empty/whitespace-only selection leaves the clipboard untouched.
 *
 * The guard against programmatic selection changes (e.g. search stepping
 * through matches) is not built here — nocx has no search yet, and the guard
 * arrives with it (Tabby's `preventNextOnSelectionChangeEvent` pattern, cited
 * in prior-art.md).
 */
export function shouldCopy(text: string): boolean {
  return text.trim().length > 0
}
