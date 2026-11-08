// The store behind "a hole a person has been told about is not announced
// again for that session" (nocx-ipuy3, view state is kept, not store state).
// It is RENDERER PRESENTATION state and lives in localStorage for the same
// two reasons IntegrationSilenceStore gives (integration/status.ts): it must
// survive a restart, and it is per machine rather than a synced setting.
import { describe, expect, it } from 'vitest'

import {
  RecoveryAnnouncementStore,
  type RecoveryAnnouncementStorage,
} from './recovery-announcements'

function memoryStorage(): RecoveryAnnouncementStorage {
  const items = new Map<string, string>()
  return {
    getItem: (key) => items.get(key) ?? null,
    setItem: (key, value) => {
      items.set(key, value)
    },
  }
}

describe('RecoveryAnnouncementStore', () => {
  it('answers nothing for a session nothing was announced for', () => {
    const store = new RecoveryAnnouncementStore(memoryStorage())
    expect(store.announced('sid-a')).toEqual([])
  })

  it('survives a restart, because a new store over the same storage reads it back', () => {
    const storage = memoryStorage()
    new RecoveryAnnouncementStore(storage).record('sid-a', [{ start: 100, end: 300 }])
    expect(new RecoveryAnnouncementStore(storage).announced('sid-a')).toEqual([
      { start: 100, end: 300 },
    ])
  })

  it('keeps sessions apart, so one window never silences another session', () => {
    const store = new RecoveryAnnouncementStore(memoryStorage())
    store.record('sid-a', [{ start: 0, end: 10 }])
    expect(store.announced('sid-b')).toEqual([])
  })

  it('merges what is already announced with the growth instead of replacing it', () => {
    const store = new RecoveryAnnouncementStore(memoryStorage())
    store.record('sid-a', [{ start: 100, end: 300 }])
    store.record('sid-a', [{ start: 300, end: 400 }])
    expect(store.announced('sid-a')).toEqual([{ start: 100, end: 400 }])
  })

  it('unions overlapping and unsorted ranges into a non-overlapping list', () => {
    const store = new RecoveryAnnouncementStore(memoryStorage())
    store.record('sid-a', [
      { start: 300, end: 400 },
      { start: 100, end: 200 },
    ])
    store.record('sid-a', [{ start: 150, end: 350 }])
    expect(store.announced('sid-a')).toEqual([{ start: 100, end: 400 }])
  })

  it('drops ranges that are backwards or not finite rather than letting them poison the record', () => {
    const store = new RecoveryAnnouncementStore(memoryStorage())
    store.record('sid-a', [
      { start: 10, end: 10 },
      { start: 20, end: 15 },
      { start: Number.NaN, end: 30 },
      { start: 40, end: 50 },
    ])
    expect(store.announced('sid-a')).toEqual([{ start: 40, end: 50 }])
  })

  it('keeps the most recent sessions and lets the oldest go', () => {
    const store = new RecoveryAnnouncementStore(memoryStorage())
    for (let i = 0; i < 70; i++) store.record(`sid-${i}`, [{ start: 0, end: 10 }])
    expect(store.announced('sid-0')).toEqual([])
    expect(store.announced('sid-69')).toEqual([{ start: 0, end: 10 }])
  })

  it('reads a corrupt record as nothing announced', () => {
    const storage = memoryStorage()
    storage.setItem('nocx.recovery.announced.v1', '{"not":"a list"}')
    expect(new RecoveryAnnouncementStore(storage).announced('sid-a')).toEqual([])
  })

  it('survives a storage that refuses to read or write', () => {
    const denied: RecoveryAnnouncementStorage = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('denied')
      },
    }
    const store = new RecoveryAnnouncementStore(denied)
    expect(store.announced('sid-a')).toEqual([])
    expect(() => store.record('sid-a', [{ start: 0, end: 10 }])).not.toThrow()
  })

  it('does nothing at all without storage', () => {
    const store = new RecoveryAnnouncementStore(null)
    store.record('sid-a', [{ start: 0, end: 10 }])
    expect(store.announced('sid-a')).toEqual([])
  })
})
