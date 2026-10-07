---
name: Sandbox Linux CI VM
type: architecture-spine
purpose: build-substrate
altitude: epic
paradigm: isolated batch worker
scope: Linux native CI execution; no application policy, dependency or host-kernel changes
status: final
created: 2026-10-07
updated: 2026-10-07
binds: [nocx-a0qhd.16, nocx-a0qhd.17]
sources:
  - docs/superpowers/specs/2026-10-05-sandbox-main-rewrite-design.md
  - docs/decisions/0081-filesystem-authority-belongs-to-one-helper-owned-launch.md
  - https://www.kernel.org/releases.json
  - https://cdn.kernel.org/pub/linux/kernel/v7.x/sha256sums.asc
  - https://www.qemu.org/docs/master/system/linuxboot.html
  - https://packages.ubuntu.com/noble/qemu-system-x86
  - https://github.blog/changelog/2024-04-02-github-actions-hardware-accelerated-android-virtualization-now-available/
  - https://github.com/qemu/qemu/blob/v8.2.2/hw/misc/debugexit.c
companions: []
---

# Architecture Spine — Sandbox Linux CI VM

## Design Paradigm

An isolated batch worker boots a real Linux kernel in QEMU/KVM. Existing smoke
consumers execute inside the guest, not against a simulated syscall response.
The host builds a disposable image and observes the guest's terminal result.

## Inherited Invariants

| Inherited                                              | From parent                     | Binds here                                                   |
| ------------------------------------------------------ | ------------------------------- | ------------------------------------------------------------ |
| AD-1…AD-10                                             | docs/architecture.md            | No transport, terminal, session or module ownership changes  |
| Native ABI9, fail-closed, exact immutable grant        | ADR-0081                        | Existing helper and smoke code remain authoritative          |
| One-origin release archives and 20 MiB ceiling         | Sandbox implementation contract | Packaged lane never rebuilds current helpers                 |
| No PR, dependency upgrades or Darwin lint suppressions | User decisions                  | VM tools belong only to CI; remaining blockers stay explicit |

## Invariants & Rules

### AD-11 — Real guest kernel, unchanged host [ADOPTED]

- **Binds:** Linux source gate, release packaged gate, local VM verification.
- **Prevents:** A hosted image label or mocked ABI being counted as enforcement.
- **Rule:** Boot SHA-256-pinned upstream Linux 7.2.9 with built-in Landlock,
  seccomp/filter/user notification, ext4, virtio PCI/block, devtmpfs, Unix/IPv4,
  PTY and serial support. Use `/dev/vda` as root, `/ci-init` as PID1 and
  `lsm=landlock`; retain native diagnostics assertions. Require readable/writable
  `/dev/kvm` and successful KVM VM creation, then boot with `-accel kvm`; no host
  kernel install, reboot, ABI downgrade or TCG fallback. Existing smoke queries
  ABI and refuses below 9. Report kernel, QEMU version and kernel digest. A runner
  label does not prove nested-virtualization viability; cold hosted execution does.

### AD-12 — Guest owns its complete disposable filesystem

- **Binds:** Image builder, guest init, cleanup.
- **Prevents:** Guest access to user state or changes outside the proof fixture.
- **Rule:** Give QEMU one private raw ext4 image populated from a Ubuntu 24.04
  userspace. No host filesystem exports, shared HOME, control sockets, secrets,
  network device or listening port. Stage the input layout below without host
  credentials, runtime/e2e state or tracker data. Include Git/tar, native GCC,
  binutils/libc development headers, the Go SDK and the complete public module
  closure of both current and pinned historical revisions. Proofs use UID1001;
  PID1 alone mounts proc/sys/dev/pts, enables loopback and reports termination.
  Teardown targets only this invocation's container/image/temp paths and QEMU PID.

