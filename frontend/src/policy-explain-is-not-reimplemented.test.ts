import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

/**
 * THE PRECEDENCE ORDER HAS ONE IMPLEMENTATION AND IT IS THE BACKEND'S
 * EVALUATOR (nocx-8nktm, AD-8).
 *
 * `EvaluateInvocation` crosses three layers in a fixed order: the effect row
 * first, and a refusing row is final BEFORE any rule is read; then the rules,
 * where two guards make a matching rule not count; then the resource layer,
 * which tells an editable row scope from an immutable fence. That order is
 * written down in exactly one place, and `policy.explain` carries the steps it
 * actually took so a page can explain a decision without knowing any of it.
 *
 * WHY THE WORDS ARE BANNED HERE, and not merely discouraged. A second
 * implementation of one concept is not duplication somebody tidies up later —
 * it is a regression with a delay fuse. The two agree everywhere anyone looks
 * and disagree somewhere nobody did. "Am I in an ssh context" had two
 * derivations that agreed on every case anyone tried and disagreed on exactly
 * one: `ssh` with no trailing space, which is the state a user is in when they
 * press Tab instead of the space. The suppressed surface un-suppressed itself
 * at the only moment it mattered and inserted a saved host over the user's
 * choice. A renderer that recomputed "did this rule apply" would fail the same
 * way, and it would fail while EXPLAINING a decision to a person — telling
 * them their rule lost a contest it was never entered in.
 *
 * So: a surface READS an explanation and never BUILDS one.
 *
 * WHAT THIS TEST DOES NOT CLAIM. It cannot see a reimplementation written in
 * plain `if`s over the policy document — there is no distinctive word in
 * `if (row.decision === 'refuse')`, and banning that spelling would fire on a
 * page that merely draws a refusing row. It catches the three named guards,
 * the conflict rule, and any step this side MINTS rather than receives. When
 * it fires, the fix is to ask `policy.explain` — never to rename the thing it
 * caught.
 */

const SRC = resolve(__dirname)

/**
 * The evaluator's own vocabulary, in the shapes only a reimplementation has.
 *
 * Each is a COMPARISON or a named rank rather than a bare mention: a rule
 * editor legitimately holds a `grantedUnder` and a page legitimately shows an
 * `evaluatorVersion`. Deciding with one is what no surface may do.
 */
const REIMPLEMENTED = [
  {
    what: 'the most-restrictive-wins rule among overlapping matching rules',
    pattern: /\brestrictiveRank\b|\bmostRestrictive\b|\bMOST_RESTRICTIVE\b|most[-\s]restrictive/i,
  },
  {
    what: 'the evaluator-version guard that makes a stale loose permit inert',
    pattern: /\b(evaluatorVersion|EVALUATOR_VERSION)\s*(===|!==|==|!=|<=|>=|<|>)/,
  },
  {
    what: 'the GrantedUnder guard that stops a widening permit reaching another effect',
    pattern: /\bgrantedUnder\s*(===|!==|==|!=)/,
  },
  {
    what: 'the rule-staleness predicate itself',
    pattern: /\bneedsConfirmation\b/i,
  },
]

/** The trace-kind vocabulary. Reading one (`step.kind === 'row-refuses'`) is
 *  what a surface is for; MINTING one is claiming to have taken a step the
 *  evaluator did not report. */
const MINTS_A_STEP =
  /\bkind\s*:\s*['"](unparsed|effect-row|row-refuses|disqualified|rule-matched|rule-stale|rule-other-effect|resource-inside|resource-outside-fence|resource-outside-row-scope|resource-not-reached)['"]/

/** Whether a line is a comment. A rule about SHIPPED CODE may not fire on the
 *  prose explaining the rule — this file's own header would be the first false
 *  positive, and `policy-client.ts`'s doc comment for `explain()` the second. */
function isComment(line: string): boolean {
  const t = line.trim()
  return t.startsWith('//') || t.startsWith('*') || t.startsWith('/*')
}

/**
 * Every hand-written renderer module.
 *
 * `generated/` is excluded deliberately: those files are a transcript of the
 * contract's own prose, regenerated from `contracts/` and checked by
 * `npm run contracts:check`. The schema is allowed to DESCRIBE the order —
 * describing it is the whole point of an explanation — and no renderer decides
 * anything with a doc comment.
 */
function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      if (entry === 'generated' || entry === 'test-support') continue
      out.push(...sourceFiles(path))
      continue
    }
    if (!/\.(ts|tsx)$/.test(entry)) continue
    if (/\.(test|spec)\.(ts|tsx)$/.test(entry)) continue
    out.push(path)
  }
  return out
}

interface Hit {
  file: string
  line: number
  what: string
  text: string
}

function scan(): Hit[] {
  const hits: Hit[] = []
  for (const path of sourceFiles(SRC)) {
    const lines = readFileSync(path, 'utf8').split('\n')
    lines.forEach((text, i) => {
      if (isComment(text)) return
      for (const { what, pattern } of REIMPLEMENTED) {
        if (pattern.test(text)) {
          hits.push({ file: relative(SRC, path), line: i + 1, what, text: text.trim() })
        }
      }
      if (MINTS_A_STEP.test(text)) {
        hits.push({
          file: relative(SRC, path),
          line: i + 1,
          what: 'a trace step minted here rather than received from policy.explain',
          text: text.trim(),
        })
      }
    })
  }
  return hits
}

function report(hits: Hit[]): string {
  return [
    'The renderer has started deciding what only the backend evaluator may decide:',
    ...hits.map((h) => `  ${h.file}:${h.line} — ${h.what}\n    ${h.text}`),
    '',
    'The precedence order — the effect row first, a refusing row final BEFORE any',
    'rule is read, then the two rule guards, then the fence and the row scopes —',
    'has ONE implementation: content.EvaluateInvocation. It records the steps it',
    'actually took, and policy.explain carries them, so a page can say why a',
    'decision came out the way it did without owning the order that produced it.',
    '',
    'A second implementation here would not disagree where anyone looks. It would',
    'agree on every case anyone tried and disagree on the one nobody did — and it',
    'would do so while explaining a decision to a person, telling them their rule',
    'lost a contest it was never entered in.',
    '',
    'Ask policy.explain (PolicyClient.explain) and render the steps it returns.',
    'Renaming the thing this caught is not the fix.',
  ].join('\n')
}

describe('the precedence order is not reimplemented in the renderer (AD-8)', () => {
  it('finds no shipped module deciding it, and none minting a trace step', () => {
    const hits = scan()
    expect(hits, hits.length === 0 ? '' : report(hits)).toEqual([])
  })

  it('is looking at the modules that could hold one', () => {
    // A grep that greps nothing passes forever. This is the guard on the
    // guard: the walk must reach the policy surface itself.
    const files = sourceFiles(SRC).map((p) => relative(SRC, p))
    expect(files).toContain('policy-client.ts')
    expect(files.length).toBeGreaterThan(50)
  })
})
