# ADR-0100 — A Darwin blocking channel write has an owned, killable writer

- **Status:** Accepted
- **Date:** 2026-10-21
- **Decided by:** the worker on `nocx-sd8uk.1.1`, completing native Fish support
  after the macOS backpressure failure in PR #445's CI.
- **Related:** AD-5/6/7/8, [ADR-0024](0024-authenticated-shell-integration-channel.md),
  [ADR-0049](0049-the-channel-we-own-is-the-carrier.md), and
  [ADR-0099](0099-fish-keeps-native-startup-and-the-helper-adapts-its-private-io.md).
  Supersedes only ADR-0099's per-call-nonblocking **write mechanism on Darwin**.
  Native startup, private activation, framing, lifecycle authority and reads are unchanged.

## Context and evidence

CI run [39684594700](https://github.com/shady2k/nocx/actions/runs/39684594700)
passed native Fish on macOS, then blocked in the socket-backpressure fixture's
`SendmsgN(MSG_DONTWAIT)` until the package's ten-minute timeout. The production
writer used the same operation. This was a platform contract error, not a reason
to extend a test timeout.

Darwin's [socket send-buffer-space check](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/uipc_socket.c)
uses `SS_NBIO` or the kernel-private `MSG_NBIO` to avoid its blocking `sbwait`;
`MSG_DONTWAIT` alone does not prevent that wait. Polling for writable space does
not reserve it for a following blocking send.

The shell's connected descriptor survives short-lived helpers and is inherited by
nested Bash. `dup` shares its open-file description: changing `O_NONBLOCK` on a
duplicate still changes the shell's behavior. `shutdown` would likewise affect the
surviving channel. A blocked Go write cannot simply be abandoned in a goroutine.

## Decision

Keep Linux's per-call-nonblocking path and Darwin descriptors that are already
nonblocking on the existing bounded writer. For a **blocking Darwin description**,
the helper duplicates the descriptor with `F_DUPFD_CLOEXEC` and starts the fixed
macOS base utility `/bin/cat` with that owned duplicate as stdout. This adds no
installed package or language runtime. Apple's [raw cat implementation](https://github.com/apple-oss-distributions/text_cmds/blob/main/cat/cat.c)
copies bytes without changing socket flags.

The canonical `lifecyclecodec.WriteRawFrame` writes through a new anonymous stdin
pipe. The operation's original absolute context deadline governs the worker;
cancellation kills only that process, releasing the pipe reader and any blocked
parent write. Close stdin and call `Wait` before returning: both the worker and
`CommandContext`'s watcher must finish. Close only owned copies, never shut down
the channel or alter its shared flags.

Callers continue to serialize frames. A worker failure fails that operation;
never replay a possibly partial frame. Existing lifecycle loss/recovery owns the
subsequent behavior. Payloads never enter argv, environment, named files or child
diagnostics. The public helper boundary continues to return fixed error strings.

## Consequences and verification

Blocking Darwin sends incur one additional short-lived base-OS process. Avoiding
that process would require a complete nonblocking-consumer cutover, including
nested Bash, rather than a local Fish flag change. Private kernel flags, shared
socket timeout changes and unjoined blocked goroutines are rejected alternatives.

The backpressure fixture now fills its exclusively owned socket in nonblocking
mode and restores blocking mode **before handoff**. A bounded child process tests
cancellation/deadline with a maximum-size frame, unchanged shared flags and no
appended bytes. Darwin-specific checks cover worker start failure and exact
maximum-frame delivery without trailing data or closing the surviving stream.

Local Linux checks exercised the actual Darwin writer through a temporary build
overlay, including the built helper CLI under full socket backpressure. This is
not native Darwin evidence; the macOS CI job remains the native platform gate.
