package transport

// workspaces.* / tabs.* / panes.* — the layout chain on the wire (nocx-isoph.2,
// design .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §4.1, §4.4,
// §7).
//
// WHAT THIS IS. §4.1 arriving: an invariant whose two ends live in two
// processes has no owner, so the objects move rather than a protocol being
// written. The backend owns the workspace, the tab and the pane; the frontend
// ASKS it to create, decorate, reorder, move and destroy them, and renders
// what it is told. Every result shape is declared once in contracts/ and the
// renderer's types are generated from there.
//
// THE ID IS UNTRUSTED INPUT (§7), and that has exactly three consequences
// here, none of them optional:
//
//  1. THE SHAPE IS VALIDATED, NEVER BELIEVED. Every id is checked against the
//     UUIDv7 shape by the registered params validator, which runs BEFORE the
//     handler and therefore before anything can be written.
//  2. AN INSERT ON AN EXISTING ID FAILS. It never overwrites. A repeat of the
//     SAME request returns the SAME object, which is the store's business and
//     is documented at length in internal/content/layout_sqlite.go.
//  3. KNOWING AN ID CONFERS NO RIGHT TO USE IT. A UUIDv7 embeds a timestamp
//     and is guessable by construction, so nothing in this file treats
//     possession of an id as evidence of anything — there is no ownership
//     check here to be fooled, and none may be added on that basis. What
//     bounds these methods is the connection's own admission to the socket,
//     exactly as it bounds every other control-plane method.
//
// WHAT THIS DELIBERATELY DOES NOT DO. It adds no notification, and therefore
// NO TAB ADDRESS (§4.4). The backend gains a tab ROW; every backend→renderer
// address remains a sessionId the renderer resolves, because a tab holds
// several panes and "the tab that spoke" is not well defined. tabId appears
// on this wire only as a field of a pane the renderer asked for.
//
// CREATION IS ALWAYS CREATION-WITH-CONTENT, and the params say so
// (nocx-isoph.3, §4.1): workspaces.create carries the first tab and that tab's
// first pane, tabs.create carries its first pane. There is never a moment at
// which an empty container exists, which is what makes "a container exists
// only while it holds a member" a property of the model rather than a sweep.
// The store enforces it — ErrNoFirstTab, ErrNoFirstPane — and this file does
// not re-implement the rule, it only carries the fields.
//
// A CLOSE CARRIES THE REPLACEMENT for the same §7 reason the creates carry
// their ids: when the close would leave the application with no tab at all,
// one is minted, and its tab and pane ids are DURABLE, so they are the
// frontend's to mint and never the backend's. A close that would empty the
// application and named no replacement is refused whole and removes nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lineage"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/workspace"
)

// ── wire shapes ───────────────────────────────────────────────────────────

// The three objects, as the renderer sees them. Hand-written on this side and
// validated against contracts/*.schema.json by the DTO and over-the-wire
// tests (contracts/README.md): the schema is the declaration, and these
// structs are what has to satisfy it.
//
// Every nullable field is a POINTER with no omitempty, because absent and
// null are different answers and the schema requires the key: a tab with no
// name is a tab whose label is derived from its panes, and a missing key
// would make that indistinguishable from an older backend.
type workspaceWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Colour is null for a workspace nobody coloured: the default, and any row
	// the backend minted for a session nobody recorded. It is the user's
	// choice from a closed palette the RENDERER owns — the store keeps a
	// string and judges none of it, so a value a newer renderer understands is
	// carried rather than refused.
	Colour   *string `json:"colour"`
	Position int     `json:"position"`
}

type tabWire struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspaceId"`
	ParentID    *string `json:"parentId"`
	Name        *string `json:"name"`
	Colour      *string `json:"colour"`
	Position    int     `json:"position"`
	Pinned      bool    `json:"pinned"`
	Layout      string  `json:"layout"`
	SeenAt      *int64  `json:"seenAt"`
}

type paneWire struct {
	ID             string  `json:"id"`
	TabID          string  `json:"tabId"`
	Cwd            string  `json:"cwd"`
	Kind           string  `json:"kind"`
	Endpoint       *string `json:"endpoint"`
	SizeShare      float64 `json:"sizeShare"`
	SandboxGranted bool    `json:"sandboxGranted"`
}

func wireWorkspace(ws content.Workspace) workspaceWire {
	return workspaceWire{ID: ws.ID, Name: ws.Name, Colour: ws.Colour, Position: ws.Position}
}

