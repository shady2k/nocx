// Negative fixture for nocx/no-connection-open (ADR-ba6xet1b, nocx-oalo1.3.5).
// The SAME source is linted twice by src/connection-owner-gate.test.ts: at a
// path outside frontend/src/connection/ it must report, and at a path inside
// it must stay silent — that is the whole rule, in one file.
//
// The interface is deliberate too: a type describes the seam and reports
// nothing, because a type decides nothing. The calls are the surface.

interface Client {
  openSSHSession(profileId: string): Promise<void>
  reconnect(): Promise<void>
}

export function reopen(client: Client, profileId: string): void {
  void client.openSSHSession(profileId)
  void client.reconnect()
}
