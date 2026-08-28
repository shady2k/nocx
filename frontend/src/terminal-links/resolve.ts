// ═══════════════════════════════════════════════════════════════════════════
// A detected path + the tab's origin → the absolute path to ask the provider
// for, or a NAMED refusal.
//
// Pure and synchronous: no filesystem, no wire. Existence is the provider's
// answer, not this module's guess — a stat here would be a second opinion
// that goes stale between the check and the read.
//
// The refusals are the point. A relative path resolved against a cwd nobody
// verified opens SOME file, just not the one that was printed, and a wrong
// file opening confidently is worse than a message saying why nothing did.
// AD-5's cwdVerified is what separates the two, and this module refuses
// rather than degrading quietly.
// ═══════════════════════════════════════════════════════════════════════════

import type { LinkTarget } from './detect'

/** What resolution needs to know about the tab the link was printed in. */
export interface ResolveOrigin {
  /** The tab's current working directory (OSC 7). */
  readonly cwd: string
  /** Whether that cwd came from the shell rather than from an inference. */
  readonly cwdVerified: boolean
  /** The session's home directory, when the caller has learned it. Absent
   *  means `~/…` cannot be resolved — and is refused, not guessed. */
  readonly home?: string
}

export type Resolution =
  | { ok: true; absolute: string }
  /** `no-cwd`: a relative path with no verified cwd to join it onto.
   *  `no-home`: a `~/…` path and no known home directory. */
  | { ok: false; reason: 'no-cwd' | 'no-home' }

/**
 * Resolve one path target. POSIX semantics throughout: nocx's providers are
 * a POSIX local filesystem and SFTP, and a Windows path is not a shape the
 * grammar emits.
 *
 * `..` is collapsed LEXICALLY, which is not the same as resolving symlinks —
 * `a/link/../b` may name a different file than the kernel would pick. That is
 * deliberate: the provider answers with the file's `canonical` identity, and
 * a renderer that tried to out-guess it would just be wrong earlier.
 */
export function resolvePath(target: LinkTarget, origin: ResolveOrigin): Resolution {
  if (target.kind !== 'path') return { ok: false, reason: 'no-cwd' }
  const raw = target.path

  if (raw.startsWith('/')) return { ok: true, absolute: normalize(raw) }

  if (raw.startsWith('~/')) {
    if (origin.home === undefined || origin.home === '') return { ok: false, reason: 'no-home' }
    return { ok: true, absolute: normalize(`${origin.home}/${raw.slice(2)}`) }
  }

  if (!origin.cwdVerified || origin.cwd === '') return { ok: false, reason: 'no-cwd' }
  return { ok: true, absolute: normalize(`${origin.cwd}/${raw}`) }
}

/** Collapse `//`, `.` and `..` in an absolute POSIX path. `..` at the root
 *  stays at the root rather than escaping upward into nonsense. */
function normalize(path: string): string {
  const out: string[] = []
  for (const seg of path.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      out.pop()
      continue
    }
    out.push(seg)
  }
  return `/${out.join('/')}`
}