func wireTab(t content.Tab) tabWire {
	return tabWire{
		ID: t.ID, WorkspaceID: t.WorkspaceID, ParentID: t.ParentID, Name: t.Name,
		Colour: t.Colour, Position: t.Position, Pinned: t.Pinned,
		Layout: string(t.Layout), SeenAt: t.SeenAt,
	}
}

func wirePane(p content.Pane) paneWire {
	return paneWire{
		ID: p.ID, TabID: p.TabID, Cwd: p.Cwd, Kind: string(p.Kind),
		Endpoint: p.Endpoint, SizeShare: p.SizeShare,
	}
}

// wireWorkspaces, wireTabs and wirePanes force the slice to be non-nil: an
// empty collection must marshal as [] and never null — the renderer's first
// .map assumes it, and a null there is the nocx-25k9.14 class of defect.
func wireWorkspaces(all []content.Workspace) []workspaceWire {
	out := make([]workspaceWire, 0, len(all))
	for _, ws := range all {
		out = append(out, wireWorkspace(ws))
	}
	return out
}

func wireTabs(all []content.Tab) []tabWire {
	out := make([]tabWire, 0, len(all))
	for _, t := range all {
		out = append(out, wireTab(t))
	}
	return out
}

func wirePanes(all []content.Pane, granted map[string]struct{}) []paneWire {
	out := make([]paneWire, 0, len(all))
	for _, p := range all {
		w := wirePane(p)
		_, w.SandboxGranted = granted[p.ID]
		out = append(out, w)
	}
	return out
}

// workspaceCreateResponse answers with ALL THREE rows the call made: a
// creation with content has no answer that names only its container, and the
// renderer needs the tab and the pane as stored — with the container
// references the store filled in — to draw anything at all.
type workspaceCreateResponse struct {
	Workspace workspaceWire `json:"workspace"`
	FirstTab  tabWire       `json:"firstTab"`
	FirstPane paneWire      `json:"firstPane"`
	Replayed  bool          `json:"replayed"`
}

type workspaceResponse struct {
	Workspace workspaceWire `json:"workspace"`
}

type workspaceListResponse struct {
	Workspaces []workspaceWire `json:"workspaces"`
}

type tabCreateResponse struct {
	Tab       tabWire  `json:"tab"`
	FirstPane paneWire `json:"firstPane"`
	Replayed  bool     `json:"replayed"`
}

type tabResponse struct {
	Tab tabWire `json:"tab"`
}

type tabListResponse struct {
	Tabs []tabWire `json:"tabs"`
}

type paneCreateResponse struct {
	Pane     paneWire `json:"pane"`
	Replayed bool     `json:"replayed"`
}

type paneResponse struct {
	Pane paneWire `json:"pane"`
}

// layoutReadResponse is the whole chain, and the answer to the one question
// nocx-isoph.2 left the renderer unable to ask (nocx-isoph.4): what is there.
// Every other method here changes one thing and answers about that thing; a
// renderer that could only do that would have to remember the rest, and what
// it remembers it owns — which is the invariant §4.1 moved into this process
// to give an owner.
//
// The three collections are FLAT and the tabs are not scoped to a workspace,
// unlike tabs.reorder's: a reorder is an operation on one strip, while this
// is the picture the renderer draws itself from, and a renderer that had to
// ask per workspace would be deciding which ones to ask about.
type layoutReadResponse struct {
	DefaultWorkspaceID string          `json:"defaultWorkspaceId"`
	Workspaces         []workspaceWire `json:"workspaces"`
	Tabs               []tabWire       `json:"tabs"`
	Panes              []paneWire      `json:"panes"`
}

// closedResponse is the answer to a close. The id and nothing else: there is
// no object left to describe, and a copy of the row as it was would be a fact
// about a thing that no longer exists.
type closedResponse struct {
	ID string `json:"id"`
}

// ── params ────────────────────────────────────────────────────────────────

type workspaceCreateParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Colour is chosen in the same dialog as the name (nocx-2mipw) and
	// travels with the create rather than following it as a second call: a
	// workspace that existed uncoloured for one round trip would flash the
	// neutral pill and then repaint, and a create whose second half failed
	// would leave a workspace the user thinks they coloured.
	Colour   *string `json:"colour"`
	Position int     `json:"position"`
	// FirstTab and FirstPane are what make this creation-with-content. They
	// carry no container reference of their own: this call is what creates
	// the containers, so naming one would be asking the caller to repeat what
	// it is already saying (the store refuses a mismatch rather than
	// silently re-parenting).
	FirstTab  firstTabParams  `json:"firstTab"`
	FirstPane firstPaneParams `json:"firstPane"`
}

