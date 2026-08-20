/**
 * sandbox-path-classes — the ONE renderer statement of the two-class rule.
 *
 * The backend owns this rule (internal/settings.checkSandboxPathConflict) and
 * is the only authority on it. What this module exists to prevent is the
 * renderer saying something ELSE: before nocx-61alt the rule had three
 * implementations — the backend's, `sandboxPeerKey` in the Settings surface,
 * and `otherActive` in the pre-launch dialog — and the two renderer copies
 * compared exact strings in both directions while the backend compared
 * containment in one. So a read-only folder inside a read & write folder
 * passed both renderer checks and was refused by the backend in different
 * words, and a read & write folder inside a read-only one — which ADR-0039
 * exists to permit — was described by the dialog as a conflict.
 *
 * The rule, stated once:
 *
 *   A read-only grant may not be the same directory as, or sit inside, a
 *   read & write grant: the writable grant would subsume it and the
 *   read-only classification would mean nothing.
 *
 *   A read & write grant inside a read-only one is ALLOWED and is the point
 *   of two classes — a writable island in a broader read-only tree.
 *
 * Paths here are what the native picker returned: absolute, and already
 * canonical as far as the renderer can know. The backend re-derives its own
 * answer from canonical paths and remains the decision; this is only what the
 * surface may say before asking.
 */

/** True when `path` is `root` itself or a component-wise descendant of it. */
export function isWithin(root: string, path: string): boolean {
  if (root === path) return true
  if (root === '/') return true
  return path.startsWith(root.endsWith('/') ? root : root + '/')
}

/**
 * The read-only grant `readOnlyPath` cannot stand while one of `writablePaths`
 * equals it or contains it. Returns the offending writable grant, or null.
 */
export function writableSubsuming(
  readOnlyPath: string,
  writablePaths: readonly string[],
): string | null {
  for (const writable of writablePaths) {
    if (isWithin(writable, readOnlyPath)) return writable
  }
  return null
}

/**
 * Whether adding `path` to `target` contradicts the grants already active in
 * the other class, and the sentence to show when it does. `null` means the
 * surface has nothing to object to — which is not the same as the backend
 * accepting it.
 */
export function classConflict(
  target: 'readOnly' | 'readWrite',
  path: string,
  activeReadOnly: readonly string[],
  activeWritable: readonly string[],
): string | null {
  if (target === 'readOnly') {
    const writable = writableSubsuming(path, activeWritable)
    if (writable === null) return null
    return writable === path
      ? `"${path}" is already a read & write folder. Remove it there first to make it read-only.`
      : `"${path}" is inside the read & write folder "${writable}", so it cannot be read-only. Remove that folder first.`
  }
  // Read & write. Only the same directory is a contradiction: a writable
  // folder INSIDE a read-only one is the two-class exception, not an error.
  const clash = activeReadOnly.find((readOnly) => readOnly === path)
  return clash === undefined
    ? null
    : `"${path}" is already a read-only folder. Remove it there first to make it read & write.`
}
