// D9, as one predicate — the rule that decides whether a file the person
// named is a file nocx can move.
//
// ## Why it is here and not written twice
//
// Two surfaces ask it, and they used to answer it differently. The drop
// handler asked D9 ("is there a path to insert?") and the Files panel's two
// menus asked about the MACHINE ("is this tab remote?"), which came from
// D9's first wording — a local tab never copies. That wording was reasoned
// from the desktop build, where the renderer and the tab's shell are
// provably one machine, and it is simply false in a browser: a local tab is
// a shell on the BACKEND's machine and the dropped file is on the
// BROWSER's. The drop was corrected (nocx-9le.5.22) and the menus were not,
// so a browser drop on a local tab uploaded while the menu one keystroke
// away said the capability did not exist. Two owners of one question,
// agreeing everywhere anybody looked and disagreeing on the case a person
// actually met.
//
// ## The rule
//
// D9 reads: whoever has the PATH inserts it, whoever has only the BYTES
// uploads them. So the question underneath both surfaces is not about the
// machine at all — it is "does this gesture have a file to MOVE?" — and
// exactly one combination answers no:
//
//   Wails window + local tab   The runtime hands Go an absolute path and
//                              the native picker mints a ticket for a file
//                              already on that machine. There is nowhere to
//                              move it to; the drop inserts the path and
//                              the menu offers nothing.
//   Wails window + ssh tab     A ticket names a file on the backend's
//                              machine and the tab's shell is elsewhere.
//   browser + local tab        A `File` is bytes with no location, on the
//                              BROWSER's machine; the tab's shell is on the
//                              backend's. R1 is satisfied — the backend's
//                              own machine is the host that tab is on.
//   browser + ssh tab          Bytes here, a shell over there.
//
// Absence, never a disabled row, is how the one `false` is expressed in a
// menu: the capability does not apply to that machine, and a greyed-out row
// would be a promise the product cannot keep.

/** The two facts the rule turns on, and nothing else. `native` is asked of
 *  the one owner of "are we inside the webview" (wails-runtime.ts) and
 *  injected here so a test can drive both halves; `kind` is the active
 *  tab's origin — the machine its shell is on. */
export interface UploadContext {
  native: boolean
  kind: 'local' | 'ssh'
}

/**
 * Does an upload started in this environment, for a tab on this machine,
 * actually move a file somewhere it is not?
 *
 * True for three of the four combinations above; false only inside the
 * Wails window on a local tab, where the file is already where it would be
 * sent. Callers express the `false` as absence: the drop inserts the path
 * it was given, and the menus leave the item out.
 */
export function uploadMovesTheFile(ctx: UploadContext): boolean {
  return !(ctx.native && ctx.kind === 'local')
}
