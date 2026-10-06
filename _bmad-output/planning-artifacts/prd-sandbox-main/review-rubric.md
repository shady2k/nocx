# PRD Quality Review — Filesystem Sandbox for Local Shell Sessions

## Overall verdict

The PRD is a strong product/security contract: immutable launch authority, transactional replacement, exact recovery, truthful diagnostics, explicit exclusions, and native release evidence reinforce one coherent thesis. Review identified two acceptance ambiguities in successive passes; the PRD now explicitly enumerates RW mutation classes and records the approved discovery/collection bounds with acceptance references. Both findings are dispositioned as resolved below. This remains static document review, not implementation or runtime evidence.

## Decision-readiness — strong

§1 “Vision,” §4.1 “Profiles, policy preview, and authority,” and FR-3 state the central decision explicitly: launch authority is immutable and independent of mutable profiles, pane flags, and agent brand. §4.3 “Replacement and recovery lifecycle” exposes the availability trade-off rather than smoothing it away: changing protection replaces the shell, failed preparation preserves the source, and uncertainty never buys availability through an ordinary-shell fallback.

§5 “Non-Goals (Explicit)” and §6.2 “Out of Scope for MVP” name what is given up, including network and broader process containment, remote/Windows enforcement, live grant widening, learning, and automatic retries. §10 “Open Questions” appropriately distinguishes resolved product scope from native feasibility and execution evidence that remain delivery gates. The finding below requests the existing approved limits, not another product-scope decision.

## Substance over theater — strong

The vision is specific to a fixed filesystem grant for an actual local-shell process tree, protected-process reconnection, and explicit replacement. §2.1 “Jobs To Be Done” supplies relevant developer/operator needs. UJ-1–UJ-4 each exercise a consequential launch, recovery, diagnostic, or removal decision through one protagonist rather than inventing a decorative persona inventory. There is no unsupported innovation claim.

The NFRs are product-specific. FR-9 specifies 500 retained inbox records, 200 records per page, and 32 provisional resolutions. §8 “Cross-Cutting NFRs and Constraints” specifies the policy envelope and root-count caps. FR-10 names private data and delivery surfaces, while FR-11 rejects mock or cross-platform substitutes for native evidence. The residual bounds omission identified below does not make these concrete constraints boilerplate.

## Strategic coherence — strong

FR-1–FR-3 separate future defaults from current authority; FR-4–FR-5 establish process-tree and nocx-endpoint boundaries; FR-6–FR-7 preserve authority through replacement/restart; and FR-8–FR-10 expose truthful state without leaking metadata or widening a live grant. FR-11 closes the delivery path. Those features all serve §1 rather than forming a miscellaneous backlog.

§7 “Success Metrics” uses security and lifecycle acceptance rather than activity metrics. SM-1 combines confinement with useful filesystem access and trusted coordinator connectivity; SM-2 requires exact recovery and nonduplicated spawning; SM-3 covers migration, policy rejection, and privacy. SM-C1 explicitly rejects gaming launch-success metrics through downgrade, concealed unknown state, silent root expansion, or exaggerated diagnostic certainty. §6.1 “In Scope” is therefore a coherent platform-capability MVP.

## Done-ness clarity — adequate

All FR-1–FR-11 have testable consequences. FR-6/FR-7 and the §4.3 lifecycle table are particularly precise about commit boundaries, source-close failure, response loss, exact dead inventory, and unknown helper generations. §9 “Acceptance Summary” requires real production/native paths, not mocked RPC echoes or source-text checks. These are delivery gates, not claims of completed evidence.

The latest §4.2 and FR-4 text names file-content writes/truncation, creation/removal, directories, rename, hard-link, symbolic-link, and cross-root cases while preserving metadata/ioctl/pre-opened-FD/pre-existing-hardlink exclusions. §8 and §9 now state the approved bounded native-discovery limits and connect them to acceptance. These are substantially more useful than generic promises of filesystem security; native behavior remains an implementation gate.

### Findings

- **medium — resolved by author update** State bounded-resource acceptance limits (§8 “Cross-Cutting NFRs and Constraints,” Bounds; §9 “Acceptance Summary,” items 3 and 7) — The initial review found an unidentified “approved implementation plan” dependency and no stated bound for the Nix fixture. The PRD now records the approved discovery limits (16,384 entries; ELF strings 4 KiB, 256 tags, 1 MiB/section, 64 MiB total; graph 65,536 nodes/depth 64), Nix fixture (>256 package roots), bounded platform diagnostic collection, and references those constraints in acceptance items 3 and 7. The fixed policy and inbox/page/provisional caps remain explicit. _Disposition:_ The PRD now gives the reviewer-visible bounds available from the approved requirements; no numeric tracee/log byte cap was specified by that source, so none was invented. Runtime conformance is unverified.

## Scope honesty — strong

