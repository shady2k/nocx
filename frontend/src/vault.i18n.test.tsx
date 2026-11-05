// @vitest-environment jsdom
/**
 * i18n tests for the vault surface (nocx-dej6 stage 3c) — the proof that a
 * call-site reads its strings through the catalogue rather than the literal:
 * the setup dialog's empty state mounted under 'en' shows the English
 * catalogue values, and the same mount under 'ru' shows Russian. The
 * catalogue parity fixture only checks that en and ru know the same keys;
 * these tests check that this surface actually resolves them at render time.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from '@solidjs/testing-library'
import {
  VaultSection,
  SetupDialog,
  UnlockDialog,
  createVaultState,
  vaultErrorMessage,
} from './vault'
import type { VaultClient, VaultStatus } from './vault-client'
import { setActiveLocale } from './i18n/locale-model'
import { RpcError } from './dispatcher'
import { clearStack } from './ui/overlay/stack'

const UNINITIALIZED: VaultStatus = {
  state: 'uninitialized',
  hasPassphrase: false,
  autoSealMinutes: 0,
  defaultProvider: null,
  providers: [],
}

function mountVaultSection() {
  const vaultClient = {
    status: () => Promise.resolve(UNINITIALIZED),
    resetPreview: () =>
      Promise.resolve({
        secretCount: 0,
        profileCount: 0,
        systemKeychainReachable: true,
        residue: [],
      }),
    setAutoSeal: () => Promise.resolve(),
    setDefaultProvider: () => Promise.resolve(),
  } as unknown as VaultClient
  const vaultController = createVaultState(vaultClient)
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <VaultSection vaultClient={vaultClient} vaultController={vaultController} />, {
    container,
  })
  return { container }
}

// The locale signal is module state; every test starts and leaves it English
// so one test's switch cannot decide what the next test reads.
beforeEach(() => {
  setActiveLocale('en')
})

afterEach(() => {
  setActiveLocale('en')
  vi.clearAllMocks()
  document.body.innerHTML = ''
})

describe('vault surface renders through the catalogue', () => {
  it('shows the English catalogue values under en', async () => {
    const { container } = mountVaultSection()
    // The section refreshes its status on mount; once it lands, the state
    // card names the uninitialized vault in English.
    await vi.waitFor(() => {
      expect(container.textContent).toContain('Protection is not set up yet')
    })
    expect(container.textContent).toContain('Set up protection')
  })

  it('shows Russian under ru', async () => {
    setActiveLocale('ru')
    const { container } = mountVaultSection()
    // The Protection section title and the Reset row label are catalogue
    // strings — the card's own title is still English, untranslated here.
    await vi.waitFor(() => {
      expect(container.textContent).toContain('Защита')
    })
    expect(container.textContent).toContain('Сбросить хранилище')

    // The setup sheet's label is aria-only text: the Prompt paints it on the
    // dialog element, not in textContent. Mount the sheet directly — it is
    // the same component the section opens.
    const vaultClient = {
      setup: () => Promise.resolve({ recoveryCode: 'rc' }),
    } as unknown as VaultClient
    const dlg = document.body.appendChild(document.createElement('div'))
    render(() => <SetupDialog open vaultClient={vaultClient} onClose={() => {}} />, {
      container: dlg,
    })
    const prompt = document.querySelector('[aria-modal][aria-label]')
    expect(prompt?.getAttribute('aria-label')).toBe('Настроить хранилище')
  })

  it('renders the state card and unlock titles through the ru catalogue', async () => {
    setActiveLocale('ru')
    const { container } = mountVaultSection()
    // The uninitialized card's title is the same string the Secrets page
    // shows for its unset state; the section's own action names it in Russian.
    await vi.waitFor(() => {
      expect(container.textContent).toContain('Защита ещё не настроена')
    })
    expect(container.textContent).toContain('Настроить защиту')

    // The unlock prompt's bare title — what the card's Unlock button opens —
    // resolves through vault.unlock.title, not the call-site literal.
    const vaultClient = {
      unseal: () => Promise.resolve(),
    } as unknown as VaultClient
    // The overlay stack is module state the afterEach DOM wipe cannot reach:
    // the setup test's Prompt entry survives it, and a new prompt would portal
    // into that dead element instead of its own mount. Clear it first.
    clearStack()
    const dlg = document.body.appendChild(document.createElement('div'))
    render(
      () => <UnlockDialog open vaultClient={vaultClient} vaultStatus={null} onClose={() => {}} />,
      { container: dlg },
    )
    await vi.waitFor(() => {
      const prompt = dlg.querySelector('[aria-modal][aria-label]')
      expect(prompt?.getAttribute('aria-label')).toBe('Разблокировать хранилище')
    })
    expect(dlg.textContent).toContain('Разблокировать хранилище')
  })

  it('speaks vault reason codes in Russian', () => {
    setActiveLocale('ru')
    // A store that did not answer: the reason table resolves the code under
    // the active locale at read time — a module-level table of plain strings
    // would have pinned the import-time (English) wording forever.
    const err = new RpcError('sealed', undefined, { reason: 'locked' })
    expect(vaultErrorMessage(err)).toBe(
      'Связка ключей заблокирована. Разблокируйте её и повторите.',
    )
    setActiveLocale('en')
    expect(vaultErrorMessage(err)).toBe('Your login keychain is locked. Unlock it and try again.')
  })
})
