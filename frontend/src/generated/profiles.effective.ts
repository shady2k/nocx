/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/profiles.effective.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the profiles.effective JSON-RPC method — the batched effective profile resolution with per-field provenance. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it. The fields map is a closed set: propertyNames pins the exact key set ToEffectiveDTO emits, so a field dropped from the wire (or a renderer depending on one the wire does not carry) fails the contract instead of looking like a working field. The fields map carries the desiredMode axis (raw|script|helper); helperConsent is a required per-profile field beside id and fields because it is persisted per destination and never inherited.
 */
export interface ProfilesEffectiveResponse {
  /**
   * One resolved profile per requested id, in request order. Never null: no matches is [].
   */
  profiles: EffectiveProfile[]
  /**
   * Per-profile resolution failures, keyed by the requested id. Absent when every profile resolved. The renderer must not present a half-resolved batch as a whole one: a profile that errored appears here, not in profiles.
   */
  errors?: ProfileError[]
}
export interface EffectiveProfile {
  /**
   * The profile id this resolution belongs to.
   */
  id: string
  /**
   * Every inheritable option, resolved. Omission is indistinguishable from 'unresolved', so every field is always present — including zeros, false and the hardcoded default. The key set is closed: propertyNames pins the names ToEffectiveDTO emits and required demands all of them, so a field dropped from the wire fails the contract.
   */
  fields: {
    [k: string]: EffectiveField
  }
  /**
   * The helper-consent axis (spec §3.5): whether the user has consented to nocx deploying the helper binary on this destination. Persisted per destination, never inherited; script mode never reads it. Always present — unknown is an honest state, never an absent field the renderer could silently read as granted.
   */
  helperConsent: 'unknown' | 'granted' | 'denied'
}
export interface EffectiveField {
  /**
   * The resolved value for this field. Type varies by field: string for host/user/secrets/enums, integer for ports and timeouts, boolean for toggles. tsType is a json-schema-to-typescript hint: the schema is deliberately untyped (the value really is field-dependent), and the generator renders an unrestricted schema as an index signature, which would reject scalar values at the type level.
   */
  value: unknown
  source: FieldSource
}
export interface FieldSource {
  /**
   * Closed enum for the renderer to switch on — never parse id/label to guess the layer.
   */
  kind: 'profile' | 'group' | 'sshConfig' | 'global' | 'default'
  /**
   * Group id when kind is group, empty otherwise.
   */
  id: string
  /**
   * Group name when kind is group and the group is known, empty otherwise.
   */
  label: string
}
export interface ProfileError {
  id: string
  error: string
}
