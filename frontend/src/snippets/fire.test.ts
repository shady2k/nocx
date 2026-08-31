// @vitest-environment jsdom
// The fire adapter — the composition root's half of the palette contract:
// session facts read AT FIRE TIME (never captured when the palette opened),
// one resolution per fire, delivery through the pane's insertSnippet (the
// one insertion policy, design §9.2) or the clipboard (an explicit
// destination, §9.2), and an ask value that never reaches the logging seam
// (§7.5 — only the title is logged).
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import type { SnippetFire } from '../terminal-content'
import type { SessionFacts } from './resolve'
import { createSnippetFireAdapter, type SnippetFireDeps } from './fire'

type FactsMock = Mock<() => Promise<SessionFacts>>
type InsertMock = Mock<(text: string) => Promise<SnippetFire>>
type WriteMock = Mock<(text: string) => Promise<void>>

/** The answers map — dynamic field names from the snippet body, so a Map
 *  is the model (resolveBody's own contract); the helper keeps the literals
 *  off the seam so the test reads as data, not construction. */
const answersOf = (entries: Array<[string, string]>): Map<string, string> => new Map(entries)

const SNIP = (over: Partial<{ id: string; title: string; body: string }> = {}) => ({
  id: 's1',
  title: 'the title',
  body: '',
  ...over,
})

const FACTS: SessionFacts = { cwd: '/repo', host: 'vm', user: 'dev', branch: 'main' }

interface FireDepsOver {
  facts?: FactsMock
  insert?: InsertMock
  activeInsertNull?: boolean
  write?: WriteMock
}
interface FireDeps extends SnippetFireDeps {
  /** The SAME insert mock the adapter calls — one per deps(), since the
   *  adapter reads activeInsert() once per fire and the assertions must
   *  inspect the mock it actually called. */
  insertSpy(): InsertMock
}

function deps(over: FireDepsOver = {}): FireDeps {
  const insert: InsertMock =
    over.insert ?? vi.fn().mockResolvedValue({ ok: true, where: 'pty' } satisfies SnippetFire)
  const target = { insertSnippet: insert }
  return {
    facts: over.facts ?? vi.fn().mockResolvedValue(FACTS),
    activeInsert: over.activeInsertNull === true ? () => null : () => target,
    clipboard: { writeText: over.write ?? vi.fn().mockResolvedValue(undefined) },
    insertSpy: () => insert,
  }
}

const fire = (
  d: SnippetFireDeps,
  body: string,
  over: { destination?: 'input' | 'clipboard'; answers?: Map<string, string> } = {},
) =>
  createSnippetFireAdapter(d)({
    snippet: SNIP({ body }),
    answers: over.answers ?? answersOf([]),
    destination: over.destination ?? 'input',
  })

afterEach(() => {
  vi.restoreAllMocks()
})

