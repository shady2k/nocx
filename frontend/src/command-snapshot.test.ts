// @vitest-environment jsdom
// Command-existence snapshot store (OSC 636). The shell hook emits a session
// hello carrying a nonce, then snapshots of the live command table; the store
// enforces the nonce handshake and the size caps, and keeps the previous
// snapshot on any malformed or oversized payload. The rendering verdicts
// (resolved/unresolved/indeterminate/unavailable) live in shell-highlight.ts;
// this module only answers "is there a snapshot and does it contain this name".
import { describe, it, expect, beforeEach } from 'vitest'
import {
  CommandSnapshotStore,
  parseOsc636,
  MAX_SNAPSHOT_NAMES,
  MAX_SNAPSHOT_CHARS,
  MAX_SHARED_NAMES,
} from './command-snapshot'

const NONCE = 'a1b2c3d4e5f60718293a4b5c6d7e8f90'

/** Mirror of the hook's hex escaping: \\ for backslash, \xHH for control/C1
 *  and ';', everything else raw. Test names are ASCII, so this is a no-op
 *  except where a test deliberately exercises escaping. */
function hexEscape(name: string): string {
  return (
    name
      .replace(/[\\;]/g, (ch) => (ch === '\\' ? '\\\\' : '\\x3b'))
      // The control/C1 range is the point of this mirror — it must escape
      // exactly what the hook escapes.
      .replace(
        // eslint-disable-next-line no-control-regex
        /[\x00-\x1f\x7f-\x9f]/g,
        (ch) => `\\x${ch.charCodeAt(0).toString(16).padStart(2, '0')}`,
      )
  )
}

function snapshotPayload(names: string[], nonce = NONCE): string {
  return `S;${nonce};${names.map(hexEscape).join(';')}`
}

/** Give a store the SHARED half (the target's PATH set), the way the answer
 *  to shell.commandNames does. `status` requires both halves, so a test that
 *  asserts a store can judge existence has to supply both. */
function applyShared(store: CommandSnapshotStore, names: string[], state = 'ready' as const): void {
  store.applySharedNames({ state, names, ageMs: 0, reason: '', truncated: false })
}

describe('parseOsc636', () => {
  it('parses a hello (session nonce)', () => {
    expect(parseOsc636(`H;${NONCE}`)).toEqual({ kind: 'hello', nonce: NONCE })
  })

  it('parses a snapshot of names', () => {
    expect(parseOsc636(snapshotPayload(['pwd', 'ls', 'sdfsdf']))).toEqual({
      kind: 'snapshot',
      nonce: NONCE,
      names: ['pwd', 'ls', 'sdfsdf'],
    })
  })

  it('decodes \\\\ as backslash and \\xHH as the byte', () => {
    expect(parseOsc636(snapshotPayload(['a\\b', 'c;d', 'e\x07f']))).toEqual({
      kind: 'snapshot',
      nonce: NONCE,
      names: ['a\\b', 'c;d', 'e\x07f'],
    })
  })

  it('passes raw UTF-8 through (the terminal already decoded it)', () => {
    expect(parseOsc636(snapshotPayload(['café']))).toEqual({
      kind: 'snapshot',
      nonce: NONCE,
      names: ['café'],
    })
  })

  // Every other case here feeds the parser a payload this file built with
  // its own mirror of the escaping — which proves the parser is consistent
  // with the mirror, never that it is consistent with a shell. This one is
  // the literal a REAL shell produced: the exact bytes
  // `internal/shellintegration/scripts/nocx.{bash,zsh}` emit for these eight
  // names, pinned on the shell side by TestBashSnapshotEncodesHostileNames
  // and TestZshSnapshotEncodesHostileNames (same eight names, same expected
  // string). Both ends are held against one literal, so a change to either
  // encoder breaks a test on its own side rather than passing quietly on
  // both.
  it('decodes the literal payload the shell hooks actually emit', () => {
    const wire = 'plain;with space;semi\\x3bcolon;back\\\\slash;ctl\\x01x;del\\x7fx;c1\\x82x;utfé;'
    expect(parseOsc636(`S;${NONCE};${wire}`)).toEqual({
      kind: 'snapshot',
      nonce: NONCE,
      names: [
        'plain',
        'with space',
        'semi;colon',
        'back\\slash',
        'ctl\x01x',
        'del\x7fx',
        'c1\x82x',
        'utfé',
      ],
    })
  })

  it('skips empty segments (the hook joins names with trailing separators)', () => {
    expect(parseOsc636(snapshotPayload(['pwd', 'ls']).concat(';'))).toEqual({
      kind: 'snapshot',
      nonce: NONCE,
      names: ['pwd', 'ls'],
    })
  })
  it('returns null for an unknown sub-command (whitelist on receive)', () => {
    expect(parseOsc636('X;whatever')).toBeNull()
  })

  it('returns null for a hello with no nonce', () => {
    expect(parseOsc636('H;')).toBeNull()
  })

  it('returns null for a hello with trailing fields', () => {
    expect(parseOsc636(`H;${NONCE};junk`)).toBeNull()
  })

  it('returns null for a snapshot with no nonce', () => {
    expect(parseOsc636('S;;pwd')).toBeNull()
  })

  it('returns null for a malformed hex escape', () => {
    expect(parseOsc636('S;n;\\xZZ')).toBeNull()
    expect(parseOsc636('S;n;trailing\\')).toBeNull()
  })

  it('returns null for an empty snapshot (an "everything is unknown" lie)', () => {
    expect(parseOsc636(`S;${NONCE};`)).toBeNull()
    expect(parseOsc636(`S;${NONCE};;;`)).toBeNull()
  })
})

