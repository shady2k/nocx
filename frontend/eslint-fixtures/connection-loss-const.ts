// Negative fixture for nocx/no-connection-loss (ADR-ba6xet1b,
// nocx-oalo1.3.5). `interrupted` is the connection manager's verdict; the
// renderer may draw it and may not mint it. The second case reaches the
// literal through a const, which a rule that compares literal nodes cannot
// see.

const LOST = 'interrupted'

export function lost(): { cause: string } {
  return { cause: LOST }
}

export function lostInline(): { cause: 'interrupted' } {
  return { cause: 'interrupted' }
}
