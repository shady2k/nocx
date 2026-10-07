# ADR-0082 — Native CI owns its guest kernel, not the host's

- **Status:** Accepted (user-approved CI execution design; implementation acceptance pending)
- **Date:** 2026-10-07
- **Related:** ADR-0081; `nocx-a0qhd.17`.
- **Design:** [Linux CI VM architecture spine](../../_bmad-output/planning-artifacts/architecture-sandbox-linux-ci-vm/architecture-sandbox-linux-ci-vm.md).

## Context

The Ubuntu 26.04 hosted jobs in release dry-run
[37600777587](https://github.com/shady2k/nocx/actions/runs/37600777587) reported
Landlock ABI8. The image label did not provide the ABI9 pathname-socket boundary
required by ADR-0081. Source and packaged smoke correctly refused that runtime.
Lowering the floor would change the product guarantee; a container cannot supply
a different kernel. Enumerating repository self-hosted runners returned HTTP403.
The user chose an isolated VM CI job, not a workstation kernel change or automatic
runner registration, and separately left Darwin lint as a blocker without gosec
suppressions. Critical npm audit findings still block PR creation.

## Decision

Only the Linux native CI execution seam boots a real, SHA-256-pinned upstream
Linux 7.2.9 kernel under QEMU/KVM. Its built-in Landlock and seccomp notification
features remain subject to the existing smoke's runtime ABI and diagnostic checks.
The invoking host kernel stays unchanged. Missing KVM, inputs or guest evidence
fails; there is no ABI downgrade, software-emulation substitute or retry.

The guest gets a disposable ext4 disk, not an exported host filesystem, HOME,
control socket or network interface. The existing source and packaged consumers
run as an unprivileged guest user with offline Go/build inputs and their generated
VT/notices/artifacts in the existing repository layout. Source CI builds current
helpers before boot; release CI stages the exact one-origin downloaded archives
and never rebuilds those current helper bytes. macOS continues natively.

A single `scripts/sandbox-linux-vm/run.sh` serves both Linux workflow callsites.
Root guest PID1 aggregates real consumer exits and writes an explicit result to
QEMU's isa-debug-exit device: success `0x10` encodes host status33, failure `0x11`
encodes35. Only33 is accepted. In particular QEMU startup status1 or normal shutdown
status0 cannot count as a completed native proof. RAM, CPU, disk and stage deadlines
are bounded within the existing job ceilings; cleanup owns only its invocation's
resources.

Image compilation uses an invocation-owned docker-container BuildKit builder,
pinned to `moby/buildkit:v0.33.1`, bounded to two CPUs and 2GiB memory, and removed
with its private state after image loading or failure. It never prunes the shared
builder. This avoids process-lifetime cache-reference retention after a canceled
RUN ([upstream #7206](https://github.com/moby/buildkit/issues/7206)); the host Docker
daemon is not restarted. Native inputs resolve the actual current and historical
consumer package graphs, not unrelated application/UI/tool modules. The kernel
uses `allnoconfig` plus explicit native/userspace features within the same budget;
every requested fragment value must survive configuration before compilation.
The fixed x86 source extraction omits Documentation, non-x86
arm/arm64/mips/powerpc/riscv implementation trees and unrelated perf/testing tools,
but retains their Kconfig menus required by unconditional parser sources. One
shared Ubuntu compiler/userspace package layer avoids duplicate installations.
The pinned tar
is fetched with the host resolver inside the image budget and SHA-256-verified
by the image before extraction.

## Alternatives

- Another hosted image label: still does not prove the runtime ABI or KVM fit.
- Container-only execution: retains the incompatible host kernel.
- ABI8 fallback: removes the pathname-socket guarantee and is rejected.
- Installing/rebooting a hosted or workstation kernel: changes an owner outside
  the disposable native execution seam.
- Automatic self-hosted registration: unavailable authority and an unrelated
  operational commitment.

## Acceptance and limitations

Cold-cache local and hosted source/packaged runs must prove the real kernel,
positive and negative filesystem/descendant/IPC/TCP/diagnostic behavior, genuine
failure propagation and bounded teardown. Hosted nested virtualization is a
runtime prerequisite, not a guarantee implied by an image label. VM success does
not remove Darwin lint or npm audit blockers and does not prove an unexercised
app-bundle/AppImage/signing hash chain. This introduces no application VM feature,
network-isolation promise or new helper artifact/update policy.

Local cold execution has proved source plus packaged consumers (673.64s; final
shared-staging-deadline implementation 726.22s) and a standalone packaged lane
using the exact seven archives of release dry-run 37600777587 / SHA `34e8c85a`
(586.20s; final shared-staging-deadline implementation 689.18s), without rebuilding
current helpers.
All booted Linux 7.2.9, queried Landlock ABI10 and activated two CPUs.
An actual failed native consumer propagated guest status35; an actual QEMU
unsupported-machine startup propagated status1. The host rejected both rather
than accepting either as terminal33 success. Owned images, builders/state and
invocation directories were removed. Hosted execution on the integrated SHA is
still required before implementation acceptance.
