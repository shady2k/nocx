// ═══════════════════════════════════════════════════════════════════════════
// The link grammar — ONE owner for both surfaces that show terminal text.
//
// The live xterm grid and the frozen DOM scrollback are two renderings of the
// same bytes, so they must not carry two answers to "is this a link". The
// repo has already paid for a second derivation of one predicate once (the
// two `is this an ssh context` answers that agreed everywhere except the one
// keystroke that mattered — AGENTS.md), and this predicate runs over every
// line of every command's output, where a disagreement is invisible until it
// is not. Both surfaces ask here.
//
// Pure text in, spans out. No DOM, no cwd, no filesystem: this module says
// WHAT a run of characters looks like, never whether it exists or what
// happens when you click it. Resolution against a cwd is `resolve.ts`, the
// action is `activate.ts`, and keeping them apart is what lets the grammar be
// tested exhaustively without a backend.
//
// The expensive half of this file is the REJECTIONS. A false positive does
// not merely fail to open — it puts a dead underline under ordinary prose,
// on every line of every command, forever. `v0.3.0`, `12/20`, `--no-verify`,
// `e.g.` and `user@host:~/x` all appear in this repo's own output and all
// look like paths to a careless scanner. Each has a test.
// ═══════════════════════════════════════════════════════════════════════════

/** What a span resolves to. A url is openable as it stands; a path still
 *  needs a cwd and a filesystem, which this module deliberately lacks. */
export type LinkTarget =
  { kind: 'url'; url: string } | { kind: 'path'; path: string; line?: number; col?: number }

/** One detected link. `from`/`to` are UTF-16 code-unit offsets into the text
 *  handed to detectLinks — the same units the DOM and CM6 count in. */
export interface LinkSpan {
  from: number
  to: number
  target: LinkTarget
}

// ── URLs ──────────────────────────────────────────────────────────────────

// Only the two schemes the product can actually honour. `shell.openUrl`
// refuses anything that is not http(s) before it reaches the browser, so
// matching `ftp:` or `javascript:` here would mint a link whose only possible
// outcome is a toast saying no — a link that cannot work must not be drawn.
const URL_RE = /\bhttps?:\/\/[^\s<>"'`]+/g

// ── Paths ─────────────────────────────────────────────────────────────────

// The character set a path token may be built from. Space is excluded, so a
// path containing one is not detected — a real limitation, chosen because the
// alternative (guessing where an unquoted path ends) mis-detects far more
// often than it helps. `:` is IN the set so the `:line:col` suffix arrives
// attached; everything downstream then has to earn it.
const PATH_TOKEN_RE = /[A-Za-z0-9._~/+@%$:-]+/g

// Trailing characters that belong to the sentence rather than to the path.
// Leading ones are never stripped: `./x`, `../x` and `~/x` all start with a
// character this list contains.
const TRAILING_PUNCT = '.,:;!?'

// `path:12:5` and `path:12`. Lazy on the head, because a greedy head reads
// `src/main.ts:12:5` as line 5 of `src/main.ts:12` — it matches, it is
// plausible, and it is wrong.
const LINE_COL_RE = /^(.+?):(\d+):(\d+)$/
const LINE_RE = /^(.+?):(\d+)$/

// A bare filename — no directory at all — is a path only when a line suffix
// nominates it (`AGENTS.md:84`). The extension must START with a letter,
// which is the whole reason `v0.3.0:1` is not a file called `v0.3.0`.
const BARE_FILE_RE = /^[\w.-]+\.[A-Za-z][A-Za-z0-9]{0,7}$/

// Digits and separators only: `12/20`, `1/2`, `192.168.1.1`. Contains a
// slash and still is not a path.
const NUMERIC_ONLY_RE = /^[\d/.]+$/

/**
 * Every link in one line of terminal text, in first-occurrence order and
 * never overlapping: URLs are matched first and a path candidate that falls
 * inside one is dropped, so `https://x/a/b.ts:12` is one url and not also a
 * path called `b.ts`.
 */
export function detectLinks(text: string): LinkSpan[] {
  if (text.length === 0) return []
  const spans: LinkSpan[] = []
  const urlRanges: Array<[number, number]> = []

  for (const m of text.matchAll(URL_RE)) {
    const raw = m[0]
    const url = trimTrailing(raw)
    if (url.length === 0) continue
    const from = m.index
    const to = from + url.length
    urlRanges.push([from, to])
    spans.push({ from, to, target: { kind: 'url', url } })
  }

  for (const m of text.matchAll(PATH_TOKEN_RE)) {
    const from = m.index
    const to = from + m[0].length
    if (urlRanges.some(([a, b]) => from < b && to > a)) continue
    const token = trimTrailing(m[0])
    if (token.length === 0) continue
    const target = parsePath(token)
    if (target === null) continue
    spans.push({ from, to: from + token.length, target })
  }

  return spans.sort((a, b) => a.from - b.from)
}

/** Drop sentence punctuation and brackets the prose owns, not the link.
 *  A closing bracket is only the prose's when the match does not open one —
 *  `https://en.wikipedia.org/wiki/Foo_(bar)` keeps its paren. */
function trimTrailing(raw: string): string {
  let out = raw
  for (;;) {
    const last = out[out.length - 1]
    if (last === undefined) return out
    if (TRAILING_PUNCT.includes(last)) {
      out = out.slice(0, -1)
      continue
    }
    const open = CLOSERS[last]
    if (open !== undefined && count(out, last) > count(out, open)) {
      out = out.slice(0, -1)
      continue
    }
    return out
  }
}

const CLOSERS: Record<string, string | undefined> = { ')': '(', ']': '[', '}': '{', '>': '<' }

function count(s: string, ch: string): number {
  let n = 0
  for (const c of s) if (c === ch) n++
  return n
}

/**
 * A punctuation-trimmed token → a path target, or null when the token is
 * some other thing that happens to be spelled out of the same characters.
 */
function parsePath(token: string): LinkTarget | null {
  let path = token
  let line: number | undefined
  let col: number | undefined

  const withCol = LINE_COL_RE.exec(token)
  if (withCol !== null) {
    path = withCol[1]
    line = Number(withCol[2])
    col = Number(withCol[3])
  } else {
    const withLine = LINE_RE.exec(token)
    if (withLine !== null) {
      path = withLine[1]
      line = Number(withLine[2])
    }
  }

  // A colon that survived the suffix strip means the token names something
  // that is not a path on this filesystem — `user@host:~/notes.md` is an ssh
  // destination, and opening it as a local file would be a lie.
  if (path.includes(':')) return null
  // A leading dash is a flag (`--no-verify`), never a path. `./-x` still
  // works: the prefix check below is what a dash-leading FILE would need.
  if (path.startsWith('-')) return null
  if (path.length === 0) return null

  const rooted =
    path.startsWith('/') || path.startsWith('~/') || path.startsWith('./') || path.startsWith('../')
  const nested = path.includes('/') && !NUMERIC_ONLY_RE.test(path)
  const namedByLine = line !== undefined && BARE_FILE_RE.test(path)
  if (!rooted && !nested && !namedByLine) return null

  const target: LinkTarget = { kind: 'path', path }
  if (line !== undefined) target.line = line
  if (col !== undefined) target.col = col
  return target
}
