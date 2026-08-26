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
  control: 'toggle' | 'text' | 'number' | 'select' | 'secret'
  dataClass: 'publicConfig' | 'privateMetadata' | 'privateContent' | 'secretAuthenticator'
  /**
   * The declared default. Absent for secret-class settings and for zero-value defaults (the Go wire omits them via omitempty).
   */
  default?: boolean | string | number | null
  /**
   * The select control's choices; present only for control: select.
   */
  options?: {
    value: string
    label: string
  }[]
  min?: number
  /**
   * The declared ceiling: a number setting's largest allowed value, or — on control "text" — the longest allowed text, counted in characters. Which it is is never ambiguous, because a text control has no numeric value; unit names what the number counts. Text past it is REFUSED, never truncated, and the screen states the bound rather than letting it be discovered by losing text to it.
   */
  max?: number
  unit?: string
  zeroLabel?: string
  /**
   * The text control's paragraph variant; present only for control: text. The screen renders the kit's multiline TextField, where Enter inserts a newline instead of committing. A variant rather than a sixth control kind, matching the kit: TextField takes a multiline prop and there is no second component.
   */
  multiline?: boolean
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
