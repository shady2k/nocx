/**
 * settings-domain — framework‑neutral settings domain transitions.
 *
 * Three pieces of domain logic extracted from settings.ts:
 *   1. Draft preservation on rejected save.
 *   2. Provenance‑based reset.
 *   3. Snapshot revision policy (AD-7 model: authority encoded in the type).
 *
 * No DOM dependency, no Solid import. Pure functions and branded types only.
 *
 * AD-7 model: AcceptedSnapshot can ONLY be produced by
 * AcceptedSnapshot.accept(), which performs the revision check.  No code path
 * can create an "accepted" snapshot without going through the gate — the
 * private constructor enforces it at compile time.
 */

// ── Types ──────────────────────────────────────────────────────────────────

import type { Declaration } from './generated/settings.describe'

/**
 * The settings wire vocabulary, generated from contracts/settings.describe.schema.json
 * (npm run contracts) — never hand-edited. The domain re-exports it so
 * callers keep importing from here; a hand-written type can want a field the
 * wire does not carry, a generated one cannot (contracts/README.md).
 */
export type { Declaration, SettingsGroup } from './generated/settings.describe'

/**
 * The permanent caption under a number field, read from the declaration —
 * never a literal the screen invents.
 *
 * It says what the CURRENT value means before it says what values are
 * allowed: at a sentinel the range is the less useful of the two, and a
 * field reading "0" above "0 – 3650 days" tells a user nothing about what
 * zero does. Away from the sentinel the range comes back.
 */
export function numberRangeCaption(decl: Declaration, value?: number): string | undefined {
  if (value === 0 && decl.zeroLabel !== undefined) return decl.zeroLabel
  const suffix = decl.unit !== undefined ? ' ' + decl.unit : ''
  if (decl.min !== undefined && decl.max !== undefined) {
    return `${decl.min} – ${decl.max}${suffix}`
  }
  if (decl.min !== undefined) return `≥ ${decl.min}${suffix}`
  if (decl.max !== undefined) return `≤ ${decl.max}${suffix}`
  return undefined
}

/** The out-of-range error for a numeric value, derived from the declaration's
 *  Min/Max. Returns undefined when the value is inside the bounds. */
export function numberRangeError(decl: Declaration, value: number): string | undefined {
  const suffix = decl.unit !== undefined ? ' ' + decl.unit : ''
  if (decl.min !== undefined && value < decl.min) {
    return `Must be at least ${decl.min}${suffix}`
  }
  if (decl.max !== undefined && value > decl.max) {
    return `Must be at most ${decl.max}${suffix}`
  }
  return undefined
}

/**
 * The save error a field should show BENEATH itself — undefined when the
 * field's own caption slot already carries the same fact.
 *
 * Both layers legitimately reject an out-of-range number: the screen from
 * the declaration's Min/Max, and the backend because it is the authority and
 * must never take the value on the screen's word. Rendering both puts the
 * fact on screen twice, the second time in the backend's language — measured
 * in a real browser on 2026-08-01: a caption reading "Must be at least 128
 * MiB" with `settings: "history.diskCeilingMiB" validation failed: value 1
 * below minimum 128` directly under it, wider than the field's column.
 *
 * Suppression is narrow on purpose. It applies only when the caption slot is
 * ALREADY showing the range error for this very value; a rejection the
 * screen did not predict (an unknown key, a transport failure, a rule the
 * declaration does not express) still reaches the user verbatim, because a
 * silent save failure is the worse defect.
 */
export function fieldSaveError(
  decl: Declaration,
  value: number,
  saveError: string | undefined,
): string | undefined {
  if (saveError === undefined) return undefined
  if (decl.control === 'number' && numberRangeError(decl, value) !== undefined) return undefined
  return saveError
}

/** A raw settings snapshot from the backend, *before* revision checking. */
export interface SettingsSnapshot {
  values: Record<string, unknown>
  overridden: string[]
  revision: number
}

