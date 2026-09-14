/**
 * The short form of a working directory that a command block and the composer
 * both show: the last two path segments, `~` for home, and `~` for a directory
 * the shell never reported. ONE derivation — the block header and the composer
 * each carried a copy (scrollback/blocks.ts, editor.ts), which is how two
 * surfaces start naming the same directory two ways.
 */
export function cwdLabel(cwd: string): string {
  const path = cwd.trim().replace(/\/+$/, '') || '~'
  const parts = path.split('/').filter(Boolean)
  if (path === '~' || parts.length === 0) return path
  return parts.slice(-2).join('/')
}
