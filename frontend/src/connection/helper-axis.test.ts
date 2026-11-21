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
import { helperAxis } from './helper-axis'

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
