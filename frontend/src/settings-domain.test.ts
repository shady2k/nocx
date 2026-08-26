// settings-domain — pure functions only, no jsdom, no DOM, no markup.
import { describe, it, expect } from 'vitest'
import {
  createMirror,
  numberRangeCaption,
  numberRangeError,
  textLengthCaption,
  textLengthError,
  fieldSaveError,
  recordSaveOutcome,
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
