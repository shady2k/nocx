# Skills reconciliation without changing product scope

Task: nocx-q8yjf.22. Owner profile approved 2026-10-09 in the setup conversation.

## Outcome and boundaries

The existing team workflow remains the authority. Reconcile its checks, tracker operations and entry points with setup protocol 0.40.0. Preserve English artifacts, v0-6 and its budget, test-first development, worker-session policy, time-record exemptions, Jev consent and the records-level document evidence decision. No product behavior, production service, credential, dependency or acceptance criterion changes. No bulk queue cleanup. Native submitted results remain recoverable.

The outer workspace contract has been corrected with owner approval to use br/SQLite rather than the retired bd/Dolt store. The owner also approved the previously recorded supporting-change exception: process-only changes owe their tooling checks and independent review, not unrelated product suites.

## Design review: requirements, architecture, implementation and documentation

Apply the existing BMAD separation of requirements (Mary), architecture (Winston), implementation (Amelia), and documentation (Paige) in this bounded design. The local _bmad directory contains configuration only; no BMAD agent implementation is available there. This record does not claim an external BMAD review took place.

Requirements: actual staged commits, introduced push commits and CI revisions must be checked. Existing acceptance_criteria fields are authoritative criteria and must not be lost during normalization. Missing gate inputs fail closed. A tree that never contained the integration may be recognized visibly; a configured tree missing a file may not bypass enforcement.

Architecture: AD-1 through AD-10 and ADR-0011 remain untouched. Process tooling has no access to application secrets. The tracker's shared SQLite store is not branch-local; br resolves the main checkout. Keep historical exports for baselines. The separate-export-ref migration and full document-readiness gate remain their existing tracked work, not silently represented as completed here.

Native readiness reads br's complete JSONL export, resolved by `br where --json`
and refreshed through `sync --flush-only` with auto-import disabled. It changes
only the generated export, never an issue or assignment. Native `list` omits
edges/comments; a complete native `show` over the historical queue exceeded the
smoke deadline and is not the implementation. Nested task groups inherit their
containing stage. A blocked or closed ancestor prevents a forced leaf claim.

Implementation: keep the existing adapter, commit-links entry point, connect command and hooks. Copy all five shipped checks and their format helpers byte-for-byte. Extend existing APIs rather than introduce a parallel version. A contextual ready/claim operation checks the requested stage and checkout, keeps blocks edges, and invokes br's atomic exclusive claim. Same-stage implemented prerequisites require their integrated revision in the checkout and recorded related-check evidence. A prerequisite in an earlier stage of the same feature additionally requires that stage's stored accepted revision in the checkout; other features require closure. Store accepted-stage metadata explicitly as `accepted: <revision> -- <evidence>` comments. Submitted work is never offered for reimplementation.

Push enumeration: peel local tips and tags to commits, subtract every commit reached by refs actually held by the target remote, including the pushed ref's old tip. A ref introducing no commits passes visibly. No stdin ref lines, unreadable objects, incomplete remote enumeration and an unexpectedly empty PR range fail. Reuse the enumeration for commit links and backlog range baseline selection. Do not trigger product suites from pre-push.

Connect: check runtime, all local hook inputs, readable config and a usable tracker before setting hooks. Fail without a partial connection; restore local git config if an import fails. A missing format helper, product checker or adapter refuses with the connect command.

Product documents: pre-commit checks staged content; CI checks the actual PR head or pushed revision. Present documents remain checked before opening the setup PR and on every PR in CI. Full behavioral document readiness stays unwired on nocx-q8yjf.2 and is checked by reading in the meantime.

Documentation: update the existing integration and agent pointer, not a rival command guide. Record preserved choices, proof evidence and limitations on this task. Avoid claiming protected evidence, completed cleanup, successful Jev availability, full product-suite coverage or a landed installation without observing them.

## Acceptance and security evidence

Run the shipped fixture corpora and byte comparisons. Compare normalized status counts and contextual ready leaves with native tracker results. Exercise exclusive claims, release, submitted and implemented transitions, same-stage and accepted-earlier-stage prerequisite claims with edges retained, independent work, cycles and exact task-reference parsing in an isolated scratch tracker. Post run-script claim records, round-trip bytes, damage and void a copied record.

Exercise actual hooks and the connect command in a scratch clone: backlog violation and recovery; linked, unknown and unlinked messages; missing-input rejection; staged invalid product-document rejection; present-document removed-path rejection; empty-ref, existing-commit and new-unlinked-commit push cases on a scratch remote. Never publish proof commits.

Run checks of changed tooling, a bounded deliberate-mutation sample and independent review. Preserve the earlier product-command proofs where inputs are unchanged; disclose unavailable runtime and review fallbacks. Jev consent remains true if its key is unavailable: no project text is sent and no availability proof is claimed.

Publish once through a pull request after applicable checks pass. Merge needs an explicit owner request. Final acceptance requires the main checkout to use the landed files and its connected hooks; a branch and PR are written and proved, not installed.
