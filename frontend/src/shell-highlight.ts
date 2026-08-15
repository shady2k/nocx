// Read-only shell syntax highlighting (ADR-0010 §Decision 4: the language
// layer arrives as a constructor-passed extension, not as editor hard-coding).
//
// Two surfaces consume the SAME tokenizer, so their token classes agree by
// construction:
//   - the live command editor installs `shellExtensions` (CM6 decorations);
//   - frozen block headers run the static `highlightShellText` pass.
// Both are recoloured by a theme switch because the tok-* classes resolve to
// `--color-*` semantic tokens in style.css (ADR-0013: colour literals live in
// themes/ only).
//
// The tokenizer is the VS Code `shellscript` TextMate grammar run through
// Shiki's pure-JS regex engine (`shiki/engine/javascript`) — no Oniguruma
// WASM, so the packaged app's CSP is never involved. `includeExplanation:
// 'scopeName'` yields the grammar's scope names, not theme colours: the
// palette stays with the `--color-*` tokens. The grammar loads asynchronously
// at module init; until it is ready both surfaces render plain text, and the
// live editor re-decorates as soon as the load completes (first paint is
// never blocked; the prompt never waits).
//
// Scope is deliberately syntactic: the tokenizer never asks whether a command
// is on PATH, whether it is an alias or a function, whether a flag is real,
// or whether a path exists. Those need the session's own shell and are out of
// scope (the brief names them explicitly). `sdf` is a command like any other
// word in command position — no existence claim, no diagnostic.

import { createHighlighterCore } from 'shiki/core'
import type { HighlighterCore } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { StateEffect } from '@codemirror/state'
import type { Extension, Range } from '@codemirror/state'
import { Decoration, EditorView, ViewPlugin } from '@codemirror/view'
import type { DecorationSet, ViewUpdate } from '@codemirror/view'
import type { CommandSnapshotStore } from './command-snapshot'

// ── Grammar loading ─────────────────────────────────────────────────────────

/** The loaded highlighter, or null until the async init below completes. */
let highlighter: HighlighterCore | null = null

/** Callbacks that want to run once the tokenizer exists (frozen-header repaint). */
const readyCallbacks = new Set<() => void>()

const TOKEN_OPTIONS = {
  lang: 'shellscript',
  theme: 'nord',
  includeExplanation: 'scopeName',
} as const

/**
 * Resolves when the tokenizer is ready to colour text. Tests await this
 * before asserting classes; the app never blocks on it (plain text until
 * ready). The grammar module is ~45 KB and the engine is pure JS, so this
 * completes in a few milliseconds at startup.
 */
export const shellHighlightReady: Promise<void> = (async () => {
  const hl = await createHighlighterCore({
    langs: [import('@shikijs/langs/shellscript')],
    themes: [import('@shikijs/themes/nord')],
    engine: createJavaScriptRegexEngine(),
  })
  // TextMate rule compilation is lazy inside Shiki. Complete one real parse
  // before publishing readiness: otherwise the first editor to tokenize under
  // a parallel startup can observe the grammar between construction and its
  // first compiled rule set, rendering plain command words until the next
  // document change (nocx-dze9).
  hl.codeToTokens('true', TOKEN_OPTIONS)
  highlighter = hl
  for (const cb of readyCallbacks) cb()
  readyCallbacks.clear()
})()

/**
 * Run `cb` once the tokenizer is ready (on a microtask if it already is).
 * Used by the frozen-header path to repaint headers that were rendered while
 * the grammar was still loading.
 */
export function onShellHighlightReady(cb: () => void): void {
  if (highlighter !== null) {
    queueMicrotask(cb)
    return
  }
  readyCallbacks.add(cb)
}

// ── Scope-name → tok-* class mapping ────────────────────────────────────────
//
// Matched by longest prefix first, so `string.unquoted.argument` beats
// `string.` and `keyword.operator` beats `keyword.`. The grammar's full scope
// vocabulary is not enumerated here — only the roles the existing `.tok-*`
// classes in style.css express.

const SCOPE_CLASSES: ReadonlyArray<readonly [prefix: string, cls: string]> = [
  ['punctuation.definition.string.heredoc', 'tok-heredoc'],
  ['punctuation.terminator.statement', 'tok-operator'],
  ['punctuation.separator.statement', 'tok-operator'],
  ['punctuation.definition.string', 'tok-string'],
  ['string.unquoted.heredoc', 'tok-heredoc'],
  ['support.function.builtin', 'tok-command'],
  ['string.unquoted.argument', 'tok-path'],
  ['constant.other.option', 'tok-flag'],
  ['entity.name.function', 'tok-command'],
  ['entity.name.command', 'tok-command'],
  ['keyword.operator', 'tok-operator'],
  ['variable.', 'tok-variable'],
  ['constant.', 'tok-atom'],
  ['keyword.', 'tok-keyword'],
  ['comment.', 'tok-comment'],
  ['string.', 'tok-string'],
]

