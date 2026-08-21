// ApiStore — the one list every part of the workbench reads (the Files and
// Git store pattern): the open collections, the request in the form, and the
// runs. Plain Solid signals; nothing here renders, and no method is called
// during a render.
//
// Three rules, each with the thing it stops:
//
// 1. SEND WRITES FIRST, BECAUSE THE FILE IS WHAT GETS SENT. `api.request.send`
//    takes a handle and a path — never a request value — so what goes out is
//    what is on disk. A form able to send something the file does not contain
//    would be a second truth beside the one design §6.4 names, and the two
//    would agree until the day they did not. The write happens only when the
//    draft actually differs from what was read: pressing Send on an untouched
//    request must not rewrite the file, because a git diff a person did not
//    cause is how the "shareable through git" claim stops being true.
//    A write that FAILS stops the send: the file never held what would have
//    gone out, so sending would put the OLD request on the wire under the new
//    request's name.
//
// 2. A FAILED SEND IS A RUN, NOT A DISAPPEARANCE. Refused connection, TLS
//    error, a dead handle — each becomes a row in the list saying why. A run
//    that vanishes on failure teaches that nothing happened.
//
// 3. ONE LIST OF COLLECTIONS, WHICHEVER DOOR THE FOLDER CAME THROUGH.
//    `api.collections.open` answers a handle plus a collection;
//    `api.collections.list` answers rows. `adoptOpenedCollection` puts the
//    first into the second's shape (api-model.ts), so nothing downstream has
//    to ask which call produced the row it is looking at.
//
// The pretty/raw choice belongs to ONE run. A single flag for the list would
// mean opening the raw text of the run you are reading also opens it for the
// nineteen above it.

import { createSignal } from 'solid-js'
import type { ApiWorkbenchServices } from './api-client'
import {
  adoptImportedRequest,
  adoptOpenedCollection,
  type ApiImportNote,
  type ApiOpenCollection,
  type ApiRequest,
  type ApiResponse,
} from './api-model'
import type { Unsupported as PostmanNote } from '../generated/api.import.postman'

/** How a run's body is being read. */
export type ApiRunView = 'pretty' | 'raw'

/** One exchange, as the list holds it. Exactly one of `response` and `error`
 *  is set: an exchange either came back or did not, and a row that could
 *  hold both would have to decide which one to believe. */
export interface ApiRun {
  readonly id: number
  /** The method and URL as the FORM had them when Send was pressed — what
   *  the person asked for. Kept on the run so scrolling back through twenty
   *  of them does not require remembering what the form held at the time.
   *
   *  The REQUEST itself is deliberately not kept. The raw view draws the
   *  request from `response.raw.request` — the backend's own account of what
   *  it put on the socket, with the spans it placed (§11.2) — so a copy of
   *  the model here would be a second, unread answer to "what was sent", and
   *  a field written by two call sites and read by none is the shape this
   *  repo has shipped before. */
  readonly method: string
  readonly url: string
  readonly response: ApiResponse | null
  readonly error: string | null
  readonly view: ApiRunView
}

/** Which request file the form is showing, or null when the draft came from
 *  an import and has no file behind it yet. */
interface ApiSelection {
  readonly handle: string
  readonly relPath: string
}

export interface ApiStore {
  collections(): readonly ApiOpenCollection[]
  selected(): ApiSelection | null
  draft(): ApiRequest | null
  /** True while the draft differs from what the file last answered. */
  dirty(): boolean
  runs(): readonly ApiRun[]
  notes(): readonly ApiImportNote[]
  /** The last failure, in the words the backend used, or '' when the last
   *  thing attempted worked. On the surface rather than in a log: a degrade
   *  the UI does not show is a feature that does not exist surviving a
   *  release. */
  error(): string
  loading(): boolean
  sending(): boolean

