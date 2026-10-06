# Security Review — Filesystem Sandbox for Local Shell Sessions

## Overall verdict

The PRD states the approved process-tree, immutable-grant, IPC-boundary, and fail-closed lifecycle contracts without claiming runtime implementation evidence. One material ambiguity was found in the first review: RW did not define the mutation classes represented by consent, leaving implementation and acceptance under-specified. The PRD now names policy-mediated file-content, create/remove, and name-operation classes and explicitly preserves the approved limitations; native cross-root behavior remains an acceptance observation, not a broadened guarantee.

## Findings

- **[medium] RW grant operation set was unspecified (§4.2 FR-4 and Acceptance Summary item 1)** — The original wording “RW permits the specified working filesystem operations” did not define which mutations users were approving, while acceptance checked only write/truncate/unlink. This could yield different authority behind an identical preview. _Disposition:_ Resolved in the PRD: RW now enumerates file-content writes/truncation, file/directory creation/removal, and rename/hard-link/symlink name operations, while explicitly saying chmod/ioctl, pre-opened descriptors, and pre-existing hardlinks are not universally blocked. Cross-root rename/link behavior is a separately recorded backend acceptance outcome.

## Review boundary

This is a static review of the PRD only. It is not an implementation audit, native-runtime test, or evidence that either platform currently enforces these requirements.
