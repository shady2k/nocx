// @vitest-environment jsdom
//
// The editor-side per-target store (ADR-0004 §3, nocx-4ff.7): drafts and
// session history keyed by the REGISTRY's target id. The seam is the id —
// a third target gets both by registering, nothing here names a target.
import { describe, it, expect } from 'vitest'
import { TargetState, queryTargetHistory, type TargetHistoryRow } from './target-state'

const draft = (text: string, from = 0, to = 0, scrollTop = 0) => ({ text, from, to, scrollTop })

let seq = 0
const row = (doc: string, cwd = '/repo', host = '', at = 1000): TargetHistoryRow => ({
  doc,
  cwd,
  host,
  at,
  seq: seq++,
})

describe('TargetState drafts', () => {
  it('drafts are keyed by target id: a switch saves one target’s draft and restores the other’s', () => {
    const s = new TargetState()
    s.saveDraft('shell', draft('git status', 3, 5, 12))
    expect(s.draft('shell')).toEqual(draft('git status', 3, 5, 12))
    // The agent has never been edited: no draft of its own.
    expect(s.draft('agent')).toBeUndefined()
  })

  it('a later save under the same id replaces the earlier draft (the live document wins)', () => {
    const s = new TargetState()
    s.saveDraft('shell', draft('git status'))
    s.saveDraft('shell', draft('git log'))
    expect(s.draft('shell')?.text).toBe('git log')
  })

  it('a third target registers and gets its own draft without any change to the store', () => {
    const s = new TargetState()
    s.saveDraft('shell', draft('echo hi'))
    s.saveDraft('recall', draft('a question'))
    expect(s.draft('recall')?.text).toBe('a question')
    expect(s.draft('shell')?.text).toBe('echo hi')
  })
})

describe('TargetState history', () => {
  it('record appends to the active target’s corpus only — corpora never interleave', () => {
    const s = new TargetState()
    s.record('shell', row('echo hi'))
    s.record('agent', row('what does docs mean?'))
    s.record('agent', row('and this one?'))
    s.record('shell', row('ls'))
    expect(s.history('agent').map((r) => r.doc)).toEqual(['what does docs mean?', 'and this one?'])
    expect(s.history('shell').map((r) => r.doc)).toEqual(['echo hi', 'ls'])
  })

  it('an id with no submissions has an empty corpus, never undefined', () => {
    const s = new TargetState()
    expect(s.history('agent')).toEqual([])
  })
})

describe('queryTargetHistory — a target’s corpus as a recall page', () => {
  it('serves the corpus newest first with the same rung filters the ledger fallback uses', () => {
    const page = queryTargetHistory(
      [row('older', '/repo', 'box'), row('newer', '/other', 'box'), row('elsewhere', '/repo', '')],
      'directory',
      '/repo',
      'box',
    )
    expect(page.entries.map((e) => e.command)).toEqual(['older'])
    expect(page.entries[0]).toMatchObject({
      status: 'unknown',
      maskedCount: 0,
      maskedKinds: [],
      cwd: '/repo',
      host: 'box',
    })
    expect(page.source).toBe('session')
    expect(page.exhausted).toBe(true)
  })

  it('the host rung filters by host alone; everywhere by nothing', () => {
    const rows = [row('local thing', '/repo', ''), row('remote thing', '/repo', 'box')]
    expect(queryTargetHistory(rows, 'host', '/repo', 'box').entries.map((e) => e.command)).toEqual([
      'remote thing',
    ])
    expect(queryTargetHistory(rows, 'everywhere', '/repo', '').entries).toHaveLength(2)
  })

  it('a text filter narrows the rung the same way the store query narrows', () => {
    const rows = [row('echo hi'), row('make deploy'), row('make test')]
    expect(
      queryTargetHistory(rows, 'everywhere', '/', '', 'make').entries.map((e) => e.command),
    ).toEqual(['make test', 'make deploy'])
  })

  it('coverage is the oldest row’s timestamp; an empty corpus has no horizon', () => {
    expect(
      queryTargetHistory([row('a', '/', '', 500), row('b', '/', '', 900)], 'everywhere', '/', '')
        .coverage,
    ).toBe(500)
    expect(queryTargetHistory([], 'everywhere', '/', '').coverage).toBeNull()
  })

  it('rows are stable across pages: the id survives a filter change (selection preservation)', () => {
    const rows = [row('echo one'), row('echo two')]
    const wide = queryTargetHistory(rows, 'everywhere', '/', '')
    const narrow = queryTargetHistory(rows, 'everywhere', '/', '', 'one')
    const wideId = wide.entries.find((e) => e.command === 'echo one')?.id
    const narrowId = narrow.entries.find((e) => e.command === 'echo one')?.id
    expect(narrowId).toBe(wideId)
    expect(wide.entries[0].id).not.toBe(wide.entries[1].id)
  })
})