// firstTabParams is a tab minus its workspace: the decoration a tab may be
// born with, and nothing that names where it lives.
type firstTabParams struct {
	ID       string  `json:"id"`
	Name     *string `json:"name"`
	Colour   *string `json:"colour"`
	Position int     `json:"position"`
	Pinned   bool    `json:"pinned"`
	Layout   string  `json:"layout"`
}

// firstPaneParams is a pane minus its tab.
type firstPaneParams struct {
	ID        string  `json:"id"`
	Cwd       string  `json:"cwd"`
	Kind      string  `json:"kind"`
	Endpoint  *string `json:"endpoint"`
	SizeShare float64 `json:"sizeShare"`
}

func (p firstTabParams) tab(workspaceID string, parentID *string) content.Tab {
	return content.Tab{
		ID: p.ID, WorkspaceID: workspaceID, ParentID: parentID, Name: p.Name,
		Colour: p.Colour, Position: p.Position, Pinned: p.Pinned,
		Layout: content.TabLayout(p.Layout),
	}
}

func (p firstPaneParams) pane(tabID string) content.Pane {
	return content.Pane{
		ID: p.ID, TabID: tabID, Cwd: p.Cwd, Kind: content.PaneKind(p.Kind),
		Endpoint: p.Endpoint, SizeShare: p.SizeShare,
	}
}

type workspaceRenameParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// workspaceRecolourParams shapes exactly like tabRecolourParams, because it
// is the same act one rung up: null is "clear the colour", which is an
// operation a person can ask for and not an omitted field.
type workspaceRecolourParams struct {
	ID     string  `json:"id"`
	Colour *string `json:"colour"`
}

type workspaceReorderParams struct {
	IDs []string `json:"ids"`
}

// layoutCloseParams closes a container. The replacement is consulted ONLY
// when the close would otherwise leave the application with no tab at all, so
// a caller that always sends one is not asking for a tab it will not get —
// and a caller that never does will one day be refused with nothing removed.
type layoutCloseParams struct {
	ID          string             `json:"id"`
	Replacement *replacementParams `json:"replacement"`
}

type replacementParams struct {
	TabID  string `json:"tabId"`
	PaneID string `json:"paneId"`
	Cwd    string `json:"cwd"`
}

func (p layoutCloseParams) replacement() content.Replacement {
	if p.Replacement == nil {
		return content.Replacement{}
	}
	return content.Replacement{TabID: p.Replacement.TabID, PaneID: p.Replacement.PaneID, Cwd: p.Replacement.Cwd}
}

type tabCreateParams struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspaceId"`
	ParentID    *string         `json:"parentId"`
	Name        *string         `json:"name"`
	Colour      *string         `json:"colour"`
	Position    int             `json:"position"`
	Pinned      bool            `json:"pinned"`
	Layout      string          `json:"layout"`
	FirstPane   firstPaneParams `json:"firstPane"`
}

// tabRenameParams and tabRecolourParams take a POINTER, and null is the
// operation rather than the absence of one: clearing a tab's name puts it
// back to the label derived from its panes (§4.5).
type tabRenameParams struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

type tabRecolourParams struct {
	ID     string  `json:"id"`
	Colour *string `json:"colour"`
}

type tabPinParams struct {
	ID     string `json:"id"`
	Pinned bool   `json:"pinned"`
}

type tabReorderParams struct {
	WorkspaceID string   `json:"workspaceId"`
	IDs         []string `json:"ids"`
}

type paneCreateParams struct {
	ID        string  `json:"id"`
	TabID     string  `json:"tabId"`
	Cwd       string  `json:"cwd"`
	Kind      string  `json:"kind"`
	Endpoint  *string `json:"endpoint"`
	SizeShare float64 `json:"sizeShare"`
}

type paneMoveParams struct {
	ID    string `json:"id"`
	TabID string `json:"tabId"`
}

// paneCwdParams is panes.setCwd: where the pane's shell IS, reported by the
// renderer when the shell says so and not before (AD-5 — a verified OSC 7,
// never the provider's session-open fallback).
type paneCwdParams struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

// ── ingress bounds ────────────────────────────────────────────────────────

