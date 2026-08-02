/**
 * profiles-model — framework‑neutral connection profiles list state.
 *
 * Derived from: frontend/src/connections.ts
 *   ConnectionManagerViewImpl.profiles → .profiles  (line 29)
 *   ConnectionManagerViewImpl.groups → .groups  (line 30)
 *
 * View‑level state (selectedID, editing) is deliberately NOT modeled here —
 * those are form/selection concerns that belong in the view layer, not in
 * the application state module.
 *
 * Authority:
 *   Both lists are written only by ConnectionManagerViewImpl.refresh(),
 *   which reads them from the backend via ProfileClient.
 *
 * Terminal render state is NOT modeled (AD-6).
 */

import type { ProfileGroup, SSHProfile } from '../profiles'

// ── Types ──────────────────────────────────────────────────────────────────

/**
 * Connection profiles list state.
 *
 * Derived from: `ConnectionManagerViewImpl` class (connections.ts:26-893)
 *   .profiles (line 29)
 *   .groups (line 30)
 */
export interface ProfileLists {
  readonly profiles: readonly SSHProfile[]
  readonly groups: readonly ProfileGroup[]
}

// ── Factory ─────────────────────────────────────────────────────────────────

/** Create an empty profile lists container. */
export function createProfileLists(): ProfileLists {
  return { profiles: [], groups: [] }
}

// ── Pure transition functions ───────────────────────────────────────────────

/**
 * Replace both profile lists atomically.
 *
 * Authority: ConnectionManagerViewImpl.refresh() calls ProfileClient RPC
 * methods (listProfiles, listGroups).
 *
 * Derived from: ConnectionManagerViewImpl.refresh (connections.ts:49-64)
 */
export function setProfileLists(
  _prev: ProfileLists,
  profiles: readonly SSHProfile[],
  groups: readonly ProfileGroup[],
): ProfileLists {
  return {
    profiles: [...profiles],
    groups: [...groups],
  }
}
