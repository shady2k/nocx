// The API workbench, as one pane (design §9.1, §9.2): the collection tree,
// the request form, and the list of runs — left to right and top to bottom.
//
// ONE workbench, not a pane per request. Requests are switched between
// constantly, so a tab each would make the strip worse than a list is; the
// tree lives HERE and is not duplicated into a sidebar view, because two
// trees would be two owners of one selection.
//
// The environment WAS deliberately a statement rather than a control, on the
// argument that the api.* contract carried no environment method — "a
// dropdown over nothing would be a control that governs nothing". The
// argument was right and the premise was wrong: `api.request.send` has
// accepted `envRelPath` since it was written, and what was missing was the
// renderer's half. So the statement was a surface saying "there is no route
// to choose" while the send path resolved variables against an environment
// nobody could name, and every collection whose URL is `{{baseUrl}}/…` —
// nearly every Postman export — failed from the product while working
// perfectly over the control plane (nocx-pnvnn).
//
// It is a control now, over the collection's own environments. It is absent
// rather than empty for a collection that has none, for the reason the old
// comment gives: a picker with nothing in it is a control that governs
// nothing.

import {
  For,
  Show,
  createEffect,
  createSignal,
  onCleanup,
  onMount,
  untrack,
  type JSX,
} from 'solid-js'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Caption } from '../ui/caption'
import { EmptyState } from '../ui/empty-state'
import { IconButton } from '../ui/icon-button'
import {
  ArrowDownIcon,
  CloseIcon,
  CopyIcon,
  FolderIcon,
  FolderOpenIcon,
  MoreIcon,
  PencilIcon,
  PlusIcon,
  RefreshIcon,
  TrashIcon,
} from '../ui/icons'
import { ResizeHandle } from '../ui/resize-handle'
import { ContextMenu, type ContextMenuItem } from '../ui/context-menu'
import { Section } from '../ui/section'
import { TextField } from '../ui/text-field'
import { StatusCard } from '../ui/status-card'
import { TreeEmpty } from '../ui/tree-empty'
import { TreeRow } from '../ui/tree-row'
import { WatchBadge } from '../ui/watch-badge'
import { showConfirm } from '../ui/dialog'
import { dismissToast, showToast } from '../ui/toast'
import { createClipboardAccess } from '../clipboard'
import {
  contentsOf,
  directoryOf,
  filterCollections,
  flattenCollections,
  type ApiTreeRow,
} from './api-tree'
import { malformedReason } from './malformed-reason'
import { CollectionDialog } from './collection-dialog'
import { FolderView, type FolderEntry } from './folder-view'
import { EnvironmentView, toRows, toStored, type ValueRow } from './environment-view'
import {
  classifyPastedSource,
  environmentPath,
  proposedDestination,
  proposedDestinationFromDocument,
  proposedDestinationFromURL,
} from './api-paths'
import { API_IMPORT_DROP_TARGET, CurlImportDialog, PostmanImportDialog } from './import-dialogs'
import { RequestCrumbs } from './request-crumbs'
import { MoveToFolderDialog } from './move-dialog'
import { RequestEditor, RequestLine, type SecretTarget } from './request-form'
import { RunList } from './run-list'
import type { ApiStore, VariableAnswer } from './api-store'
import type { DirectoryPicker, FilePicker, ImportSource, NativeDropPort } from './api-client'
import type { ApiOpenCollection, ApiParam, ApiRequest, ApiRoute } from './api-model'
import { findVariables } from './variable-reference'

export interface ApiPaneProps {
  store: ApiStore
  /**
   * The native directory picker, when the backend offers one.
   *
   * It is not on the store: the store owns api.* state, and `dialog.*` is
   * another domain's method reached through another client (AD-8). It
   * arrives here for the same reason `openFileDialog` arrives at
   * KeyMaterialInput — a capability the surface offers only where it exists.
   */
  openDirectory?: DirectoryPicker
  /**
   * The native FILE picker, when the backend offers one.
   *
   * Beside the directory picker rather than merged with it: they are two
   * `dialog.*` methods, each of which can report itself unavailable on its
   * own, and one signal for both would draw a control this build cannot
   * honour (api-client.ts).
   */
  openFile?: FilePicker
  /**
   * The native window drop, when the build has one.
   *
   * Absent wherever there is no Wails runtime, which is every `make dev-web`
   * run and the whole e2e harness. That is no longer the same thing as "no
   * drop": there the drop is a DOM event carrying the BYTES, which the ask
   * takes and sends as a document (spec §1a). What this port carries is the
   * other route — the paths Go took off the runtime — and its ABSENCE is
   * also what tells the ask that a DOM drop is its own to act on
   * (`nativeWindow`, below).
   */
  nativeDrop?: NativeDropPort
}

/** Floors for the two seams: a tree column narrower than this cannot show a
 *  nested request's name, and a request half narrower than this cannot show
 *  a name-and-value row. */
const MIN_TREE_WIDTH = 180
const MIN_REQUEST_WIDTH = 320

/** What a drop of several exports is told, in ONE place: the native half
 *  and the browser half are the same rule — an import makes one collection,
 *  and N collections is N destinations, which is a different question and
 *  not one this ask can answer by guessing which of them was meant. */
const MULTIPLE_EXPORTS_REFUSAL = 'Drop one export at a time — an import makes one collection.'

/** What a paste that is neither a URL nor a document is told. The sentence
 *  names the two things this ask takes rather than the one it refused,
 *  because curl has its own door in the request editor and a person who
 *  pasted a command line is one control away from the right one. */
const NOT_AN_EXPORT_REFUSAL =
  "That is not a Postman export or a URL — paste the export's text, or drop the file below."

/** A fetch that leaves from this machine. One object rather than a literal
 *  per site, because "direct" is one state and three spellings of it is
 *  three states that agree until they do not. */
/** How long the folder page waits for typing to stop before it writes. Long
 *  enough that a row typed character by character is one write, short enough
 *  that a person who types and looks up has already been saved. */
const FOLDER_SAVE_DEBOUNCE = 400

const DIRECT_ROUTE: ApiRoute = { kind: 'direct', profileId: '', insecureTls: false }

/** What a REQUEST drag carries, so the drop handler can tell its own drag
 *  from a foreign one (an OS file drop carries `Files`, the tab strip
 *  carries its own type). The payload is the request's handle and path as
 *  JSON — the two facts the move call needs — and the DATA is read only at
 *  the drop, which is the one moment the browser hands it over. */
const API_DRAG_MIME = 'application/x-nocx-api-request'

/** What a drop onto ANOTHER COLLECTION is told. `api.request.move` takes
 *  one handle — nocx-8aczn put cross-collection out of the METHOD, not
 *  just out of the gesture — so the drop is refused, and it SAYS so rather
 *  than silently doing nothing. */
const CROSS_COLLECTION_REFUSAL = 'A request can only be moved within its own collection.'

/** The two header names HTTP itself defines as carrying credentials. A LIST
 *  OF NAMES, not a detector: `internal/secrets.Detect` is the one owner of
 *  "is this text credential-shaped", it lives on the backend, and a second
 *  derivation of it here would agree with it about every value anyone tried
 *  and disagree about the one that mattered (AGENTS.md). What this side can
 *  own without a second owner is a GRAMMAR question, and that is all it
 *  asks. */
const CREDENTIAL_HEADERS: ReadonlySet<string> = new Set(['authorization', 'proxy-authorization'])

/** Whether `s` is exactly one `{{name}}` and nothing else — the renderer's
 *  half of `apiimport.varRef`, over the scan that already mirrors the
 *  backend's variable grammar (variable-reference.ts). */
function isOneVariableReference(s: string): boolean {
  const spans = findVariables(s)
  return spans.length === 1 && spans[0].from === 0 && spans[0].to === s.length
}

/**
 * Where a credential sits in the request AS TEXT, or '' when there is none.
 *
 * WHY THE SURFACE ASKS AT ALL (nocx-flidy). The folder ask used to promise
 * "no secret value is ever written into it", which was true while every
 * credential arrived as a variable NAME resolved from the vault. nocx-14exx
 * decided — deliberately, and re-confirmed since — that a credential a person
 * pastes stays where they put it, so a curl line's Authorization header and
 * an auth field's literal are text in the request file, in the folder that
 * sentence is about. Nothing here rewrites, refuses or sanitises it; the
 * sentence is the only thing that changes.
 *
 * THE RULE IS `authFromHeader`'s, mirrored: an Authorization value is a
 * scheme, a space, and the credential, and the credential is variable-bound
 * exactly when it is one `{{name}}`. `Bearer {{token}}` therefore carries
 * nothing, and `Bearer ghp_…` carries everything. A value with no space at
 * all is the whole credential. THE AUTH BLOCK IS THE SAME QUESTION ANSWERED
 * FOR ITS FIELDS: since nocx-6hg2w.20 the token and password fields are text
 * like the headers, so a literal in either is exactly as much text in the
 * file as one in an Authorization header — and one grammar decides both, so
 * the two cannot diverge.
 *
 * IT ANSWERS ABOUT THE REQUEST IN THE FORM, which is what is about to be
 * saved into the folder being named, and never about the collection's other
 * files: `api.collections.list` carries a request's path, name and method
 * and no header at all, so a claim about the whole folder would be one the
 * renderer cannot make. That is why the sentence NAMES what it looked at.
 */
function literalCredentialIn(req: ApiRequest | null): string {
  if (req === null) return ''
  for (const h of req.headers) {
    if (!h.enabled) continue
    if (!CREDENTIAL_HEADERS.has(h.name.trim().toLowerCase())) continue
    const value = h.value.trim()
    if (value === '') continue
    const space = value.indexOf(' ')
    const credential = space < 0 ? value : value.slice(space + 1).trim()
    if (credential === '' || isOneVariableReference(credential)) continue
    return h.name.trim()
  }
  const auth = req.auth
  if (auth.kind === 'bearer' || auth.kind === 'apikey') {
    const v = auth.token.trim()
    if (v !== '' && !isOneVariableReference(v)) return auth.kind === 'apikey' ? 'API key' : 'Bearer'
  }
  if (
    auth.kind === 'basic' &&
    auth.password.trim() !== '' &&
    !isOneVariableReference(auth.password.trim())
  ) {
    return 'Basic password'
  }
  return ''
}

/**
 * The one source the import ask is holding.
 *
 * `path` is a place on the machine running Go — the native drop and the
 * system picker. `file` is the bytes themselves — a browser drop and the kit's
 * file input — read at submit rather than now, because a file can move or be
 * revoked between the gesture and the press. `document` and `url` are what
 * the paste box yielded, classified once by `classifyPastedSource`.
 */
type HeldSource =
  | { kind: 'none' }
  | { kind: 'path'; path: string }
  | { kind: 'file'; file: File }
  | { kind: 'document'; text: string }
  | { kind: 'url'; url: string }

/** What the source line says the ask is holding, in the currency the person
 *  recognises: the path they picked, the file they dropped, the address they
 *  pasted — and for the export's own text, which has no name of its own, what
 *  it is. '' is "holding nothing", which is also what disables Import. */
function sourceLabel(held: HeldSource): string {
  switch (held.kind) {
    case 'path':
      return held.path
    case 'file':
      return held.file.name
    case 'document':
      return 'Pasted Postman export'
    case 'url':
      return held.url
    default:
      return ''
  }
}

/** What the (hidden) export field shows — the half of the held source that is
 *  a FILE by any route. It is derived rather than stored so that the field
 *  cannot go on displaying a path the ask has already replaced with a paste:
 *  one source, one place it is read from. */
function sourcePath(held: HeldSource): string {
  if (held.kind === 'path') return held.path
  if (held.kind === 'file') return held.file.name
  return ''
}

/** The empty set a filtered tree is flattened against — one object rather
 *  than a new Set per read, because `rows()` runs on every keystroke. */
const NOTHING_COLLAPSED: ReadonlySet<string> = new Set()

/** ONE REQUEST, ADDRESSED: where it is, and what to say it by.
 *
 *  The three facts every act about a request needs, and one shape for all of
 *  them — the menu's target, the move chooser's "open, and about THIS", and
 *  the row a button on the folder page acts on. It was `MoveTarget` and
 *  carried a comment saying it was the same three facts the request menu
 *  held; a second name for one shape is how the two drift. */
interface RequestTarget {
  handle: string
  relPath: string
  name: string
}