const (
	// maxLayoutColourRunes bounds a colour. It is a token like "#ff8800" or a
	// theme key, never a document.
	maxLayoutColourRunes = 64
	// maxLayoutEndpointRunes bounds a canonical user@host:port.
	maxLayoutEndpointRunes = 512
	// maxLayoutMembers bounds a reorder. A strip a person can see is orders
	// of magnitude under this; the bound exists so one frame cannot make the
	// server hold an unbounded list, not to express a product limit.
	maxLayoutMembers = 1_024
	// maxLayoutPosition bounds a position. Negative is meaningless — there is
	// nothing before the first slot — and the ceiling keeps a position from
	// being used to carry a payload.
	maxLayoutPosition = 1 << 20
)

// ── the id is a shape, and the shape is checked ───────────────────────────

// validLayoutID reports whether s is a canonical UUIDv7: 8-4-4-4-12 lower or
// upper case hex, version nibble 7, RFC 4122 variant (the first nibble of the
// fourth group in 8, 9, a, b).
//
// The VERSION is checked, not merely the shape, because §7's table says
// UUIDv7 for all three objects and a schema that says v7 while accepting a v4
// is a schema advertising what it does not deliver. What is NOT read is the
// timestamp inside it: it is guessable by construction, so it is evidence of
// nothing and no decision here may rest on it.
func validLayoutID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHexDigit(c) {
				return false
			}
		}
	}
	if s[14] != '7' {
		return false
	}
	switch s[19] {
	case '8', '9', 'a', 'b', 'A', 'B':
		return true
	}
	return false
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// layoutID validates one client-minted id and names the field in the refusal.
func layoutID(field, id string) string {
	if strings.TrimSpace(id) == "" {
		return field + " is required"
	}
	if !validLayoutID(id) {
		return field + " must be a UUIDv7"
	}
	return ""
}

// workspaceRef validates a REFERENCE to a workspace, which is the one place
// an id here may not be a UUIDv7: the default workspace is minted by the
// backend under a name of its own (internal/workspace.Default), never renders
// and is never created through these methods. One owner of that constant, and
// this reads it rather than restating it.
func workspaceRef(field, id string) string {
	if id == string(workspace.Default) {
		return ""
	}
	return layoutID(field, id)
}

func layoutPosition(position int) string {
	if position < 0 || position > maxLayoutPosition {
		return fmt.Sprintf("position must be between 0 and %d", maxLayoutPosition)
	}
	return ""
}

func layoutMemberIDs(ids []string) string {
	if len(ids) == 0 {
		return "ids must name at least one member"
	}
	if len(ids) > maxLayoutMembers {
		return fmt.Sprintf("ids exceeds %d members", maxLayoutMembers)
	}
	for _, id := range ids {
		if msg := layoutID("ids", id); msg != "" {
			return msg
		}
	}
	return ""
}

// nullableBounded checks an optional string that may legitimately be null.
func nullableBounded(field string, v *string, bound int) string {
	if v == nil {
		return ""
	}
	return boundedRunes(field, *v, bound)
}

// ── validators ────────────────────────────────────────────────────────────

func validateWorkspaceCreateRaw(raw json.RawMessage) string {
	var p workspaceCreateParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.Name) == "" {
		// A workspace is always created deliberately, so it always has a
		// name — unlike a tab, which is minted by a drag nobody named.
		return "name is required"
	}
	if msg := boundedRunes("name", p.Name, maxConfigNameRunes); msg != "" {
		return msg
	}
	if msg := nullableBounded("colour", p.Colour, maxLayoutColourRunes); msg != "" {
		return msg
	}
	if msg := layoutPosition(p.Position); msg != "" {
		return msg
	}
	if msg := validateFirstTab(p.FirstTab); msg != "" {
		return msg
	}
	return validateFirstPane(p.FirstPane)
}

// validateFirstTab and validateFirstPane check the members a create carries.
// They bound and shape-check exactly what the standalone create does; whether
// a member is REQUIRED at all is the store's rule (ErrNoFirstTab,
// ErrNoFirstPane) and is not restated here, because a second statement of one
// rule is a second thing to keep in step.
func validateFirstTab(p firstTabParams) string {
	if msg := layoutID("firstTab.id", p.ID); msg != "" {
		return msg
	}
	if msg := nullableBounded("firstTab.name", p.Name, maxConfigNameRunes); msg != "" {
		return msg
	}
	if msg := nullableBounded("firstTab.colour", p.Colour, maxLayoutColourRunes); msg != "" {
		return msg
	}
	if msg := layoutPosition(p.Position); msg != "" {
		return msg
	}
	switch content.TabLayout(p.Layout) {
	case content.LayoutRow, content.LayoutColumn:
		return ""
	default:
		return "firstTab.layout must be one of row, column"
	}
}