/** Class a command word gets when the snapshot says it does not exist. */
const UNRESOLVED_COMMAND_CLASS = 'tok-command tok-unresolved'

/**
 * Characters that make a command-position word not a bare literal name:
 * whitespace (\s — tokens never carry control bytes beyond that: the
 * tokenizer already skips whitespace-only runs), expansion ($, backtick),
 * quoting, separators/redirects/globs, a path (/), a comment/history marker
 * (#, !), tilde. Such a word has no literal name to check in the snapshot —
 * it is indeterminate, never unresolved.
 */
const NON_LITERAL = /[\s$`'"\\;|&<>()*?[\]{}#!~/]/

export type CommandVerdict = 'resolved' | 'unresolved' | 'indeterminate' | 'unavailable'

/**
 * The four-state verdict for a command-position word. Only bare literal
 * names are checked against the snapshot; a word inside a substitution or
 * containing expansion syntax is indeterminate; with no snapshot everything
 * is unavailable (never unresolved — see the section comment).
 */
export function verdictForCommand(
  text: string,
  substituted: boolean,
  store: CommandSnapshotStore,
): CommandVerdict {
  if (substituted) return 'indeterminate'
  if (NON_LITERAL.test(text)) return 'indeterminate'
  if (store.status === 'unavailable') return 'unavailable'
  return store.has(text) ? 'resolved' : 'unresolved'
}
// ── Command-existence verdicts (OSC 636 snapshot) ─────────────────────────
//
// The tokenizer is positional, not existential: `sdf` in command position is
// styled like `ls`, and says nothing about whether it exists (that was the
// honest-but-useless state this feature replaces). Existence is answered by
// the session's own shell via the OSC 636 snapshot (command-snapshot.ts).
// Four states, and the fourth keeps us honest:
//
//   resolved      snapshot applies, name found        → command style (now)
//   unresolved    snapshot applies, name NOT found    → + subtle underline
//   indeterminate expansion/substitution — no literal → command style,
//                name to check                          no verdict
//   unavailable   no hook, no snapshot                → command style,
//                                                       no verdict
//
// `unavailable` must never collapse into `unresolved`: on a host without our
// integration everything would be underlined, which is the same lie as
// calling `sdfsdf` green — pointing the other way.
//
// A command-position word is only checkable when it is a bare literal name.
// Anything that expands or resolves elsewhere — $VAR, $( ), backticks, a
// path (./x resolves against the cwd the snapshot knows nothing about),
// quotes — has no name to check and is indeterminate. A word INSIDE a
// substitution (the `pick` of "$(pick)") is likewise indeterminate: the
// grammar still marks it as a command, but its scope stack carries the
// substitution, which is the tell.
interface ShellToken {
  from: number
  to: number
  cls: string
  /** True when the token sits inside $( ), backticks or an interpolation. */
  substituted: boolean
}

/** The one tokenizer. Synchronous once the grammar is loaded (measured ~0.23 ms
 * per realistic command line); returns [] while the grammar is still loading.
 *
 * A token's explanation carries the grammar's scope stack for that token,
 * outermost first, possibly across several nested rules. The innermost scope
 * is the most specific role, so scopes are walked inside-out; the first scope
 * that names a role we style wins. Adjacent tokens that map to the same class
 * (e.g. `"` + content + `"` of a quoted string) are merged into one span so
 * the live line and the frozen header render identically.
 */
function tokenizeShell(text: string, store: CommandSnapshotStore): ShellToken[] {
  const hl = highlighter
  if (!hl || text.length === 0) return []
  const { tokens } = hl.codeToTokens(text, TOKEN_OPTIONS)
  const out: ShellToken[] = []
  for (const lineTokens of tokens) {
    for (const t of lineTokens) {
      if (t.content.length === 0 || /^\s+$/.test(t.content)) continue
      let cls: string | null = null
      let substituted = false
      const explanation = t.explanation ?? []
      // Substitution detection runs over the WHOLE scope stack (unbounded):
      // the class lookup below exits early at the innermost role match, which
      // would miss the meta.scope.subshell marker sitting further out.
      for (let k = explanation.length - 1; k >= 0 && !substituted; k--) {
        const scopes = explanation[k].scopes
        for (let j = scopes.length - 1; j >= 0 && !substituted; j--) {
          const scopeName = scopes[j].scopeName
          if (
            scopeName.startsWith('meta.scope.subshell') ||
            scopeName.startsWith('string.interpolated')
          ) {
            substituted = true
          }
        }
      }
      for (let k = explanation.length - 1; k >= 0 && cls === null; k--) {
        const scopes = explanation[k].scopes
        for (let j = scopes.length - 1; j >= 0 && cls === null; j--) {
          const scopeName = scopes[j].scopeName
          for (const [prefix, candidate] of SCOPE_CLASSES) {
            if (scopeName.startsWith(prefix)) {
              cls = candidate
              break
            }
          }
        }
      }
      if (cls === null) continue
      if (
        cls === 'tok-command' &&
        verdictForCommand(t.content, substituted, store) === 'unresolved'
      ) {
        cls = UNRESOLVED_COMMAND_CLASS
      }
      // Shiki 4.x reports offsets absolute in the document, not per line.
      const from = t.offset
      const to = from + t.content.length
      const prev = out[out.length - 1]
      if (prev && prev.to === from && prev.cls === cls) {
        prev.to = to
      } else {
        out.push({ from, to, cls, substituted })
      }
    }
  }
  return out
}

// ── Live editor: CM6 decorations from the tokens ────────────────────────────

/** Forces every live surface to re-tokenize (fired once the grammar loads). */
const refreshEffect = StateEffect.define<null>()

/** One mark decoration per class string (incl. the combined unresolved one). */
const MARK_CLASSES = [
  ...new Set([...SCOPE_CLASSES.map(([, cls]) => cls), UNRESOLVED_COMMAND_CLASS]),
]
const MARKS: Record<string, Decoration> = Object.fromEntries(
  MARK_CLASSES.map((cls) => [cls, Decoration.mark({ class: cls })]),
)

function computeDecorations(text: string, store: CommandSnapshotStore): DecorationSet {
  const ranges: Array<Range<Decoration>> = []
  for (const { from, to, cls } of tokenizeShell(text, store)) {
    ranges.push(MARKS[cls].range(from, to))
  }
  return Decoration.set(ranges, true)
}

class ShellHighlight {
  decorations: DecorationSet
  /** False once the owning view is destroyed; gates the async re-decoration. */
  private alive = true
  private unsubscribeSnapshot: () => void
  private store: CommandSnapshotStore

  constructor(view: EditorView, store: CommandSnapshotStore) {
    this.store = store
    this.decorations = computeDecorations(view.state.doc.toString(), store)
    // A snapshot arriving mid-typing changes verdicts for the WHOLE line, so
    // it re-decorates like the grammar-ready path does — same refreshEffect.
    // The subscription is to THIS tab's store: another tab's snapshot must
    // not re-decorate this editor.
    this.unsubscribeSnapshot = store.subscribe(() => {
      if (this.alive) view.dispatch({ effects: refreshEffect.of(null) })
    })
    if (highlighter === null) {
      void shellHighlightReady.then(() => {
        if (this.alive) view.dispatch({ effects: refreshEffect.of(null) })
      })
    }
  }

  destroy() {
    this.alive = false
    this.unsubscribeSnapshot()
  }

  update(update: ViewUpdate) {
    if (
      update.docChanged ||
      update.transactions.some((tr) => tr.effects.some((e) => e.is(refreshEffect)))
    ) {
      this.decorations = computeDecorations(update.state.doc.toString(), this.store)
    } else {
      this.decorations = this.decorations.map(update.changes)
    }
  }
}

/** Install in a CommandEditor to colour the live command line. Takes the
 *  tab's snapshot store so verdicts and snapshot subscriptions never cross
 *  tabs — each editor re-decorates only on its own tab's snapshot. */
export function shellExtensions(store: CommandSnapshotStore): Extension[] {
  const plugin = ViewPlugin.fromClass(
    class extends ShellHighlight {
      constructor(view: EditorView) {
        super(view, store)
      }
    },
    { decorations: (v) => v.decorations },
  )
  return [plugin]
}

// ── Frozen headers: the same tokens as HTML ─────────────────────────────────

const ESCAPE: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;' }

/** Escape text for an HTML text node (enough for our own span building). */
function escapeHtml(text: string): string {
  return text.replace(/[&<>]/g, (ch) => ESCAPE[ch])
}

/**
 * Static pass for frozen block headers: tokenize `text` with the same
 * tokenizer the live editor uses, and return HTML where every token range is
 * wrapped in its class. The text itself is always escaped, so the result is
 * safe to assign to innerHTML. While the grammar is still loading this is the
 * plain escaped text — identical to what the live editor shows pre-ready.
 */
export function highlightShellText(text: string, store: CommandSnapshotStore): string {
  const tokens = tokenizeShell(text, store)
  if (tokens.length === 0) return escapeHtml(text)
  let html = ''
  let pos = 0
  for (const { from, to, cls } of tokens) {
    html += escapeHtml(text.slice(pos, from))
    html += `<span class="${cls}">${escapeHtml(text.slice(from, to))}</span>`
    pos = to
  }
  html += escapeHtml(text.slice(pos))
  return html
}
