// ═══════════════════════════════════════════════════════════════════════════
// Skill surface (nocx-btg7d) — the tab a skill is read in, replacing the
// modal `Dialog` skills-section.tsx used to open one in.
//
// Two exports, both wired by main.tsx:
//
//   registerSkillSurface(registry, tm, deps) — the one wiring point.
//     Captures the PaneManager and the SkillsStore the content reads
//     through, and declares the surface id in the SurfaceRegistry.
//
//   openSkill(name) — what a skill's row (a later task) calls. Deduplication
//     is PaneManager.openPane's singletonKey behaviour; the key is
//     NAMESPACED (`skill:${name}`), because openPane matches the key ALONE
//     with no check of the surface type (panes.ts:1630) — a bare skill name
//     would collide with any other surface someday keyed by the same string.
//     Notes does the same, for the same reason, at `note:${id}`.
//
// The registry's factory cannot open a view — a view has no meaning without
// a skill to be about — so the entry exists for identity (deep links,
// surface-type constants) and build() throws loudly rather than returning a
// content that would silently read nothing, exactly as fileViewer's does.
//
// WHY A TAB, REPLACING THE DIALOG: a modal is for "answer this now", and
// reading a skill is the opposite — nobody is blocked on it. The card's own
// argument for being a modal was that reading it must not cost the page a
// person is on; a tab satisfies that better than a modal does, because it
// does not even cover the page underneath it — Settings is still there when
// the read is done. And two skills side by side, to compare one against the
// other, is a question a modal cannot be asked at all: there is exactly one
// of it.
// ═══════════════════════════════════════════════════════════════════════════

import type { PaneManager } from '../panes'
import type { SurfaceRegistry } from '../surface-registry'
import { SURFACE_ID_SKILL } from '../surface-registry'
import type { ContentDescriptor, SingletonKey, SurfaceType } from '../pane-content'
import { SkillViewContent, type SkillViewDeps } from './skill-view-content'

export type { SkillViewDeps }

// Neither surface constant is exported (matching file-viewer/index.ts, which
// keeps its own SURFACE_ID_FILE_VIEWER and SURFACE_FILE_VIEWER module-local):
// nothing outside this module needs the surface id or its wire type, and an
// export nothing imports is exactly what the dead-exports ratchet exists to
// catch.

/** Stable surface type (B.7), used in restore descriptors and deep links. */
const SURFACE_SKILL: SurfaceType = 'nocx.skill' as SurfaceType

// ── Wiring (module-level, set once by the composition root) ────────────────

interface Wiring {
  readonly tm: PaneManager
  readonly deps: SkillViewDeps
}

let wiring: Wiring | null = null

/** The one wiring point. Call exactly once, after the PaneManager and the
 *  SkillsStore exist. */
export function registerSkillSurface(
  registry: SurfaceRegistry,
  tm: PaneManager,
  deps: SkillViewDeps,
): void {
  wiring = { tm, deps }
  registry.register(SURFACE_ID_SKILL, {
    surfaceType: SURFACE_SKILL,
    singletonKey: null,
    factory: () => {
      throw new Error(
        `nocx: ${SURFACE_ID_SKILL} cannot be opened without a skill — use openSkill()`,
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
 * Open (or focus) the tab for one skill, by the name it is requested under.
 *
 * `name` is the REQUESTED name, not necessarily the resolved one: two roots
 * can discover a skill under the same name, and discovery keeps the first
 * root's copy (internal/skill/discover.go:153). SkillViewContent resolves
 * which concrete skill that is once the store answers, and holds onto that
 * resolution — never the name alone — for the rest of the tab's life (see
 * skill-view-content.tsx).
 *
 * restoreDescriptor is deliberately null, for the reason the file viewer
 * states: nothing serialises the tab list and nothing reconstructs a tab
 * from a descriptor, so a fifth writer of a field with no reader would be
 * the exact defect this repo has shipped before.
 */
export function openSkill(name: string): void {
  if (!wiring) {
    throw new Error('nocx: openSkill called before registerSkillSurface')
  }
  const descriptor: ContentDescriptor = {
    surfaceType: SURFACE_SKILL,
    singletonKey: `skill:${name}` as SingletonKey,
    restoreDescriptor: null,
    supportsAttention: false,
    defaultTitle: name,
  }
  wiring.tm.openPane(new SkillViewContent(name, wiring.deps), descriptor)
}
