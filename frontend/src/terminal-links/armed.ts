// ═══════════════════════════════════════════════════════════════════════════
// "Is the link modifier held?" — asked by BOTH surfaces, so it is answered
// once (AD-8).
//
// The frozen DOM decides whether a click opens; the live xterm decides
// whether a link EXISTS at all (its provider returns nothing while unarmed,
// which is how the engine's own hover underline is kept from advertising a
// click that would do nothing). Two trackers would let those two disagree
// about the state of one key.
//
// ⌘ or ⌃, either one, matching every other chord in this codebase
// (`e.metaKey || e.ctrlKey`) rather than sniffing the platform.
//
// The disarming cases are the ones a hand-rolled version forgets: a window
// that loses focus never sees the keyup, and neither does a tab that is
// hidden mid-chord — leaving the terminal armed forever, one stray click
// from opening something nobody asked for.
// ═══════════════════════════════════════════════════════════════════════════

export interface ArmedTracker {
  /** Whether ⌘/⌃ is held right now. */
  armed(): boolean
  /** Called on every transition; returns an unsubscribe. */
  subscribe(cb: (armed: boolean) => void): () => void
  dispose(): void
}

export function trackLinkModifier(target: Window = window): ArmedTracker {
  let value = false
  const subs = new Set<(armed: boolean) => void>()

  const set = (next: boolean): void => {
    if (next === value) return
    value = next
    for (const cb of [...subs]) cb(next)
  }

  // Read the modifier off the EVENT rather than off the key name: a keydown
  // of `Meta` and a keydown of `d` while ⌘ is held both carry metaKey, so
  // arming survives a chord that started before the pointer arrived.
  const onKey = (e: KeyboardEvent): void => set(e.metaKey || e.ctrlKey)
  const disarm = (): void => set(false)

  target.addEventListener('keydown', onKey, true)
  target.addEventListener('keyup', onKey, true)
  target.addEventListener('blur', disarm)
  target.document?.addEventListener('visibilitychange', disarm)

  return {
    armed: () => value,
    subscribe(cb) {
      subs.add(cb)
      return () => subs.delete(cb)
    },
    dispose() {
      target.removeEventListener('keydown', onKey, true)
      target.removeEventListener('keyup', onKey, true)
      target.removeEventListener('blur', disarm)
      target.document?.removeEventListener('visibilitychange', disarm)
      subs.clear()
    },
  }
}
