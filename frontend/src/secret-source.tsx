/**
 * SecretSource — "where does this value come from", answered with one
 * control: a literal typed fresh, or a reference to a secret the vault
 * already holds (ADR-0017 §1, bead nocx-rzjw).
 *
 * One owner of the two-way choice. The connections editor's password field,
 * the endpoint's key field and every custom-header value row all ask the
 * same question, and a second vocabulary for it is the defect the endpoints
 * bead exists to fix — so they share this component, and a new surface that
 * needs the choice imports it rather than building a third one.
 *
 * The component owns the segmented control and the picker. The caller owns
 * the data (mode, value, secrets) and the 'new' mode's input, which is
 * passed in as JSX exactly as the connections editor passes its password
 * action into the auth editor.
 */
import { Show, type Component, type JSX } from 'solid-js'
import { Field } from './ui/field'
import { SegmentedControl } from './ui/segmented-control'
import { Select } from './ui/select'
import { secretOptions } from './key-material-input'
import type { InventoryEntry } from './vault-client'

export type SecretSourceMode = 'new' | 'secret'

export interface SecretSourceProps {
  /** Element-id prefix for the segmented control and picker. */
  id: string
  /** The field label above the choice: "Password" in the connections
   *  editor, "API key" for the endpoint's own key, "Value" on a header
   *  row. Each surface names its noun. */
  label: string
  mode: SecretSourceMode
  onModeChange: (mode: SecretSourceMode) => void
  /** The segment labels: "Type a new one" / "Use existing secret" for the
   *  endpoint key, "Type a value" / "Use existing secret" for a header
   *  value. */
  newLabel: string
  secretLabel: string
  ariaLabel: string
  /** The 'new' mode's input (a TextField or the connections editor's
   *  password action). Rendered under the segment while mode is 'new'. */
  newControl?: JSX.Element
  /** The vault's rows of the kind this source accepts. Empty when the vault
   *  is locked — the picker then offers nothing. */
  secrets: InventoryEntry[]
  /** The bound row handle, when the value currently comes from the vault. */
  value?: string
  onValueChange: (value: string | undefined) => void
  placeholder?: string
}

export const SecretSource: Component<SecretSourceProps> = (props) => {
  return (
    <>
      <Field for={`${props.id}-source`} label={props.label}>
        <SegmentedControl
          options={[
            { value: 'new', label: props.newLabel },
            { value: 'secret', label: props.secretLabel },
          ]}
          value={props.mode}
          onChange={(value) => props.onModeChange(value as SecretSourceMode)}
          ariaLabel={props.ariaLabel}
        />
      </Field>
      <Show when={props.mode === 'new'}>{props.newControl}</Show>
      <Show when={props.mode === 'secret'}>
        <Field for={`${props.id}-secret`} label="Existing secret">
          <Select
            value={props.value ?? ''}
            onChange={(value) => props.onValueChange(value || undefined)}
            options={secretOptions(props.secrets, props.value)}
            placeholder={props.placeholder ?? '\u2014 None \u2014'}
          />
        </Field>
      </Show>
    </>
  )
}
