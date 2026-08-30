// THE frontend half of `ssh.reconnect`: what a pane does when its SSH
// connection is lost.
//
// A module with one owner, like restore-setting.ts and for the same reason —
// the answer is needed by a PANE, at the moment a session dies, and threading
// it down through the pane manager's constructor would put a policy in a
// signature that is about the window's parts.
//
// Unlike restore.onStartup this is read EVERY time it is needed rather than
// once at boot: the decision it governs happens mid-session, so a person who
// changes it must have changed it for the next loss and not for the next
// launch.

/** The declared key. */
export const SSH_RECONNECT_KEY = 'ssh.reconnect'

/** What a lost pane may do. */
export type ReconnectPolicy = 'ask' | 'auto' | 'never'

/** The declared default (internal/settings/settings.go: SSHReconnect).
 *  `ask` rather than `auto`, and a failed settings read must land here for
 *  the same reason the default does: reconnecting by itself can duplicate
 *  work that is still running on the far host, and a fetch that failed is not
 *  permission to do that. */
export const SSH_RECONNECT_DEFAULT: ReconnectPolicy = 'ask'

/** How many times `auto` tries before it falls back to the button.
 *
 *  Three, and then it stops. A pane that retried forever would hammer a host
 *  that is down and hide from the person that anything is wrong; the offer on
 *  the tab is the honest end state, and it is the same one `ask` starts at. */
export const AUTO_RECONNECT_ATTEMPTS = 3

/** How long to wait before attempt n (1-based), in milliseconds.
 *
 *  Backoff rather than a fixed interval: the common cause of a lost SSH
 *  connection is a network that is not back yet, and three probes in three
 *  seconds all fail for the same reason. Capped, because the last attempt
 *  should still land while the person is looking at the tab. */
export function autoReconnectDelayMs(attempt: number): number {
  return Math.min(1000 * 2 ** Math.max(0, attempt - 1), 8000)
}

let policy: ReconnectPolicy = SSH_RECONNECT_DEFAULT

/** Adopt the backend's value. Anything that is not one of the three — an
 *  older backend, a failed fetch — leaves the declared default in place. */
export function applySSHReconnect(value: unknown): void {
  if (value === 'ask' || value === 'auto' || value === 'never') policy = value
}

/** What a pane should do when its session is lost. */
export function sshReconnectPolicy(): ReconnectPolicy {
  return policy
}