func validateFirstPane(p firstPaneParams) string {
	if msg := layoutID("firstPane.id", p.ID); msg != "" {
		return msg
	}
	return validatePaneFacts("firstPane", p.Cwd, p.Kind, p.Endpoint, p.SizeShare)
}

func validateWorkspaceRenameRaw(raw json.RawMessage) string {
	var p workspaceRenameParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.Name) == "" {
		return "name is required"
	}
	return boundedRunes("name", p.Name, maxConfigNameRunes)
}

func validateWorkspaceRecolourRaw(raw json.RawMessage) string {
	var p workspaceRecolourParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	return nullableBounded("colour", p.Colour, maxLayoutColourRunes)
}

func validateWorkspaceReorderRaw(raw json.RawMessage) string {
	var p workspaceReorderParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return layoutMemberIDs(p.IDs)
}

func validateLayoutCloseRaw(raw json.RawMessage) string {
	var p layoutCloseParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	if p.Replacement == nil {
		return ""
	}
	// A replacement that IS sent is shape-checked like anything else: its two
	// ids are durable and client-minted, so they are untrusted in exactly the
	// same way (§7).
	if msg := layoutID("replacement.tabId", p.Replacement.TabID); msg != "" {
		return msg
	}
	if msg := layoutID("replacement.paneId", p.Replacement.PaneID); msg != "" {
		return msg
	}
	// An empty cwd is legal and means the backend process's own directory,
	// which is what an unconfigured local shell already inherits.
	return boundedRunes("replacement.cwd", p.Replacement.Cwd, maxCwdRunes)
}

func validateTabCreateRaw(raw json.RawMessage) string {
	var p tabCreateParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	if msg := workspaceRef("workspaceId", p.WorkspaceID); msg != "" {
		return msg
	}
	if p.ParentID != nil {
		if msg := layoutID("parentId", *p.ParentID); msg != "" {
			return msg
		}
	}
	if msg := nullableBounded("name", p.Name, maxConfigNameRunes); msg != "" {
		return msg
	}
	if msg := nullableBounded("colour", p.Colour, maxLayoutColourRunes); msg != "" {
		return msg
	}
	if msg := layoutPosition(p.Position); msg != "" {
		return msg
	}
	switch content.TabLayout(p.Layout) {
	case content.LayoutRow, content.LayoutColumn:
	default:
		// The set is closed and stays closed: panes do not nest, so an
		// asymmetric layout is not expressible and is not meant to be (§5).
		return "layout must be one of row, column"
	}
	return validateFirstPane(p.FirstPane)
}

func validateTabRenameRaw(raw json.RawMessage) string {
	var p tabRenameParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	return nullableBounded("name", p.Name, maxConfigNameRunes)
}

func validateTabRecolourRaw(raw json.RawMessage) string {
	var p tabRecolourParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	return nullableBounded("colour", p.Colour, maxLayoutColourRunes)
}

func validateTabPinRaw(raw json.RawMessage) string {
	var p tabPinParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return layoutID("id", p.ID)
}

func validateTabReorderRaw(raw json.RawMessage) string {
	var p tabReorderParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := workspaceRef("workspaceId", p.WorkspaceID); msg != "" {
		return msg
	}
	return layoutMemberIDs(p.IDs)
}

func validatePaneCreateRaw(raw json.RawMessage) string {
	var p paneCreateParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	if msg := layoutID("tabId", p.TabID); msg != "" {
		return msg
	}
	return validatePaneFacts("", p.Cwd, p.Kind, p.Endpoint, p.SizeShare)
}

