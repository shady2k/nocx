// SecretChip — the kit's badge wearing the atomic reference chip (ui/README
// table). Rendered by the CM6 decoration over {{secret:NAME}} (secret-chip.ts
// beside the editor) and, in the unresolved variant, over a recalled masked
// segment: the badge shape is the kit's (ui-badge), this module is the
// chip's DOM. A surface may place it and never repaint it.
//
// Three variants, one chip (the kit grows by variants, never by
// near-duplicates):
//
//   - resolved: a rendering of the REFERENCE, never of its value — the
//     document keeps `{{secret:NAME}}` verbatim (ADR-0021). The lock glyph
//     marks it as a vault secret; the name is the inventory name
//     (ADR-0016); the tone is info.
//   - unresolved: a rendering of a MASK — the block's command line after
//     the ack, and a recalled row that cannot run as written. It shows the
//     kind's human label, not a name (there is no name yet) and not the
//     value (there is no value in the renderer); the tone is warning. This
//     is what the receipt points at when a row is hovered.
//   - damaged: the middle row of design §11.1's three states — a span WE
//     substituted whose bytes no longer equal the secret. It names the
//     secret AND the SHAPE of the damage ("truncated, 24 of 214 bytes") and
//     never a byte of it, because a truncated token is a prefix of a live
//     one. That row is what makes the whole badge safe rather than
//     decorative: without it, "show the text when it does not match" would
//     print the beginning of a live credential in the clear.
//
// The damaged variant differs from the intact one by MORE than its colour —
// a different glyph and the damage text itself — because a reader who cannot
// see the colour still has to be able to tell the two apart (WCAG 1.4.1),
// and telling them apart is the entire job.
import type { BadgeTone } from './badge'

export type SecretChipVariant = 'resolved' | 'unresolved' | 'damaged'

export function createSecretChip(name: string): HTMLElement {
  return buildChip('resolved', name)
}

/** The unresolved variant: the kind label in the chip's warning tone. */
export function createSecretChipUnresolved(kindLabel: string): HTMLElement {
  return buildChip('unresolved', kindLabel)
}

/**
 * The damaged variant: this IS the span we substituted, and the bytes there
 * no longer equal the secret.
 *
 * `damage` is the shape of it and never its content — "truncated, 24 of 214
 * bytes" carries lengths only. Nothing in this function can render a value:
 * it takes a name and a shape, and there is no third parameter.
 */
export function createSecretChipDamaged(name: string, damage: string): HTMLElement {
  return buildChip('damaged', name, damage)
}

const GLYPH: Record<SecretChipVariant, string> = {
  // lock — the same register the cwd chip uses
  resolved: '\u{1F512}',
  unresolved: '\u{1F512}',
  // warning sign: the one state where the bytes are NOT what the name says,
  // so the glyph has to say it too rather than leaving it to the colour.
  damaged: '\u26A0',
}

/** The badge tone per variant. Three distinct tones, because the three
 *  states must be distinguishable from each other and not merely from
 *  ordinary text. `Record<SecretChipVariant, …>` and no cast: a variant added
 *  without a tone is a compile error, which is the only thing that stops a
 *  fourth state from silently inheriting a third's colour. */
const TONE: Record<SecretChipVariant, BadgeTone> = {
  resolved: 'info',
  unresolved: 'warning',
  damaged: 'danger',
}

function titleFor(variant: SecretChipVariant, label: string, damage: string): string {
  switch (variant) {
    case 'resolved':
      return `secret from the vault: ${label}`
    case 'unresolved':
      return 'a masked secret — pick a live value to run this command'
    case 'damaged':
      return `${label}: these bytes are no longer the secret — ${damage}`
  }
}

function buildChip(variant: SecretChipVariant, label: string, damage = ''): HTMLElement {
  const chip = document.createElement('span')
  chip.className = 'ui-badge ui-secret-chip'
  chip.dataset.variant = variant
  chip.dataset.tone = TONE[variant]
  chip.title = titleFor(variant, label, damage)

  const lock = document.createElement('span')
  lock.className = 'ui-secret-chip__lock'
  lock.setAttribute('aria-hidden', 'true')
  lock.textContent = GLYPH[variant]

  const labelEl = document.createElement('span')
  labelEl.className = 'ui-secret-chip__name'
  labelEl.textContent = label

  chip.append(lock, labelEl)

  if (variant === 'damaged') {
    // Its own part rather than appended to the name: the name is the
    // secret's identity and the damage is a fact about these bytes, and a
    // test — or a reader — must be able to see which is which.
    const damageEl = document.createElement('span')
    damageEl.className = 'ui-secret-chip__damage'
    damageEl.textContent = `\u00B7 ${damage}`
    chip.append(damageEl)
  }
  return chip
}
