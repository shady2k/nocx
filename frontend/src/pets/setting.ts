// Whether there is a pet, and how big (nocx-q4qeh.1).
//
// One owner for the answer, like reconnect-setting.ts and restore-setting.ts
// and for the same reason: the question is asked by a PANE, at the moment it
// builds its scrollback, and threading a decoration's policy down through
// the pane manager's constructor would put it in a signature that is about
// the window's parts.
//
// Read live rather than once at boot: turning the pet off is the action of
// somebody who wants it gone NOW, not at the next launch.

/** The declared keys (internal/settings/settings.go: PetsEnabled, PetsSize). */
export const PETS_ENABLED_KEY = 'pets.enabled'
export const PETS_SIZE_KEY = 'pets.size'
export const PETS_PACK_KEY = 'pets.pack'

/** The declared defaults. A failed settings read lands here. */
export const PETS_ENABLED_DEFAULT = true
export const PETS_SIZE_DEFAULT = 34
export const PETS_PACK_DEFAULT = 'cat-1'
const SIZE_MIN = 16
const SIZE_MAX = 96

let enabled = PETS_ENABLED_DEFAULT
let size = PETS_SIZE_DEFAULT
let pack = PETS_PACK_DEFAULT

type Listener = () => void
const listeners = new Set<Listener>()

function announce(): void {
  for (const l of [...listeners]) l()
}

/** Adopt the backend's values. Anything that is not of the declared type —
 *  an older backend, a failed fetch — leaves the default in place. */
export function applyPetsSettings(
  enabledValue: unknown,
  sizeValue: unknown,
  packValue: unknown,
): void {
  const before = `${enabled}/${size}/${pack}`
  if (typeof enabledValue === 'boolean') enabled = enabledValue
  // Not checked against a list here. `packBase` decides what an unknown id
  // means, in one place, so a value written by a newer build leaves the older
  // one with a cat rather than with an empty pane.
  if (typeof packValue === 'string' && packValue !== '') pack = packValue
  if (typeof sizeValue === 'number' && Number.isFinite(sizeValue)) {
    // Clamped rather than trusted: the bound is declared on the Go side, and
    // a value outside it here would mean the pet stands on ledges the terrain
    // rules measured against a different height.
    size = Math.min(SIZE_MAX, Math.max(SIZE_MIN, Math.round(sizeValue)))
  }
  if (`${enabled}/${size}/${pack}` !== before) announce()
}

export function petsEnabled(): boolean {
  return enabled
}

export function petHeight(): number {
  return size
}

export function petPack(): string {
  return pack
}

/** Called whenever either answer changes. Returns its own unsubscribe. */
export function onPetsSettingsChanged(listener: Listener): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

/** Test seam: put the module back to its declared defaults. */
export function resetPetsSettings(): void {
  enabled = PETS_ENABLED_DEFAULT
  size = PETS_SIZE_DEFAULT
  pack = PETS_PACK_DEFAULT
  listeners.clear()
}