/**
 * An accepted settings snapshot.
 *
 * Constructible ONLY through `AcceptedSnapshot.accept()`, which enforces the
 * monotonic revision policy (incoming revision >= current).  A private
 * constructor prevents any other construction path — the brand is a compile
 * time guard that makes the authority check unavoidable.
 */
export class AcceptedSnapshot {
  /** Nominal brand — prevents construction outside this module (AD-7 pattern). */
  private readonly __brand!: 'AcceptedSnapshot'

  private constructor(
    readonly values: Record<string, unknown>,
    readonly overridden: ReadonlySet<string>,
    readonly revision: number,
  ) {}

  /**
   * Accept a snapshot if its revision is not older than the current one.
   * Returns `null` when the snapshot is stale — the caller must not overwrite
   * the local mirror with stale data.
   *
   * Policy: monotonic — `incoming.revision >= currentRevision`.
   */
  static accept(currentRevision: number, snapshot: SettingsSnapshot): AcceptedSnapshot | null {
    if (snapshot.revision < currentRevision) return null
    return new AcceptedSnapshot(
      { ...snapshot.values },
      new Set(snapshot.overridden),
      snapshot.revision,
    )
  }

  /**
   * Accept a snapshot unconditionally, ignoring the current revision.
   *
   * Used only on the reconnect path: when the backend restarts the revision
   * counter resets (it is in-memory, ADR-0011 §A.1), and a monotonic check
   * would silently drop the incoming snapshot, leaving the UI showing stale
   * data.
   *
   * This is still an authority-gated entry point — the brand ensures no
   * code can produce an AcceptedSnapshot except through `accept()` or
   * `reset()`.
   */
  static reset(snapshot: SettingsSnapshot): AcceptedSnapshot {
    return new AcceptedSnapshot(
      { ...snapshot.values },
      new Set(snapshot.overridden),
      snapshot.revision,
    )
  }
}

/** Local settings mirror — the writable frontend state. */
export interface SettingsMirror {
  values: Record<string, unknown>
  draftValues: Record<string, unknown>
  overridden: Set<string>
  errors: Record<string, string>
  revision: number
}

/** Create an empty (uninitialised) settings mirror. */
export function createMirror(): SettingsMirror {
  return {
    values: {},
    draftValues: {},
    overridden: new Set(),
    errors: {},
    revision: 0,
  }
}

// ── Save outcome ───────────────────────────────────────────────────────────

/** The result of attempting to save a single setting. */
export type SaveOutcome =
  | { kind: 'accepted'; value: unknown }
  | { kind: 'rejected'; error: string; attemptedValue: unknown }

// ── Revision policy ────────────────────────────────────────────────────────

/**
 * How an incoming snapshot is admitted. The two implementations are
 * `AcceptedSnapshot.accept` (monotonic — the default everywhere) and
 * `AcceptedSnapshot.reset` (reconnect, where the backend's in-memory revision
 * counter may have restarted, ADR-0011 §A.1).
 *
 * The caller supplies the policy; no consumer branches on which one is in
 * force (AD-8: variation lives in the interface, not in a fork).
 */
export type RevisionPolicy = (
  currentRevision: number,
  snapshot: SettingsSnapshot,
) => AcceptedSnapshot | null

/** The normal policy: a snapshot older than the local mirror is refused. */
export const monotonicRevisionPolicy: RevisionPolicy = (currentRevision, snapshot) =>
  AcceptedSnapshot.accept(currentRevision, snapshot)

/**
 * The reconnect policy: the current revision is ignored on purpose, because
 * the backend's counter is in-memory and may have restarted (ADR-0011 §A.1);
 * a monotonic check would silently drop the snapshot and leave the UI stale.
 */
export const reconnectRevisionPolicy: RevisionPolicy = (_currentRevision, snapshot) =>
  AcceptedSnapshot.reset(snapshot)

// ── Reset decision ─────────────────────────────────────────────────────────

