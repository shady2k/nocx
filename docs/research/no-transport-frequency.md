# How often is there no transport? (nocx-u7uh.12)

Research finding for ADR-0024's open question: _"How common the no-transport
environments really are — POSIX `sh` remotes, `AllowTcpForwarding no`,
`docker exec`. A research bead against real hosts, not a guess from an
armchair: if it is common, Tier B moves up the roadmap."_

**Method.** Data at rest only: ADR-0024, the repo's launcher/passport code,
`~/.ssh/config` and `~/.ssh/known_hosts`, the local OpenSSH man pages and
binaries. **No remote host was connected, probed, or otherwise contacted.**
Per-host login shells and forwarding policies were not checked — that is a
finding, not a gap in this work.

## Inventory (this machine, data at rest)

- **`~/.ssh/config`: absent.** 0 user-configured host aliases, 0 `ProxyJump`
  / bastion directives observable anywhere in local SSH config.
- **`~/.ssh/known_hosts`: 7 records observed/accepted** — 1 loopback entry
  plus 6 non-local records (5 private-range IPv4 addresses, 1 DNS name). No
  hashed entries, so the count is exact for what is recorded.
  `known_hosts.old` is a rotated backup: the same identities, no additional
  hosts.
- **Recorded ≠ configured ≠ reachable.** A `known_hosts` record means the
  host's key was accepted at some point; it is not proof of current use,
  login shell, or forwarding policy. Do not read "7 records" as "7 hosts
  nocx talks to today".
- **System `/etc/ssh/ssh_config`:** only the NixOS systemd-ssh-proxy include
  (`.host`, `unix/*`, `vsock/*`, `machine/*` patterns) — machine-local
  plumbing, not user jump hosts. 0 entries relevant to the 6 non-local
  hosts.
- **Bastion note:** 0 `ProxyJump` directives observable; actual routes
  (direct, VPN mesh, or a bastion configured elsewhere) are unknown and were
  not probed. A bastion changes the forwarding question only insofar as the
  far sshd is the one that must permit the `-R` bind.

## Does the repo's tooling already record remote shells?

Partially, and ephemerally:

