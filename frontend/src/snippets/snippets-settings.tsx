/**
 * The snippets settings page — where a library is authored (design §10.4,
 * bead nocx-gjnr, plan Task 10).
 *
 * Until this page existed the palette could list and fire a library nobody
 * could add to: the store's create/update/delete/reorder had no caller at
 * all, and the only records in the product were the two the service seeds.
 * This is the surface that makes the epic's sentence true.
 *
 * Kit contract (frontend/src/ui/README.md): the same shape Connections and
 * Endpoints have — a CollectionView of RecordRows with a Dialog editor,
 * showConfirm for a delete, EmptyState for every state that has no rows.
 * Nothing here is hand-rolled and nothing repaints a kit component; the
 * surface's own CSS (styles/components/snippets.css) only places wrappers.
 *
 * Two things are this page's own:
 *
 *  - The body is a real CM6 editor (the kit has no multi-line code field),
 *    mounted through the shared host's editable mode — the file viewer's
 *    host, not a second construction of a view (cm-host.ts).
 *  - The preview line under it reports what the parser recognised and what
 *    it did not, which is the one signal that stops a mistyped
 *    `{{ask:port}` from reaching somebody's agent session as literal text.
 */
import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js'
import type { Extension } from '@codemirror/state'
import { Button } from '../ui/button'
import { CollectionView } from '../ui/collection-view'
import { RecordRow } from '../ui/record-row'
import { Dialog, showConfirm } from '../ui/dialog'
import { EmptyState } from '../ui/empty-state'
import { Field } from '../ui/field'
import { StatusCard } from '../ui/status-card'
import { IconButton } from '../ui/icon-button'
import { PencilIcon, TrashIcon } from '../ui/icons'
import { Stack } from '../ui/stack'
import { TextField } from '../ui/text-field'
import { createFormValidation, required } from '../ui/validation'
import { createSubmitGate } from '../ui/submit-gate'
import { showToast } from '../ui/toast'
import { log } from '../log'
import { EditableHost } from '../cm-host'
import { markdownLanguage, viewerHighlighting } from '../file-viewer/language-registry'
import { describeBody, type PreviewPart } from './preview'
import { ENV_KEYS, type EnvKey } from './resolve'
import type { Snippet, SnippetsState, SnippetsStore } from './snippets-store'

/** The body editor seam. `EditableHost` satisfies it; a test substitutes a
 *  fake because jsdom does not emulate contenteditable input, and the
 *  default path has its own test (the editor mounts and shows the body). */
export interface BodyEditorHost {
  mount(
    parent: HTMLElement,
    signal: AbortSignal,
    extensions?: Extension[],
    onDocChange?: (text: string) => void,
  ): void
  setDoc(text: string): void
  doc(): string
  focus(): void
  dispose(): void
}

export interface SnippetsSectionProps {
  /** The one library every surface reads — the same store the palette
   *  holds, so a save here is visible to the next fire without a
   *  notification on the wire (design §6). */
  store: SnippetsStore
  /** The body editor's construction, injected for the test seam above. */
  createBodyHost?: () => BodyEditorHost
}

/** One sentence per span, for the preview line. The env phrases come from
 *  the resolver's own table, so the preview cannot promise a key the fire
 *  refuses. */
function previewSentence(part: PreviewPart): string {
  switch (part.kind) {
    case 'env':
      return part.known
        ? `${part.text} → ${ENV_KEYS[part.key as EnvKey]}`
        : `${part.text} → not a key nocx can answer; the fire will refuse`
    case 'param':
      if (part.options.length > 0) {
        return `${part.text} → you will choose one of ${part.options.join(', ')}`
      }
      return part.defaultValue === ''
        ? `${part.text} → you will be asked`
        : `${part.text} → you will be asked (default ${part.defaultValue})`
    case 'flag':
      return part.negated
        ? `${part.text} → kept unless you tick "${part.name}"`
        : `${part.text} → kept only if you tick "${part.name}"`
    case 'secret':
      return `${part.text} → the vault secret "${part.name}"`
    case 'unrecognised':
      return `${part.text} → not recognised; it will be sent as it is`
    case 'problem':
      return `${part.text} → ${part.detail}; this snippet cannot be fired`
  }
}

const previewRecognised = (part: PreviewPart): boolean =>
  part.kind !== 'unrecognised' && part.kind !== 'problem' && !(part.kind === 'env' && !part.known)

/** The row's one-line description of a body: its first non-empty line,
 *  bounded. A body is multi-line and the row is one line — a raw body would
 *  make every row a different height and say nothing more. */