export function ApiPane(props: ApiPaneProps) {
  // The store is constructed once by ApiContent and handed in; it is a
  // dependency, not a reactive value, and nothing ever swaps one workbench's
  // store for another's. Reading it here rather than at every call site keeps
  // the surface readable — the reactivity that matters is inside the store's
  // own signals, which ARE read in tracked scopes below.
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const store = props.store
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const picker = props.openDirectory
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const filePicker = props.openFile
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const nativeDrop = props.nativeDrop
  // The clipboard a copied path lands on (AD-8). The composition root
  // injects one into the surfaces whose copy was designed with it; the
  // workbench predates that seam, so it makes its own through the same
  // factory the other surfaces fall back to.
  const clipboard = createClipboardAccess()
  const [collapsed, setCollapsed] = createSignal<ReadonlySet<string>>(new Set())
  // The Import section used to be a collapsible form here, and its
  // disclosure was written `open={false} onToggle={() => undefined}` — a
  // controlled disclosure with no state owner, so the button reported
  // "collapsed" for ever and the panel's only Postman entrance could not be
  // reached at all (nocx-6siis). It is an ask off the collections menu now,
  // which has no disclosure to own: a dialog is open or it is not, and the
  // signal that says which is `importing` below.
  // WHERE THE MENU IS, and whether it is open. The kit's ContextMenu is a
  // popover anchored at a POINT — the caller owns the open state and hands
  // it viewport coordinates — so the surface takes the point off the button
  // that opened it, which is what makes the menu hang under the `+` rather
  // than wherever the last right-click was.
  // ── The two seams (nocx: the panel's own edges) ─────────────────────────
  //
  // Both are PIXELS held here, not fractions and not settings. Pixels
  // because that is what the kit's ResizeHandle reports and what a person
  // dragged to; here rather than in a document because the workbench is a
  // pane and nothing yet persists a pane's internal geometry — the sidebar's
  // width has a settings owner (sidebar-width.ts) and this deliberately does
  // not borrow it: one owner per behaviour, and that one is the SHELL's
  // sidebar, not a pane's tree column.
  //
  // The interval, both ends: a size holds from the drag that set it until
  // the pane is disposed. A new pane starts at the defaults, which is the
  // honest state — nothing has been dragged.
  const [treeWidth, setTreeWidth] = createSignal(260)
  const [requestWidth, setRequestWidth] = createSignal(520)

  // The pane's own width, so the maxima are the pane's rather than a number
  // somebody guessed. A pane 700px wide must not offer a 900px request half:
  // the response column would be clamped to nothing and its scroll would go
  // with it.
  const [paneWidth, setPaneWidth] = createSignal(1_000)
  let root: HTMLDivElement | undefined

  // The two sizes reach the grid as custom properties, set on the element
  // rather than written as a style prop: layout belongs in CSS (ADR-0014),
  // and what a person dragged to is a VALUE for that CSS to place, not a
  // rule. It is the same move `sidebar-width.ts` makes with --sidebar-width,
  // for the same reason and through the same seam.
  createEffect(() => {
    const el = root
    if (el === undefined) return
    el.style.setProperty('--api-tree-width', `${treeWidth()}px`)
    el.style.setProperty('--api-request-width', `${requestWidth()}px`)
  })

  onMount(() => {
    // jsdom has no ResizeObserver. The guard is not defensive dressing — the
    // workbench's own tests mount this component there, and the maxima fall
    // back to the initial numbers, which is exactly right for a box that
    // never changes size.
    if (root === undefined || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver((entries) => {
      const box = entries[0]?.contentRect
      if (!box) return
      setPaneWidth(box.width)
    })
    ro.observe(root)
    onCleanup(() => ro.disconnect())
  })

  /** The tree column's bounds. The ceiling leaves the request line enough
   *  room to still be a request line — a URL field squeezed to nothing is a
   *  worse answer than a drag that stops. */
  const treeMax = (): number => Math.max(MIN_TREE_WIDTH, paneWidth() - 420)
  /** The request half's bounds, against the same rule from the other side:
   *  what is left after the tree and this half still has to be wide enough
   *  to read a JSON line in. */
  const requestMax = (): number => Math.max(MIN_REQUEST_WIDTH, paneWidth() - treeWidth() - 360)

  // WHAT IS FOLDED. Two sections in one rail, each foldable, because a person
  // working in one of them wants the other out of the way — and the state is
  // local because it does not have to outlive the pane.
  const [collectionsOpen, setCollectionsOpen] = createSignal(true)
  const [environmentsOpen, setEnvironmentsOpen] = createSignal(true)

  // The menu a ROW opens, and which row opened it. One signal rather than a
  // menu per row: only one can be open, and a menu per row would be as many
  // popovers as the tree has entries.
  const [rowMenu, setRowMenu] = createSignal<{ x: number; y: number } | null>(null)
  /**
   * WHICH FOLDER the menu is about — the collection's handle, the path within
   * it and the name to say it by. `relPath` is '' for the collection's own
   * row, which is the same '' the wire calls the collection root (§13.1).
   *
   * ONE MENU FOR BOTH KINDS OF FOLDER, because a collection IS a folder
   * (design §6.1) and the acts are the same acts: put a request in it, put a
   * folder in it. A second list for the folder rows would be two owners of
   * one menu, agreeing for as long as anybody looked and diverging the day an
   * act was added to one — this repo's most recurrent defect, with the row
   * menu as the thing the two owners disagreed about. What differs is
   * `Close collection`, and it is ABSENT on a folder row rather than present
   * and refusing: there is no such act on a folder.
   *
   * A plain variable, not a signal, for the reason `requestMenuTarget` below
   * is one: the kit closes the menu BEFORE it calls the item's `onSelect`, so
   * anything read out of a signal at that moment is read after `onClose`
   * cleared it, and every item would act on nothing.
   */
  let rowMenuTarget: { handle: string; relPath: string; name: string; collection: boolean } | null =
    null
  /** Where a REQUEST's menu hangs, or null when it is closed. */
  const [requestMenu, setRequestMenu] = createSignal<{ x: number; y: number } | null>(null)
  /**
   * WHICH request that menu is about — the handle, the path and the name to
   * say it by.
   *
   * It used to be `store.selected()`, read at the moment an item fired, on
   * the argument that the header names the open request and a copy would be
   * a second owner of it. That was right while the header's ⋮ was the only
   * door. It is wrong now that a row has one: the row a person right-clicks
   * is very often NOT the one in the form, and a Delete that read the open
   * request would name one file in its question and remove another.
   *
   * So the target is what the DOOR aimed at, and `store.selected()` stays
   * the one owner of "the open request" — the header's door asks it for the
   * answer rather than keeping its own (`openRequestMenu`).
   *
   * A plain variable, for the reason `rowMenuHandle` above is one: the kit
   * closes the menu before it calls `onSelect`, and close is what clears the
   * signal.
   */
  let requestMenuTarget: RequestTarget | null = null
  /** Where a malformed file's menu hangs — its own menu, because what a file
   *  can do is not what a folder can do: Delete and Copy Absolute Path,
   *  the two acts that need no decoded request (api.request.delete never
   *  decodes; the path IS the file). It shares one discipline with the two
   *  menus above: only one can be open, so one signal and one target. */
  const [malformedMenu, setMalformedMenu] = createSignal<{ x: number; y: number } | null>(null)
  /**
   * WHICH malformed file the menu is about — the handle, the path within
   * the collection and the name to say it by. A plain variable rather than
   * a signal for the reason its two neighbours above are: the kit closes
   * the menu BEFORE it calls `onSelect`, so a value read out of a signal
   * at that moment is read out of a signal that close has already cleared.
   */
  let malformedMenuTarget: { handle: string; relPath: string; name: string } | null = null

  const [filter, setFilter] = createSignal('')
  const [menuOpen, setMenuOpen] = createSignal(false)
  const [menuAt, setMenuAt] = createSignal({ x: 0, y: 0 })

  // THE VARIABLE A PERSON CLICKED in the address, and where. Its own state
  // rather than the collections menu's: two menus that shared one open flag
  // would be two surfaces owning one input, and the second one to open would
  // close the first from underneath the pointer.
  const [varMenu, setVarMenu] = createSignal<{ name: string; x: number; y: number } | null>(null)

  const [curling, setCurling] = createSignal(false)
  const [curlLine, setCurlLine] = createSignal('')
  const [curlRefused, setCurlRefused] = createSignal('')
  const [converting, setConverting] = createSignal(false)
  // WHERE THE IMPORTED REQUEST WILL GO. The ask's answer, kept here while the
  // ask is open and handed to the store on Convert, which is what makes it
  // the draft's destination rather than a place that follows the person
  // around afterwards (api-store.ts, `draftFolder`).
  const [curlDest, setCurlDest] = createSignal('')

  // THE ENVIRONMENT PAGE'S DRAFT. It used to keep the file it read as well,
  // because two of its fields — the route and the secret names — could not be
  // edited and had to be carried back untouched. Both are the editor's now,
  // so the draft IS the answer and there is nothing to carry.
  // WHAT THE RIGHT HALF IS SHOWING — one value, not a flag per page. With
  // the folder page (nocx-8aczn.8) a second boolean would have made "the
  // environments are open" and "a folder is open" independently true, which
  // is this repo's most recurrent defect wearing a new hat: two owners of
  // one input. `envOpen` stays as a READER of it so every call site that
  // asks "is the request on screen" reads the one value.
  const [view, setView] = createSignal<'request' | 'environments' | 'folder'>('request')
  const envOpen = (): boolean => view() === 'environments'
  /** Put the request back on screen. Every act that fills the FORM ends
   *  here — making one, importing one, opening one — because a form nobody
   *  can see is a request that did not arrive. */
  const showRequest = (): void => {
    setView('request')
  }
  const [envDirty, setEnvDirty] = createSignal(false)
  const [envCreating, setEnvCreating] = createSignal(false)
  const [envEditing, setEnvEditing] = createSignal('')
  const [envRelPath, setEnvRelPath] = createSignal('')
  const [envName, setEnvName] = createSignal('')
  const [envRows, setEnvRows] = createSignal<readonly ValueRow[]>([])
  // The ROUTE is draft state exactly as the rows are: read with the file,
  // edited here, written back on Save. It used to be carried untouched
  // through `envBase` because nothing could change it — that was the half of
  // §6.5 the product did not have.
  const [envRoute, setEnvRoute] = createSignal<ApiRoute>({
    kind: 'direct',
    profileId: '',
    insecureTls: false,
  })
  const [envRefused, setEnvRefused] = createSignal('')
  const [envBusy, setEnvBusy] = createSignal(false)
  const [folderVariables, setFolderVariables] = createSignal<readonly ApiParam[] | null>(null)
  const [folderBusy, setFolderBusy] = createSignal(false)
  const [folderLoading, setFolderLoading] = createSignal(false)
  const [folderVariablesRefused, setFolderVariablesRefused] = createSignal('')
  const [folderSaveRefused, setFolderSaveRefused] = createSignal('')
  // Whether what is on screen has been written since it was last edited. It is
  // NOT "there is nothing left to write": a folder just read has been written
  // by nobody, and telling a person "Saved" about a file they have not touched
  // is a claim about an act that did not happen.
  const [folderWritten, setFolderWritten] = createSignal(false)
  let folderReadKey = ''
  createEffect(() => {
    if (view() !== 'folder') {
      // LEAVING FLUSHES. The pending write carries its own path, so it lands on
      // the folder it was typed into even though the page has moved on.
      writeFolderNow()
      folderReadKey = ''
      return
    }
    const handle = store.activeCollection()
    const relPath = store.activeFolder()
    const key = `${handle}:${relPath}`
    if (key === folderReadKey) return
    writeFolderNow()
    folderReadKey = key
    setFolderLoading(true)
    setFolderVariables(null)
    setFolderVariablesRefused('')
    setFolderSaveRefused('')
    setFolderWritten(false)
    void store.readFolderVariables(relPath).then((result) => {
      if (folderReadKey !== key) return
      setFolderLoading(false)
      setFolderVariablesRefused(result.error)
      if (result.variables === null) return
      setFolderVariables(result.variables)
    })
  })

  const [importing, setImporting] = createSignal(false)
  /**
   * THE ONE SOURCE THE ASK IS HOLDING, and the reason it is one signal.
   *
   * Four entrances answer the same question — the native Wails drop and the
   * system picker answer with a PATH on the machine running Go, a browser
   * drop and the kit's file input answer with a FILE holding the bytes, and
   * the paste box answers with the export's TEXT or with a URL. Exactly one
   * is held at a time and a new one visibly replaces the last (spec §2),
   * which is the wire's own rule reflected in the surface: an ask that could
   * hold two would have to decide which wins, and the loser would go on
   * being displayed.
   *
   * It was two signals — a path string and a File — and the pair carried
   * exactly that defect in miniature: two owners of one answer, with the
   * stale one winning by evaluation order unless every gesture remembered to
   * clear the other. A union cannot be in two states.
   *
   * The bytes matter because the backend is not always on the person's
   * machine: a path names a file where Go runs, and `make dev-web` is
   * documented as forwarding both ports over SSH (spec §1a).
   */
  const [postmanSource, setPostmanSource] = createSignal<HeldSource>({ kind: 'none' })
  /** What is in the paste box, verbatim. What it MEANS is
   *  `classifyPastedSource`'s answer and nobody else's (api-paths.ts). */
  const [postmanPasted, setPostmanPasted] = createSignal('')
  /** Why the pasted text is not a source, or ''. Said in the renderer
   *  because the backend would not refuse it — `parseImport` hands anything
   *  that does not start `{` or `[` to the CURL parser, so a shell command
   *  would come back as a collection minted from it, or as an error
   *  mentioning curl in a dialog that never offered curl (spec §2). */
  const [pasteRefused, setPasteRefused] = createSignal('')
  /** Whether the destination is open as a FIELD. False is the summary line;
   *  the pencil is what changes it, and `askForImport` puts it back. */
  const [editingDest, setEditingDest] = createSignal(false)
  /**
   * HOW THE FETCH TRAVELS, and it exists only because one of the four
   * entrances is a URL: that is the source the BACKEND goes and gets, so it
   * is the only one with a network between the person and the document. An
   * export served inside a network reachable only through a bastion was
   * askable for and unfetchable before this, and the collection it minted
   * would have carried `direct` into every request under it (nocx-zz3cy).
   *
   * Direct is the resting state and is spelled out rather than left as a
   * blank, so the picker and the call meet one spelling of one state — but
   * it is never SENT as a key: see `importPostman` below.
   */
  const [importRoute, setImportRoute] = createSignal<ApiRoute>(DIRECT_ROUTE)
  const [postmanDest, setPostmanDest] = createSignal('')
  const [importRefused, setImportRefused] = createSignal('')
  /**
   * Whether the person has TYPED into the destination.
   *
   * Both ends: false from the moment the ask opens until a keystroke reaches
   * that field, true from then until the ask is opened again. It is what
   * makes the proposal an offer rather than a correction — choosing a second
   * export re-proposes while nobody has said where the folder goes, and
   * never once somebody has.
   *
   * It is set by the field's own input handler and not by the proposal,
   * which is the whole reason the two can share the signal underneath: a
   * value this surface wrote is not a value the person chose.
   */
  const [destTyped, setDestTyped] = createSignal(false)
  const [importingBusy, setImportingBusy] = createSignal(false)
  /**
   * WHAT ONE IMPORT COULD NOT CARRY, said once, where the person is looking.
   *
   * This was a Section pinned under the collection tree (nocx-q2cx5). The
   * fact it carries is a soft degrade, so it may not live only in a log and
   * may not live inside the ask that closes — but neither of those requires
   * a permanent corner of the sidebar for something that belongs to a
   * moment (nocx-favvl).
   *
   * STICKY is what keeps the rule. A warning dismisses itself after eight
   * seconds, which for a degrade is a slower way of being invisible;
   * `duration: 0` ends it when the person ends it, which is what the panel's
   * dismiss control did.
   *
   * ONE REPORT AT A TIME, which is what a single panel was by construction.
   * The id of the standing one is held so the next import can end it: two
   * sticky toasts would leave one import's list beside another's until
   * somebody closed both.
   */
  let standingReport = 0
  const tellWhatWasNotImported = (about: string): void => {
    const notes = store.notes()
    if (notes.length === 0) return
    dismissToast(standingReport)
    standingReport = showToast({
      level: 'warning',
      duration: 0,
      message: `Not imported from ${about}: ${notes.map((n) => `${n.what} — ${n.why}`).join('; ')}`,
    })
  }
  /** Where a credential sits as TEXT in the request that is in the form —
   *  '' when there is none. See literalCredentialIn. */
  const pastedCredential = (): string => literalCredentialIn(store.draft())
  /**
   * What a folder ask says under its field about committing the folder.
   *
   * TWO STATES, and the second is shown only when it is TRUE of the request
   * in the form. A caveat on every ask is a caveat nobody reads, and the
   * ordinary case — a collection with nothing pasted in it — must not be made
   * frightening by a warning about somebody else's folder. Neither state
   * claims the folder is safe to commit: the first says only what nocx
   * actually guarantees, which is about values bound to variables.
   */
  const commitNote = (lead: string): string => {
    const header = pastedCredential()
    if (header === '') {
      return `${lead} A value bound to a variable stays in the vault: the file carries the name, never the value.`
    }
    return (
      `${lead} A value bound to a variable stays in the vault — but the request you have open ` +
      `carries a credential as text in its ${header} header, and saving it here writes that into the folder.`
    )
  }

  // The two asks. Each owns what is typed into it, the reason its last
  // attempt was refused, and whether a call is in flight.
  const [naming, setNaming] = createSignal(false)
  const [name, setName] = createSignal('')
  const [creating, setCreating] = createSignal(false)
  // The reason the LAST CREATE was refused, in the backend's words — read off
  // the store the moment the call settles rather than tracked reactively,
  // because `store.error()` is the last failure of ANY call and a listing that
  // failed an hour ago is not a sentence about the name being typed now.
  const [nameRefused, setNameRefused] = createSignal('')

  const [opening, setOpening] = createSignal(false)
  const [folderPath, setFolderPath] = createSignal('')
  const [openingFolder, setOpeningFolder] = createSignal(false)
  const [pathRefused, setPathRefused] = createSignal('')

  // ── The third ask: a FOLDER inside a collection (nocx-8v1fu) ───────────
  //
  // The same four pieces the two above have — what is typed, why the last
  // attempt was refused, whether a call is in flight — and one more, because
  // this ask is about a place: WHICH folder the new one goes in.
  //
  // The parent is captured when the ask OPENS rather than read when it
  // submits. The menu that opened it has already closed by then, and the row
  // it hung off may have moved under a listing that arrived in between; a
  // folder made in whatever the panel happened to be pointed at is the same
  // defect the request menu's target fixed one row over.
  const [foldering, setFoldering] = createSignal(false)
  const [folderName, setFolderName] = createSignal('')
  const [folderCreating, setFolderCreating] = createSignal(false)
  const [folderRefused, setFolderRefused] = createSignal('')
  const [folderIn, setFolderIn] = createSignal<{
    handle: string
    /** The EXISTING folder it goes in, '' being the collection's own root —
     *  the value that rides on the wire as `parentRelPath`. */
    parentRelPath: string
    /** What to call that place in the ask, so the question names where. */
    label: string
  } | null>(null)

  // ── The move-to-folder chooser (nocx-8aczn.2) ─────────────────────────
  //
  // ONE SIGNAL for open AND target, and that is structural, not tidy: the
  // kit's Dialog renders its `open` prop through one expression and the
  // chooser's rows through others, and Solid re-runs each expression only
  // when THE SIGNAL IT READS changes. A plain target variable read by the
  // row expressions made them evaluate ONCE — at first render, when the
  // target was null — and keep that empty answer forever, so opening the
  // chooser showed "Root of " and no folders. Everything the chooser shows
  // reads this one signal, so every expression re-runs together and the
  // open chooser is built from the target it opened with.
  const [moveAsk, setMoveAsk] = createSignal<RequestTarget | null>(null)
  const [moveBusy, setMoveBusy] = createSignal(false)
  const [moveRefused, setMoveRefused] = createSignal('')

  /** What the chooser's destination rows are: the folders of the collection
   *  holding the target, exactly as the store's last listing has them. The
   *  root is the chooser's own row, before them. */
  const moveFolders = (): readonly string[] => {
    const t = moveAsk()
    if (t === null) return []
    return store.collections().find((c) => c.handle === t.handle)?.collection.folders ?? []
  }
  const moveCollectionName = (): string => {
    const t = moveAsk()
    if (t === null) return ''
    const open = store.collections().find((c) => c.handle === t.handle)
    return open !== undefined && open.collection.name !== '' ? open.collection.name : ''
  }

  /** The folder the OPEN request lives in, for the crumb trail — the
   *  directory half of `selected()`'s path, or null when nothing is open
   *  or the request is at the collection's root. After a move it is the
   *  new path, which is how the header names where the request went. */
  /**
   * The folder segment of the trail: where the thing in the FORM lives, or
   * null when there is nothing to name.
   *
   * Read off the store's `draftFolder`, which is the one owner of that — it
   * is set to the request's own folder when one is opened, and to the answer
   * the curl ask gave when a fileless draft arrives. Deriving it here from
   * `selected` a second time left an imported request with no folder segment
   * at all, so between Convert and Save the destination existed and was
   * invisible; the trail is where a person looks to see it.
   */
  const openFolderPath = (): string | null => {
    if (store.draft() === null) return null
    const folder = store.draftFolder()
    return folder === '' ? null : folder
  }

  /** The request being moved, for the chooser's title — the SAME the menu
   *  aimed at, read off the one signal that also says whether it is open,
   *  so the title and the destination rows re-evaluate together. */
  const moveRequestName = (): string => moveAsk()?.name ?? ''

  /** Move into an existing folder: ONE store call, and the store says
   *  whether it landed. The refusal stays in the chooser, like every ask on
   *  this surface; the destination is the row the person picked. */
  const moveTo = (folderRelPath: string): void => {
    const t = moveAsk()
    if (t === null) return
    // The chooser offers FOLDERS; the wire takes two file paths, so the
    // destination file is the folder joined to the request's own name.
    // The RESULT is still the backend's word on where it landed — this
    // join only names where it is going.
    const base = t.relPath.slice(t.relPath.lastIndexOf('/') + 1)
    const toRelPath = folderRelPath === '' ? base : `${folderRelPath}/${base}`
    setMoveBusy(true)
    void store
      .moveRequest(t.handle, t.relPath, toRelPath)
      .then(() => {
        setMoveBusy(false)
        setMoveRefused(store.error())
        if (store.error() !== '') return
        setMoveAsk(null)
        revealFolder(t.handle, folderRelPath)
      })
      .catch(() => setMoveBusy(false))
  }
  /** Make a folder at the collection's root and move into it — the two
   *  acts a young collection needs, as one gesture. The create's refusal
   *  stays in the chooser; a create that landed is followed by the move,
   *  whose refusal is the move's. */
  const moveToNewFolder = (name: string): void => {
    const t = moveAsk()
    if (t === null) return
    setMoveBusy(true)
    void store.createFolder(t.handle, '', name).then(() => {
      setMoveRefused(store.error())
      if (store.error() !== '') {
        setMoveBusy(false)
        return
      }
      const base = t.relPath.slice(t.relPath.lastIndexOf('/') + 1)
      void store.moveRequest(t.handle, t.relPath, `${name}/${base}`).then(() => {
        setMoveBusy(false)
        setMoveRefused(store.error())
        if (store.error() !== '') return
        setMoveAsk(null)
        revealFolder(t.handle, name)
      })
    })
  }

  /**
   * Whether the folder ask still offers Browse.
   *
   * Both ends of the interval: it is true from the moment the surface is
   * built with a picker wired until that picker reports itself unavailable —
   * `-32601`, which is every `make dev-web` run and any build whose
   * `dialog.openDirectory` is not wired — and it never returns for the life
   * of the surface. A control that has refused once and stays on screen is
   * the broken-looking fallback this whole capability check exists to avoid;
   * the reason it gave is shown in the ask, so the control does not simply
   * vanish without a word.
   */
  const [pickerLive, setPickerLive] = createSignal(picker !== undefined)

  /** The same interval for the FILE picker, and its own signal: the two
   *  methods retire independently, so a build whose `dialog.openDirectory`
   *  is missing must not take the export's Browse down with it. */
  const [filePickerLive, setFilePickerLive] = createSignal(filePicker !== undefined)

  // ── The drag accelerator for the same move (nocx-9db1m) ───────────────
  //
  // The right-button menu's "Move to folder…" reaches `moveRequest` through
  // the chooser above; dragging a request row onto a folder row is the
  // ACCELERATOR — the same call, no dialog. The menu stays: it is the
  // keyboard-equivalent and the only door that names every destination.
  //
  // THE DROP GRAMMAR, decided here and written where each rule is made:
  //
  // ONTO A FOLDER ROW, NOT BETWEEN TWO ROWS. The tree is sorted — folders
  // before the requests beside them, parents before children, each list
  // in the order the backend emitted it (api-tree.ts) — so reordering is
  // not a concept: a request's position is its path, not its row index.
  // There is no `api.request.reorder` call, and a drop between rows would
  // have to mean one. Only folder rows (a collection or a directory) are
  // drop targets; a request row, a malformed row and an empty row are not.
  //
  // WHAT THE ROW UNDER THE POINTER SAYS. A folder that may take the drop
  // carries `data-drop-target="ok"`; every row that may not carries
  // `data-drop-target="no"`. A drag with no feedback is a drag people do
  // not trust, and a row that says nothing while a request hangs over it
  // leaves the person guessing whether the release will land.
  //
  // THE SCROLLED TREE'S EDGES. The tree container itself is never a drop
  // target — drops land on ROWS, not on gaps above the first or below the
  // last row, and there is no auto-scroll for a gesture the tree cannot
  // answer anyway. Reaching a folder off-screen means scrolling to it
  // first, which is what a person does with a mouse in hand.
  //
  // A DROP ONTO ANOTHER COLLECTION is refused, and it SAYS so. nocx-8aczn
  // put cross-collection out of the METHOD (api.request.move takes one
  // handle), not just out of the gesture, so the refusal is a sentence the
  // person sees — never a silent nothing.
  //
  // A DROP ONTO THE ROW IT CAME FROM, and onto the folder it is already
  // in, sends NO call and reports NO error. `api.request.move` refuses a
  // move to where it already is; sending a call we know will be refused is
  // a wasted round trip, and reporting an error for a gesture that was a
  // no-op is a lie. The gesture simply ends.
  //
  // THE SOURCE IS THE DATATRANSFER, not the row it left. The drop handler
  // reads `API_DRAG_MIME` off the event: the DataTransfer withholds its
  // DATA during a dragover (a target can only see the TYPES — the same
  // rule the tab strip's drag relies on), so the source that decides the
  // grammar on dragover lives in `dragSource`, and the source that ACTS at
  // the drop is re-read from the transfer, which is authoritative even if
  // the tree re-rendered under the gesture.
  const [dragSource, setDragSource] = createSignal<{
    handle: string
    relPath: string
  } | null>(null)
  /** Which row the pointer is over during a drag, and what the drop there
   *  would do: `ok` is a legal folder, `no` is anything else. Null when no
   *  drag is in flight. One signal, because only one row can be under the
   *  pointer at a time and a menu per row would be as many states as the
   *  tree has entries. */
  const [dropOver, setDropOver] = createSignal<{ key: string; verdict: 'ok' | 'no' } | null>(null)

  /** THE SINGLE GATE for both dragover and drop. One discriminated answer
   *  so a row cannot look droppable and then refuse, or look refused and
   *  then accept:
   *
   *  - `legal` — a folder row in the SAME collection, not the folder the
   *    request is already in. The drop moves the request.
   *  - `crossCollection` — a folder row in ANOTHER collection. nocx-8aczn
   *    put this out of the METHOD (api.request.move takes one handle), so
   *    the drop is refused and SAYS so.
   *  - `noOp` — a folder row that is the request's own directory: a move
   *    to where it already is would be refused by the backend, and this
   *    surface knows it before sending. The gesture simply ends.
   *  - `notFolder` — a request, malformed or empty row. The tree is
   *    sorted, so reordering is not a concept and these are not targets. */
  const dropVerdict = (
    source: { handle: string; relPath: string },
    row: ApiTreeRow,
  ): 'legal' | 'crossCollection' | 'noOp' | 'notFolder' => {
    if (row.kind !== 'collection' && row.kind !== 'dir') return 'notFolder'
    if (row.handle !== source.handle) return 'crossCollection'
    if (row.relPath === directoryOf(source.relPath)) return 'noOp'
    return 'legal'
  }

  /** Put the request's identity on the transfer so a REAL drop carries the
   *  source off the wire (and so the dragover grammar can tell this drag
   *  from an OS file drop, which carries `Files` and never this type). */
  const onDragStart = (e: DragEvent, row: ApiTreeRow): void => {
    if (row.kind !== 'request') return
    e.dataTransfer?.setData?.(
      API_DRAG_MIME,
      JSON.stringify({ handle: row.handle, relPath: row.relPath }),
    )
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
    setDragSource({ handle: row.handle, relPath: row.relPath })
  }

  /** The drag is over — wherever it landed. Both the source and the row
   *  under the pointer stop being states. */
  const onDragEnd = (): void => {
    setDragSource(null)
    setDropOver(null)
  }

  /** The row under the pointer says what a drop there would do. A LEGAL
   *  target accepts the drop (preventDefault is what allows it to fire)
   *  and a CROSS-COLLECTION folder accepts it too — not because the move
   *  is possible, but because the refusal must be SAID at the drop, and a
   *  dragover that never calls preventDefault never delivers the drop
   *  event. Every other row refuses on the way in and shows `no`, so the
   *  person is told before the release rather than at it. */
  const onDragOver = (e: DragEvent, row: ApiTreeRow): void => {
    const source = dragSource()
    if (source === null) return
    const verdict = dropVerdict(source, row)
    setDropOver({ key: row.key, verdict: verdict === 'legal' ? 'ok' : 'no' })
    if (verdict === 'legal' || verdict === 'crossCollection') e.preventDefault()
  }

  /** Leaving a row clears its verdict. The drag is still in flight; the
   *  next row under the pointer will say its own. */
  const onDragLeave = (row: ApiTreeRow): void => {
    setDropOver((prev) => (prev?.key === row.key ? null : prev))
  }

  /** THE DROP. The source is read off the DataTransfer — NOT from the
   *  `dragSource` signal — because the transfer is the one fact that did
   *  not change if the tree re-rendered under the gesture, and because a
   *  drag that carried no payload (an OS file, a corrupted transfer) is
   *  a no-op, never a move. The same gate dragover ran decides what the
   *  drop does. */
  const onDrop = (e: DragEvent, row: ApiTreeRow): void => {
    setDragSource(null)
    setDropOver(null)
    const raw = e.dataTransfer?.getData?.(API_DRAG_MIME)
    if (raw === undefined || raw === '') return
    let carried: { handle: string; relPath: string } | null = null
    try {
      const parsed: unknown = JSON.parse(raw)
      if (
        typeof parsed === 'object' &&
        parsed !== null &&
        'handle' in parsed &&
        typeof parsed.handle === 'string' &&
        'relPath' in parsed &&
        typeof parsed.relPath === 'string'
      ) {
        carried = { handle: parsed.handle, relPath: parsed.relPath }
      }
    } catch {
      carried = null
    }
    // A syntactically valid payload with the wrong shape is still a corrupt
    // transfer: no move without a handle and a path to move.
    if (carried === null) return
    const verdict = dropVerdict(carried, row)
    if (verdict === 'crossCollection') {
      showToast({ level: 'warning', message: CROSS_COLLECTION_REFUSAL })
      return
    }
    if (verdict !== 'legal') return
    // The same join the chooser makes: the folder the person dropped onto,
    // joined to the request's own name. The RESULT is still the backend's
    // word on where it landed.
    const base = carried.relPath.slice(carried.relPath.lastIndexOf('/') + 1)
    const toRelPath = row.relPath === '' ? base : `${row.relPath}/${base}`
    void store
      .moveRequest(carried.handle, carried.relPath, toRelPath)
      .then(() => {
        const refused = store.error()
        if (refused !== '') {
          // A drag has no field the refusal could belong to, so it is said
          // where the kit says outcomes are said: a toast.
          showToast({ level: 'danger', message: refused })
          return
        }
        revealFolder(carried.handle, row.relPath)
      })
      .catch(() => {
        // The store maps failures into error() and resolves; a rejection
        // here is unexpected but must not be silent.
        showToast({ level: 'danger', message: 'The request could not be moved.' })
      })
  }

  /**
   * The rows, narrowed by the filter.
   *
   * A FILTER SUSPENDS THE COLLAPSED SET, and that is the whole of why it is
   * read here rather than merged into it. The collapsed keys are what the
   * person folded away while browsing; a match three directories down inside
   * one of them is still a match, and a filter that answered "nothing found"
   * because the answer was inside a closed folder would be lying. The set is
   * untouched, so releasing the filter puts the tree back exactly as it was.
   */
  /** Whether an ask is on screen that owns whatever the last call refused.
   *  Each of these renders the reason under its own field. */
  const anyAskOpen = (): boolean =>
    naming() || opening() || foldering() || importing() || curling() || envOpen()

  const rows = (): ApiTreeRow[] =>
    flattenCollections(
      filterCollections(store.collections(), filter()),
      filter().trim() === '' ? collapsed() : NOTHING_COLLAPSED,
      filter().trim() !== '',
    )

  /** What a connection is CALLED, by the id a run reports. The id is the
   *  fact the backend hands back; the name is this window's, and it falls
   *  back to the id rather than to nothing — a run that went through a
   *  connection somebody has since deleted still went through it. */
  const connectionName = (profileId: string): string =>
    store.connections().find((c) => c.id === profileId)?.name ?? profileId

  /** What the right half is about: the collection the workbench is pointed
   *  at, by the name its manifest declares. */
  const activeCollectionName = (): string => {
    const open = store.collections().find((c) => c.handle === store.activeCollection())
    if (!open) return ''
    return open.collection.name !== '' ? open.collection.name : open.path
  }

  /** Every folder of the active collection, as the BACKEND lists them —
   *  never derived from the request paths, which is the derivation that
   *  loses a folder with nothing in it yet. The move chooser reads the same
   *  field of the same collection. */
  const activeCollectionFolders = (): readonly string[] =>
    store.collections().find((c) => c.handle === store.activeCollection())?.collection.folders ?? []

  /** Why an open collection listed as nothing. '' when it listed. */
  const errorOf = (handle: string): string =>
    store.collections().find((c) => c.handle === handle)?.error ?? ''

  const toggle = (key: string): void => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  /** Unfold a row, whatever state it was in. Walking into a folder from the
   *  PAGE has to leave the column agreeing with it — a folder a person just
   *  stepped into, drawn folded, is the tree contradicting the trail. */
  const expand = (key: string): void => {
    setCollapsed((prev) => {
      if (!prev.has(key)) return prev
      const next = new Set(prev)
      next.delete(key)
      return next
    })
  }

  /** The collection the pointer is on, or undefined while it is on none. */
  const activeOpenCollection = (): ApiOpenCollection | undefined =>
    store.collections().find((c) => c.handle === store.activeCollection())

  /** The last segment of a path — what a thing is CALLED, as against where
   *  it is. */
  const leafOf = (relPath: string): string => relPath.slice(relPath.lastIndexOf('/') + 1)

  /**
   * The rows of the folder page: what is in the folder the person is standing
   * in, in the words a record row renders.
   *
   * Read through the ONE owner of "what hangs under here" (api-tree.ts), so
   * the page and the column beside it can never disagree. The WORDS are
   * decided here rather than in the page, because how many things a folder
   * holds is a question about the listing and the page does not have one.
   */
  const here = (): FolderEntry[] => {
    const open = activeOpenCollection()
    if (open === undefined) return []
    const at = contentsOf(open.collection, store.activeFolder())
    const folders: FolderEntry[] = at.folders.map((relPath) => {
      const inside = contentsOf(open.collection, relPath)
      const count = inside.folders.length + inside.requests.length
      return {
        relPath,
        name: leafOf(relPath),
        kind: 'Folder',
        // A folder's line says whether going in is worth it. "Empty" rather
        // than "0 items", because that is the word the tree uses for the
        // same state one column to the left.
        meta: count === 0 ? 'Empty' : count === 1 ? '1 item' : `${count} items`,
        folder: true,
      }
    })
    const requests: FolderEntry[] = at.requests.map((ref) => ({
      relPath: ref.relPath,
      // The name the FILE declares, and its own basename when it declares
      // none — the rule the tree's rows follow, so one request cannot read
      // as two things in two places.
      name: ref.name !== '' ? ref.name : leafOf(ref.relPath),
      kind: ref.method !== '' ? ref.method : 'Request',
      // THE FILE, which the name is not: a collection is a folder in git and
      // the file is what a colleague sees in the diff. It is also the only
      // thing that tells two requests a person named the same apart.
      meta: leafOf(ref.relPath),
      folder: false,
    }))
    return [...folders, ...requests]
  }

  /**
   * What one row of the folder page can be, as controls standing on it.
   *
   * The acts are the ones the tree's menus fire — the same functions, not a
   * second spelling of them — and they stand on the row rather than behind a
   * ⋮ because this is a page and not the narrow column the tree is. Three
   * buttons fit; a menu here would be a click spent to reach them.
   */
  const entryActions = (entry: FolderEntry): JSX.Element => {
    const handle = store.activeCollection()
    if (entry.folder) {
      return (
        <>
          <IconButton
            size="sm"
            title="New request in this folder"
            ariaLabel={`New request in ${entry.name}`}
            onClick={() => void store.newRequest(entry.relPath).then(showRequest)}
          >
            <PlusIcon />
          </IconButton>
          <IconButton
            size="sm"
            title="New folder inside this one"
            ariaLabel={`New folder in ${entry.name}`}
            onClick={() => askForNewFolder(handle, entry.relPath, entry.name)}
          >
            <FolderIcon />
          </IconButton>
        </>
      )
    }
    const target: RequestTarget = { handle, relPath: entry.relPath, name: entry.name }
    return (
      <>
        <IconButton
          size="sm"
          title="Duplicate"
          ariaLabel={`Duplicate ${entry.name}`}
          onClick={() => duplicateRequest(target)}
        >
          <CopyIcon />
        </IconButton>
        <IconButton
          size="sm"
          title="Move to folder…"
          ariaLabel={`Move ${entry.name}`}
          onClick={() => askToMove(target)}
        >
          <FolderOpenIcon />
        </IconButton>
        <IconButton
          size="sm"
          title="Delete request…"
          ariaLabel={`Delete ${entry.name}`}
          onClick={() => askToDelete(target)}
        >
          <TrashIcon />
        </IconButton>
      </>
    )
  }

  /** Make a request where the person is standing, and show it. The store
   *  decides WHERE — `newRequest` with no folder named means "here" — and
   *  this only puts what it made on screen, which a folder page otherwise
   *  covers. */
  const newRequestHere = (): void => {
    void store.newRequest().then(showRequest)
  }

  const activate = (row: ApiTreeRow): void => {
    if (row.kind === 'request') {
      void store.openRequest(row.handle, row.relPath)
      showRequest()
      return
    }
    // A COLLECTION OR A FOLDER IS SOMETHING YOU OPEN, and it still folds.
    //
    // It only folded before, and folding is not an answer to either question
    // a person asks by clicking one: what is in here, and where am I now.
    // The trail above went on naming a request from somewhere else and the
    // plus beside it made its request somewhere else too, because nothing on
    // this surface held "the place" — `activeFolder` does now, and this is
    // the gesture that moves it (nocx-8aczn.8). Postman and Bruno both make
    // a folder openable for the same reason; the shape here is the one this
    // surface already has for the environments.
    if (row.kind === 'collection' || row.kind === 'dir') {
      store.enterFolder(row.handle, row.relPath)
      setView('folder')
      // OPENING NEVER CLOSES. The row toggled before, which is what a row
      // does when clicking it means nothing else — and once it means "open
      // this", a click that folded the thing away was the surface arguing
      // with the person: they asked to go in and the column shut. So it
      // unfolds, and folding is the disclosure's alone (it owns its click,
      // ui/tree-row.tsx). A second click on a row a person is already in is
      // then a no-op, which is the honest answer to asking for what you have.
      expand(row.key)
      return
    }
    // A malformed file has nothing to open — the row's own text is the whole
    // answer — and nothing else in this tree folds.
    if (row.expandable) toggle(row.key)
  }

  // A FRESH ASK STARTS EMPTY. Both dialogs are mounted for the life of the
  // surface, so without this the field still holds the last answer — an
  // offer nobody wrote, and one that Enter would submit straight back to a
  // backend that has just refused it as already there. A refusal does not
  // close the ask, so what was typed survives it.
  const askForName = (): void => {
    setName('')
    setNameRefused('')
    setNaming(true)
  }

  const askForFolder = (): void => {
    setFolderPath('')
    setPathRefused('')
    setOpening(true)
  }

  /**
   * Ask for a folder's NAME, for a place chosen before the ask opened.
   *
   * A fresh ask starts empty, exactly as the two above do: the dialog is
   * mounted for the life of the surface, so without this the field still
   * holds the last answer — an offer nobody wrote, and one Enter would submit
   * straight back to a backend that has just refused it as already there.
   */
  const askForNewFolder = (handle: string, parentRelPath: string, label: string): void => {
    setFolderName('')
    setFolderRefused('')
    setFolderIn({ handle, parentRelPath, label })
    setFoldering(true)
  }

  /**
   * Make it, and answer why it did not get made.
   *
   * The refusal STAYS IN THE ASK, holding what was typed — the rule
   * collection-dialog.tsx states for itself and the reason it exists. A folder
   * name is one path component (§13.1) and a folder that is already there is
   * refused rather than merged; both are sentences about what is in this
   * field, and this surface neither composes them nor sanitises the name to
   * avoid them. It reads `store.error()` the moment the call settles, for the
   * reason `nameRefused` gives: that signal is the last failure of ANY call,
   * so it is a sentence about this one only at this instant.
   *
   * On success everything above the new row is unfolded, so the answer to
   * "where did it go" is on screen rather than inside a folder the person had
   * folded away earlier.
   */
  /**
   * What the ask is titled: the place the folder is going, said the way that
   * place is addressed.
   *
   * At the collection's root that is the COLLECTION'S NAME, because a person
   * knows the folder they opened by its name and `''` is nothing to show
   * them. Inside a folder it is the PATH within the collection rather than
   * the row's leaf name: two folders called `users` in one collection are
   * ordinary, and a title naming only the leaf would be the same sentence for
   * both.
   */
  const folderAskTitle = (): string => {
    const place = folderIn()
    if (place === null) return 'New folder'
    return `New folder in ${place.parentRelPath === '' ? place.label : place.parentRelPath}`
  }

  const createFolder = (typed: string): void => {
    const place = folderIn()
    if (place === null) return
    setFolderCreating(true)
    void store.createFolder(place.handle, place.parentRelPath, typed).then(() => {
      setFolderCreating(false)
      setFolderRefused(store.error())
      if (store.error() !== '') return
      setFoldering(false)
      revealFolder(place.handle, place.parentRelPath)
      showToast({ level: 'success', message: `Created ${typed}` })
    })
  }

  /** Unfold everything between the collection's row and the folder that was
   *  just made. A new row inside a folded parent is a row nobody can see, and
   *  the collapsed set is this surface's own — the store cannot reach it. */
  const revealFolder = (handle: string, parentRelPath: string): void => {
    const keys = [`${handle}:`]
    let prefix = ''
    if (parentRelPath !== '') {
      for (const segment of parentRelPath.split('/')) {
        prefix = prefix === '' ? segment : `${prefix}/${segment}`
        keys.push(`${handle}:${prefix}`)
      }
    }
    setCollapsed((prev) => {
      const next = new Set(prev)
      for (const key of keys) next.delete(key)
      return next
    })
  }

  const createCollection = (typed: string): void => {
    setCreating(true)
    void store.createCollection(typed).then(() => {
      setCreating(false)
      setNameRefused(store.error())
      // The store has already put the collection in the list, open and
      // pointed at — `api.collections.create` answered an open's shape — so
      // there is nothing to fetch here and nothing to select. On a refusal
      // the dialog stays, holding the name and the reason.
      if (store.error() !== '') return
      setNaming(false)
      showToast({ level: 'success', message: `Created ${typed}` })
    })
  }

  /**
   * Put a folder in the tree, and answer why it did not go in.
   *
   * The one derivation of "open a collection folder", because there are two
   * callers and only ever one answer: the folder ask below, and the import,
   * which has just written a folder and must show it (nocx-vkp9d). It owns
   * NONE of the ask's state — not `opening`, not `openingFolder`, not
   * `pathRefused` — so an import that fails to open puts no reason inside a
   * dialog the person never opened.
   *
   * It answers '' when the folder is in the tree and the backend's sentence
   * otherwise, read off the store the moment the call settles for the reason
   * `nameRefused` gives above: `store.error()` is the last failure of ANY
   * call, so it is a sentence about this one only at this instant.
   */
  const putInTree = async (path: string): Promise<string> => {
    await store.openFolder(path)
    return store.error()
  }

  const openFolder = (path: string): void => {
    setOpeningFolder(true)
    void putInTree(path).then((refused) => {
      setOpeningFolder(false)
      setPathRefused(refused)
      if (refused !== '') return
      setOpening(false)
      showToast({ level: 'success', message: `Opened ${path}` })
    })
  }

  /**
   * Ask the platform for a folder and put it in the field.
   *
   * An EMPTY path is a cancellation, not an answer — the contract
   * `dialog.openFile` already keeps — and writing it into the field would
   * erase what the person typed as the price of changing their mind. A
   * rejection is the method reporting itself unavailable: the reason goes
   * where every other refusal goes and the control retires, because the next
   * click would refuse identically.
   */
  const browseInto = (accept: (path: string) => void, refused: (reason: string) => void): void => {
    if (!picker) return
    refused('')
    void picker().then(
      (chosen) => {
        if (chosen.path !== '') accept(chosen.path)
      },
      (err: unknown) => {
        setPickerLive(false)
        refused(err instanceof Error ? err.message : String(err))
      },
    )
  }

  const browseForFolder = (): void => browseInto(setFolderPath, setPathRefused)

  /** The same picker, for the folder an import is about to CREATE. It is the
   *  destination and not the export, because `dialog.openDirectory` chooses
   *  a directory — the export has a picker of its own over `dialog.openFile`
   *  (`browseForExport`), and the two capabilities retire independently. */
  const browseForImportDest = (): void => browseInto(setPostmanDest, setImportRefused)

  /**
   * PROPOSE WHERE THE COLLECTION LANDS, from the export's PLACE — the
   * picker's answer, a native drop's path, a browser drop's file.
   *
   * One derivation for all three, so that answering the ask one way cannot
   * propose a different folder from answering it another. It is here rather
   * than in the dialog because only this level knows both what was chosen
   * and the backend's default location. The paste box's two sources propose
   * from what they read instead — `info.name`, a URL's last segment — and
   * meet these three at `offerDestination`, which is where the rule about
   * not arguing with the person lives.
   *
   * A NAME does as well as a path — `proposedDestination` takes the basename
   * and then its stem, and a bare `acme.postman_collection.json` is already
   * both (api-paths.ts).
   */
  const proposeDestination = (nameOrPath: string): void => {
    offerDestination(
      proposedDestination(
        // A READ AT A MOMENT rather than a subscription — this runs from a
        // click, a picker's answer or a drop, never from a render — so it is
        // untracked: nothing here should re-run when the listing refreshes
        // and rewrite a field somebody is typing into.
        untrack(() => store.defaultRoot()),
        nameOrPath,
      ),
    )
  }

  /**
   * THE OFFER ITSELF, once, whichever of the three derivations made it — a
   * file's stem, a pasted export's `info.name`, a URL's last segment.
   *
   * Each is its own function in api-paths.ts because each reads a different
   * thing; what they share is the rule, and a second copy of it is how one
   * entrance starts arguing with a person the others leave alone. The rule:
   * an offer is skipped once somebody has typed a destination, because a
   * person who has said where the folder goes has said it.
   */
  const offerDestination = (proposal: string): void => {
    if (untrack(destTyped)) return
    if (proposal !== '') setPostmanDest(proposal)
  }

  /** Choose the EXPORT as a PLACE — picked with the system picker, or dropped
   *  into the Wails window, both of which answer with a path on the machine
   *  running Go, and both of which land in the field that used to be the
   *  question and is now only where the answer goes. */
  const chooseExport = (path: string): void => {
    // A path REPLACES whatever was held, and the paste box empties with it:
    // exactly one source at a time (spec §2), and a box still showing the
    // text it was holding would go on offering a source the ask has let go.
    setPostmanSource(path === '' ? { kind: 'none' } : { kind: 'path', path })
    clearPaste()
    proposeDestination(path)
  }

  /** Empty the paste box and its refusal, without touching the held source —
   *  the half every OTHER entrance performs when it takes the answer over. */
  const clearPaste = (): void => {
    setPostmanPasted('')
    setPasteRefused('')
  }

  /**
   * THE EXPORT AS TEXT, OR AS AN ADDRESS — the paste box, on every keystroke.
   *
   * What the text IS is `classifyPastedSource`'s answer and not a second one
   * made here (api-paths.ts): two derivations of "is this a URL" is the
   * `ssh`-without-a-space defect in another costume — they agree on every
   * case anybody tries and disagree on the one that matters.
   *
   * A BLANK BOX IS NOT A REFUSAL. It is a person who has cleared what they
   * pasted, and the only thing it takes back is the source the box itself
   * was holding: a file dropped a moment ago is not un-dropped by a
   * keystroke in another control.
   */
  const pasteSource = (text: string): void => {
    setPostmanPasted(text)
    const held = untrack(postmanSource)
    const fromPaste = held.kind === 'document' || held.kind === 'url'
    const source = classifyPastedSource(text)
    if (source.kind === 'unusable') {
      setPasteRefused(text.trim() === '' ? '' : NOT_AN_EXPORT_REFUSAL)
      if (fromPaste) setPostmanSource({ kind: 'none' })
      return
    }
    setPasteRefused('')
    // The last attempt's refusal belonged to the source it was refused
    // about. A new one is a new attempt, and leaving the old sentence up
    // would have it read as a verdict on this one.
    setImportRefused('')
    const root = untrack(() => store.defaultRoot())
    if (source.kind === 'url') {
      setPostmanSource({ kind: 'url', url: source.url })
      offerDestination(proposedDestinationFromURL(root, source.url))
      return
    }
    setPostmanSource({ kind: 'document', text: source.document })
    offerDestination(proposedDestinationFromDocument(root, source.document))
  }

  /** Let the held source go, whichever entrance it came through. The person
   *  who dropped the wrong file gets the ask back empty rather than having to
   *  drop the right one over it. */
  const clearSource = (): void => {
    setPostmanSource({ kind: 'none' })
    clearPaste()
    setImportRefused('')
    // The route belonged to the source that travelled. Letting it stand
    // would leave a connection chosen for a URL nobody is importing any
    // more, ready to ride out with the next one unasked.
    setImportRoute(DIRECT_ROUTE)
  }

  /**
   * THE EXPORT AS A DOCUMENT — a browser drop, or the kit's file input in the
   * region beside it, both of which yield `File` objects.
   *
   * The same gesture as `chooseExport` above and answered the same way: what
   * was chosen goes in the field, and the destination is proposed from it.
   * What differs is the currency — bytes rather than a location — and that is
   * the whole of the difference, because bytes reach a backend wherever it
   * runs while a path only names a file on the machine running Go.
   *
   * The source line shows the file's NAME. It is not a path and is never
   * sent as one: the route is chosen by what this gesture answered with, so
   * `importPostman` below spells `{ document }` and never `{ path }` while a
   * file is held. The name is there because it is what the person recognises
   * from their downloads folder, and because the destination is proposed from
   * its stem exactly as it is from a path's.
   */
  const chooseDocument = (files: File[]): void => {
    if (files.length > 1) {
      // The same rule and the same sentence as the native half below: one
      // import makes one collection, and N collections is N destinations.
      setImportRefused(MULTIPLE_EXPORTS_REFUSAL)
      return
    }
    const file = files[0]
    if (file === undefined) return
    setImportRefused('')
    setPostmanSource({ kind: 'file', file })
    clearPaste()
    proposeDestination(file.name)
  }

  const browseForExport = (): void => {
    if (!filePicker) return
    setImportRefused('')
    void filePicker().then(
      (chosen) => {
        // An EMPTY path is a cancellation, not an answer — writing it into
        // the field would erase what the person typed as the price of
        // changing their mind (browseInto says the rest).
        if (chosen.path !== '') chooseExport(chosen.path)
      },
      (err: unknown) => {
        setFilePickerLive(false)
        setImportRefused(err instanceof Error ? err.message : String(err))
      },
    )
  }

  // THE DROP, answered as the same gesture the picker already answers: it
  // calls `chooseExport`, so the export path and the proposed destination
  // are one code path rather than two that agree until they do not.
  //
  // Filtered by TARGET as well as by session: the local tab's terminal pane
  // is a drop surface of the same session, and the session alone cannot tell
  // the two apart (nocx-cx442).
  if (nativeDrop) {
    onCleanup(
      nativeDrop.subscribe((p) => {
        if (p.target !== API_IMPORT_DROP_TARGET) return
        // A drop that arrives while the ask is closed belongs to nobody: the
        // target only exists while it is open, so this is a stale delivery.
        if (!untrack(importing)) return
        if (p.sources.length > 1) {
          // One import makes one collection, and N collections is N
          // destinations — a different question, and not one this ask can
          // answer by guessing which of them was meant.
          setImportRefused(MULTIPLE_EXPORTS_REFUSAL)
          return
        }
        const path = p.sources[0]?.localPath
        // No path means the drop was minted rather than described — a remote
        // tab. Nothing here can read a ticket.
        if (path === undefined || path === '') return
        setImportRefused('')
        chooseExport(path)
      }),
    )
  }

  // The two import asks open the way the other two do: empty, with no
  // reason under the field, because a fresh ask holding the last answer is
  // an offer nobody wrote (askForName says the rest).
  const askForCurl = (): void => {
    setCurlLine('')
    setCurlRefused('')
    // A FRESH ASK OPENS WHERE THE PERSON IS. It is an offer, not a question:
    // somebody who imports a curl line while standing in `iaam` has already
    // said where it goes, and only somebody who disagrees answers this.
    setCurlDest(store.activeFolder())
    setCurling(true)
  }

  const askForImport = (): void => {
    setPostmanSource({ kind: 'none' })
    setPostmanPasted('')
    setPasteRefused('')
    // The destination opens as a SENTENCE, whatever the last ask left it as:
    // it is an offer, and an ask that opened with the field already out
    // would be asking the question the reshape removed.
    setEditingDest(false)
    setImportRoute(DIRECT_ROUTE)
    // Read on every open, the same reason `openEnvironments` reads them: a
    // person may have added the connection they are about to fetch through
    // since the panel started. Absent on a build with no profile store, and
    // the store's own method is what knows that (api-store.ts).
    void store.loadConnections()
    // OUR FOLDER, before anything is chosen. `proposedDestination` completes
    // this to <defaultRoot>/<stem> the moment a source is named, but until
    // then the field said nothing at all and its placeholder said
    // /work/acme-api — an arbitrary path rather than the place this product
    // keeps collections, which is the same place `Create` next door puts one
    // without asking (nocx-cx442).
    //
    // Written through the signal rather than through `onDest`, so it does
    // not set `destTyped`: the surface proposing a value is not the person
    // having said one, and a later pick must still be able to complete it.
    const root = store.defaultRoot()
    setPostmanDest(root === '' ? '' : `${root.replace(/[\\/]+$/, '')}/`)
    setDestTyped(false)
    setImportRefused('')
    setImporting(true)
  }

  /** Open the surface, on whatever is currently being sent under — the
   *  environment somebody came here about is nearly always that one. */
  /**
   * What answers a name, as the address field needs it.
   *
   * `unknown` is not a hedge: until the backend scope has been read there is
   * no answer to give for a name, and painting it as unanswered in that
   * window is how a person learns to ignore the colour.
   */
  const variableState = (name: string): 'bound' | 'secret' | 'unbound' | 'unknown' => {
    const answer: VariableAnswer = store.variableAnswer(name)
    switch (answer.scope) {
      case 'request':
      case 'folder':
      case 'environment':
        return 'bound'
      case 'secret':
        return 'secret'
      case 'none':
        return 'unbound'
      case 'unknown':
        return 'unknown'
    }
  }

  /** What the panel says about the variable that was clicked — one line,
   *  naming WHICH SCOPE answered, and never a secret's value: the renderer
   *  does not have it (ADR-0021) and says where it lives instead. */
  const variableHeader = (name: string): string => {
    const answer = store.variableAnswer(name)
    const env = store.activeEnvironment()
    switch (answer.scope) {
      case 'request':
        // Which scope answered is the half a person cannot see anywhere
        // else: the same name can be answered twice, and which one wins
        // decides what goes out.
        return `{{${name}}} = ${answer.value ?? ''} — this request's own`
      case 'folder':
        return `{{${name}}} = ${answer.value ?? ''} — from folder ${answer.from ?? ''}`
      case 'environment':
        return `{{${name}}} = ${answer.value ?? ''} — from ${environmentName(env)}`
      case 'secret':
        return `{{${name}}} = a secret, from the vault`
      case 'unknown':
        return `{{${name}}} — no scope has been read yet`
      case 'none':
        return env === ''
          ? `{{${name}}} — nothing answers it: not this request, and no environment is chosen`
          : `{{${name}}} — nothing answers it: neither this request nor ${environmentName(env)}`
    }
  }

  /**
   * What the person can DO about the variable they clicked.
   *
   * BOTH DOORS whenever nothing answers, because the choice is the point: a
   * value belonging to this one request — an id, a page — goes here, and one
   * every request under this environment shares goes there. A single "add"
   * would make that choice for them, and the wrong scope is exactly what
   * makes two requests fight over one name.
   */
  const variableMenuItems = () => {
    const name = varMenu()?.name ?? ''
    if (name === '') return []
    const answer = store.variableAnswer(name)
    const env = store.activeEnvironment()
    const defineHere = {
      id: 'api-variable-define-request',
      label: `Add ${name} to this request`,
      icon: PlusIcon,
      onSelect: () => defineOnRequest(name),
    }
    const defineThere = {
      id: 'api-variable-define',
      label:
        env === '' ? `Add ${name} to a new environment` : `Add ${name} to ${environmentName(env)}`,
      icon: PlusIcon,
      onSelect: () => defineVariable(name),
    }
    if (answer.scope === 'folder' && answer.from !== null) {
      return [
        {
          id: 'api-variable-open-folder',
          label: `Edit folder ${answer.from}`,
          icon: PencilIcon,
          onSelect: () => {
            setVarMenu(null)
            const handle = store.activeCollection()
            if (handle === '') return
            store.enterFolder(handle, answer.from as string)
            setView('folder')
          },
        },
      ]
    }
    if (answer.scope === 'request') return [defineThere]
    if (answer.scope === 'environment' || answer.scope === 'secret') {
      return [
        {
          id: 'api-variable-open-env',
          label: `Edit ${environmentName(env)}`,
          icon: PencilIcon,
          onSelect: () => {
            setVarMenu(null)
            openEnvironments()
          },
        },
        defineHere,
      ]
    }
    return [defineHere, defineThere]
  }

  /** Put the name in THIS REQUEST's variables — the scope that answers before
   *  the environment does. Into the DRAFT, which is what a person edits and
   *  what Save writes; a row that went anywhere else would be a value the
   *  form does not know it has. */
  const defineOnRequest = (name: string): void => {
    setVarMenu(null)
    const current = store.draft()
    if (current === null) return
    if (current.variables.some((v) => v.name === name)) return
    store.editDraft({
      ...current,
      variables: [...current.variables, { name, value: '', enabled: true }],
    })
  }

  /** The environment's own NAME, by the path the picker chose it under. */
  const environmentName = (relPath: string): string =>
    store.environments().find((e) => e.relPath === relPath)?.name ?? relPath

  /**
   * Where a secret made on the Auth tab would be bound — read from the same
   * two answers the WRITE uses, so the tab cannot name one environment while
   * `bindSecret` addresses another.
   *
   * Both absences are real states of this panel and neither is an error: a
   * converted curl line has no file until it is saved, and "No environment"
   * is a row a person can choose. The tab says which one it is; it does not
   * draw a control that would fail.
   */
  const secretTarget = (): SecretTarget => {
    if (store.selected() === null) return { kind: 'no-collection' }
    const relPath = store.activeEnvironment()
    if (relPath === '') return { kind: 'no-environment' }
    return { kind: 'environment', name: environmentName(relPath) }
  }

  /**
   * Give the auth variable its value — the store's method, not the client's.
   * The store is what knows which collection and which environment a binding
   * belongs to, and a form working that out for itself would be a second
   * answer to a question that already has one.
   *
   * `false` becomes a REJECTION here because the store answers a boolean and
   * keeps the reason in `error()`. Without the translation the field would
   * empty on a refusal, and a value that never landed would look stored.
   */
  const createSecret = async (variable: string, value: string): Promise<void> => {
    if (!(await store.bindSecret(variable, value))) {
      throw new Error(store.error() || 'The value was not stored.')
    }
  }

  /**
   * Open the environment editor with this name in it, ready to be given a
   * value — the path from the problem to the place it is fixed, in one
   * gesture from where the person typed it.
   *
   * A name the environment ALREADY answers is not added twice: the editor
   * opens on the row that is there. That is why this reads the rows it just
   * filled rather than appending blind.
   */
  const defineVariable = (name: string): void => {
    setVarMenu(null)
    setView('environments')
    void store.loadConnections()
    const current = store.activeEnvironment()
    // Through pickEnvironment's own parameter rather than after it: filling
    // the editor is a READ, so a row appended out here would race it and
    // sometimes be overwritten by the answer. Nothing in this surface may
    // depend on which of two callbacks ran first.
    if (current !== '') {
      pickEnvironment(current, name)
      return
    }
    if (store.environments().length === 0) newEnvironment()
    ensureRow(name)
  }

  /** Put a row for `name` in the editor if it has none. Never a second one:
   *  a name is a key, and two rows claiming it is a file that cannot say
   *  what it answers. */
  const ensureRow = (name: string): void => {
    setEnvRows((rows) =>
      rows.some((r) => r.name === name) ? rows : [...rows, { name, value: '', secret: false }],
    )
    setEnvDirty(true)
  }

  const openEnvironments = (): void => {
    setView('environments')
    // Read on every open: a person may have added the connection they are
    // about to route through since the panel started.
    void store.loadConnections()
    const current = store.activeEnvironment()
    if (current !== '') {
      pickEnvironment(current)
      return
    }
    if (store.environments().length === 0) newEnvironment()
  }

  const newEnvironment = (): void => {
    setEnvCreating(true)
    setEnvEditing('')
    setEnvRelPath('')
    setEnvName('')
    setEnvRows([])
    setEnvRoute({ kind: 'direct', profileId: '', insecureTls: false })
    setEnvRefused('')
    setEnvDirty(false)
  }

  /**
   * Load one environment into the editor.
   *
   * The READ comes first and the editor fills from what came back — never an
   * empty form that populates a moment later, which is a form somebody types
   * into before it has finished arriving.
   */
  const pickEnvironment = (relPath: string, ensure = ''): void => {
    void store.readEnvironment(relPath).then((env) => {
      if (!env) return
      setEnvCreating(false)
      setEnvEditing(relPath)
      setEnvRelPath(relPath)
      setEnvName(env.name)
      setEnvRows(toRows(env))
      setEnvRoute(env.route)
      setEnvRefused('')
      setEnvDirty(false)
      // `ensure` is how a person arrives here FROM a variable they clicked in
      // the address: the row they came to fill is put in by the same step
      // that fills the editor, so there is no window in which the read
      // overwrites it.
      if (ensure !== '') ensureRow(ensure)
    })
  }

  /** Throw the draft away and read the file again — the file is the truth,
   *  so "reset" is a read rather than a remembered copy. */
  const resetEnvironment = (): void => {
    if (envEditing() === '') return
    pickEnvironment(envEditing())
  }

  const saveEnvironment = (): void => {
    // Read here, not in the callback: a signal read inside a deferred
    // `.then` is a reactive read outside any tracked scope, and the value it
    // would see is whatever the field holds a tick later. What is being
    // saved is what the form held when Save was pressed.
    const named = envName().trim()
    const relPath = envRelPath().trim() !== '' ? envRelPath().trim() : environmentPath(named)
    if (relPath === '') return
    setEnvBusy(true)
    void store
      .writeEnvironment(relPath, {
        name: named,
        ...toStored(envRows()),
        route: envRoute(),
      })
      .then(() => {
        setEnvBusy(false)
        setEnvRefused(store.error())
        if (store.error() !== '') return
        // The surface STAYS OPEN and moves onto the saved file: this is a
        // page, and saving one environment is rarely the last thing somebody
        // came here to do. What closes it is Back.
        setEnvCreating(false)
        setEnvEditing(relPath)
        setEnvDirty(false)
        // The picker points at what was just written, so a person who makes
        // an environment is sending under it — not choosing it a second time
        // from a list where it has just appeared.
        store.setEnvironment(relPath)
        showToast({ level: 'success', message: `Saved ${named}` })
      })
  }

  const importCurl = (): void => {
    const line = curlLine().trim()
    if (line === '') return
    setConverting(true)
    void store.importCurl(line, curlDest()).then(() => {
      setConverting(false)
      setCurlRefused(store.error())
      // A converted curl line lands in the FORM — there is no file behind
      // it yet (api-store.ts) — so the ask closes and the request pane is
      // where the person looks next.
      if (store.error() !== '') return
      // WHOSE the list is: the request this line became, named by the
      // importer off the line itself. Called unconditionally — an import
      // that carried everything has an empty list and therefore says
      // nothing, so there is no state where this attributes a report that
      // was never raised.
      tellWhatWasNotImported(store.draft()?.name ?? 'a curl line')
      setCurling(false)
      // The form is what the person looks at next, and a folder page open
      // over it would hide the request they just made.
      showRequest()
    })
  }
  /**
   * FOLDER VARIABLES SAVE THEMSELVES (nocx-x3cax.7).
   *
   * There is no Save on that page. A folder's variables are three fields in a
   * table: nothing is composed and nothing is reviewed before committing, so a
   * person who types a value and walks away has already said everything they
   * mean to say.
   *
   * The write is COALESCED — a row typed character by character costs one — and
   * the pending one is FLUSHED when the folder changes or the page is left.
   * A debounce that loses the last edit because somebody clicked away would be
   * worse than the button it replaced, so the pending write carries the path
   * and the rows it was scheduled FOR, never whatever is current when the timer
   * happens to fire.
   */
  let folderSaveTimer: ReturnType<typeof setTimeout> | undefined
  let folderSavePending: { key: string; relPath: string; rows: readonly ApiParam[] } | null = null

  // A DECLARATION, not a const: the effect above calls it, and an effect body
  // that reached a `const` before its initialiser would be a temporal-dead-zone
  // throw the moment Solid ran effects in a different order.
  function writeFolderNow(): void {
    const pending = folderSavePending
    folderSavePending = null
    if (folderSaveTimer !== undefined) {
      clearTimeout(folderSaveTimer)
      folderSaveTimer = undefined
    }
    if (pending === null) return
    setFolderSaveRefused('')
    setFolderBusy(true)
    void store.writeFolderVariables(pending.relPath, pending.rows).then((result) => {
      // The answer belongs to the folder it was asked about. A person who moved
      // on before it landed is not shown another folder's outcome.
      if (folderReadKey !== pending.key) return
      setFolderBusy(false)
      setFolderSaveRefused(result.error)
      if (result.variables === null || result.error !== '') return
      setFolderVariables(result.variables)
      setFolderWritten(true)
    })
  }

  const setFolderRows = (variables: readonly ApiParam[]): void => {
    setFolderVariables(variables)
    setFolderWritten(false)
    folderSavePending = {
      key: folderReadKey,
      relPath: store.activeFolder(),
      rows: variables,
    }
    if (folderSaveTimer !== undefined) clearTimeout(folderSaveTimer)
    folderSaveTimer = setTimeout(() => {
      folderSaveTimer = undefined
      writeFolderNow()
    }, FOLDER_SAVE_DEBOUNCE)
  }

  /**
   * Import an export, and OPEN what it wrote.
   *
   * The open is half of the act, not a courtesy after it:
   * `api.collections.list` answers the folders that are open and the import
   * registers nothing, so an import that stopped at the disk left the person
   * naming — in the panel's other ask — the path that had been in the field
   * in front of them a second earlier (nocx-vkp9d).
   *
   * WHERE EACH FAILURE IS SAID, and why the two are said in different
   * places. A refused IMPORT keeps the ask, because the destination is what
   * has to change and the field holding it is still on screen with its own
   * validation slot under it. A refused OPEN cannot use that slot: the
   * import succeeded, so the ask has closed — leaving it up would invite a
   * second import into a folder that now exists — and a toast is the only
   * surface still there. It is a `danger` one carrying the open's own
   * sentence, because the person must never read "Imported into X" while X
   * is not in the tree.
   */
  /**
   * A URL, with the route it travels only when there IS one.
   *
   * The direct case omits the key rather than spelling `route: undefined`:
   * the Go side decodes strictly and an absent route already reads as
   * direct, so a key holding nothing is a third spelling of a state that has
   * two — and the one the decoder refuses.
   */
  const urlSource = (url: string): ImportSource => {
    const route = importRoute()
    return route.kind === 'connection' ? { url, route } : { url }
  }

  const importPostman = (): void => {
    const dest = postmanDest().trim()
    const held = postmanSource()
    if (held.kind === 'none' || dest === '') return
    setImportingBusy(true)
    // WHICH ROUTE IS DECIDED BY WHAT THE GESTURE ANSWERED WITH, never by
    // what kind of build this is: a held file goes as bytes, a named place
    // goes as a path, a pasted export goes as itself, an address goes as a
    // URL the backend fetches — and a person naming a file on the backend's
    // own machine gets the path route in a browser too (api-client.ts,
    // ImportSource).
    //
    // Reading the file can fail — it may have moved, or been revoked —
    // between the drop and the press, so the read is INSIDE the chain and
    // its refusal goes where the backend's own refusals go: under the field,
    // with the ask still open.
    const source: Promise<ImportSource> =
      held.kind === 'file'
        ? held.file.text().then((document) => ({ document }))
        : Promise.resolve(
            held.kind === 'path'
              ? { path: held.path }
              : held.kind === 'document'
                ? { document: held.text }
                : urlSource(held.url),
          )
    void source
      .then((chosen) => store.importPostman(chosen, dest))
      .then(async (): Promise<void> => {
        const refused = store.error()
        setImportRefused(refused)
        if (refused !== '') return
        // The folder is what a person can point at afterwards, so it is what
        // the report is about.
        tellWhatWasNotImported(dest)
        const notOpened = await putInTree(dest)
        setImporting(false)
        if (notOpened !== '') {
          showToast({
            level: 'danger',
            message: `Imported into ${dest}, but it is not in the tree: ${notOpened}`,
          })
          return
        }
        showToast({ level: 'success', message: `Imported into ${dest}` })
      })
      .catch((err: unknown) => {
        setImportRefused(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        setImportingBusy(false)
      })
  }

  /**
   * Open the collection menu under the control that asked for it.
   *
   * The point is the BUTTON's, read at the moment of the click, rather than
   * the pointer's: a menu that hangs off the mouse is right for a
   * right-click on a row and wrong for a control in a strip, where it should
   * appear in the same place whether it was reached by mouse or by keyboard
   * (Enter on a focused button reports a pointer position of 0,0).
   */
  /** Open a row's menu under the control that asked for it — the button's
   *  box, not the pointer, for the reason openMenu below gives. */
  const openRowMenu = (e: MouseEvent, row: ApiTreeRow): void => {
    e.stopPropagation()
    const box = (e.currentTarget as HTMLElement).getBoundingClientRect()
    aimRowMenu(row)
    setRowMenu({ x: box.left, y: box.bottom })
  }

  /** Point the folder menu at one folder. Both doors go through here — the
   *  ⋮ on a collection row and the right button on any folder row — so there
   *  is one place that can leave the target unset. */
  const aimRowMenu = (row: ApiTreeRow): void => {
    rowMenuTarget = {
      handle: row.handle,
      relPath: row.relPath,
      name: row.name,
      collection: row.kind === 'collection',
    }
  }

  /** Point the request menu at one request. Both doors go through here, so
   *  there is one place that can leave the target unset. */
  const aimRequestMenu = (handle: string, relPath: string, name: string): void => {
    requestMenuTarget = { handle, relPath, name }
  }

  /** The header's ⋮ — about the request in the form, whose identity is
   *  `store.selected()` and whose name is the draft's, because a rename that
   *  has not been saved is still the name a person would be asked about. */
  const openRequestMenu = (e: MouseEvent): void => {
    const open = store.selected()
    if (open === null) return
    const box = (e.currentTarget as HTMLElement).getBoundingClientRect()
    aimRequestMenu(open.handle, open.relPath, store.draft()?.name ?? '')
    setRequestMenu({ x: box.left, y: box.bottom })
  }

  /**
   * WHAT A REQUEST CAN BE — one list, built once, reached by two doors: the
   * right button on its row in the tree, and the ⋮ over the one in the form.
   *
   * Two hand-written lists would be this repo's most recurrent defect with a
   * menu as the thing the two owners disagreed about — they would agree for
   * as long as anyone looked, and diverge the day an act was added to one.
   *
   * Every item reads `requestMenuTarget` when it FIRES rather than when it is
   * built: the kit closes the menu before calling `onSelect`, so anything
   * read from a signal at build time is read from a signal that close has
   * already cleared.
   */
  /**
   * WHAT ONE REQUEST CAN BE — the acts themselves, as functions.
   *
   * Three doors reach them: the right button on a tree row, the ⋮ over the
   * request in the form, and the buttons on the folder page's rows. A menu
   * item holding an act inline and a button holding it again would be two
   * owners of one behaviour, agreeing until the day one of them grew a
   * confirmation the other did not.
   */
  const duplicateRequest = (target: RequestTarget): void => {
    void store.duplicateRequest(target.handle, target.relPath)
  }

  const askToMove = (target: RequestTarget): void => {
    // Captured at the door, for the reason every other target on this
    // surface is: the kit closes the menu before onSelect fires, and the
    // chooser's handlers read this instead of re-deriving it.
    setMoveRefused('')
    setMoveAsk(target)
  }

  const askToDelete = (target: RequestTarget): void => {
    // "are you sure" lives in this product. A delete removes a file from a
    // folder somebody shares through git, and the only undo is a working
    // tree they may not have committed. The question NAMES what goes,
    // because "are you sure" is a question about nothing — and it names the
    // row that was AIMED AT, which is the whole reason the target is not the
    // open request any more.
    void showConfirm(
      `Delete ${target.name}? The file is removed from the collection folder.`,
      'Delete',
    ).then((yes) => {
      if (yes) void store.deleteRequest(target.handle, target.relPath)
    })
  }

  const requestMenuItems = (): ContextMenuItem[] => {
    const items: ContextMenuItem[] = [
      {
        id: 'api-row-duplicate',
        label: 'Duplicate',
        icon: CopyIcon,
        onSelect: () => {
          const target = requestMenuTarget
          if (target !== null) duplicateRequest(target)
        },
      },
      {
        id: 'api-row-move',
        label: 'Move to folder…',
        icon: FolderOpenIcon,
        onSelect: () => {
          const target = requestMenuTarget
          if (target !== null) askToMove(target)
        },
      },
      {
        id: 'api-row-delete',
        label: 'Delete request…',
        icon: TrashIcon,
        onSelect: () => {
          const target = requestMenuTarget
          if (target !== null) askToDelete(target)
        },
      },
    ]
    // CLOSE IS ABOUT THE REQUEST IN THE FORM, and only about that one. The
    // list is reached by two doors and the other one aims at a ROW, which
    // may be any request in any open collection — a row nobody has opened
    // has nothing to close, and an item that did nothing there would be a
    // control that swallows the press.
    //
    // It exists at all because there was no way to put the form down: every
    // other client closes a tab, and this surface has one form by design
    // (one draft, one selection), so the act is one item rather than a strip
    // (nocx-8aczn.9).
    const open = store.selected()
    const aimed = requestMenuTarget
    if (
      open !== null &&
      aimed !== null &&
      open.handle === aimed.handle &&
      open.relPath === aimed.relPath
    ) {
      items.splice(items.length - 1, 0, {
        id: 'api-row-close',
        label: 'Close request',
        icon: CloseIcon,
        onSelect: closeOpenRequest,
      })
    }
    return items
  }

  /**
   * Put the form down.
   *
   * The store clears the form and touches no file; the ASK is here, where
   * this surface's other "are you sure" lives (the delete above). The
   * sentence is the store's — which of the two is true is read off
   * `selected`, which the store owns — and '' is how it says there is
   * nothing to lose, so nobody is asked a question about nothing.
   *
   * What is left on screen afterwards is the FOLDER the person is standing
   * in: the right half has to show something, and an empty request form says
   * less than the place they are still in.
   */
  const closeOpenRequest = (): void => {
    const question = store.closeQuestion()
    if (question === '') {
      void store.closeRequest().then(() => setView('folder'))
      return
    }
    void showConfirm(question, 'Discard and close', 'Cancel').then((yes) => {
      if (!yes) return
      void store.closeRequest().then(() => setView('folder'))
    })
  }

  /** Point the malformed file's menu at one file. One door, the right button
   *  on its row — the same plain-variable discipline as the two above. */
  const aimMalformedMenu = (row: ApiTreeRow): void => {
    malformedMenuTarget = { handle: row.handle, relPath: row.relPath, name: row.name }
  }

  /** The clipboard a copy lands on. The composition root injects one into
   *  the surfaces whose copy was designed with it (Files, Settings); the
   *  workbench predates that seam, so it makes its own through the same
   *  factory the other surfaces fall back to. */
  const copyMalformedPath = async (target: { handle: string; relPath: string }): Promise<void> => {
    const collection = store.collections().find((c) => c.handle === target.handle)
    const text = collection === undefined ? target.relPath : `${collection.path}/${target.relPath}`
    try {
      await clipboard.writeText(text)
      showToast({ level: 'success', message: 'Copied absolute path' })
    } catch (e) {
      showToast({ level: 'danger', message: e instanceof Error ? e.message : String(e) })
    }
  }

  /**
   * WHAT A MALFORMED FILE CAN BE — one list, and it is the whole point of
   * this row's menu: a file whose request will not decode is still a file,
   * and the two acts that need no decoded request are both real. The old
   * rule — "there is nothing to put in one of ours" — was written for a
   * menu of REQUEST acts. Delete and Copy Absolute Path are FILE acts.
   *
   * Every item reads `malformedMenuTarget` when it FIRES, for the reason
   * the request menu's items do: the kit closes the menu before it calls
   * `onSelect`, so anything read from a signal at build time is read from
   * a signal that close has already cleared.
   */
  const malformedMenuItems = (): ContextMenuItem[] => [
    {
      id: 'api-malformed-delete',
      label: 'Delete…',
      icon: TrashIcon,
      onSelect: () => {
        const target = malformedMenuTarget
        if (target === null) return
        // "are you sure" lives in this product, exactly as it does for a
        // request: a delete removes a file from a folder somebody shares
        // through git, and the only undo is a working tree they may not
        // have committed. The question NAMES what goes, because "are you
        // sure" is a question about nothing.
        void showConfirm(
          `Delete ${target.name}? The file is removed from the collection folder.`,
          'Delete',
        ).then((yes) => {
          if (yes) void store.deleteRequest(target.handle, target.relPath)
        })
      },
    },
    {
      id: 'api-malformed-copy-path',
      label: 'Copy Absolute Path',
      icon: CopyIcon,
      onSelect: () => {
        const target = malformedMenuTarget
        if (target === null) return
        void copyMalformedPath(target)
      },
    },
  ]

  /**
   * WHAT A FOLDER CAN BE — one list, built once, reached by two doors: the ⋮
   * beside a collection's row and the right button on any folder row,
   * collections included.
   *
   * Every item reads `rowMenuTarget` when it FIRES rather than when it is
   * built, for the reason the request menu's items do: the kit closes the
   * menu before it calls `onSelect`, so anything read from a signal at build
   * time is read from a signal that close has already cleared.
   */
  const rowMenuItems = (): ContextMenuItem[] => {
    const items: ContextMenuItem[] = [
      {
        id: 'api-row-new-request',
        label: 'New request',
        icon: PlusIcon,
        onSelect: () => {
          const target = rowMenuTarget
          if (target === null) return
          // POINTED AT FIRST, because the row a person aimed at is very often
          // not the collection the panel was working in — and then the folder
          // within it, which is '' on a collection's own row and is exactly
          // what the allocator wants for "the collection's root".
          store.pointAt(target.handle)
          void store.newRequest(target.relPath)
        },
      },
      {
        id: 'api-row-new-folder',
        label: 'New folder…',
        // The kit has a folder glyph and no folder-plus one; the `+` is on
        // the sibling item, and what distinguishes these two is the word.
        icon: FolderIcon,
        onSelect: () => {
          const target = rowMenuTarget
          if (target === null) return
          askForNewFolder(target.handle, target.relPath, target.name)
        },
      },
    ]
    // ABSENT rather than present and refusing, which is this surface's rule
    // for every door: there is no act called "close" on a folder inside a
    // collection — the collection is what the app has open (design §6.1).
    if (rowMenuTarget?.collection === true) {
      items.push({
        id: 'api-row-close',
        // "Close collection", not the path. The row this menu hangs off
        // already says WHICH collection, and a path elided in the middle
        // answers neither question — it is not readable and it is not needed.
        label: 'Close collection',
        icon: CloseIcon,
        onSelect: () => {
          const target = rowMenuTarget
          if (target !== null) void store.closeFolder(target.handle)
        },
      })
    }
    return items
  }

  const openMenu = (e: MouseEvent): void => {
    const box = (e.currentTarget as HTMLElement).getBoundingClientRect()
    setMenuAt({ x: box.left, y: box.bottom })
    setMenuOpen(true)
  }

  return (
    <div class="api-workbench" ref={root}>
      <aside class="api-workbench__tree">
        {/* THE COLLECTIONS BAR. Two actions and each still asks — nothing
            about that changed (nocx-84shs) — but they are icons in a strip
            now rather than two full-width buttons stacked above the tree.
            The reason is what the panel is FOR: the tree is the surface a
            person works in all day and the two doors are used once per
            collection, so the doors were taking the height from the thing
            that needs it. A strip of icon actions beside the heading is the
            shape the Files panel and every editor sidebar already use, and
            the accessible names are unchanged. */}
        {/* THE BAR: what it is, whether it is still following the disk,
            and the two controls. The badge sits HERE rather than in a header
            of its own — it says whether this tree is current, and a row
            spanning the whole pane to carry one badge was taking a line from
            both columns to speak for one. */}
        {/* THE FILTER. A field rather than a search icon that reveals one:
            the column is a column, one row is what it costs, and a control
            that has to be found before it can be used is a control most
            people never learn is there. It narrows the INPUT to the tree
            (api-tree.ts) rather than hiding rendered rows, so what a person
            sees under a filter is a real tree — depths, directories and all
            — of exactly the requests that matched. */}
        <div class="api-tree__filter">
          <TextField
            id="api-filter"
            ariaLabel="Filter collections and requests"
            placeholder="Filter…"
            value={filter()}
            onInput={setFilter}
          />
        </div>

        {/* EVERYTHING ELSE, BEHIND ONE CONTROL. Opening a folder somebody
            else made, importing an export, and re-reading the disk are all
            things a person does occasionally and deliberately; each had a
            control of its own in the column, and between them they were
            taking more of the sidebar than the tree. Refresh comes in here
            with them — it was in a header of its own, which is where it went
            when it was the only action the panel had. */}
        <ContextMenu
          open={menuOpen()}
          x={menuAt().x}
          y={menuAt().y}
          data-testid="api-collections-menu-popover"
          onClose={() => setMenuOpen(false)}
          items={[
            {
              id: 'api-menu-new-collection',
              label: 'New collection…',
              icon: PlusIcon,
              onSelect: askForName,
            },
            {
              id: 'api-menu-open',
              label: 'Open folder…',
              icon: FolderOpenIcon,
              onSelect: askForFolder,
            },
            {
              id: 'api-menu-import',
              label: 'Import collection…',
              icon: ArrowDownIcon,
              onSelect: askForImport,
            },
            {
              id: 'api-menu-refresh',
              label: 'Re-read the open folders',
              icon: RefreshIcon,
              onSelect: () => void store.refresh(),
            },
          ]}
        />
        {/* What a REQUEST can be — one menu, mounted once, opened by either
            door: the right button on a row, or the ⋮ over the open one. It
            is one element rather than one per door for the same reason it is
            one item list: two would be two surfaces owning one popover, and
            the second to open would close the first from under the pointer.
            Deleting is a menu item rather than a control because it takes
            something away, so it has to be read and chosen. */}
        <ContextMenu
          open={requestMenu() !== null}
          x={requestMenu()?.x ?? 0}
          y={requestMenu()?.y ?? 0}
          data-testid="api-request-row-menu"
          onClose={() => setRequestMenu(null)}
          items={requestMenuItems()}
        />

        {/* What a FOLDER row can do — a collection's own row included, because
            a collection is a folder (§6.1). One menu, mounted once, opened by
            either door, for the reason the request menu is one: two would be
            two surfaces owning one popover, and the second to open would
            close the first from under the pointer. Closing is a menu item
            rather than a control on the row because it is the one act that
            takes something away, and here it has to be read and chosen. */}
        <ContextMenu
          open={rowMenu() !== null}
          x={rowMenu()?.x ?? 0}
          y={rowMenu()?.y ?? 0}
          data-testid="api-folder-row-menu"
          onClose={() => setRowMenu(null)}
          items={rowMenuItems()}
        />

        {/* What a MALFORMED file can do — its own menu, for the same reason
            the folder's is one list and not a second copy of the request's:
            a file whose request will not decode is still a FILE, and the two
            acts that need no decoded request — delete it, copy its path —
            are real. The old rule ("there is nothing to put in one of
            ours") was written for a menu of request acts; there was, it
            turned out, a menu of file acts all along. */}
        <ContextMenu
          open={malformedMenu() !== null}
          x={malformedMenu()?.x ?? 0}
          y={malformedMenu()?.y ?? 0}
          data-testid="api-malformed-row-menu"
          onClose={() => setMalformedMenu(null)}
          items={malformedMenuItems()}
        />
        {/* WHAT THIS VARIABLE IS, where it was clicked. A menu rather than a
            panel of its own: the kit already owns "a small thing anchored at
            a point that dismisses itself", including the Escape, the
            outside-click and the focus return, and a second surface with its
            own copy of those would be the two-owners defect with a popover
            on top. The header carries the fact and the row carries the one
            action there is. */}
        <ContextMenu
          open={varMenu() !== null}
          x={varMenu()?.x ?? 0}
          y={varMenu()?.y ?? 0}
          header={varMenu() ? variableHeader(varMenu()?.name ?? '') : undefined}
          data-testid="api-variable-menu"
          onClose={() => setVarMenu(null)}
          items={variableMenuItems()}
        />
        <CollectionDialog
          open={naming()}
          title="New collection"
          submitLabel="Create"
          fieldId="api-new-collection-name"
          fieldLabel="Name"
          fieldDescription={commitNote(
            'A name, not a path — the folder is made where nocx keeps collections.',
          )}
          placeholder="orders-api"
          value={name()}
          onInput={setName}
          error={nameRefused()}
          busy={creating()}
          onCancel={() => setNaming(false)}
          onSubmit={createCollection}
        />
        <CollectionDialog
          open={opening()}
          title="Open a collection folder"
          submitLabel="Open"
          fieldId="api-collection-path"
          fieldLabel="Collection folder"
          fieldDescription={commitNote('The folder you place.')}
          placeholder="/work/acme-api"
          value={folderPath()}
          onInput={setFolderPath}
          error={pathRefused()}
          busy={openingFolder()}
          onBrowse={pickerLive() ? browseForFolder : undefined}
          onCancel={() => setOpening(false)}
          onSubmit={openFolder}
        />
        {/* THE THIRD ASK, and the same component as the other two on purpose:
            "ask for one string about a new thing" is one question, and a
            second component for it would be the two-owners defect in
            miniature (collection-dialog.tsx says so at length). It stays open
            when the backend refuses and renders the reason under the field,
            which is the whole of the criterion about a name being refused on
            the backend's terms rather than sanitised away here. */}
        <CollectionDialog
          open={foldering()}
          title={folderAskTitle()}
          submitLabel="Create folder"
          fieldId="api-new-folder-name"
          fieldLabel="Name"
          fieldDescription="One folder, one name — not a path. A folder inside this one is made by asking again from its own row."
          placeholder="reports"
          value={folderName()}
          onInput={setFolderName}
          error={folderRefused()}
          busy={folderCreating()}
          onCancel={() => setFoldering(false)}
          onSubmit={createFolder}
        />
        <MoveToFolderDialog
          open={moveAsk() !== null}
          requestName={moveRequestName()}
          collectionName={moveCollectionName()}
          folders={moveFolders()}
          error={moveRefused()}
          busy={moveBusy()}
          onCancel={() => setMoveAsk(null)}
          onMove={moveTo}
          onNewFolderAndMove={moveToNewFolder}
        />
        <PostmanImportDialog
          open={importing()}
          pasted={postmanPasted()}
          onPaste={pasteSource}
          pasteRefusal={pasteRefused()}
          sourceLabel={sourceLabel(postmanSource())}
          onClearSource={clearSource}
          sourceIsURL={postmanSource().kind === 'url'}
          connections={store.connections()}
          route={importRoute()}
          onRoute={setImportRoute}
          file={sourcePath(postmanSource())}
          dest={postmanDest()}
          editingDest={editingDest()}
          onEditDest={() => setEditingDest(true)}
          onFile={chooseExport}
          onDest={(value) => {
            setDestTyped(true)
            setPostmanDest(value)
          }}
          defaultRoot={store.defaultRoot()}
          dropSession={nativeDrop?.session() ?? null}
          // Whether the WINDOW takes drops natively — a different question from
          // whether a session is open for one to be attributed to. The
          // capability IS the answer: the composition root builds this port
          // only where there is a Wails runtime (main.tsx), so no surface here
          // asks that runtime a second time.
          nativeWindow={nativeDrop !== undefined}
          onFiles={chooseDocument}
          error={importRefused()}
          busy={importingBusy()}
          onBrowseFile={filePickerLive() ? browseForExport : undefined}
          onBrowse={pickerLive() ? browseForImportDest : undefined}
          onCancel={() => setImporting(false)}
          onSubmit={importPostman}
        />
        <CurlImportDialog
          open={curling()}
          value={curlLine()}
          onInput={setCurlLine}
          error={curlRefused()}
          busy={converting()}
          onCancel={() => setCurling(false)}
          onSubmit={importCurl}
          dest={curlDest()}
          onDest={setCurlDest}
          folders={activeCollectionFolders()}
          collectionName={store.activeCollection() === '' ? '' : activeCollectionName()}
        />
        {/* ONE REFUSAL, ONE PLACE. `store.error()` is the last failure of any
            call, and when an ask is on screen that ask is already showing it
            under the field the person typed into. Rendering it here as well
            put the same sentence twice on one screen — and the copy that was
            not beside the field was the one that could not be acted on. */}
        <Show when={store.error() !== '' && !anyAskOpen()}>
          <StatusCard
            tone="danger"
            title="That did not work"
            description={store.error()}
            action={<Button onClick={() => void store.refresh()}>Re-read the open folders</Button>}
          />
        </Show>
        {/* The watch did not come up, so the tree has stopped following the
            disk. It is a WARNING rather than a danger — everything on screen
            is still true, it has simply stopped being kept true — and it is a
            state with the one action for it, which is what StatusCard is. Not
            a toast: a toast cannot answer "why is this stale?" ten minutes
            later. Refresh is the retry, because it re-sends the watch set. */}
        <Show when={store.watchFailed() !== ''}>
          <StatusCard
            tone="warning"
            title="Not watching these folders"
            description={`${store.watchFailed()} — changes on disk will not appear on their own until this recovers.`}
            action={<Button onClick={() => void store.refresh()}>Retry</Button>}
          />
        </Show>

        {/* THE SECTION'S OWN PAIR: make one, and everything else. The same
            two controls stand on the environments section below and on every
            collection row, and that sameness is the point — one grammar for
            "the thing this line is about", wherever the line is. */}
        <Section
          id="api-collections"
          title="Collections"
          dense
          collapsible
          open={collectionsOpen()}
          onToggle={() => setCollectionsOpen((was) => !was)}
          actions={
            <>
              {/* The kit's badge, not a second one. `local` is
                  unconditionally true because a collection folder is
                  backend-LOCAL (§13.1) — the watch binding is opened against
                  a local session on purpose, so there is no remote case here
                  whose designed mode is polling. */}
              <WatchBadge
                testId="api-polling-badge"
                mode={store.watchMode()}
                reason={store.watchDegradedReason()}
                local
              />
              <IconButton
                id="api-new-collection"
                size="sm"
                title="New collection"
                ariaLabel="New collection"
                onClick={askForName}
              >
                <PlusIcon />
              </IconButton>
              <IconButton
                id="api-collections-menu"
                size="sm"
                title="More collection actions"
                ariaLabel="More collection actions"
                selected={menuOpen()}
                onClick={openMenu}
              >
                <MoreIcon />
              </IconButton>
            </>
          }
        >
          <div
            class="api-tree"
            role="tree"
            aria-label="Collections"
            /* THE SCROLLED TREE'S EDGES (nocx-9db1m): the container itself
               is never a drop target, and a drag over the gap above the
               first row or below the last must clear the last row's
               verdict rather than leave it glowing. `target ===
               currentTarget` is the gap: a dragover on a row has the row
               as its target and bubbles here with a different one. */
            onDragOver={(e: DragEvent) => {
              if (e.target === e.currentTarget) setDropOver(null)
            }}
          >
            <Show
              when={rows().length > 0}
              fallback={
                <Show
                  when={filter().trim() === ''}
                  fallback={
                    <EmptyState
                      title="Nothing matches"
                      description={`No collection or request contains “${filter().trim()}”.`}
                    />
                  }
                >
                  <EmptyState
                    title="No collections open"
                    description="Make one, open a folder you already have, or import a Postman export."
                  />
                </Show>
              }
            >
              <For each={rows()}>
                {(row) => (
                  <Show
                    when={row.kind !== 'empty'}
                    /* The kit's own answer to "this folder holds nothing"
                       (ui/tree-empty.tsx says why it is not a row). The
                       surface places it and paints none of it. */
                    fallback={<TreeEmpty depth={row.depth} />}
                  >
                    <div
                      class="api-tree__row"
                      data-rel-path={row.kind === 'request' ? row.relPath : undefined}
                      data-row-key={row.key}
                      /* THE DRAG ACCELERATOR (nocx-9db1m). A request row is
                         draggable; a folder row is a drop target. The
                         grammar — which rows take a drop, what the row
                         under the pointer says, the scrolled edges, the
                         cross-collection refusal, the no-op drops — is
                         decided in the drag handlers above, and the
                         verdict (`data-drop-target`) is the ONE answer both
                         dragover and drop use. The source rides the
                         DataTransfer, not the DOM. */
                      draggable={row.kind === 'request'}
                      data-drop-target={
                        dropOver()?.key === row.key ? dropOver()?.verdict : undefined
                      }
                      onDragStart={(e: DragEvent) => onDragStart(e, row)}
                      onDragEnd={onDragEnd}
                      onDragOver={(e: DragEvent) => onDragOver(e, row)}
                      onDragLeave={() => onDragLeave(row)}
                      onDrop={(e: DragEvent) => onDrop(e, row)}
                      onClick={() => activate(row)}
                      /* THE RIGHT BUTTON, which this tree answered by handing
                       the webview's own menu — reload, save image as — to a
                       person who had asked what they could do with a
                       request. The kit's ContextMenu says in its own first
                       line that this is what it is for, and the Files panel
                       (files-view.tsx) and the tab strip (tab.tsx) both
                       wire exactly this. The point is the POINTER's here,
                       unlike the control in the strip: a menu a person
                       opened by aiming should appear where they aimed. */
                      onContextMenu={(e: MouseEvent) => {
                        if (row.kind === 'request') {
                          e.preventDefault()
                          aimRequestMenu(row.handle, row.relPath, row.name)
                          setRequestMenu({ x: e.clientX, y: e.clientY })
                          return
                        }
                        if (row.kind === 'malformed') {
                          e.preventDefault()
                          aimMalformedMenu(row)
                          setMalformedMenu({ x: e.clientX, y: e.clientY })
                          return
                        }
                        if (row.kind === 'collection' || row.kind === 'dir') {
                          e.preventDefault()
                          aimRowMenu(row)
                          setRowMenu({ x: e.clientX, y: e.clientY })
                        }
                        /* AN UNREADABLE FILE WAS ONCE LEFT WITH THE PLATFORM'S
                         menu, on the reasoning that there is nothing to put
                         in one of ours. That was written for a menu of
                         REQUEST acts — Duplicate, Rename, Send — all of
                         which need a decoded request. It is false for acts
                         on a FILE: delete and copy-path need no decode, so
                         a malformed row is right-clickable like every other
                         row, and the row above it now carries a menu. */
                      }}
                    >
                      <TreeRow
                        name={row.name}
                        depth={row.depth}
                        kind={row.kind === 'request' ? 'regular' : rowKind(row)}
                        /* WHAT THE MARK MEANS, per kind. A collection row is
                         marked when it is the one new requests go into; a
                         request row is marked when it is the one in the form
                         — the fact the header states as `Playground › test`
                         and the tree did not state at all, in a list where
                         two rows are routinely one word apart. The mark is
                         the kit's own (`data-selected`), the same vocabulary
                         the environment list below uses. It follows
                         `store.selected()`, which is the ONE owner of "the
                         open request": a curl import detaches the form from
                         every file and nulls it, and the tree then marks
                         nothing rather than the row it used to be. */
                        selected={
                          row.kind === 'collection'
                            ? row.handle === store.activeCollection()
                            : row.kind === 'request' &&
                              store.selected()?.handle === row.handle &&
                              store.selected()?.relPath === row.relPath
                        }
                        /* THE REASON A ROW IS WHAT IT IS, on hover. A
                         malformed file's own words are the decoder's, not a
                         person's — `malformedReason` rewrites them — and a
                         collection whose listing failed says why in the
                         listing's own words (already written for a person,
                         api.collections.list's `error`). Both used to be
                         red paragraphs in the document flow, which the kit
                         forbids: a message about an action does not live in
                         the document flow. The row's `title` is the seam
                         the kit provides. */
                        hint={
                          row.reason !== ''
                            ? malformedReason(row.reason)
                            : row.kind === 'collection' && errorOf(row.handle) !== ''
                              ? errorOf(row.handle)
                              : undefined
                        }
                        expanded={row.expanded}
                        onToggle={() => toggle(row.key)}
                        badge={
                          <>
                            <Show when={row.method !== ''}>
                              <Badge tone="neutral">{row.method}</Badge>
                            </Show>
                            {/* THE ROW'S OWN PAIR, the same one the section
                            heading above it carries: make one, and everything
                            else. It replaced a bare ✕ on the row — one
                            unlabelled click from closing a folder, with
                            nothing between the pointer and the act. Closing
                            is a menu item now, where a destructive thing has
                            to be chosen rather than brushed past. */}
                            {/* A REQUEST ROW CARRIES NO ACTIONS OF ITS OWN.
                            Duplicate was drawn here first and looked wrong,
                            and the reason it looked wrong is the reason the
                            kit has a menu: a row is a NAME in a list that is
                            already narrow, and an always-drawn control on it
                            competes with the one thing the row exists to
                            say. The acts are on the right button now
                            (nocx-rmjj8), where the tab strip and the Files
                            panel already put theirs. */}
                            <Show when={row.kind === 'collection'}>
                              <span class="api-tree__row-actions">
                                <IconButton
                                  size="sm"
                                  title="New request"
                                  ariaLabel={`New request in ${row.name}`}
                                  onClick={(e: MouseEvent) => {
                                    e.stopPropagation()
                                    store.pointAt(row.handle)
                                    // The collection's own root: this control
                                    // is only ever on a collection row, whose
                                    // relPath is ''.
                                    void store.newRequest(row.relPath)
                                  }}
                                >
                                  <PlusIcon />
                                </IconButton>
                                <IconButton
                                  size="sm"
                                  title="More"
                                  ariaLabel={`More actions for ${row.name}`}
                                  onClick={(e: MouseEvent) => openRowMenu(e, row)}
                                >
                                  <MoreIcon />
                                </IconButton>
                              </span>
                            </Show>
                          </>
                        }
                      />
                    </div>
                  </Show>
                )}
              </For>
            </Show>
          </div>

          {/* The foot: what governs a send, and the doors that bring one in.
            Below the tree and out of its way — the tree takes the height,
            these take what is left. */}
        </Section>

        {/* ENVIRONMENTS, beside the collections — the owner's reference puts
            them in the same rail, and the reason holds here: an environment
            is CHOSEN many times a day and edited rarely, so the thing that
            should always be on screen is the list, not a control that opens
            one. It replaced the picker that used to sit on the pane's
            header; the list IS the picker, and a second one would be two
            places to answer one question.

            It shows the ACTIVE collection's environments, because that is
            what an environment belongs to (§6.5) — the tick says which one a
            send goes out under. */}
        <Show when={store.activeCollection() !== ''}>
          <Section
            id="api-environments"
            title="Environments"
            dense
            collapsible
            open={environmentsOpen()}
            onToggle={() => setEnvironmentsOpen((was) => !was)}
            actions={
              <>
                <IconButton
                  id="api-new-environment"
                  size="sm"
                  title="New environment"
                  ariaLabel="New environment"
                  onClick={() => {
                    setView('environments')
                    newEnvironment()
                  }}
                >
                  <PlusIcon />
                </IconButton>
                <IconButton
                  id="api-environments-open"
                  size="sm"
                  title="Manage environments"
                  ariaLabel="Manage environments"
                  selected={envOpen()}
                  onClick={openEnvironments}
                >
                  <MoreIcon />
                </IconButton>
              </>
            }
          >
            <div class="api-environments-rail">
              <Show
                when={store.environments().length > 0}
                fallback={
                  <p class="api-environments__note">
                    None yet. A URL written in <code>{'{{baseUrl}}'}</code> resolves against one of
                    these.
                  </p>
                }
              >
                <For each={store.environments()}>
                  {(env) => (
                    // The kit's ghost Button with `selected` — the same row
                    // the environments page uses, and the same one Tabs and
                    // the settings rail are made of.
                    <Button
                      variant="ghost"
                      selected={env.relPath === store.activeEnvironment()}
                      onClick={() => store.setEnvironment(env.relPath)}
                    >
                      {env.name}
                    </Button>
                  )}
                </For>
                {/* Sending under NONE is a choice a person makes, so it is a
                    row like the others rather than an absence they have to
                    work out — the request goes out exactly as its file has
                    it. */}
                <Button
                  variant="ghost"
                  selected={store.activeEnvironment() === ''}
                  onClick={() => store.setEnvironment('')}
                >
                  No environment
                </Button>
              </Show>
            </div>
          </Section>
        </Show>
      </aside>

      {/* The tree's trailing edge. It is the kit's separator — the same
          component the shell's sidebar uses, now on its second caller, which
          is what the kit comment predicted when it said "in the kit where
          the next pane edge can reuse it". */}
      {/* Each seam is PLACED by a wrapper of the surface's own. The kit's
          handle takes no class — a surface may not name a component's
          identity — so the cell it lives in is a box beside it rather than
          the component itself. */}
      <div class="api-workbench__seam" data-seam="tree">
        <ResizeHandle
          ariaLabel="Resize the collections column"
          value={treeWidth()}
          min={MIN_TREE_WIDTH}
          max={treeMax()}
          onChange={setTreeWidth}
          onCommit={setTreeWidth}
        />
      </div>

      {/* THE HEADER: where this request lives, what it is called — edited
          in place — and Save. The environment used to sit here; it is in the
          sidebar now, beside the collections, because switching one is far
          more frequent than editing one and a list that is always on screen
          IS the switch. */}
      <div class="api-workbench__head">
        {/* THE CRUMBS SAY WHICH PAGE THIS IS. Environments opens as
            `Playground › Environments`, in the same line that says
            `Playground › Create user` for a request — one trail, one place a
            person reads where they are, and the page below it no longer
            needs a title of its own. */}
        <Show when={view() === 'environments'}>
          <header class="api-crumbs">
            <span class="api-crumbs__collection">{activeCollectionName()}</span>
            <span class="api-crumbs__sep" aria-hidden="true">
              ›
            </span>
            <span class="api-crumbs__here">Environments</span>
            <span class="api-crumbs__save">
              <IconButton
                id="api-environments-close"
                size="sm"
                title="Back to the request"
                ariaLabel="Back to the request"
                onClick={showRequest}
              >
                <CloseIcon />
              </IconButton>
            </span>
          </header>
        </Show>
        {/* A FOLDER'S OWN TRAIL. The same line, saying the same kind of
            thing: which collection, and where in it. The doors in its tail
            act HERE — that is the whole point of standing somewhere — and
            the way back is offered only while there is a request to go back
            to, because a control that returns to nothing is one that
            swallows the press. */}
        <Show when={view() === 'folder'}>
          <header class="api-crumbs">
            <span class="api-crumbs__collection">{activeCollectionName()}</span>
            <Show when={store.activeFolder() !== ''}>
              <span class="api-crumbs__sep" aria-hidden="true">
                ›
              </span>
              <span class="api-crumbs__here">{store.activeFolder()}</span>
            </Show>
            <span class="api-crumbs__save">
              <IconButton
                id="api-folder-new-request"
                size="sm"
                title="New request in this folder"
                ariaLabel="New request in this folder"
                onClick={newRequestHere}
              >
                <PlusIcon />
              </IconButton>
              <IconButton
                id="api-folder-import-curl"
                size="sm"
                title="Import a curl command into this folder"
                ariaLabel="Import a curl command into this folder"
                onClick={askForCurl}
              >
                <ArrowDownIcon />
              </IconButton>
              <Show when={store.draft() !== null}>
                <IconButton
                  id="api-folder-close"
                  size="sm"
                  title="Back to the request"
                  ariaLabel="Back to the request"
                  onClick={showRequest}
                >
                  <CloseIcon />
                </IconButton>
              </Show>
            </span>
          </header>
        </Show>
        <Show when={view() === 'request'}>
          <RequestCrumbs
            collection={activeCollectionName()}
            name={store.draft()?.name ?? null}
            folder={openFolderPath()}
            onRename={(name) => {
              const draft = store.draft()
              if (draft) store.editDraft({ ...draft, name })
            }}
            onMore={store.selected() !== null ? openRequestMenu : undefined}
            // ABSENCE IS THE CAPABILITY. `newRequest` writes into the ACTIVE
            // collection and answers nothing when there is none, so a
            // control handed in with none open would be one that swallows
            // the press. The row's plus and the row's menu are untouched:
            // they are how a request is made in a collection that is not the
            // one the workbench is pointed at.
            onNew={store.activeCollection() !== '' ? newRequestHere : undefined}
            onImportCurl={askForCurl}
          />
        </Show>
      </div>

      {/* THE LINE SPANS BOTH HALVES, and that is the geometry the owner
          asked for: a URL is the widest thing on this surface, and it is
          what a person edits between one send and the next. Under it the
          request and what came back sit SIDE BY SIDE — before this the
          response was below a form two screens tall, so reading the answer
          meant scrolling away from the question. */}
      {/* The line belongs to a REQUEST. With a page open there is none, and a
          disabled method-and-URL row above a page about something else is a
          control that governs nothing. */}
      <Show when={view() === 'request'}>
        <div class="api-workbench__line">
          <RequestLine
            request={store.draft()}
            dirty={store.dirty()}
            // SENDABLE IS "there is a request", not "there is a file". A
            // request with no file behind it gets one on the way out
            // (api-store.ts): the send path already writes a dirty draft
            // before the exchange, and "not saved yet" is that same rule.
            sendable={store.draft() !== null}
            sending={store.pending() !== null}
            onEdit={(next) => store.editDraft(next)}
            variableState={variableState}
            onVariable={(name, at) => setVarMenu({ name, x: at.x, y: at.y })}
            onSend={() => void store.send()}
            onStop={() => void store.stop()}
          />
        </div>
      </Show>

      {/* A PAGE TAKES THE HALF while it is open — the request and its response
          are still there, underneath, and Back puts them on screen again. A
          page rather than an overlay: nothing is dimmed and nothing is
          covered, so the tree beside it still works. */}
      <Show when={view() === 'folder'}>
        <div class="api-workbench__page">
          <FolderView
            folder={store.activeFolder()}
            entries={here()}
            onOpen={(entry) => {
              if (entry.folder) {
                store.enterFolder(store.activeCollection(), entry.relPath)
                expand(`${store.activeCollection()}:${entry.relPath}`)
                return
              }
              void store.openRequest(store.activeCollection(), entry.relPath)
              showRequest()
            }}
            actions={entryActions}
            variables={folderVariables()}
            loading={folderLoading()}
            written={folderWritten()}
            busy={folderBusy()}
            error={folderVariablesRefused()}
            saveError={folderSaveRefused()}
            onVariables={setFolderRows}
            onNewRequest={newRequestHere}
          />
        </div>
      </Show>
      <Show
        when={view() !== 'environments'}
        fallback={
          <div class="api-workbench__page">
            <EnvironmentView
              environments={store.environments()}
              editing={envEditing()}
              active={store.activeEnvironment()}
              creating={envCreating()}
              name={envName()}
              relPath={envRelPath()}
              rows={envRows()}
              dirty={envDirty()}
              busy={envBusy()}
              error={envRefused()}
              onPick={pickEnvironment}
              onNew={newEnvironment}
              onName={(v) => {
                setEnvName(v)
                setEnvDirty(true)
              }}
              onRelPath={setEnvRelPath}
              onRows={(rows) => {
                setEnvRows(rows)
                setEnvDirty(true)
              }}
              route={envRoute()}
              onRoute={(route) => {
                setEnvRoute(route)
                setEnvDirty(true)
              }}
              connections={store.connections()}
              // The one control in this panel that carries a credential. It
              // is handed the store's method rather than the client's: the
              // store is what knows which collection and which environment
              // the editor is showing, and a surface naming those itself
              // would be a second answer to a question the store already
              // owns.
              onBindSecret={(variable, value) => store.bindSecret(variable, value).then(() => {})}
              onSave={saveEnvironment}
              onReset={resetEnvironment}
            />
          </div>
        }
      >
        <Show when={view() === 'request'}>
          <section class="api-workbench__request" aria-label="Request">
            <RequestEditor
              request={store.draft()}
              scopeVariables={store.scopeVariables()}
              onEdit={(next) => store.editDraft(next)}
              secretTarget={secretTarget()}
              onCreateSecret={createSecret}
            />
          </section>

          {/* The seam between the question and the answer. Vertical now: the
            two halves are beside each other, so what a person drags is how
            much of the width the request keeps. */}
          <div class="api-workbench__seam" data-seam="request">
            <ResizeHandle
              ariaLabel="Resize the request half"
              value={requestWidth()}
              min={MIN_REQUEST_WIDTH}
              max={requestMax()}
              onChange={setRequestWidth}
              onCommit={setRequestWidth}
            />
          </div>

          <section class="api-workbench__runs" aria-label="Runs">
            {/* The column says what it is. Every run under it carries its own
              status, elapsed time and size (run-list.tsx), so this names the
              column and does not restate one run's numbers at the top of a
              list of many — which would be two owners of one fact the moment
              a second run arrived. */}
            <div class="api-workbench__runs-head">
              <Caption>Response</Caption>
              <Show when={store.runs().length > 0}>
                <Badge tone="neutral">{String(store.runs().length)}</Badge>
              </Show>
            </div>
            <RunList
              runs={store.runs()}
              onView={(id, at) => store.setRunView(id, at)}
              connectionName={connectionName}
            />
          </section>
        </Show>
      </Show>
    </div>
  )
}

/** The kit's row vocabulary for a workbench row. A collection and a directory
 *  are both folders as far as the row is concerned; a file the format did not
 *  recognise is `unreadable`, which is the kit's own word for a row that
 *  exists and cannot be opened. */
function rowKind(row: ApiTreeRow): 'dir' | 'unreadable' {
  return row.kind === 'malformed' ? 'unreadable' : 'dir'
}