describe('CommandSnapshotStore', () => {
  let store: CommandSnapshotStore

  beforeEach(() => {
    store = new CommandSnapshotStore()
  })

  it('starts unavailable with no names', () => {
    expect(store.status).toBe('unavailable')
    expect(store.has('pwd')).toBe(false)
  })

  it('the hello establishes the nonce; a matching snapshot becomes ready', () => {
    store.ingest(`H;${NONCE}`)
    expect(store.status).toBe('unavailable') // hello alone is not a snapshot
    store.ingest(snapshotPayload(['pwd', 'ls']))
    expect(store.status).toBe('unavailable') // and one half is not both
    applyShared(store, ['git'])
    expect(store.status).toBe('ready')
    expect(store.has('pwd')).toBe(true)
    expect(store.has('ls')).toBe(true)
    expect(store.has('sdfsdf')).toBe(false)
  })

  it('a snapshot before the hello is discarded (nonce unknown)', () => {
    store.ingest(snapshotPayload(['pwd']))
    expect(store.status).toBe('unavailable')
  })

  it('a snapshot with the wrong nonce is discarded silently and the previous snapshot survives', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['pwd']))
    store.ingest(snapshotPayload(['evil'], 'deadbeefdeadbeefdeadbeefdeadbeef'))
    applyShared(store, ['git'])
    expect(store.status).toBe('ready')
    expect(store.has('pwd')).toBe(true)
    expect(store.has('evil')).toBe(false)
  })

  it('only the FIRST hello is accepted — a forged re-hello cannot re-anchor the nonce', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(`H;deadbeefdeadbeefdeadbeefdeadbeef`)
    store.ingest(snapshotPayload(['evil'], 'deadbeefdeadbeefdeadbeefdeadbeef'))
    expect(store.status).toBe('unavailable')
  })

  it("two stores never see each other's payloads — per-tab isolation", () => {
    const a = new CommandSnapshotStore()
    const b = new CommandSnapshotStore()
    const NONCE_B = 'deadbeefdeadbeefdeadbeefdeadbeef'
    a.ingest(`H;${NONCE}`)
    a.ingest(snapshotPayload(['pwd', 'ls']))
    b.ingest(`H;${NONCE_B}`)
    b.ingest(snapshotPayload(['kubectl'], NONCE_B))
    applyShared(a, ['git'])
    applyShared(b, ['git'])
    expect(a.status).toBe('ready')
    expect(a.has('pwd')).toBe(true)
    expect(a.has('ls')).toBe(true)
    expect(a.has('kubectl')).toBe(false)
    expect(b.status).toBe('ready')
    expect(b.has('kubectl')).toBe(true)
    expect(b.has('pwd')).toBe(false)
    // A third tab whose session never sent a snapshot reports unavailable
    // even while both of the others are ready.
    const c = new CommandSnapshotStore()
    expect(c.status).toBe('unavailable')
    expect(c.has('pwd')).toBe(false)
  })

  it('a newer valid snapshot replaces the previous one', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['pwd']))
    store.ingest(snapshotPayload(['ls']))
    expect(store.has('pwd')).toBe(false)
    expect(store.has('ls')).toBe(true)
  })

  it('an oversized name count is rejected without clearing the previous snapshot', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['pwd']))
    const huge = Array.from({ length: MAX_SNAPSHOT_NAMES + 1 }, (_, i) => `cmd${i}`)
    store.ingest(snapshotPayload(huge))
    applyShared(store, ['git'])
    expect(store.status).toBe('ready')
    expect(store.has('pwd')).toBe(true)
  })
  it('an oversized payload is rejected without clearing the previous snapshot', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['pwd']))
    const name = 'x'.repeat(MAX_SNAPSHOT_CHARS + 1)
    store.ingest(snapshotPayload([name]))
    applyShared(store, ['git'])
    expect(store.status).toBe('ready')
    expect(store.has('pwd')).toBe(true)
  })

  it('a malformed payload is rejected without clearing the previous snapshot', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['pwd']))
    store.ingest('S;' + NONCE + ';bad\\escape')
    applyShared(store, ['git'])
    expect(store.status).toBe('ready')
    expect(store.has('pwd')).toBe(true)
  })

  it('notifies subscribers when a snapshot is applied, and unsubscribing stops that', () => {
    store.ingest(`H;${NONCE}`)
    const seen: string[] = []
    const un = store.subscribe(() => seen.push('applied'))
    store.ingest(snapshotPayload(['pwd']))
    expect(seen).toEqual(['applied'])
    un()
    store.ingest(snapshotPayload(['ls']))
    expect(seen).toEqual(['applied'])
  })

  it('does not notify on discarded payloads', () => {
    store.ingest(`H;${NONCE}`)
    const seen: string[] = []
    const un = store.subscribe(() => seen.push('applied'))
    store.ingest(snapshotPayload(['evil'], 'deadbeefdeadbeefdeadbeefdeadbeef'))
    store.ingest('S;' + NONCE + ';bad\\escape')
    expect(seen).toEqual([])
    un()
  })

  // ── the shared half (shell.commandNames) ──────────────────────────────
  //
  // Two halves, two truths: the session-local tables belong to THIS shell
  // and arrive over OSC 636; the target's PATH set is identical for every
  // session to that target and arrives over shell.commandNames. The store
  // holds both and is careful about which question each can answer.

  it('existence needs BOTH halves — one alone cannot say a command does not exist', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['myalias']))
    // With only the shell's own tables, `git` is not in the store — and
    // saying "does not exist" here would strike through a real command
    // because the PATH scan had not landed.
    expect(store.status).toBe('unavailable')
    applyShared(store, ['git', 'ls'])
    expect(store.status).toBe('ready')
    expect(store.has('git')).toBe(true)
    expect(store.has('myalias')).toBe(true)
    expect(store.has('sdfsdf')).toBe(false)
  })

  it('completion offers the union of whatever has arrived', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['gitalias']))
    expect(store.matching('git')).toEqual(['gitalias'])
    applyShared(store, ['git', 'gitk'])
    expect(store.matching('git')).toEqual(['git', 'gitalias', 'gitk'])
  })

  it('a shared half that did not complete publishes nothing and keeps its state', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['myalias']))
    applyShared(store, ['git'])
    expect(store.has('git')).toBe(true)

    store.applySharedNames({
      state: 'timed-out',
      names: ['partial'],
      ageMs: 0,
      reason: 'the scan did not finish',
      truncated: false,
    })
    expect(store.commandNamesState).toBe('timed-out')
    expect(store.has('partial')).toBe(false)
    expect(store.has('git')).toBe(false)
    // And existence is indeterminate again: the half that answers for PATH
    // is gone, so nothing may be called nonexistent.
    expect(store.status).toBe('unavailable')
  })

  it('a stale shared half is served, with its age', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['myalias']))
    store.applySharedNames({
      state: 'stale',
      names: ['git'],
      ageMs: 90_000,
      reason: '',
      truncated: false,
    })
    expect(store.commandNamesState).toBe('stale')
    expect(store.commandNamesAgeMs).toBe(90_000)
    expect(store.has('git')).toBe(true)
    expect(store.status).toBe('ready')
  })

  it('an over-long shared set is refused rather than truncated', () => {
    const huge = Array.from({ length: MAX_SHARED_NAMES + 1 }, (_, i) => `cmd${i}`)
    applyShared(store, huge)
    expect(store.has('cmd0')).toBe(false)
    expect(store.status).toBe('unavailable')
  })

  it('the shared half starts as running — nothing has been asked yet', () => {
    expect(store.commandNamesState).toBe('running')
    expect(store.commandNamesAgeMs).toBe(0)
    expect(store.commandNamesReason).toBe('')
  })

  it('applying the shared half notifies subscribers', () => {
    const seen: string[] = []
    const un = store.subscribe(() => seen.push('applied'))
    applyShared(store, ['git'])
    expect(seen).toEqual(['applied'])
    un()
  })

  it('reset clears the nonce and the snapshot', () => {
    store.ingest(`H;${NONCE}`)
    store.ingest(snapshotPayload(['pwd']))
    applyShared(store, ['git'])
    store.reset()
    expect(store.status).toBe('unavailable')
    expect(store.has('pwd')).toBe(false)
    expect(store.has('git')).toBe(false)
    expect(store.commandNamesState).toBe('running')
  })
})
