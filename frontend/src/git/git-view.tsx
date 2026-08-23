// GitPanel's view descriptor (design §5.4): the Git activity-bar view,
// ordered after Files (-1) and Ports (0), wired in main.tsx. The descriptor
// is the whole deliverable — the store is created ONCE here and shared
// between the header action (refresh) and the panel body, exactly like the
// files and ports patterns.
//
// The store deliberately outlives the panel: the commit form lives in the
// store per binding and must survive a view switch (design §5.4), so the
// panel unmounts into setVisible(false) — polling stops — and never
// disposes. The store follows the ACTIVE tab through the reactive origin
// accessor whether or not the Git view is on screen; a hidden panel keeps
// its binding (design §5.5: sidebar collapsed → polling stops, the binding
// stays) and is fresh the moment it is seen again.

import { Show } from 'solid-js'
import type { Component } from 'solid-js'
import type { ActiveOrigin } from '../pane-content'
import type { SidebarViewDescriptor } from '../sidebar'
import { IconButton } from '../ui/icon-button'
import { RefreshIcon } from '../ui/icons'
import { SearchField } from '../ui/search-field'
import { createClipboardAccess, type ClipboardAccess } from '../clipboard'
import { createUrlOpener, type UrlOpener } from '../open-url'
import type { GitPanelServices } from './git-client'
import { createGitStore, type GitStore } from './git-store'
import { GitPanel, type GitDiffOpener } from './git-panel'
import { openGitDiff } from './git-diff/open-git-diff'

const GIT_VIEW_ID = 'git'
/** After Files (-1) and Ports (0) — the design's "third after Files and
 *  Ports". */
const GIT_VIEW_ORDER = 1

/** The activity bar's Git glyph: Lucide `git-branch` under ISC, same
 *  currentColor viewBox vocabulary as the kit icons and FilesIcon. */
const GitBranchIcon: Component = () => (
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <line x1="6" x2="6" y1="3" y2="15" />
    <circle cx="18" cy="6" r="3" />
    <circle cx="6" cy="18" r="3" />
    <path d="M18 9a9 9 0 0 1-9 9" />
  </svg>
)

export interface GitViewDeps {
  /** The panel's backend surface (createGitPanelServices(dispatcher)). */
  services: GitPanelServices
  /** The diff-tab opener; defaults to the real openGitDiff seam. Tests
   *  substitute a recorder. */
  opener?: GitDiffOpener
  /** A pre-built store. The composition root supplies its own when it must
   *  reach the store's seams (onDiffStale for the diff surface); tests
   *  supply one to keep the store observable. Defaults to a fresh store
   *  over `services`. */
  store?: GitStore
  /** The clipboard seam; defaults to the platform one, exactly like the
   *  Files view (AD-8 — one owner per behaviour). Tests substitute a
   *  recorder. */
  clipboard?: ClipboardAccess
  /** The URL-open seam; defaults to the platform one over `services`,
   *  exactly like the clipboard (AD-8 — one owner per behaviour). Tests
   *  substitute a recorder. */
  urlOpener?: UrlOpener
  /** Reactive accessor for the ACTIVE tab's origin — the coordinator wires
   *  it to PaneManager.activeOrigin() through onActivePaneChange, exactly
   *  like the files view. */
  activeOrigin: () => ActiveOrigin | null
}

/** Build the Git view descriptor. The store is created once, per
 *  descriptor, and shared between the header action (refresh) and the
 *  panel body — one signal, one backend call site. */
export function createGitView(deps: GitViewDeps): SidebarViewDescriptor {
  const opener: GitDiffOpener = deps.opener ?? { open: (target) => openGitDiff(target) }
  const store = deps.store ?? createGitStore(deps.services)

  /** THE FILTER (nocx-52by), declared for the shell's pinned slot rather
   *  than rendered in the body. It stood above the two lists it narrows and
   *  scrolled away with them, which is exactly backwards — the control that
   *  narrows a long list is the one thing that must stay reachable while you
   *  scroll it (owner, 2026-08-22). The shell pins it; this only says which
   *  child it is.
   *
   *  It closes over the same `store` the refresh action and the panel body
   *  do, so the field and the lists read one signal and cannot disagree.
   *
   *  It is present only in the states that HOLD a list: every other state is
   *  a StatusCard or an EmptyState explaining why there is nothing, and a
   *  search box above an explanation is noise (the rule the Ports panel
   *  states as `listAvailable`). The panel's own `<Match>` carried this
   *  before; now it is said here, on the same two states, from the same
   *  store. */
  const GitFilter: Component = () => (
    <Show when={store.state() === 'ready' || store.state() === 'tooManyChanges'}>
      <div class="git-filter" data-testid="git-filter">
        <SearchField
          value={store.filter()}
          onInput={(v) => store.setFilter(v)}
          placeholder="Filter files…"
          ariaLabel="Filter changed files"
          onKeyDown={(e) => {
            if (e.key === 'Escape' && store.filter() !== '') {
              e.stopPropagation()
              store.setFilter('')
            }
          }}
        />
      </div>
    </Show>
  )

  return {
    id: GIT_VIEW_ID,
    title: 'Git',
    icon: GitBranchIcon,
    filter: GitFilter,
    actions: () => (
      <IconButton
        data-testid="git-refresh"
        size="sm"
        ariaLabel="Refresh git status"
        title="Refresh git status"
        disabled={store.origin() === null}
        onClick={() => store.refresh()}
      >
        <RefreshIcon />
      </IconButton>
    ),
    view: (props) => (
      <GitPanel
        store={store}
        opener={opener}
        clipboard={deps.clipboard ?? createClipboardAccess()}
        urlOpener={deps.urlOpener ?? createUrlOpener(deps.services)}
        activeOrigin={props.activeOrigin}
        visible={props.visible}
      />
    ),
    order: GIT_VIEW_ORDER,
  }
}
