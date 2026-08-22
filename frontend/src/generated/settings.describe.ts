/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/settings.describe.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the settings.describe JSON-RPC method. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it.
 */
export interface SettingsDescribe {
  /**
   * Every declared setting, in declaration order.
   */
  declarations: Declaration[]
  /**
   * The settings rail's group catalogue — id, title, order — in rail order. Component pages name these ids in their registry entries in settings.tsx; a page whose group id is absent here is a defect, never a silent top-level page.
   */
  groups: SettingsGroup[]
  /**
   * Which group each generated section belongs to. A section absent from this map is ungrouped and renders at top level beside the groups. Keyed by section, never by declaration: two declarations in one section cannot disagree about their group.
   */
  sectionGroups: {
    [k: string]: string
  }
}
export interface Declaration {
  /**
   * Stable dotted id, e.g. "terminal.fontSize".
   */
  key: string
  /**
   * Group heading in the UI. The section is what a rail group maps, never the individual declaration.
   */
  section: string
  label: string
  description: string
  control: 'toggle' | 'text' | 'number' | 'select' | 'secret' | 'paths'
  dataClass: 'publicConfig' | 'privateMetadata' | 'privateContent' | 'secretAuthenticator'
  /**
   * The declared default. Path-list settings use an array of strings; scalar settings use their scalar value. Absent for secret-class settings and for zero-value scalar defaults (the Go wire omits them via omitempty).
   */
  default?: (boolean | string | number | null) | string[]
  /**
   * The select control's choices; present only for control: select.
   */
  options?: {
    value: string
    label: string
  }[]
  min?: number
  max?: number
  unit?: string
  zeroLabel?: string
}
export interface SettingsGroup {
  /**
   * Stable id a component page names in its registry entry.
   */
  id: string
  title: string
  /**
   * Rail position; the rail sorts groups by it. Distinct per group.
   */
  order: number
}