describe('the fire adapter (design §8, §9.2, §11)', () => {
  it('reads the session facts AT FIRE TIME — every call, never a captured snapshot', async () => {
    let cwd = '/first'
    const facts: FactsMock = vi.fn().mockImplementation(() => Promise.resolve({ ...FACTS, cwd }))
    const d = deps({ facts })
    await expect(fire(d, 'cd {{env:cwd}}')).resolves.toEqual({ kind: 'delivered', where: 'pty' })
    expect(facts).toHaveBeenCalledTimes(1)
    // The pane moved while the palette was open; the SECOND fire resolves
    // against the NEW cwd, because the adapter reads the pane per call.
    cwd = '/second'
    await fire(d, 'cd {{env:cwd}}')
    expect(d.insertSpy().mock.calls.map((c) => c[0])).toEqual(['cd /first', 'cd /second'])
  })

  it('resolves env and ask spans into the body once, then delivers through insertSnippet', async () => {
    const d = deps()
    const outcome = await createSnippetFireAdapter(d)({
      snippet: SNIP({ body: 'run {{env:branch}} with {{port=8080}}' }),
      answers: answersOf([['port', '9090']]),
      destination: 'input',
    })
    expect(outcome).toEqual({ kind: 'delivered', where: 'pty' })
    expect(d.insertSpy()).toHaveBeenCalledWith('run main with 9090')
  })

  it('refuses an env key the pane cannot answer, naming the keys, and delivers nothing', async () => {
    const d = deps({ facts: vi.fn().mockResolvedValue({ ...FACTS, branch: null }) })
    const outcome = await fire(d, 'echo {{env:branch}}')
    expect(outcome).toEqual({
      kind: 'refused',
      reason: { kind: 'env-unavailable', keys: ['branch'] },
    })
    expect(d.insertSpy()).not.toHaveBeenCalled()
  })

  // A MALFORMED BODY REFUSES AT THE FIRE, not only in the settings preview.
  // A snippet arrives through backup and restore and may never have been
  // opened in Settings at all, so the preview cannot be the only reader of
  // the parse — and a body that cannot be read must not be fired as the
  // literal text it happens to be (design §7 step 1).
  it('refuses a body that does not parse, names the problem, and writes nothing', async () => {
    const d = deps()
    const outcome = await fire(d, '{% if x %}no end')
    expect(outcome).toEqual({
      kind: 'refused',
      reason: { kind: 'malformed', detail: 'this condition has no {% endif %}' },
    })
    expect(d.insertSpy()).not.toHaveBeenCalled()
  })

  it('refuses with no-owner when no pane is active — never a fallthrough', async () => {
    const d = deps({ activeInsertNull: true })
    await expect(fire(d, 'echo hi')).resolves.toEqual({
      kind: 'refused',
      reason: { kind: 'no-owner' },
    })
  })

  it('maps each insertSnippet refusal to its structured reason (the palette owns the sentences)', async () => {
    const refused: Array<[SnippetFire, unknown]> = [
      [{ ok: false, reason: 'no-owner' }, { kind: 'no-owner' }],
      [
        { ok: false, reason: 'multi-line-no-bracketed-paste' },
        { kind: 'multi-line-no-bracketed-paste' },
      ],
      [
        { ok: false, reason: 'unresolved-secret', name: 'prod-db' },
        { kind: 'unresolved-secret', name: 'prod-db' },
      ],
      [{ ok: false, reason: 'write-failed' }, { kind: 'write-failed' }],
    ]
    for (const [fireResult, expected] of refused) {
      const d = deps({ insert: vi.fn().mockResolvedValue(fireResult) })
      const outcome = await fire(d, 'echo hi')
      expect(outcome).toEqual({ kind: 'refused', reason: expected })
    }
  })

  it('a secret reference to the clipboard is refused, naming it, and nothing is written (§11.1)', async () => {
    const write: WriteMock = vi.fn().mockResolvedValue(undefined)
    const d = deps({ write })
    const outcome = await fire(d, 'echo {{secret:prod-db}}', { destination: 'clipboard' })
    expect(outcome).toEqual({
      kind: 'refused',
      reason: { kind: 'secret-to-clipboard', name: 'prod-db' },
    })
    expect(write).not.toHaveBeenCalled()
  })

  it('a clean clipboard fire writes the resolved text and delivers as clipboard', async () => {
    const write: WriteMock = vi.fn().mockResolvedValue(undefined)
    const d = deps({ write })
    const outcome = await fire(d, 'hello {{env:user}}', { destination: 'clipboard' })
    expect(outcome).toEqual({ kind: 'delivered', where: 'clipboard' })
    expect(write).toHaveBeenCalledWith('hello dev')
  })

  it('a refused clipboard write is refused, not swallowed', async () => {
    const d = deps({ write: vi.fn().mockRejectedValue(new Error('denied')) })
    const outcome = await fire(d, 'hello', { destination: 'clipboard' })
    expect(outcome).toEqual({ kind: 'refused', reason: { kind: 'write-failed' } })
  })

  it('an ask answer never reaches the logging seam — only the title is logged (§7.5)', async () => {
    const consoleLog = vi.spyOn(console, 'log').mockImplementation(() => {})
    const consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    const secretish = 'hunter2-not-a-real-value'
    const d = deps()
    await createSnippetFireAdapter(d)({
      snippet: SNIP({ body: 'ssh -L {{local=8080}}' }),
      answers: answersOf([['local', secretish]]),
      destination: 'input',
    })
    const all = [...consoleLog.mock.calls, ...consoleWarn.mock.calls, ...consoleError.mock.calls]
      .map((c) => c.join(' '))
      .join('\n')
    expect(all).not.toContain(secretish)
    // The title is the one allowed fact about a fire.
    expect(all).toContain('the title')
  })

  it('a fire whose body still carries an unanswered ask field refuses rather than firing literal text', async () => {
    const d = deps()
    const outcome = await createSnippetFireAdapter(d)({
      snippet: SNIP({ body: 'echo {{port}}' }),
      answers: answersOf([]),
      destination: 'input',
    })
    expect(outcome.kind).toBe('refused')
    expect(d.insertSpy()).not.toHaveBeenCalled()
  })
})
