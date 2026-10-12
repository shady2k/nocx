# ADR-0083: The frontend test runner retires the vulnerable worker pool

Date: 2026-10-12

Status: Accepted for this implementation under the owner's dependency-security scope decision (`nocx-l3pk`); PR acceptance and merge remain pending.

Supersedes: none.

## Context

The required dependency audit recorded in `nocx-l3pk` found high and critical
advisories in the root and frontend npm trees, including a vulnerable Vitest 3
worker-pool/mocker path. Updates within the existing Vitest 3 range did not remove
that path. The owner authorized the narrowly scoped security repair, including a
necessary Vitest major upgrade, before PR review; this record does not claim the PR
or its implementation has been accepted.

## Decision

Use Vitest `^4.1.11` and regenerate the root and frontend lockfiles with npm's
supported, non-force resolution. Do not add dependency overrides or hand-edit
resolution or integrity data. Keep production dependency majors and root direct
dependency declarations unchanged.

Vitest 4.1.11 declares Node.js `^20.0.0 || ^22.0.0 || >=24.0.0` and accepts Vite
`^6 || ^7 || ^8` as a peer, so the project's Vite 7 toolchain is within its declared
support range. The upstream migration guide specifies that mocked constructors must
use a constructible regular function or class rather than an arrow implementation.
Use those functions where the renderer mock is instantiated with `new`, and use
callable `Mock<Port['method']>` signatures for test fixtures instead of overloaded
`ReturnType<typeof vi.fn>` types. Preserve behavior assertions and give typed
fixtures complete required wire fields; do not weaken tests to accommodate the
upgrade.

Vitest 4 is the previous major under the upstream release policy, which receives
security and important fixes, with the supported migration documented by Vitest.
This is a supported security cutover, not a claim that a chosen major is supported
indefinitely.

## Why not the obvious alternative

`npm audit fix --force` offered Vitest 5.0.3 in the earlier investigation. That
would take an additional major and unrelated migration changes without being
necessary for this remediation. Remaining on Vitest 3 retained the vulnerable
worker-pool/mocker path. Suppressing advisories, forcing a resolution, or weakening
behavior tests does not repair the dependency graph.

## Evidence and what the next person inherits

Official references:

- [Vitest release policy](https://vitest.dev/releases)
- [Vitest security policy](https://github.com/vitest-dev/vitest/security/policy)
- [Vitest 4 migration guide](https://v4.vitest.dev/guide/migration)
- [Vitest 4.1.11 registry metadata](https://registry.npmjs.org/vitest/4.1.11)

Context7 was attempted through the `context7-mcp` skill, but no Context7 MCP
resource/tool is mounted in this session. The official Vitest sources and npm
registry metadata above were consulted directly instead of asserting unverified
support. Implementation audits, typechecks, suites, and real-browser checks on
this independent branch remain pending until its final checks; no prior mixed-tree
smoke is evidence for this branch. Their actual outcomes belong in the PR record,
not in this decision rationale.

No storage, credential, helper, PTY, or wire ownership changes are introduced by
the decision. AD-1, AD-6, AD-7, AD-8 and ADR-0011 remain binding. Recheck the
upstream maintenance window on the next security upgrade; do not retain this major
after its support ends merely because this record chose it once.
