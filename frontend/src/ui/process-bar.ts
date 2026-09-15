// ProcessBar — the running-state footer that replaces the composer while a
// normal-buffer command owns the pane (decision
// 2026-09-15-terminal-screen-mockup-decision.md §1, "Running-state gap"):
// an info mark and "Process running" on the leading edge, "Send input" and
// "Stop" — with a separate "Interrupt" for Ctrl+C — on the trailing one.
//
// PRESENTATION AND CALLBACKS ONLY (decision §3's frozen ProcessBar seam):
// this module calls no client API and owns no lifecycle fact. TerminalContent
// decides WHEN to show it (ordinary running, normal buffer — never
// alternate-screen, never while summoned) and WHICH callback each button
// reaches: `onStop` is the same stop owner the running block's own Stop
// uses (`runningActions.stop`, `signalActiveCommand('stop')`), `onInterrupt`
// is a DIFFERENT intent (`signalActiveCommand('interrupt')`) as the decision
// record's semantic-mismatch note requires — "Stop Ctrl+C" is not an
// accurate statement of this branch's Stop/Interrupt semantics, so the
// footer never claims Ctrl+C is Stop's shortcut, only Interrupt's.
//
// Vanilla-emitted, like PaneContext and ProcessBar's composer-card sibling:
// the terminal screen that uses it is imperative DOM (ADR-0012), built once
// per pane and toggled with `hidden`. No Solid version exists until a Solid
// surface needs one.
//
// Identity `ui-process-bar`; a surface places it and never repaints it
// (ui/README).

import { InfoIcon, iconElement } from './icons'
import { createButton } from './button-element'

export interface ProcessBarFacts {
  /** Whether the trailing controls have anywhere to send input right now
   *  (decision: "Hide/disable the action when there is no writable
   *  target, using the current capability result"). `onSendInput`,
   *  `onStop` and `onInterrupt` are all no-ops while false. */
  inputAvailable: boolean
}

export interface ProcessBarActions {
  /** Focuses the existing live terminal — never submits an empty command,
   *  inserts a newline, or summons Ask (decision: "Thus there remains one
   *  place keystrokes go"). */
  onSendInput: () => void
  /** The SAME stop owner as the running block's own Stop
   *  (`runningActions.stop`) — one intent, reachable two ways. */
  onStop: () => void
  /** A DIFFERENT intent from Stop: `signalActiveCommand('interrupt')`,
   *  the byte-0x03 escalation. Never wired to the same handler as Stop. */
  onInterrupt: () => void
}

export function createProcessBar(actions: ProcessBarActions): HTMLElement {
  const el = document.createElement('div')
  el.className = 'ui-process-bar'

  const info = document.createElement('span')
  info.className = 'ui-process-bar__info'
  const icon = iconElement(InfoIcon)
  icon.classList.add('ui-process-bar__icon')
  const label = document.createElement('span')
  label.textContent = 'Process running'
  info.append(icon, label)

  const controls = document.createElement('span')
  controls.className = 'ui-process-bar__controls'

  const sendInput = createButton({
    label: 'Send input',
    variant: 'ghost',
    ariaLabel: 'Send input — focus the live terminal',
    onClick: () => actions.onSendInput(),
  })
  sendInput.dataset.control = 'send-input'

  const interrupt = createButton({
    label: 'Interrupt',
    variant: 'ghost',
    title: 'Ctrl+C',
    onClick: () => actions.onInterrupt(),
  })
  interrupt.dataset.control = 'interrupt'
  const interruptHint = document.createElement('span')
  interruptHint.className = 'ui-process-bar__hint'
  interruptHint.setAttribute('aria-hidden', 'true')
  interruptHint.textContent = 'Ctrl+C'

  const stop = createButton({
    label: 'Stop',
    variant: 'default',
    onClick: () => actions.onStop(),
  })
  stop.dataset.control = 'stop'

  controls.append(sendInput, interrupt, interruptHint, stop)
  el.append(info, controls)
  return el
}

/** Reflect capability facts onto an existing bar — TerminalContent's
 *  lifecycle-ownership seam, on every reconciliation while the bar is
 *  shown. Only "Send input" gates on a writable target: Stop and Interrupt
 *  travel through `signalActiveCommand`, which already refuses on its own
 *  when nothing is running (the bar is shown only while something is), so
 *  disabling them here a second time would be a second owner of that
 *  answer. */
export function updateProcessBar(el: HTMLElement, facts: ProcessBarFacts): void {
  const btn = el.querySelector<HTMLButtonElement>('[data-control="send-input"]')
  if (btn) btn.disabled = !facts.inputAvailable
}
