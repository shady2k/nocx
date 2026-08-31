# Snippet parameters and conditions — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A snippet body can offer a choice from a list and can keep or drop whole regions of its own text, both answered in one form at fire time.

**Architecture:** One parser (`frontend/src/snippets/parse.ts`) reads the body once and returns fields, blocks, value spans and diagnostics. Preview, the ask form and the fire adapter are its only consumers and derive nothing themselves. Resolution stays exactly where it is — in the renderer, at fire time, one pass (snippets design §8). The backend, the wire and the contracts do not change.

**Tech Stack:** TypeScript, SolidJS, Vitest, Playwright. Go only for the two seed bodies in `internal/snippet/service.go`.

**Spec:** `.internal/specs/2026-08-30-snippet-parameters-and-conditions-design.md`. Read §4 (grammar), §5 (one name one role), §6 (the parse result) and §7 (resolution order) before Task 1.

## Global Constraints

- **`{{secret:…}}` is never consumed, rewritten or emptied.** Its offsets come from
  `frontend/src/secret-reference.ts` (`findReferences`) and nowhere else — the parser
  calls it, never re-derives it. `secret-reference.ts`, `secret-chip.ts` and `submit.ts`
  must appear in **no commit of this plan**.
- **One parser.** After Task 5, `frontend/src/snippets/preview.ts`, `resolve.ts`,
  `snippet-ask-dialog.tsx` and `snippets-settings.tsx` contain no brace literal and no
  regular expression over a body.
- **Answers are inserted literally** and never re-parsed (spec §7 step 6).
- **Resolution order is load-bearing** (spec §7): parse → visible fields → missing answers
  → cut blocks → check `env` → substitute. `env` is checked **after** cutting.
- **No new component in `frontend/src/ui/`.** `Select`, `Checkbox` and `TextField` already
  exist; a surface may place a kit component and may never repaint it (AGENTS.md).
- **TDD**, red before green, one commit per task at minimum.
- Run `cd frontend && npx vitest run src/snippets` for this plan's unit tests. Do **not**
  run `make ci-full`, the containerized jobs or the e2e suite from a task — that is the
  integrator's, once, on the merged tree (AGENTS.md, "Git authority").

## File Structure

| File                                              | Responsibility                                                                                          |
| ------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `frontend/src/snippets/parse.ts`                  | **new.** The only reader of the grammar. Returns `SnippetParse`.                                        |
| `frontend/src/snippets/parse.test.ts`             | **new.** Tests for the above, including the namespace-disjointness test moved from `reference.test.ts`. |
| `frontend/src/snippets/reference.ts`              | **deleted** in Task 1 — `parse.ts` replaces it.                                                         |
| `frontend/src/snippets/reference.test.ts`         | **deleted** in Task 1; its disjointness test moves.                                                     |
| `frontend/src/snippets/reference-namespaces.ts`   | loses `ask`, gains the "no colon = parameter" rule in prose.                                            |
| `frontend/src/snippets/resolve.ts`                | resolution on top of the parse; owns `visibleFields`, block cutting, substitution.                      |
| `frontend/src/snippets/preview.ts`                | renders the parse into `PreviewPart[]`. Decides nothing.                                                |
| `frontend/src/snippets/snippets-settings.tsx`     | one sentence per part; new part kinds.                                                                  |
| `frontend/src/snippets/snippet-ask-dialog.tsx`    | `Select`, `Checkbox`, reactive visibility.                                                              |
| `frontend/src/snippets/fire.ts`                   | one new refusal reason.                                                                                 |
| `frontend/src/snippets/snippets-quick-connect.ts` | its sentence; `askFields` → `needsForm`.                                                                |
| `frontend/src/main.tsx:1477`                      | `askFields` → `needsForm`.                                                                              |
| `internal/snippet/service.go`                     | the two seed bodies, rewritten.                                                                         |
| `e2e/snippets.spec.ts`                            | the end-to-end check.                                                                                   |

---

### Task 1: The parser — value spans

**Files:**

- Create: `frontend/src/snippets/parse.ts`
- Create: `frontend/src/snippets/parse.test.ts`
- Delete: `frontend/src/snippets/reference.ts`, `frontend/src/snippets/reference.test.ts`
- Modify: `frontend/src/snippets/reference-namespaces.ts`
- Modify: `frontend/src/snippets/preview.ts`, `frontend/src/snippets/resolve.ts` (imports only — keep them compiling)

**Interfaces:**

- Consumes: `findReferences` from `../secret-reference` (unchanged, read-only).
- Produces: the types below. Every later task depends on these names exactly.

```ts
export type SpanKind = 'param' | 'env' | 'secret' | 'unrecognised'

export interface ValueSpan {
  readonly from: number
  readonly to: number
  readonly kind: SpanKind
  /** param: the raw declaration ("w=a|b"). env: the key. secret: the vault
   *  name, from findReferences. unrecognised: the quoted text. */
  readonly arg: string
}
```

**Acceptance Criteria:**

- `{{name}}`, `{{name=d}}` and `{{name=a|b}}` are `param` spans; `{{env:cwd}}` is `env`; `{{secret:k}}` is `secret` with the vault's name; `{{evn:cwd}}` and `{{ask:port}}` are `unrecognised`.
- A malformed `{{ask:port}` (one brace) is `unrecognised`, quoted to the brace, as today.
- `REFERENCE_NAMESPACES` has exactly the keys `secret` and `env`.

- [ ] **Step 1: Write the failing test**

Create `frontend/src/snippets/parse.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { REFERENCE_NAMESPACES } from './reference-namespaces'
import { parse } from './parse'

describe('parse — value spans', () => {
  it('a bare name with no colon is a parameter', () => {
    expect(parse('run {{worker}}').spans).toEqual([
      { from: 4, to: 14, kind: 'param', arg: 'worker' },
    ])
  })

  it('a declaration keeps its default and its option list intact', () => {
    expect(parse('{{w=claude|omp}} {{p=8080}}').spans).toEqual([
      { from: 0, to: 16, kind: 'param', arg: 'w=claude|omp' },
      { from: 17, to: 27, kind: 'param', arg: 'p=8080' },
    ])
  })

  it('a registered namespace is its own kind, and the vault owns its name', () => {
    expect(parse('cd {{env:cwd}} && psql {{secret:prod-db}}').spans).toEqual([
      { from: 3, to: 14, kind: 'env', arg: 'cwd' },
      { from: 23, to: 41, kind: 'secret', arg: 'prod-db' },
    ])
  })

  it('a colon with an unregistered namespace stays literal — ask: included', () => {
    expect(parse('{{evn:cwd}} {{ask:port}}').spans).toEqual([
      { from: 0, to: 11, kind: 'unrecognised', arg: '{{evn:cwd}}' },
      { from: 12, to: 24, kind: 'unrecognised', arg: '{{ask:port}}' },
    ])
  })

  it('one closing brace is not a span, and is quoted to that brace', () => {
    expect(parse('curl :{{ask:port}').spans).toEqual([
      { from: 6, to: 17, kind: 'unrecognised', arg: '{{ask:port}' },
    ])
  })

  it('the registry lists only the two owners left', () => {
    expect(Object.keys(REFERENCE_NAMESPACES).sort()).toEqual(['env', 'secret'])
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd frontend && npx vitest run src/snippets/parse.test.ts`
Expected: FAIL — `Failed to resolve import "./parse"`.

- [ ] **Step 3: Write the parser's value half**

Create `frontend/src/snippets/parse.ts`:

