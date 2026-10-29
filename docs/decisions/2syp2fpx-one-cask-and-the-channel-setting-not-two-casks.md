# ADR-2syp2fpx — One cask and the channel setting, not two casks

- **Status:** Accepted (owner decision, 2026-10-27)
- **Date:** 2026-10-27
- **Supersedes:** [ADR-0097](0097-trunk-based-releases-with-a-nightly-channel.md)'s
  two-cask clause and its "the nightly cask installs the app preset to the nightly update channel" clause. The rest of
  ADR-0097 — trunk-based releases, the nightly channel, the same-bytes promotion — stands unchanged.
- **Related:** [ADR-0052](0052-a-code-identity-without-a-publisher-identity.md),
  [ADR-0086](0086-a-third-party-homebrew-tap-may-clear-quarantine-for-the-unsigned-cask.md),
  ADR-6bs6bk2p (Linux artifacts), tasks `nocx-1ebrs.35.4` and `nocx-1ebrs.66.1.7` (both cancelled by this record)

## Context

ADR-0097 gave the tap two casks — `Casks/nocx.rb` following stable and `Casks/nocx@nightly.rb` following nightly — and
said the nightly cask presets the app's update channel on install. That clause assumed a per-channel install: one
Homebrew package that brings a nightly build, another that brings a stable one, each declaring `conflicts_with` the
other, each carrying its own pinned checksum and updated on its own schedule.

Two things in the milestone it supports contradict that:

- **Promotion publishes the same bytes.** ADR-0097's promotion is a byte-identical promotion: the artefacts a nightly
  verified are the artefacts stable serves. A cask that selects a build by _which cask you installed_ therefore
  implies a build per channel, which the same-bytes promotion forbids.
- **The channel already exists as a setting.** `update.channel` (stable by default, nightly opt-in) selects the
  signed manifest base; the updater on the nightly channel applies the nightly rule, on stable the stable rule. The
  channel is a property of the app's configuration, not of the package that installed it.

## Decision

**One application, one cask, one channel setting.**

- The tap carries **`Casks/nocx.rb`** and nothing else. There is no `nocx@nightly` cask, no `conflicts_with` pair, and
  no install-time preset.
- **Switching channels is the setting.** A person who wants nightlies switches `update.channel` to `nightly` in
  Settings; **the switch checks at once** and installs the latest nightly. This is the same on macOS and on Linux,
  because it is the app's own setting rather than the installer's.
- **Switching back is not a downgrade.** Going nightly → stable stays on the installed build until a **newer stable**
  is published; the stable rule is semver-only, so an older stable is never offered as an "update". The person leaves
  the nightly line at the next stable release, not by reinstalling.
- **Promotion keeps updating the one cask**: the stable cask's version and pinned sha256 move to the promoted
  artefacts, unchanged from ADR-0097's promotion half.

Why this and not the two casks:

- **Same-bytes promotion forbids a per-channel build**, and a per-channel cask implies one.
- **Fewer moving parts**: one cask file, one checksum to keep true, one conflict declaration fewer, and no install-time
  side effect that can silently disagree with the setting the person later chooses.
- **Identical on both platforms**: `update.channel` is the same fact on macOS and Linux; a cask-only preset would make
  the channel story differ by installer.
- **Precedent**: iTerm2 and Sparkle-based apps expose the channel as a setting rather than a second download, and
  Ghostty's channel is a setting too. Nothing in the field needs a second package to express "nightly".

## Consequences

- **`nocx-1ebrs.35.4`** (the mutually exclusive nightly cask) and **`nocx-1ebrs.66.1.7`** (the cask-install channel
  preset seam) are **cancelled by this record**. Neither is deferred: the behaviour they were for is now the setting's
  job, and the preset seam they needed does not exist because nothing needs it.
- The nightly workflow writes **no cask** — it already does not, and that stays a property of the design rather than an
  oversight.
- A person on nightlies is not a separate kind of install: the same app, the same cask, one setting apart. Support
  questions reduce to "which channel is this install on", answerable in Settings and in the About page.
- The risk this record takes is the one ADR-0097's second cask was avoiding: a person who wants to _try_ nightly must
  find the setting and switch it, rather than installing a different package. The About page and the update surface
  must make the channel visible and switchable in one step, or this decision costs discoverability.
