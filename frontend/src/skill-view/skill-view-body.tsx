// ═══════════════════════════════════════════════════════════════════════════
// SkillViewBody — the bundle, beside the file (nocx-4m1n1): every file the
// skill carries, on the left, and the chosen one's bytes on the right.
//
// THE COLUMN'S EXISTENCE IS THE ANSWER. The modal card this tab replaces
// drew the file list only when a skill had more than one file
// (skills-section.tsx:691), so a one-file skill said nothing at all about
// what it carried. This column is drawn ALWAYS, for a bundle of one file
// too — its presence is what says "this is what the skill is made of";
// its absence used to say nothing, which is the defect.
//
// TWO CALLS, NOT ONE — `skills.files` AND `skills.scan`, SEPARATELY. A first
// version read every listed file through `skills.file` to learn which ones
// the live scan matched, on open, in parallel — up to `MaxSkillFiles` (256)
// JSON-RPC calls in one tick against `configSub`'s non-blocking depth-8
// submission semaphore (`ws.go:1446,1640`; `control/submission.go`'s
// `TryAcquire`, which refuses rather than queues a burst and can starve an
// unrelated config request from another window), to carry file text across
// the socket nobody asked to see. A second version folded the scan into
// `skills.files` itself — one call, server-side — and traded that fan-out
// for a worse defect: the file list, the tab's PRIMARY NAVIGATION, now
// waited for every file to be read and scanned before it could render at
// all, on exactly the bundles the fan-out used to hurt.
//
// So `skills.files` stays a bare `readdir` (unchanged, fast, renders the
// list immediately) and `skills.scan` is a second call that answers once
// the scan has actually run — server-side, over the whole manifest, reusing
// `skills.audit`'s own bounded read-and-scan loop with no model call and no
// file text crossing the wire. A person can pick a file and read it while
// the scan is still in flight.
//
// THREE STATES A ROW CAN BE IN, AND THEY MUST NOT BE CONFUSED, because they
// are three different VALUES rather than one absent one:
//   - NOT SCANNED YET — `skills.scan` has not answered (still in flight, or
//     it failed). The row shows a pending mark — never nothing, because
//     nothing here would be indistinguishable from "scanned, clean".
//   - SCANNED, MATCHED — the kit's `StatusDot tone="warning"`.
//   - SCANNED, CLEAN — no dot at all, and THIS is the only state in which an
//     absent dot means anything. internal/skill/file.go:88-91 is why: an
//     absent finding is not a vouch, so a mark must exist for every state
//     that is not "scanned and the scan said nothing".
// A file the scan could not individually read (`omitted` — too large, not
// text, unreadable, or the shared scan budget spent) is drawn with the SAME
// pending mark as "not scanned yet": both are "unknown", worded by why.
//
// THE MANIFEST, THE SCAN, AND WHICHEVER FILE IS ON SCREEN ARE ALL RE-READ ON
// EVERY ACTIVATION, not only once in onMount. A skill's own module comment
// says a tab "lives for days"; `refreshToken` is a number `SkillViewContent`
// bumps on every `setVisible(true)` (the same re-read `SkillsStore.refresh`
// already gets), and this component's effect re-fetches all three each time
// it changes — otherwise a long-lived tab would show the bytes and the marks
// exactly as they were the day it opened.
// ═══════════════════════════════════════════════════════════════════════════

import { For, Show, createEffect, createSignal, on, onCleanup, onMount, type JSX } from 'solid-js'
import {
  FileReadout,
  RecordRow,
  Section,
  Stack,
  StatusCard,
  type Fact,
  type FileReadoutOutcome,
  type StatusDotTone,
} from '../ui'
import { ResizeHandle } from '../ui/resize-handle'
import { skillFileOutcome } from '../skills-presentation'
import type { SkillsFile } from '../generated/skills.file'
import type { SkillsScan } from '../generated/skills.scan'
import type { SkillsStore } from '../skills-store'

export interface SkillViewBodyProps {
  /** The RESOLVED skill's name — see skill-view-content.tsx's module
   *  comment for why this is never the requested one. Stable for the life
   *  of this component: SkillViewContent mounts a body once per resolved
   *  skill and disposes it when the skill leaves, rather than handing an
   *  existing instance a new name. */
  name: string
  store: SkillsStore
  /** Bumped by SkillViewContent on every `setVisible(true)` — see the module
   *  comment. The effect below re-fetches on every change, INCLUDING the
   *  first: there is no separate onMount fetch, so there is exactly one
   *  place "go read the manifest and the scan" happens rather than two that
   *  could drift apart on when each fires. */
  refreshToken: number
}

