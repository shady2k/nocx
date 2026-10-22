/**
 * How a path stands relative to a known home — the ONE `~` decision
 * (nocx-1ebrs.45): the prompt line and the tab title both abbreviate a
 * path with `~`, and two derivations of the same fact are how two surfaces
 * start naming the same directory two ways. `home` is optional and, when
 * it is not known, the answer is `outside` — a session whose home nobody
 * has reported yet is not the same fact as a session that is actually at
 * its home directory, and collapsing the two would say something nobody
 * verified.
 */
export type HomeRelation =
  | { readonly kind: 'home' }
  | { readonly kind: 'under'; readonly rest: readonly string[] }
  | { readonly kind: 'outside'; readonly segments: readonly string[] }

/**
 * The relation of a path to `home` — see HomeRelation. Both sides are
 * normalized here (trim, trailing slashes), so no caller has to know a
 * precondition to get the same answer: `dir/` is `dir`, whichever side of
 * the comparison it sits on.
 */
export function homeRelation(path: string, home?: string): HomeRelation {
  const norm = path.trim().replace(/\/+$/, '')
  const normHome = home?.trim().replace(/\/+$/, '')
  if (normHome) {
    if (norm === normHome) return { kind: 'home' }
    if (norm.startsWith(`${normHome}/`)) {
      return {
        kind: 'under',
        rest: norm
          .slice(normHome.length + 1)
          .split('/')
          .filter(Boolean),
      }
    }
  }
  return { kind: 'outside', segments: norm.split('/').filter(Boolean) }
}

/**
 * The path a command block and the composer both show on their prompt line
 * (spec 2026-09-15 §2). ONE derivation — the block header and the composer
 * each carried a copy before this (scrollback/blocks.ts, editor.ts), and the
 * tab title takes the same `~` decision through `homeRelation`
 * (nocx-1ebrs.45), which is how two surfaces start naming the same
 * directory two ways.
 *
 * `home` is optional and, when it is not known, the path is shown in full —
 * NEVER shortened to a guessed `~` (see `homeRelation`).
 */
export function cwdLabel(cwd: string, home?: string): string {
  const path = cwd.trim().replace(/\/+$/, '')
  if (!path || path === '~') return '~'

  const rel = homeRelation(path, home)
  if (rel.kind === 'home') return '~'
  if (rel.kind === 'under') return withTail('~', rel.rest)

  // Not under a known home (or none is known at all): the absolute path,
  // never a guessed `~`.
  return withTail('', rel.segments)
}

/**
 * `root` + the segments, collapsing anything past four behind a `…/` that
 * keeps only the last three — long enough to still say something, short
 * enough that the prompt line does not run off the end of the row.
 */
function withTail(root: string, segments: readonly string[]): string {
  if (segments.length === 0) return root || '/'
  if (segments.length > 4) return `${root}/…/${segments.slice(-3).join('/')}`
  return `${root}/${segments.join('/')}`
}
