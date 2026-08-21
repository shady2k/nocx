// The API workbench surface — the public seam for the composition root.
//
// Wiring lives here rather than in main.tsx for the reason the file viewer's
// and the notes surface's do: the singleton key is this module's decision, and
// the entry that opens the pane is part of the surface rather than of the
// shell. main.tsx registers it and places the entry; it decides nothing about
// either.
//
// The entry is an ACTION, not a view (sidebar.tsx's two zones): it opens a tab
// and never touches the side panel, which is what design §9.2 asks for — "the
// activity-bar icon opens or focuses the workbench pane and does not expand
// the side panel" — and what the Settings gear already does. The tree lives in
// the workbench and is deliberately not duplicated into a panel view: two
// trees would be two owners of one selection.

import type { ContentDescriptor } from '../pane-content'
import type { PaneManager } from '../panes'
import type { SidebarAction } from '../sidebar'
import { SurfaceRegistry, SURFACE_ID_API } from '../surface-registry'
import { ArrowRightLeftIcon } from '../ui/icons'
import { ApiContent, API_PANE_TITLE, SINGLETON_API, SURFACE_API } from './api-content'
import type { ApiWorkbenchServices } from './api-client'

interface Wiring {
  readonly tm: PaneManager
  readonly registry: SurfaceRegistry
}

let wiring: Wiring | null = null

/**
 * The one wiring point. Call once, after the PaneManager and the dispatcher
 * exist.
 *
 * restoreDescriptor is deliberately null, for the reason the file viewer
 * states: nothing serialises the tab list and nothing reconstructs a tab from
 * a descriptor, so another writer of a field with no reader would be the
 * defect this repo has shipped before.
 */
export function registerApiSurface(
  registry: SurfaceRegistry,
  tm: PaneManager,
  services: ApiWorkbenchServices,
): void {
  wiring = { tm, registry }
  registry.register(SURFACE_ID_API, {
    surfaceType: SURFACE_API,
    singletonKey: SINGLETON_API,
    factory: () => new ApiContent(services),
    descriptor: {
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: API_PANE_TITLE,
    },
  })
}

/**
 * Open — or focus — the workbench, and hand back the instance that is
 * actually on screen.
 *
 * `openPane` deduplicates on the singleton key, so when the workbench is
 * already open the content just built is discarded and the live instance is
 * the existing tab's. Talking to the one we built would have addressed a
 * surface nobody can see — silently, and only on the second invocation
 * (openSettingsPane records the same trap).
 */
export function openApiWorkbench(): ApiContent {
  if (!wiring) throw new Error('nocx: openApiWorkbench called before registerApiSurface')
  const built = wiring.registry.build(SURFACE_ID_API) as {
    content: ApiContent
    descriptor: ContentDescriptor
  }
  const live = wiring.tm.openPane(built.content, built.descriptor).content
  if (!(live instanceof ApiContent)) {
    throw new Error('nocx: the API singleton is not an ApiContent')
  }
  return live
}

/** The activity bar's bottom-zone entry. Declared here so the shell places it
 *  and decides nothing about it.
 *
 *  Its glyph is the kit's `ArrowRightLeftIcon` — a request out and a response
 *  back. It was `ArrowRightIcon`, chosen with no recorded reason, and a
 *  single arrow is what every other rail in this product uses to mean "go
 *  there": the entry read as navigation (nocx-zccer). Its words are
 *  API_PANE_TITLE, the same constant the pane hands the strip, so the rail
 *  and the tab cannot drift apart. */
export function apiSidebarAction(): SidebarAction {
  return {
    id: SURFACE_ID_API,
    title: API_PANE_TITLE,
    icon: ArrowRightLeftIcon,
    onActivate: () => {
      openApiWorkbench()
    },
  }
}
