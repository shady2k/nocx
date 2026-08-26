/**
 * AuthenticationEditor — the SSH authentication source editor shared by
 * connections and groups (ADR-0017 §1).
 *
 * The editor owns the domain rule that a connection authenticates with the
 * secrets it names: the vault rows bound to the profile's options, or the
 * inline user/method the user types here. There is no Credential control —
 * the credential aggregate is gone, and the word never appears (ADR-0017 §4).
 *
 * The parent owns the minting flows (key input with its file picker and
 * passphrase prompt) and passes their JSX into the method slots, exactly as
 * the editor's previous incarnation did.
 *
 * ONE CONTROL FOR THE PASSWORD (nocx-3o0ed.4). ADR-0017 §1 gave "where does
 * this secret come from" to a segmented control, SecretSource, which asked a
 * person to declare a mode before they had done anything and then drew the
 * word "Password" twice — once on its own Field and once on the input inside
 * it. That control is gone. Under Password there is one SecretTextField: type
 * a password and press its lock to store it, or press the lock and take one
 * the vault already holds. What the field HOLDS is the answer — a whole
 * `{{secret:secrow:…}}` reference means the connection authenticates with
 * that row — so nothing has to be declared in advance, and there is no second
 * vocabulary beside the one every other value field in the product uses.
 */
import { Show, createMemo, type Component, type JSX } from 'solid-js'
import { AUTH_SEGMENTS } from './auth-methods'
import type { AuthMode } from './profiles'
import { boundSecret, secretReference } from './secret-reference'
import { SecretTextField, secretMarks, type VaultState } from './api/secret-text-field'
import type { SecretEntry, SecretPickerSource } from './ui/secret-picker'
import { Field } from './ui/field'
import { SegmentedControl } from './ui/segmented-control'
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
  publicKeyAction?: JSX.Element
  authSuffix?: JSX.Element
  /** Every password secret this editor can NAME — the vault's rows, plus any
   *  minted in this session that the inventory has not caught up with. Used
   *  for the chip over a bound value: a field showing `secrow:…` where a name
   *  belongs is the regression this control exists to prevent. */
  passwordEntries: SecretEntry[]
  /** The vault's lifecycle state, so a locked vault says so on the chip
   *  rather than claiming the secret is missing. */
  vaultState?: VaultState
  /** The picker behind the password field's lock. Absent in the dev-web
   *  harness and bare embeds; the field then has no lock, and a bound
   *  password still names its secret. */
  passwordSource?: SecretPickerSource
  /** The bound password secret's row handle (ADR-0017 §1). */
  passwordSecret?: string
  onPasswordSecretChange: (value: string | undefined) => void
  /** A sentence under the password field, for a surface whose store writes
   *  the binding at once. The connections editor's does — the mint patches
   *  the profile the moment it lands, so cancelling the editor afterwards
   *  will not undo it, and a person is owed that before they press the lock
   *  rather than after. A group default has no such write and passes none. */
  passwordDescription?: string
  /** The literal a person is typing before they store it. Held by the parent
   *  because the parent is what mints it — the lock's store row hands the
   *  text back to the connections editor, which calls `savePassword` and
   *  binds the row it gets (nocx-3o0ed.4). */
  passwordDraft?: string
  onPasswordDraftChange?: (value: string) => void
}

export interface AuthMethodEditorProps {
  id: string
  auth?: AuthMode
  onAuthChange: (value: AuthMode | undefined) => void
  inherit?: boolean
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
      {/* No password control here. AuthenticationEditor owns it — one field,
          drawn once, under the Password method. Drawing it here as well put
          two identical "Set Password" buttons under two "Password" labels in
          front of the user (nocx-azxe.6). */}
      <Show when={props.auth === 'publicKey'}>{props.publicKeyAction}</Show>
    </>
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
  /** What the password field holds. A bound profile holds its reference; an
   *  unbound one holds whatever the person has typed so far. The binding is
   *  the parent's state and the literal is the parent's draft, so there is no
   *  third copy here to fall out of step with either. */
  const passwordText = (): string =>
    props.passwordSecret ? secretReference(props.passwordSecret) : (props.passwordDraft ?? '')

  /** THE WRITE SEAM, and the whole of what the segmented control used to
   *  decide. A value that is exactly one reference binds the profile to that
   *  row; anything else is a literal the person has not stored yet, and
   *  typing over a bound value unbinds it — which is what editing a
   *  credential looks like when nobody has to declare a mode first. */
  const onPasswordInput = (value: string): void => {
    const handle = boundSecret(value)
    if (handle !== undefined) {
      props.onPasswordDraftChange?.('')
      props.onPasswordSecretChange(handle)
      return
    }
    if (props.passwordSecret !== undefined) props.onPasswordSecretChange(undefined)
    props.onPasswordDraftChange?.(value)
  }

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
        publicKeyAction={props.publicKeyAction}
        suffix={props.authSuffix}
      />
      <Show when={props.auth === 'password'}>
        <SecretTextField
          id={`${props.id}-password`}
          label="Password"
          // Masked while it holds a password, plain once it holds a
          // reference. A typed password is material and belongs behind dots
          // — the dialog this replaced masked it too — while a reference is
          // not material, and masking it would hide the chip that names the
          // secret (endpoints-section.tsx says the same thing at its key).
          type={props.passwordSecret === undefined ? 'password' : 'text'}
          value={passwordText()}
          onInput={onPasswordInput}
          description={props.passwordDescription}
          source={props.passwordSource}
          marks={secretMarks(passwordText(), props.passwordEntries, props.vaultState ?? 'unknown')}
          placeholder={props.inherit ? '\u2014 Not set (inherit) \u2014' : '\u2014 None \u2014'}
        />
      </Show>
    </Stack>
  )
}
