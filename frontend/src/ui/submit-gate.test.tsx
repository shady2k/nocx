// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { createFormValidation, required } from './validation'
import { createSignal } from 'solid-js'
import { createSubmitGate } from './submit-gate'
import { ToastHost, clearToasts, toasts } from './toast'

afterEach(() => {
  cleanup()
  clearToasts()
})

// The gate's contract is the user's submit button: press it on a form that
// does not pass, and the form tells you — every failing field is revealed,
// the first one is focused, and the toast region carries the first failing
// rule's message — with how many fields need attention when more than one
// fails. These tests
// drive that seam with real inputs in the DOM and the
// real ToastHost; nothing here is mocked.
function refuse() {
  return createFormValidation({
    host: () => required('Host')(''),
    port: () => required('Port')(''),
  })
}

function mountForm(hideHostPanel = false) {
  return render(() => (
    <>
      <div hidden={hideHostPanel ? true : undefined}>
        <input id="host" />
      </div>
      <input id="port" />
      <ToastHost />
    </>
  ))
}

describe('createSubmitGate', () => {
  it('lets a form that passes submit without revealing or announcing', async () => {
    const v = createFormValidation({ host: () => required('Host')('box') })
    render(() => (
      <>
        <input id="host" />
        <ToastHost />
      </>
    ))
    const gate = createSubmitGate(v)
    expect(await gate()).toBe(true)
    expect(toasts()).toHaveLength(0)
    expect(v.error('host')).toBeUndefined()
  })

  it('focuses the first failing control and states the count in the live region', async () => {
    const v = refuse()
    mountForm()
    const gate = createSubmitGate(v)
    await gate()

    // The first failing field in declaration order is the one the user lands on.
    expect(document.activeElement?.id).toBe('host')
    // The refusal is announced through the toast host's region, and every
    // failing field is revealed at once.
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].message).toBe('Host is required — 2 fields need attention')
    expect(v.error('host')).toBe('Host is required')
    expect(v.error('port')).toBe('Port is required')
  })

  it('reads one failing field as its rule message alone, with no count', async () => {
    const v = createFormValidation({
      host: () => required('Host')(''),
      port: () => required('Port')('8080'),
    })
    mountForm()
    const gate = createSubmitGate(v)
    await gate()
    expect(toasts()[0].message).toBe('Host is required')
    expect(toasts()[0].message).not.toContain('field needs attention')
  })
  it('with one failing field the toast carries that rule message verbatim, never a bare count', async () => {
    const v = createFormValidation({
      host: () => required('Host')('box'),
      port: () => required('Port')(''),
    })
    mountForm()
    const gate = createSubmitGate(v)
    await gate()
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].message).toBe('Port is required')
  })

  it('with several failing fields the toast carries the first failing rule message and the count', async () => {
    const v = createFormValidation({
      host: () => required('Host')('box'),
      port: () => required('Port')(''),
      forwards: () => 'Forward 1: destination is required for local',
    })
    mountForm()
    const gate = createSubmitGate(v)
    await gate()
    expect(toasts()).toHaveLength(1)
    // Declaration order decides which message leads, exactly as firstError()
    // decides it: port is the first failing rule, and the forwards message
    // stays out of the announcement.
    expect(toasts()[0].message).toBe('Port is required — 2 fields need attention')
  })

  it('calls the reveal hook with the failing field before focus, so the field ends up focused', async () => {
    const v = refuse()
    mountForm(true)
    const calls: string[] = []
    const gate = createSubmitGate(v, {
      reveal: (field) => {
        calls.push(field)
        // What a Tabs surface does: open the panel holding the field.
        document.querySelector<HTMLElement>('div[hidden]')!.hidden = false
      },
    })
    await gate()
    expect(calls).toEqual(['host'])
    expect(document.activeElement?.id).toBe('host')
    // No "could not focus" caveat: the reveal was enough.
    expect(toasts()[0].message).toBe('Host is required — 2 fields need attention')
  })

  it('waits for an async reveal before attempting focus', async () => {
    const v = refuse()
    mountForm(true)
    let openPanel: () => void = () => {}
    const revealPromise = new Promise<void>((resolve) => {
      openPanel = () => {
        document.querySelector<HTMLElement>('div[hidden]')!.hidden = false
        resolve()
      }
    })
    const gate = createSubmitGate(v, {
      reveal: () => revealPromise,
    })
    const pending = gate()
    // Reveal is still in flight: focus must not have been attempted yet.
    expect(document.activeElement?.id).not.toBe('host')
    openPanel()
    expect(await pending).toBe(false)
    expect(document.activeElement?.id).toBe('host')
  })

  // nocx-74cn.3: the gate's reveal contract allows an async hook, and during
  // an awaited reveal the world is free to change — a lazily loaded panel can
  // populate a field, reactive rules can move. The gate must act on the state
  // it can see AFTER the reveal settles, never on the snapshot it read before
  // the await. These two tests make that mutation real: the reveal hook
  // changes the validation itself, so a gate that never re-reads fails them.
  it('after an async reveal that makes the form valid, allows the submit — no toast, no focus', async () => {
    const [host, setHost] = createSignal('')
    const v = createFormValidation({ host: () => required('Host')(host()) })
    mountForm(true)
    const gate = createSubmitGate(v, {
      reveal: async () => {
        // The panel settles AFTER the gate read the failing field, and
        // populating it makes the form valid — the exact window the contract
        // opens.
        await Promise.resolve()
        setHost('box')
        document.querySelector<HTMLElement>('div[hidden]')!.hidden = false
      },
    })
    // Decision (written down in submit-gate.ts): the submit is ALLOWED — the
    // gate's contract is "true means the values pass", and they pass now.
    expect(await gate()).toBe(true)
    expect(toasts()).toHaveLength(0)
    expect(document.activeElement?.id).not.toBe('host')
  })

  it('after an async reveal that moves the first error, focuses and announces the new first field', async () => {
    const [host, setHost] = createSignal('')
    const v = createFormValidation({
      host: () => required('Host')(host()),
      port: () => required('Port')(''),
    })
    mountForm(true)
    const gate = createSubmitGate(v, {
      reveal: async () => {
        await Promise.resolve()
        // Host was first in declaration order; the reveal makes it valid, so
        // the first error moves to port. A gate that kept its pre-await
        // snapshot would focus host and announce the stale message.
        setHost('box')
        const panel = document.querySelector<HTMLElement>('div[hidden]')
        if (panel) panel.hidden = false
      },
    })
    expect(await gate()).toBe(false)
    expect(document.activeElement?.id).toBe('port')
    expect(toasts()[0].message).toBe('Port is required')
  })

  it('reports it could not focus when no reveal hook opens the panel', async () => {
    const v = refuse()
    mountForm(true)
    const gate = createSubmitGate(v)
    await gate()
    expect(document.activeElement?.id).not.toBe('host')
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].message).toBe(
      'Host is required — 2 fields need attention — could not focus the first field',
    )
  })

  it('reports it could not focus when no control exists for the field', async () => {
    const v = createFormValidation({
      forwards: () => 'Forward 1: destination is required for local',
    })
    render(() => <ToastHost />)
    const gate = createSubmitGate(v)
    await gate()
    expect(toasts()[0].message).toBe(
      'Forward 1: destination is required for local — could not focus the first field',
    )
  })

  it('announces through the toast host region and adds no other live region', async () => {
    const v = refuse()
    mountForm()
    const gate = createSubmitGate(v)
    await gate()
    const host = document.querySelector('.ui-toast-host')
    expect(host?.getAttribute('role')).toBe('status')
    expect(document.querySelectorAll('[aria-live]')).toHaveLength(1)
    expect(host?.textContent).toContain('Host is required — 2 fields need attention')
  })

  it('does not re-announce on every keystroke once the first invalid field is focused', async () => {
    const v = refuse()
    mountForm()
    ;(document.getElementById('host') as HTMLInputElement).focus()
    const gate = createSubmitGate(v)
    await gate()
    expect(toasts()).toHaveLength(1)
    // Typing (the user fixing the field) announces nothing by itself; only a
    // further refused submit would raise another toast.
    v.answer('host', 'box')
    expect(toasts()).toHaveLength(1)
  })
})
