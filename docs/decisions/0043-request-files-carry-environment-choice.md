# ADR-0043 — A request file carries its environment choice

- **Status:** Accepted
- **Date:** 2026-08-27
- **Related:** AD-8 (one owner per behaviour), ADR-0011 (persistence storage
  capabilities and secret references), ADR-0033 (UI state is a document, not a
  setting), `.internal/specs/2026-08-21-api-testing-design.md` §§6.4 and 6.5

## Context

The API workbench lets a person choose which environment supplies variables and
which route carries a request. That answer must survive reopening the request
and restarting the app, while two requests in one collection must be able to
choose different environments.

The existing `internal/uistate` document is the wrong owner. ADR-0033 limits it
to incidental machine-local UI state such as window geometry, sidebar state and
the front tab. An environment choice changes the request's execution context;
it is not a side effect of arranging the UI. The settings registry is also the
wrong owner: a setting is a deliberate application preference, whereas this
choice belongs to one request and must follow that request. A collection-level
document would give both requests the same answer and is explicitly a later T2
scope.

The request file is already the source of truth for the request sent by
`api.request.send`. Keeping the environment choice there gives the choice the
same lifetime and identity as the request, without adding a fourth state store.

## Decision

The request file carries an optional environment path in `environment`. The
backend model represents it as `*string` and the JSON field uses `omitempty`.
The renderer reads and writes that field through the existing request document
path.

This is a **shared document choice, not a personal choice**. A request file is
committed to git, so its selected environment travels to colleagues who open
that file. That is intentional: the request and its execution context remain
reproducible as one reviewable document, and two people do not silently run the
same checked-in request under different persisted answers.

The choice is not a hidden personal default. The workbench shows the selected
environment beside the request, the send path reads the same `environmentFor`
function as the picker, and an explicit `No environment` row is available. A
missing or invalid stored path cannot name an environment outside the request's
collection and falls back to the existing default rule. These are transparency
and validation safeguards, not a confirmation barrier: a valid production
choice committed by one person can still be used by another. Teams must review
the environment field like the URL and method. If that residual risk requires
personal defaults, the format decision must be reopened rather than silently
layering personal state beside the request document.

## Three states

The field has three meaningful states:

- `nil` / an omitted field means nobody has chosen an environment. The existing
  default applies: exactly one environment is selected, otherwise none is.
- A non-nil path names the selected environment within the collection.
- A non-nil empty string means the person explicitly chose `No environment`.

Two states are insufficient. Treating omission and `""` as the same value would
make an explicit choice of no environment revert to the sole environment on a
refresh, or make an unanswered request indistinguishable from a deliberate
choice. The distinction is part of the persisted document, not an in-memory
signal convention.

## Rationale

The request file is the existing owner of the bytes that `api.request.send`
reads. Storing the choice there keeps the selection per request, makes it
available after a restart, and lets the existing file read/write lifecycle
persist it. `internal/uistate` would incorrectly classify execution semantics
as incidental UI state; settings would require a second identity and lifetime
for data already owned by the request; a collection document would violate the
required independence of sibling requests.

## Reversibility

The field is optional and written with `omitempty`, so old request files without
`environment` remain valid and retain the old default behaviour. The persisted
choice can be removed by deleting the field, which returns the request to the
`nil` state. `internal/apicoll/roundtrip_test.go` proves a selected path survives
service recreation, while
`internal/transport/ws_api_request_variables_test.go` proves over the real
socket that an explicit empty choice is sent as `""` and a legacy file omits the
field.

## Consequences

- A request's selected environment is versioned with that request and is
  visible to collaborators through the committed file.
- Sibling requests in one collection can carry independent choices.
- Old files remain readable and use the unchanged one-environment default.
- The renderer must not maintain a second collection- or workspace-level
  selection. The picker and sender continue to share `environmentFor`.
- A future personal-only preference would require a new, explicit format and
  ownership decision; it must not be introduced as an untracked override.