| Guest input                                                          | Contract                                                                                                                                                             |
| -------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/ci/repo`                                                           | Tracked working-tree files; proof cwd; readable by UID1001                                                                                                           |
| `/ci/repo/.git`                                                      | Credential-free HEAD object closure; `core.bare=false`, worktree `/ci/repo`; historical tree `3160c5c6cfcee34b166e13480f1f71de2ac9901c` must exist; owned by UID1001 |
| `/ci/repo/build/libghostty-vt/vendor`                                | Verified VT vendor layout, including native GNU archive and headers, unchanged from existing fetch                                                                   |
| `/ci/repo/internal/helper/notices/licenses/THIRD_PARTY_LICENSES.txt` | Existing generated verified notices                                                                                                                                  |
| `/ci/repo/internal/helper/deploy/artifacts/bin`                      | Exact remote helper archives and `local/` helper archives; release lane copies without transformation                                                                |
| `/ci/repo/internal/helper/runner/bin/linux-amd64`                    | Existing generated runner gzip for source local-helper compilation; packaged helper bytes remain untouched                                                           |
| `/opt/go`, `/ci/modules`, `/tmp/go-build`, `/home/nocx-proof`        | SDK, integrity-checked extracted public modules plus graph/checksum metadata, fresh writable build cache and private HOME; redundant download ZIPs are not retained  |
| `/ci/lane`                                                           | Root-owned read-only file containing exactly `source` or `packaged`; sole host→guest dispatch input                                                                  |

PID1 provides a clean environment: SDK-first PATH, `GOTOOLCHAIN=local`,
`GOPROXY=off`, `GOSUMDB=off`, `GOMODCACHE=/ci/modules`, `GOCACHE=/tmp/go-build`,
`CGO_ENABLED=1`, `NOCX_SANDBOX_SMOKE_MANDATORY=1`, private HOME and TMPDIR.
Missing compiler/module/generated inputs fail; no network substitute is available.

### AD-13 — One explicit lane and unchanged native consumers

- **Binds:** CI workflow, release workflow, guest command.
- **Prevents:** A rebuilt helper passing in place of the release's one-origin bytes.
- **Rule:** `source` dispatches `go run ./scripts/sandbox-smoke-linux source`,
  then `go run ./scripts/sandbox-smoke-linux packaged`, after host-side
  `helpers-this-machine`; `packaged` dispatches only the latter with downloaded
  release archives. No helper-building Make target runs in the guest. PID1 waits
  for real exits and stops on the first failure. Both retain old-generation,
  ordinary-session, descendant, pathname-socket, TCP, diagnostic and readiness
  assertions. Offline inputs cover both build graphs.

### AD-14 — Guest failure cannot become host success

- **Binds:** Guest PID1, QEMU invocation, workflow result.
- **Prevents:** QEMU's normal shutdown or a console marker masking a failed proof.
- **Rule:** After all required commands exit, root PID1 writes a 32-bit value to
  isa-debug-exit port `0xf4`: `0x10` iff every command succeeded, `0x11` otherwise.
  QEMU uses `iobase=0xf4,iosize=0x04` and encodes `(value << 1) | 1`; the host
  accepts only status 33. Guest failure is 35; ordinary QEMU startup failure 1
  and shutdown 0 are failures, not success. Console markers and cleanup status
  are never authority. Missing result, panic, timeout, unavailable KVM or any
  nonzero proof fails. Preserve contexts/job ceilings; no retries or enlarged limits.

### AD-15 — One reusable Linux VM execution seam

- **Binds:** Product CI and release packaged matrix.
- **Prevents:** Two images or native proof implementations drifting independently.
- **Rule:** Both invoke `scripts/sandbox-linux-vm/run.sh` with an explicit lane.
  Move only these Linux jobs to `ubuntu-24.04`, install its QEMU package and grant
  the invoking CI UID access to `/dev/kvm` only in that disposable hosted job.
  The local script never changes device permissions. Product helper build remains
  outside the VM, as does release's one-origin helper build. macOS continues
  natively. Darwin lint and npm audit remain blockers; VM success cannot claim
  complete release/signing-chain acceptance.

## Stack

| Name                | Version                                                                                                       |
| ------------------- | ------------------------------------------------------------------------------------------------------------- |
| Guest kernel source | Linux 7.2.9; tar.xz SHA-256 b4c5dfbe51a364a6c7f03869200f88c8e1f77403539005f14b7fc6bc91b8d8ba                  |
| Guest userspace     | Ubuntu 24.04                                                                                                  |
| CI QEMU/KVM         | Ubuntu 24.04 distribution QEMU 8.2; local version reported                                                    |
| Image builder       | Invocation-owned docker-container BuildKit `moby/buildkit:v0.33.1`; no shared cache pruning or daemon restart |
| Go and VT toolchain | Existing repository Go 1.26 and Zig 0.16.0 pins                                                               |

## Operational Envelope

Only owned disposable local/CI resources change. Docker builds the kernel and
userspace and preserves filesystem ownership while making the ext4 image; it never
mounts the host root or user HOME. QEMU runs as the invoking host user. Use x86_64
KVM, two vCPUs, 2 GiB guest RAM and one 8 GiB sparse raw disk. Kernel build
concurrency is two. Build resources live in a private directory on the project
filesystem, not shared `/tmp`; check input-size-dependent free disk space before
image creation and fail before filling the host. Stream export into image
construction rather than retaining another rootfs tar copy.

Image compilation owns a uniquely named BuildKit builder and its cache volume,
bounded to two CPUs and 2 GiB RAM without swap; the driver uses its standard
privileged BuildKit worker with only its own state volume, not a privileged
loop-mount disk builder. Remove that builder
and state after image load or on failure. Current/historical consumer package
graphs resolve against a private cache before boot; unrelated UI/tool module
closures and redundant download ZIPs are not staged. Private Git worktree paths
switch from host staging to `/ci/repo` at handoff, without disabling VCS checks.

Kernel/image build has a 12-minute deadline; staging has four minutes; VM execution
has 35 minutes for source and ten for packaged; cleanup has one minute. Existing
15-minute consumer contexts and 60/30-minute job ceilings remain unchanged. The
kernel is built from allnoconfig with explicit native/userspace prerequisites and
no modules. Require every requested fragment value in the resolved configuration,
including the virtio/block/proc-sysctl menu dependencies, before compiling. Extract
no Documentation, unrelated perf/testing tools or arm/arm64/mips/powerpc/riscv
architecture implementation sources for this fixed x86 build; retain all Kconfig
menus because the parser loads other architectures' crypto menus. Use one shared
Ubuntu compiler/userspace package layer, not duplicate concurrent apt installs.
ACPI supplies q35 CPU discovery; cross-memory attach supplies the existing native
observer's process_vm_readv syscall. Preserve exact generated directory destinations
when tracked placeholders already exist; do not nest an artifact below its embed path.
Cold-cache local and hosted
source/packaged execution are acceptance,
not assumed timing. A failed run preserves its failure while cleanup releases
only owned resources. Public downloads precede boot; no guest egress or telemetry.

## Deferred

Application VM support, network isolation as a product feature, kernel upgrades,
self-hosted runner registration, artifact/signing policy changes and Darwin lint
exceptions are outside this CI execution seam.
