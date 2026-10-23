# ADR-0102 — repowise is removed

- **Status:** Accepted
- **Date:** 2026-10-23
- **Supersedes:** ADR-0056 — repowise is kept for history, not for search
- **Beads:** `nocx-1ebrs.72`

## Context

ADR-0056 retained repowise for its git-history risk and co-change analysis, while
ranking `grep` as the source search tool. That decision depended on a locally
installed MCP service, a per-clone generated index, and machine-level hooks. The
repository no longer needs to install or direct contributors to that integration.
Its active configuration and setup guidance create ongoing obligations even when
contributors do not use it, and a locally generated index is not a source of truth
for code structure.

The useful distinctions behind ADR-0056 remain: direct source inspection answers
what exists and who calls it; history and risk questions require examining git
history. Those are ordinary engineering questions and do not require this specific
integration to be part of the repository contract.

## Decision

Remove repowise from active repository documentation and configuration. Use
`grep`, `glob`, direct source reading and git history for repository investigation.
Do not install an MCP server, hooks or generated local index as part of the project.
This repository decision does not remove personal, machine-level setup; that is
outside this ADR and must be handled by each machine's owner.

ADR-0056 remains unchanged as a historical record. This ADR supersedes its decision
to keep repowise active, not its measurement record or the broader rule to verify
claims against source.

## Why this rather than the obvious alternative

Keeping the integration solely for its risk view leaves setup, configuration and
index maintenance in the project contract for a capability that is not required
for normal work. Replacing it with another generated index would repeat the same
maintenance cost without an accepted need. Direct inspection preserves the clear
source-of-truth boundary and avoids making local tooling a prerequisite.

## What the next person inherits

The live repository docs and config no longer prescribe repowise. The historical
measurements and the prior decision remain readable in ADR-0056 and its cited
records. Machine-level hooks, registrations and indexes are not cleaned up by this
change.
