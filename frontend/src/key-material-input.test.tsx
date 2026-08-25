// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, fireEvent } from '@solidjs/testing-library'
import { KeyMaterialInput, publicKeyMistake, secretOptions } from './key-material-input'
import { toasts, clearToasts } from './ui/toast'
import type { InventoryEntry } from './vault-client'

function entry(overrides: Partial<InventoryEntry> = {}): InventoryEntry {
  return {
    id: 'secrow:aaaa',
    name: 'prod api key',
    kind: 'password',
    provider: 'file',
    ownerId: '',
    usedBy: 0,
    reachable: true,
    ...overrides,
  }
}

// A picker labels a secret with the secret's NAME. The one case where it
// cannot — the record names a row the inventory does not list, because the
// secret was deleted or the vault could not answer — is where the label used
// to be the row handle itself, so a person opening their endpoint read
// `secrow:dd39558499fe31b5ddce0f88a5d31320` where the name of their own key
// belongs (nocx-5ratm).
describe('secretOptions', () => {
  it('names each row the vault listed', () => {
    expect(secretOptions([entry()])).toEqual([{ value: 'secrow:aaaa', label: 'prod api key' }])
  })

  it('keeps a bound row that the vault did not list, and never labels it with its handle', () => {
    const opts = secretOptions([entry()], 'secrow:gone')
    // The binding is not silently dropped: dropping it would read as "None"
    // and a save would then clear a credential nobody meant to clear.
    expect(opts.map((o) => o.value)).toEqual(['secrow:aaaa', 'secrow:gone'])
    const fallback = opts[1]
    expect(fallback.label).not.toContain('secrow:')
    expect(fallback.label).toBe('Unavailable secret')
  })

  it('adds nothing when the bound row is listed', () => {
    expect(secretOptions([entry()], 'secrow:aaaa')).toHaveLength(1)
  })
})

// Uploading `id_ed25519.pub` instead of `id_ed25519` is the mistake this
// catches, and it is the one a user actually made: the backend answered "not a
// valid private key: ssh: no key found", the renderer logged it and showed
// nothing, and Create looked inert.
describe('publicKeyMistake', () => {
  it.each([
    'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA user@host',
    'ssh-rsa AAAAB3NzaC1yc2EAAAA user@host',
    'ecdsa-sha2-nistp256 AAAAE2VjZHNh user@host',
    'sk-ssh-ed25519@openssh.com AAAAG3NrLXNz user@host',
  ])('recognises %s as a public key and says which file is wanted', (line) => {
    const msg = publicKeyMistake(line)
    expect(msg).toBeDefined()
    expect(msg).toContain('.pub')
  })

  it('leaves a private key alone', () => {
    expect(
      publicKeyMistake(
        '-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaA\n-----END OPENSSH PRIVATE KEY-----',
      ),
    ).toBeUndefined()
  })

  // It must not become a second opinion about what a private key is — the
  // backend has the parser. Anything that is not recognisably a public key
  // passes through to it, including nonsense.
  it('does not judge anything else', () => {
    expect(publicKeyMistake('')).toBeUndefined()
    expect(publicKeyMistake('not a key at all')).toBeUndefined()
    expect(
      publicKeyMistake('-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----'),
    ).toBeUndefined()
  })

  it('reads only the first line, so a key with a public half pasted after it still passes', () => {
    expect(
      publicKeyMistake(
        '-----BEGIN OPENSSH PRIVATE KEY-----\nb3Bl\n-----END OPENSSH PRIVATE KEY-----\nssh-ed25519 AAAA',
      ),
    ).toBeUndefined()
  })
})
// One message about the material, not a stack of them. Choosing a .pub used to
// render "That is a public key…" and "not a valid private key: ssh: no key
// found" one above the other, plus a toast — the same news three times, and
// the two the eye lands on first were the least useful.
describe('KeyMaterialInput error reporting', () => {
  beforeEach(() => clearToasts())

  function renderWith(error: string | undefined) {
    return render(() => (
      <KeyMaterialInput
        id="k"
        mode="file"
        onModeChange={() => {}}
        pathValue=""
        onPathChange={() => {}}
        materialValue=""
        onMaterialChange={() => {}}
        error={error}
      />
    ))
  }

  it('shows the parent verdict when nothing local was found', async () => {
    const { container } = renderWith('not a valid private key: ssh: no key found')
    await Promise.resolve()
    const shown = Array.from(container.querySelectorAll('.ui-field-error')).filter((e) =>
      e.textContent?.includes('ssh: no key found'),
    )
    expect(shown.length).toBe(1)
  })

  it('shows exactly one message, never two', async () => {
    const { container } = renderWith('not a valid private key: ssh: no key found')
    const native = container.querySelector('.ui-file-input__native') as HTMLInputElement
    const file = new File(['ssh-ed25519 AAAAC3 user@host'], 'id.pub', { type: 'text/plain' })
    Object.defineProperty(native, 'files', { value: [file], configurable: true })
    fireEvent.change(native)

    await vi.waitFor(() => {
      const shown = Array.from(container.querySelectorAll('.ui-field-error')).filter((e) =>
        e.textContent?.includes('.pub'),
      )
      expect(shown.length).toBe(1)
      // And it is the local one, which names the file the user wants.
      expect(shown[0].textContent).toContain('.pub')
    })
  })

  it('raises a toast, not a field paragraph, when the chosen file cannot be read', async () => {
    const { container } = renderWith(undefined)
    const native = container.querySelector('.ui-file-input__native') as HTMLInputElement
    const unreadable = {
      name: 'broken.key',
      text: () => Promise.reject(new Error('read failure')),
    }
    Object.defineProperty(native, 'files', { value: [unreadable], configurable: true })
    fireEvent.change(native)

    await vi.waitFor(() => {
      expect(
        toasts().some((t) => t.level === 'danger' && t.message.includes('Could not read')),
      ).toBe(true)
    })
    // A read failure is the outcome of the read, not a verdict on the field.
    expect(
      Array.from(container.querySelectorAll('.ui-field-error')).some((e) =>
        e.textContent?.includes('Could not read'),
      ),
    ).toBe(false)
  })
})

