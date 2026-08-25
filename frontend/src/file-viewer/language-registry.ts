// ═══════════════════════════════════════════════════════════════════════════
// Language registry for the read-only file viewer (fm-w7).
//
// A SMALL set of formats that actually turn up in terminal work — JSON, YAML,
// Markdown, shell, Go, TypeScript/JavaScript, Python — with plain text as the
// correct fallback. The deliberate ceiling: termic ships the full
// @codemirror/lang-* set; the bundle cost of that is reported in the brief's
// acceptance numbers and this registry stops far below it. A format that is
// not here renders as plain text, which is always honest.
//
// Highlighting reuses the repo's one token vocabulary: every tag maps to a
// `tok-*` class whose colours live in style.css as --color-* tokens
// (ADR-0013), so a theme switch recolours the viewer like every other
// surface. No colour literal exists in this module or in file-viewer.css.
// ═══════════════════════════════════════════════════════════════════════════

import { HighlightStyle, StreamLanguage, syntaxHighlighting } from '@codemirror/language'
import { go } from '@codemirror/lang-go'
import { javascript } from '@codemirror/lang-javascript'
import { json, jsonParseLinter } from '@codemirror/lang-json'
import { linter, lintGutter } from '@codemirror/lint'
import { markdown } from '@codemirror/lang-markdown'
import { python } from '@codemirror/lang-python'
import { yaml } from '@codemirror/lang-yaml'
import { shell } from '@codemirror/legacy-modes/mode/shell'
import type { Extension } from '@codemirror/state'
import { lineNumbers } from '@codemirror/view'
import { tags } from '@lezer/highlight'

// ── Highlighting: CM6 tags → the repo's tok-* vocabulary ───────────────────

/**
 * Maps the lezer tag vocabulary to the tok-* classes style.css already
 * colours. The mapping is deliberately coarse — the tok-* set is the shell
 * vocabulary and this viewer must not invent colour roles (ADR-0013); what
 * matters is that comment/keyword/string/etc. read consistently with the rest
 * of the app and follow the theme switch.
 */
const viewerHighlight = HighlightStyle.define([
  {
    tag: [tags.comment, tags.lineComment, tags.blockComment, tags.docComment],
    class: 'tok-comment',
  },
  {
    tag: [tags.keyword, tags.moduleKeyword, tags.controlKeyword, tags.operatorKeyword],
    class: 'tok-keyword',
  },
  {
    tag: [tags.string, tags.special(tags.string), tags.character, tags.escape],
    class: 'tok-string',
  },
  {
    tag: [tags.variableName, tags.special(tags.variableName), tags.propertyName],
    class: 'tok-variable',
  },
  {
    tag: [
      tags.operator,
      tags.compareOperator,
      tags.arithmeticOperator,
      tags.logicOperator,
      tags.bitwiseOperator,
      tags.definitionOperator,
      tags.updateOperator,
    ],
    class: 'tok-operator',
  },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], class: 'tok-atom' },
  { tag: [tags.meta, tags.processingInstruction], class: 'tok-meta' },
  // Function call sites read like the shell's command words — one vocabulary
  // for "something that runs", and the class is already mapped.
  {
    tag: [tags.function(tags.variableName), tags.function(tags.propertyName)],
    class: 'tok-command',
  },
])

/** The highlighting extension every language in this registry shares. */
export const viewerHighlighting = syntaxHighlighting(viewerHighlight)

// ── Language selection ──────────────────────────────────────────────────────

const LAST_SEGMENT = /([^/\\]+)$/

/** Basename of a provider path, whatever the platform's separator is. */
export function basenameOf(path: string): string {
  const m = LAST_SEGMENT.exec(path)
  return m ? m[1] : path
}

/** Lowercased final extension (".json", ".tsx"), or '' when there is none. */
export function extensionOf(path: string): string {
  const base = basenameOf(path)
  const dot = base.lastIndexOf('.')
  // A leading dot (".bashrc", ".gitignore") is a hidden file, not an extension.
  if (dot <= 0) return ''
  return base.slice(dot).toLowerCase()
}

/** Markdown by NAME rather than by path — the snippet body is authored as
 *  prose and is not a file, so it has no extension to look up (design
 *  §10.4). Same registry, because "which language module a surface gets" is
 *  one question with one owner. */
export function markdownLanguage(): Extension {
  return markdown()
}

/** JSON by NAME, WITH ITS PARSER'S OPINION: the language module plus the
 *  linter that reports where the document stops being JSON. They are one
 *  export because they are one answer — a surface that declares "this is
 *  JSON" and then does not say where it is broken has declared nothing a
 *  person can act on. `@codemirror/lang-json` ships the linter; nothing here
 *  writes a second parser.
 *
 *  The gutter comes with it: a squiggle says WHERE, and the marker in the
 *  gutter is what makes it visible when the error scrolled off. */
export function jsonEditing(): Extension {
  return [json(), lineNumbers(), linter(jsonParseLinter()), lintGutter()]
}

/** The CM6 language extension for a file path, or [] for plain text. */
export function languageForPath(path: string): Extension {
  switch (extensionOf(path)) {
    case '.json':
      return json()
    case '.yaml':
    case '.yml':
      return yaml()
    case '.md':
    case '.markdown':
      return markdown()
    case '.sh':
    case '.bash':
    case '.zsh':
      // Shell has no @codemirror/lang-* package; the legacy stream mode is the
      // standard home and StreamLanguage adapts it to the tag system.
      return StreamLanguage.define(shell)
    case '.go':
      return go()
    case '.ts':
      return javascript({ typescript: true })
    case '.tsx':
      return javascript({ typescript: true, jsx: true })
    case '.js':
    case '.mjs':
    case '.cjs':
      return javascript()
    case '.jsx':
      return javascript({ jsx: true })
    case '.py':
      return python()
    default:
      // Plain text: no language extension, no highlighting. The correct
      // fallback, not a gap — a config file or log the set does not know is
      // rendered exactly as it is.
      return []
  }
}
