# ADR-727g63tr — The far helper is a fact of the connection, and panels reach the host only through it

- **Status:** accepted (2026-11-16) — reviewed by OpenAI `gpt-6.1-sol` in three rounds
  (`nocx-oalo1.6.1`); the last open finding, a panel's retry of a failed install, was settled
  by the owner the same day (decision 2 below)
- **Date:** 2026-11-16
- **Taken from** `nocx-oalo1.6`, the owner's decisions of 2026-11-13 ("the front takes
  connection state and opening entirely from the backend"; Git, Files and Ports work only
  through the helper) and of 2026-11-16 ("availability must be on the connection";
  "raw/script/helper must be set on the host")
- **Supersedes** ADR-0069's place for the answer (the saved connection's `desiredMode`, and a
  hand-typed `ssh`'s choice applying to that session only) and ADR-0068's first paragraph's
  "on the saved connection". Everything else in both stands: the ask is at connect, it chooses
  a method, choosing `helper` records consent by fingerprint, and no feature surface initiates,
  offers or requests an install. **Extends** ADR-akhhb098 and ADR-ba6xet1b. The design is
  section 5.10 of `.internal/specs/2026-11-06-ssh-foundation-design.md`.

## Context

Three panels reach an SSH tab's host, each by its own road (read at `ce81746c9`):

- **Git** goes only through the far helper. Whether one serves the tab is decided lazily,
  inside `git.open` (`helperGitFactory`, `internal/app/helper_git.go:124`), per session, and
  the refusal reaches the renderer as prose that `git-panel.tsx:148` reads with a regex.
- **Files** on an SSH tab always goes over SFTP (`filesystemProviderFactory`, `app.go:2881` →
  `ssh_fsconn.go` → `filesystem/sftp`), with or without a far helper; a local tab takes the
  local provider (`app.go:2883`). The far helper has no file service.
- **Ports** execs `ss`/`netstat` over SSH through the local helper (`ssh.sample-ports`,
  `sshsvc/probeops.go:278`), keyed by profile, so an ad-hoc host gets none. The far helper has
  no ports service.

