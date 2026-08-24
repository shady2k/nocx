// GitPanel — the Git sidebar view's body (design §5.4): the repository the
// shell is standing in, split into Staged and Unstaged lists, with
// stage/unstage, stage-all/unstage-all and the commit form. The store owns
// every call; the panel renders state and routes user intent into it.
//
// THE BODY, and that is why nothing here mentions `SidebarView`. The frame —
// header, header actions, pinned filter row, the one scrolling body — is the
// kit's, and it is reached through the DESCRIPTOR in git-view.tsx, which the
// shell renders inside `SidebarView` for every panel alike (sidebar.tsx
// builds it once). A panel body that imported the frame would be the second
// owner of a layout the shell already owns; what a body may say about the
// frame is which of its children is the filter, and it says that in the
// descriptor beside `actions`. Same for notes-panel.tsx (nocx-708q.3).
//
// The render rules that make it correct:
//
// 1. state() IS THE DISCRIMINATOR, SWITCHED ON FIRST — exactly one of the
//    eight render states (design §5.4), plus the two phases that scaffold
//    them (opening spinner, failed-open retry). The list bodies render
//    under ready and tooManyChanges, which are the same surface under the
//    D9 cap banner.
// 2. THE ROW IS THE KIT'S. Every row is FileStatusRow; the panel passes the
//    wire's letter and never a glyph or a colour. The stage/unstage control
//    lives in the row's actions slot and owns its click — activating it
//    never opens the diff (the kit guarantees it; the tests prove it).
// 3. WHAT THE PANEL CANNOT DO, IT DOES NOT DRAW (D14). On an SSH tab the
//    mutation controls are ABSENT from the DOM, not disabled — a disabled
//    control advertises a capability the surface does not have. While a
//    conflict is unresolved the whole-index controls refuse, VISIBLY and
//    with the reason (D19): measured, git add -A resolves the conflict
//    using the marker-laden file and bare git reset aborts the merge.
// 4. A CLICKED ROW'S DIFF TARGETS THE BINDING AS IT WAS AT THE CLICK — the
//    store's binding() read inside the handler, never a re-bound one — and
//    carries the FROZEN origin (cwdFollow:false) so the diff tab answers
//    activeOrigin() as the same machine and the panel never re-binds away
//    from the binding the tab reads through (the race-4 contract).
// 5. A FAILED COMMIT KEEPS THE MESSAGE and shows git's own output, with the
//    truncation mark when the capture bound was reached (D11); the
//    degraded-environment warning precedes the first commit (D6).
//
// The panel mounts and unmounts with the view; the STORE outlives it — the
// commit form survives a view switch, and polling stops the moment the view
// is hidden (setVisible(false) on unmount), while the binding stays
// (design §5.5).

import { createEffect, createMemo, For, Match, on, onCleanup, Show, Switch } from 'solid-js'
import type { ActiveOrigin } from '../pane-content'
import { relativeTime } from '../recall'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { CollectionRow } from '../ui/collection-view'
import { EmptyState } from '../ui/empty-state'
import { CopyIcon, ExternalLinkIcon, PlusIcon, ResetIcon } from '../ui/icons'
import { IconButton } from '../ui/icon-button'
import { Section } from '../ui/section'
import { Spinner } from '../ui/spinner'
import { StatusCard } from '../ui/status-card'
import { TextField } from '../ui/text-field'
import { matchesPathFilter } from './git-filter'
import { showToast } from '../ui/toast'
import type { FileStatus } from '../ui/file-status-row'
import { FileStatusRow } from '../ui/file-status-row'
import type { Entry } from '../generated/git.status'
import type { LogEntry as GitLogEntry } from '../generated/git.log'
import type { ClipboardAccess } from '../clipboard'
import type { UrlOpener } from '../open-url'
import { branchUrl, commitUrl } from './git-remote-url'
import type { GitDiffSide } from './git-client'
import type { GitDiffTarget } from './git-diff/open-git-diff'
import type { GitStore } from './git-store'

/** The diff-tab opener seam (worker G's surface, design §5.4): the panel
 *  calls this fixed contract and never builds the tab itself. A no-op
 *  default keeps the panel testable before the surface lands; main.tsx
 *  wires the real openGitDiff. */
export interface GitDiffOpener {
  open(target: GitDiffTarget): void
}

