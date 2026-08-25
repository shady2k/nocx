// ═══════════════════════════════════════════════════════════════════════════
// SurfaceRegistry — single declaration of every tab-surface's type, key,
// descriptor, and factory. The composition root registers a surface once;
// every entry point (sidebar, keyboard shortcut, deep link) resolves
// through the registry rather than rebuilding the same descriptor.
//
// AD-8 corollary: a registry whose consumers switch on the surface type is
// the anti-pattern. Consumers look up by a stable id and receive a fully-
// formed ContentDescriptor — there is no "type" field to branch on.
// ═══════════════════════════════════════════════════════════════════════════

import type { PaneContent, ContentDescriptor, SurfaceType, SingletonKey } from './pane-content'

// ── Surface ids ───────────────────────────────────────────────────────────
// Constants rather than bare strings at the call sites: a typo should be a
// compile error where the type system can reach it, and a loud throw from
// build() where it cannot.

export const SURFACE_ID_SETTINGS = 'settings'
export const SURFACE_ID_SANDBOX_STATISTICS = 'sandbox-statistics'

/** The API workbench (design §9.1). One pane, singleton-keyed: a request
 *  opens INSIDE it rather than as its own tab, so there is exactly one of it
 *  and the id is also what the activity bar's entry is called. */
export const SURFACE_ID_API = 'api'

// ── Registration ──────────────────────────────────────────────────────────

export interface SurfaceRegistration {
  /** Branded surface type used in restore descriptors and deep links. */
  readonly surfaceType: SurfaceType

  /** Singleton key for content types that allow at most one open tab. */
  readonly singletonKey: SingletonKey | null

  /** Factory that creates a new PaneContent instance. Called each time
   *  a consumer opens the surface, so the singleton guarantee must come
   *  from PaneManager.openPane's singletonKey dedup, not from caching here. */
  readonly factory: () => PaneContent

  /** Descriptor fields that are not surfaceType or singletonKey — those
   *  are pulled from the registration above so they are never duplicated. */
  readonly descriptor: Omit<ContentDescriptor, 'surfaceType' | 'singletonKey'>
}

// ── Registry ──────────────────────────────────────────────────────────────

export class SurfaceRegistry {
  private readonly entries = new Map<string, SurfaceRegistration>()

  /** Register a surface under a stable id. Overwrites an existing entry. */
  register(id: string, registration: SurfaceRegistration): void {
    this.entries.set(id, registration)
  }

  /** Look up a registration by id. Returns undefined if not registered —
   *  this one is a genuine lookup, unlike build() below. */
  get(id: string): SurfaceRegistration | undefined {
    return this.entries.get(id)
  }

  /** Build a full ContentDescriptor and a fresh PaneContent instance for
   *  opening a surface.
   *
   *  Throws on an unknown id rather than returning undefined. An unregistered
   *  id is a programmer error, not a runtime condition: returning undefined
   *  would put a branch in every caller and turn a typo into a keyboard
   *  shortcut that silently does nothing, which reaches the user as "it
   *  stopped working" with nothing in the log. */
  build(id: string): { content: PaneContent; descriptor: ContentDescriptor } {
    const reg = this.entries.get(id)
    if (!reg) {
      const known = [...this.entries.keys()].join(', ') || '(none)'
      throw new Error(`SurfaceRegistry: unknown surface id "${id}". Registered ids: ${known}`)
    }
    return {
      content: reg.factory(),
      descriptor: {
        surfaceType: reg.surfaceType,
        singletonKey: reg.singletonKey,
        ...reg.descriptor,
      },
    }
  }
}