// validatePaneFacts is the pane's own shape, wherever a pane arrives: on its
// own through panes.create, or as the first member of a container. One
// implementation, because two would agree until the day they did not.
func validatePaneFacts(prefix, cwd, kind string, endpoint *string, sizeShare float64) string {
	if prefix != "" {
		prefix += "."
	}
	// An EMPTY cwd is legal and means the backend process's own directory,
	// which is what an unconfigured local shell already inherits — the same
	// value replacement.cwd has always taken, and now for the same reason.
	// A renderer opening a tab does not yet know where the shell will land:
	// the cwd arrives from the session a round trip after the pane exists, so
	// requiring one here would only buy a path the renderer invented. What is
	// still refused is an unbounded one.
	if utf8.RuneCountInString(cwd) > maxCwdRunes {
		return prefix + "cwd is bounded"
	}
	if msg := nullableBounded(prefix+"endpoint", endpoint, maxLayoutEndpointRunes); msg != "" {
		return msg
	}
	switch content.PaneKind(kind) {
	case content.PaneLocal:
		// An endpoint on a local pane would be accepted and then dropped —
		// the empty string is a real value meaning the local machine, so
		// there is nowhere honest to put it.
		if endpoint != nil {
			return prefix + "endpoint is an ssh fact and is only accepted on kind = ssh"
		}
	case content.PaneSSH:
		if endpoint == nil || strings.TrimSpace(*endpoint) == "" {
			return prefix + "endpoint is required on kind = ssh"
		}
	default:
		return prefix + "kind must be one of local, ssh"
	}
	if math.IsNaN(sizeShare) || math.IsInf(sizeShare, 0) || sizeShare <= 0 || sizeShare > 1 {
		return prefix + "sizeShare must be greater than 0 and at most 1 — it is this pane's share of its tab"
	}
	return ""
}

func validatePaneCwdRaw(raw json.RawMessage) string {
	var p paneCwdParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	// An EMPTY cwd is refused here, unlike at creation. There it means "wherever
	// an unconfigured shell lands", which is the honest answer before a session
	// exists; here it would be a report about a shell that has one, and storing
	// it would overwrite a real directory with a blank.
	if strings.TrimSpace(p.Cwd) == "" {
		return "cwd is required"
	}
	if utf8.RuneCountInString(p.Cwd) > maxCwdRunes {
		return "cwd is bounded"
	}
	return ""
}

func validatePaneMoveRaw(raw json.RawMessage) string {
	var p paneMoveParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := layoutID("id", p.ID); msg != "" {
		return msg
	}
	return layoutID("tabId", p.TabID)
}

// ── the handler ───────────────────────────────────────────────────────────

// layoutHandlers answers every layout method. It holds the LayoutOperation
// (nil → the content store is not wired) and the Responder, and nothing else:
// no connection, no connState, and deliberately no notion of who owns a row.
// A layout object is application-wide — a workspace created on one connection
// is renamed on the next — and §7 forbids reading a right out of an id, so
// there is nothing per-connection for this handler to consult.
type layoutHandlers struct {
	op    capability.LayoutOperation
	wired bool
	r     Responder
}