§2.2 “Non-Users (v1),” §5 “Non-Goals (Explicit),” and §6.2 “Out of Scope for MVP” make the principal omissions explicit. The PRD does not conflate a filesystem sandbox with a network sandbox, whole-account isolation, protection against arbitrary same-user host processes, or retroactive control of detached processes. The detailed exclusions constrain the broad vision rather than leaving implementation to disclose limitations later.

There are no open product-scope questions and zero NOTE FOR PM callouts. §11 “Assumptions Index” explicitly records that no user-detail assumptions were added. §10 now records one bounded-collection numeric-budget question as an architecture gate because the approved plan requires bounded collectors without specifying a per-event byte/work cap. The resource-bound finding is otherwise resolved: all numeric discovery and retained-inbox limits present in the source are recorded; no substitute limits were invented.

## Downstream usability — adequate

§0 “Document Purpose” names UX, architecture, implementation planning, and verification as consumers. Grouped FRs and stable FR/UJ/SM identifiers support extraction. §3 “Glossary” clearly separates mutable Profile, Effective policy, per-process Launch, immutable Grant, Preparation, Replacement, Unknown, and Diagnostic attempt. All four journeys name Alex and connect to the local-developer/operator jobs without requiring a separate persona artifact.

The §4.3 lifecycle table supplies consumer-facing outcomes for candidate readiness, durable commit, source retirement, exact inventory, and uncertain helper state. FR-8 follows shield and exact `/sandbox` actions through to Settings, draft preservation, and exclusion from PTY/history/ledger submission. These are specified boundary outcomes only; this review does not claim that runtime routing implements them.

No unresolved source-extraction limitation remains in the acceptance contract. The approved numeric bounds and required diagnostic collection characteristics are now recorded in §8 and referenced by §9. Implementation-specific mappings can remain downstream; a new traceability matrix is unnecessary.

## Shape fit — strong

A capability-spec shape fits this brownfield security rewrite with meaningful but bounded desktop interactions. Four short journeys justify their space through explicit consent, reconnection, future-policy suggestions, and removal without overwhelming the FRs. A lifecycle table, exclusions, constraints, and production-path acceptance are proportionate for a chain-top document feeding UX, architecture, and verification.

§4.5 “Data integrity, privacy, and delivery,” FR-10, and FR-11 address brownfield concerns: preserving workspace/pane/session/ledger/execution-grant state, refusing malformed or newer databases safely, and shipping matching helper/runner artifacts through existing delivery paths. There are no concrete source-file references to validate. Deferring internal architecture while retaining these observable requirements is appropriate; no repository implementation or packaging correctness is inferred.

## Mechanical notes

- **Review scope:** Read the complete rubric, rubric-walker instructions, and full PRD. The PRD changed during review; the full updated document was reread before producing this report. No addendum was present in the workspace. This reviewer did not edit the PRD or run builds, lint, tests, formatting, or native/runtime verification.
- **ID continuity:** FR-1–FR-11, UJ-1–UJ-4, and SM-1–SM-3 are unique and contiguous. SM-C1 is an explicitly labeled counter-metric, not a numbering gap. The FR references attached to SM-1–SM-3 resolve. Acceptance items 1–8 are contiguous.
- **Glossary drift:** No meaning-changing drift among Profile, Grant, Launch, Preparation, Replacement, Unknown, and Diagnostic attempt was found. RO/RW are expanded as read-only/read-write in the surrounding narrative. Apply/relaunch/Remove are defined together under Replacement.
- **Diagnostic wording:** UJ-3 is titled “after a denied operation,” but its body says observed attempt and source/precision. Read with the Diagnostic attempt definition and FR-9, the title describes the user trigger; it does not authorize presenting a prediction as proven kernel denial.
- **Cross-references:** “Approved main-rewrite plan” in §0 and “approved implementation plan” in §8 are not resolvable document references. The acceptance-impacting consequence is reported above rather than duplicated as another finding. No broken FR/UJ/SM references were found.
- **Assumptions Index roundtrip:** There are no inline ASSUMPTION tags and no indexed assumption entries; §11 states that no user-detail assumptions were added. There are no unindexed NOTE FOR PM callouts. §10 records one implementation-detail question about numeric observer read/work caps; native gates are not claimed passed.
- **UJ protagonists:** UJ-1–UJ-4 each name Alex; local-shell/operator context is carried by their actions and §2.1. No floating unnamed journeys.
- **Required sections:** Vision, jobs/users, journeys, glossary, grouped FRs, non-goals, MVP scope, success/counter-metrics, cross-cutting NFRs, acceptance, open questions, and assumptions are present. Draft frontmatter does not claim final approval or completed implementation evidence.

## Author update after review

After this review, the PRD was updated to state the architecture-owned per-event diagnostic read/work budget as an explicit open question. This preserves the source requirement for bounded collection without fabricating a numeric cap absent from the approved plan. The reviewer did not independently re-review that final edit.
