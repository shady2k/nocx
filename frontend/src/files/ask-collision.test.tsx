// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { askCollision } from './ask-collision'

afterEach(() => {
  document.body.innerHTML = ''
})

/** The dialog's buttons, in the order the kit lays them out. */
function buttonSaying(label: string): HTMLElement {
  const found = [...document.querySelectorAll<HTMLElement>('button')].find(
    (b) => (b.textContent ?? '').trim() === label,
  )
  if (found === undefined) throw new Error(`no button saying ${label}`)
  return found
}

describe('asking about one collision', () => {
  it('answers with what the person picked and how far it reaches', async () => {
    const answer = askCollision({ name: 'notes.txt', destination: '/srv', remaining: 3 })
    // The checkbox is drawn because there are others to apply to.
    document.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click()
    buttonSaying('Overwrite').click()
    await expect(answer).resolves.toEqual({ answer: 'overwrite', applyToAll: true })
  })

  it('reads a dismissal as skip, never as overwrite', async () => {
    const answer = askCollision({ name: 'notes.txt', destination: '/srv', remaining: 1 })
    // The native cancel — Escape, and the same path the close button and a
    // click outside take.
    document
      .querySelector('dialog')!
      .dispatchEvent(new Event('cancel', { bubbles: true, cancelable: true }))
    // Getting this backwards would let a stray keypress destroy a file on
    // a server, which is the one outcome the dialog exists to prevent.
    await expect(answer).resolves.toEqual({ answer: 'skip', applyToAll: false })
  })

  it('takes itself down with the answer, so the next question is a fresh one', async () => {
    const answer = askCollision({ name: 'a.txt', destination: '/srv', remaining: 1 })
    buttonSaying('Keep both').click()
    await answer
    // The disposal is deferred a microtask so Dialog's own cleanup runs
    // against a live root; drain it before looking.
    await Promise.resolve()
    await Promise.resolve()
    expect(document.querySelector('dialog')).toBeNull()
  })
})
