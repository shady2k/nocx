// Where a Download item belongs, as one predicate.
//
// ## The question is not "is this tab remote"
//
// It is the same question `upload-eligibility.ts` asks, turned around:
// **can the person reach these bytes any other way?** A download exists to
// put a file where the person can open it, and there is exactly one
// combination where it already is.
//
//   Wails window + local tab    NO. The file is on the machine the window
//                               is running on. `Show in Finder` is the
//                               action for it, and it is already in the
//                               menu on exactly this combination.
//   Wails window + ssh tab      YES. The file is on the far host.
//   browser + local tab         YES, and this is the row that is easy to
//                               get wrong. The tab's shell is on the
//                               BACKEND's machine and the person is
//                               looking at a browser somewhere else, so
//                               "it is already on your disk" is false —
//                               `Show in Finder` would open a window on a
//                               machine nobody is sitting at.
//   browser + ssh tab           YES. The file is two machines away.
//
// So the rule is `uploadMovesTheFile`'s rule with the same two inputs and
// the same single `false`, and the temptation is to call that function and
// be done. It is a DIFFERENT question that currently has the same answer:
// upload asks whether the gesture moves a file the person named, download
// asks whether the bytes are already reachable. They coincide today because
// both turn on "is the tab's machine the person's machine", and they would
// stop coinciding the moment either direction grew a case the other did not
// — a read-only remote, a local tab the desktop build cannot reveal. One
// predicate serving two questions is the defect AGENTS.md's "two surfaces
// may never own the same input" describes, arriving as a shared helper
// rather than as a duplicate.
//
// Absence is how the `false` is expressed, never a disabled row — the same
// rule the panel already follows for `Show in Finder` and for `Upload…`. A
// greyed-out Download on the one combination where the file is already on
// the disk would be a promise the product cannot keep.

/** The two facts the rule turns on. `native` is asked of the one owner of
 *  "are we inside the webview" (wails-runtime.ts) and injected so a test can
 *  drive both halves; `kind` is the active tab's origin — the machine its
 *  shell is on. */
export interface DownloadContext {
  native: boolean
  kind: 'local' | 'ssh'
}

/**
 * Are the bytes of a file on this tab's machine out of the person's reach
 * without a transfer?
 *
 * True for three of the four combinations above; false only inside the
 * Wails window on a local tab, where the file is already on the disk the
 * window is running from. Callers express the `false` as absence.
 */
export function downloadReachesTheBytes(ctx: DownloadContext): boolean {
  return !(ctx.native && ctx.kind === 'local')
}
