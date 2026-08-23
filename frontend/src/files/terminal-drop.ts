// The terminal's drop gesture (design §4, §5.5) — drop a file onto the tab
// and it goes onto the machine that tab is on, into that tab's cwd.
//
// ## One gesture, two sources, and one of them never reaches the DOM
//
// In a browser the drop yields `File` objects and the upload is a STREAM:
// the renderer holds the bytes. In the Wails window the drop never becomes
// a DOM event we act on — v3's runtime hands the absolute paths to Go,
// which mints a source ticket per file and sends them back as a
// `files.dropped` notification carrying a name, a size and a ticket, and no
// path at all (R2) — except on a local tab, which mints nothing and where
// the path IS what the gesture is for (D9, below). So this module listens
// in both places and converges on the same flow; `hasWailsWebview()` is what decides which half is live,
// and it is asked rather than re-derived (there is one owner of "are we
// inside the webview", in wails-runtime.ts).
//
// ## Whoever has the path inserts it; whoever has only the bytes uploads
//
// That is D9, and it is the whole rule. It appeals to nothing about which
// machine anything is on, which is why every case falls out of it instead
// of needing an exception:
//
// - The Wails drop yields an ABSOLUTE PATH. Go took it from the runtime
//   and, for a local tab alone, sends it back in `files.dropped`
//   (`localPath`). There is a path, so it is inserted at the prompt and no
//   byte moves — copying a file onto the machine it is already on is not
//   what anybody asked for.
// - A browser drop yields a `File`: a name, a size and no location. There
//   is nothing to insert, so the bytes are uploaded into the tab's cwd,
//   through the same call a remote drop makes.
//
// D9 first said a local tab NEVER copies and a browser drop must refuse.
// That was reasoned from the desktop build, where the renderer and the
// tab's shell are provably one machine. In a browser they are not: a local
// tab is a shell on the BACKEND's machine and the file is on the
// BROWSER's, and the two coincide only under `make dev-web`. Refusing was
// never a defence — R1 forbids sending a file to the WRONG host, and the
// backend's own machine is exactly the host that tab is on.
//
// What has NOT changed is that a base name is never inserted in place of a
// path. It looks like it worked, and then the command runs against whatever
// `report.pdf` resolves to in the shell's cwd, or against no file at all.
//
// WHICH HALF APPLIES IS NOT DECIDED HERE. It is `uploadMovesTheFile` in
// upload-eligibility.ts, because the Files panel's menus ask the same
// question and the two used to answer it differently (nocx-9le.5.24).
//
// ## A dropped FOLDER is refused here, at the gesture
//
// Directories are out of scope (design §4) and the browser does not make
// that easy: a dropped folder arrives in `dataTransfer.files` as an
// ordinary `File` with the folder's name and its directory entry's size,
// and it simply cannot be read. Attempted, it produced two rows with no
// file name, `0%`, "Waiting for the server" and a toast reading "Failed to
// fetch" — a network error offered as the answer to "can I send a folder"
// (owner, 2026-08-22). `webkitGetAsEntry().isDirectory` is what says so
// before anything is registered; see `droppedDirectories` for why neither
// the size nor the MIME type can be used instead, and `folderRefusal` for
// why a mixed drop sends the files and refuses only the folder.
//
// The Wails half needs none of it — Go stats the path and refuses anything
// that is not a regular file before it mints a ticket.
//
// ## What is deliberately NOT here
//
// **Dropping onto an individual folder row.** Out, in `§4` and in `§5.5`:
// it is a third target rule for a gesture nobody asked for. The panel's
// current folder is the panel's target and the tab's cwd is the terminal's.
//
// **A second upload path for local tabs.** The collision question,
// progress, cancellation and the unsettled phase are transport-agnostic and
// are REUSED: both branches below end at the same `flow.send`. Two would be
// two owners of one behaviour, agreeing until the day they did not.
//
// ## The tab strip still reorders
//
// A dragover is acted on ONLY when the drag carries `Files`. The tab
// strip's own drag sets `application/x-nocx-tab` (layout/strip-drag.ts),
// so it never matches — the same condition v3's runtime applies before it
// touches a drag, and the reason EnableFileDrop does not break reordering.

