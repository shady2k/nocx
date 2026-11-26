// @vitest-environment jsdom
//
// The far helper's axis, folded once for every surface (ADR-727g63tr
// decision 2, design §5.10). The test walks the CONTRACT's branches, not a
// list this module invented: a branch the wire can send and the fold cannot
// phrase fails here, and a sentence that leaks an enum token into a person's
// window fails with it.
import { afterEach, describe, expect, it } from 'vitest'

import type { PaneConnection } from '../generated/pane.connection'
import { setActiveLocale } from '../i18n/locale-model'
import { helperAxis, helperGate, savedProfileId, withSavedConnection } from './helper-axis'

type Helper = PaneConnection['helper']

const TOKEN =
  /mode-raw|mode-script|helper-lost|install-failed|consent-pending|consent-denied|unsupported-platform|exec-forbidden|version-mismatch/i

const branches: Helper[] = [
  { state: 'starting', action: 'none' },
  { state: 'serving', action: 'none' },
  { state: 'paused', reason: 'connection', action: 'none' },
  { state: 'unavailable', reason: 'mode-raw', action: 'host-settings' },
  { state: 'unavailable', reason: 'mode-script', action: 'host-settings' },
  { state: 'unavailable', reason: 'helper-lost', action: 'refresh' },
  { state: 'unavailable', reason: 'helper-lost', action: 'none' },
  { state: 'unavailable', reason: 'consent-pending', action: 'none' },
  { state: 'unavailable', reason: 'consent-denied', action: 'none' },
  { state: 'unavailable', reason: 'unsupported-platform', action: 'none' },
  { state: 'unavailable', reason: 'install-failed', action: 'none' },
  { state: 'unavailable', reason: 'exec-forbidden', action: 'none' },
  { state: 'unavailable', reason: 'version-mismatch', action: 'none' },
  { state: 'unavailable', reason: 'connection', action: 'none' },
]

afterEach(() => setActiveLocale('en'))

describe('the helper axis is folded once', () => {
  it('gives every branch that has something to say a sentence with no token in it', () => {
    for (const helper of branches) {
      const axis = helperAxis(helper)
      expect(axis, JSON.stringify(helper)).not.toBeNull()
      if (helper.state === 'serving') {
        expect(axis?.sentence, 'serving says nothing').toBeNull()
        continue
      }
      expect(axis?.sentence, JSON.stringify(helper)).toBeTruthy()
      expect(axis?.sentence ?? '', JSON.stringify(helper)).not.toMatch(TOKEN)
    }
  })

  it('offers exactly the backend action — no refresh on a decision, no host settings on a loss', () => {
    expect(
      helperAxis({ state: 'unavailable', reason: 'helper-lost', action: 'refresh' })?.action,
    ).toBe('refresh')
    expect(
      helperAxis({ state: 'unavailable', reason: 'helper-lost', action: 'none' })?.action,
    ).toBeNull()
    expect(
      helperAxis({ state: 'unavailable', reason: 'install-failed', action: 'none' })?.action,
    ).toBeNull()
    expect(
      helperAxis({ state: 'unavailable', reason: 'consent-denied', action: 'none' })?.action,
    ).toBeNull()
    expect(
      helperAxis({ state: 'unavailable', reason: 'mode-raw', action: 'host-settings' })?.action,
    ).toBe('host-settings')
    expect(
      helperAxis({ state: 'unavailable', reason: 'mode-script', action: 'host-settings' })?.action,
    ).toBe('host-settings')
    expect(helperAxis({ state: 'paused', reason: 'connection', action: 'none' })?.action).toBeNull()
    expect(helperAxis({ state: 'starting', action: 'none' })?.action).toBeNull()
    expect(helperAxis({ state: 'serving', action: 'none' })?.action).toBeNull()
  })

  it('says starting quietly and a refusal as a warning', () => {
    expect(helperAxis({ state: 'starting', action: 'none' })?.tone).toBe('muted')
    expect(helperAxis({ state: 'paused', reason: 'connection', action: 'none' })?.tone).toBe(
      'warning',
    )
    expect(
      helperAxis({ state: 'unavailable', reason: 'helper-lost', action: 'refresh' })?.tone,
    ).toBe('warning')
  })

  it('has no axis when the connection has not answered — it mints no state', () => {
    expect(helperAxis(null)).toBeNull()
    expect(helperAxis(undefined)).toBeNull()
  })
})

describe('the saved-connection fold is one answer for the head and the panel', () => {
  const settings = helperAxis({ state: 'unavailable', reason: 'mode-raw', action: 'host-settings' })

  it('calls an empty Quick Connect profile unsaved, exactly like null', () => {
    // Quick Connect opens a host with profileId '' (main.tsx's
    // newSSHPane('', ...)). A saved profile is a non-empty id and nothing
    // else; every surface asks here rather than testing the raw option.
    expect(savedProfileId(null)).toBeNull()
    expect(savedProfileId(undefined)).toBeNull()
    expect(savedProfileId('')).toBeNull()
    expect(savedProfileId('profile-1')).toBe('profile-1')
  })

  it('replaces the settings door with the save sentence on an ad-hoc tab', () => {
    const folded = withSavedConnection(settings, '')
    expect(folded?.action).toBeNull()
    expect(folded?.sentence).toContain('Raw integration')
    expect(folded?.sentence).toContain('Save this connection to change how nocx integrates')
    expect(folded?.sentence ?? '').not.toMatch(TOKEN)
  })

  it('leaves a saved connection’s settings door alone', () => {
    const folded = withSavedConnection(settings, 'profile-1')
    expect(folded?.action).toBe('host-settings')
    expect(folded?.sentence).not.toContain('Save this connection')
  })

  it('never touches an action that is not the settings door', () => {
    const lost = helperAxis({ state: 'unavailable', reason: 'helper-lost', action: 'refresh' })
    expect(withSavedConnection(lost, '')?.action).toBe('refresh')
    expect(withSavedConnection(null, '')).toBeNull()
  })
})

describe('the gate every surface asks before it touches a host', () => {
  it('allows a local tab, which has no helper at all', () => {
    expect(helperGate({ kind: 'local' })).toBeNull()
    expect(helperGate({ kind: 'local', connection: null })).toBeNull()
  })

  it('holds an SSH tab whose observation has not arrived, and one that says starting', () => {
    // Both mean "nothing to ask yet": an observation that has not arrived has
    // no state to draw, and inventing one would be this layer deciding a
    // connection fact (ADR-ba6xet1b).
    expect(helperGate({ kind: 'ssh' })).toBe('starting')
    expect(helperGate({ kind: 'ssh', connection: null })).toBe('starting')
    expect(
      helperGate({ kind: 'ssh', connection: { helper: { state: 'starting', action: 'none' } } }),
    ).toBe('starting')
  })

  it('allows exactly the serving axis, and blocks every other state', () => {
    expect(
      helperGate({ kind: 'ssh', connection: { helper: { state: 'serving', action: 'none' } } }),
    ).toBeNull()
    for (const helper of branches) {
      if (helper.state === 'serving' || helper.state === 'starting') continue
      expect(helperGate({ kind: 'ssh', connection: { helper } }), JSON.stringify(helper)).toBe(
        'blocked',
      )
    }
  })
})
