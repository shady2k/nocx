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

import { For, Show, createEffect, createSignal, onCleanup, onMount, untrack } from 'solid-js'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Caption } from '../ui/caption'
import { EmptyState } from '../ui/empty-state'
import { IconButton } from '../ui/icon-button'
import {
  ArrowDownIcon,
  CloseIcon,
  FolderOpenIcon,
  MoreIcon,
  PencilIcon,
  PlusIcon,
  RefreshIcon,
  TrashIcon,
} from '../ui/icons'
import { MarkerList } from '../ui/marker-list'
import { ResizeHandle } from '../ui/resize-handle'
import { ContextMenu } from '../ui/context-menu'
import { Section } from '../ui/section'
import { TextField } from '../ui/text-field'
import { StatusCard } from '../ui/status-card'
import { TreeRow } from '../ui/tree-row'
import { WatchBadge } from '../ui/watch-badge'
import { showConfirm } from '../ui/dialog'
import { showToast } from '../ui/toast'
import { filterCollections, flattenCollections, type ApiTreeRow } from './api-tree'
import { CollectionDialog } from './collection-dialog'
import { EnvironmentView, toRows, toStored, type ValueRow } from './environment-view'
import { environmentPath, proposedDestination } from './api-paths'
import { CurlImportDialog, PostmanImportDialog } from './import-dialogs'
import { RequestCrumbs } from './request-crumbs'
import { RequestEditor, RequestLine } from './request-form'
import { RunList } from './run-list'
import type { ApiStore, VariableAnswer } from './api-store'
import type { DirectoryPicker, FilePicker } from './api-client'
import type { ApiRoute } from './api-model'

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
}

/** Floors for the two seams: a tree column narrower than this cannot show a
 *  nested request's name, and a request half narrower than this cannot show
 *  a name-and-value row. */
const MIN_TREE_WIDTH = 180
const MIN_REQUEST_WIDTH = 320

/** The empty set a filtered tree is flattened against — one object rather
 *  than a new Set per read, because `rows()` runs on every keystroke. */