export type ResetReason = 'notOverridden'

export interface ResetAllowed {
  canReset: true
}

export interface ResetDenied {
  canReset: false
  reason: ResetReason
}

export type ResetDecision = ResetAllowed | ResetDenied

// ── Pure transition functions ──────────────────────────────────────────────

/**
 * Record the outcome of a save attempt against a settings mirror.
 *
 * On **accepted**: the value is written into `values`, the key is added to
 * `overridden`, and any previous draft/error for this key is cleared.
 *
 * On **rejected**: the attempted value is preserved in `draftValues` (so the
 * user can edit rather than retype), and the error is stored in `errors`.
 *
 * Returns a **new** mirror — the argument is not mutated.
 */
export function recordSaveOutcome(
  mirror: SettingsMirror,
  key: string,
  outcome: SaveOutcome,
): SettingsMirror {
  // Clone all mutable state.
  const nextValues = { ...mirror.values }
  const nextDrafts = { ...mirror.draftValues }
  const nextErrors = { ...mirror.errors }
  const nextOverridden = new Set(mirror.overridden)

  // Always clear stale per-key state before recording the outcome.
  delete nextDrafts[key]
  delete nextErrors[key]

  if (outcome.kind === 'accepted') {
    nextValues[key] = outcome.value
    nextOverridden.add(key)
  } else {
    nextErrors[key] = outcome.error
    nextDrafts[key] = outcome.attemptedValue
  }

  return {
    values: nextValues,
    draftValues: nextDrafts,
    overridden: nextOverridden,
    errors: nextErrors,
    revision: mirror.revision,
  }
}

/**
 * Determine whether a setting can be reset, based on its provenance.
 *
 * A setting is eligible for reset when its key is in the `overridden` set
 * (provenance = customized).  The calling code also guards the UI by control
 * type (secrets never render a provenance badge), but the function itself
 * decides purely on provenance data.
 *
 * Public utility: extracting this means the provenance logic is a single
 * pure function rather than embedded in DOM rendering code.  Tests prove the
 * decision is correct without a DOM.
 */
export function canResetSetting(overridden: ReadonlySet<string>, key: string): ResetDecision {
  if (!overridden.has(key)) return { canReset: false, reason: 'notOverridden' }
  return { canReset: true }
}

/**
 * Whether a setting differs from its default, for the "modified" marker, the
 * per-section counts and the Modified-only filter.
 *
 * Deliberately not the same question as `canResetSetting`. The backend records
 * an override the moment a key is written, so setting a value, changing your
 * mind, and setting it back leaves an override whose value equals the default —
 * and the UI then claimed the setting was modified when nothing about it was.
 * That is what the user sees, and value equality is what they mean.
 *
 * Compared with JSON rather than `===` because a default can be an array or an
 * object; `===` would call every one of those modified forever. Key order
 * within an object is the one thing this does not see through, and settings
 * defaults are declared literals, so their order is stable.
 */
export function isSettingModified(
  overridden: ReadonlySet<string>,
  key: string,
  effective: unknown,
  defaultValue: unknown,
): boolean {
  if (!overridden.has(key)) return false
  if (defaultValue === undefined) return true
  if (effective === defaultValue) return false
  try {
    return JSON.stringify(effective) !== JSON.stringify(defaultValue)
  } catch {
    // Circular or otherwise unserialisable: fall back to the override record,
    // which is the answer that at least never hides a real change.
    return true
  }
}

/**
 * Apply an accepted snapshot to produce a new settings mirror.
 *
 * Clears drafts and errors because a fresh snapshot represents the
 * authoritative server-side state — any previous in-flight edits are no
 * longer relevant.
 */
export function applyAcceptedSnapshot(snapshot: AcceptedSnapshot): SettingsMirror {
  return {
    values: { ...snapshot.values },
    draftValues: {},
    overridden: new Set(snapshot.overridden),
    errors: {},
    revision: snapshot.revision,
  }
}
