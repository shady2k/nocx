// Passive status row. The caller supplies session facts; unknown values stay absent.
export interface TerminalStatusFacts {
  shell?: string
  encoding: string
}
export function createTerminalStatus(facts: TerminalStatusFacts): HTMLElement {
  const el = document.createElement('div')
  el.className = 'ui-terminal-status'
  updateTerminalStatus(el, facts)
  return el
}
export function updateTerminalStatus(el: HTMLElement, facts: TerminalStatusFacts): void {
  const shell = document.createElement('span')
  shell.textContent = facts.shell?.split('/').pop() ?? ''
  shell.title = facts.shell ?? ''
  const encoding = document.createElement('span')
  encoding.textContent = facts.encoding
  encoding.title = 'Terminal stream encoding'
  el.replaceChildren(shell, encoding)
}
