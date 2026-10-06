---
name: nocx Sandbox
status: final
updated: 2026-10-05
---

# nocx Sandbox — Design Spine

## Brand & Style

The Sandbox surface is part of nocx, not a new visual product. It communicates consequential filesystem policy with the same restrained, practical language as existing Settings and terminal controls. Security state must be explicit in words and never inferred from color, icon, a saved profile, or an enabled feature toggle.

## Colors

No new color tokens. Inherit the active nocx theme and existing semantic tokens. State is always labeled (`Off`, `Enforce`, `Unknown`, `Unavailable`); color and shield glyphs are supplementary, not the meaning.

## Typography

Inherit the UI kit typography. Use existing heading, body, caption, and monospace conventions; canonical filesystem roots and grant identifiers use the established path/code treatment without truncation hiding the full value from assistive technology.

## Layout & Spacing

Inherit the singleton Settings page layout, grouped rail, page sections, responsive behavior, and existing spacing. The Sandbox page is one Settings destination with a readable single-column flow. Do not add a second visual system, a full-window security dashboard, or a separate Statistics destination.

## Elevation & Depth

Inherit existing surfaces and dialog treatment. Preview and confirmation use the existing `Dialog` pattern and are never nested. No custom modal primitive or bespoke security panel chrome.

## Shapes

Inherit UI kit component shapes and active theme tokens.

## Components

Use existing UI kit components and behavioral patterns: `Dialog`, `RecordRow`, `CollectionView`, `StatusCard`, `EditableRowList`, `IconButton`, `Select`, section primitives, and toast API where applicable. No raw browser `alert`/`confirm`/`prompt`, no bespoke modal, no direct non-token state colors. The activity-bar shield is an icon-only contextual action with an accessible state-bearing name and tooltip; it is not a fake navigation view.

## Do's and Don'ts

| Do                                                                                           | Don't                                                                                |
| -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| Name the active pane, workspace/profile source, current launch mode, and limitations in text | Treat a profile setting or green icon as evidence that the running shell is enforced |
| Show the effective roots and what will happen before the explicit launch confirmation        | Hide baseline roots or imply that the editable list is the entire effective policy   |
| Reuse nocx UI kit and theme                                                                  | Add another design system, bespoke modal, Learn mode, or Statistics page             |
| Keep diagnostics explicitly qualified by source and precision                                | Call observed attempts a complete audit or proof that the kernel denied an operation |