/** The floor a dragged or measured list column may never cross, and the
 *  width a tab opens at before anybody has dragged anything — not
 *  persisted (nothing in this repo persists a pane's internal split, and a
 *  tab that remembered one dragged width would be the first). */
const MIN_LIST_WIDTH = 180
const DEFAULT_LIST_WIDTH = 260
/** Read before any real measurement lands (jsdom has no ResizeObserver, and
 *  the real one has not fired its first entry yet); generous enough that
 *  the default width sits well inside 40% of it. */
const FALLBACK_PANE_WIDTH = 900

type FilesState =
  | { kind: 'loading' }
  | { kind: 'ready'; files: readonly string[]; truncated: boolean; maxFiles: number }
  | { kind: 'unavailable'; message: string }

/** `loading` covers both "never asked yet" and "asked, still in flight" —
 *  the two are the same row state (pending) and nothing on screen tells
 *  them apart. `unavailable` is a THIRD thing: the call answered with a
 *  refusal, which still leaves every row pending but additionally earns a
 *  banner saying the scan itself could not run (see the module comment's
 *  "never the third state" rule). Reset to `loading` on every refresh —
 *  unlike FilesState and FileState below, this is CORRECT: the marks
 *  genuinely do become unknown again the moment a new scan is asked for,
 *  because the previous answer says nothing about bytes that may have
 *  changed since. */
type ScanState =
  | { kind: 'loading' }
  | {
      kind: 'ready'
      read: SkillsScan['read']
      matches: SkillsScan['matches']
      omitted: SkillsScan['omitted']
    }
  | { kind: 'unavailable'; message: string }

/** No `loading` member: FileState is never reset to a placeholder on a
 *  refresh (see loadFile's own comment) — `null` (the initial signal value)
 *  is the only "nothing to show yet" this type needs, for the one moment
 *  that is actually true: before the very first file has ever been read. */
type FileState = { kind: 'ready'; result: SkillsFile } | { kind: 'unavailable'; message: string }

const messageOf = (err: unknown): string => (err instanceof Error ? err.message : String(err))

/** Short words for why the scan skipped a file — a different register from
 *  skills-section.tsx's `omissionWords` (a whole sentence for a card's
 *  refusal paragraph), because this sits beside a dot on a dense row rather
 *  than standing alone. Exhaustive over the same closed wire union, so a
 *  fifth reason fails this switch rather than rendering nothing. */
function scanSkipWords(reason: SkillsScan['omitted'][number]['reason']): string {
  switch (reason) {
    case 'too-large':
      return 'not scanned: too large'
    case 'not-text':
      return 'not scanned: not text'
    case 'unreadable':
      return 'not scanned: unreadable'
    case 'budget-spent':
      return 'not scanned: scan budget spent'
  }
}

