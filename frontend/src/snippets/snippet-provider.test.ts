// The snippet completion provider (design §10.2, bead nocx-nlhe): snippets
// in the ONE dropdown, as rows beside command names — never a second
// suggestion surface, and never ghost text.
//
// The rules that are not about this provider in isolation are tested where
// they live: the ranking collision against a real command name is in
// rank.test.ts, and "acceptance resolves, Tab does not auto-apply" is in
// controller.test.ts. A snippet tested alone would pass all three while the
// dropdown put a saved phrase over the user's executable.
import { describe, expect, it, vi } from 'vitest'
import { snippetProvider } from './snippet-provider'
import { createSnippetFireAdapter } from './fire'
import type { SessionFacts } from './resolve'
import type { SuggestContext } from '../suggest/providers'
import type { Snippet } from './snippets-store'
import type { SnippetFire } from '../terminal-content'

const SNIP = (over: Partial<Snippet> & { id: string }): Snippet => ({
  title: over.id,
  body: 'body',
  ...over,
})

function ctxFor(doc: string, token: string, position: 'command' | 'argument'): SuggestContext {
  const from = doc.lastIndexOf(token)
  return {
    doc,
    token: { text: token, from, to: from + token.length },
    position,
    isLocal: true,
    cwd: '/repo',
    host: '',
  }
}

function providerOver(snippets: Snippet[], onLoad = vi.fn()) {
  return snippetProvider({ snippets: () => snippets, ensureLoaded: onLoad })
}

const ask = (p: ReturnType<typeof providerOver>, ctx: SuggestContext) =>
  p.suggest(ctx, new AbortController().signal) as { candidates: { id: string }[] }

describe('the snippet provider (nocx-nlhe)', () => {
  it('answers in command position, matched on the TITLE', () => {
    const p = providerOver([
      SNIP({ id: 'a', title: 'deploy-staging', body: 'kubectl rollout status api' }),
      SNIP({ id: 'b', title: 'tail-logs', body: 'journalctl -f' }),
    ])
    const out = ask(p, ctxFor('dep', 'dep', 'command'))
    expect(out.candidates.map((c) => c.id)).toEqual(['snippet:a'])
  })

  it('does not match on the body — the row a person picks is the one they can see', () => {
    // The title is what the dropdown shows and what the query is measured
    // against; a body match would offer a row whose text explains nothing.
    const p = providerOver([SNIP({ id: 'a', title: 'deploy', body: 'kubectl rollout' })])
    expect(ask(p, ctxFor('kube', 'kube', 'command')).candidates).toHaveLength(0)
  })

  it('is not applicable in argument position, nor for a path-looking token', () => {
    const p = providerOver([SNIP({ id: 'a', title: 'deploy' })])
    expect(p.applicable(ctxFor('git dep', 'dep', 'argument'))).toBe(false)
    expect(p.applicable(ctxFor('./dep', './dep', 'command'))).toBe(false)
    expect(p.applicable(ctxFor('dep', 'dep', 'command'))).toBe(true)
  })

  it('answers nothing for an empty token — a bare prompt is not a snippet query', () => {
    const p = providerOver([SNIP({ id: 'a', title: 'deploy' })])
    expect(ask(p, ctxFor('', '', 'command')).candidates).toHaveLength(0)
  })

  it('never offers ghost text, whatever the match', () => {
    const p = providerOver([SNIP({ id: 'a', title: 'deploy' })])
    const out = p.suggest(ctxFor('deploy', 'deploy', 'command'), new AbortController().signal) as {
      candidates: { eligibleForGhostText: boolean }[]
    }
    // §10.2: the ask form cannot run in a ghost, so a snippet must never
    // be the text that types itself ahead of the caret.
    expect(out.candidates[0].eligibleForGhostText).toBe(false)
  })

  it('carries the snippet id, its source word, and the token it replaces', () => {
    const p = providerOver([SNIP({ id: 'a', title: 'deploy', body: 'x' })])
    const out = p.suggest(ctxFor('git; dep', 'dep', 'command'), new AbortController().signal) as {
      candidates: {
        source: string
        snippetId?: string
        replacement: { from: number; to: number }
        insertText: string
        displayText: string
      }[]
    }
    const c = out.candidates[0]
    expect(c.source).toBe('snippet')
    expect(c.snippetId).toBe('a')
    expect(c.displayText).toBe('deploy')
    // The replacement is the TOKEN, never the whole line: after a `;` or a
    // pipe the earlier command is somebody else's text and a snippet has no
    // business rewriting it.
    expect(c.replacement).toEqual({ from: 5, to: 8 })
    // insertText is the title rather than the body: the body is resolved at
    // ACCEPTANCE (env/ask at fire time), and the ranking's exact-match rung
    // reads this field — a body here would let a snippet claim an exact
    // match on text nobody typed.
    expect(c.insertText).toBe('deploy')
  })

  it('asks the store to load itself when the library has not been read yet', () => {
    const ensureLoaded = vi.fn()
    const p = providerOver([], ensureLoaded)
    ask(p, ctxFor('dep', 'dep', 'command'))
    expect(ensureLoaded).toHaveBeenCalled()
  })

  it('an ask: snippet is offered like any other — §10.2 disables ghost text, not the row', () => {
    const p = providerOver([SNIP({ id: 'a', title: 'ssh-port', body: 'ssh -p {{port}} h' })])
    expect(ask(p, ctxFor('ssh-', 'ssh-', 'command')).candidates).toHaveLength(1)
  })
})

describe('resolution happens at acceptance, not at suggestion (design §8)', () => {
  it('a branch that changed between the query and the acceptance is the one that lands', async () => {
    // The defect this rules out: a candidate carrying resolved insertText.
    // The query happens while the pane is on `main`; by the time the person
    // picks the row they have switched to `hotfix`, and the phrase that
    // reaches the shell must name the branch they are ON.
    const snippet = SNIP({ id: 'a', title: 'push', body: 'git push origin {{env:branch}}' })
    const p = providerOver([snippet])

    const suggested = ask(p, ctxFor('pu', 'pu', 'command')) as unknown as {
      candidates: { insertText: string; snippetId?: string }[]
    }
    const row = suggested.candidates[0]
    // The row carries the title and a reference — no resolved text at all,
    // which is what makes the staleness impossible rather than unlikely.
    expect(row.insertText).toBe('push')
    expect(row.insertText).not.toContain('main')
    expect(row.snippetId).toBe('a')

    let branch = 'main'
    const facts = (): Promise<SessionFacts> =>
      Promise.resolve({ cwd: '/repo', host: '', user: '', branch })
    const inserted: string[] = []
    const insert = (text: string): Promise<SnippetFire> => {
      inserted.push(text)
      return Promise.resolve({ ok: true, where: 'pty' })
    }

    // The person switches branches between the query and the acceptance.
    branch = 'hotfix'

    const outcome = await createSnippetFireAdapter({
      facts,
      activeInsert: () => ({ insertSnippet: insert }),
      clipboard: { writeText: () => Promise.resolve() },
    })({ snippet, answers: new Map(), destination: 'input' })

    expect(outcome.kind).toBe('delivered')
    expect(inserted).toEqual(['git push origin hotfix'])
  })
})
