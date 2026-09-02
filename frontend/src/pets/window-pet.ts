// The window's pet: who owns it, and where anything asks for it
// (nocx-q4qeh.1).
//
// One animal per WINDOW, not per pane. It is a window ornament: switching
// tabs must not make it vanish and reappear, which is exactly what a per-pane
// pet did — it lived inside the scrollback it was walking on, so it died with
// the pane and was born again in the next one.
//
// A module with one owner rather than a constructor parameter, for the reason
// reconnect-setting.ts gives for the same shape: the thing that needs it is a
// PANE, at the moment a command starts, and threading a window ornament down
// through the pane manager's constructor would put it in a signature that is
// about the window's parts.

import { PetOverlay } from './overlay'

let pet: PetOverlay | null = null

/**
 * Give this window its pet. Called once, by the composition root.
 *
 * `host` is the element the animal lives over — the whole application shell,
 * so that the tab strip's underside is terrain like any block edge. A second
 * call replaces the first and disposes it, which only a test should ever do.
 */
export function mountWindowPet(host: HTMLElement): PetOverlay {
  pet?.dispose()
  pet = new PetOverlay({ host, blocks: host })
  return pet
}

/** The window's pet, or null in anything that never mounted one — which is
 *  every unit test that builds a pane. */
export function windowPet(): PetOverlay | null {
  return pet
}

/** Test seam: take the animal away again. */
export function unmountWindowPet(): void {
  pet?.dispose()
  pet = null
}