export function SkillViewBody(props: SkillViewBodyProps): JSX.Element {
  const [filesState, setFilesState] = createSignal<FilesState>({ kind: 'loading' })
  const [scanState, setScanState] = createSignal<ScanState>({ kind: 'loading' })
  const [fileState, setFileState] = createSignal<FileState | null>(null)
  const [selectedPath, setSelectedPath] = createSignal<string | null>(null)
  const [listWidth, setListWidth] = createSignal(DEFAULT_LIST_WIDTH)
  const [paneWidth, setPaneWidth] = createSignal(FALLBACK_PANE_WIDTH)

  let disposed = false
  let bodyEl: HTMLDivElement | undefined
  let listEl: HTMLDivElement | undefined
  let viewEl: HTMLDivElement | undefined
  // Which manifest read, which scan read, and which per-file read is
  // current — the same generation guard skills-section.tsx's
  // `fileGeneration` uses, for the same reason: a reactivation or a second
  // selection must not let a slower, earlier read land after a faster,
  // later one.
  let manifestGeneration = 0
  let scanGeneration = 0
  let fileGeneration = 0

  const filePaths = (): readonly string[] => {
    const state = filesState()
    return state.kind === 'ready' ? state.files : []
  }

  const filesRefusal = (): string => {
    const state = filesState()
    return state.kind === 'unavailable' ? state.message : ''
  }

  const scanRefusal = (): string => {
    const state = scanState()
    return state.kind === 'unavailable' ? state.message : ''
  }

  /** [180px, 40% of the pane] — the brief's own bracket. */
  const listMax = (): number => Math.max(MIN_LIST_WIDTH, paneWidth() * 0.4)

  /** Reads ONE file, on demand — never more than one in flight matters,
   *  because a later selection or a later refresh always wins the race.
   *
   *  DOES NOT BLANK THE VIEW FIRST. An earlier version set `{kind:'loading'}`
   *  here before every read, including the ones `loadManifest` issues on
   *  every `setVisible(true)` re-activation — so the file already on screen
   *  disappeared behind a placeholder for the length of a round trip on
   *  every reactivation, which is exactly the stall the two-call split
   *  exists to avoid, just moved one call over. The bytes on screen now stay
   *  exactly what they were until the NEW answer (or refusal) actually
   *  arrives; there is nothing to show only before the very first read ever
   *  completes, which `fileState`'s initial `null` already covers. */
  const loadFile = async (path: string): Promise<void> => {
    const generation = ++fileGeneration
    try {
      const result = await props.store.file(props.name, path)
      if (disposed || generation !== fileGeneration) return
      setFileState({ kind: 'ready', result })
    } catch (err) {
      if (disposed || generation !== fileGeneration) return
      setFileState({ kind: 'unavailable', message: messageOf(err) })
    }
  }

  const selectFile = (path: string): void => {
    setSelectedPath(path)
    void loadFile(path)
  }

  /** A bare directory listing — paths, the cut, nothing else — so it stays
   *  fast and the list renders whether or not the scan (below) has answered
   *  yet. Also re-reads whichever file is on screen, so an activation
   *  refreshes the view pane too (see the module comment). Keeps the
   *  current selection across a refresh when it is still there; falls back
   *  to the first file when it is not (the file was deleted) or there was
   *  no selection yet.
   *
   *  DOES NOT BLANK THE LIST FIRST. An earlier version set `{kind:'loading'}`
   *  unconditionally on every call, including the ones every `setVisible
   *  (true)` re-activation issues — so every row unmounted, and the
   *  selected file's bytes were replaced by a placeholder, for the length
   *  of a round trip on every reactivation of an already-open tab. That is
   *  the exact stall this two-call split exists to prevent, arriving
   *  through the re-read path instead of the first open. The last READY
   *  state now stays on screen until the new answer or refusal replaces it;
   *  the initial `{kind:'loading'}` the signal starts with is untouched and
   *  still covers the one moment that is genuinely true: before the very
   *  first read this tab ever makes. */
  const loadManifest = async (): Promise<void> => {
    const generation = ++manifestGeneration
    try {
      const result = await props.store.files(props.name)
      if (disposed || generation !== manifestGeneration) return
      setFilesState({
        kind: 'ready',
        files: result.files,
        truncated: result.truncated,
        maxFiles: result.maxFiles,
      })
      const current = selectedPath()
      const next =
        current !== null && result.files.includes(current) ? current : (result.files[0] ?? null)
      if (next !== null) selectFile(next)
      else setSelectedPath(null)
    } catch (err) {
      if (disposed || generation !== manifestGeneration) return
      setFilesState({ kind: 'unavailable', message: messageOf(err) })
    }
  }

  /** The live scan's own answer, fetched separately from the manifest (see
   *  the module comment for why) — never gates the file list, and never
   *  gates a selection either. RESET TO `loading` ON EVERY CALL, unlike
   *  `loadManifest`/`loadFile` above: the marks genuinely do become unknown
   *  again the instant a fresh scan is asked for, because the previous
   *  answer is about bytes that may have moved since. */
  const loadScan = async (): Promise<void> => {
    const generation = ++scanGeneration
    setScanState({ kind: 'loading' })
    try {
      const result = await props.store.scan(props.name)
      if (disposed || generation !== scanGeneration) return
      setScanState({
        kind: 'ready',
        read: result.read,
        matches: result.matches,
        omitted: result.omitted,
      })
    } catch (err) {
      if (disposed || generation !== scanGeneration) return
      setScanState({ kind: 'unavailable', message: messageOf(err) })
    }
  }

  // The one place "go read the manifest and the scan" happens (see the
  // module comment): `on` with no `defer` runs once immediately at setup —
  // replacing a separate onMount fetch — and again every time refreshToken
  // changes. The two calls are independent, so one failing must not stop
  // the other from being asked.
  createEffect(
    on(
      () => props.refreshToken,
      () => {
        void loadManifest()
        void loadScan()
      },
    ),
  )

  onMount(() => {
    // jsdom has no ResizeObserver — the guard leaves `paneWidth` at its
    // fallback, which is exactly right for a box that never changes size in
    // a test.
    if (bodyEl === undefined || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver((observed) => {
      const box = observed[0]?.contentRect
      if (box) setPaneWidth(box.width)
    })
    ro.observe(bodyEl)
    onCleanup(() => ro.disconnect())
  })

  onCleanup(() => {
    disposed = true
  })

  // The dragged width reaches the grid as a custom property, the same seam
  // api-pane.tsx's tree column uses: layout is CSS's job (ADR-0014), and
  // what a person dragged to is a value for that CSS to place, not a rule.
  // Read through Math.min against the live ceiling rather than trusting
  // ResizeHandle's own clamp, which only re-settles on a `value` change and
  // would otherwise leave the painted width stale the instant the pane
  // narrows without a drag.
  createEffect(() => {
    const el = bodyEl
    if (el === undefined) return
    const width = Math.max(MIN_LIST_WIDTH, Math.min(listWidth(), listMax()))
    el.style.setProperty('--skill-view-list-width', `${width}px`)
  })

  /** A file's mark. Three states — see the module comment for why they must
   *  never be confused: PENDING (the scan has not answered, or it failed,
   *  or this particular file was individually skipped, OR the scan's own
   *  directory walk never saw this path at all — see below) draws a neutral
   *  dot, worded by why; MATCHED draws the warning dot; CLEAN — actually
   *  read by the scan, and the scan said nothing about it — draws no mark
   *  at all, because an absent finding is not a vouch (file.go:88) and a
   *  dot for "clean" would be exactly the all-clear that fact forbids
   *  drawing.
   *
   *  CLEAN REQUIRES `read.includes(path)`, NOT MERELY "ABSENT FROM
   *  MATCHES/OMITTED". skills.files and skills.scan each walk the skill's
   *  directory at their OWN moment, through separate calls — a file created
   *  in the gap between the two is in this component's `filePaths()` but in
   *  neither the scan's `matches` nor its `omitted`, and falling through to
   *  "clean" there would be the original race rendering as an all-clear
   *  again, just through timing instead of a fan-out. `read` is what makes
   *  the distinction exact rather than probable: a path outside it was
   *  never examined by THIS scan, whatever else is true about it. */
  const matchStatus = (path: string): { tone: StatusDotTone; text: string } | undefined => {
    const scan = scanState()
    if (scan.kind !== 'ready') {
      return { tone: 'neutral', text: `${path}: scan pending` }
    }
    const omission = scan.omitted.find((o) => o.path === path)
    if (omission) return { tone: 'neutral', text: `${path}: ${scanSkipWords(omission.reason)}` }
    const matched = scan.matches.find((m) => m.path === path)
    if (matched) return { tone: 'warning', text: `${path} matched the static scan` }
    if (scan.read.includes(path)) return undefined
    return { tone: 'neutral', text: `${path}: scan pending` }
  }

  const truncationNotice = (): string => {
    const state = filesState()
    if (state.kind !== 'ready' || !state.truncated) return ''
    return `The list stops at the first ${state.maxFiles} files, and this skill carries more files than that. They are still on disk, still in a backup, and still readable — this list just does not name them, and the scan runs only over the files shown.`
  }

  const facts = (): Fact[] => {
    const path = selectedPath()
    if (path === null) return []
    const entry = fileState()
    const resolved = entry?.kind === 'ready' ? entry.result : null
    const said: Fact[] = [
      { name: 'Skill', value: resolved?.name ?? props.name },
      { name: 'File', value: resolved?.path ?? path },
    ]
    if (resolved) said.push({ name: 'Provenance', value: resolved.provenance })
    return said
  }

  const outcome = (): FileReadoutOutcome | null => {
    const entry = fileState()
    if (entry === null) return null
    if (entry.kind === 'unavailable') return { kind: 'unreadable', message: entry.message }
    return skillFileOutcome(entry.result)
  }

  const readingSentence = (): string => {
    const path = selectedPath()
    return path === null
      ? `Reading the files of “${props.name}”.`
      : `Reading ${path} of “${props.name}”.`
  }

  const readoutLabel = (): string => `${selectedPath() ?? ''} of “${props.name}”, verbatim`

  /** The row buttons in DOM order — RecordRow's title IS the row's tab stop
   *  (record-row.tsx: "the name is the control"), so this is the one place
   *  that fact is read back out, rather than a second index kept in a
   *  signal that could drift from what is actually focused. */
  const rowButtons = (): HTMLButtonElement[] =>
    listEl ? Array.from(listEl.querySelectorAll<HTMLButtonElement>('.ui-record-row__open')) : []

  const focusRow = (index: number): void => {
    const buttons = rowButtons()
    if (buttons.length === 0) return
    const clamped = Math.max(0, Math.min(index, buttons.length - 1))
    buttons[clamped]?.focus()
  }

  /**
   * ↑/↓ move a roving focus between the list's rows, Enter opens the
   * focused row and moves focus into the view — three behaviours no kit
   * list gives a caller today (CollectionRow's rows are reached one at a
   * time by Tab, which is right for a form of buttons and is not this: a
   * person scanning down a short file list). `preventDefault` owns Enter
   * completely, including in a real browser where a focused native button
   * would otherwise raise its own click from the same keydown — this
   * handler is the only place that decision is made, so the row's onClick
   * and this can never both fire for one key press.
   */
  const onListKeyDown = (e: KeyboardEvent): void => {
    const buttons = rowButtons()
    if (buttons.length === 0) return
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement)
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      focusRow(current < 0 ? 0 : current + 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      focusRow(current < 0 ? 0 : current - 1)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const path = filePaths()[current < 0 ? 0 : current]
      if (path === undefined) return
      selectFile(path)
      viewEl?.focus()
    }
  }

  return (
    <div
      class="skill-view__body"
      ref={(el) => {
        bodyEl = el
      }}
    >
      <div class="skill-view__split">
        <div class="skill-view__list-col">
          <Stack gap="loose">
            {/* THE CHECK — Task 10 fills this from content.db's stored
                check. Neutral wording on purpose: the header above already
                renders "Re-check" for a skill that HAS a stored check, and
                a placeholder claiming "not checked yet" unconditionally
                would contradict it in the same tab. It reads nothing:
                wiring it to the scan marks below would be exactly the
                "stored fact standing in for a live one" mistake the module
                comment warns against — the two are different facts with
                different sources. */}
            <Section title="Check">
              <StatusCard
                tone="neutral"
                title="Nothing here yet"
                description="Task 10 shows what content.db knows about this skill here."
              />
            </Section>
            <Section title="Files">
              <Show when={filesRefusal()}>
                <StatusCard
                  tone="danger"
                  title="What this skill carries could not be listed"
                  description={filesRefusal()}
                />
              </Show>
              <Show when={scanRefusal()}>
                {/* "never the third state": every row still carries its
                    neutral pending mark (matchStatus falls into the
                    scan.kind !== 'ready' branch), and THIS is where the
                    reason lives — a row is never quietly promoted to
                    "clean" because the scan itself could not run. */}
                <StatusCard
                  tone="warning"
                  title="The static scan could not run"
                  description={scanRefusal()}
                />
              </Show>
              <Show when={truncationNotice()}>
                <StatusCard
                  tone="warning"
                  title="This list is not the whole skill"
                  description={truncationNotice()}
                />
              </Show>
              <div
                class="skill-view__file-list"
                role="list"
                aria-label={`Files in ${props.name}`}
                onKeyDown={onListKeyDown}
                ref={(el) => {
                  listEl = el
                }}
              >
                <Stack divided dense>
                  <For each={filePaths()}>
                    {(path) => (
                      <RecordRow
                        title={path}
                        density="dense"
                        selected={selectedPath() === path}
                        status={matchStatus(path)}
                        actions={undefined}
                        onActivate={() => selectFile(path)}
                      />
                    )}
                  </For>
                </Stack>
              </div>
            </Section>
          </Stack>
        </div>
        {/* The kit's separator, placed rather than repainted — the same
            arrangement api-pane.tsx's tree column uses on its second
            caller. */}
        <div class="skill-view__seam">
          <ResizeHandle
            ariaLabel="Resize the file list"
            value={listWidth()}
            min={MIN_LIST_WIDTH}
            max={listMax()}
            onChange={setListWidth}
            onCommit={setListWidth}
          />
        </div>
        {/* tabIndex=-1: not in the Tab order, but a legitimate focus target
            for onListKeyDown's Enter to move focus INTO — the acceptance
            criterion's "focus moves to the view", read literally. */}
        <div
          class="skill-view__view-col"
          tabIndex={-1}
          ref={(el) => {
            viewEl = el
          }}
        >
          <Show
            when={outcome()}
            fallback={
              <StatusCard
                tone="neutral"
                title="Reading this file"
                description={readingSentence()}
              />
            }
          >
            {(said) => <FileReadout facts={facts()} ariaLabel={readoutLabel()} outcome={said()} />}
          </Show>
        </div>
      </div>
    </div>
  )
}