const NOTHING_COLLAPSED: ReadonlySet<string> = new Set()

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
  // WHICH ROW the menu is about, in a plain variable and not a signal. The
  // kit's ContextMenu closes BEFORE it calls the item's onSelect — the action
  // is what the person is waiting for, so the popover goes first — and
  // `onClose` is what clears the open state. Reading the row out of a signal
  // that close had just cleared meant every item acted on nothing.
  let rowMenuHandle = ''
  /** Where the open request's menu hangs, or null when it is closed. WHICH
   *  request it is about is not held here: it is the one the header is
   *  naming, which is `store.selected()` — one owner of "the open request",
   *  and a copy would be a second that could disagree with it. */
  const [doomed, setDoomed] = createSignal<{ x: number; y: number } | null>(null)

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

  // THE ENVIRONMENT PAGE'S DRAFT. It used to keep the file it read as well,
  // because two of its fields — the route and the secret names — could not be
  // edited and had to be carried back untouched. Both are the editor's now,
  // so the draft IS the answer and there is nothing to carry.
  const [envOpen, setEnvOpen] = createSignal(false)
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

  const [importing, setImporting] = createSignal(false)
  const [postmanFile, setPostmanFile] = createSignal('')
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
  const anyAskOpen = (): boolean => naming() || opening() || importing() || curling() || envOpen()

  const rows = (): ApiTreeRow[] =>
    flattenCollections(
      filterCollections(store.collections(), filter()),
      filter().trim() === '' ? collapsed() : NOTHING_COLLAPSED,
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

  const activate = (row: ApiTreeRow): void => {
    if (row.kind === 'request') {
      void store.openRequest(row.handle, row.relPath)
      return
    }
    // A malformed file has nothing to open — the row's own text is the whole
    // answer — and a collection or directory row toggles, the way every file
    // tree in the product does (the disclosure is a 16px target and the row
    // is the whole width).
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

  const openFolder = (path: string): void => {
    setOpeningFolder(true)
    void store.openFolder(path).then(() => {
      setOpeningFolder(false)
      setPathRefused(store.error())
      if (store.error() !== '') return
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
   *  a directory — the export itself is typed until there is a file picker
   *  to reach for. */
  const browseForImportDest = (): void => browseInto(setPostmanDest, setImportRefused)

  /**
   * Choose the EXPORT with the system picker, and propose where its
   * collection lands.
   *
   * The proposal is the second half of the same gesture, and it is here
   * rather than in the dialog because only this level knows both the chosen
   * file and the backend's default location. It is skipped once somebody has
   * typed a destination: a person who has said where the folder goes has
   * said it, and a later pick that overwrote them would be the surface
   * arguing.
   */
  const chooseExport = (path: string): void => {
    setPostmanFile(path)
    // Both reads below are READS AT A MOMENT rather than subscriptions —
    // this runs from a click or a picker's answer, never from a render — so
    // they are untracked: nothing here should re-run when the listing
    // refreshes and rewrite a field somebody is typing into.
    if (untrack(destTyped)) return
    const proposal = proposedDestination(
      untrack(() => store.defaultRoot()),
      path,
    )
    if (proposal !== '') setPostmanDest(proposal)
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

  // The two import asks open the way the other two do: empty, with no
  // reason under the field, because a fresh ask holding the last answer is
  // an offer nobody wrote (askForName says the rest).
  /**
   * Save the request in the form.
   *
   * IT ASKS NOTHING, either way. With a file behind it this writes. Without
   * one — a converted curl line — the store gives it a file named after the
   * request it already is, uniquified against the folder (api-store.ts). The
   * ask that used to stand here wanted a FILE name for something that had a
   * name, in the currency of paths rather than of names, at the moment
   * somebody was reaching for Send. The name is renamed in the header, in
   * place, whenever they know what it should be.
   */
  const saveRequest = (): void => {
    const write = store.selected() !== null ? store.saveDraft() : store.saveDraftAs()
    void write.then(() => {
      if (store.error() === '') showToast({ level: 'success', message: 'Saved' })
    })
  }

  const askForCurl = (): void => {
    setCurlLine('')
    setCurlRefused('')
    setCurling(true)
  }

  const askForImport = (): void => {
    setPostmanFile('')
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
   * `unknown` is not a hedge: until an environment has been read there is no
   * answer to give for a name this request does not answer itself, and
   * painting it as unanswered in that window is how a person learns to
   * ignore the colour.
   */
  const variableState = (name: string): 'bound' | 'secret' | 'unbound' | 'unknown' => {
    const answer: VariableAnswer = store.variableAnswer(name)
    switch (answer.scope) {
      case 'request':
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
      case 'environment':
        return `{{${name}}} = ${answer.value ?? ''} — from ${environmentName(env)}`
      case 'secret':
        return `{{${name}}} = a secret, from the vault`
      case 'unknown':
        return `{{${name}}} — no environment has been read yet`
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
    setEnvOpen(true)
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
    setEnvOpen(true)
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
    void store.importCurl(line).then(() => {
      setConverting(false)
      setCurlRefused(store.error())
      // A converted curl line lands in the FORM — there is no file behind
      // it yet (api-store.ts) — so the ask closes and the request pane is
      // where the person looks next.
      if (store.error() !== '') return
      setCurling(false)
    })
  }

  const importPostman = (): void => {
    const file = postmanFile().trim()
    const dest = postmanDest().trim()
    if (file === '' || dest === '') return
    setImportingBusy(true)
    void store.importPostman(file, dest).then(() => {
      setImportingBusy(false)
      setImportRefused(store.error())
      if (store.error() !== '') return
      setImporting(false)
      showToast({ level: 'success', message: `Imported into ${dest}` })
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
  const openRowMenu = (e: MouseEvent, handle: string): void => {
    e.stopPropagation()
    const box = (e.currentTarget as HTMLElement).getBoundingClientRect()
    rowMenuHandle = handle
    setRowMenu({ x: box.left, y: box.bottom })
  }

  const openRequestMenu = (e: MouseEvent): void => {
    const box = (e.currentTarget as HTMLElement).getBoundingClientRect()
    setDoomed({ x: box.left, y: box.bottom })
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
        {/* What the OPEN request can be. Deleting is a menu item rather than a
            control for the reason closing a collection is: it takes
            something away, so it has to be read and chosen. */}
        <ContextMenu
          open={doomed() !== null}
          x={doomed()?.x ?? 0}
          y={doomed()?.y ?? 0}
          data-testid="api-request-row-menu"
          onClose={() => setDoomed(null)}
          items={[
            {
              id: 'api-row-delete',
              label: 'Delete request…',
              icon: TrashIcon,
              onSelect: () => {
                const target = store.selected()
                const named = store.draft()?.name ?? ''
                if (!target) return
                // AND THEN IT ASKS — through the kit's own confirm, which is
                // where "are you sure" lives in this product. A delete
                // removes a file from a folder somebody shares through git,
                // and the only undo is a working tree they may not have
                // committed. The question NAMES what goes, because "are you
                // sure" is a question about nothing.
                void showConfirm(
                  `Delete ${named}? The file is removed from the collection folder.`,
                  'Delete',
                ).then((yes) => {
                  setDoomed(null)
                  if (yes) void store.deleteRequest(target.handle, target.relPath)
                })
              },
            },
          ]}
        />

        {/* What a collection row can do. Closing is here rather than on the
            row itself because it is the one act that takes something away,
            and here it has to be read and chosen. */}
        <ContextMenu
          open={rowMenu() !== null}
          x={rowMenu()?.x ?? 0}
          y={rowMenu()?.y ?? 0}
          data-testid="api-collection-row-menu"
          onClose={() => setRowMenu(null)}
          items={[
            {
              id: 'api-row-new-request',
              label: 'New request',
              icon: PlusIcon,
              onSelect: () => {
                if (rowMenuHandle === '') return
                store.pointAt(rowMenuHandle)
                void store.newRequest()
              },
            },
            {
              id: 'api-row-close',
              // "Close collection", not the path. The row this menu hangs
              // off already says WHICH collection, and a path elided in the
              // middle answers neither question — it is not readable and it
              // is not needed.
              label: 'Close collection',
              icon: CloseIcon,
              onSelect: () => {
                if (rowMenuHandle !== '') void store.closeFolder(rowMenuHandle)
              },
            },
          ]}
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
          fieldDescription="A name, not a path — the folder is made where nocx keeps collections. It is safe to commit: no secret value is ever written into it."
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
          fieldDescription="The folder you place. It is safe to commit: no secret value is ever written into it."
          placeholder="/work/acme-api"
          value={folderPath()}
          onInput={setFolderPath}
          error={pathRefused()}
          busy={openingFolder()}
          onBrowse={pickerLive() ? browseForFolder : undefined}
          onCancel={() => setOpening(false)}
          onSubmit={openFolder}
        />
        <PostmanImportDialog
          open={importing()}
          file={postmanFile()}
          dest={postmanDest()}
          onFile={chooseExport}
          onDest={(value) => {
            setDestTyped(true)
            setPostmanDest(value)
          }}
          defaultRoot={store.defaultRoot()}
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
          <div class="api-tree" role="tree" aria-label="Collections">
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
                  <div
                    class="api-tree__row"
                    data-rel-path={row.kind === 'request' ? row.relPath : undefined}
                    data-row-key={row.key}
                    onClick={() => activate(row)}
                  >
                    <TreeRow
                      name={row.name}
                      depth={row.depth}
                      kind={row.kind === 'request' ? 'regular' : rowKind(row)}
                      selected={
                        row.kind === 'collection' && row.handle === store.activeCollection()
                      }
                      disabled={row.kind === 'malformed'}
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
                          <Show when={row.kind === 'collection'}>
                            <span class="api-tree__row-actions">
                              <IconButton
                                size="sm"
                                title="New request"
                                ariaLabel={`New request in ${row.name}`}
                                onClick={(e: MouseEvent) => {
                                  e.stopPropagation()
                                  store.pointAt(row.handle)
                                  void store.newRequest()
                                }}
                              >
                                <PlusIcon />
                              </IconButton>
                              <IconButton
                                size="sm"
                                title="More"
                                ariaLabel={`More actions for ${row.name}`}
                                onClick={(e: MouseEvent) => openRowMenu(e, row.handle)}
                              >
                                <MoreIcon />
                              </IconButton>
                            </span>
                          </Show>
                        </>
                      }
                    />
                    <Show when={row.reason !== ''}>
                      <p class="api-tree__reason">{row.reason}</p>
                    </Show>
                    {/* Why a folder that IS open has nothing under it — the
                      listing failed, and the row is where the reason belongs
                      now that the second list is gone. */}
                    <Show when={row.kind === 'collection' && errorOf(row.handle) !== ''}>
                      <p class="api-tree__reason">{errorOf(row.handle)}</p>
                    </Show>
                  </div>
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
                    setEnvOpen(true)
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

        {/* The foot is the whole element, not a container that empties: a
            box with a rule above it and nothing in it is a line the surface
            draws for no reason. */}
        <Show when={store.notes().length > 0}>
          <div class="api-workbench__foot">
            {/* WHAT THE LAST IMPORT COULD NOT CARRY. It used to live inside
              the Import form, which is exactly where it could not be read:
              the form closed, and the list of what was silently dropped went
              with it. A degrade that is only visible while the control that
              caused it is open is a degrade nobody sees (AGENTS.md). It is
              absent when there is nothing to say. */}
            <Section title="Not imported">
              <MarkerList
                items={store.notes().map((n) => ({
                  tone: 'excluded' as const,
                  text: `${n.what} — ${n.why}`,
                }))}
              />
            </Section>
          </div>
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
        <Show
          when={!envOpen()}
          fallback={
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
                  onClick={() => setEnvOpen(false)}
                >
                  <CloseIcon />
                </IconButton>
              </span>
            </header>
          }
        >
          <RequestCrumbs
            collection={activeCollectionName()}
            name={store.draft()?.name ?? null}
            onRename={(name) => {
              const draft = store.draft()
              if (draft) store.editDraft({ ...draft, name })
            }}
            savable={store.draft() !== null && (store.dirty() || store.selected() === null)}
            onSave={saveRequest}
            onMore={store.selected() !== null ? openRequestMenu : undefined}
          />
        </Show>
      </div>

      {/* THE LINE SPANS BOTH HALVES, and that is the geometry the owner
          asked for: a URL is the widest thing on this surface, and it is
          what a person edits between one send and the next. Under it the
          request and what came back sit SIDE BY SIDE — before this the
          response was below a form two screens tall, so reading the answer
          meant scrolling away from the question. */}
      {/* The line belongs to a REQUEST. With the environments page open there
          is none, and a disabled method-and-URL row above a page about
          something else is a control that governs nothing. */}
      <Show when={!envOpen()}>
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
            onImportCurl={askForCurl}
          />
        </div>
      </Show>

      {/* The environments TAKE THE HALF while they are open — the request and
          its response are still there, underneath, and Back puts them on
          screen again. A page rather than an overlay: nothing is dimmed and
          nothing is covered, so the tree beside it still works. */}
      <Show
        when={!envOpen()}
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
        <section class="api-workbench__request" aria-label="Request">
          <RequestEditor request={store.draft()} onEdit={(next) => store.editDraft(next)} />
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
            onView={(id, view) => store.setRunView(id, view)}
            connectionName={connectionName}
          />
        </section>
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
