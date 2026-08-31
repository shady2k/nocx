/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.stat.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the files.stat JSON-RPC method: the kind of one path, obtained without enumerating its parent. Providers follow symlinks, so the kind describes the object a path link would open or expand. The result is deliberately minimal because path-link classification does not read size, mode or modification time.
 */
export interface FilesStatResult {
  /**
   * The object kind at the requested path: regular file, directory, or another filesystem object. Symlinks are followed before classification.
   */
  kind: 'regular' | 'dir' | 'other'
}
