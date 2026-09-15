/**
 * The path a command block and the composer both show on their prompt line
 * (spec 2026-09-15 §2). ONE derivation — the block header and the composer
 * each carried a copy before this (scrollback/blocks.ts, editor.ts), which is
 * how two surfaces start naming the same directory two ways.
 *
 * `home` is optional and, when it is not known, the path is shown in full —
 * NEVER shortened to a guessed `~`: a session whose home nobody has reported
 * yet is not the same fact as a session that is actually at its home
 * directory, and collapsing the two would say something nobody verified.
 */
export function cwdLabel(cwd: string, home?: string): string {
  const path = cwd.trim().replace(/\/+$/, '')
  if (!path || path === '~') return '~'

  const normHome = home?.trim().replace(/\/+$/, '')
  if (normHome) {
    if (path === normHome) return '~'
    if (path.startsWith(`${normHome}/`)) {
      const rest = path
        .slice(normHome.length + 1)
        .split('/')
        .filter(Boolean)
      return withTail('~', rest)
    }
  }

  // Not under a known home (or none is known at all): the absolute path,
  // never a guessed `~`.
  const segments = path.split('/').filter(Boolean)
  return withTail('', segments)
}

/**
 * `root` + the segments, collapsing anything past four behind a `…/` that
 * keeps only the last three — long enough to still say something, short
 * enough that the prompt line does not run off the end of the row.
 */
function withTail(root: string, segments: string[]): string {
  if (segments.length === 0) return root || '/'
  if (segments.length > 4) return `${root}/…/${segments.slice(-3).join('/')}`
  return `${root}/${segments.join('/')}`
}
