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
// ## What is deliberately NOT here
//
// **Dropping onto an individual folder row.** Out, in `§4` and in `§5.5`:
// it is a third target rule for a gesture nobody asked for. The panel's
// current folder is the panel's target and the tab's cwd is the terminal's.
//
// **A local tab does not copy (D9).** Every terminal inserts the dropped
// file's PATH at the prompt, and copying a file onto the machine it is
// already on is not a thing anybody asked for. This is one surface giving
// one input the meaning its context gives it — not two surfaces owning one
// gesture: there is exactly one drop handler and it reads the tab's kind.
//
// Which build the drop arrives through decides whether D9 can be honoured
// at all, and only one of them can. The Wails drop carries the path: Go
// took it from the runtime and, for a local tab alone, sends it back in
// `files.dropped` (`localPath`). A browser drop carries a `File`, which has
// a name and no location — that is the web platform and not a gap here — so
// there is no path to insert and the honest answer is to say so. Inserting
// the base name instead is worse than doing nothing: it looks like it
// worked, and then the command runs against whatever `report.pdf` resolves
// to in the shell's cwd, or against no file at all.
//
// ## The tab strip still reorders
//
// A dragover is acted on ONLY when the drag carries `Files`. The tab
// strip's own drag sets `application/x-nocx-tab` (layout/strip-drag.ts),
// so it never matches — the same condition v3's runtime applies before it
// touches a drag, and the reason EnableFileDrop does not break reordering.

import { hasWailsWebview } from '../wails-runtime'
import type { UploadServices } from './upload-client'
import type { UploadFlow, UploadReport, UploadSource } from './upload-flow'

/** What the tab is, at the moment of the drop. A subset of ActiveOrigin —
 *  the fields a destination is derived from — so this module can be driven
 *  without a pane. */
export interface DropOrigin {
  sessionId: string
  kind: 'local' | 'ssh'
  cwd: string | null
  cwdVerified: boolean
}

/** One dropped file as this module sees it, before the tab's kind decides
 *  what the drop means. It is an UploadSource plus the one thing an upload
 *  never needs and the prompt insert cannot do without.
 *
 *  `localPath` is present only where two things hold at once: the drop came
 *  through the Wails runtime, and it landed on a local tab. A browser `File`
 *  has no location to report, and a drop on a remote tab is deliberately
 *  told nothing but a name and a size (R2) — so its absence is the question
 *  "can this gesture honour D9", already answered by whoever built it. */
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
   *  surface that is — the draft at a prompt, the pty at a password. */
  insert: (text: string) => void
  report: UploadReport
  /** Injected so a test can drive both halves; production asks the one
   *  module that owns the question. */
  native?: () => boolean
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
    if (o.kind === 'local') {
      // D9. NO upload method is called on this path, and that is the point.
      const paths = sources.map((s) => s.localPath).filter((p) => p !== undefined)
      if (paths.length !== sources.length) {
        // Only the browser half gets here, and it gets here every time: a
        // `File` names itself and never says where it is. Refusing is the
        // honest answer, because the name on its own is not the path D9
        // promises and would run against a different file or none.
        report(
          'A browser cannot tell nocx where a dropped file is, so it cannot put its path on the command line.',
          'warning',
        )
        return
      }
      insert(paths.map(shellQuote).join(' '))
      return
    }
    // The destination is the tab's cwd — the same verified OSC 7 value the
    // Files panel follows. An UNVERIFIED cwd is the provider's fallback
    // answer to "where are we" and not a claim (AD-5), and an upload is a
    // write: putting a file into a guessed directory on somebody's server
    // is the one outcome worth refusing a gesture over.
    if (o.cwd === null || !o.cwdVerified) {
      report('nocx does not know this tab’s directory yet, so it cannot upload into it.', 'warning')
      return
    }
    const bindingId = await bindingFor(o.sessionId)
    if (bindingId === null) {
      report('The files of this machine could not be reached, so nothing was uploaded.', 'danger')
      return
    }
    await flow.send({ bindingId, destDir: o.cwd }, sources)
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
    const files = Array.from(e.dataTransfer?.files ?? [])
    void handle(files.map((f) => ({ name: f.name, size: f.size, blob: f })))
  }

  element.addEventListener('dragover', onDragOver)
  element.addEventListener('dragleave', onDragLeave)
  element.addEventListener('drop', onDrop)

  /** The native half. Filtered by session, because every pane subscribes
   *  and exactly one of them was dropped on. */
  const unsubDropped = deps.services.subscribeDropped((p) => {
    if (p.sessionId !== origin()?.sessionId) return
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