```ts
// The one reader of the snippet grammar. Preview, the ask form and the fire
// adapter are its consumers and derive NOTHING themselves — the legend an
// author reads in Settings and the refusal a fire produces come from this
// one computation, so they cannot disagree (design §6, AD-8).
//
// The vault's namespace is not parsed here. `findReferences` owns "this is a
// reference to a secret" and is called for it, because a second derivation
// of that predicate is the failure the snippets spec §7 was written against.
import { findReferences } from '../secret-reference'

export type SpanKind = 'param' | 'env' | 'secret' | 'unrecognised'

export interface ValueSpan {
  readonly from: number
  readonly to: number
  readonly kind: SpanKind
  readonly arg: string
}

/** Every `{{…}}` whose content carries no `}`. Deliberately open: the
 *  classification below decides what it is, and an opening that matches
 *  nothing here is reported as unrecognised by `unrecognisedFrom`. */
const VALUE_RE = /\{\{([^}]*)\}\}/g

/** The namespaces a colon may commit a span to. Anything else stays
 *  literal — which is what keeps `{{ask:port}}` visible after the `ask`
 *  namespace was retired. */
const OWNED = new Set(['env', 'secret'])

const OPENING = '{{'

/** How much of an unrecognised opening to quote back: to the first `}}`,
 *  else to the first `}`, else to the end of the line — never across lines,
 *  because a runaway quote would be the rest of the body. */
function unrecognisedText(body: string, at: number): string {
  const nl = body.indexOf('\n', at)
  const line = body.slice(at, nl >= 0 ? nl : body.length)
  const both = line.indexOf('}}')
  if (both >= 0) return line.slice(0, both + 2)
  const one = line.indexOf('}')
  if (one >= 0) return line.slice(0, one + 1)
  return line
}

export function valueSpans(body: string): ValueSpan[] {
  const secrets = new Map(findReferences(body).map((r) => [r.from, r]))
  const recognised = new Map<number, ValueSpan>()
  for (const m of body.matchAll(VALUE_RE)) {
    const from = m.index
    const to = from + m[0].length
    const secret = secrets.get(from)
    if (secret !== undefined) {
      recognised.set(from, { from, to, kind: 'secret', arg: secret.name })
      continue
    }
    const content = m[1]
    const colon = content.indexOf(':')
    if (colon < 0) {
      recognised.set(from, { from, to, kind: 'param', arg: content })
      continue
    }
    const ns = content.slice(0, colon)
    if (ns === 'env') {
      recognised.set(from, { from, to, kind: 'env', arg: content.slice(colon + 1) })
      continue
    }
    if (!OWNED.has(ns)) {
      recognised.set(from, { from, to, kind: 'unrecognised', arg: m[0] })
    }
  }

  const out: ValueSpan[] = []
  for (let at = body.indexOf(OPENING); at >= 0; at = body.indexOf(OPENING, at + 1)) {
    const hit = recognised.get(at)
    if (hit !== undefined) {
      out.push(hit)
      // Skip past it: a `{{` inside a span belongs to that span.
      at += hit.to - hit.from - 1
      continue
    }
    const text = unrecognisedText(body, at)
    out.push({ from: at, to: at + text.length, kind: 'unrecognised', arg: text })
  }
  return out
}

export interface SnippetParse {
  readonly spans: readonly ValueSpan[]
}

export function parse(body: string): SnippetParse {
  return { spans: valueSpans(body) }
}
```

- [ ] **Step 4: Run it and watch it pass**

Run: `cd frontend && npx vitest run src/snippets/parse.test.ts`
Expected: PASS, 6 tests.

- [ ] **Step 5: Retire `reference.ts` and shrink the registry**

Delete `frontend/src/snippets/reference.ts` and `frontend/src/snippets/reference.test.ts`.

Replace the body of `frontend/src/snippets/reference-namespaces.ts` with:

```ts
// The namespace registry — one declaration of who may claim a `{{ns:arg}}`
// namespace, so a third feature cannot claim one twice.
//
// A colon is what commits a span to this registry. A `{{…}}` WITHOUT a colon
// is a parameter and belongs to no namespace at all — which is why `ask` is
// gone: a question is now written `{{port=8080}}`, and the token shapes are
// separated by a character rather than by a lookup that could disagree
// (parameters-and-conditions design §3).
export const REFERENCE_NAMESPACES = {
  secret: 'vault (secret-reference.ts / vault.resolveLine)',
  env: 'snippets (resolved at fire time)',
} as const

export type ReferenceNamespace = keyof typeof REFERENCE_NAMESPACES
```

In `preview.ts` and `resolve.ts`, replace `import { findSnippetSpans } from './reference'`
with `import { valueSpans } from './parse'` and change each `findSnippetSpans(` call to
`valueSpans(`, mapping `span.ns === 'env'` to `span.kind === 'env'` and `span.ns === 'ask'`
to `span.kind === 'param'`. These two files are rewritten properly in Tasks 4 and 5; here
they only have to compile and keep their existing tests green **except** the two that
assert the retired behaviour — delete `preview.test.ts`'s "a bare {{cwd}} and a misspelt
namespace are unrecognised" case and let Task 5 write its replacement.

- [ ] **Step 6: Run the suite**

Run: `cd frontend && npx vitest run src/snippets && npx tsc --noEmit -p tsconfig.json`
Expected: PASS. Any failure here is a call site you have not updated.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/snippets frontend/src/main.tsx
git commit -m "refactor(frontend): one parser reads the snippet grammar, and a colon decides who owns a span (<task-bead-id>)"
```

---

### Task 2: The parser — blocks and structural diagnostics

**Files:**

- Modify: `frontend/src/snippets/parse.ts`
- Modify: `frontend/src/snippets/parse.test.ts`

**Interfaces:**

- Consumes: Task 1's `valueSpans`, `SnippetParse`.
- Produces:

```ts
export interface Block {
  readonly openFrom: number
  readonly openTo: number
  readonly closeFrom: number
  readonly closeTo: number
  readonly name: string
  readonly negated: boolean
}

export interface Escape {
  readonly from: number
  readonly to: number
}

export type DiagnosticKind =
  | 'unclosed-block'
  | 'stray-endif'
  | 'nested-block'
  | 'unterminated-tag'
  | 'unknown-tag'
  | 'condition-on-parameter'
  | 'conflicting-declaration'

export interface Diagnostic {
  readonly from: number
  readonly to: number
  readonly kind: DiagnosticKind
  readonly detail: string
}
```

`SnippetParse` grows `blocks`, `escapes` and `diagnostics`.

**Acceptance Criteria:**

- `{% if x %}…{% endif %}` and `{% if not x %}…{% endif %}` produce one `Block` each.
- `{%%` produces an `Escape` and is not a tag opening.
- Each of `unclosed-block`, `stray-endif`, `nested-block`, `unterminated-tag`, `unknown-tag` is produced by the body that earns it, with offsets pointing at the offending tag.

- [ ] **Step 1: Write the failing tests**

Append to `frontend/src/snippets/parse.test.ts`:

```ts
describe('parse — blocks', () => {
  it('an if/endif pair is one block, with its offsets', () => {
    const p = parse('a{% if fast %}b{% endif %}c')
    expect(p.blocks).toEqual([
      { openFrom: 1, openTo: 14, closeFrom: 15, closeTo: 26, name: 'fast', negated: false },
    ])
    expect(p.diagnostics).toEqual([])
  })

  it('"not" negates, and is not part of the name', () => {
    expect(parse('{% if not fast %}x{% endif %}').blocks[0]).toMatchObject({
      name: 'fast',
      negated: true,
    })
  })

  it('two sibling blocks are two blocks', () => {
    expect(parse('{% if a %}1{% endif %}{% if b %}2{% endif %}').blocks).toHaveLength(2)
  })

  it('{%% is an escape and never opens a tag', () => {
    const p = parse('write {%% if x %} literally')
    expect(p.escapes).toEqual([{ from: 6, to: 9 }])
    expect(p.blocks).toEqual([])
    expect(p.diagnostics).toEqual([])
  })
})

describe('parse — structural diagnostics', () => {
  const kinds = (body: string): string[] => parse(body).diagnostics.map((d) => d.kind)

  it('an unclosed block is reported at its opening', () => {
    expect(kinds('{% if x %}body')).toEqual(['unclosed-block'])
    expect(parse('{% if x %}body').diagnostics[0]).toMatchObject({ from: 0, to: 10 })
  })

  it('an endif with nothing open is reported', () => {
    expect(kinds('body{% endif %}')).toEqual(['stray-endif'])
  })

  it('a nested block is reported and is not supported', () => {
    expect(kinds('{% if a %}{% if b %}x{% endif %}{% endif %}')).toContain('nested-block')
  })

  it('a tag with no closing %} is reported', () => {
    expect(kinds('{% if x')).toEqual(['unterminated-tag'])
  })

  it('a tag that is neither if nor endif is reported', () => {
    expect(kinds('{% for x %}{% endif %}')).toContain('unknown-tag')
  })
})
```

- [ ] **Step 2: Run and watch them fail**

Run: `cd frontend && npx vitest run src/snippets/parse.test.ts`
Expected: FAIL — `p.blocks` is undefined.

- [ ] **Step 3: Implement the tag scan**

Add to `frontend/src/snippets/parse.ts`:

```ts
export interface Block {
  readonly openFrom: number
  readonly openTo: number
  readonly closeFrom: number
  readonly closeTo: number
  readonly name: string
  readonly negated: boolean
}

export interface Escape {
  readonly from: number
  readonly to: number
}

export type DiagnosticKind =
  | 'unclosed-block'
  | 'stray-endif'
  | 'nested-block'
  | 'unterminated-tag'
  | 'unknown-tag'
  | 'condition-on-parameter'
  | 'conflicting-declaration'

export interface Diagnostic {
  readonly from: number
  readonly to: number
  readonly kind: DiagnosticKind
  readonly detail: string
}

const TAG_OPEN = '{%'
const TAG_CLOSE = '%}'
/** The escape: `{%%` is a literal `{%`. ONE escaping mechanism, applied to
 *  the delimiter that gained a meaning — `|` inside a default is knowingly
 *  not expressible (design §4.1). */
const ESCAPE = '{%%'

interface OpenTag {
  readonly from: number
  readonly to: number
  readonly name: string
  readonly negated: boolean
}

interface TagScan {
  readonly blocks: Block[]
  readonly escapes: Escape[]
  readonly diagnostics: Diagnostic[]
}

function scanTags(body: string): TagScan {
  const blocks: Block[] = []
  const escapes: Escape[] = []
  const diagnostics: Diagnostic[] = []
  let open: OpenTag | null = null

  let at = body.indexOf(TAG_OPEN)
  while (at >= 0) {
    if (body.startsWith(ESCAPE, at)) {
      escapes.push({ from: at, to: at + ESCAPE.length })
      at = body.indexOf(TAG_OPEN, at + ESCAPE.length)
      continue
    }
    const tagFrom = at
    const close = body.indexOf(TAG_CLOSE, tagFrom + TAG_OPEN.length)
    if (close < 0) {
      diagnostics.push({
        from: tagFrom,
        to: body.length,
        kind: 'unterminated-tag',
        detail: 'this tag has no closing %}',
      })
      break
    }
    const tagTo = close + TAG_CLOSE.length
    const words = body
      .slice(tagFrom + TAG_OPEN.length, close)
      .trim()
      .split(/\s+/)
    at = body.indexOf(TAG_OPEN, tagTo)

    if (words.length === 1 && words[0] === 'endif') {
      if (open === null) {
        diagnostics.push({
          from: tagFrom,
          to: tagTo,
          kind: 'stray-endif',
          detail: 'there is no {% if %} open here',
        })
        continue
      }
      blocks.push({
        openFrom: open.from,
        openTo: open.to,
        closeFrom: tagFrom,
        closeTo: tagTo,
        name: open.name,
        negated: open.negated,
      })
      open = null
      continue
    }

    const isIf =
      words[0] === 'if' && (words.length === 2 || (words.length === 3 && words[1] === 'not'))
    if (isIf) {
      if (open !== null) {
        diagnostics.push({
          from: tagFrom,
          to: tagTo,
          kind: 'nested-block',
          detail: 'a condition inside another condition is not supported',
        })
        continue
      }
      open = {
        from: tagFrom,
        to: tagTo,
        name: words[words.length - 1],
        negated: words.length === 3,
      }
      continue
    }

    diagnostics.push({
      from: tagFrom,
      to: tagTo,
      kind: 'unknown-tag',
      detail: 'only {% if %}, {% if not %} and {% endif %} exist',
    })
  }

  if (open !== null) {
    diagnostics.push({
      from: open.from,
      to: open.to,
      kind: 'unclosed-block',
      detail: 'this condition has no {% endif %}',
    })
  }
  return { blocks, escapes, diagnostics }
}
```

**Note on the loop.** `at` advances via `body.indexOf(TAG_OPEN, …)` at exactly two points —
after an escape, and after a well-formed tag's `%}` — so a `{%` inside a tag's own text can
never be re-entered, and `tagFrom` is captured before `at` moves. The tests in Step 1 assert
exact offsets and will catch any slip.

Then widen the result:

```ts
export interface SnippetParse {
  readonly spans: readonly ValueSpan[]
  readonly blocks: readonly Block[]
  readonly escapes: readonly Escape[]
  readonly diagnostics: readonly Diagnostic[]
}

export function parse(body: string): SnippetParse {
  const tags = scanTags(body)
  return {
    spans: valueSpans(body),
    blocks: tags.blocks,
    escapes: tags.escapes,
    diagnostics: tags.diagnostics,
  }
}
```

- [ ] **Step 4: Run and watch them pass**

Run: `cd frontend && npx vitest run src/snippets/parse.test.ts`
Expected: PASS, all block and diagnostic cases.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/snippets/parse.ts frontend/src/snippets/parse.test.ts
git commit -m "feat(frontend): a snippet body carries condition blocks, and says how they are malformed (<task-bead-id>)"
```

---

### Task 3: The parser — fields and semantic diagnostics

**Files:**

- Modify: `frontend/src/snippets/parse.ts`, `frontend/src/snippets/parse.test.ts`

**Interfaces:**

- Consumes: Task 1's `ValueSpan`, Task 2's `Block`, `Diagnostic`.
- Produces:

```ts
export type FieldKind = 'text' | 'select' | 'flag'
export interface ConditionRef {
  readonly name: string
  readonly negated: boolean
}
export interface Field {
  readonly name: string
  readonly kind: FieldKind
  readonly defaultValue: string // '' for a flag and for a bare parameter
  readonly options: readonly string[] // [] unless kind === 'select'
  readonly inside: ConditionRef | null // the block this field lives in
}
/** `w=a|b` → name w, options [a,b], default a. `p=8080` → default 8080.
 *  `p` → a USE, not a declaration. Only the FIRST `=` separates. */
export function splitDeclaration(arg: string): {
  name: string
  defaultValue: string
  options: string[]
  declared: boolean
}
```

`SnippetParse` grows `fields`.

**Acceptance Criteria:**

- Fields come back in first-occurrence order across parameters **and** condition names.
- A name used only in `{% if %}` has `kind: 'flag'`; a name with `|` has `kind: 'select'` and its first option as `defaultValue`; anything else is `'text'`.
- A field written inside a block carries that block as `inside`.
- `{% if worker %}` where `worker` is also substituted → `condition-on-parameter`.
- `{{w=claude}}` … `{{w=a|b}}` → `conflicting-declaration`. `{{w=a|b}}` … `{{w}}` → **no** diagnostic: a bare use is not a declaration.

- [ ] **Step 1: Write the failing tests**

Append to `frontend/src/snippets/parse.test.ts`:

```ts
describe('parse — fields', () => {
  it('a bare name is a text field with no default', () => {
    expect(parse('{{who}}').fields).toEqual([
      { name: 'who', kind: 'text', defaultValue: '', options: [], inside: null },
    ])
  })

  it('an option list is a select whose first option is the default', () => {
    expect(parse('{{w=claude|omp|codex}}').fields).toEqual([
      {
        name: 'w',
        kind: 'select',
        defaultValue: 'claude',
        options: ['claude', 'omp', 'codex'],
        inside: null,
      },
    ])
  })

  it('only the first = separates, so a default may contain one', () => {
    expect(parse('{{q=a=b}}').fields[0]).toMatchObject({ defaultValue: 'a=b', options: [] })
  })

  it('a name only ever named by a condition is a flag', () => {
    expect(parse('{% if fast %}go{% endif %}').fields).toEqual([
      { name: 'fast', kind: 'flag', defaultValue: '', options: [], inside: null },
    ])
  })

  it('a field inside a block carries the block it lives in', () => {
    expect(parse('{% if fast %}{{n=3}}{% endif %}').fields).toEqual([
      { name: 'fast', kind: 'flag', defaultValue: '', options: [], inside: null },
      {
        name: 'n',
        kind: 'text',
        defaultValue: '3',
        options: [],
        inside: { name: 'fast', negated: false },
      },
    ])
  })

  it('one entry per name, in first-occurrence order', () => {
    expect(parse('{{b}} {{a=1}} {{a}}').fields.map((f) => f.name)).toEqual(['b', 'a'])
  })

  it('a repeated use is not a redeclaration', () => {
    expect(parse('{{w=a|b}} again {{w}}').diagnostics).toEqual([])
  })
})

describe('parse — semantic diagnostics', () => {
  const kinds = (body: string): string[] => parse(body).diagnostics.map((d) => d.kind)

  it('a condition on a substituted name is refused, not read as always-true', () => {
    expect(kinds('{{worker}} {% if worker %}x{% endif %}')).toEqual(['condition-on-parameter'])
  })

  it('two declarations that disagree are refused', () => {
    expect(kinds('{{w=claude}} {{w=a|b}}')).toEqual(['conflicting-declaration'])
  })
})
```

- [ ] **Step 2: Run and watch them fail**

Run: `cd frontend && npx vitest run src/snippets/parse.test.ts`
Expected: FAIL — `p.fields` is undefined.

- [ ] **Step 3: Implement field derivation**

Add to `frontend/src/snippets/parse.ts`:

```ts
export type FieldKind = 'text' | 'select' | 'flag'

export interface ConditionRef {
  readonly name: string
  readonly negated: boolean
}

export interface Field {
  readonly name: string
  readonly kind: FieldKind
  readonly defaultValue: string
  readonly options: readonly string[]
  readonly inside: ConditionRef | null
}

/** A `|` anywhere in the value makes the declaration an option list, and the
 *  FIRST option is the default. A default containing a literal `|` is
 *  therefore not expressible — named in design §4.1 as a limitation, not
 *  solved, because a second escaping mechanism costs more than the case. */
export function splitDeclaration(arg: string): {
  name: string
  defaultValue: string
  options: string[]
  declared: boolean
} {
  const at = arg.indexOf('=')
  if (at < 0) return { name: arg, defaultValue: '', options: [], declared: false }
  const name = arg.slice(0, at)
  const rest = arg.slice(at + 1)
  if (!rest.includes('|')) return { name, defaultValue: rest, options: [], declared: true }
  const options = rest.split('|')
  return { name, defaultValue: options[0], options, declared: true }
}

function blockAt(blocks: readonly Block[], offset: number): ConditionRef | null {
  for (const b of blocks) {
    if (offset > b.openTo && offset < b.closeFrom) return { name: b.name, negated: b.negated }
  }
  return null
}

function deriveFields(
  body: string,
  spans: readonly ValueSpan[],
  blocks: readonly Block[],
): { fields: Field[]; diagnostics: Diagnostic[] } {
  const diagnostics: Diagnostic[] = []
  const byName = new Map<string, Field>()
  const order: string[] = []
  const declaredAt = new Map<string, ValueSpan>()

  // Parameters first pass, in body order, interleaved with the block
  // openings below by a final sort on first offset.
  const firstAt = new Map<string, number>()

  for (const span of spans) {
    if (span.kind !== 'param') continue
    const d = splitDeclaration(span.arg)
    const existing = byName.get(d.name)
    if (existing === undefined) {
      byName.set(d.name, {
        name: d.name,
        kind: d.options.length > 0 ? 'select' : 'text',
        defaultValue: d.defaultValue,
        options: d.options,
        inside: blockAt(blocks, span.from),
      })
      order.push(d.name)
      firstAt.set(d.name, span.from)
      if (d.declared) declaredAt.set(d.name, span)
      continue
    }
    if (!d.declared) continue
    const prior = declaredAt.get(d.name)
    if (prior === undefined) {
      // The first mention was a bare use; this declaration supplies the
      // shape, and the field keeps the earlier position.
      byName.set(d.name, {
        ...existing,
        kind: d.options.length > 0 ? 'select' : 'text',
        defaultValue: d.defaultValue,
        options: d.options,
      })
      declaredAt.set(d.name, span)
      continue
    }
    if (
      existing.defaultValue !== d.defaultValue ||
      existing.options.join('|') !== d.options.join('|')
    ) {
      diagnostics.push({
        from: span.from,
        to: span.to,
        kind: 'conflicting-declaration',
        detail: `"${d.name}" is declared twice, with different answers on offer`,
      })
    }
  }

  for (const b of blocks) {
    const asParam = byName.get(b.name)
    if (asParam !== undefined && asParam.kind !== 'flag') {
      diagnostics.push({
        from: b.openFrom,
        to: b.openTo,
        kind: 'condition-on-parameter',
        detail: `"${b.name}" is filled into the text, so it cannot also be a condition`,
      })
      continue
    }
    if (asParam !== undefined) continue
    byName.set(b.name, {
      name: b.name,
      kind: 'flag',
      defaultValue: '',
      options: [],
      inside: null,
    })
    order.push(b.name)
    firstAt.set(b.name, b.openFrom)
  }

  const fields = order
    .map((n) => byName.get(n))
    .filter((f): f is Field => f !== undefined)
    .sort((a, z) => (firstAt.get(a.name) ?? 0) - (firstAt.get(z.name) ?? 0))
  return { fields, diagnostics }
}
```

Widen `parse`:

```ts
export function parse(body: string): SnippetParse {
  const tags = scanTags(body)
  const spans = valueSpans(body)
  const derived = deriveFields(body, spans, tags.blocks)
  return {
    spans,
    blocks: tags.blocks,
    escapes: tags.escapes,
    fields: derived.fields,
    diagnostics: [...tags.diagnostics, ...derived.diagnostics],
  }
}
```

and add `readonly fields: readonly Field[]` to `SnippetParse`.

- [ ] **Step 4: Run and watch them pass**

Run: `cd frontend && npx vitest run src/snippets/parse.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/snippets/parse.ts frontend/src/snippets/parse.test.ts
git commit -m "feat(frontend): a snippet's fields and their kinds are derived from how the body uses them (<task-bead-id>)"
```

---

### Task 4: Resolution on the parse

**Files:**

- Modify: `frontend/src/snippets/resolve.ts`, `frontend/src/snippets/resolve.test.ts`

**Interfaces:**

- Consumes: `parse`, `Field`, `Diagnostic` from `./parse`.
- Produces:

```ts
export const FLAG_ON = 'on'
export function visibleFields(parsed: SnippetParse, answers: ReadonlyMap<string, string>): Field[]
export function needsForm(body: string): boolean
export type ResolveOutcome =
  | { kind: 'resolved'; text: string }
  | { kind: 'needs-fields'; fields: Field[] }
  | { kind: 'refused'; reason: 'env-unavailable'; keys: string[] }
  | { kind: 'refused'; reason: 'malformed'; diagnostics: Diagnostic[] }
export function resolveBody(
  body: string,
  facts: SessionFacts,
  answers: ReadonlyMap<string, string>,
): ResolveOutcome
```

`askFields` and `splitAsk` are **deleted**; `ENV_KEYS`, `EnvKey`, `hasSecretReference` and `SessionFacts` keep their current exports.

**Acceptance Criteria:**

- A body with a diagnostic refuses with `reason: 'malformed'`, before anything else.
- A field inside a switched-off block is neither asked nor substituted.
- `{{env:branch}}` inside a switched-off block does **not** refuse the fire.
- A tag alone on its line takes its whole line, in a kept block **and** a dropped one.
- An answer containing `{{worker}}` or `{%%` is inserted literally.
- `{{secret:k}}` survives byte-for-byte.
- An ANSWER equal to `{{secret:prod}}` stays a live vault reference, and the clipboard
  destination refuses it — the behaviour §9 of the spec pins, tested rather than assumed.

- [ ] **Step 1: Write the failing tests**

Replace `frontend/src/snippets/resolve.test.ts` with a file keeping its existing
`resolveBody` cases (rewritten from `{{ask:port=8080}}` to `{{port=8080}}`) and adding:

```ts
import { describe, expect, it } from 'vitest'
import { FLAG_ON, needsForm, resolveBody, visibleFields, type SessionFacts } from './resolve'
import { parse } from './parse'

const FULL: SessionFacts = { cwd: '/w', host: 'h', user: 'u', branch: 'main' }
const NONE = new Map<string, string>()
const on = (...names: string[]): Map<string, string> => new Map(names.map((n) => [n, FLAG_ON]))

describe('conditions', () => {
  const BODY = 'a\n{% if fast %}\nquick\n{% endif %}\nz'

  it('a switched-on block keeps its text and loses its tag lines', () => {
    expect(resolveBody(BODY, FULL, on('fast'))).toEqual({
      kind: 'resolved',
      text: 'a\nquick\nz',
    })
  })

  it('a switched-off block leaves no blank line behind', () => {
    expect(resolveBody(BODY, FULL, new Map([['fast', '']]))).toEqual({
      kind: 'resolved',
      text: 'a\nz',
    })
  })

  it('"not" inverts it', () => {
    const body = '{% if not fast %}slow{% endif %}'
    expect(resolveBody(body, FULL, new Map([['fast', '']]))).toEqual({
      kind: 'resolved',
      text: 'slow',
    })
    expect(resolveBody(body, FULL, on('fast'))).toEqual({ kind: 'resolved', text: '' })
  })

  it('a tag sharing its line loses only the tag', () => {
    expect(resolveBody('x {% if f %}y{% endif %} z', FULL, on('f'))).toEqual({
      kind: 'resolved',
      text: 'x y z',
    })
  })

  it('a field inside a switched-off block is not asked', () => {
    const body = '{% if f %}{{n=3}}{% endif %}'
    expect(visibleFields(parse(body), new Map([['f', '']])).map((x) => x.name)).toEqual(['f'])
    expect(resolveBody(body, FULL, new Map([['f', '']]))).toEqual({ kind: 'resolved', text: '' })
  })

  it('an env key inside a switched-off block no longer refuses the fire', () => {
    const body = '{% if f %}{{env:branch}}{% endif %}ok'
    const noBranch: SessionFacts = { ...FULL, branch: null }
    expect(resolveBody(body, noBranch, new Map([['f', '']]))).toEqual({
      kind: 'resolved',
      text: 'ok',
    })
    expect(resolveBody(body, noBranch, on('f'))).toEqual({
      kind: 'refused',
      reason: 'env-unavailable',
      keys: ['branch'],
    })
  })

  it('{%% arrives as a literal {%', () => {
    expect(resolveBody('write {%% if x %}', FULL, NONE)).toEqual({
      kind: 'resolved',
      text: 'write {% if x %}',
    })
  })
})

describe('a malformed body refuses before anything else', () => {
  it('names its diagnostics', () => {
    const out = resolveBody('{% if x %}unclosed', FULL, NONE)
    expect(out).toMatchObject({ kind: 'refused', reason: 'malformed' })
    expect(out.kind === 'refused' && out.reason === 'malformed' && out.diagnostics[0].kind).toBe(
      'unclosed-block',
    )
  })
})

describe('an answer that is itself a secret reference (design §9)', () => {
  it('stays a live reference — the destination policy governs it, not this module', () => {
    const out = resolveBody('psql {{db}}', FULL, new Map([['db', '{{secret:prod}}']]))
    expect(out).toEqual({ kind: 'resolved', text: 'psql {{secret:prod}}' })
  })
})

describe('an answer is never re-parsed', () => {
  it('template notation typed into a field arrives as text', () => {
    const answers = new Map([['a', '{{b}} {%% x']])
    expect(resolveBody('{{a}}', FULL, answers)).toEqual({
      kind: 'resolved',
      text: '{{b}} {%% x',
    })
  })
})

describe('needsForm', () => {
  it('is false for a body with nothing to fill in', () => {
    expect(needsForm('git status')).toBe(false)
  })
  it('is true for a parameter and for a flag', () => {
    expect(needsForm('{{p}}')).toBe(true)
    expect(needsForm('{% if f %}x{% endif %}')).toBe(true)
  })
})
```

- [ ] **Step 2: Run and watch them fail**

Run: `cd frontend && npx vitest run src/snippets/resolve.test.ts`
Expected: FAIL — `visibleFields` / `FLAG_ON` / `needsForm` are not exported.

- [ ] **Step 3: Rewrite the resolver**

Replace the body of `frontend/src/snippets/resolve.ts` below its imports with:

```ts
import { findReferences } from '../secret-reference'
import { parse, type Diagnostic, type Field, type SnippetParse } from './parse'
import type { SessionFacts } from './session-facts'

export type { SessionFacts } from './session-facts'

/** A flag's answer. A flag never reaches the text, so its value is a token
 *  and not a substitution — and a name is either a flag or a parameter,
 *  never both (parse reports the overlap), so this can never collide with
 *  something a person typed. */
export const FLAG_ON = 'on'

export type ResolveOutcome =
  | { kind: 'resolved'; text: string }
  | { kind: 'needs-fields'; fields: Field[] }
  | { kind: 'refused'; reason: 'env-unavailable'; keys: string[] }
  | { kind: 'refused'; reason: 'malformed'; diagnostics: Diagnostic[] }

const satisfied = (
  cond: { name: string; negated: boolean },
  answers: ReadonlyMap<string, string>,
): boolean => (answers.get(cond.name) === FLAG_ON) !== cond.negated

/** The fields a person should be looking at right now: a field inside a
 *  block they switched off is not one of them (design §7 step 2). */
export function visibleFields(parsed: SnippetParse, answers: ReadonlyMap<string, string>): Field[] {
  return parsed.fields.filter((f) => f.inside === null || satisfied(f.inside, answers))
}

/** Whether firing this body should open the form at all. Kept here rather
 *  than at each surface so no caller learns the grammar (AD-8). */
export function needsForm(body: string): boolean {
  return parse(body).fields.length > 0
}

export function hasSecretReference(text: string): boolean {
  return findReferences(text).length > 0
}

export const ENV_KEYS = {
  cwd: "the pane's working directory",
  host: "the pane's host",
  user: 'the ssh user (a local shell has none)',
  branch: 'the checked-out git branch',
} as const

export type EnvKey = keyof typeof ENV_KEYS

function envValue(key: string, facts: SessionFacts): string | null {
  return key in ENV_KEYS ? facts[key as EnvKey] : null
}

/** A tag's cut. Alone on its line, the whole line goes — including its
 *  newline — so a body with two conditions does not arrive full of holes.
 *  Sharing its line, only the tag goes. This is Handlebars' standalone-line
 *  rule; Go templates make you write `{{- -}}` by hand and everyone forgets. */
function tagCut(body: string, from: number, to: number): { from: number; to: number } {
  const lineStart = body.lastIndexOf('\n', from - 1) + 1
  const nl = body.indexOf('\n', to)
  const lineEnd = nl < 0 ? body.length : nl
  if (body.slice(lineStart, from).trim() === '' && body.slice(to, lineEnd).trim() === '') {
    return { from: lineStart, to: Math.min(lineEnd + 1, body.length) }
  }
  return { from, to }
}

interface Edit {
  from: number
  to: number
  text: string
}

export function resolveBody(
  body: string,
  facts: SessionFacts,
  answers: ReadonlyMap<string, string>,
): ResolveOutcome {
  const parsed = parse(body)
  // 1. A malformed body refuses before anything else, and refuses HERE as
  //    well as in the preview: a snippet arrives through backup/restore and
  //    may never have been opened in Settings (design §7 step 1).
  if (parsed.diagnostics.length > 0) {
    return { kind: 'refused', reason: 'malformed', diagnostics: [...parsed.diagnostics] }
  }

  // 2-3. Only a visible field is owed an answer.
  const pending = visibleFields(parsed, answers).filter((f) => !answers.has(f.name))
  if (pending.length > 0) return { kind: 'needs-fields', fields: pending }

  // 4. The cuts. A dropped block takes everything from its opening tag's cut
  //    to its closing tag's; a kept one loses only its two tags.
  const edits: Edit[] = []
  const dropped: Array<{ from: number; to: number }> = []
  for (const b of parsed.blocks) {
    const openCut = tagCut(body, b.openFrom, b.openTo)
    const closeCut = tagCut(body, b.closeFrom, b.closeTo)
    if (satisfied({ name: b.name, negated: b.negated }, answers)) {
      edits.push({ ...openCut, text: '' }, { ...closeCut, text: '' })
      continue
    }
    const cut = { from: openCut.from, to: closeCut.to }
    edits.push({ ...cut, text: '' })
    dropped.push(cut)
  }
  const isDropped = (from: number): boolean => dropped.some((d) => from >= d.from && from < d.to)

  // 5. Only NOW are env keys checked, and only the ones that survived the
  //    cut — an unavailable fact inside a switched-off paragraph is not the
  //    fire's problem (design §7 step 5).
  const missing: string[] = []
  const seen = new Set<string>()
  for (const span of parsed.spans) {
    if (span.kind !== 'env' || isDropped(span.from)) continue
    if (envValue(span.arg, facts) === null && !seen.has(span.arg)) {
      seen.add(span.arg)
      missing.push(span.arg)
    }
  }
  if (missing.length > 0) return { kind: 'refused', reason: 'env-unavailable', keys: missing }

  // 6. Substitution and un-escaping join the same edit list, so an ANSWER
  //    containing `{%%` or `{{x}}` is never touched — it is inserted after
  //    every edit position was fixed.
  for (const span of parsed.spans) {
    if (isDropped(span.from)) continue
    if (span.kind === 'env') {
      edits.push({ from: span.from, to: span.to, text: envValue(span.arg, facts) ?? '' })
    } else if (span.kind === 'param') {
      const name = span.arg.split('=', 1)[0]
      edits.push({ from: span.from, to: span.to, text: answers.get(name) ?? '' })
    }
    // 'secret' is left intact — not ours (design §3, snippets §11.1).
    // 'unrecognised' is sent as it is, which is the point of reporting it.
  }
  for (const e of parsed.escapes) {
    if (isDropped(e.from)) continue
    edits.push({ from: e.from, to: e.to, text: '{%' })
  }

  edits.sort((a, z) => z.from - a.from)
  let text = body
  for (const e of edits) text = text.slice(0, e.from) + e.text + text.slice(e.to)
  return { kind: 'resolved', text }
}
```

- [ ] **Step 4: Run and watch them pass**

Run: `cd frontend && npx vitest run src/snippets/resolve.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/snippets/resolve.ts frontend/src/snippets/resolve.test.ts
git commit -m "feat(frontend): a snippet resolves its conditions before it checks its facts (<task-bead-id>)"
```

---

### Task 5: The preview renders the parse

**Files:**

- Modify: `frontend/src/snippets/preview.ts`, `frontend/src/snippets/preview.test.ts`

**Interfaces:**

- Consumes: `parse`, `Field`, `Diagnostic`, `ValueSpan`.
- Produces:

```ts
export type PreviewPart =
  | { kind: 'env'; text: string; key: string; known: boolean }
  | { kind: 'param'; text: string; name: string; defaultValue: string; options: readonly string[] }
  | { kind: 'flag'; text: string; name: string; negated: boolean }
  | { kind: 'secret'; text: string; name: string }
  | { kind: 'unrecognised'; text: string }
  | { kind: 'problem'; text: string; detail: string }
export function describeBody(body: string): PreviewPart[]
```

**Acceptance Criteria:**

- `describeBody` contains no regular expression and no brace literal; every part it emits comes from `parse`.
- A bare `{{cwd}}` now reports as a `param` — `preview.test.ts:36` reverses.
- `{{evn:cwd}}` and `{{ask:port}}` still report as `unrecognised`.
- Every diagnostic kind from Task 2 and Task 3 appears as a `problem` part.

- [ ] **Step 1: Write the failing tests**

In `frontend/src/snippets/preview.test.ts` replace the deleted `{{cwd}}` case and add:

```ts
it('a bare name is a parameter now, not a mistake', () => {
  expect(describeBody('{{cwd}}')).toEqual([
    { kind: 'param', text: '{{cwd}}', name: 'cwd', defaultValue: '', options: [] },
  ])
})

it('a misspelt namespace is still unrecognised, and so is the retired ask:', () => {
  expect(describeBody('{{evn:cwd}} {{ask:port}}')).toEqual([
    { kind: 'unrecognised', text: '{{evn:cwd}}' },
    { kind: 'unrecognised', text: '{{ask:port}}' },
  ])
})

it('an option list reports what it will offer', () => {
  expect(describeBody('{{w=a|b}}')).toEqual([
    { kind: 'param', text: '{{w=a|b}}', name: 'w', defaultValue: 'a', options: ['a', 'b'] },
  ])
})

it('a condition reports as a flag', () => {
  const parts = describeBody('{% if fast %}x{% endif %}')
  expect(parts).toContainEqual({
    kind: 'flag',
    text: '{% if fast %}',
    name: 'fast',
    negated: false,
  })
})

it('a malformed body reports its problem', () => {
  const parts = describeBody('{% if x %}no end')
  expect(parts.some((p) => p.kind === 'problem')).toBe(true)
})
```

- [ ] **Step 2: Run and watch them fail**

Run: `cd frontend && npx vitest run src/snippets/preview.test.ts`
Expected: FAIL — `param` / `flag` / `problem` kinds do not exist.

- [ ] **Step 3: Rewrite `preview.ts` as a pure render**

Replace the file's body (keep and update the header comment — it already says this module
decides nothing, and after this task that is literally true):

```ts
import { parse, splitDeclaration } from './parse'
import { ENV_KEYS } from './resolve'

export type PreviewPart =
  | { kind: 'env'; text: string; key: string; known: boolean }
  | {
      kind: 'param'
      text: string
      name: string
      defaultValue: string
      options: readonly string[]
    }
  | { kind: 'flag'; text: string; name: string; negated: boolean }
  | { kind: 'secret'; text: string; name: string }
  | { kind: 'unrecognised'; text: string }
  | { kind: 'problem'; text: string; detail: string }

export function describeBody(body: string): PreviewPart[] {
  const parsed = parse(body)
  const at = new Map<number, PreviewPart>()

  for (const span of parsed.spans) {
    const text = body.slice(span.from, span.to)
    if (span.kind === 'env') {
      at.set(span.from, { kind: 'env', text, key: span.arg, known: span.arg in ENV_KEYS })
    } else if (span.kind === 'secret') {
      at.set(span.from, { kind: 'secret', text, name: span.arg })
    } else if (span.kind === 'param') {
      const d = splitDeclaration(span.arg)
      at.set(span.from, {
        kind: 'param',
        text,
        name: d.name,
        defaultValue: d.defaultValue,
        options: d.options,
      })
    } else {
      at.set(span.from, { kind: 'unrecognised', text })
    }
  }
  for (const b of parsed.blocks) {
    at.set(b.openFrom, {
      kind: 'flag',
      text: body.slice(b.openFrom, b.openTo),
      name: b.name,
      negated: b.negated,
    })
  }
  // A problem takes the position it is about, replacing whatever the scan
  // made of it: an author reading the line needs the refusal, not the
  // classification that will never be used.
  for (const d of parsed.diagnostics) {
    at.set(d.from, { kind: 'problem', text: body.slice(d.from, d.to), detail: d.detail })
  }

  return [...at.entries()].sort((a, z) => a[0] - z[0]).map(([, part]) => part)
}
```

- [ ] **Step 4: Run and watch them pass**

Run: `cd frontend && npx vitest run src/snippets/preview.test.ts`
Expected: PASS.

- [ ] **Step 5: Prove the "one parser" constraint by reading**

Run: `grep -n "{{\|{%\|RegExp\|matchAll\|/g" frontend/src/snippets/preview.ts`
Expected: no hit outside the header comment. Same for `resolve.ts`. If there is one, it is
a derivation that belongs in `parse.ts`.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/snippets/preview.ts frontend/src/snippets/preview.test.ts
git commit -m "refactor(frontend): the snippet preview renders the parse and derives nothing (<task-bead-id>)"
```

---

### Task 6: The settings page says the new sentences

**Files:**

- Modify: `frontend/src/snippets/snippets-settings.tsx:76-94`
- Test: `frontend/src/snippets/snippets-settings.test.tsx`

**Interfaces:**

- Consumes: Task 5's `PreviewPart` union.
- Produces: no new export. `previewSentence` and `previewRecognised` gain cases.

**Acceptance Criteria:**

- Each new part kind has a sentence; the `switch` is exhaustive with no `default`, so adding a kind later stops the build.
- A `problem` part renders as not-recognised, like `unrecognised` and an unknown env key.
- No new CSS class naming an error concept (`frontend/lint-fixtures/check-error-vocabulary.mjs` — the preview line already has `sn-preview__part[data-recognised]` and it is the affordance to reuse).

- [ ] **Step 1: Write the failing test**

Add to `frontend/src/snippets/snippets-settings.test.tsx`, following the file's existing
render-and-query style:

```tsx
it('the preview line names an option list, a condition and a malformed tag', async () => {
  // …mount the settings page and open the editor as the neighbouring tests do…
  await typeBody('{{w=a|b}}\n{% if fast %}x{% endif %}\n{% if bad %}')
  const line = screen.getByTestId('snippet-preview')
  expect(line.textContent).toContain('you will be asked (one of a, b)')
  expect(line.textContent).toContain('kept only when "fast" is ticked')
  expect(line.textContent).toContain('no {% endif %}')
})
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd frontend && npx vitest run src/snippets/snippets-settings.test.tsx`
Expected: FAIL on the missing sentences.

- [ ] **Step 3: Extend the two functions**

In `frontend/src/snippets/snippets-settings.tsx`:

```tsx
function previewSentence(part: PreviewPart): string {
  switch (part.kind) {
    case 'env':
      return part.known
        ? `${part.text} → ${ENV_KEYS[part.key as EnvKey]}`
        : `${part.text} → not a key nocx can answer; the fire will refuse`
    case 'param':
      if (part.options.length > 0) {
        return `${part.text} → you will be asked (one of ${part.options.join(', ')})`
      }
      return part.defaultValue === ''
        ? `${part.text} → you will be asked`
        : `${part.text} → you will be asked (default ${part.defaultValue})`
    case 'flag':
      return part.negated
        ? `${part.text} → kept only when "${part.name}" is NOT ticked`
        : `${part.text} → kept only when "${part.name}" is ticked`
    case 'secret':
      return `${part.text} → the vault secret "${part.name}"`
    case 'unrecognised':
      return `${part.text} → not recognised; it will be sent as it is`
    case 'problem':
      return `${part.text} → ${part.detail}; the fire will refuse`
  }
}

const previewRecognised = (part: PreviewPart): boolean =>
  part.kind !== 'unrecognised' && part.kind !== 'problem' && !(part.kind === 'env' && !part.known)
```

- [ ] **Step 4: Run and watch it pass**

Run: `cd frontend && npx vitest run src/snippets && npx eslint src/snippets`
Expected: PASS, no warnings.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/snippets/snippets-settings.tsx frontend/src/snippets/snippets-settings.test.tsx
git commit -m "feat(frontend): the snippet preview line names an option list, a condition and a malformed tag (<task-bead-id>)"
```

---

### Task 7: The form offers a choice, a tick, and only the questions that apply

**Files:**

- Modify: `frontend/src/snippets/snippet-ask-dialog.tsx`
- Test: `frontend/src/snippets/snippet-ask-dialog.test.tsx` (create if absent)

**Interfaces:**

- Consumes: `parse`, `visibleFields`, `FLAG_ON`, `Field`.
- Produces: `SnippetAskDialogHandle` unchanged — `ask(snippet)` and `dispose()`. `deps.fire` keeps its signature `(snippet, answers: ReadonlyMap<string,string>) => Promise<string|null>`.

**Acceptance Criteria:**

- A `select` field renders the kit `Select` with its options and its default preselected.
- A `flag` field renders the kit `Checkbox`, un-ticked.
- Ticking a flag reveals the fields inside its block, seeded with their defaults; un-ticking hides them again and keeps what was typed, so re-ticking restores it.
- The submitted map carries `FLAG_ON` for a ticked flag and `''` for an un-ticked one.
- No element in this file sets `background`, `border`, `color`, `font-*`, `padding` or `box-shadow` on a kit component (AGENTS.md: a surface may place, never repaint).

- [ ] **Step 1: Write the failing test**

Create `frontend/src/snippets/snippet-ask-dialog.test.tsx`:

```tsx
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { mountSnippetAskDialog } from './snippet-ask-dialog'

const snippet = (body: string) => ({ id: 'i', title: 'T', body })

describe('the ask form', () => {
  it('offers a select for an option list, with the first option chosen', async () => {
    const fire = vi.fn(async () => null)
    const host = document.createElement('div')
    document.body.append(host)
    const h = mountSnippetAskDialog(host, { fire, onDelivered: () => {} })
    h.ask(snippet('run {{w=claude|omp|codex}}'))
    const select = (await screen.findByLabelText('w')) as HTMLSelectElement
    expect(select.tagName).toBe('SELECT')
    expect(select.value).toBe('claude')
    fireEvent.change(select, { target: { value: 'codex' } })
    fireEvent.click(screen.getByRole('button', { name: 'Insert' }))
    await waitFor(() => expect(fire).toHaveBeenCalled())
    expect(fire.mock.calls[0][1].get('w')).toBe('codex')
    h.dispose()
  })

  it('hides a field inside a block until its flag is ticked', async () => {
    const fire = vi.fn(async () => null)
    const host = document.createElement('div')
    document.body.append(host)
    const h = mountSnippetAskDialog(host, { fire, onDelivered: () => {} })
    h.ask(snippet('{% if fast %}at {{n=3}}{% endif %}'))
    expect(screen.queryByLabelText('n')).toBeNull()
    fireEvent.click(await screen.findByLabelText('fast'))
    expect(await screen.findByLabelText('n')).toBeTruthy()
    h.dispose()
  })
})
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd frontend && npx vitest run src/snippets/snippet-ask-dialog.test.tsx`
Expected: FAIL — the form renders text fields for everything.

- [ ] **Step 3: Rebuild the form on the parse**

In `frontend/src/snippets/snippet-ask-dialog.tsx`, replace the `names`/`values` signal pair
with one answers map and derive the visible fields from it:

```tsx
import { Checkbox } from '../ui/checkbox'
import { Select } from '../ui/select'
import { parse } from './parse'
import { FLAG_ON, visibleFields } from './resolve'

// …inside mountSnippetAskDialog:
const [snippet, setSnippet] = createSignal<Snippet | null>(null)
const [answers, setAnswers] = createSignal<ReadonlyMap<string, string>>(new Map())

const parsed = createMemo(() => {
  const s = snippet()
  return s === null ? null : parse(s.body)
})

/** The questions that apply right now. A field inside a block the person
 *  switched off is not asked — an optional paragraph must not levy its
 *  questions on everybody (design §7 step 2). Seeding happens here rather
 *  than in `ask`, because a field can become visible for the first time
 *  halfway through the form. */
const visible = createMemo(() => {
  const p = parsed()
  if (p === null) return []
  const fields = visibleFields(p, answers())
  const missing = fields.filter((f) => !answers().has(f.name))
  if (missing.length > 0) {
    setAnswers((prev) => {
      const next = new Map(prev)
      // A flag starts un-ticked; a parameter starts on its default.
      for (const f of missing) next.set(f.name, f.kind === 'flag' ? '' : f.defaultValue)
      return next
    })
  }
  return fields
})

const setAnswer = (name: string, value: string): void =>
  setAnswers((prev) => new Map(prev).set(name, value))
```

and render each field by kind:

```tsx
<For each={visible()}>
  {(f) => (
    <Show
      when={f.kind !== 'flag'}
      fallback={
        <Checkbox
          id={`snippet-ask-${f.name}`}
          label={f.name}
          checked={answers().get(f.name) === FLAG_ON}
          onChange={(on) => setAnswer(f.name, on ? FLAG_ON : '')}
        />
      }
    >
      <Show
        when={f.kind === 'select'}
        fallback={
          <TextField
            id={`snippet-ask-${f.name}`}
            label={f.name}
            value={answers().get(f.name) ?? ''}
            onInput={(v) => setAnswer(f.name, v)}
          />
        }
      >
        <Select
          id={`snippet-ask-${f.name}`}
          label={f.name}
          value={answers().get(f.name) ?? f.defaultValue}
          options={f.options.map((o) => ({ value: o, label: o }))}
          onChange={(v) => setAnswer(f.name, v)}
        />
      </Show>
    </Show>
  )}
</For>
```

`submit` sends `answers()` straight through; `ask(s)` becomes:

```tsx
ask(s: Snippet): void {
  if (parse(s.body).fields.length === 0) return
  setAnswers(new Map())
  setError('')
  setSnippet(s)
}
```

**Check `Select`'s and `Checkbox`'s real prop names** in `frontend/src/ui/select.tsx` and
`frontend/src/ui/checkbox.tsx` before writing this — the shapes above are the intent, not a
transcription, and the kit is the authority.

- [ ] **Step 4: Run and watch it pass**

Run: `cd frontend && npx vitest run src/snippets && npx tsc --noEmit -p tsconfig.json`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/snippets/snippet-ask-dialog.tsx frontend/src/snippets/snippet-ask-dialog.test.tsx
git commit -m "feat(frontend): the snippet form offers a choice and a tick, and asks only what applies (<task-bead-id>)"
```

---

### Task 8: The call sites, and a fire that refuses a malformed body

**Files:**

- Modify: `frontend/src/main.tsx:1477`, `frontend/src/snippets/snippets-quick-connect.ts:63-95,174`, `frontend/src/snippets/fire.ts`
- Test: `frontend/src/snippets/fire.test.ts`, `frontend/src/snippets/snippets-quick-connect.test.ts`

**Interfaces:**

- Consumes: `needsForm` (Task 4), `resolveBody`'s new `malformed` outcome.
- Produces: `SnippetRefusalReason` gains `{ kind: 'malformed-body'; details: string[] }`.

**Acceptance Criteria:**

- `askFields` has no callers and no longer exists.
- Firing `{% if x %}unclosed` refuses with a sentence naming the problem, and writes nothing.
- The `switch` in `snippetRefusalMessage` stays exhaustive — the build breaks if a reason is added without a sentence.

- [ ] **Step 1: Write the failing test**

Add to `frontend/src/snippets/fire.test.ts`:

```ts
it('a malformed body refuses and writes nothing', async () => {
  const insert = vi.fn()
  const fire = createSnippetFireAdapter({
    facts: async () => FULL_FACTS,
    activeInsert: () => ({ insertSnippet: insert }),
    clipboard: { writeText: vi.fn() },
  })
  const out = await fire({
    snippet: { id: 'i', title: 'T', body: '{% if x %}no end' },
    answers: new Map(),
    destination: 'input',
  })
  expect(out).toMatchObject({ kind: 'refused', reason: { kind: 'malformed-body' } })
  expect(insert).not.toHaveBeenCalled()
})
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd frontend && npx vitest run src/snippets/fire.test.ts`
Expected: FAIL — the outcome is `no-owner` or a resolved fire.

- [ ] **Step 3: Wire the reason and the sentence**

In `frontend/src/snippets/fire.ts` add to the union and handle it:

```ts
  | { kind: 'malformed-body'; details: string[] }
```

```ts
if (outcome.kind === 'refused' && outcome.reason === 'malformed') {
  return {
    kind: 'refused',
    reason: { kind: 'malformed-body', details: outcome.diagnostics.map((d) => d.detail) },
  }
}
if (outcome.kind === 'refused') {
  return { kind: 'refused', reason: { kind: 'env-unavailable', keys: outcome.keys } }
}
```

In `frontend/src/snippets/snippets-quick-connect.ts` add the sentence:

```ts
    case 'malformed-body':
      return `This snippet cannot be read: ${r.details.join('; ')} — nothing was inserted.`
```

and at line 174 replace `const fields = askFields(snippet.body)` / `if (fields.length === 0)`
with `if (!needsForm(snippet.body))`, importing `needsForm` from `./resolve`.

In `frontend/src/main.tsx:1477` replace `if (askFields(snippet.body).length > 0)` with
`if (needsForm(snippet.body))` and fix the import on line 121.

- [ ] **Step 4: Run and watch it pass**

Run: `cd frontend && npx vitest run && npx tsc --noEmit -p tsconfig.json`
Expected: PASS. Then `grep -rn askFields frontend/src` must print nothing.

- [ ] **Step 5: Commit**

```bash
git add frontend/src
git commit -m "feat(frontend): a snippet nobody can read refuses at the fire, not only in Settings (<task-bead-id>)"
```

---

### Task 9: The seeds teach the syntax that exists

**Files:**

- Modify: `internal/snippet/service.go:44-56`
- Test: `internal/snippet/service_test.go`

**Interfaces:** none — the bodies are data.

**Acceptance Criteria:**

- Neither seed uses `{{ask:…}}`.
- The rule the file's comment already states still holds: a seed may use `{{env:cwd}}` and parameters, and may **not** use an env key that depends on where the pane is pointed (`host`, `user`, `branch`).
- One seed demonstrates an option list and a condition, because that is what this work added and a seed's only job is to teach the syntax.

- [ ] **Step 1: Write the failing test**

Add to `internal/snippet/service_test.go`:

```go
func TestSeedsUseOnlyFactsAnOrdinaryLocalPaneHas(t *testing.T) {
	svc := NewService(newMemStore(), seqIDs())
	got, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, s := range got {
		for _, banned := range []string{"{{ask:", "{{env:host}}", "{{env:user}}", "{{env:branch}}"} {
			if strings.Contains(s.Body, banned) {
				t.Errorf("seed %q uses %s, which does not fire in an ordinary local pane", s.Title, banned)
			}
		}
	}
}
```

(Reuse whatever store and id-generator helpers `service_test.go` already defines; do not
add new ones.)

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/snippet/ -run Seeds -v`
Expected: FAIL — "Forward a port over ssh" uses `{{ask:`.

- [ ] **Step 3: Rewrite the two bodies**

In `internal/snippet/service.go`, keep the comment block above `seeds()` verbatim — its rule
is unchanged — and replace the two bodies:

```go
	return []Snippet{
		{
			ID:    s.newID(),
			Title: "Explain this project",
			Body:  "Explain what the project in {{env:cwd}} does, and how it is laid out.\n{% if deep %}\nGo file by file rather than summarising.\n{% endif %}",
		},
		{
			ID:    s.newID(),
			Title: "Forward a port over ssh",
			Body:  "ssh -{{mode=L|R}} {{local=8080}}:localhost:{{remote=8080}} {{host}}",
		},
	}
```

- [ ] **Step 4: Run and watch it pass**

Run: `go test ./internal/snippet/`
Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add internal/snippet
git commit -m "fix(snippet): the seeds teach the syntax the product has (<task-bead-id>)"
```

---

### Task 10: Preview and fire report the same diagnostic

**Files:**

- Create: `frontend/src/snippets/diagnostics-parity.test.ts`

**Interfaces:** consumes `describeBody` and `resolveBody` only — no production change is expected. If this test fails, the fix belongs in whichever of the two derives something.

**Acceptance Criteria:**

- For one table of malformed bodies covering every `DiagnosticKind`, the preview reports a `problem` part and the fire refuses with `reason: 'malformed'`, and the details match.

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest'
import { describeBody } from './preview'
import { resolveBody, type SessionFacts } from './resolve'

const FACTS: SessionFacts = { cwd: '/w', host: 'h', user: 'u', branch: 'main' }

// One row per DiagnosticKind in parse.ts. Adding a kind without adding a row
// here is the gap this table exists to close.
const MALFORMED = [
  ['unclosed-block', '{% if x %}body'],
  ['stray-endif', 'body{% endif %}'],
  ['nested-block', '{% if a %}{% if b %}x{% endif %}{% endif %}'],
  ['unterminated-tag', '{% if x'],
  ['unknown-tag', '{% for x %}{% endif %}'],
  ['condition-on-parameter', '{{w}}{% if w %}x{% endif %}'],
  ['conflicting-declaration', '{{w=a}} {{w=b|c}}'],
] as const

describe('the legend and the refusal come from one computation', () => {
  it.each(MALFORMED)('%s: preview says problem, fire refuses', (_kind, body) => {
    const problems = describeBody(body).filter((p) => p.kind === 'problem')
    expect(problems.length).toBeGreaterThan(0)

    const out = resolveBody(body, FACTS, new Map())
    expect(out).toMatchObject({ kind: 'refused', reason: 'malformed' })
    if (out.kind !== 'refused' || out.reason !== 'malformed') return
    expect(out.diagnostics.map((d) => d.detail).sort()).toEqual(
      problems.map((p) => (p.kind === 'problem' ? p.detail : '')).sort(),
    )
  })
})
```

- [ ] **Step 2: Run it**

Run: `cd frontend && npx vitest run src/snippets/diagnostics-parity.test.ts`
Expected: PASS. A failure means one of the two surfaces is deriving something — fix the
derivation, never the test.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/snippets/diagnostics-parity.test.ts
git commit -m "test(frontend): the snippet legend and the snippet refusal cannot disagree (<task-bead-id>)"
```

---

### Task 11: The end-to-end check

**Files:**

- Modify: `e2e/snippets.spec.ts`

**Interfaces:** consumes the shipped product through the same seams the existing specs in
this file use — read `e2e/snippets.spec.ts:119-178` first and copy its setup exactly rather
than inventing one.

**Acceptance Criteria:**

- One test watches a person create a snippet carrying an option list and a condition, fire it with the palette chord while a program owns the pane and no command editor exists, pick a worker from the select, leave the flag un-ticked, and see the resolved text arrive with the excluded sentence gone, **no blank line where it was**, and no trailing newline.

- [ ] **Step 1: Write the test**

Add to `e2e/snippets.spec.ts`, inside the existing
`test.describe('a saved snippet reaches a running program')`:

```ts
test('a chosen option and an un-ticked condition reach the program', async ({ page }) => {
  // …the same stand + program-owns-the-pane setup as the test above it…
  await createSnippet(page, {
    title: 'Orchestrator',
    body: 'run {{worker=claude|omp|codex}}\n{% if parallel %}\nin parallel\n{% endif %}\ndone',
  })
  await firePaletteChord(page)
  await page.getByRole('option', { name: 'Orchestrator' }).click()

  await page.getByLabel('worker').selectOption('codex')
  // `parallel` is left un-ticked: a flag starts off (design §5).
  await page.getByRole('button', { name: 'Insert' }).click()

  await expect.poll(() => readProgramStdin(page)).toBe('run codex\ndone')
})
```

- [ ] **Step 2: Run it in the container — CI's environment, and faster**

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/snippets.spec.ts`
Expected: PASS. If it fails on layout rather than on the assertion above, check AGENTS.md
("Its failure set is not CI's") before changing anything.

- [ ] **Step 3: Commit**

```bash
git add e2e/snippets.spec.ts
git commit -m "test(e2e): a snippet's chosen option and its dropped paragraph reach the program (<task-bead-id>)"
```

---

## Ordering

```
1 ──► 2 ──► 3 ──► 4 ──► 7 ──► 8 ──► 11
             └──► 5 ──► 6
             └──────────────► 10  (needs 4 and 5)
9 is independent of everything and may run any time after 1.
```

`bd dep add` edges: 2←1, 3←2, 4←3, 5←3, 6←5, 7←4, 8←7, 10←4, 10←5, 11←8. Task 9 free.
The ready front stays at about three.