export interface GitPanelProps {
  store: GitStore
  /** The diff-tab opener (the seam agreed with worker G; tests substitute
   *  a recorder). */
  opener: GitDiffOpener
  /** The clipboard seam (AD-8: one owner per behaviour — the same
   *  ClipboardAccess the Files panel copies with). The write rejects when
   *  the platform refused, and the panel says so; it never swallows. */
  clipboard: ClipboardAccess
  /** The browser-open seam: the panel computes the URL and hands it to the
   *  shared capability (open-url.ts — the same class of thing as the
   *  clipboard seam, AD-8). The capability picks its platform path
   *  synchronously inside the click: window.open on web, shell.openUrl
   *  through the backend when native. It rejects when no browser could be
   *  reached and the panel says so out loud. */
  urlOpener: UrlOpener
  /** The ACTIVE tab's origin — a reactive accessor, never a capture. */
  activeOrigin: () => ActiveOrigin | null
  /** True while this view is on screen and the panel is expanded. */
  visible: () => boolean
}

/** The wire's letter for the kit row. The kit's vocabulary is the seven
 *  letters in file-status-row.tsx; porcelain v2 can also send `T`
 *  (typechange), which is a modification and reads as one — mapped here,
 *  because the kit owns the tone table and the surface may not extend it. */
function statusLetter(column: string): FileStatus {
  if (column === 'T') return 'M'
  return column as FileStatus
}

/** Which diff invocation a row deserves: an untracked row has nothing to
 *  diff against, so its side is `untracked` — git diff --no-index against
 *  /dev/null (design diff.go). */
function diffSideFor(letter: FileStatus): GitDiffSide {
  return letter === '?' ? 'untracked' : 'unstaged'
}

/** Turn one rejected mutation's reason into the sentence a person reads in
 *  the toast (nocx-8sudy — the git panel's tenth site of the error-home
 *  sweep). The wire carries the backend's own words: the git domain errors
 *  from internal/git/errors.go ride the JSON-RPC message verbatim
 *  (gitErrorCode only picks the code), an invocation failure is a fmt error
 *  carrying git's own output, and a dropped socket rejects with the
 *  transport's plain words ("closed", "ws closed", "not connected"). The
 *  mapped sentence stays a person's while the reason survives, exactly like
 *  ports.tsx's listingFailureMessage.
 *
 *  Returns null when the panel must not toast: a control-plane saturation
 *  refusal arrives with the fixed message "Control plane busy", and the
 *  dispatcher already raised its own deduplicated danger toast for it — a
 *  second toast would be the same news twice.
 *  Not mapped here: "git: unknown binding" (the store re-resolves that ONE
 *  refusal — reason "unknown-binding" on the wire, nocx-bpqil — through
 *  git.open before the error is stored; every other -32602 refusal reaches
 *  this mapper as itself) and a failed commit's OUTPUT (that is the commit
 *  state, shown in the panel, D11 — never a rejection).
 */
function mutationFailureMessage(reason: string): string | null {
  if (reason === 'Control plane busy') return null
  const r = reason.toLowerCase()
  if (
    /not connected|ws closed|closed|connection (lost|closed|reset|refused)/.test(r) ||
    r.includes('disconnected')
  ) {
    return 'The change could not be made — the connection was lost.'
  }
  if (r.includes('nothing is staged')) {
    return 'Nothing is staged to commit — stage a file first.'
  }
  if (r.includes('cannot amend')) {
    return 'This branch has no commit to amend yet.'
  }
  if (r.includes('belongs to session') || r.includes('handle released')) {
    return "The change could not be made — this view's repository is no longer available."
  }
  if (r.includes('conflicted')) {
    return 'The change could not be made while a merge conflict is unresolved.'
  }
  if (r.includes('not available')) {
    return 'The change could not be made — git is not available.'
  }
  // The open set: never pretend to a completeness we cannot have. The
  // sentence stays a person's; the reason is appended so the diagnostic
  // survives (AGENTS.md: a soft degrade must be visible in the product).
  return `The change could not be made (${reason}).`
}

type RowList = 'staged' | 'unstaged' | 'conflicted'

interface GitRow {
  key: string
  entry: Entry
  list: RowList
}