import { hasWailsWebview } from '../wails-runtime'
import type { UploadServices } from './upload-client'
import { uploadMovesTheFile } from './upload-eligibility'
import type { UploadFlow, UploadReport, UploadSource } from './upload-flow'

/** This surface's name in `data-file-drop-target`. Exported so the element
 *  that carries the attribute and the subscriber that filters on it read one
 *  constant — two string literals is how they drift apart. */
export const TERMINAL_DROP_TARGET = 'terminal'

/** What the tab is, at the moment of the drop. A subset of ActiveOrigin —
 *  the fields a destination is derived from — so this module can be driven
 *  without a pane. */
export interface DropOrigin {
  sessionId: string
  kind: 'local' | 'ssh'
  cwd: string | null
  cwdVerified: boolean
  /** WHICH MACHINE this tab is on, as a person names it — `ActiveOrigin.machine`,
   *  which is the string the tab strip's own second line shows. Carried
   *  because the operations list is GLOBAL: by the time the row is drawn
   *  there is no tab to ask, so the answer travels with the transfer from
   *  the moment it starts. */
  machine: string
}

/** One dropped file as this module sees it, before the drop's meaning is
 *  decided. It is an UploadSource plus the one thing an upload never needs
 *  and the prompt insert cannot do without.
 *
 *  `localPath` is present only where two things hold at once: the drop came
 *  through the Wails runtime, and it landed on a local tab. A browser `File`
 *  has no location to report, and a drop on a remote tab is deliberately
 *  told nothing but a name and a size (R2) — so its presence IS the D9
 *  question "is there a path to insert", already answered by whoever built
 *  it. */
type DroppedFile = UploadSource & { localPath?: string }

export interface TerminalDropDeps {
  /** The pane element the drop lands on. */
  element: HTMLElement
  /** Read at drop time and never captured: a tab's cwd moves under it. */
  origin: () => DropOrigin | null
  /** The files.dropped half of the wire — the Wails source. */
  services: Pick<UploadServices, 'subscribeDropped'>
  flow: UploadFlow
  /** A binding for the tab's session, opened on demand and reused. Null
   *  when one could not be had. */
  bindingFor: (sessionId: string) => Promise<string | null>
  /** Put text where the person is typing (D9). The terminal owns which
   *  surface that is — the draft at a prompt, the pty at a password.
   *  Reached only when the drop carried paths. */
  insert: (text: string) => void
  report: UploadReport
  /** Injected so a test can drive both halves; production asks the one
   *  module that owns the question. */
  native?: () => boolean
}

/**
 * WHICH OF A BROWSER DROP'S ITEMS ARE DIRECTORIES — a parallel array to
 * `dataTransfer.files`, `true` where that entry is a folder.
 *
 * Directories are deliberately out of scope (design §4: a recursive walk,
 * mkdir, partial failure mid-tree, symlinks and modes are a materially
 * larger problem on the same mechanism, deferred to the next wave). The
 * browser does not spare us the decision: a dropped folder arrives in
 * `dataTransfer.files` as an ordinary `File` carrying the folder's name and
 * the size of its directory ENTRY — 192 B on APFS — which cannot be read.
 * Left to itself the body POST fails and the person is told "Failed to
 * fetch" about a question they never asked (owner, 2026-08-22).
 *
 * `webkitGetAsEntry().isDirectory` is the web platform's own answer and the
 * only reliable one. The two tempting substitutes are both wrong: 192 B is
 * a coincidence of one filesystem, and an empty `File.type` is what plenty
 * of real files have.
 *
 * The ITEMS are what carry it and the FILES are what the upload needs, so
 * the two lists are aligned by position — which holds because a drop
 * appends one file item per file, in order. Items of kind `string` (a drag
 * that also carries text) are dropped first, since those have no `File` at
 * all and would shift every index after them.
 *
 * An engine with no `items` yields nothing rather than guessing, and the
 * caller then treats every entry as a file — which is exactly what happened
 * before this existed. The Wails half needs none of it: Go stats the path
 * and refuses anything that is not a regular file already
 * (`describeSource`, `SourceTicketStore.mint`).
 */