func (h layoutHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "layout store not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.LayoutService) error {
		switch req.Method {
		case "layout.read":
			snap, err := svc.Snapshot(ctx)
			granted := map[string]struct{}{}
			if err == nil {
				granted, err = svc.SandboxGrantedPaneIDs(ctx)
			}
			h.answer(req, err, func() any {
				return layoutReadResponse{
					DefaultWorkspaceID: snap.DefaultWorkspaceID,
					Workspaces:         wireWorkspaces(snap.Workspaces),
					Tabs:               wireTabs(snap.Tabs),
					Panes:              wirePanes(snap.Panes, granted),
				}
			})
		case "workspaces.create":
			var p workspaceCreateParams
			if !h.decode(req, &p) {
				return nil
			}
			made, err := svc.CreateWorkspace(ctx,
				content.Workspace{ID: p.ID, Name: p.Name, Colour: p.Colour, Position: p.Position},
				p.FirstTab.tab(p.ID, nil),
				p.FirstPane.pane(p.FirstTab.ID))
			h.answer(req, err, func() any {
				return workspaceCreateResponse{
					Workspace: wireWorkspace(made.Object.Workspace),
					FirstTab:  wireTab(made.Object.FirstTab),
					FirstPane: wirePane(made.Object.FirstPane),
					Replayed:  made.Replayed,
				}
			})
		case "workspaces.rename":
			var p workspaceRenameParams
			if !h.decode(req, &p) {
				return nil
			}
			ws, err := svc.RenameWorkspace(ctx, p.ID, p.Name)
			h.answer(req, err, func() any { return workspaceResponse{Workspace: wireWorkspace(ws)} })
		case "workspaces.recolour":
			var p workspaceRecolourParams
			if !h.decode(req, &p) {
				return nil
			}
			ws, err := svc.RecolourWorkspace(ctx, p.ID, p.Colour)
			h.answer(req, err, func() any { return workspaceResponse{Workspace: wireWorkspace(ws)} })
		case "workspaces.reorder":
			var p workspaceReorderParams
			if !h.decode(req, &p) {
				return nil
			}
			all, err := svc.ReorderWorkspaces(ctx, p.IDs)
			h.answer(req, err, func() any { return workspaceListResponse{Workspaces: wireWorkspaces(all)} })
		case "workspaces.close":
			var p layoutCloseParams
			if !h.decode(req, &p) {
				return nil
			}
			err := svc.DeleteWorkspace(ctx, p.ID, p.replacement())
			h.answer(req, err, func() any { return closedResponse{ID: p.ID} })
		case "tabs.create":
			var p tabCreateParams
			if !h.decode(req, &p) {
				return nil
			}
			made, err := svc.CreateTab(ctx, content.Tab{
				ID: p.ID, WorkspaceID: p.WorkspaceID, ParentID: p.ParentID, Name: p.Name,
				Colour: p.Colour, Position: p.Position, Pinned: p.Pinned,
				Layout: content.TabLayout(p.Layout),
			}, p.FirstPane.pane(p.ID))
			h.answer(req, err, func() any {
				return tabCreateResponse{
					Tab:       wireTab(made.Object.Tab),
					FirstPane: wirePane(made.Object.FirstPane),
					Replayed:  made.Replayed,
				}
			})
		case "tabs.rename":
			var p tabRenameParams
			if !h.decode(req, &p) {
				return nil
			}
			tab, err := svc.RenameTab(ctx, p.ID, p.Name)
			h.answer(req, err, func() any { return tabResponse{Tab: wireTab(tab)} })
		case "tabs.recolour":
			var p tabRecolourParams
			if !h.decode(req, &p) {
				return nil
			}
			tab, err := svc.RecolourTab(ctx, p.ID, p.Colour)
			h.answer(req, err, func() any { return tabResponse{Tab: wireTab(tab)} })
		case "tabs.pin":
			var p tabPinParams
			if !h.decode(req, &p) {
				return nil
			}
			tab, err := svc.PinTab(ctx, p.ID, p.Pinned)
			h.answer(req, err, func() any { return tabResponse{Tab: wireTab(tab)} })
		case "tabs.reorder":
			var p tabReorderParams
			if !h.decode(req, &p) {
				return nil
			}
			tabs, err := svc.ReorderTabs(ctx, p.WorkspaceID, p.IDs)
			h.answer(req, err, func() any { return tabListResponse{Tabs: wireTabs(tabs)} })
		case "tabs.close":
			var p layoutCloseParams
			if !h.decode(req, &p) {
				return nil
			}
			err := svc.DeleteTab(ctx, p.ID, p.replacement())
			h.answer(req, err, func() any { return closedResponse{ID: p.ID} })
		case "panes.create":
			var p paneCreateParams
			if !h.decode(req, &p) {
				return nil
			}
			made, err := svc.CreatePane(ctx, content.Pane{
				ID: p.ID, TabID: p.TabID, Cwd: p.Cwd, Kind: content.PaneKind(p.Kind),
				Endpoint: p.Endpoint, SizeShare: p.SizeShare,
			})
			h.answer(req, err, func() any {
				return paneCreateResponse{Pane: wirePane(made.Object), Replayed: made.Replayed}
			})
		case "panes.setCwd":
			var p paneCwdParams
			if !h.decode(req, &p) {
				return nil
			}
			pane, err := svc.SetPaneCwd(ctx, p.ID, p.Cwd)
			h.answer(req, err, func() any { return paneResponse{Pane: wirePane(pane)} })
		case "panes.move":
			var p paneMoveParams
			if !h.decode(req, &p) {
				return nil
			}
			pane, err := svc.MovePane(ctx, p.ID, p.TabID)
			h.answer(req, err, func() any { return paneResponse{Pane: wirePane(pane)} })
		case "panes.close":
			var p layoutCloseParams
			if !h.decode(req, &p) {
				return nil
			}
			err := svc.DeletePane(ctx, p.ID, p.replacement())
			h.answer(req, err, func() any { return closedResponse{ID: p.ID} })
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

func (h layoutHandlers) decode(req jsonrpcRequest, dst any) bool {
	if err := json.Unmarshal(req.Params, dst); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return false
	}
	return true
}

// answer sends the result, or maps the failure. The result is built lazily so
// a failed call never marshals a zero-valued object.
func (h layoutHandlers) answer(req jsonrpcRequest, err error, result func() any) {
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: layoutErrorCode(err), Message: err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(result()))
}

