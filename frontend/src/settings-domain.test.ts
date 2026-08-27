// settings-domain — pure functions only, no jsdom, no DOM, no markup.
import { describe, it, expect } from 'vitest'
import {
  createMirror,
  numberRangeCaption,
  numberRangeError,
  parseRouteSettingKey,
  textLengthCaption,
  textLengthError,
  fieldSaveError,
  recordSaveOutcome,
  sectionBlocks,
  canResetSetting,
  applyAcceptedSnapshot,
  AcceptedSnapshot,
  type Declaration,
  type SettingsMirror,
  type SettingsSnapshot,
} from './settings-domain'

// ── helpers ────────────────────────────────────────────────────────────────

/** A specific-membership matcher that checks `.has()` on a Set. */
function hasKeys(s: ReadonlySet<string>, ...keys: string[]): boolean {
  return keys.every((k) => s.has(k))
}

// ── createMirror ───────────────────────────────────────────────────────────

describe('createMirror', () => {
  it('returns empty, zero-revision state', () => {
    const m = createMirror()
    expect(m.values).toEqual({})
    expect(m.draftValues).toEqual({})
    expect(m.overridden.size).toBe(0)
    expect(m.errors).toEqual({})
    expect(m.revision).toBe(0)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
//  1. Draft preservation on rejected save
// ═══════════════════════════════════════════════════════════════════════════

describe('recordSaveOutcome', () => {
  // ── accepted ──────────────────────────────────────────────────────────

  it('writes value into values and marks overridden on accepted save', () => {
    const m = createMirror()
    const next = recordSaveOutcome(m, 'terminal.fontSize', {
      kind: 'accepted',
      value: 16,
    })
    expect(next.values['terminal.fontSize']).toBe(16)
    expect(hasKeys(next.overridden, 'terminal.fontSize')).toBe(true)
    // Original was not mutated.
    expect('terminal.fontSize' in m.values).toBe(false)
    expect(m.overridden.size).toBe(0)
  })

  it('clears existing draft for the key on accepted save', () => {
    const m: SettingsMirror = {
      values: { 'terminal.fontSize': 12 },
      draftValues: { 'terminal.fontSize': 20 },
      overridden: new Set(['terminal.fontSize']),
      errors: {},
      revision: 1,
    }
    const next = recordSaveOutcome(m, 'terminal.fontSize', {
      kind: 'accepted',
      value: 16,
    })
    expect(next.values['terminal.fontSize']).toBe(16)
    expect('terminal.fontSize' in next.draftValues).toBe(false)
  })

  it('clears existing error for the key on accepted save', () => {
    const m: SettingsMirror = {
      values: { key1: 'old' },
      draftValues: {},
      overridden: new Set(),
      errors: { key1: 'previous error' },
      revision: 1,
    }
    const next = recordSaveOutcome(m, 'key1', { kind: 'accepted', value: 'new' })
    expect('key1' in next.errors).toBe(false)
  })

  // ── rejected ──────────────────────────────────────────────────────────

  it('preserves attempted value in draftValues on rejected save', () => {
    const m = createMirror()
    const next = recordSaveOutcome(m, 'terminal.fontFamily', {
      kind: 'rejected',
      error: 'validation error',
      attemptedValue: 'Bad Font',
    })
    expect(next.draftValues['terminal.fontFamily']).toBe('Bad Font')
    expect(next.errors['terminal.fontFamily']).toBe('validation error')
    // Original values are NOT polluted by the attempted value.
    expect('terminal.fontFamily' in m.values).toBe(false)
    // Draft is only in the new state.
    expect('terminal.fontFamily' in m.draftValues).toBe(false)
  })

  it('clears existing draft for the key before recording a new rejection', () => {
    const m: SettingsMirror = {
      values: { k: 'original' },
      draftValues: { k: 'old-draft' },
      overridden: new Set(),
      errors: {},
      revision: 0,
    }
    const next = recordSaveOutcome(m, 'k', {
      kind: 'rejected',
      error: 'new error',
      attemptedValue: 'new-draft',
    })
    expect(next.draftValues['k']).toBe('new-draft')
    expect(next.errors['k']).toBe('new error')
  })

  it('does not mark key as overridden on rejected save', () => {
    const m = createMirror()
    const next = recordSaveOutcome(m, 'key', {
      kind: 'rejected',
      error: 'nope',
      attemptedValue: 'x',
    })
    expect(hasKeys(next.overridden, 'key')).toBe(false)
  })

  // ── preserves unrelated state ─────────────────────────────────────────

  it('preserves unrelated values and overridden state across a save', () => {
    const m: SettingsMirror = {
      values: { existing: 'val' },
      draftValues: {},
      overridden: new Set(['existing']),
      errors: {},
      revision: 5,
    }
    const next = recordSaveOutcome(m, 'newkey', { kind: 'accepted', value: 'newval' })
    expect(next.values['existing']).toBe('val')
    expect(hasKeys(next.overridden, 'existing', 'newkey')).toBe(true)
    expect(next.revision).toBe(5)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
//  2. Provenance-based reset
// ═══════════════════════════════════════════════════════════════════════════

describe('canResetSetting', () => {
  it('allows reset when key is overridden', () => {
    const overridden: ReadonlySet<string> = new Set(['terminal.fontSize'])
    const r = canResetSetting(overridden, 'terminal.fontSize')
    expect(r.canReset).toBe(true)
  })

  it('denies reset when key is not overridden', () => {
    const r = canResetSetting(new Set(), 'terminal.fontSize')
    expect(r.canReset).toBe(false)
    if (!r.canReset) {
      expect(r.reason).toBe('notOverridden')
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
//  3. Snapshot revision policy (AD-7 authority-in-the-type)
// ═══════════════════════════════════════════════════════════════════════════

describe('AcceptedSnapshot', () => {
  // ── accept ────────────────────────────────────────────────────────────

  it('accepts snapshot with revision >= current', () => {
    const snap: SettingsSnapshot = {
      values: { 'terminal.fontSize': 16 },
      overridden: ['terminal.fontSize'],
      revision: 2,
    }
    const accepted = AcceptedSnapshot.accept(1, snap)
    expect(accepted).not.toBeNull()
    if (accepted) {
      expect(accepted.values['terminal.fontSize']).toBe(16)
      expect(accepted.overridden.has('terminal.fontSize')).toBe(true)
      expect(accepted.revision).toBe(2)
    }
  })

  it('accepts snapshot with equal revision (common refresh)', () => {
    const snap: SettingsSnapshot = {
      values: {},
      overridden: [],
      revision: 3,
    }
    const accepted = AcceptedSnapshot.accept(3, snap)
    expect(accepted).not.toBeNull()
  })

  it('rejects stale snapshot (revision < current)', () => {
    const snap: SettingsSnapshot = {
      values: {},
      overridden: [],
      revision: 1,
    }
    const accepted = AcceptedSnapshot.accept(5, snap)
    expect(accepted).toBeNull()
  })

  it('accepts snapshot with revision 0 when current is 0 (initial load)', () => {
    const snap: SettingsSnapshot = {
      values: { k: 'v' },
      overridden: ['k'],
      revision: 0,
    }
    const accepted = AcceptedSnapshot.accept(0, snap)
    expect(accepted).not.toBeNull()
  })

  it('accepts higher revision when current is default 0', () => {
    const snap: SettingsSnapshot = {
      values: { k: 'v' },
      overridden: ['k'],
      revision: 5,
    }
    const accepted = AcceptedSnapshot.accept(0, snap)
    expect(accepted).not.toBeNull()
  })

  // ── AD-7: authority enforcement ───────────────────────────────────────

  it('values are frozen against mutation through the accepted snapshot', () => {
    const snap: SettingsSnapshot = {
      values: { k: 'v' },
      overridden: ['k'],
      revision: 1,
    }
    const accepted = AcceptedSnapshot.accept(0, snap)!
    // Mutating the original input does not affect the accepted copy.
    snap.values['k'] = 'mutated'
    expect(accepted.values['k']).toBe('v')
  })

  // ── reset (reconnect path) ───────────────────────────────────────────

  it('reset accepts snapshot unconditionally regardless of revision', () => {
    const snap: SettingsSnapshot = {
      values: { k: 'v' },
      overridden: ['k'],
      revision: 0,
    }
    // After reconnect: this.revision = 5, incoming revision = 0
    const accepted = AcceptedSnapshot.reset(snap)
    expect(accepted).toBeInstanceOf(AcceptedSnapshot)
    expect(accepted.values['k']).toBe('v')
    expect(accepted.overridden.has('k')).toBe(true)
    expect(accepted.revision).toBe(0)
  })

  it('values are isolated from mutation after reset', () => {
    const snap: SettingsSnapshot = {
      values: { k: 'v' },
      overridden: [],
      revision: 0,
    }
    const accepted = AcceptedSnapshot.reset(snap)
    snap.values['k'] = 'mutated'
    expect(accepted.values['k']).toBe('v')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
//  applyAcceptedSnapshot
// ═══════════════════════════════════════════════════════════════════════════

describe('applyAcceptedSnapshot', () => {
  it('replaces values and overridden from the accepted snapshot', () => {
    const snap = AcceptedSnapshot.accept(0, {
      values: { k: 'v' },
      overridden: ['k'],
      revision: 2,
    })!
    const next = applyAcceptedSnapshot(snap)
    expect(next.values).toEqual({ k: 'v' })
    expect(hasKeys(next.overridden, 'k')).toBe(true)
    expect(next.revision).toBe(2)
  })

  it('clears draftValues and errors on receiving a fresh snapshot', () => {
    const snap = AcceptedSnapshot.accept(1, {
      values: { k: 'new' },
      overridden: [],
      revision: 2,
    })!
    const next = applyAcceptedSnapshot(snap)
    expect(next.draftValues).toEqual({})
    expect(next.errors).toEqual({})
    expect(next.values['k']).toBe('new')
    expect(next.overridden.size).toBe(0)
  })
})

describe('number range caption and error (nocx-w7h.7)', () => {
  const daysDecl: Declaration = {
    key: 'history.retentionDays',
    section: 'History',
    label: 'Keep history for',
    description: 'How long a completed command is kept.',
    control: 'number',
    dataClass: 'publicConfig',
    min: 0,
    max: 3650,
    unit: 'days',
  }

  it('the caption reads the range from Min/Max, with the unit', () => {
    expect(numberRangeCaption(daysDecl)).toBe('0 – 3650 days')
  })

  it('one-sided bounds produce one-sided captions; no bounds produce none', () => {
    const minOnly: Declaration = { ...daysDecl, max: undefined }
    const maxOnly: Declaration = { ...daysDecl, min: undefined }
    const none: Declaration = { ...daysDecl, min: undefined, max: undefined }
    expect(numberRangeCaption(minOnly)).toBe('≥ 0 days')
    expect(numberRangeCaption(maxOnly)).toBe('≤ 3650 days')
    expect(numberRangeCaption(none)).toBeUndefined()
  })

  // A sentinel explained only in prose is a sentinel nobody reads — the
  // owner's verdict on `Keep history for = 0`. The caption says what the
  // value MEANS while it is the sentinel, and goes back to the range as
  // soon as the number is an ordinary one.
  it('at a declared sentinel the caption says what the value means, not the range', () => {
    const withZero: Declaration = { ...daysDecl, zeroLabel: 'Kept until the size limit is reached' }
    expect(numberRangeCaption(withZero, 0)).toBe('Kept until the size limit is reached')
    expect(numberRangeCaption(withZero, 30)).toBe('0 – 3650 days')
    // A zero with nothing declared about it is just a number.
    expect(numberRangeCaption(daysDecl, 0)).toBe('0 – 3650 days')
    // And with no value to judge, the range is all there is to say.
    expect(numberRangeCaption(withZero)).toBe('0 – 3650 days')
  })

  it('a value outside the range yields the error; inside yields none', () => {
    expect(numberRangeError(daysDecl, -1)).toBe('Must be at least 0 days')
    expect(numberRangeError(daysDecl, 4000)).toBe('Must be at most 3650 days')
    expect(numberRangeError(daysDecl, 30)).toBeUndefined()
  })
})

// ═══════════════════════════════════════════════════════════════════════════
//  The routing matrix — axes derived from the declarations, never listed here
// ═══════════════════════════════════════════════════════════════════════════

describe('parseRouteSettingKey (nocx-3mniv)', () => {
  it('splits a well-formed cell key into its two ids', () => {
    expect(parseRouteSettingKey('notifications.route.programNotify.banner')).toEqual({
      rowId: 'programNotify',
      columnId: 'banner',
    })
  })

  it('is not fooled by a key that merely resembles one', () => {
    // Not under the namespace at all.
    expect(parseRouteSettingKey('notifications.debounceMs')).toBeNull()
    expect(parseRouteSettingKey('terminal.fontSize')).toBeNull()
    // Under the namespace and malformed: one segment, three segments, or an
    // empty one. Each of these renders as an ordinary row rather than
    // disappearing into the grid — see sectionBlocks below.
    expect(parseRouteSettingKey('notifications.route.programNotify')).toBeNull()
    expect(parseRouteSettingKey('notifications.route.a.b.c')).toBeNull()
    expect(parseRouteSettingKey('notifications.route..banner')).toBeNull()
    expect(parseRouteSettingKey('notifications.route.programNotify.')).toBeNull()
    expect(parseRouteSettingKey('notifications.route.')).toBeNull()
  })
})

describe('sectionBlocks (nocx-3mniv)', () => {
  function cell(kind: string, channel: string, label: string): Declaration {
    return {
      key: `notifications.route.${kind}.${channel}`,
      section: 'Notifications',
      label,
      description: `${label} description`,
      control: 'toggle',
      dataClass: 'publicConfig',
      default: false,
    }
  }

  const bellBanner = cell('bell', 'banner', 'A terminal bell → OS banner')
  const bellToast = cell('bell', 'toast', 'A terminal bell → In-app toast')
  const progBanner = cell('programNotify', 'banner', 'A program asked → OS banner')
  const progToast = cell('programNotify', 'toast', 'A program asked → In-app toast')

  const debounce: Declaration = {
    key: 'notifications.debounceMs',
    section: 'Notifications',
    label: 'Quiet window',
    description: 'How long one notification silences the next.',
    control: 'number',
    dataClass: 'publicConfig',
    default: 500,
    min: 0,
    max: 60000,
  }

  it('a section with no cell keys is unchanged — one block per declaration, in order', () => {
    const blocks = sectionBlocks([debounce])
    expect(blocks).toEqual([{ kind: 'setting', decl: debounce }])
  })

  it('collapses every cell in the section into one matrix, at the position of the first', () => {
    const blocks = sectionBlocks([bellBanner, bellToast, progBanner, progToast, debounce])
    expect(blocks.map((b) => b.kind)).toEqual(['matrix', 'setting'])
    expect(blocks[1]).toEqual({ kind: 'setting', decl: debounce })
  })

  it('the axes come from the keys in first-seen order, and nothing else names them', () => {
    const blocks = sectionBlocks([progBanner, progToast, bellBanner, bellToast])
    const b = blocks[0]
    expect(b.kind).toBe('matrix')
    if (b.kind !== 'matrix') return
    expect(b.matrix.rows.map((r) => r.id)).toEqual(['programNotify', 'bell'])
    expect(b.matrix.columns.map((c) => c.id)).toEqual(['banner', 'toast'])
  })

  // The declaration a backend has never shipped before is the whole test: no
  // list here needs editing for it to appear.
  it('a kind this code has never heard of gets its row from the declaration alone', () => {
    const invented = cell('diskFull', 'toast', 'The disk filled up → In-app toast')
    const blocks = sectionBlocks([progBanner, progToast, invented])
    const b = blocks[0]
    if (b.kind !== 'matrix') throw new Error('expected a matrix block')
    expect(b.matrix.rows.map((r) => r.id)).toEqual(['programNotify', 'diskFull'])
    expect(b.matrix.rows.map((r) => r.label)).toEqual(['A program asked', 'The disk filled up'])
    expect(b.matrix.cell('diskFull', 'toast')).toBe(invented)
    // And a pair the backend does not offer is absent, not invented.
    expect(b.matrix.cell('diskFull', 'banner')).toBeUndefined()
  })

  it('the axis labels are the halves of the cell label, and fall back to the id', () => {
    const b = sectionBlocks([bellBanner, bellToast])[0]
    if (b.kind !== 'matrix') throw new Error('expected a matrix block')
    expect(b.matrix.rows.map((r) => r.label)).toEqual(['A terminal bell'])
    expect(b.matrix.columns.map((c) => c.label)).toEqual(['OS banner', 'In-app toast'])

    // A label that does not carry the separator cannot remove a control from
    // the grid: the id stands in for the half that is missing.
    const odd = { ...cell('bell', 'banner', 'A terminal bell') }
    const c = sectionBlocks([odd])[0]
    if (c.kind !== 'matrix') throw new Error('expected a matrix block')
    expect(c.matrix.rows[0].label).toBe('A terminal bell')
    expect(c.matrix.columns[0].label).toBe('banner')
  })

  // "Visible rather than silently absent from the grid": a key under the
  // namespace that does not parse is still a control the user can operate.
  it('a malformed cell key renders as an ordinary setting, not as nothing', () => {
    const malformed: Declaration = {
      ...bellBanner,
      key: 'notifications.route.bell',
      label: 'A terminal bell',
    }
    const blocks = sectionBlocks([progBanner, malformed])
    expect(blocks.map((b) => b.kind)).toEqual(['matrix', 'setting'])
    expect(blocks[1]).toEqual({ kind: 'setting', decl: malformed })
  })

  it('a cell key carrying something other than a toggle is an ordinary setting too', () => {
    const notAToggle: Declaration = { ...progBanner, control: 'number', default: 3 }
    const blocks = sectionBlocks([notAToggle])
    expect(blocks).toEqual([{ kind: 'setting', decl: notAToggle }])
  })
})

describe("the bound on a person's own paragraph (nocx-avogl.4)", () => {
  const personal: Declaration = {
    key: 'assistant.personalInstructions',
    section: 'Instructions',
    label: 'Your instructions to the assistant',
    description: 'Standing instructions added to every question.',
    control: 'text',
    dataClass: 'privateContent',
    default: '',
    multiline: true,
    max: 2000,
    unit: 'characters',
  }

  it('states how long the text is and how long it may be, before anything is lost', () => {
    expect(textLengthCaption(personal, '')).toBe('0 / 2000 characters')
    expect(textLengthCaption(personal, 'abc')).toBe('3 / 2000 characters')
  })

  it('an unbounded text setting has no caption to show', () => {
    expect(textLengthCaption({ ...personal, max: undefined }, 'abc')).toBeUndefined()
    expect(textLengthError({ ...personal, max: undefined }, 'x'.repeat(9999))).toBeUndefined()
  })

  it('past the bound is an error in the same words a number ceiling uses', () => {
    expect(textLengthError(personal, 'x'.repeat(2000))).toBeUndefined()
    expect(textLengthError(personal, 'x'.repeat(2001))).toBe('Must be at most 2000 characters')
  })

  // Counted the way the backend counts, or the caption states a bound the
  // backend does not enforce. Go counts runes; JavaScript's .length counts
  // UTF-16 code units, so 1001 astral characters would read as 2002 here and
  // be refused on a screen the backend would have accepted.
  it('counts characters, not UTF-16 code units', () => {
    expect(textLengthCaption(personal, '🙂🙂')).toBe('2 / 2000 characters')
    expect(textLengthError(personal, '🙂'.repeat(1500))).toBeUndefined()
  })

  // The backend refuses over-long text too, in its own language. Printing
  // both puts one fact on the screen twice — the same defect the number
  // suppression was bought by.
  it('the backend rejection is not repeated under a field whose caption already says it', () => {
    const backend =
      'settings: "assistant.personalInstructions" validation failed: value is 2001 characters'
    expect(fieldSaveError(personal, NaN, backend, 'x'.repeat(2001))).toBeUndefined()
    expect(fieldSaveError(personal, NaN, backend, 'short')).toBe(backend)
  })
})