// The Secret segment is the ADR-0017 way to bind an existing vault row: the
// picker lists the vault's private-key rows, the bound one is the current
// value, and choosing a row reports it upward.
describe('KeyMaterialInput secret mode', () => {
  const KEY_ROWS: InventoryEntry[] = [
    {
      id: 'secrow:key-1',
      name: 'Key for prod-web',
      kind: 'private-key',
      provider: 'test',
      ownerId: '',
      usedBy: 0,
      reachable: true,
    },
    {
      id: 'secrow:key-2',
      name: 'Key for prod-db',
      kind: 'private-key',
      provider: 'test',
      ownerId: '',
      usedBy: 0,
      reachable: true,
    },
  ]

  function renderSecret(
    extra?: Partial<{
      secretValue?: string
      onSecretChange: (value: string | undefined) => void
    }>,
  ) {
    return render(() => (
      <KeyMaterialInput
        id="k"
        mode="secret"
        onModeChange={() => {}}
        pathValue=""
        onPathChange={() => {}}
        materialValue=""
        onMaterialChange={() => {}}
        secrets={KEY_ROWS}
        secretValue={extra?.secretValue}
        onSecretChange={extra?.onSecretChange ?? (() => {})}
      />
    ))
  }

  function picker(container: HTMLElement): HTMLSelectElement {
    const field = container.querySelector('label[for="k-secret"]')?.closest('.ui-field')
    expect(field, 'secret picker field not found').toBeTruthy()
    return field!.querySelector('.ui-select') as HTMLSelectElement
  }

  it('selecting a secret reports the row handle', () => {
    const onSecretChange = vi.fn()
    const { container } = renderSecret({ onSecretChange })
    fireEvent.change(picker(container), { target: { value: 'secrow:key-2' } })
    expect(onSecretChange).toHaveBeenCalledWith('secrow:key-2')
  })

  it('shows the bound row as the current value', () => {
    const { container } = renderSecret({ secretValue: 'secrow:key-1' })
    expect(picker(container).value).toBe('secrow:key-1')
  })

  it('clearing the picker reports undefined', () => {
    const onSecretChange = vi.fn()
    const { container } = renderSecret({ onSecretChange })
    fireEvent.change(picker(container), { target: { value: '' } })
    expect(onSecretChange).toHaveBeenCalledWith(undefined)
  })
})