  refresh(): Promise<void>
  openFolder(path: string): Promise<void>
  closeFolder(handle: string): Promise<void>
  openRequest(handle: string, relPath: string): Promise<void>
  editDraft(next: ApiRequest): void
  send(): Promise<void>
  importCurl(line: string): Promise<void>
  importPostman(path: string, dest: string): Promise<void>
  setRunView(id: number, view: ApiRunView): void
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export function createApiStore(services: ApiWorkbenchServices): ApiStore {
  const [collections, setCollections] = createSignal<readonly ApiOpenCollection[]>([])
  const [selected, setSelected] = createSignal<ApiSelection | null>(null)
  const [draft, setDraft] = createSignal<ApiRequest | null>(null)
  const [saved, setSaved] = createSignal<ApiRequest | null>(null)
  const [runs, setRuns] = createSignal<readonly ApiRun[]>([])
  const [notes, setNotes] = createSignal<readonly ApiImportNote[]>([])
  const [error, setError] = createSignal('')
  const [loading, setLoading] = createSignal(false)
  const [sending, setSending] = createSignal(false)

  // Run ids come from a counter rather than a clock: `Date.now()` gives two
  // runs fired in the same millisecond one id, and a list keyed by a
  // duplicate id renders one row for two exchanges.
  let nextRunId = 1

  /** The draft differs from what the file last answered. Compared by value —
   *  the form replaces the object on every keystroke, so identity would say
   *  "dirty" for a field typed into and typed back. */
  const dirty = (): boolean => {
    const d = draft()
    const s = saved()
    if (d === null || s === null) return false
    return JSON.stringify(d) !== JSON.stringify(s)
  }

  const refresh = async (): Promise<void> => {
    setLoading(true)
    try {
      const result = await services.listCollections()
      setCollections(result.collections)
      setError('')
    } catch (err) {
      setError(message(err))
    } finally {
      setLoading(false)
    }
  }

  const openFolder = async (path: string): Promise<void> => {
    try {
      const result = await services.openCollection(path)
      const row: ApiOpenCollection = {
        handle: result.handle,
        path,
        error: '',
        collection: adoptOpenedCollection(result.collection),
      }
      // The handle identifies the row, not the path: re-opening the folder
      // the user already has open must not put a second copy of it in the
      // tree with a stale listing beside a fresh one.
      setCollections((prev) => [...prev.filter((c) => c.handle !== row.handle), row])
      setError('')
    } catch (err) {
      setError(message(err))
    }
  }

  const closeFolder = async (handle: string): Promise<void> => {
    try {
      await services.closeCollection(handle)
      setCollections((prev) => prev.filter((c) => c.handle !== handle))
      // The form was showing a request in the folder that just left. Keeping
      // it would leave a Send pointed at a handle that no longer resolves.
      if (selected()?.handle === handle) {
        setSelected(null)
        setDraft(null)
        setSaved(null)
      }
      setError('')
    } catch (err) {
      setError(message(err))
    }
  }

  const openRequest = async (handle: string, relPath: string): Promise<void> => {
    try {
      const result = await services.readRequest(handle, relPath)
      setSelected({ handle, relPath })
      setDraft(result.request)
      setSaved(result.request)
      setError('')
    } catch (err) {
      // The previous request stays in the form. Clearing it would make one
      // unreadable file look like the whole collection went away.
      setError(message(err))
    }
  }

  const editDraft = (next: ApiRequest): void => {
    setDraft(next)
  }

  const send = async (): Promise<void> => {
    const target = selected()
    const request = draft()
    if (target === null || request === null) return
    setSending(true)
    try {
      if (dirty()) {
        // Rule 1: the file is what gets sent, so it holds the draft before
        // the exchange — and a refused write stops here, never sending the
        // request the file still contains under the draft's name.
        await services.writeRequest(target.handle, target.relPath, request)
        setSaved(request)
      }
      const result = await services.sendRequest(target.handle, target.relPath)
      setRuns((prev) => [
        {
          id: nextRunId++,
          method: request.method,
          url: request.url,
          response: result.response,
          error: null,
          view: 'pretty',
        },
        ...prev,
      ])
      setError('')
    } catch (err) {
      const reason = message(err)
      if (dirty()) {
        // The write is the step that failed: nothing went out, so there is
        // no exchange to record — only a reason the file could not be saved.
        setError(reason)
      } else {
        setRuns((prev) => [
          {
            id: nextRunId++,
            method: request.method,
            url: request.url,
            response: null,
            error: reason,
            view: 'pretty',
          },
          ...prev,
        ])
      }
    } finally {
      setSending(false)
    }
  }

  const importCurl = async (line: string): Promise<void> => {
    try {
      const result = await services.importCurl(line)
      // No file behind it yet, so nothing is selected — Send stays refused
      // until the request is saved into a collection, which is honest: there
      // is nothing on disk for api.request.send to send.
      setSelected(null)
      setSaved(null)
      setDraft(adoptImportedRequest(result.request))
      setNotes(result.unsupported)
      setError('')
    } catch (err) {
      setError(message(err))
    }
  }

  const importPostman = async (path: string, dest: string): Promise<void> => {
    try {
      const result = await services.importPostman(path, dest)
      // Both importers' "what did not come across" is one vocabulary — a
      // feature named, and why — so the surface holds one list of them.
      const carried: ApiImportNote[] = result.unsupported satisfies PostmanNote[]
      setNotes(carried)
      setError('')
      // The folder is on disk now; the listing is what puts it in the tree.
      await refresh()
    } catch (err) {
      setError(message(err))
    }
  }

  const setRunView = (id: number, view: ApiRunView): void => {
    setRuns((prev) => prev.map((r) => (r.id === id ? { ...r, view } : r)))
  }

  return {
    collections,
    selected,
    draft,
    dirty,
    runs,
    notes,
    error,
    loading,
    sending,
    refresh,
    openFolder,
    closeFolder,
    openRequest,
    editDraft,
    send,
    importCurl,
    importPostman,
    setRunView,
  }
}