- The launch carrier and the `ShellAuto` dispatcher detect the remote login
  shell **at launch time** via `$0` (the login shell's own argv[0]): `bash` →
  bash tier, `zsh` → zsh tier, other POSIX (`dash`/`ash`/`busybox sh`/`ksh`)
  → minimal tier, `csh`/`tcsh`/`fish` → plain login shell
  (`internal/shellintegration/launcher_auto.go`).
- The OSC 636 `P` passport names `tier` ∈ {`enhanced`, `blocks`, `minimal`}
  plus `scriptVersion` and `generation`
  (`frontend/src/environment-passport.ts`), and `shell.footprint.status`
  persists per-host installed-generation facts (`generation`,
  `protocolVersion`, `scriptVersion`, `lastObservedAt`) — "last OBSERVED via
  an accepted passport", never a live claim about the host.
- Nothing persists _"this host's login shell is X"_ as a durable profile
  field. The profile `DesiredMode` already reserves `helper` — the Tier-B
  deployed binary, consent-gated — but no current field records a detected
  shell.

So today the tooling can tell you _which bundle/tier a host last ran_, and
nothing about the host's shell or sshd policy when it has not been connected
to.

## The three no-transport cases, locally grounded

1. **POSIX `sh` login shell** (`dash`, `busybox ash`, `mksh`): no
   network-redirection builtin — this is ADR-0024 §2's supported-shell
   contract ("bash with network redirection compiled in is the first
   implementation; zsh needs `zmodload zsh/net/tcp`; POSIX `sh` … needs a
   proven adapter or gets nothing"). Note the scope of the loss: the minimal
   tier **already integrates** these shells today (ENV-file prompt
   integration, verified under dash in the repo's pty tests); under
   ADR-0024 the loss is specifically the _authenticated lifecycle_, not
   prompt rendering.
2. **`AllowTcpForwarding no`**: affects every shell on such a host, including
   bash. Local OpenSSH is 10.4p1 (`sshd -V`; store path `openssh-10.4p1`);
   its `sshd_config(5)` states for `AllowTcpForwarding`: _"yes (the
   default)"_, and adds that disabling it _"does not improve security unless
   users are also denied shell access, as they can always install their own
   forwarders."_ This machine's own `sshd_config` sets no
   `AllowTcpForwarding` → default `yes`. So "usually on" is a documented
   fact of the default; "no" is a deliberate hardening policy
   (CIS-style) or vendor lock-down — unmeasurable from data at rest.
3. **`docker exec`**: the container runs in its own network namespace (its
   `127.0.0.1` is not the host's), and the local descriptor handover
   (`exec.Cmd.ExtraFiles` at `internal/pty/pty_local.go`) cannot reach a pty
   the docker daemon spawned. Conventional terminal until a helper binary.
   Whether `docker exec` is used at all is not recorded anywhere locally.

## Shell network-redirection builtin status

- **bash**: `/dev/tcp` + `/dev/udp` redirections since 2.04; a compile-time
  feature, on in mainstream distro builds. The repo itself verified the
  `exec {fd}<>/dev/tcp/127.0.0.1/<port>` path end to end (ADR-0024 §2).
- **zsh**: no `/dev/tcp` redirection; the standard `zsh/net/tcp` module
  provides the `ztcp` builtin (ADR-0024 §2: "zsh needs `zmodload
zsh/net/tcp`").
- **POSIX `sh`** (`dash`, `busybox ash`, `mksh`): none. (ksh93 — not
  assessed: no local binary, no remote contact.)

## Fraction plausibly affected

**Not estimable.** 0 of the 6 non-local hosts were probed; all 6 are
unknown. The worst case is 6/6 and the best case is 0/6; no point estimate
is offered because priors are not measurements and the sample is one
machine's `known_hosts`. Confidence in _any_ number here would be an
unfounded claim.

What is measured is the cost of being wrong: ADR-0024 §3 makes refusal
detectable synchronously _before_ enhanced mode is offered, and the session
falls back to a conventional terminal. Every case degrades to the same safe
state, so the frequency question decides roadmap priority, not safety.

## Recommendation

**Tier B does not move up on this evidence.** One machine's `known_hosts`
cannot support a roadmap change; the ADR itself ties Tier B's revisit to the
helper (which makes the forwarded-port transport
disposable) and to richer remote metadata (`docs/architecture.md:203`). The
no-transport environments are real but their frequency is unmeasured; the
correct next step is a measurement, not a build. Keep Tier B deferred;
revisit when either (a) a probe of this machine's hosts shows a material
share of `sh`/`ash` remotes or `AllowTcpForwarding no` hosts, or (b) the
helper lands.

## What could not be determined, and what would settle it

- **Login shell of each recorded host.** Settled by one read-only probe:
  `ssh -o BatchMode=yes -o ConnectTimeout=3 <host> 'ps -p $$ -o comm='`
  (or `getent passwd "$USER" | cut -d: -f7`). **Not run** — these are the
  owner's production hosts and probing requires escalation.
- **Per-host `AllowTcpForwarding`.** Settled by `sshd -T | grep -i
allowtcpforwarding` on the host. **Not run**, same reason.
- **`docker exec` usage.** Settled by asking the owner; nothing locally
  records it.
- **Whether the recorded hosts are still in use.** `known_hosts` carries no
  timestamps; only the owner knows.
- **ksh93 presence on the recorded hosts.** Affects how absolute the "POSIX
  sh has no network redirection" blanket is.

Evidence boundary: every conclusion above rests on local data at rest —
ADR-0024, repo source, `~/.ssh` files, and local man pages and binaries. No
remote host was connected, probed, or otherwise contacted; no external
lookups contributed conclusions.
