// Choosing files to upload — the second gesture's source half (design §4).
//
// Two environments, one question, and the difference is R2. In the Wails
// window the picker is NATIVE and its answer is a backend-minted source
// ticket: the file the person chose lives on the backend's machine, and a
// renderer that could name it by path could ask the backend to read
// ~/.ssh/id_ed25519 and send it to a host of the renderer's choosing. So
// dialog.openFileForUpload mints instead of returning, and the renderer
// learns a display name and a size and nothing else.
//
// In a browser there is no Wails and deliberately no fallback that would
// let the renderer name a source. What there is instead is the browser's
// own picker, whose answer is `File` objects — bytes the renderer already
// holds, which is a stream upload and needs no path either.
//
// The `<input type="file">` here is never rendered and never styled: it is
// created detached, clicked, and removed. It is the platform's way of
// RAISING a picker, the way navigator.clipboard is the platform's way of
// writing the clipboard — not a control the surface drew for itself. The
// kit's FileInput is the control for a form that shows a chosen file; a
// menu item that opens a picker shows nothing.

import { hasWailsWebview } from '../wails-runtime'
import type { UploadServices } from './upload-client'
import type { UploadReport, UploadSource } from './upload-flow'

export interface PickDeps {
  services: Pick<UploadServices, 'pickSource'>
  report: UploadReport
  /** Injected so a test can drive both halves. */
  native?: () => boolean
  /** The document the browser picker is raised in. */
  doc?: Document
}

/** The browser's picker. Resolves to what was chosen, or nothing when the
 *  person cancelled. */
function pickInBrowser(doc: Document): Promise<UploadSource[]> {
  return new Promise((resolve) => {
    const input = doc.createElement('input')
    input.type = 'file'
    input.multiple = true
    doc.body.appendChild(input)
    let settled = false
    const finish = (sources: UploadSource[]): void => {
      if (settled) return
      settled = true
      input.remove()
      resolve(sources)
    }
    input.addEventListener('change', () => {
      finish(Array.from(input.files ?? []).map((f) => ({ name: f.name, size: f.size, blob: f })))
    })
    // Cancel is its own event and not every engine fires it. When it does
    // not, this promise simply never settles and nothing happens — which is
    // what cancelling asked for. The alternative, resolving on a focus
    // event, fires spuriously while the picker is still open.
    input.addEventListener('cancel', () => finish([]))
    input.click()
  })
}

/** Ask the person for files to upload. Never rejects: an unavailable
 *  picker is reported and answered with nothing chosen. */
export async function pickUploadSources(deps: PickDeps): Promise<UploadSource[]> {
  const native = deps.native ?? hasWailsWebview
  if (!native()) return pickInBrowser(deps.doc ?? document)
  try {
    const picked = await deps.services.pickSource()
    // "" is cancel — not an error, the way dialog.openFile's empty path
    // already works. The TICKET, not the size, is what says whether
    // anything was chosen: 0 is also a genuinely empty file.
    if (picked.sourceTicket === '') return []
    return [{ name: picked.name, size: picked.size, sourceTicket: picked.sourceTicket }]
  } catch (e) {
    deps.report(
      `The file picker is not available here: ${e instanceof Error ? e.message : String(e)}`,
      'danger',
    )
    return []
  }
}