Each keeps a "lost" state of its own (`ErrFSLost`/`ErrFSDead`, `hostHelper.dead`, discovery
`connDead`, the probes' timeout-as-lost), and the renderer reads the rest from error text
(`nocx/no-error-text-match` entries in `git-panel.tsx` and `ports.tsx`). The mode is read per
open (`effectiveModeFor`, `helper_git.go:564`), so two tabs riding one connection can disagree
about whether it may carry a helper.

## Decision

1. **The mode — `auto`, `raw`, `script`, `helper` — belongs to the host.** It lives in a host
   record keyed by host-key fingerprint, beside consent and a separate fact from it
   (ADR-0105). The connect-time ask (ADR-0069) writes its answer there, for a saved connection
   and a typed `ssh` alike; the connection editor reads and writes the same record; `open`'s
   `desiredMode` and the profile's copy go. Before a host's first fingerprint is known its mode
   is `auto`, which means the connect-time ask, as today. A change of mode applies to the
   host's connections at once: a connection whose mode leaves `helper` stops its far helper
   for panels; a shell already open keeps the integration it opened with until it is
   reopened. No stored profile mode is carried over (greenfield).

2. **Whether a far helper serves a connection is one fact, with one owner, on the
   connection.** A new package, `internal/farhelper`, owns it per `connectionId`. Its lifetime
   is the connection's:
   - **created** when the connection manager reports a connection `connected` for the first
     time; **removed** when the connection ends (`ended`/`failed`), never on a pane's close.
     It holds no lease of its own on the connection, so it never keeps a connection alive;
     a connection carrying only a tunnel keeps its entry while it lives. Removal cancels the
     entry's context, which stops an outstanding probe, install, dial or hello (an install is
     a remote command whose cancellation kills it; an interrupted install is overwritten by
     the next, as the install already tolerates), and closes the far-helper client it owns.
   - **decides once per connection generation and decision revision**, in the background,
     never on a panel's request: mode, recorded consent, platform probe, install, dial, hello
     at the expected install generation. It never asks. **The ask stays where ADR-0069 and
     ADR-0105 put it** — at connect, owned by the open path as `waiting_person(helper-consent)`
     — and `internal/farhelper` reads the recorded answer; a connection with no recorded
     answer (the ask dismissed, a connection a tunnel opened) is `absent(consent-pending)`.
     The **decision revision** advances whenever the host's mode or consent changes; it is
     a second fence beside the connection generation, because a mode change does not replace
     the transport (`hostconn/supervisor.go:850` advances the generation only on a new link).
     A revision change cancels the outstanding work; mode and consent are re-read before any
     remote effect (install, dial) and again before publishing; a result tagged with an older
     generation or revision is dropped.
   - **its own state** is `deciding | serving | absent(reason) | broken(reason)`, separate
     from the transport. `broken` is a far helper that was serving and whose channel or daemon
     failed while the transport is `connected` (`client.Done()`, a failed hello, a generation
     mismatch); it redials with bounded backoff and returns to `serving` only after a hello at
     the expected generation. `absent` is a decision (mode, consent, platform, install failed,
     exec forbidden, version mismatch) and does not change on a transport pause or return.
   - **on a coordinator restart** it is rebuilt for every connection the local helper's
     connection manager reports, with or without panes; it keeps nothing on disk.

   It is published on `pane.connection` for every pane of the connection as a required member
   `helper{state, reason, action}`: `state` is `starting | serving | unavailable | paused`,
   the backend's fold of the transport and the far helper's own state (`paused` when the
   transport is not `connected` and the far helper was or would be `serving`; an `absent` one
   stays `unavailable` through a pause); `reason` is a closed enum — `mode-raw`, `mode-script`,
   `consent-pending`, `consent-denied`, `unsupported-platform`, `install-failed`,
   `exec-forbidden`, `version-mismatch`, `helper-lost`, `connection`; `action` is
   `host-settings | refresh | none`. Per ADR-0068 no action installs or accepts anything:
   `host-settings` opens the host record's mode. `refresh` is offered only for `helper-lost` — a
   far helper that was serving on this connection and is gone — and it only redials and
   re-hellos the helper already installed; it never installs. `install-failed` and every
   other `absent` reason carry no button but `host-settings` or none (owner, 2026-11-16: "if
   the install failed we need not show retry; only if there was a connection and it vanished,
   and it will be refresh rather than retry"). ADR-0068 stands whole: no surface a panel
   draws can start an install. The renderer draws the member
   and derives nothing.

3. **The helper axis says only whether the far helper is reachable. What a service can do is
   the service's.** A panel's method refuses with the axis's reason when the axis is not
   `serving`. When it is, each service answers its own typed outcomes, as today: Ports'
   "no sampling tool on this host" (`discovery.go:50`), Git's operation refusals ("cannot
   amend"), Files' permission errors. Those are not second owners of the connection, and they
   keep typed codes rather than prose.

4. **Git, Files and Ports reach the host only through the far helper.** Each takes the
   connection's helper client from `internal/farhelper`. No panel opens SFTP, execs over SSH
   through the local helper or keeps a lost state. SFTP remains only for the helper's
   install. A local tab keeps its local providers.

5. **The far helper gains two services.**
   - `fs`: `stat`, `list`, `canonical`, `read` (a range; a whole-file read answers
     `version{size, mtime, sha256}` of the bytes it returned; the helper `fstat`s the open
     descriptor before and after reading and, if size or mtime moved, reads again up to three
     times and then refuses `changed-during-read`, the fence `assistant/fileversion.go:85`
     keeps locally),
     and **one write primitive**: a write session addressed by a client-chosen id, writing a
     temporary file in the target's directory, then `commit` with an optional
     `expect{size, mtime, sha256}`. At commit the helper re-reads the target's version and
     renames only if it matches, else refuses `changed-since-read`. Commits through the
     helper are serialized per resolved target path by a lock inside the helper. **The guarantee is
     stated, not implied:** the check and the rename are adjacent in one process on the host,
     so no network round trip lies between them; a writer that does not go through the helper
     can still land between the two system calls, and POSIX offers no compare-and-rename to
     close that, so a file changed while an approval waited is refused and a change in the
     same microseconds is not. That is the guarantee ADR-anntnxdj's approval needs — the
     approved edit never lands on a file someone else changed during the wait — and no more
     is claimed. The target is resolved with symlinks followed once, and the
     rename replaces the resolved path. **A commit never writes twice:** the data
     lives only in the session's temporary file, which the rename consumes, so asking again
     has nothing to re-apply. The outcome is remembered by session id for the helper's
     lifetime; after a helper restart the same question answers `unknown-outcome` with the
     target's current version, which the client compares with the sha256 it wrote. Every
     session is listed in a journal in the helper's state directory before its temporary file
     is created and removed after commit or abort; at start the helper deletes every listed
     file, and one it cannot delete is reported in the helper's inventory, never silently
     left. Upload is the
     same primitive without `expect`. `watch` is a change notification the helper produces by
     polling at the SFTP provider's current cadence.
   - `ports`: one sample of the host's listening sockets. The ladder (`discovery/probes.go:41`)
     and parser (`discovery/parse.go`) move into a new transport-free package,
     `internal/portsprobe`, with execution injected: the far helper runs it locally, and
     nothing in it imports `internal/ssh`
     (`helper/deploy/dependency_test.go:40`).

   `internal/filesystem/helperfs` implements `filesystem.Provider` with `Uploader` and
   `Downloader` over `fs`. The files binding's `Handle` reports the connection's helper axis,
   so the assistant's file tools (`nocx-a62cl`) show the same reason as the panel.

6. **Ports is keyed by the connection, not the profile.** An ad-hoc host has ports.

## Options considered

- **Availability per panel**, each answering on its own `open`, as Git does today. Three
  owners of one fact, which ADR-ba6xet1b forbids. Rejected.
- **A per-pane notification of its own** (`pane.panels`). The fact is the connection's; a
  per-pane copy has nothing to keep the copies equal. Rejected on the owner's word.
- **The registry left in `internal/app`, behind an interface.** The composition root would
  own behaviour, against §5.1.1 and AD-8. Rejected: `internal/farhelper` owns it and
  `internal/app` only wires it.
- **On the connection, owned by `internal/farhelper`, published on `pane.connection`**
  (chosen).

## Consequences

- **On a host whose mode is not `helper`, or whose helper is refused or failed, Files and
  Ports stop working and say why**, where Files works today over SFTP. The owner's decision
  of 2026-11-13, restated because it is the visible cost.
- The mode leaves profiles and opens: 227 sites in 76 non-test files read `desiredMode`
  today. A profile shows its host's mode and changing it there changes it for every tab on
  that host.
- About 3,000 lines leave: the SFTP Files path outside the install, exec port discovery and
  `ssh.sample-ports`, every panel's lost state, the renderer's panel regexes and their
  baseline entries.
- The window protocol moves to version 39.
- New checks: a connection-owner rule refusing a second decider of the helper axis and a panel
  reaching the host by any road but `internal/farhelper` (section 5.10's C23, C24).