// layoutErrorCode maps a store failure to a JSON-RPC code. Everything the
// RENDERER can cause is invalid params and never a server fault: an id it
// reused for a second object, an id naming a row that is not there, a reorder
// that is not a permutation, a lineage edge the admission rules refuse, a
// create with no first member, a close with no replacement, a close of the
// default workspace, and a move across workspaces (which §12 q. 5 leaves
// open and which therefore fails closed). A caller that can tell those apart
// from a broken backend is a caller that can retry the right ones.
func layoutErrorCode(err error) int {
	switch {
	case errors.Is(err, content.ErrIDConflict),
		errors.Is(err, content.ErrNoSuchWorkspace),
		errors.Is(err, content.ErrNoSuchTab),
		errors.Is(err, content.ErrNoSuchPane),
		errors.Is(err, content.ErrNotAPermutation),
		errors.Is(err, content.ErrNoFirstTab),
		errors.Is(err, content.ErrNoFirstPane),
		errors.Is(err, content.ErrMismatchedContainer),
		errors.Is(err, content.ErrNoReplacement),
		errors.Is(err, content.ErrDefaultWorkspace),
		errors.Is(err, content.ErrCrossWorkspaceMove),
		errors.Is(err, lineage.ErrSelf),
		errors.Is(err, lineage.ErrCycle),
		errors.Is(err, lineage.ErrTooDeep),
		errors.Is(err, capability.ErrOperationInactive):
		return -32602
	}
	return -32603
}

// layoutReader is the layout chain's READ seam, or nil when the content store
// is not wired. It is what the open handler resolves a pane's workspace
// through (§4.5) — a read, off the pool, with no gate: see openHandlers.panes
// for why acquiring the gated operation there would be a deadlock rather than
// a nicety.
func (s *WSServer) layoutReader() paneWorkspaces {
	if s.contentDB == nil {
		return nil
	}
	return s.contentDB.Layout()
}

// ── registration ──────────────────────────────────────────────────────────

// layoutSpecs declares the layout methods on the CONTENT operation queue: the
// layout chain is three tables in content's schema v1, so it shares the
// content domain's gate and queue with the ledger and history.*.
func (s *WSServer) layoutSpecs(contentSub control.Submission, lane control.Admission, contentGate control.Admission) []methodSpec {
	var op capability.LayoutOperation
	if s.contentDB != nil {
		op = capability.NewLayoutOperation(contentGate, lane, s.contentDB)
	}
	wired := s.contentDB != nil
	build := func(r Responder) handlerFunc {
		h := layoutHandlers{op: op, wired: wired, r: r}
		return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
	}
	specs := []struct {
		method string
		// validate is nil for the ONE method that takes no params: the read.
		// noParams() is an assertion rather than an omission — a caller that
		// sends some is refused, so "layout.read takes nothing" is checked
		// rather than merely intended.
		validate func(json.RawMessage) string
	}{
		{"layout.read", nil},
		{"workspaces.create", validateWorkspaceCreateRaw},
		{"workspaces.rename", validateWorkspaceRenameRaw},
		{"workspaces.recolour", validateWorkspaceRecolourRaw},
		{"workspaces.reorder", validateWorkspaceReorderRaw},
		{"workspaces.close", validateLayoutCloseRaw},
		{"tabs.create", validateTabCreateRaw},
		{"tabs.rename", validateTabRenameRaw},
		{"tabs.recolour", validateTabRecolourRaw},
		{"tabs.pin", validateTabPinRaw},
		{"tabs.reorder", validateTabReorderRaw},
		{"tabs.close", validateLayoutCloseRaw},
		{"panes.create", validatePaneCreateRaw},
		{"panes.move", validatePaneMoveRaw},
		{"panes.setCwd", validatePaneCwdRaw},
		// panes.close is DeletePane's wire caller, and it takes the same
		// close params as tabs.close and workspaces.close because it is the
		// same act one rung down: removing the last pane takes its tab, that
		// tab's workspace if it was the last, and mints the replacement if
		// the application is left with nothing.
		{"panes.close", validateLayoutCloseRaw},
	}
	out := make([]methodSpec, 0, len(specs))
	for _, spec := range specs {
		validator := noParams()
		if spec.validate != nil {
			validator = params(spec.validate)
		}
		out = append(out, regResponder(contentSub, spec.method, validator, build))
	}
	return out
}
