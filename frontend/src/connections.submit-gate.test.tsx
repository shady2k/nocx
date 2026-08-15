// @vitest-environment jsdom
/**
 * Component-level proof of the profile editor's submit gate (nocx-74cn).
 *
 * The kit tests in ui/submit-gate.test.tsx drive a hand-built fixture with a
 * `[hidden]` container standing in for a closed panel. What they do not prove
 * is the thing the feature exists for: that pressing Save on the REAL profile
 * editor, with the invalid field on a section the user is not looking at,
 * opens that section and lands the caret in the field. These tests mount the
 * real ConnectionsView and press the real Save button.
 *
 * The invalid Advanced value is typed as `-5` rather than a non-numeric
 * string: a number input sanitizes non-numeric input to `''` — jsdom
 * implements the same value-sanitization algorithm the browsers do, so a user
 * cannot enter one. `-5` is a genuine keystroke that survives the input,
 * fails the same `nonNegativeInteger` rule, and exercises exactly the path
 * the gap describes.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { ConnectionsView } from './connections'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import { clearToasts, toasts } from './ui'
import type { SSHProfile } from './profiles'

const PROFILE: SSHProfile = {
  id: 'ssh:p1',
  type: 'ssh',
  name: 'prod-web',
  options: {
    host: 'web.example.com',
    port: 22,
    user: 'deploy',
    keepaliveInterval: 0,
    keepaliveCountMax: 0,
    readyTimeout: 0,
    agentForward: false,
    canBeJumpServer: false,
  },
}

// Every method the editor's save path could reach is spied so the tests can
// assert the refusal kept the save off the wire. Mocked with success values so
// a wrongly-passing gate fails on `toHaveBeenCalled`, not on a dispatcher
// error from the un-wired real implementation.
function createMockClient() {
  const pc = new ProfileClient(new Dispatcher())
  vi.spyOn(pc, 'listProfiles').mockResolvedValue([PROFILE])
  vi.spyOn(pc, 'listGroups').mockResolvedValue([])
  vi.spyOn(pc, 'sessionStatus').mockResolvedValue({ statuses: {} })
  vi.spyOn(pc, 'loadEffective').mockResolvedValue({ profiles: [] })
  const patchProfile = vi.spyOn(pc, 'patchProfile').mockResolvedValue({ id: 'ssh:p1', fields: {} })
  const updateProfile = vi.spyOn(pc, 'updateProfile').mockResolvedValue(PROFILE)
  return { client: pc, patchProfile, updateProfile }
}

function mount() {
  const { client, patchProfile, updateProfile } = createMockClient()
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <ConnectionsView client={client} />, { container })
  return { container, client, patchProfile, updateProfile }
}

afterEach(() => {
  clearToasts()
  vi.clearAllMocks()
  cleanup()
})

async function waitForProfiles(container: HTMLElement) {
  await vi.waitFor(() => {
    expect(container.querySelectorAll('.ui-record-row__title').length).toBe(1)
  })
}

function findDialogByTitleContaining(container: HTMLElement, partial: string): HTMLElement | null {
  const titles = container.querySelectorAll('.nocx-dialog__title')
  for (const t of titles) {
    if (t.textContent && t.textContent.includes(partial)) return t.closest('.nocx-dialog')
  }
  return null
}

async function openProfileEditor(container: HTMLElement, profileName: string) {
  const editBtn = container.querySelector('.ui-collection-row__actions [aria-label^="Edit "]')
  expect(editBtn, `Edit button for "${profileName}" not found`).toBeTruthy()
  ;(editBtn! as HTMLElement).click()
  await vi.waitFor(() => {
    expect(findDialogByTitleContaining(container, profileName)).toBeTruthy()
  })
}

function selectProfileSection(container: HTMLElement, label: string) {
  const btn = Array.from(container.querySelectorAll('.ui-tabs__list .ui-button')).find(
    (b) => b.textContent?.trim() === label,
  )
  expect(btn, `profile tab "${label}" not found`).toBeTruthy()
  ;(btn! as HTMLElement).click()
}

function clickSegmentedOption(container: HTMLElement, label: string) {
  const option = Array.from(container.querySelectorAll('[role="radio"]')).find(
    (r) => r.textContent?.trim() === label,
  )
  expect(option, `SegmentedControl option "${label}" not found`).toBeTruthy()
  ;(option! as HTMLElement).click()
}

function clickSave(container: HTMLElement) {
  const dialog = findDialogByTitleContaining(container, 'prod-web')!
  const btn = Array.from(dialog.querySelectorAll('.ui-button')).find(
    (b) => b.textContent?.trim() === 'Save Connection',
  )
  expect(btn, 'Save Connection button not found').toBeTruthy()
  fireEvent.click(btn!)
}

describe('profile editor submit gate — real surface', () => {
  it('Save with an invalid Advanced field opens Advanced, lands the caret in the field, and never reaches the client', async () => {
    const { container, patchProfile, updateProfile } = mount()
    await waitForProfiles(container)
    await openProfileEditor(container, 'prod-web')

    // The editor opens on General; the Advanced panel (and its field) is in
    // the DOM but hidden — the state a user meets before pressing Save.
    expect(container.querySelector('#profile-ready-timeout')?.closest('[hidden]')).toBeTruthy()

    // Put the invalid value in the field the way a user does — type it.
    selectProfileSection(container, 'Advanced')
    const timeout = container.querySelector('#profile-ready-timeout') as HTMLInputElement
    fireEvent.input(timeout, { target: { value: '-5' } })
    // Back to General: the invalid field is now on a section the user is not
    // looking at when they press Save.
    selectProfileSection(container, 'General')
    expect(container.querySelector('#profile-ready-timeout')?.closest('[hidden]')).toBeTruthy()

    clickSave(container)

    // The refusal is announced, the section holding the field is opened, and
    // the caret lands in the offending control.
    await vi.waitFor(() => {
      expect(toasts().some((t) => t.message.includes('Ready timeout must be a whole number'))).toBe(
        true,
      )
    })
    await vi.waitFor(() => {
      expect(container.querySelector('#profile-ready-timeout')?.closest('[hidden]')).toBeNull()
    })
    expect(document.activeElement).toBe(container.querySelector('#profile-ready-timeout'))
    // The rule's message is under the field, the gate claimed a real focus
    // (no "could not focus" caveat), and the save stayed off the wire.
    expect(container.textContent).toContain('Ready timeout must be a whole number')
    expect(toasts()[0].message).not.toContain('could not focus')
    expect(patchProfile).not.toHaveBeenCalled()
    expect(updateProfile).not.toHaveBeenCalled()
    expect(findDialogByTitleContaining(container, 'prod-web')).toBeTruthy()
  })

  it('Save with an invalid forward row opens Forwards, refuses, and honestly reports it could not focus', async () => {
    const { container, patchProfile, updateProfile } = mount()
    await waitForProfiles(container)
    await openProfileEditor(container, 'prod-web')

    // Create the invalid row the way a user does (the new row has no
    // destination), then leave the section — the offending field is on a
    // panel the user is not looking at when they press Save.
    selectProfileSection(container, 'Forwards')
    const addBtn = await vi.waitFor(() => {
      const btn = Array.from(container.querySelectorAll('.ui-button')).find(
        (b) => b.textContent?.trim() === 'Add forward',
      )
      expect(btn).toBeTruthy()
      return btn as HTMLElement
    })
    addBtn.click()
    await vi.waitFor(() => {
      expect(container.querySelectorAll('.ui-row-list__row').length).toBe(1)
    })
    selectProfileSection(container, 'General')

    clickSave(container)

    await vi.waitFor(() => {
      expect(
        toasts().some((t) => t.message.includes('Forward 1: destination is required for local')),
      ).toBe(true)
    })
    // The gate switched the editor to the section holding the offending row.
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-row-list__row')?.closest('[hidden]')).toBeNull()
    })
    expect(container.textContent).toContain('Forward 1: destination is required for local')
    // Honestly: the forwards list has no single focusable control by design
    // (PROFILE_CONTROL_ID.forwards is undefined), so the gate says it could
    // not focus rather than pretending it did — and the save stayed off the
    // wire.
    expect(toasts().some((t) => t.message.includes('could not focus'))).toBe(true)
    expect(patchProfile).not.toHaveBeenCalled()
    expect(updateProfile).not.toHaveBeenCalled()
    expect(findDialogByTitleContaining(container, 'prod-web')).toBeTruthy()
  })

  it('a refused save whose field has no focusable control (stored-secret key) says so and focuses nothing else', async () => {
    const { container, patchProfile, updateProfile } = mount()
    await waitForProfiles(container)
    await openProfileEditor(container, 'prod-web')

    // The key field's controlId resolves to undefined in stored-secret mode:
    // only path and pasted-material modes have a text control to focus.
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')
    await vi.waitFor(() => {
      expect(container.querySelector('[aria-label="Key input mode"]')).toBeTruthy()
    })
    clickSegmentedOption(container, 'Secret')

    const activeBefore = document.activeElement
    clickSave(container)

    await vi.waitFor(() => {
      expect(
        toasts().some(
          (t) =>
            t.message.includes('Choose a private key:') &&
            t.message.includes('could not focus the first field'),
        ),
      ).toBe(true)
    })
    // The refusal is honest: the toast carries the key rule's message and the
    // caveat, and no count — a single failing field's own message is the whole
    // story, so "1 field needs attention" would only repeat it (the gate
    // counts only when several fields fail). The caret stayed where it was:
    // there is no control to land it in.
    expect(document.activeElement).toBe(activeBefore)
    expect(patchProfile).not.toHaveBeenCalled()
    expect(updateProfile).not.toHaveBeenCalled()
    expect(findDialogByTitleContaining(container, 'prod-web')).toBeTruthy()
    // nocx-74cn.2: the key rule's message has a home. The Private Key row
    // carries it through the kit's Field error slot — the key can come from a
    // file, a path, pasted material, or a stored secret, so no single input
    // owns the rule; the row does. In stored-secret mode there is no
    // focusable control, so the row is exactly where a user reading it will
    // see the message; the gate's "could not focus" caveat above is asserted
    // deliberate.
    await vi.waitFor(() => {
      const keyError = Array.from(container.querySelectorAll('.ui-field-error')).find((e) =>
        e.textContent?.includes('Choose a private key:'),
      )
      expect(keyError).toBeTruthy()
    })
  })

  it('a refused save in the default file mode marks the Private Key row with the rule message', async () => {
    const { container, patchProfile, updateProfile } = mount()
    await waitForProfiles(container)
    await openProfileEditor(container, 'prod-web')

    // The bead's reproduction steps exactly: Public Key, and no key at all.
    // The key input opens in its default mode — 'Choose file'
    // (DEFAULT_KEY_MODE) — and the user uploads nothing.
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')
    await vi.waitFor(() => {
      expect(container.querySelector('[aria-label="Key input mode"]')).toBeTruthy()
    })

    clickSave(container)

    await vi.waitFor(() => {
      expect(
        toasts().some(
          (t) =>
            t.message.includes('Choose a private key:') &&
            t.message.includes('could not focus the first field'),
        ),
      ).toBe(true)
    })
    // The rule's message is under the row, not only in the toast: the form
    // itself says which row is wrong.
    await vi.waitFor(() => {
      const keyError = Array.from(container.querySelectorAll('.ui-field-error')).find((e) =>
        e.textContent?.includes('Choose a private key:'),
      )
      expect(keyError).toBeTruthy()
    })
    expect(patchProfile).not.toHaveBeenCalled()
    expect(updateProfile).not.toHaveBeenCalled()
    expect(findDialogByTitleContaining(container, 'prod-web')).toBeTruthy()
  })

  it('a refused save in path mode focuses the path input and marks the row with the rule message', async () => {
    const { container, patchProfile, updateProfile } = mount()
    await waitForProfiles(container)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')
    await vi.waitFor(() => {
      expect(container.querySelector('[aria-label="Key input mode"]')).toBeTruthy()
    })
    clickSegmentedOption(container, 'Path')

    clickSave(container)

    await vi.waitFor(() => {
      expect(toasts().some((t) => t.message.includes('Choose a private key:'))).toBe(true)
    })
    // The path field is the mode's focusable control: the gate opens the
    // section, lands the caret in it, and needs no caveat.
    expect(document.activeElement).toBe(container.querySelector('#profile-key-path'))
    expect(toasts().some((t) => t.message.includes('could not focus'))).toBe(false)
    await vi.waitFor(() => {
      const keyError = Array.from(container.querySelectorAll('.ui-field-error')).find((e) =>
        e.textContent?.includes('Choose a private key:'),
      )
      expect(keyError).toBeTruthy()
    })
    expect(patchProfile).not.toHaveBeenCalled()
    expect(updateProfile).not.toHaveBeenCalled()
    expect(findDialogByTitleContaining(container, 'prod-web')).toBeTruthy()
  })

  it('a refused save in material mode focuses the key textarea and marks the row with the rule message', async () => {
    const { container, patchProfile, updateProfile } = mount()
    await waitForProfiles(container)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')
    await vi.waitFor(() => {
      expect(container.querySelector('[aria-label="Key input mode"]')).toBeTruthy()
    })
    clickSegmentedOption(container, 'Paste key')

    clickSave(container)

    await vi.waitFor(() => {
      expect(toasts().some((t) => t.message.includes('Choose a private key:'))).toBe(true)
    })
    expect(document.activeElement).toBe(container.querySelector('#profile-key-text'))
    expect(toasts().some((t) => t.message.includes('could not focus'))).toBe(false)
    await vi.waitFor(() => {
      const keyError = Array.from(container.querySelectorAll('.ui-field-error')).find((e) =>
        e.textContent?.includes('Choose a private key:'),
      )
      expect(keyError).toBeTruthy()
    })
    expect(patchProfile).not.toHaveBeenCalled()
    expect(updateProfile).not.toHaveBeenCalled()
    expect(findDialogByTitleContaining(container, 'prod-web')).toBeTruthy()
  })
})
