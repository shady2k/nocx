// @vitest-environment jsdom
import { render, cleanup, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AuthenticationEditor } from './authentication-editor'

// THE PASSWORD CONTROL IS ONE CONTROL, and this editor has drawn it twice in
// two different ways.
//
// First (nocx-azxe.6) AuthMethodEditor showed `passwordAction` whenever the
// method was Password and the editor showed the SAME element again under its
// own "Type a new one" / "Use existing secret" choice — two identical "Set
// Password" buttons under two "Password" labels, and two elements for one
// accessible name. Then the choice itself was the duplication: SecretSource
// drew a Field labelled "Password" around a segmented control and a TextField
// labelled "Password" inside it.
//
// Both are gone (nocx-3o0ed.4). Under the Password method there is exactly one
// field, it is labelled once, and what it HOLDS says where the password comes
// from — a `{{secret:…}}` reference is the vault's row, anything else is a
// literal nobody has stored yet. These tests hold that count at one.

afterEach(cleanup)

const noop = () => {}

const BOUND = [{ id: 'secrow:aaaa', name: 'Password for prod-web' }]

function renderPasswordEditor(over: { passwordSecret?: string } = {}) {
  render(() => (
    <AuthenticationEditor
      id="test-auth"
      username="someone"
      onUsernameChange={noop}
      auth="password"
      onAuthChange={noop}
      passwordEntries={BOUND}
      vaultState="unsealed"
      passwordSecret={over.passwordSecret}
      onPasswordSecretChange={noop}
      publicKeyAction={<button type="button">Choose Key</button>}
    />
  ))
}

describe('AuthenticationEditor', () => {
  it('labels the password field exactly once', () => {
    renderPasswordEditor()
    // One control, one label. Two labelled "Password" is what the segmented
    // control and its inner input were.
    expect(screen.getAllByLabelText('Password')).toHaveLength(1)
    expect(
      Array.from(document.querySelectorAll('label')).filter(
        (l) => l.textContent?.trim() === 'Password',
      ),
    ).toHaveLength(1)
  })

  it('offers no second control beside the field', () => {
    renderPasswordEditor()
    // The action button and the source segments are both gone: the field's
    // own lock is the door to the vault, and a control offering the same act
    // beside it is the third vocabulary this editor no longer has.
    expect(screen.queryByRole('button', { name: 'Set Password' })).toBeNull()
    expect(screen.queryByRole('radio', { name: 'Use existing secret' })).toBeNull()
    expect(screen.queryByRole('radio', { name: 'Type a new one' })).toBeNull()
  })

  it('names the bound secret in the field, never its handle', () => {
    renderPasswordEditor({ passwordSecret: 'secrow:aaaa' })
    const field = screen.getByLabelText('Password')
    // The VALUE is the opaque reference — that is what the profile binds —
    // while what a person reads is the secret's name.
    expect((field as HTMLInputElement).value).toBe('{{secret:secrow:aaaa}}')
    expect(document.body.textContent).toContain('Password for prod-web')
    expect(document.body.textContent).not.toContain('secrow:aaaa')
  })

  it('draws the public key action exactly once', () => {
    render(() => (
      <AuthenticationEditor
        id="test-auth"
        username="someone"
        onUsernameChange={noop}
        auth="publicKey"
        onAuthChange={noop}
        passwordEntries={[]}
        passwordSecret={undefined}
        onPasswordSecretChange={vi.fn()}
        publicKeyAction={<button type="button">Choose Key</button>}
      />
    ))
    expect(screen.getAllByRole('button', { name: 'Choose Key' })).toHaveLength(1)
  })
})
