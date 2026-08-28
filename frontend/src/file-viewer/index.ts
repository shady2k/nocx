// ═══════════════════════════════════════════════════════════════════════════
// File viewer surface (fm-w7) — public seam for the composition root.
//
// Two exports, both wired by main.tsx:
//
//   registerFileViewerSurface(registry, tm, deps) — the one wiring point.
//     Captures the PaneManager and the read/liveness dependencies the content
//     needs, and declares the surface id in the SurfaceRegistry. The binding
//     registry (and its D6 endpoint-match policy) lives at the call site and
//     is passed in as the liveness seam — this module never imports it.
//
//   openFileViewer(target) — what the Files panel calls. Deduplication is
//     PaneManager.openPane's singletonKey behaviour; the key is built from the
//     CANONICAL path (D12), so two symlinks to one file are one tab.
//
// The registry's factory cannot open a viewer — a viewer has no meaning
// without a target — so the entry exists for identity (deep links, docs,
// surface-type constants) and build() throws loudly rather than returning a
// content that would silently read nothing.
// ═══════════════════════════════════════════════════════════════════════════

import type { PaneManager } from '../panes'
import type { SurfaceRegistry } from '../surface-registry'
import type { ContentDescriptor, SingletonKey, SurfaceType } from '../pane-content'
import {
  FileViewerContent,
  type FileViewerDeps,
  type FileViewerTarget,
} from './file-viewer-content'

export type { FileViewerDeps, FileViewerTarget }

/** Stable surface id: the registry key for this surface. */
const SURFACE_ID_FILE_VIEWER = 'fileViewer'

/** Stable surface type (B.7), used in restore descriptors and deep links. */
const SURFACE_FILE_VIEWER: SurfaceType = 'nocx.fileViewer' as SurfaceType

// ── Wiring (module-level, set once by the composition root) ────────────────

interface Wiring {
  readonly tm: PaneManager
  readonly deps: FileViewerDeps
}

let wiring: Wiring | null = null

/**
 * The one wiring point. Call exactly once, after the PaneManager and the
 * caller's binding registry exist.
 *
 * `deps.onBindingLiveness` must invoke its callback synchronously with the
 * binding's current state and on every later transition (the content relies
 * on the synchronous first call to decide whether to read at all).
 */
export function registerFileViewerSurface(
  registry: SurfaceRegistry,
  tm: PaneManager,
  deps: FileViewerDeps,
): void {
  wiring = { tm, deps }
  registry.register(SURFACE_ID_FILE_VIEWER, {
    surfaceType: SURFACE_FILE_VIEWER,
    singletonKey: null,
    factory: () => {
      throw new Error(
        `nocx: ${SURFACE_ID_FILE_VIEWER} cannot be opened without a target — use openFileViewer()`,
      )
    },
    descriptor: {
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: '',
    },
  })
}

/**
 * Open (or focus) a viewer tab for one file.
 *
 * The singletonKey is `${endpointId ?? 'local'}:${canonical}` — the CANONICAL
 * path, never the lexical one: two symlinks to one file are one file (D12),
 * and using `path` here would open two tabs claiming to be different.
 *
 * restoreDescriptor is deliberately null. The field is written in four
 * places, typed unknown, and read nowhere — nothing serialises the tab list
 * and nothing reconstructs a tab from a descriptor. Adding a fifth writer
 * would commit the exact defect this repo has shipped before. When tab
 * restore grows a reader, the shape it should adopt is
 * `{type:'file', endpointId, path, displayHost}`.
 */
export function openFileViewer(target: FileViewerTarget): void {
  if (!wiring) {
    throw new Error('nocx: openFileViewer called before registerFileViewerSurface')
  }
  const singletonKey: SingletonKey =
    `${target.endpointId ?? 'local'}:${target.canonical}` as SingletonKey
  const descriptor: ContentDescriptor = {
    surfaceType: SURFACE_FILE_VIEWER,
    singletonKey,
    restoreDescriptor: null,
    supportsAttention: false,
    // Provenance rides the title, asymmetrically (spec §5.4): a remote file
    // is "srv-01 · nginx.conf", a local file is the basename alone. Absence
    // of a host marker is what means "this machine", so the marker is never
    // spent on the local case.
    defaultTitle: target.displayHost ? `${target.displayHost} · ${target.name}` : target.name,
  }
  const content = new FileViewerContent(target, wiring.deps)
  const pane = wiring.tm.openPane(content, descriptor)
  // A file is ONE tab (the singleton key is the canonical path), so a second
  // link to the same file at a different line lands here with the tab already
  // open and `content` thrown away. The jump then has to be asked for
  // explicitly — openPane only activated the tab, and a tab that comes to the
  // front still showing line 10 is how "click did nothing" looks.
  if (target.line !== undefined && pane.content !== content) {
    if (pane.content instanceof FileViewerContent) pane.content.revealLine(target.line)
  }
}