function bodySummary(body: string): string {
  const line = body.split('\n').find((l) => l.trim() !== '') ?? ''
  return line.length > 80 ? `${line.slice(0, 79)}…` : line
}

export function SnippetsSection(props: SnippetsSectionProps) {
  // 'loading' until the subscription answers — which it does synchronously
  // on mount, with whatever the store already holds. Reading the store here
  // would be reading a prop outside a tracked scope.
  const [state, setState] = createSignal<SnippetsState>({ kind: 'loading' })
  const [searchQuery, setSearchQuery] = createSignal('')
  const [dialogOpen, setDialogOpen] = createSignal(false)
  const [editing, setEditing] = createSignal<Snippet | null>(null)
  const [title, setTitle] = createSignal('')
  const [body, setBody] = createSignal('')
  /** The backend's reason for refusing the last save, kept ON the dialog:
   *  a toast over a closed editor would take away both the sentence and the
   *  draft it is about. */
  const [saveError, setSaveError] = createSignal('')

  /** The row being dragged, and where it would land — the gesture's own
   *  state. The drop line is drawn from this rather than from the pointer's
   *  position, so what is shown is exactly what moveTo will do. */
  const [draggingId, setDraggingId] = createSignal<string | null>(null)
  const [dropAt, setDropAt] = createSignal<{ id: string; edge: 'above' | 'below' } | null>(null)

  let bodyHost: BodyEditorHost | null = null
  let bodyAbort: AbortController | null = null

  onMount(() => {
    const unsubscribe = props.store.subscribe(setState)
    onCleanup(unsubscribe)
    void props.store.refresh()
  })
  onCleanup(() => {
    bodyAbort?.abort()
  })

  const snippets = createMemo<readonly Snippet[]>(() => {
    const s = state()
    return s.kind === 'ready' ? s.snippets : []
  })
  const filtering = createMemo(() => searchQuery().trim() !== '')
  const filtered = createMemo(() => {
    const q = searchQuery().trim().toLowerCase()
    if (q === '') return snippets()
    return snippets().filter(
      (s) => s.title.toLowerCase().includes(q) || s.body.toLowerCase().includes(q),
    )
  })

  const validation = createFormValidation(
    { title: () => required('Title')(title()) },
    { controlId: () => 'snippet-title' },
  )
  const gate = createSubmitGate(validation)

  /** Mount the CM6 editor into the dialog's body slot. Called by the ref,
   *  which fires once per dialog opening — the previous host is disposed
   *  with its own AbortController, the way every host consumer does it. */
  function mountBody(parent: HTMLElement): void {
    bodyAbort?.abort()
    bodyAbort = new AbortController()
    const host = props.createBodyHost ? props.createBodyHost() : new EditableHost()
    bodyHost = host
    // The language and highlighting are the file viewer's registry — one
    // owner of "which CM6 language this text is" (design §10.4: markdown is
    // already in the bundle, and a body is prose with commands in it).
    host.mount(parent, bodyAbort.signal, [markdownLanguage(), viewerHighlighting], (text) =>
      setBody(text),
    )
    host.setDoc(body())
  }

  function openNew(): void {
    setEditing(null)
    setTitle('')
    setBody('')
    setSaveError('')
    validation.reset()
    setDialogOpen(true)
  }

  function openEdit(s: Snippet): void {
    setEditing(s)
    setTitle(s.title)
    setBody(s.body)
    setSaveError('')
    validation.reset()
    setDialogOpen(true)
  }

  function closeDialog(): void {
    setDialogOpen(false)
    bodyAbort?.abort()
    bodyAbort = null
    bodyHost = null
  }

  async function save(): Promise<void> {
    if (!(await gate())) return
    // The host is the authority on what is in the field; the signal follows
    // it through onDocChange, and this read is the one that reaches the
    // wire.
    const text = bodyHost?.doc() ?? body()
    const name = title().trim()
    const target = editing()
    try {
      if (target) {
        await props.store.update(target.id, name, text)
      } else {
        await props.store.create(name, text)
      }
      closeDialog()
      showToast({ level: 'success', message: `Saved "${name}"` })
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      log.error('Failed to save snippet', { message })
      // Stays on the dialog, beside the draft that caused it.
      setSaveError(message)
    }
  }

  async function remove(s: Snippet): Promise<void> {
    if (!(await showConfirm(`Delete "${s.title}"?`))) return
    try {
      await props.store.remove(s.id)
      showToast({ level: 'success', message: `Deleted "${s.title}"` })
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      log.error('Failed to delete snippet', { message })
      showToast({ level: 'danger', message: `Could not delete "${s.title}": ${message}` })
    }
  }

  /** Put `movedId` where `targetId` sits. The wire takes the WHOLE order
   *  (the service refuses anything that is not a permutation of the
   *  library), so the move is computed over the full list and sent as the
   *  full list — never as the pair that changed.
   *
   *  Ordering is a DRAG, not a pair of buttons: no other list in this
   *  product has arrow controls, and a row a person can pick up is the
   *  thing every list they already use behaves like (owner review, and the
   *  tab strip's own reorder). Alt+↑/↓ does the same from the keyboard, so
   *  the order is not mouse-only. */
  async function moveTo(movedId: string, targetId: string): Promise<void> {
    if (movedId === targetId) return
    const ids = snippets().map((x) => x.id)
    const from = ids.indexOf(movedId)
    const to = ids.indexOf(targetId)
    if (from < 0 || to < 0) return
    ids.splice(to, 0, ...ids.splice(from, 1))
    try {
      await props.store.reorder(ids)
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      log.error('Failed to reorder snippets', { message })
      showToast({ level: 'danger', message: `Could not reorder: ${message}` })
    }
  }

  /** Which side of `targetId` the dragged row lands on. Read from the
   *  ORDER, not from the pointer: moveTo removes the row and re-inserts it
   *  at the target's index, so a row coming from above ends up after the
   *  target and one coming from below ends up before it. A line drawn from
   *  the cursor's half of the row would be a guess, and half the time it
   *  would promise the wrong place. */
  function dropEdge(movedId: string, targetId: string): 'above' | 'below' | null {
    if (movedId === targetId) return null
    const ids = snippets().map((x) => x.id)
    const from = ids.indexOf(movedId)
    const to = ids.indexOf(targetId)
    if (from < 0 || to < 0) return null
    return from < to ? 'below' : 'above'
  }

  /** The keyboard's reorder: one place up or down from the focused row. */
  function moveBy(s: Snippet, by: -1 | 1): void {
    // A filter hides rows, so "one place" would mean a place in the STORED
    // order that the person cannot see. The stored order is what fires, so
    // reordering waits until the whole list is on screen.
    if (filtering()) return
    const list = snippets()
    const at = list.findIndex((x) => x.id === s.id)
    const target = list[at + by]
    if (target === undefined) return
    void moveTo(s.id, target.id)
  }

  function renderRow(s: Snippet) {
    return (
      <div
        class="sn-row"
        // The whole row is the handle: a person drags the thing they see,
        // and a list of two rows does not need a grip column to find.
        draggable={!filtering()}
        data-dragging={draggingId() === s.id ? 'true' : undefined}
        // Where this row would land, drawn as a line on that edge. The
        // dragged id comes from a signal rather than from the dataTransfer:
        // a dragover event may not READ the payload (the browser hides it
        // until the drop), and a gesture that cannot say where it lands is
        // the one thing a drag must not do.
        data-drop={dropAt()?.id === s.id ? dropAt()?.edge : undefined}
        onDragStart={(e: DragEvent) => {
          e.dataTransfer?.setData('text/plain', s.id)
          if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
          setDraggingId(s.id)
        }}
        onDragEnd={() => {
          setDraggingId(null)
          setDropAt(null)
        }}
        onDragOver={(e: DragEvent) => {
          // Without this the drop never fires — the default is "no drop
          // here", and a list that cannot be dropped into looks broken
          // exactly halfway through the gesture.
          if (filtering()) return
          e.preventDefault()
          if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
          const moved = draggingId()
          const edge = moved === null ? null : dropEdge(moved, s.id)
          setDropAt(edge === null ? null : { id: s.id, edge })
        }}
        onDragLeave={() => {
          if (dropAt()?.id === s.id) setDropAt(null)
        }}
        onDrop={(e: DragEvent) => {
          e.preventDefault()
          const movedId = e.dataTransfer?.getData('text/plain') ?? draggingId()
          setDraggingId(null)
          setDropAt(null)
          if (movedId) void moveTo(movedId, s.id)
        }}
        onKeyDown={(e: KeyboardEvent) => {
          // Alt+arrow rather than a bare arrow: the row list is not a
          // listbox, and a bare arrow belongs to the page's scroll.
          if (!e.altKey || e.metaKey || e.ctrlKey) return
          if (e.key === 'ArrowUp') {
            e.preventDefault()
            moveBy(s, -1)
          } else if (e.key === 'ArrowDown') {
            e.preventDefault()
            moveBy(s, 1)
          }
        }}
      >
        <RecordRow
          title={s.title}
          meta={bodySummary(s.body)}
          onActivate={() => openEdit(s)}
          actions={
            <>
              <IconButton ariaLabel={`Edit ${s.title}`} onClick={() => openEdit(s)}>
                <PencilIcon />
              </IconButton>
              <IconButton ariaLabel={`Delete ${s.title}`} onClick={() => void remove(s)}>
                <TrashIcon />
              </IconButton>
            </>
          }
        />
      </div>
    )
  }

  const emptyContent = () => {
    const s = state()
    if (s.kind === 'loading') return <EmptyState title="Loading snippets" />
    if (s.kind === 'unavailable') {
      // The soft degrade, visible in the product and not only in a log
      // (§11.5): the reason, a retry, and NO create — there is nothing that
      // could accept one.
      return (
        <EmptyState
          title="Couldn't load your snippets"
          description={s.message}
          action={
            <Button variant="default" onClick={() => void props.store.refresh()}>
              Retry
            </Button>
          }
        />
      )
    }
    return (
      <EmptyState
        title="No snippets yet"
        description="Save a phrase once and fire it into whatever is taking input."
        action={
          <Button variant="primary" onClick={openNew}>
            + New snippet
          </Button>
        }
      />
    )
  }

  const parts = createMemo(() => describeBody(body()))

  return (
    <div class="sn-root">
      <CollectionView
        searchValue={searchQuery()}
        onSearch={setSearchQuery}
        searchPlaceholder="Filter snippets"
        searchLabel="Filter snippets"
        actions={
          <Show when={state().kind === 'ready'}>
            <Button variant="primary" onClick={openNew}>
              + New snippet
            </Button>
          </Show>
        }
        hasItems={snippets().length > 0}
        empty={emptyContent()}
      >
        <div role="list" aria-label="Snippet list">
          <For each={filtered()}>{(s) => renderRow(s)}</For>
        </div>
        <Show when={filtering() && filtered().length === 0}>
          <EmptyState
            title="Nothing matches this filter"
            description={`No snippet's title or body contains "${searchQuery().trim()}".`}
          />
        </Show>
      </CollectionView>

      <Dialog
        open={dialogOpen()}
        onClose={closeDialog}
        onSubmit={() => void save()}
        title={editing() ? `Edit snippet: ${editing()!.title}` : 'New snippet'}
        size="lg"
        footer={
          <>
            <Button variant="default" onClick={closeDialog}>
              Cancel
            </Button>
            <Button variant="primary" onClick={() => void save()}>
              {editing() ? 'Save snippet' : 'Create snippet'}
            </Button>
          </>
        }
      >
        <Stack>
          <TextField
            id="snippet-title"
            label="Title"
            required
            value={title()}
            onInput={setTitle}
            onBlur={() => validation.touch('title')}
            error={validation.error('title')}
            placeholder="Deploy the staging service"
          />
          {/* The editor is mounted per OPENING, not per page: the Dialog
              keeps its children in the DOM while it is closed, so a ref that
              fired once at page construction would have mounted a host
              before there was a draft to put in it — and every later edit
              would have opened on the first snippet's body. */}
          <Show when={dialogOpen()}>
            <Field for="snippet-body" label="Body">
              <div
                class="sn-body-editor"
                id="snippet-body"
                ref={(el: HTMLDivElement) => mountBody(el)}
              />
            </Field>
          </Show>
          <div class="sn-preview" role="status" aria-label="What the snippet parser recognised">
            <Show
              when={parts().length > 0}
              fallback={
                <span class="sn-preview__none">
                  Nothing to fill in — the body is sent as it is.
                </span>
              }
            >
              <For each={parts()}>
                {(part) => (
                  <span class="sn-preview__part" data-recognised={String(previewRecognised(part))}>
                    {previewSentence(part)}
                  </span>
                )}
              </For>
            </Show>
          </div>
          {/* The refusal stays ON the editor, beside the draft it is about:
              a toast would close over a dialog the person still has to fix,
              taking both the sentence and the typing with it. */}
          <Show when={saveError() !== ''}>
            <StatusCard
              tone="danger"
              title="Could not save this snippet"
              description={saveError()}
            />
          </Show>
        </Stack>
      </Dialog>
    </div>
  )
}