function droppedDirectories(transfer: DataTransfer): boolean[] {
  const items: DataTransferItemList | undefined = transfer.items
  if (items === undefined || items === null) return []
  const flags: boolean[] = []
  for (let i = 0; i < items.length; i++) {
    const item = items[i]
    if (item.kind !== 'file') continue
    // Absent in an engine that does not implement it, and null for an item
    // that cannot answer. Neither is a claim that this IS a directory.
    const entry = typeof item.webkitGetAsEntry === 'function' ? item.webkitGetAsEntry() : null
    flags.push(entry !== null && entry.isDirectory)
  }
  return flags
}

/**
 * What the person is told about the folders in their drop.
 *
 * It names the LIMIT rather than reporting whatever went wrong downstream,
 * and it names the way round it — somebody dropping a folder has asked for
 * something reasonable, and "unsupported" on its own tells them nothing
 * they can do next.
 *
 * A MIXED DROP SENDS THE FILES AND REFUSES THE FOLDER. Dropping the whole
 * batch because one item cannot be sent punishes the three that could;
 * sending three quietly while saying nothing about the fourth is worse
 * still, because the person watches three rows appear and has to work out
 * for themselves which item is missing. So the message says exactly which
 * was refused, and — since the rows for the rest are about to appear — how
 * many are on their way.
 */
function folderRefusal(folders: string[], sending: number): string {
  const what = folders.length === 1 ? 'a folder' : 'folders'
  const named = folders.join(', ')
  const were = folders.length === 1 ? 'was' : 'were'
  const rest =
    sending === 0
      ? ''
      : sending === 1
        ? ' The other file is being uploaded.'
        : ` The other ${sending} files are being uploaded.`
  return (
    `nocx cannot upload ${what} yet, so ${named} ${were} not sent. ` +
    `Send the files inside it, or archive it and send the archive.${rest}`
  )
}

/** A drag we act on. v3's runtime uses exactly this test before it touches
 *  a drag, and the tab strip's own drag deliberately fails it. */
function carriesFiles(transfer: DataTransfer | null): boolean {
  if (transfer === null) return false
  // `types` is a DOMStringList in some engines and an array in others;
  // both answer to includes() via Array.from.
  return Array.from(transfer.types).includes('Files')
}

/**
 * Quote one name for a shell.
 *
 * A third private copy of four characters of regex: `ssh-transition.ts` and
 * `integration/status.ts` each hold one and neither exports it. Extracting
 * the one answer is right and is a change to two modules this gesture does
 * not touch — recorded rather than done here, because a silent third copy
 * is how the count reaches four.
 */
