import { describe, expect, it } from 'vitest'

import { redactHome, updateFailureDetails, updateStateLine } from './update-details'

/**
 * The details are pasted into public issues (nocx-vh2hr.1.16), so two things
 * are asserted here and nothing else: the facts a diagnosis needs are in the
 * text, and the home directory never is — including inside the backend's own
 * error, which is the field most likely to carry a path.
 */
describe('redactHome', () => {
  it('replaces a macOS or Linux home directory with ~ and keeps the path', () => {
    expect(redactHome('/Users/ada/Library/Caches/nocx/update.zip')).toBe(
      '~/Library/Caches/nocx/update.zip',
    )
    expect(redactHome('/home/ada/.local/state/nocx')).toBe('~/.local/state/nocx')
    expect(redactHome('read /home/ada/x: No such file')).toBe('read ~/x: No such file')
  })

  it('leaves paths that are not a home directory alone', () => {
    // What the product itself named is what makes the report useful.
    expect(redactHome('/Applications/nocx.app/Contents/MacOS/nocx')).toBe(
      '/Applications/nocx.app/Contents/MacOS/nocx',
    )
    expect(redactHome('/private/var/folders/xy/T/nocx')).toBe('/private/var/folders/xy/T/nocx')
    expect(redactHome('/Users')).toBe('/Users')
  })

  it('replaces every home in one line', () => {
    expect(redactHome('/Users/ada/a -> /Users/ada/b')).toBe('~/a -> ~/b')
  })
})

describe('updateStateLine', () => {
  it('says a journal is present only when one is', () => {
    expect(updateStateLine({ Pending: true })).toBe('a rollback journal is present')
    expect(updateStateLine({ Pending: true, HeldByOlderCoordinator: true })).toBe(
      'a rollback journal is present; the update waits for an older coordinator',
    )
    expect(updateStateLine({ Pending: false })).toBe('no journal; nothing is pending')
  })

  it('reports a download in flight as its progress', () => {
    expect(updateStateLine({ Downloading: true, Downloaded: 63_000_000, Total: 150_000_000 })).toBe(
      'Downloading 42% · 63 of 150 MB',
    )
    // A manifest that named no total: the notice's own word for it.
    expect(updateStateLine({ Downloading: true })).toBe('Downloading…')
  })
})

describe('updateFailureDetails', () => {
  const facts = {
    stage: 'apply',
    error:
      'update apply: download write: context deadline exceeded: /Users/ada/Library/Caches/nocx/update.zip',
    version: '0.8.1',
    build: '202611021101',
    channel: 'nightly',
    platform: 'darwin/arm64',
    state: 'a rollback journal is present',
  }

  it('carries the real error, its step, the build and the platform', () => {
    const text = updateFailureDetails(facts)
    expect(text).toBe(
      [
        'nocx update failure',
        'stage: apply',
        'error: update apply: download write: context deadline exceeded: ~/Library/Caches/nocx/update.zip',
        'version: 0.8.1',
        'build: 202611021101',
        'channel: nightly',
        'platform: darwin/arm64',
        'update state: a rollback journal is present',
      ].join('\n'),
    )
    expect(text).not.toContain('/Users/ada')
  })

  it('lists the managed debris the refusal named, redacted the same way', () => {
    const text = updateFailureDetails({
      ...facts,
      error:
        "update apply: reconcile before apply: the backup at /Users/ada/Applications/.nocx-backup.app is not nocx's own bundle — move or remove it, then try again",
    })
    expect(text).toContain('debris: ~/Applications/.nocx-backup.app')
    expect(text).not.toContain('/Users/ada')
  })

  it('says nothing it was not given: no debris line without one, no other text', () => {
    const text = updateFailureDetails({ ...facts, error: 'update apply: extraction failed in 3 s' })
    expect(text).not.toMatch(/debris/)
    expect(text.split('\n')).toHaveLength(8)
  })
})