export function GitPanel(props: GitPanelProps) {
  // Re-scope on origin change: the panel follows the ACTIVE tab, and the
  // store decides whether the change re-opens (different cwd/repo) or is a
  // no-op (frozen origin, same scope — design §5.4). The accessor is read
  // INSIDE the on() source so the read is tracked.
  createEffect(
    on(
      () => props.activeOrigin(),
      (origin) => props.store.rescope(origin),
    ),
  )
  // Visibility gates polling (D13). The mount effect runs once with the
  // current value; the unmount cleanup stops the timer — the store itself
  // outlives the panel so the commit form survives a view switch.
  createEffect(
    on(
      () => props.visible(),
      (v) => props.store.setVisible(v),
    ),
  )

  // The outcome of a pressed mutation action is a Toast, raised once at
  // the moment it happens (ui/README.md: "a message about an action does
  // not live in the document flow"). The store keeps the account — it is
  // cleared by the NEXT mutation (beginMutation) — so this effect is
  // edge-triggered on the signal's VALUE, the ports pattern: `on()` fires
  // only when the value changes, so a persisted failure raises once, a
  // recovery (null, at the next mutation's start) followed by another
  // failure raises again, and a panel remount never re-toasts the same
  // failure. `null` means the failure is already visible (the dispatcher's
  // own saturation toast — mutationFailureMessage's contract).
  createEffect(
    on(
      () => props.store.mutationError(),
      (err) => {
        if (err === null) return
        const message = mutationFailureMessage(err.message)
        if (message !== null) showToast({ level: 'danger', message })
      },
    ),
  )
  onCleanup(() => props.store.setVisible(false))

  /** Open the diff for the row the user clicked, against the binding as it
   *  is at the moment of the click — never a binding the panel re-bound to
   *  since the row rendered (race 4). The origin rides along FROZEN: a diff
   *  tab has no opinion about where the shell is now, and the content
   *  answering activeOrigin() with it is what keeps the panel bound to the
   *  repository the tab reads through. */
  const openDiff = (row: GitRow): void => {
    const b = props.store.binding()
    const o = props.store.origin()
    if (b === null || o === null) return
    const side: GitDiffSide =
      row.list === 'staged' ? 'staged' : diffSideFor(statusLetter(row.entry.y))
    props.opener.open({
      bindingId: b.bindingId,
      toplevel: b.toplevel,
      path: row.entry.path,
      side,
      origin: {
        sessionId: o.sessionId,
        kind: o.kind,
        cwd: o.cwd,
        cwdVerified: o.cwdVerified,
        host: o.host,
        cwdFollow: false,
      },
    })
  }

  // The rows are keyed {list, path} — a file with both columns non-'.' is
  // legitimately two rows (design §5.1 "porcelain.go"), so the path alone
  // is not a key.
  const stagedRows = createMemo<GitRow[]>(() =>
    (props.store.status()?.staged ?? []).map((entry) => ({
      key: `staged:${entry.path}`,
      entry,
      list: 'staged' as const,
    })),
  )
  const unstagedRows = createMemo<GitRow[]>(() =>
    (props.store.status()?.unstaged ?? []).map((entry) => ({
      key: `unstaged:${entry.path}`,
      entry,
      list: 'unstaged' as const,
    })),
  )
  const conflictedRows = createMemo<GitRow[]>(() =>
    (props.store.status()?.conflicted ?? []).map((entry) => ({
      key: `conflicted:${entry.path}`,
      entry,
      list: 'conflicted' as const,
    })),
  )

  // ── The path filter (nocx-52by) ───────────────────────────────────────
  // One predicate in one place (git-filter.ts): case-insensitive substring
  // over the repository-relative path, so a directory name matches a row
  // that renders the file name first (nocx-uf0p). Renderer-side over the
  // status the store already holds — typing never issues a request, so it
  // cannot churn the D17 scope or the D13 poll. The filter applies to ALL
  // three lists (staged, unstaged, conflicted): it is one rule, and a
  // conflicted row that does not match is no more "matching" than any
  // other row.
  const filter = createMemo(() => props.store.filter())
  const filterActive = createMemo(() => filter().trim() !== '')
  const filteredStaged = createMemo<GitRow[]>(() =>
    stagedRows().filter((row) => matchesPathFilter(row.entry.path, filter())),
  )
  const filteredUnstaged = createMemo<GitRow[]>(() =>
    unstagedRows().filter((row) => matchesPathFilter(row.entry.path, filter())),
  )
  const filteredConflicted = createMemo<GitRow[]>(() =>
    conflictedRows().filter((row) => matchesPathFilter(row.entry.path, filter())),
  )
  // The section headings count the rows ON SCREEN — what matches the
  // filter — so a heading never lies about the list it heads. With no
  // filter that equals what exists; with one, the visible filter box above
  // explains the difference, and the header's "N changed" stays the wire's
  // total (the repository's word, not the list's).
  const stagedEmptyState = createMemo(() => filterActive() && filteredStaged().length === 0)
  const unstagedEmptyState = createMemo(() => filterActive() && filteredUnstaged().length === 0)
  const conflictedEmptyState = createMemo(() => filterActive() && filteredConflicted().length === 0)

  /** Distinct files on screen — the "first M" of the capped banner (D9):
   *  a file in both lists is one record shown twice. */
  const shownCount = createMemo(() => {
    const seen = new Set<string>()
    for (const row of stagedRows()) seen.add(row.entry.path)
    for (const row of unstagedRows()) seen.add(row.entry.path)
    for (const row of conflictedRows()) seen.add(row.entry.path)
    return seen.size
  })

  // The live status: read inside a tracked scope so the rows re-render on
  // every applied status (the Solid gate's accessor-inside-memo pattern).
  const status = createMemo(() => props.store.status())
  const branchLabel = createMemo(() => {
    const st = props.store.status()
    if (st === null) return ''
    if (st.unborn) return 'no commits yet'
    if (st.detached) return st.head === '' ? 'detached' : `detached @ ${st.head}`
    return st.branch
  })
  const upstreamLabel = createMemo(() => {
    const st = props.store.status()
    if (st === null || st.upstream === '') return ''
    return `${st.upstream} \u2191${st.ahead} \u2193${st.behind}`
  })

  /** The branch name a copy produces: the real branch, never the
   *  "no commits yet" label and never a detached HEAD (which has no
   *  branch name — the porcelain carries none, and D14 says what the
   *  panel cannot do, it does not draw). On an unborn branch the branch
   *  IS copyable: it is the name git will create. */
  const branchToCopy = createMemo(() => {
    const st = props.store.status()
    if (st === null || st.detached) return null
    return st.branch === '' ? null : st.branch
  })

  /** The branch's hosting URL, or null when the remote is not a
   *  recognised web host. This is the D14 gate for BOTH open
   *  affordances: an absent link is absent from the DOM, never a
   *  disabled control advertising a capability the surface does not
   *  have. */
  const branchOpenUrl = createMemo(() => {
    const st = props.store.status()
    const remote = props.store.remoteUrl()
    if (st === null || remote === null) return null
    return branchUrl(remote, st.branch)
  })

  /** The one browser-open path: the URL is already derived and
   *  recognised (branchUrl/commitUrl returned it), so the only failure
   *  left is the browser's — a popup blocked on web, the backend refusing
   *  when native — and the panel says so out loud. The open itself is
   *  synchronous inside this handler on the web path (open-url.ts: an
   *  awaited open would lose the click's user gesture to popup blockers);
   *  only the failure report is a promise. */
  const openExternal = (url: string): void => {
    props.urlOpener.open(url).catch(() => {
      showToast({ level: 'danger', message: "Couldn't open the link in your browser" })
    })
  }

  /** Copy the branch name in one action, confirming with a toast — the
   *  clipboard write is the seam's (AD-8), and a refused write is told,
   *  never swallowed. */
  const copyBranch = (): void => {
    const name = branchToCopy()
    if (name === null) return
    props.clipboard.writeText(name).then(
      () => showToast({ level: 'success', message: 'Branch name copied' }),
      () => showToast({ level: 'danger', message: "Couldn't copy the branch name" }),
    )
  }

  /** Open the current branch on its hosting, derived from the remote git
   *  reported — never a URL the panel invented. */
  const openBranch = (): void => {
    const st = props.store.status()
    const remote = props.store.remoteUrl()
    if (st === null || remote === null) return
    const url = branchUrl(remote, st.branch)
    if (url === null) return
    openExternal(url)
  }

  /** Open one commit on its hosting, the same way. */
  const openCommit = (hash: string): void => {
    const remote = props.store.remoteUrl()
    if (remote === null) return
    const url = commitUrl(remote, hash)
    if (url === null) return
    openExternal(url)
  }

  /** The commit row's actions: the open-on-hosting link, absent from the
   *  DOM when no recognised remote exists (D14) — never disabled. */
  const commitActions = (entry: GitLogEntry) => {
    if (props.store.remoteUrl() === null) return undefined
    return (
      <IconButton
        size="xs"
        data-testid="git-open-commit"
        ariaLabel={`Open commit ${entry.shortHash} on its hosting`}
        title="Open commit on its hosting"
        onClick={(e: MouseEvent) => {
          e.stopPropagation()
          openCommit(entry.hash)
        }}
      >
        <ExternalLinkIcon />
      </IconButton>
    )
  }
  const capBanner = createMemo(() => {
    const st = props.store.status()
    if (st === null || st.completeness === 'complete') return null
    if (st.completeness === 'capped') {
      return `${st.total} changes, showing the first ${shownCount()}`
    }
    return `more than ${st.total} changes`
  })

  const canCommit = createMemo(() => {
    const st = props.store.status()
    const subject = props.store.commitSubject().trim()
    return subject !== '' && st !== null && st.staged.length > 0
  })

  const commitDisabled = () =>
    !canCommit() || props.store.mutationInFlight() || props.store.commitState() === 'running'

  /** The stage/unstage control in the row's actions slot. It owns its
   *  click: activating it never activates the row. The containment the kit
   *  guarantees is checked when the click BUBBLES to the row — but issuing
   *  the mutation flips mutationInFlight synchronously, which re-renders
   *  this row with no action and detaches the button mid-dispatch, so by
   *  the time the event reaches the row the containment check has nothing
   *  left to contain. The handler therefore stops propagation before the
   *  store call — the TreeRow disclosure pattern the CollectionRow comment
   *  names ("there the button stops propagation"). */
  const rowAction = (row: GitRow) => {
    if (props.store.mutationInFlight()) return undefined
    if (row.list === 'staged') {
      return (
        <IconButton
          data-testid="git-row-unstage"
          size="xs"
          ariaLabel={`Unstage ${row.entry.path}`}
          title="Unstage"
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            props.store.unstage([row.entry.path])
          }}
        >
          <ResetIcon />
        </IconButton>
      )
    }
    if (row.list === 'unstaged') {
      return (
        <IconButton
          data-testid="git-row-stage"
          size="xs"
          ariaLabel={`Stage ${row.entry.path}`}
          title="Stage"
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            props.store.stage([row.entry.path])
          }}
        >
          <PlusIcon />
        </IconButton>
      )
    }
    return undefined
  }

  const renderRow = (row: GitRow) => {
    const letter =
      row.list === 'staged'
        ? statusLetter(row.entry.x)
        : row.list === 'unstaged'
          ? statusLetter(row.entry.y)
          : ('U' as FileStatus)
    // A conflicted row is not stageable from the panel and has nothing to
    // diff (conflicts as a surface are out of scope) — it shows with its
    // status letter and no actions.
    const activatable = row.list !== 'conflicted'
    return (
      <FileStatusRow
        path={row.entry.path}
        status={letter}
        // The wire's counts pass straight through: the panel decides
        // nothing about them. Absent means "no count exists" (untracked,
        // binary, conflicted, bounded-out read) and the kit renders
        // nothing — never +0 −0 (brief nocx-i4ki).
        added={row.entry.added}
        deleted={row.entry.deleted}
        onActivate={activatable ? () => openDiff(row) : undefined}
        actions={rowAction(row)}
      />
    )
  }
  /** One commit of the Commits list (brief, git.log): the subject on the
   *  primary line, then the short hash, the relative time and the refs
   *  pointing at it. The row is the kit's CollectionRow in its dense
   *  variant; the refs are the kit's Badge — the surface composes kit
   *  parts and repaints none of them. A bare HEAD ref is a detached HEAD,
   *  and the info tone is what says it out loud. The actions slot holds
   *  the open-on-hosting link when the remote is recognised, and nothing
   *  at all when it is not (D14). */
  const renderCommit = (entry: GitLogEntry) => (
    <CollectionRow
      density="dense"
      actions={commitActions(entry)}
      info={
        <div class="git-log-row" data-testid="git-log-row">
          <span class="git-log-row__subject" title={entry.subject}>
            {entry.subject}
          </span>
          <span class="git-log-row__meta">
            <span class="git-log-row__hash">{entry.shortHash}</span>
            <span class="git-log-row__time">
              {/* The wall clock, like every timestamp this product renders:
                  relativeTime is the recall overlay's one owner (AD-8). */}
              {relativeTime(Date.parse(entry.authoredAt), Date.now())}
            </span>
            <For each={entry.refs}>
              {(ref) => (
                <Badge tone={ref === 'HEAD' ? 'info' : 'neutral'} data-testid="git-log-ref">
                  {ref}
                </Badge>
              )}
            </For>
          </span>
        </div>
      }
    />
  )

  /** The D9 half of the Commits section: a bounded log must say which of
   *  the two answers it is. Capped means more commits exist than the list
   *  holds (the extra record was the proof); cut means the read was
   *  interrupted at the work ceiling and Total is a lower bound. */
  const logCapBanner = createMemo(() => {
    const lg = props.store.log()
    if (lg === null || lg.completeness === 'complete') return null
    if (lg.completeness === 'capped') return `More than ${lg.entries.length} commits`
    return `More than ${lg.total} commits`
  })

  /** The one action an empty filtered list offers: drop the filter. A
   *  FUNCTION, not an element — Solid mounts a JSX expression once, and the
   *  same element instance cannot sit in two sections at once; each empty
   *  section must own its button. Three sections, one recovery. */
  const clearFilterAction = () => (
    <Button size="sm" data-testid="git-filter-clear" onClick={() => props.store.setFilter('')}>
      Clear filter
    </Button>
  )

  const mutationBusy = () => props.store.mutationInFlight()

  return (
    <div class="git-panel" data-testid="git-panel">
      <Switch>
        <Match when={props.store.phase() === 'opening'}>
          <div class="git-loading" data-testid="git-loading">
            <Spinner label="Opening repository" />
          </div>
        </Match>
        <Match when={props.store.phase() === 'failed'}>
          <div class="git-error" data-testid="git-error">
            <StatusCard
              tone="danger"
              title="Could not reach git"
              description={props.store.openError() ?? undefined}
              action={
                <Button
                  size="sm"
                  data-testid="git-retry-open"
                  onClick={() => props.store.refresh()}
                >
                  Retry
                </Button>
              }
            />
          </div>
        </Match>
        <Match when={props.store.state() === 'noPane'}>
          <EmptyState
            title="No repository to show"
            description="Focus a terminal tab to see the repository your shell is standing in."
          />
        </Match>
        <Match when={props.store.state() === 'remote'}>
          <EmptyState
            title="Git on a remote host isn't supported yet"
            description="The git panel is local-only for now — remote repositories wait on the relay."
          />
        </Match>
        <Match when={props.store.state() === 'noCwd'}>
          <StatusCard
            tone="neutral"
            title="No verified working directory"
            description="This session has no shell integration, so nocx does not know where the shell is standing. Start the shell in a repository to see its status here."
          />
        </Match>
        <Match when={props.store.state() === 'notARepository'}>
          <StatusCard
            tone="neutral"
            title="Not a git repository"
            description={
              props.store.origin()?.cwd !== null && props.store.origin()?.cwd !== undefined
                ? `${props.store.origin()?.cwd} is not inside a git repository.`
                : 'The shell is not inside a git repository.'
            }
          />
        </Match>
        <Match when={props.store.state() === 'gitUnavailable'}>
          <StatusCard
            tone="warning"
            title="git is not installed"
            description="Install git (or add it to PATH) and refresh — the panel runs the same git your terminal would."
          />
        </Match>
        <Match when={props.store.state() === 'gitTooOld'}>
          <StatusCard
            tone="warning"
            title="git is too old"
            description={`Found ${props.store.gitVersion() ?? 'an unknown version'} — the panel needs git 2.25 or newer.`}
          />
        </Match>
        <Match when={props.store.state() === 'ready' || props.store.state() === 'tooManyChanges'}>
          {/* ── Header: branch, upstream, changed count ───────────────── */}
          <Show when={status() !== null}>
            {/* One line each, in that order: the branch, its upstream, then
                the count. The branch is TEXT, not a chip — a badge is a fixed
                little shape for a short word, and a branch name is neither
                short nor bounded, so `fix/e2e-files-reveal-and-container`
                filled the header three lines deep before the kit was taught
                that a chip is one line. And nothing shares the branch's line
                but its own copy control: the count sitting beside it took a
                fixed ~80px out of the one string in this header that has to
                be read in full, which is how a branch clipped at
                `fix/e2e-files-revea…` while its own row was half empty. */}
            <div class="git-header" data-testid="git-header">
              <div class="git-header__line">
                <span class="git-header__branch" data-testid="git-branch" title={branchLabel()}>
                  {branchLabel()}
                </span>
                {/* The copy affordance, on the branch, the way orca draws
                    it. Absent when there is no branch name to copy — a
                    detached HEAD has none (D14). */}
                <Show when={branchToCopy() !== null}>
                  <IconButton
                    size="xs"
                    data-testid="git-copy-branch"
                    ariaLabel="Copy branch name"
                    title="Copy branch name"
                    onClick={copyBranch}
                  >
                    <CopyIcon />
                  </IconButton>
                </Show>
              </div>
              <Show when={upstreamLabel() !== ''}>
                <div class="git-header__line">
                  <span
                    class="git-header__upstream"
                    data-testid="git-upstream"
                    title={upstreamLabel()}
                  >
                    {upstreamLabel()}
                  </span>
                  {/* The open-on-hosting link, beside the upstream, the way
                      orca draws it. Absent — never disabled — when the
                      remote is not a recognised web host (D14): a
                      local-path remote and an unknown host produce no
                      link, because the panel does not guess URLs. */}
                  <Show when={branchOpenUrl() !== null}>
                    <IconButton
                      size="xs"
                      data-testid="git-open-branch"
                      ariaLabel="Open branch on its hosting"
                      title="Open branch on its hosting"
                      onClick={openBranch}
                    >
                      <ExternalLinkIcon />
                    </IconButton>
                  </Show>
                </div>
              </Show>
              <span class="git-header__count" data-testid="git-changed-count">
                {status()!.total} changed
              </span>
            </div>
          </Show>
          {/* ── The D9 cap banner: a traversal that could not be completed
               must not look complete. ─────────────────────────────────── */}
          <Show when={capBanner() !== null}>
            <div class="git-cap-banner" data-testid="git-too-many-changes">
              {capBanner()}
            </div>
          </Show>
          {/* ── A failed poll/mutation status read: stale, never fresh. ── */}
          <Show when={props.store.statusStale()}>
            <div class="git-stale" data-testid="git-status-stale">
              <span>Status may be stale — the last refresh failed.</span>
              <Button
                size="sm"
                data-testid="git-stale-refresh"
                onClick={() => props.store.refresh()}
              >
                Refresh
              </Button>
            </div>
          </Show>
          {/* ── Whole-index controls. Refused, visibly, while any entry is
               conflicted (D19) — absent entirely on SSH (D14, the remote
               state never reaches this branch). ──────────────────────── */}
          {/* Named, not glyphed. Two bare icons under the header read as
              decoration — nothing on screen says which one stages and which
              one throws the index away, and the second is the one a user
              must not press by accident. Both reference products label
              these. */}
          <div class="git-list-actions" data-testid="git-list-actions">
            <Button
              data-testid="git-stage-all"
              size="sm"
              title="Stage all changes"
              disabled={mutationBusy() || props.store.conflictsPresent()}
              onClick={() => props.store.stageAll()}
            >
              Stage all
            </Button>
            <Button
              data-testid="git-unstage-all"
              size="sm"
              title="Unstage all changes"
              disabled={mutationBusy() || props.store.conflictsPresent()}
              onClick={() => props.store.unstageAll()}
            >
              Unstage all
            </Button>
          </div>
          <Show when={props.store.conflictsPresent()}>
            <p class="git-conflict-refusal" data-testid="git-conflict-refusal">
              Unresolved merge conflicts — stage-all and unstage-all are disabled until the conflict
              is resolved.
            </p>
          </Show>
          {/* THE FILTER IS NOT HERE. It stood exactly at this point, above
               the two lists it narrows, and it went up and off the panel
               with them the moment anybody scrolled — the whole reason a
               filter exists is to be reachable while you scroll a long list
               (owner, 2026-08-22). It is `GitFilter` in git-view.tsx now,
               declared on the descriptor and pinned by the shell, reading
               the same `store.filter()` this panel narrows by. Nothing else
               about it moved: typing still narrows rows already in the store
               and issues no request, and a collapsed section stays collapsed
               because the filter narrows lists while the disclosure hides
               them (nocx-nak2). */}
          {/* ── The two lists ─────────────────────────────────────────── */}
          <Section
            title={`Staged (${filteredStaged().length})`}
            dense
            collapsible
            open={props.store.sectionOpen('staged')}
            onToggle={() => props.store.toggleSection('staged')}
          >
            <Show when={stagedEmptyState()}>
              {/* A filter that matches nothing is a state, never a blank:
                  the section says so, and offers the one recovery. */}
              <EmptyState title="No staged files match" action={clearFilterAction()} />
            </Show>
            <Show when={!stagedEmptyState()}>
              <div
                class="git-list"
                role="list"
                aria-label="Staged changes"
                data-testid="git-staged-list"
              >
                <For each={filteredStaged()}>{(row) => renderRow(row)}</For>
              </div>
            </Show>
          </Section>
          <Section
            title={`Unstaged (${filteredUnstaged().length})`}
            dense
            collapsible
            open={props.store.sectionOpen('unstaged')}
            onToggle={() => props.store.toggleSection('unstaged')}
          >
            <Show when={unstagedEmptyState()}>
              <EmptyState title="No unstaged files match" action={clearFilterAction()} />
            </Show>
            <Show when={!unstagedEmptyState()}>
              <div
                class="git-list"
                role="list"
                aria-label="Unstaged changes"
                data-testid="git-unstaged-list"
              >
                <For each={filteredUnstaged()}>{(row) => renderRow(row)}</For>
              </div>
            </Show>
          </Section>
          <Show when={conflictedRows().length > 0}>
            {/* The section stays when a conflict exists even if the filter
                hides its rows — a conflict is a state that must stay in
                sight (nocx-nak2) — while the filter still applies to it
                like any other list. */}
            <Section title={`Conflicted (${filteredConflicted().length})`} dense>
              <Show when={conflictedEmptyState()}>
                <EmptyState title="No conflicted files match" action={clearFilterAction()} />
              </Show>
              <Show when={!conflictedEmptyState()}>
                <div
                  class="git-list"
                  role="list"
                  aria-label="Conflicted files"
                  data-testid="git-conflicted-list"
                >
                  <For each={filteredConflicted()}>{(row) => renderRow(row)}</For>
                </div>
              </Show>
            </Section>
          </Show>
          {/* ── The commit form (design §5.4, D11, D6) ─────────────────── */}
          <Section title="Commit" dense>
            {/* The form is a form, not a list. `dense` gives Section's Stack
                `gap: 0`, which is right for rows that carry their own padding
                and wrong for a column of fields, a checkbox, a warning and a
                button — they arrived touching each other. The surface owns
                its own vertical rhythm here; it places, it does not repaint. */}
            <div class="git-commit-form">
              <TextField
                id="git-commit-subject"
                label="Subject"
                placeholder="Commit subject"
                value={props.store.commitSubject()}
                onInput={(v) => props.store.setCommitSubject(v)}
              />
              <TextField
                id="git-commit-body"
                label="Body"
                multiline
                placeholder="Commit body"
                value={props.store.commitBody()}
                onInput={(v) => props.store.setCommitBody(v)}
              />
              <Checkbox
                checked={props.store.amend()}
                onChange={() => props.store.toggleAmend()}
                label="Amend last commit"
              />
              <Show when={props.store.envState() === 'degraded'}>
                <div class="git-env-warning" data-testid="git-env-degraded">
                  Hooks will run in a degraded environment:{' '}
                  {props.store.envReason() ?? 'the shell environment could not be resolved'}
                </div>
              </Show>
              <Show
                when={props.store.commitState() === 'failed' && props.store.commitOutput() !== null}
              >
                <div class="git-commit-output" data-testid="git-commit-output">
                  <pre>{props.store.commitOutput()!.output}</pre>
                  <Show when={props.store.commitOutput()!.truncated}>
                    <span data-testid="git-commit-output-truncated">(output truncated)</span>
                  </Show>
                </div>
              </Show>
              <Button
                variant="primary"
                data-testid="git-commit"
                disabled={commitDisabled()}
                onClick={() => props.store.commit()}
              >
                Commit
              </Button>
            </div>
          </Section>

          {/* ── Commits (brief, git.log): what already happened, newest
               first, so a user confirms the commit they just made without
               leaving the panel. Read when the panel opens, on refresh and
               after a commit — never on the poll (D13). ─────────────── */}
          <Section
            title="Commits"
            dense
            collapsible
            open={props.store.sectionOpen('commits')}
            onToggle={() => props.store.toggleSection('commits')}
          >
            <div class="git-log" role="list" aria-label="Recent commits" data-testid="git-log">
              <Show when={props.store.logState() === 'loading'}>
                <p class="git-log__state" data-testid="git-log-loading">
                  Loading commits…
                </p>
              </Show>
              <Show when={props.store.logState() === 'failed' && props.store.logError() !== null}>
                <div class="git-log__state" data-testid="git-log-failed">
                  <span>Couldn't load commits — {props.store.logError()}</span>
                  <Button
                    size="sm"
                    data-testid="git-log-retry"
                    onClick={() => props.store.refresh()}
                  >
                    Retry
                  </Button>
                </div>
              </Show>
              <Show when={props.store.log() !== null && logCapBanner() !== null}>
                <p class="git-log__cap" data-testid="git-log-capped">
                  {logCapBanner()}
                </p>
              </Show>
              <Show
                when={
                  props.store.logState() === 'loaded' &&
                  props.store.log() !== null &&
                  props.store.log()!.entries.length === 0
                }
              >
                <p class="git-log__state" data-testid="git-log-empty">
                  No commits yet
                </p>
              </Show>
              <For each={props.store.log()?.entries ?? []}>{(entry) => renderCommit(entry)}</For>
            </div>
          </Section>
        </Match>
      </Switch>
    </div>
  )
}
