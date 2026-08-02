/**
 * AuthenticationEditor — the SSH authentication source editor shared by
 * connections and groups (ADR-0017 §1).
 *
 * The editor owns the domain rule that a connection authenticates with the
 * secrets it names: the vault rows bound to the profile's options, or the
 * inline user/method the user types here. There is no Credential control —
 * the credential aggregate is gone, and the word never appears (ADR-0017 §4).
 *
 * The parent owns the minting flows (password dialog, key input with its
 * file picker and passphrase prompt) and passes their JSX into the method
 * slots, exactly as the editor's previous incarnation did. What the editor
 * adds is the secret picker: under Password, the vault's password rows,
 * with the bound one shown as the current value.
 */
import { Show, createMemo, createSignal, untrack, type Component, type JSX } from 'solid-js'
import { AUTH_SEGMENTS } from './auth-methods'
import type { InventoryEntry } from './vault-client'
import type { AuthMode } from './profiles'
import { secretOptions } from './key-material-input'
import { Field } from './ui/field'
import { SegmentedControl } from './ui/segmented-control'
import { Select, type SelectOption } from './ui/select'
import { Stack } from './ui/stack'
import { TextField } from './ui/text-field'

const INHERIT_AUTH = '__inherit__'

export interface AuthenticationEditorProps {
  id: string
  username?: string
  onUsernameChange: (value: string | undefined) => void
  auth?: AuthMode
  onAuthChange: (value: AuthMode | undefined) => void
  inherit?: boolean
  passwordAction?: JSX.Element
  publicKeyAction?: JSX.Element
  authSuffix?: JSX.Element
  /** The vault's password rows, for the picker under the Password method.
   *  Empty when the vault is locked — the picker then offers nothing. */
  passwordSecrets: InventoryEntry[]
  /** The bound password secret's row handle (ADR-0017 §1). */
  passwordSecret?: string
  onPasswordSecretChange: (value: string | undefined) => void
}

export interface AuthMethodEditorProps {
  id: string
  auth?: AuthMode
  onAuthChange: (value: AuthMode | undefined) => void
  inherit?: boolean
  passwordAction?: JSX.Element
  publicKeyAction?: JSX.Element
  suffix?: JSX.Element
}

/** Method selection and method-specific controls shared by every auth form. */
export const AuthMethodEditor: Component<AuthMethodEditorProps> = (props) => {
  const options = createMemo(() =>
    props.inherit
      ? [
          { value: INHERIT_AUTH, label: 'Inherit', title: 'Not set — inherit from parent' },
          ...AUTH_SEGMENTS,
        ]
      : AUTH_SEGMENTS,
  )

  return (
    <>
      <Field for={`${props.id}-method`} label="Method">
        <SegmentedControl
          options={options()}
          value={props.inherit && props.auth === undefined ? INHERIT_AUTH : (props.auth ?? '')}
          onChange={(value) =>
            props.onAuthChange(value === INHERIT_AUTH ? undefined : (value as AuthMode))
          }
          ariaLabel="Authentication method"
        />
      </Field>
      {props.suffix}
      <Show when={props.auth === 'password'}>{props.passwordAction}</Show>
      <Show when={props.auth === 'publicKey'}>{props.publicKeyAction}</Show>
    </>
  )
}

/** SecretPicker — one vault row kind as a Select. The bound row, when it is
 *  in the inventory, is the current value: an empty credential is visible
 *  before Connect is pressed (b5bu). */
export const SecretPicker: Component<{
  id: string
  label: string
  secrets: InventoryEntry[]
  value?: string
  onChange: (value: string | undefined) => void
  placeholder?: string
}> = (props) => {
  // The bound row, when it is in the inventory, is the current value: an
  // empty credential is visible before Connect is pressed (b5bu). When the
  // row is missing from the inventory (vault locked), a fallback option
  // carries the opaque handle so the bound secret is never shown as "None".
  const options = createMemo((): SelectOption[] => secretOptions(props.secrets, props.value))
  return (
    <Field for={`${props.id}-secret`} label={props.label}>
      <Select
        value={props.value ?? ''}
        onChange={(value) => props.onChange(value || undefined)}
        options={options()}
        placeholder={props.placeholder ?? '\u2014 None \u2014'}
      />
    </Field>
  )
}

/**
 * The SSH authentication source editor shared by connections and groups.
 *
 * UI primitives remain in ui/. This component owns the domain rule that a
 * connection authenticates with a bound secret or an inline user+method —
 * never with an invisible object (ADR-0017).
 */
export const AuthenticationEditor: Component<AuthenticationEditorProps> = (props) => {
  // The password method offers the same two-way choice the key method's
  // four segments do: type a new one, or use a secret the vault already
  const [passwordMode, setPasswordMode] = createSignal<'new' | 'secret'>(
    untrack(() => (props.passwordSecret ? 'secret' : 'new')),
  )

  return (
    <Stack>
      <TextField
        id={`${props.id}-user`}
        label="User"
        value={props.username ?? ''}
        placeholder={
          props.inherit ? '\u2014 Not set (inherit) \u2014' : '\u2014 Your local username \u2014'
        }
        onInput={(value) => props.onUsernameChange(value || undefined)}
      />
      <AuthMethodEditor
        id={props.id}
        auth={props.auth}
        onAuthChange={props.onAuthChange}
        inherit={props.inherit}
        passwordAction={props.passwordAction}
        publicKeyAction={props.publicKeyAction}
        suffix={props.authSuffix}
      />
      <Show when={props.auth === 'password'}>
        <Field for={`${props.id}-password-source`} label="Password">
          <SegmentedControl
            options={[
              { value: 'new', label: 'Type a new one' },
              { value: 'secret', label: 'Use existing secret' },
            ]}
            value={passwordMode()}
            onChange={(value) => setPasswordMode(value as 'new' | 'secret')}
            ariaLabel="Password source"
          />
        </Field>
        <Show when={passwordMode() === 'new'}>{props.passwordAction}</Show>
        <Show when={passwordMode() === 'secret'}>
          <SecretPicker
            id={props.id}
            label="Existing secret"
            secrets={props.passwordSecrets}
            value={props.passwordSecret}
            onChange={props.onPasswordSecretChange}
            placeholder={props.inherit ? '\u2014 Not set (inherit) \u2014' : '\u2014 None \u2014'}
          />
        </Show>
      </Show>
    </Stack>
  )
}
