/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/skills.setPin.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface SkillsSetPin {
  name: string
  pins: {
    keepEnabled?: boolean
    keepUnchanged?: boolean
  }
}
