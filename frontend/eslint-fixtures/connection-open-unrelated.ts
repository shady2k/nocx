// Positive control for nocx/no-connection-open (ADR-ba6xet1b,
// nocx-oalo1.3.5): a method called `reconnect` on a type that is NOT the ipc
// client is not a connection entry. A rule that reports the name would refuse a device or a socket its own
// `reconnect`, so the check asks which type declares the member.

interface Reconnectable {
  reconnect(): void
}

export function restore(device: Reconnectable): void {
  device.reconnect()
}