function shellQuote(s: string): string {
  if (/^[\w@%+=:,./-]+$/.test(s)) return s
  return `'${s.replace(/'/g, `'\\''`)}'`
}

export function attachTerminalDrop(deps: TerminalDropDeps): () => void {
  const { element, origin, flow, bindingFor, insert, report } = deps
  const native = deps.native ?? hasWailsWebview

  // THE DROP-TARGET ATTRIBUTES ARE NOT THIS MODULE'S. `data-file-drop-target`
  // and `data-session-id` are set by TerminalContent when the session opens
  // and removed when it is disposed (nocx-9le.5.8) — the session is what a
  // native drop routes to and it does not exist before then. A second writer
  // here would be two owners of one attribute, agreeing until the day they
  // did not. What this module owns is `data-drop-active`, which says a files
  // drag is over the pane right now.

  async function handle(sources: DroppedFile[]): Promise<void> {
    if (sources.length === 0) return
    const o = origin()
    if (o === null) {
      report(
        'This tab cannot say which machine it is on, so there is nowhere to put a file.',
        'warning',
      )
      return
    }
    if (!uploadMovesTheFile({ native: native(), kind: o.kind })) {
      // D9's insert half. WHICH half applies is not this module's to
      // decide — upload-eligibility.ts owns that rule, and the Files
      // panel's menus ask it too, so the drop and the menu cannot give
      // different answers about the same tab (nocx-9le.5.24).
      //
      // What stays here is the guard the branch itself needs: every
      // dropped file must actually have arrived with a path. A source
      // that has none falls through to the refusal below rather than
      // being inserted as a bare base name, which looks like it worked
      // and then runs the command against whatever `report.pdf` resolves
      // to in the shell's cwd. NO upload method is called on this path,
      // and that is the point.
      const paths = sources.map((s) => s.localPath).filter((p) => p !== undefined)
      if (paths.length === sources.length) {
        insert(paths.map(shellQuote).join(' '))
        return
      }
    }
    // Neither half of the rule applies: no path to insert AND nothing to
    // upload. A source carries bytes as a `blob` (the browser) or names
    // them with a `sourceTicket` (the Wails runtime); one with neither
    // would start a transfer whose body can never arrive, and the person
    // would watch "uploading" forever. Refusing says so instead.
    //
    // An EMPTY ticket is no ticket, not a ticket that is empty: the backend
    // sends `sourceTicket: ""` for a drop it minted nothing for, which is
    // every drop on a local tab (ws_upload_source.go, SourcePick.Ticket).
    if (sources.some((s) => s.blob === undefined && !s.sourceTicket)) {
      report('nocx was not told where that file is, so it could not be sent.', 'warning')
      return
    }
    // The destination is the tab's cwd — the same verified OSC 7 value the
    // Files panel follows. An UNVERIFIED cwd is the provider's fallback
    // answer to "where are we" and not a claim (AD-5), and an upload is a
    // write: putting a file into a guessed directory is the one outcome
    // worth refusing a gesture over, on this machine as much as on
    // anybody else's.
    if (o.cwd === null || !o.cwdVerified) {
      report('nocx does not know this tab’s directory yet, so it cannot upload into it.', 'warning')
      return
    }
    const bindingId = await bindingFor(o.sessionId)
    if (bindingId === null) {
      report('The files of this machine could not be reached, so nothing was uploaded.', 'danger')
      return
    }
    await flow.send({ bindingId, destDir: o.cwd, machine: o.machine }, sources)
  }

  const onDragOver = (e: DragEvent): void => {
    if (!carriesFiles(e.dataTransfer)) return
    // Only now: preventDefault on a drag we do not own is what would stop
    // the tab strip's reorder.
    e.preventDefault()
    element.dataset.dropActive = ''
  }

  const onDragLeave = (): void => {
    delete element.dataset.dropActive
  }

  const onDrop = (e: DragEvent): void => {
    if (!carriesFiles(e.dataTransfer)) return
    e.preventDefault()
    delete element.dataset.dropActive
    // Inside the webview the drop has already gone to Go, which is minting
    // tickets for it; acting on the DOM event too would send every file
    // twice, once as a stream and once as a path.
    if (native()) return
    const transfer = e.dataTransfer
    if (transfer === null) return
    const files = Array.from(transfer.files)
    // REFUSED BEFORE ANYTHING IS REGISTERED, and that is the point: a row
    // implies something is happening to the thing it names, and nothing is
    // ever going to happen to a folder. It never reaches the flow, so it
    // never becomes a queued row and never becomes a transfer.
    const isDirectory = droppedDirectories(transfer)
    const folders = files.filter((_, i) => isDirectory[i] === true)
    const sendable = files.filter((_, i) => isDirectory[i] !== true)
    if (folders.length > 0) {
      report(
        folderRefusal(
          folders.map((f) => f.name),
          sendable.length,
        ),
        'warning',
      )
    }
    if (sendable.length === 0) return
    void handle(sendable.map((f) => ({ name: f.name, size: f.size, blob: f })))
  }

  element.addEventListener('dragover', onDragOver)
  element.addEventListener('dragleave', onDragLeave)
  element.addEventListener('drop', onDrop)

  /** The native half. Filtered by session AND by target: every pane
   *  subscribes and exactly one of them was dropped on — and since
   *  nocx-cx442 a session can have more than one drop surface, because the
   *  import ask names the local tab too. Session alone had the pane typing
   *  an export's path at the prompt because somebody dropped it in a
   *  dialog. */
  const unsubDropped = deps.services.subscribeDropped((p) => {
    if (p.sessionId !== origin()?.sessionId) return
    if (p.target !== TERMINAL_DROP_TARGET) return
    void handle(
      p.sources.map((s) => ({
        name: s.name,
        size: s.size,
        sourceTicket: s.sourceTicket,
        localPath: s.localPath,
      })),
    )
  })

  return () => {
    element.removeEventListener('dragover', onDragOver)
    element.removeEventListener('dragleave', onDragLeave)
    element.removeEventListener('drop', onDrop)
    delete element.dataset.dropActive
    unsubDropped()
  }
}
