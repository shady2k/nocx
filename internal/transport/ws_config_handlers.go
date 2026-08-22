package transport

// The config-domain control handlers as constructed types (migration map,
// "profiles.* / groups.* / settings.* — the config domain"): each handler
// holds a ConfigOperation (gates [config, vault] — the row-resolving write
// paths and the secret-class settings are vault-backed) or a
// TabbyImportOperation, plus the Responder. Never the *WSServer: a handler
// constructed with the operation cannot reach a store it was not given.
//
// The pure wire helpers (wireProfile, wireGroup, wireEffectiveSecretFields,
// vault.RowFor, optionsToWire) stay here — they map stored references to
// renderer row handles and touch no store. Row-handle RESOLUTION now lives
// in ConfigService (migration map delete-list: optionsFromWire, groupFromWire,
// sparseFromWire, secretRowInputs, rowToSecretRef are gone).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/importer"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
)

// settingsSetParams carries the key and the untyped value.
type settingsSetParams struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// settingsResetParams carries the key to reset.
type settingsResetParams struct {
	Key string `json:"key"`
}

// settingsSecretSetParams carries the key and the secret value.
type settingsSecretSetParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// settingsSecretDeleteParams carries the key to delete.
type settingsSecretDeleteParams struct {
	Key string `json:"key"`
}

// settingsSecretExistsParams carries the key to check.
type settingsSecretExistsParams struct {
	Key string `json:"key"`
}

// ── config-domain ingress bounds ─────────────────────────────────────────
//
// Every one of these params is renderer-supplied and reaches something real:
// a profile host is dialled, a key path is read at connect time, a forward
// is replayed, a group default is inherited, an endpoint URL is dialled, a
// secret setting is stored, a Tabby config is parsed. The bound for each
// field comes from what the field does, and where the repo already decides
// a rule (profile.ValidForwards, profile.ValidateEndpoint,
// profile.ValidateBaseURL, ProfileDefaults.Validate, validatePatch) the
// validator calls it rather than keeping a second copy.

const (
	// maxConfigIDRunes bounds renderer-supplied profile, group and endpoint
	// ids. Ids are backend-minted "typ:custom:slug:uuid"; a renderer-supplied
	// id only replaces the mint, and the ask path bounds the same class of
	// value at 128 (maxIDRunes).
	maxConfigIDRunes = 128
	// maxConfigNameRunes bounds display names (profile, group, endpoint,
	// model). Names are echoed in lists and slugified into minted ids.
	maxConfigNameRunes = 200
	// maxJumpHostRunes bounds jumpHost, a profile name or id.
	maxJumpHostRunes = 256
	// maxIconRunes and maxColorRunes bound a group's icon (an emoji) and
	// color (a hex literal), rendered verbatim in the sidebar.
	maxIconRunes  = 64
	maxColorRunes = 64
	// maxSecretRowRunes bounds a renderer-supplied secret row handle
	// ("secrow:" + 32 hex, vault.RowFor). The resolver is the authority on
	// whether a handle resolves; this is the wire bound before resolution.
	maxSecretRowRunes = 128
	// maxEnumRunes bounds the closed-set option fields (auth, desiredMode,
	// relayConsent, portDiscovery, behaviorOnSessionEnd) and the profile
	// type. Their value sets are short literals. An unrecognised value is
	// deliberately NOT refused here — resolution falls back to the default
	// for a stored value (nocx-mlm7) — so this is a wire bound, not a
	// closed-set check.
	maxEnumRunes = 64
	// maxSettingsKeyRunes bounds a settings key: registry-declared dotted
	// names like "history.enabled". The registry is the store-side authority
	// on which keys exist; this bounds the name before the lookup.
	maxSettingsKeyRunes = 256
	// maxSecretSettingRunes bounds a secret-class setting value and the
	// Tabby vault passphrase. A wire-cost bound sized like the assistant
	// key's ceiling (maxProbeKeyRunes — some providers issue long JWTs):
	// the product defines no tighter ceiling for either value, so the
	// validator's job is the generous bound plus the control-character
	// refusal, not a naming rule.
	maxSecretSettingRunes = 8_000
	// maxEndpointURLRunes bounds an endpoint base URL, matching the probe
	// path's maxProbeURLRunes — the same URL is dialled by the Test button
	// and the ask path.
	maxEndpointURLRunes = 2_000
	// maxModelNameRunes bounds an endpoint model name and alias. Model ids
	// ride request bodies verbatim (the probe validator bounds them at 200).
	maxModelNameRunes = 200
	// maxHeaderNameRunes bounds a custom header name (bead nocx-lyyk). A
	// header name is short by HTTP nature; the bound is a wire-cost ceiling.
	maxHeaderNameRunes = 256
	// maxHeaderValueRunes bounds a literal custom header value. Values ride
	// request headers verbatim; the bound is a wire-cost ceiling, not a
	// product rule about how long a header may be.
	maxHeaderValueRunes = 4_096
)

// decodeObject decodes params into dst, treating absent, null or an empty
// payload as an empty object — a field-aware validator then answers "x is
// required" rather than a parse error. Returns "" on success.
func decodeObject(raw json.RawMessage, dst any) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return "params must be a JSON object"
	}
	return ""
}

// boundedRunes returns "" when s is within bound, else a message naming the
// field and the bound.
func boundedRunes(field string, s string, bound int) string {
	if utf8.RuneCountInString(s) > bound {
		return fmt.Sprintf("%s exceeds %d characters", field, bound)
	}
	return ""
}

// boundedOptionalRunes bounds an optional string-typed field (the stored
// options use typed pointers: *AuthMode, *DesiredMode, ...). A nil pointer
// is an unset field and always passes.
func boundedOptionalRunes[T ~string](field string, v *T, bound int) string {
	if v == nil {
		return ""
	}
	return boundedRunes(field, string(*v), bound)
}

// configIDRunes is the common id bound, applied everywhere an id is checked.
func configIDRunes(field, id string) string {
	return boundedRunes(field, id, maxConfigIDRunes)
}

// validateSecretRow bounds a renderer-supplied row handle. The vault's
// resolver is the authority on whether a handle resolves; this is the wire
// bound before resolution, and a control character never occurs in a minted
// handle.
func validateSecretRow(field, row string) string {
	if msg := boundedRunes(field, row, maxSecretRowRunes); msg != "" {
		return msg
	}
	if hasControlChars(row) {
		return field + " must not contain control characters"
	}
	return ""
}

// validateForwardList asks the ONE authority on stored forward lists
// (profile.ValidForwards) — the connection editor and any transport-side
// gate ask the same question.
func validateForwardList(field string, fs []profile.ForwardSpec) string {
	if err := profile.ValidForwards(fs); err != nil {
		return fmt.Sprintf("%s: %v", field, err)
	}
	return ""
}

// validateStoredOptions checks every reachable field of a profile's stored
// options block — the fields the connection layer reads at connect time.
func validateStoredOptions(o profile.StoredSSHProfileOptions) string {
	if o.Host == "" {
		return "options.host is required"
	}
	if msg := boundedRunes("options.host", o.Host, maxHostRunes); msg != "" {
		return msg
	}
	if hasControlChars(o.Host) {
		return "options.host must not contain control characters"
	}
	if o.Port != nil && (*o.Port < 0 || *o.Port > 65535) {
		return "options.port must be between 0 and 65535"
	}
	if o.User != nil {
		if msg := boundedRunes("options.user", *o.User, maxUserRunes); msg != "" {
			return msg
		}
		if hasControlChars(*o.User) {
			return "options.user must not contain control characters"
		}
	}
	if o.KeyPath != nil {
		if msg := boundedRunes("options.keyPath", *o.KeyPath, maxPathRunes); msg != "" {
			return msg
		}
		if hasControlChars(*o.KeyPath) {
			return "options.keyPath must not contain control characters"
		}
	}
	if o.JumpHost != nil {
		if msg := boundedRunes("options.jumpHost", *o.JumpHost, maxJumpHostRunes); msg != "" {
			return msg
		}
	}
	for _, f := range []struct {
		field string
		row   string
	}{
		{"options.passwordSecret", o.PasswordSecret},
		{"options.keySecret", o.KeySecret},
		{"options.keyPassphraseSecret", o.KeyPassphraseSecret},
	} {
		if msg := validateSecretRow(f.field, f.row); msg != "" {
			return msg
		}
	}
	for _, f := range []struct {
		field string
		v     *int
	}{
		{"options.keepaliveInterval", o.KeepaliveInterval},
		{"options.keepaliveCountMax", o.KeepaliveCountMax},
		{"options.readyTimeout", o.ReadyTimeout},
	} {
		if f.v != nil && *f.v < 0 {
			return f.field + " must not be negative"
		}
	}
	if msg := boundedOptionalRunes("options.auth", o.Auth, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedOptionalRunes("options.desiredMode", o.DesiredMode, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedOptionalRunes("options.relayConsent", o.RelayConsent, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedOptionalRunes("options.portDiscovery", o.PortDiscovery, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedOptionalRunes("options.behaviorOnSessionEnd", o.BehaviorOnSessionEnd, maxEnumRunes); msg != "" {
		return msg
	}
	if o.Forwards != nil {
		if msg := validateForwardList("options.forwards", *o.Forwards); msg != "" {
			return msg
		}
	}
	return ""
}

// validateProfileBase bounds the identity fields shared by every profile
// type. Nothing here is required: create mints an id, and the domain rule
// for a usable profile is the host (validateStoredOptions), not these.
func validateProfileBase(b profile.Base) string {
	if msg := configIDRunes("id", b.ID); msg != "" {
		return msg
	}
	if msg := boundedRunes("name", b.Name, maxConfigNameRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("type", b.Type, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := configIDRunes("group", b.Group); msg != "" {
		return msg
	}
	if msg := boundedRunes("icon", b.Icon, maxIconRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("color", b.Color, maxColorRunes); msg != "" {
		return msg
	}
	return ""
}

// validateProfileParams is the shared create/update check; idRequired
// separates the two methods (update must name the record, create mints).
func validateProfileParams(p profile.SSHProfile, idRequired bool) string {
	if idRequired && p.ID == "" {
		return "id is required"
	}
	if msg := validateProfileBase(p.Base); msg != "" {
		return msg
	}
	return validateStoredOptions(p.Options)
}

// validateProfileCreateRaw is the registered validator for profiles.create.
func validateProfileCreateRaw(raw json.RawMessage) string {
	var p profile.SSHProfile
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return validateProfileParams(p, false)
}

// validateProfileUpdateRaw is the registered validator for profiles.update.
func validateProfileUpdateRaw(raw json.RawMessage) string {
	var p profile.SSHProfile
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return validateProfileParams(p, true)
}

// validateProfileDeleteRaw is the registered validator for profiles.delete.
// The handler needs the id to delete; today an empty id falls through to a
// store "not found" -32603, which is a client error wearing the wrong code.
func validateProfileDeleteRaw(raw json.RawMessage) string {
	var p struct {
		ID string `json:"id"`
	}
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	return configIDRunes("id", p.ID)
}

// validateEffectiveRaw is the registered validator for profiles.effective.
// An empty batch is a legitimate request (the handler answers an empty
// result); every id in a batch is bounded.
func validateEffectiveRaw(raw json.RawMessage) string {
	var p effectiveParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	for _, id := range p.IDs {
		if msg := configIDRunes("ids", id); msg != "" {
			return msg
		}
	}
	return ""
}

// validatePatchSetForwards checks a profiles.patch set value for
// options.forwards against the one authority on forward lists. The service
// decodes strictly (toForwardSpecs) and would silently ignore a malformed
// list (ApplyPatchSet's false return is dropped), so the boundary refuses
// what the handler would silently swallow.
func validatePatchSetForwards(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "options.forwards must be an array of forward specs"
	}
	var fs []profile.ForwardSpec
	if err := json.Unmarshal(b, &fs); err != nil {
		return "options.forwards must be an array of forward specs"
	}
	return validateForwardList("options.forwards", fs)
}

// validatePatchRaw is the registered validator for profiles.patch. It calls
// the handler's own validatePatch (id required, allowlisted paths, disjoint
// set/unset) and then bounds the values. The three secret paths must carry a
// string — the service's own rule ("%s must be a string") — and a forwards
// value must satisfy ValidForwards. The remaining set values are bounded as
// strings by the floor's walk (64k runes, bounded nesting); the string
// fields get their own ceilings when the value is a string. A wrong-typed
// value for a non-secret path is the handler's silent-coercion defect
// (ApplyPatchSet's toString/toInt/toBool), reported, not silently changed.
func validatePatchRaw(raw json.RawMessage) string {
	var p patchParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if err := validatePatch(p); err != nil {
		return err.Error()
	}
	if msg := configIDRunes("id", p.ID); msg != "" {
		return msg
	}
	for path, v := range p.Set {
		switch path {
		case "options.passwordSecret", "options.keySecret", "options.keyPassphraseSecret":
			s, ok := v.(string)
			if !ok {
				return fmt.Sprintf("%s must be a string", path)
			}
			if msg := validateSecretRow(path, s); msg != "" {
				return msg
			}
		case "options.forwards":
			if msg := validatePatchSetForwards(v); msg != "" {
				return msg
			}
		case "options.user":
			if s, ok := v.(string); ok {
				if msg := boundedRunes(path, s, maxUserRunes); msg != "" {
					return msg
				}
			}
		case "options.jumpHost":
			if s, ok := v.(string); ok {
				if msg := boundedRunes(path, s, maxJumpHostRunes); msg != "" {
					return msg
				}
			}
		case "options.auth", "options.desiredMode", "options.relayConsent",
			"options.portDiscovery", "options.behaviorOnSessionEnd":
			if s, ok := v.(string); ok {
				if msg := boundedRunes(path, s, maxEnumRunes); msg != "" {
					return msg
				}
			}
		}
	}
	return walkGeneric(p.Set, 0)
}

// validateSparseDefaults bounds every reachable field of a group's defaults
// block — the values profiles inherit through the cascade.
func validateSparseDefaults(d *profile.ProfileDefaults) string {
	o := d.SparseSSHOptions
	if o.Port != nil && (*o.Port < 0 || *o.Port > 65535) {
		return "defaults.port must be between 0 and 65535"
	}
	if o.User != nil {
		if msg := boundedRunes("defaults.user", *o.User, maxUserRunes); msg != "" {
			return msg
		}
		if hasControlChars(*o.User) {
			return "defaults.user must not contain control characters"
		}
	}
	if o.KeyPath != nil {
		if msg := boundedRunes("defaults.keyPath", *o.KeyPath, maxPathRunes); msg != "" {
			return msg
		}
		if hasControlChars(*o.KeyPath) {
			return "defaults.keyPath must not contain control characters"
		}
	}
	if o.JumpHost != nil {
		if msg := boundedRunes("defaults.jumpHost", *o.JumpHost, maxJumpHostRunes); msg != "" {
			return msg
		}
	}
	for _, f := range []struct {
		field string
		v     *string
	}{
		{"defaults.passwordSecret", o.PasswordSecret},
		{"defaults.keySecret", o.KeySecret},
		{"defaults.keyPassphraseSecret", o.KeyPassphraseSecret},
	} {
		if f.v != nil {
			if msg := validateSecretRow(f.field, *f.v); msg != "" {
				return msg
			}
		}
	}
	for _, f := range []struct {
		field string
		v     *int
	}{
		{"defaults.keepaliveInterval", o.KeepaliveInterval},
		{"defaults.keepaliveCountMax", o.KeepaliveCountMax},
		{"defaults.readyTimeout", o.ReadyTimeout},
	} {
		if f.v != nil && *f.v < 0 {
			return f.field + " must not be negative"
		}
	}
	if msg := boundedOptionalRunes("defaults.auth", o.Auth, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedOptionalRunes("defaults.desiredMode", o.DesiredMode, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedOptionalRunes("defaults.portDiscovery", o.PortDiscovery, maxEnumRunes); msg != "" {
		return msg
	}
	if msg := boundedOptionalRunes("defaults.behaviorOnSessionEnd", o.BehaviorOnSessionEnd, maxEnumRunes); msg != "" {
		return msg
	}
	return ""
}

// validateGroupParams bounds every reachable field of a ProfileGroup.
// idRequired separates create (mint) from update/apply (name the record);
// the unknown-keys check is the defaults' own Validate.
func validateGroupParams(g profile.ProfileGroup, idRequired bool) string {
	if idRequired && g.ID == "" {
		return "id is required"
	}
	if msg := configIDRunes("id", g.ID); msg != "" {
		return msg
	}
	if msg := configIDRunes("parentGroupId", g.ParentGroupID); msg != "" {
		return msg
	}
	if msg := boundedRunes("name", g.Name, maxConfigNameRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("icon", g.Icon, maxIconRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("color", g.Color, maxColorRunes); msg != "" {
		return msg
	}
	if g.Defaults != nil {
		if msg := validateSparseDefaults(g.Defaults); msg != "" {
			return msg
		}
		if err := g.Defaults.Validate(); err != nil {
			return err.Error()
		}
	}
	return ""
}

// validateGroupCreateRaw is the registered validator for groups.create.
func validateGroupCreateRaw(raw json.RawMessage) string {
	var g profile.ProfileGroup
	if msg := decodeObject(raw, &g); msg != "" {
		return msg
	}
	return validateGroupParams(g, false)
}

// validateGroupUpdateRaw is the registered validator for groups.update.
func validateGroupUpdateRaw(raw json.RawMessage) string {
	var g profile.ProfileGroup
	if msg := decodeObject(raw, &g); msg != "" {
		return msg
	}
	return validateGroupParams(g, true)
}

// validateGroupDeleteRaw is the registered validator for groups.delete.
func validateGroupDeleteRaw(raw json.RawMessage) string {
	var p struct {
		ID string `json:"id"`
	}
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	return configIDRunes("id", p.ID)
}

// validateGroupImpactRaw is the registered validator for groups.impact. It
// calls the params' own validate (exactly one of group / deleteGroupId,
// group.id present) and then bounds the embedded group or the id.
func validateGroupImpactRaw(raw json.RawMessage) string {
	var p groupImpactParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if err := p.validate(); err != nil {
		return err.Error()
	}
	if p.Group != nil {
		return validateGroupParams(*p.Group, false)
	}
	return configIDRunes("deleteGroupId", p.DeleteGroupID)
}

// validateProfileMoveImpactRaw is the registered validator for
// profiles.moveImpact. It calls the params' own validate (non-empty
// profileIds) and bounds every id; an empty targetGroupId is the deliberate
// promotion-to-root request.
func validateProfileMoveImpactRaw(raw json.RawMessage) string {
	var p profileMoveImpactParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if err := p.validate(); err != nil {
		return err.Error()
	}
	for _, id := range p.ProfileIDs {
		if msg := configIDRunes("profileIds", id); msg != "" {
			return msg
		}
	}
	return configIDRunes("targetGroupId", p.TargetGroupID)
}

// validateGroupApplyRaw is the registered validator for groups.apply. The
// params ARE the array (JSON-RPC positional form, which the floor already
// admitted); the handler refuses an empty array, and the store requires
// every member to name a group (ErrGroupIDRequired).
func validateGroupApplyRaw(raw json.RawMessage) string {
	var groups []profile.ProfileGroup
	if len(strings.TrimSpace(string(raw))) == 0 {
		return "groups required"
	}
	if err := json.Unmarshal(raw, &groups); err != nil {
		return "params must be a JSON array of groups"
	}
	if len(groups) == 0 {
		return "groups required"
	}
	for i, g := range groups {
		if msg := validateGroupParams(g, true); msg != "" {
			return fmt.Sprintf("groups[%d]: %s", i, msg)
		}
	}
	return ""
}

// validateSettingsKey is the shared key check for the settings.* methods:
// present, bounded, and free of control characters. The registry remains the
// authority on whether a key EXISTS (the handler answers "Unknown setting").
func validateSettingsKey(key string) string {
	if key == "" {
		return "key is required"
	}
	if msg := boundedRunes("key", key, maxSettingsKeyRunes); msg != "" {
		return msg
	}
	if hasControlChars(key) {
		return "key must not contain control characters"
	}
	return ""
}

// validateSettingsKeyRaw is the registered validator for settings.reset,
// settings.secretDelete and settings.secretExists.
func validateSettingsKeyRaw(raw json.RawMessage) string {
	var p struct {
		Key string `json:"key"`
	}
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return validateSettingsKey(p.Key)
}

// validateSettingsSetRaw is the registered validator for settings.set. The
// value's meaning belongs to the descriptor (the handler type-checks it
// against the control kind); the validator bounds the key, requires a
// non-null value, and walks the value with the floor's own bounds.
func validateSettingsSetRaw(raw json.RawMessage) string {
	var p settingsSetParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := validateSettingsKey(p.Key); msg != "" {
		return msg
	}
	if len(p.Value) == 0 {
		return "value is required"
	}
	if strings.TrimSpace(string(p.Value)) == "null" {
		return "value must not be null"
	}
	var v any
	if err := json.Unmarshal(p.Value, &v); err != nil {
		return "value must be valid JSON"
	}
	return walkGeneric(v, 0)
}

// validateSettingsSecretSetRaw is the registered validator for
// settings.secretSet: a secret-class value is a credential, so it is
// required, bounded and free of control characters.
func validateSettingsSecretSetRaw(raw json.RawMessage) string {
	var p struct {
		Key   string  `json:"key"`
		Value *string `json:"value"`
	}
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if msg := validateSettingsKey(p.Key); msg != "" {
		return msg
	}
	if p.Value == nil {
		return "value is required"
	}
	if msg := boundedRunes("value", *p.Value, maxSecretSettingRunes); msg != "" {
		return msg
	}
	if hasControlChars(*p.Value) {
		return "value must not contain control characters"
	}
	return ""
}

// validateEndpointParamsWith is the ONE validator for the endpoint write
// params: the base fields plus the credential row (the "use an existing
// secret" choice, nocx-rzjw) and the custom headers (nocx-lyyk). The record
// level rules are profile.ValidateEndpoint — the same check the store runs
// on save — and the key rides the params once to become an Authorization
// header, so it gets the probe key's bound and the control-character
// refusal.
func validateEndpointParamsWith(name, baseURL string, schema profile.EndpointSchema, key, credentialRow string, models []endpointModelInput, headers []endpointHeaderInput) string {
	if msg := boundedRunes("name", name, maxConfigNameRunes); msg != "" {
		return msg
	}
	if msg := boundedRunes("baseUrl", baseURL, maxEndpointURLRunes); msg != "" {
		return msg
	}
	if key != "" {
		if msg := boundedRunes("key", key, maxProbeKeyRunes); msg != "" {
			return msg
		}
		if hasControlChars(key) {
			return "key must not contain control characters"
		}
	}
	// The credential has ONE source: a typed key (minted or rotated) or a
	// row handle (referenced). Both is a contradiction, and the renderer's
	// source control makes both unreachable — this is the wire's own check,
	// and the service holds the same rule as its backstop.
	if key != "" && credentialRow != "" {
		return "the endpoint credential has two sources: a typed key and an existing secret are mutually exclusive"
	}
	if credentialRow != "" {
		if msg := boundedRunes("credential", credentialRow, maxSecretRowRunes); msg != "" {
			return msg
		}
		if !isRowHandle(credentialRow) {
			return "credential must be a secrow handle"
		}
	}
	for i, m := range models {
		if msg := boundedRunes(fmt.Sprintf("models[%d].name", i), m.Name, maxModelNameRunes); msg != "" {
			return msg
		}
		if hasControlChars(m.Name) {
			return fmt.Sprintf("models[%d].name must not contain control characters", i)
		}
		if m.Alias != nil {
			if msg := boundedRunes(fmt.Sprintf("models[%d].alias", i), *m.Alias, maxModelNameRunes); msg != "" {
				return msg
			}
		}
	}
	if msg := validateEndpointHeaderRows(headers); msg != "" {
		return msg
	}
	e := profile.Endpoint{
		Name:          name,
		BaseURL:       baseURL,
		Schema:        resolveEndpointSchema(schema),
		CredentialRef: credentialRow,
		Models:        wireModelsToStored(models),
		Headers:       wireHeadersToStored(headers),
	}
	if err := profile.ValidateEndpoint(e); err != nil {
		return err.Error()
	}
	return ""
}

// validateEndpointHeaderRows checks the wire-form header rows: per-row
// bounds and the row-handle grammar, exactly-one-source, and then the
// record-level rules (profile.ValidateEndpointHeaders — the ONE owner of the
// refused-name set, the control characters and the duplicate rule), so a
// header refused at save time is refused identically at probe time.
func validateEndpointHeaderRows(headers []endpointHeaderInput) string {
	for i, h := range headers {
		if msg := boundedRunes(fmt.Sprintf("headers[%d].name", i), h.Name, maxHeaderNameRunes); msg != "" {
			return msg
		}
		if (h.Value == nil) == (h.Secret == nil) {
			return fmt.Sprintf("headers[%d]: a header value needs exactly one source — a literal or an existing secret", i)
		}
		if h.Value != nil {
			if msg := boundedRunes(fmt.Sprintf("headers[%d].value", i), *h.Value, maxHeaderValueRunes); msg != "" {
				return msg
			}
		}
		if h.Secret != nil {
			if msg := boundedRunes(fmt.Sprintf("headers[%d].secret", i), *h.Secret, maxSecretRowRunes); msg != "" {
				return msg
			}
			if !isRowHandle(*h.Secret) {
				return fmt.Sprintf("headers[%d].secret must be a secrow handle", i)
			}
		}
	}
	if err := profile.ValidateEndpointHeaders(wireHeadersToStored(headers)); err != nil {
		return err.Error()
	}
	return ""
}

// validateEndpointCreateRaw is the registered validator for endpoints.create.
func validateEndpointCreateRaw(raw json.RawMessage) string {
	var p endpointCreateParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	return validateEndpointParamsWith(p.Name, p.BaseURL, p.Schema, p.Key, p.Credential, p.Models, p.Headers)
}

// validateEndpointUpdateRaw is the registered validator for endpoints.update.
func validateEndpointUpdateRaw(raw json.RawMessage) string {
	var p endpointUpdateParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	if msg := configIDRunes("id", p.ID); msg != "" {
		return msg
	}
	return validateEndpointParamsWith(p.Name, p.BaseURL, p.Schema, p.Key, p.Credential, p.Models, p.Headers)
}

// validateEndpointDeleteRaw is the registered validator for endpoints.delete.
func validateEndpointDeleteRaw(raw json.RawMessage) string {
	var p struct {
		ID string `json:"id"`
	}
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	return configIDRunes("id", p.ID)
}

// validateTabbyImportRaw is the registered validator for profiles.importTabby
// and profiles.tabbyPreview: config is required (the handler's own rule),
// and the passphrase — when supplied — is a credential: bounded as a
// wire-cost ceiling and free of control characters. The config's honest
// ceiling is the params wire budget (maxParamsBytes): the importer defines
// no tighter bound, and a tighter number needs product data on real Tabby
// exports.
func validateTabbyImportRaw(raw json.RawMessage) string {
	var p struct {
		Config     string `json:"config"`
		Passphrase string `json:"passphrase,omitempty"`
	}
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if p.Config == "" {
		return "config is required"
	}
	if msg := boundedRunes("passphrase", p.Passphrase, maxSecretSettingRunes); msg != "" {
		return msg
	}
	if hasControlChars(p.Passphrase) {
		return "passphrase must not contain control characters"
	}
	return ""
}

// validateTabbyExecuteRaw is the registered validator for
// profiles.tabbyExecute. The plan token is backend-minted by storePlan as
// exactly 64 lowercase hex characters; anything else cannot name a plan, so
// the shape is closed.
func validateTabbyExecuteRaw(raw json.RawMessage) string {
	var p tabbyExecuteParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if p.PlanToken == "" {
		return "planToken is required"
	}
	if len(p.PlanToken) != 64 {
		return "planToken must be the 64-character token the preview returned"
	}
	for _, r := range p.PlanToken {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "planToken must be the 64-character token the preview returned"
		}
	}
	return ""
}

// profileHandlers answers the profiles.* methods. wired is true when the
// profile repository is wired; the old handler answered -32601 "profiles not
// available" without it, and the tests assert that.
type profileHandlers struct {
	op    capability.ConfigOperation // nil → config domain not wired
	wired bool                       // profile repository wired
	r     Responder
}

func (h profileHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "profiles not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		switch req.Method {
		case "profiles.list":
			profs, err := svc.ListProfiles()
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
				return nil
			}
			// Secret references stay backend-owned: hand the renderer row handles.
			for i := range profs {
				profs[i] = wireProfile(profs[i])
			}
			_ = h.r.TryResult(req.ID, mustMarshal(profs))
		case "profiles.create":
			var p profile.SSHProfile
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			// Mint an ID when the renderer sends none.
			if p.ID == "" {
				p.ID = profile.NewProfileID("ssh", p.Name)
			}
			// The renderer names secrets by row handle; the service resolves
			// them to references before storage (migration map).
			if err := svc.CreateProfile(p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: profileMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireProfile(p)))
		case "profiles.update":
			var p profile.SSHProfile
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if p.ID == "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "id required"})
				return nil
			}
			if err := svc.UpdateProfile(p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: profileMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireProfile(p)))
		case "profiles.delete":
			var params struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if err := svc.DeleteProfile(params.ID); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(true))
		case "profiles.effective":
			h.handleEffective(ctx, svc, req)
		case "profiles.patch":
			h.handlePatch(ctx, svc, req)
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleEffective is the batched effective-profile resolution (profiles.effective).
func (h profileHandlers) handleEffective(ctx context.Context, svc capability.ConfigService, req jsonrpcRequest) {
	var params effectiveParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	if len(params.IDs) == 0 {
		_ = h.r.TryResult(req.ID, mustMarshal(effectiveResponse{}))
		return
	}

	allProfiles, err := svc.ListProfiles()
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("load profiles: %v", err)})
		return
	}
	allGroups, err := svc.ListGroups()
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("load groups: %v", err)})
		return
	}
	// Build lookups first.
	profByID := make(map[string]profile.SSHProfile, len(allProfiles))
	for _, p := range allProfiles {
		profByID[p.ID] = p
	}
	groupByID := make(map[string]profile.ProfileGroup, len(allGroups))
	for _, g := range allGroups {
		groupByID[g.ID] = g
	}

	var dtos []profile.EffectiveProfileDTO
	var errs []profileErrorEntry

	for _, id := range params.IDs {
		p, ok := profByID[id]
		if !ok {
			errs = append(errs, profileErrorEntry{ID: id, Error: "profile not found"})
			continue
		}

		// Identity lives inline on the profile (ADR-0017): the effective
		// options are the resolved options.
		eff, err := profile.ResolveEffectiveProfile(p, allGroups, profile.SparseSSHOptions{})
		if err != nil {
			errs = append(errs, profileErrorEntry{ID: id, Error: err.Error()})
			continue
		}

		// Secret references stay backend-owned: hand the renderer row handles.
		dto := profile.ToEffectiveDTO(eff, groupByID)
		wireEffectiveSecretFields(&dto)
		dtos = append(dtos, dto)
	}

	_ = h.r.TryResult(req.ID, mustMarshal(effectiveResponse{
		Profiles: dtos,
		Errors:   errs,
	}))
}

// handlePatch applies explicit set/unset operations (profiles.patch). The
// mutation and the follow-up read run inside ONE operation, so the read
// observes the awaited mutation under the same gate (the transport never
// promises FIFO between separately-admitted requests).
func (h profileHandlers) handlePatch(ctx context.Context, svc capability.ConfigService, req jsonrpcRequest) {
	var params patchParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	if err := validatePatch(params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error()})
		return
	}

	allProfiles, listErr := svc.ListProfiles()
	if listErr != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("load profiles: %v", listErr)})
		return
	}
	var target *profile.SSHProfile
	for i := range allProfiles {
		if allProfiles[i].ID == params.ID {
			target = &allProfiles[i]
			break
		}
	}
	if target == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: fmt.Sprintf("profile %q not found", params.ID)})
		return
	}
	_ = target

	// PatchProfile resolves the three secret paths' row handles, applies the
	// set/unset operations, validates, and persists — one store write, the
	// same validation the old handler performed in line. Its failures are the
	// service's fixed vocabulary: the row-resolution and type validations
	// (client errors) and the store failures.
	if err := svc.PatchProfile(params.ID, params.Set, params.Unset); err != nil {
		code := profileMethodErrorCode(err)
		if patchValidationError(err) {
			code = -32602
		}
		_ = h.r.TryError(req.ID, RPCError{Code: code, Message: err.Error()})
		return
	}

	// The effective profile for the response is derived from the STORED
	// state after the patch, so the response never shows a half-applied set.
	after, err := svc.ListProfiles()
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("load profiles: %v", err)})
		return
	}
	var patched *profile.SSHProfile
	for i := range after {
		if after[i].ID == params.ID {
			patched = &after[i]
			break
		}
	}
	if patched == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("resolve after patch: profile %q not found", params.ID)})
		return
	}
	allGroups, err := svc.ListGroups()
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("load groups: %v", err)})
		return
	}
	groupByID := make(map[string]profile.ProfileGroup, len(allGroups))
	for _, g := range allGroups {
		groupByID[g.ID] = g
	}

	// Resolve effective profile from the patched stored options directly.
	eff, err := profile.ResolveEffectiveProfile(*patched, allGroups, profile.SparseSSHOptions{})
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("resolve after patch: %v", err)})
		return
	}

	dto := profile.ToEffectiveDTO(eff, groupByID)
	wireEffectiveSecretFields(&dto)
	_ = h.r.TryResult(req.ID, mustMarshal(dto))
}

// patchValidationError reports whether a PatchProfile error is one of the
// service's fixed validation failures (a client error, -32602) rather than a
// store failure. The capability service collapses validation and store
// failures into one error value, and its vocabulary is closed — these
// literals are the service's own texts (config.go), never request data, so
// matching them is not matching user input.
func patchValidationError(err error) bool {
	for _, lit := range []string{
		"must be a string",
		"host is required and cannot be unset",
		"no vault: cannot resolve a secret row",
		"unknown secret row",
	} {
		if strings.Contains(err.Error(), lit) {
			return true
		}
	}
	return false
}

// groupHandlers answers the groups.* methods. wired is true when the group
// repository is wired (-32601 "groups not available" without it).
type groupHandlers struct {
	op    capability.ConfigOperation
	wired bool // group repository wired
	r     Responder
}

func (h groupHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "groups not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		switch req.Method {
		case "groups.list":
			groups, err := svc.ListGroups()
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
				return nil
			}
			// The renderer addresses secret bindings by row handle (ADR-0011 §2):
			// convert every stored reference in the defaults before marshaling.
			for i := range groups {
				groups[i] = wireGroup(groups[i])
			}
			_ = h.r.TryResult(req.ID, mustMarshal(groups))
		case "groups.create":
			var g profile.ProfileGroup
			if err := json.Unmarshal(req.Params, &g); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			// Mint an ID when the renderer sends none, as profiles.create does.
			if g.ID == "" {
				g.ID = profile.NewGroupID(g.Name)
			}
			// The service resolves the defaults' row handles to stored
			// references before storage (migration map).
			if err := svc.CreateGroup(g); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: profileMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireGroup(g)))
		case "groups.update":
			var g profile.ProfileGroup
			if err := json.Unmarshal(req.Params, &g); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if g.ID == "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "id required"})
				return nil
			}
			// Resolve the defaults' row handles before comparing against
			// storage, or the guard below would see every secret binding as
			// a change (nocx: the defaults guard compares resolved
			// references, never a row against a ref).
			resolved, werr := svc.ResolveGroup(g)
			if werr != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: werr.Error()})
				return nil
			}
			// Guard: ParentGroupID and Defaults cannot be changed through
			// generic CRUD — the renderer MUST use groups.impact +
			// groups.apply (migration map: the guard stays in the handler,
			// reading via ListGroups).
			allGroups, loadErr := svc.ListGroups()
			if loadErr != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: loadErr.Error()})
				return nil
			}
			var stored *profile.ProfileGroup
			for i := range allGroups {
				if allGroups[i].ID == g.ID {
					stored = &allGroups[i]
					break
				}
			}
			if stored == nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "group not found"})
				return nil
			}
			if g.ParentGroupID != stored.ParentGroupID {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "ParentGroupId can only be changed through groups.apply, not groups.update"})
				return nil
			}
			if defaultsChanged(stored.Defaults, resolved.Defaults) {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Defaults can only be changed through groups.apply, not groups.update"})
				return nil
			}
			if err := svc.UpdateGroup(resolved); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: profileMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireGroup(g)))
		case "groups.delete":
			var params struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if params.ID == "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "id required"})
				return nil
			}
			// Use atomic delete (promotes children to root).
			if err := svc.DeleteGroupAtomic(params.ID); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: profileMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(true))
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleGroupImpact computes the effect of a proposed group change.
func (h groupHandlers) handleGroupImpact(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "groups not available"})
		return
	}
	var params groupImpactParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if err := params.validate(); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error()})
		return
	}

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		allProfiles, err := svc.ListProfiles()
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		allGroups, err := svc.ListGroups()
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}

		if params.Group != nil {
			// The renderer proposes bindings by row handle: resolve them to
			// stored references before computing impact, or the resolution
			// of the proposed defaults would carry row handles into the
			// diff (and the response must never leak references — the
			// diff layer re-derives rows from the resolved values).
			proposed, werr := svc.ResolveGroup(*params.Group)
			if werr != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: werr.Error()})
				return nil
			}
			resp := computeGroupUpdateImpact(proposed, allProfiles, allGroups)
			_ = h.r.TryResult(req.ID, mustMarshal(resp))
		} else {
			resp := computeGroupDeleteImpact(params.DeleteGroupID, allProfiles, allGroups)
			_ = h.r.TryResult(req.ID, mustMarshal(resp))
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleProfileMoveImpact computes the effect of moving profiles to a group.
func (h groupHandlers) handleProfileMoveImpact(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "profiles not available"})
		return
	}
	var params profileMoveImpactParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if err := params.validate(); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error()})
		return
	}

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		allProfiles, err := svc.ListProfiles()
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		allGroups, err := svc.ListGroups()
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}

		resp := computeProfileMoveImpact(params.ProfileIDs, params.TargetGroupID, allProfiles, allGroups)
		_ = h.r.TryResult(req.ID, mustMarshal(resp))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleGroupApply applies one or more group changes atomically.
func (h groupHandlers) handleGroupApply(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "groups not available"})
		return
	}
	var groups []profile.ProfileGroup
	if err := json.Unmarshal(req.Params, &groups); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if len(groups) == 0 {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "groups required"})
		return
	}

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		// The service resolves the renderer's row handles and applies the
		// groups under one store write (migration map: ApplyGroups, atomic;
		// row-resolving).
		if err := svc.ApplyGroups(groups); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: profileMethodErrorCode(err), Message: err.Error()})
			return nil
		}
		// The echo carries the row handles the renderer addressed, never the
		// stored references (ADR-0011 §2).
		for i := range groups {
			groups[i] = wireGroup(groups[i])
		}
		_ = h.r.TryResult(req.ID, mustMarshal(groups))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// settingsHandlers answers the settings.* methods. The settings surface is a
// sub-surface of the config domain (ConfigService.Settings()); wired is true
// when the settings registry is wired — the old handler answered the odd
// -32601 "Method not found" without it, and the tests assert that.
type settingsHandlers struct {
	op    capability.ConfigOperation
	wired bool // settings registry wired
	r     Responder
}

func (h settingsHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "Method not found"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		ss := svc.Settings()
		switch req.Method {
		case "settings.describe":
			_ = h.r.TryResult(req.ID, mustMarshal(map[string]any{
				"declarations":  ss.Declarations(),
				"groups":        ss.Groups(),
				"sectionGroups": ss.SectionGroups(),
			}))
		case "settings.getSnapshot":
			snap, err := ss.GetSnapshot()
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "settings.getSnapshot: " + err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(map[string]any{
				"values":     snap.Values,
				"overridden": snap.Overridden,
				"revision":   snap.Revision,
			}))
		case "settings.set":
			h.handleSet(ss, req)
		case "settings.reset":
			h.handleReset(ss, req)
		case "settings.secretSet":
			h.handleSecretSet(ss, req)
		case "settings.secretDelete":
			h.handleSecretDelete(ss, req)
		case "settings.secretExists":
			h.handleSecretExists(ss, req)
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// findDescriptorByKey looks up a setting declaration by key.
func findDescriptorByKey(ss capability.SettingsService, key string) settings.Descriptor {
	for _, d := range ss.Descriptors() {
		if d.Key() == key {
			return d
		}
	}
	return nil
}

func (h settingsHandlers) handleSet(ss capability.SettingsService, req jsonrpcRequest) {
	var p settingsSetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	desc := findDescriptorByKey(ss, p.Key)
	if desc == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Unknown setting: " + p.Key})
		return
	}
	if desc.Control() == settings.ControlSecret {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Secret settings must use settings.secretSet"})
		return
	}

	var setErr error
	switch desc.Control() {
	case settings.ControlToggle:
		var b bool
		if err := json.Unmarshal(p.Value, &b); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid value: expected boolean"})
			return
		}
		bk, ok := desc.(*settings.Bool)
		if !ok {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Setting " + p.Key + " is declared as a toggle but is not a Bool key"})
			return
		}
		setErr = ss.SetBool(bk, b)
	case settings.ControlText:
		var str string
		if err := json.Unmarshal(p.Value, &str); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid value: expected string"})
			return
		}
		sk, ok := desc.(*settings.String)
		if !ok {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Setting " + p.Key + " is declared as text but is not a String key"})
			return
		}
		setErr = ss.SetString(sk, str)
	case settings.ControlNumber:
		var n float64
		if err := json.Unmarshal(p.Value, &n); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid value: expected number"})
			return
		}
		nk, ok := desc.(*settings.Number)
		if !ok {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Setting " + p.Key + " is declared as a number but is not a Number key"})
			return
		}
		setErr = ss.SetNumber(nk, n)
	case settings.ControlSelect:
		var str string
		if err := json.Unmarshal(p.Value, &str); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid value: expected string"})
			return
		}
		sk, ok := desc.(*settings.Select)
		if !ok {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Setting " + p.Key + " is declared as a select but is not a Select key"})
			return
		}
		setErr = ss.SetSelect(sk, str)
	case settings.ControlPaths:
		var paths []string
		if err := json.Unmarshal(p.Value, &paths); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid value: expected array of strings"})
			return
		}
		pk, ok := desc.(*settings.PathList)
		if !ok {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Setting " + p.Key + " is declared as paths but is not a PathList key"})
			return
		}
		setErr = ss.SetPaths(pk, paths)
	}

	if setErr != nil {
		if errors.Is(setErr, settings.ErrValidation) {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: setErr.Error()})
			return
		}
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: setErr.Error()})
		return
	}

	_ = h.r.TryResult(req.ID, mustMarshal(map[string]bool{"ok": true}))
}

// tabbyHandlers answers the Tabby import methods. The parse/plan logic stays
// in the handler (it owns the Tabby YAML grammar); the TabbyImportService is
// the only store access (migration map). configWired controls the
// "profiles not available" refusal; executeWired the "import not available"
// refusal; storeWired the "credential store not available" refusal — all
// three are the old handler's nil-checks, preserved as construction facts.
type tabbyHandlers struct {
	op           capability.TabbyImportOperation
	configWired  bool // profiles + groups wired
	executeWired bool // + credential store + profile service wired
	storeWired   bool // credential store wired (secrets can be minted)
	plans        tabbyPlanStore
	providerName func(context.Context) string // transport-owned answer to "which store would hold imported secrets"
	log          log.Logger
	r            Responder
}

func (h settingsHandlers) handleReset(ss capability.SettingsService, req jsonrpcRequest) {
	var p settingsResetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	desc := findDescriptorByKey(ss, p.Key)
	if desc == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Unknown setting: " + p.Key})
		return
	}

	if err := ss.Reset(desc); err != nil {
		if errors.Is(err, settings.ErrValidation) {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: err.Error()})
			return
		}
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "settings.reset: " + err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(map[string]bool{"ok": true}))
}

func (h settingsHandlers) handleSecretSet(ss capability.SettingsService, req jsonrpcRequest) {
	var p settingsSecretSetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	desc := findDescriptorByKey(ss, p.Key)
	if desc == nil || desc.Control() != settings.ControlSecret {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Not a secret setting: " + p.Key})
		return
	}

	sk, ok := desc.(*settings.Secret)
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Setting " + p.Key + " is declared as secret but is not a Secret key"})
		return
	}
	if err := ss.SecretSet(sk, p.Value); err != nil {
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "settings.secretSet: ", err))
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(map[string]bool{"ok": true}))
}

func (h settingsHandlers) handleSecretDelete(ss capability.SettingsService, req jsonrpcRequest) {
	var p settingsSecretDeleteParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	desc := findDescriptorByKey(ss, p.Key)
	if desc == nil || desc.Control() != settings.ControlSecret {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Not a secret setting: " + p.Key})
		return
	}

	sk, ok := desc.(*settings.Secret)
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Setting " + p.Key + " is declared as secret but is not a Secret key"})
		return
	}
	if err := ss.SecretDelete(sk); err != nil {
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "settings.secretDelete: ", err))
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(map[string]bool{"ok": true}))
}

func (h settingsHandlers) handleSecretExists(ss capability.SettingsService, req jsonrpcRequest) {
	var p settingsSecretExistsParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	desc := findDescriptorByKey(ss, p.Key)
	if desc == nil || desc.Control() != settings.ControlSecret {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Not a secret setting: " + p.Key})
		return
	}

	sk, ok := desc.(*settings.Secret)
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Setting " + p.Key + " is declared as secret but is not a Secret key"})
		return
	}
	exists, err := ss.SecretExists(sk)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "settings.secretExists: " + err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(map[string]bool{"exists": exists}))
}

// tabbyPlanStore is the transport-owned one-time import plan store: plans
// are decrypted server-side and never reach the renderer; the handler gets
// exactly the four plan-lifecycle operations, nothing else.
type tabbyPlanStore interface {
	storePlan(plan *importPlan) (string, error)
	claimPlan(token string) *importPlan
	releasePlan(token string)
	finishPlan(token string)
}

// handleTabbyPreview parses a Tabby config and returns a preview of what
// would be imported, without writing anything. Uses planTabbyImport for the
// shared planning logic.
func (h tabbyHandlers) handleTabbyPreview(ctx context.Context, req jsonrpcRequest) {
	if !h.configWired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "profiles not available"})
		return
	}
	var params struct {
		Config     string `json:"config"`
		Passphrase string `json:"passphrase,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Config == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: config (YAML string) required"})
		return
	}

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.TabbyImportService) error {
		plan, preview, err := h.planTabbyImport(ctx, svc, params.Config, params.Passphrase)
		if err != nil {
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "Tabby preview: ", err))
			return nil
		}
		_ = plan // stored server-side by preview.PlanToken
		_ = h.r.TryResult(req.ID, mustMarshal(preview))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleTabbyExecute executes a previously previewed Tabby import plan.
// Takes the plan token from the preview response.
func (h tabbyHandlers) handleTabbyExecute(ctx context.Context, req jsonrpcRequest) {
	if !h.executeWired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "import not available"})
		return
	}
	var params tabbyExecuteParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.PlanToken == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: planToken required"})
		return
	}

	// Claim the plan so concurrent calls for the same token are rejected.
	plan := h.plans.claimPlan(params.PlanToken)
	if plan == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Plan not found, expired, or already in progress. Please preview again."})
		return
	}

	// On any failure, release the plan for retry (vault setup/unlock flow),
	// and release it BEFORE the caller is told the attempt failed. The retry
	// is sent the instant the renderer reads the error — that is what this
	// method exists for: unlock the vault, send the same token again — and it
	// arrives on its own goroutine, so it races the tail of this one. A claim
	// still standing when it lands refuses it with "Please preview again",
	// which throws away the decrypted plan and makes the user re-enter the
	// tabby passphrase, for no reason but that the machine was busy.
	//
	// The release used to be deferred, which put it after the response AND
	// after h.op.Run had already dropped the operation's own gate, so nothing
	// held the retry back over exactly the window in which the retry could
	// not be served. It reported as an intermittent import of zero profiles
	// in TestTabbyExecute_VaultRetry — one of the names nocx-2h08 rotates
	// through. release is idempotent, and the defer stays as the guard for
	// the paths that return without answering.
	var settled bool
	release := func() {
		if !settled {
			settled = true
			h.plans.releasePlan(params.PlanToken)
		}
	}
	defer release()

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.TabbyImportService) error {
		// Mint every secret, binding each password onto the profile whose
		// options match the target the tabby vault keyed it to (ADR-0017 §1).
		// Passphrases are minted as unbound rows: a passphrase belongs to a
		// private key the import cannot fingerprint, and the connection
		// editor binds it.
		for _, cp := range plan.creds {
			kind := vault.KindPassword
			if cp.isPassphrase {
				kind = vault.KindKeyPassphrase
			}
			secretID, err := svc.CreateSecret(ctx, credential.NewSecret(cp.secret),
				vault.SecretMeta{Name: cp.name, Kind: kind})
			if err != nil {
				release()
				_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "Store secret: ", err))
				return nil
			}
			if cp.isPassphrase {
				continue
			}
			for i := range plan.profiles {
				o := &plan.profiles[i].Options
				port := 0
				if o.Port != nil {
					port = *o.Port
				}
				user := ""
				if o.User != nil {
					user = *o.User
				}
				if user == cp.targetUser && o.Host == cp.targetHost && port == cp.targetPort {
					o.PasswordSecret = string(secretID)
					break
				}
			}
		}

		// No credential records are imported: the bindings live on the profiles.
		result := svc.AtomicImport(plan.profiles, plan.groups)
		if len(result.ImportErrors) > 0 {
			release()
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Import failed: " + result.ImportErrors[0]})
			return nil
		}

		// All writes succeeded — remove the plan permanently, before the
		// result, so a client that fires the same token again on seeing the
		// success is refused by a store that no longer holds it rather than
		// by a claim that is about to be released.
		h.plans.finishPlan(params.PlanToken)
		settled = true
		_ = h.r.TryResult(req.ID, mustMarshal(result))
		return nil
	})
	if err != nil {
		release()
		answerOperationRefusal(h.r, req, err)
	}
}

// handleImportTabby is the one-shot import: parse, decrypt, mint every
// secret, then atomically import the profiles and groups.
func (h tabbyHandlers) handleImportTabby(ctx context.Context, req jsonrpcRequest) {
	if !h.configWired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "profiles not available"})
		return
	}
	var params struct {
		Config     string `json:"config"`
		Passphrase string `json:"passphrase,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Config == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: config (YAML string) required"})
		return
	}

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.TabbyImportService) error {
		h.doImportTabby(ctx, svc, params, req)
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// doImportTabby is the shared import body; it responds to req on every path.
func (h tabbyHandlers) doImportTabby(ctx context.Context, svc capability.TabbyImportService, params struct {
	Config     string `json:"config"`
	Passphrase string `json:"passphrase,omitempty"`
}, req jsonrpcRequest,
) {
	cfg, err := importer.ParseTabbyConfig([]byte(params.Config))
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Parse Tabby config: " + err.Error()})
		return
	}

	// Decrypt vault and build credentials + profile matching.
	// Profiles carry their secret bindings directly (ADR-0017): the minted
	// password reference goes into the profile's own options, matched by the
	// connection target the tabby vault keyed it to.
	type pwKey struct {
		user, host string
		port       int
	}
	pwLookup := make(map[pwKey]credential.SecretID)

	if cfg.Vault != nil && cfg.Vault.Encrypted {
		if !h.storeWired {
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "Store secret: ", errors.New("credential store not available")))
			return
		}
		if params.Passphrase == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Vault is encrypted: passphrase required"})
			return
		}
		vaultContents, err := importer.DecryptTabbyVault(cfg.Vault, params.Passphrase)
		if err != nil {
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "Decrypt vault: ", err))
			return
		}

		// Plan every secret before creating any, so a shape we cannot read
		// never leaves an orphaned secret behind.
		//
		// A secret we cannot interpret is SKIPPED, never fatal. Tabby's vault
		// is shared by every plugin the user has installed, so an unknown type
		// is normal rather than exceptional — and aborting on one would throw
		// away the profiles and groups that imported fine. The shapes below are
		// verified against tabby-ssh/src/services/passwordStorage.service.ts.
		type secretPlan struct {
			ts        importer.TabbySecret
			val       string
			keyName   string // private-key identifier (key-passphrase)
			keyTarget *pwKey // connection target (password)
		}
		plans := make([]secretPlan, 0, len(vaultContents.DecodedSecrets()))
		skipped := 0
		for _, sec := range vaultContents.DecodedSecrets() {
			var val string
			if err := json.Unmarshal(sec.Value, &val); err != nil || val == "" {
				h.log.Warn("tabby import: skipping secret with unreadable value", "type", sec.Type)
				skipped++
				continue
			}
			switch sec.Type {
			case "ssh:password":
				// getVaultKeyForConnection → {user, host, port}
				var t struct {
					User string `json:"user"`
					Host string `json:"host"`
					Port int    `json:"port"`
				}
				if err := json.Unmarshal(sec.Key, &t); err != nil || t.Host == "" {
					h.log.Warn("tabby import: skipping password secret with unreadable key")
					skipped++
					continue
				}
				plans = append(plans, secretPlan{
					ts:        sec,
					val:       val,
					keyTarget: &pwKey{user: t.User, host: t.Host, port: t.Port},
				})
			case "ssh:key-passphrase":
				// getVaultKeyForPrivateKey → {hash: id}. It is an object, not a
				// string: reading it as a string failed for every real Tabby
				// vault and, before this, aborted the whole import.
				var k struct {
					Hash string `json:"hash"`
				}
				if err := json.Unmarshal(sec.Key, &k); err != nil || k.Hash == "" {
					h.log.Warn("tabby import: skipping key-passphrase secret with unreadable key")
					skipped++
					continue
				}
				plans = append(plans, secretPlan{ts: sec, val: val, keyName: privateKeyLabel(k.Hash)})
			default:
				// Everything else, including Tabby's "file" secrets. Those hold
				// base64 file CONTENT — usually a private key — which is not a
				// credential secret and does not belong in a password slot.
				// Importing key material is its own feature, not a side effect
				// of this one.
				h.log.Info("tabby import: skipping secret of unhandled type", "type", sec.Type)
				skipped++
			}
		}
		if skipped > 0 {
			h.log.Info("tabby import: some vault secrets were not imported", "skipped", skipped, "imported", len(plans))
		}

		// All secrets validated. Create each one in the SecretStore, carrying
		// the name the credential will bear (ADR-0016: the secret owns its
		// name, and an import mints both together).
		for _, p := range plans {
			name := p.keyName
			kind := vault.KindKeyPassphrase
			if p.ts.Type == "ssh:password" {
				name = p.keyTarget.user + "@" + p.keyTarget.host
				kind = vault.KindPassword
			}
			secretID, err := svc.CreateSecret(ctx, credential.NewSecret(p.val),
				vault.SecretMeta{Name: name, Kind: kind})
			if err != nil {
				_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "Store secret: ", err))
				return
			}
			switch p.ts.Type {
			case "ssh:password":
				// The secret is bound to the connection it belongs to; no
				// credential record is minted (ADR-0017 §1).
				pwLookup[*p.keyTarget] = secretID
			case "ssh:key-passphrase":
				// Passphrases stay unbound rows: a passphrase belongs to a
				// private key, and the imported key is a path whose
				// fingerprint is not readable at import time. The connection
				// editor's secret picker binds it where the user chooses.
				_ = p.keyName
			}
		}
	}

	// Domain service path: atomic import.
	var profiles []profile.SSHProfile
	for _, tp := range cfg.Profiles {
		if tp.Type != "ssh" {
			continue
		}
		p := importer.ConvertProfile(tp)
		if p.Options.User != nil && p.Options.Host != "" {
			port := 0
			if p.Options.Port != nil {
				port = *p.Options.Port
			}
			user := ""
			if p.Options.User != nil {
				user = *p.Options.User
			}
			if secretID, ok := pwLookup[pwKey{user: user, host: p.Options.Host, port: port}]; ok {
				p.Options.PasswordSecret = string(secretID)
			}
		}
		profiles = append(profiles, p)
	}

	var groups []profile.ProfileGroup
	for _, tg := range cfg.Groups {
		var defaults *profile.ProfileDefaults
		if tg.Defaults != nil {
			d, err := profile.DecodeDefaults(tg.Defaults)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprintf("Import failed: group %q defaults: %v", tg.Name, err)})
				return
			}
			defaults = &d
		}
		groups = append(groups, profile.ProfileGroup{
			ID:            tg.ID,
			ParentGroupID: tg.ParentGroupID,
			Name:          tg.Name,
			Icon:          tg.Icon,
			Color:         tg.Color,
			Defaults:      defaults,
			Editable:      true,
		})
	}

	result := svc.AtomicImport(profiles, groups)
	if len(result.ImportErrors) > 0 {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Import failed: " + result.ImportErrors[0]})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(result.ProfilesImported))
}

// planTabbyImport parses a Tabby config, decrypts its vault (if passphrase
// supplied), and plans every profile, group, and secret WITHOUT writing
// anything. Returns the full importPlan for execution and a preview response
// for the renderer. The plan is stored server-side by the returned token.
// The only store access is through the TabbyImportService (the collision
// reads); everything else is pure parsing.
func (h tabbyHandlers) planTabbyImport(ctx context.Context, svc capability.TabbyImportService, configYAML, passphrase string) (*importPlan, *TabbyPreviewResponse, error) {
	cfg, err := importer.ParseTabbyConfig([]byte(configYAML))
	if err != nil {
		return nil, nil, err
	}

	// Decrypt vault and build secret plans. Each password plan carries
	// the connection target the tabby vault keyed it to, so execution can
	// bind the minted secret onto the right profile (ADR-0017 §1).
	var credentials []credentialPlan
	skipped := make([]SkippedInfo, 0)

	if cfg.Vault != nil && cfg.Vault.Encrypted {
		if passphrase == "" {
			return nil, nil, errors.New("vault is encrypted: passphrase required")
		}
		vaultContents, decryptErr := importer.DecryptTabbyVault(cfg.Vault, passphrase)
		if decryptErr != nil {
			return nil, nil, decryptErr
		}

		for _, sec := range vaultContents.DecodedSecrets() {
			var val string
			umErr := json.Unmarshal(sec.Value, &val)
			if umErr != nil || val == "" {
				skipped = append(skipped, SkippedInfo{
					SecretType: sec.Type,
					Reason:     "unreadable value",
				})
				continue
			}
			switch sec.Type {
			case "ssh:password":
				var t struct {
					User string `json:"user"`
					Host string `json:"host"`
					Port int    `json:"port"`
				}
				umErr = json.Unmarshal(sec.Key, &t)
				if umErr != nil || t.Host == "" {
					skipped = append(skipped, SkippedInfo{
						SecretType: sec.Type,
						Reason:     "unreadable key (missing host)",
					})
					continue
				}
				name := t.User + "@" + t.Host
				credentials = append(credentials, credentialPlan{
					name:       name,
					secret:     val,
					targetUser: t.User,
					targetHost: t.Host,
					targetPort: t.Port,
				})

			case "ssh:key-passphrase":
				var k struct {
					Hash string `json:"hash"`
				}
				umErr = json.Unmarshal(sec.Key, &k)
				if umErr != nil || k.Hash == "" {
					skipped = append(skipped, SkippedInfo{
						SecretType: sec.Type,
						Reason:     "unreadable key (missing hash)",
					})
					continue
				}
				keyName := privateKeyLabel(k.Hash)
				credentials = append(credentials, credentialPlan{
					name:         keyName,
					secret:       val,
					isPassphrase: true,
				})

			default:
				skipped = append(skipped, SkippedInfo{
					SecretType: sec.Type,
					Reason:     "unhandled secret type",
				})
			}
		}
	}

	// Convert profiles.
	var profiles []profile.SSHProfile
	for _, tp := range cfg.Profiles {
		if tp.Type != "ssh" {
			continue
		}
		p := importer.ConvertProfile(tp)
		// Profiles no longer link to credentials (ADR-0017): a profile's
		// secret references are backend-owned, and an import brings none.
		profiles = append(profiles, p)
	}

	// Convert groups.
	var groups []profile.ProfileGroup
	for _, tg := range cfg.Groups {
		var defaults *profile.ProfileDefaults
		if tg.Defaults != nil {
			d, decodeErr := profile.DecodeDefaults(tg.Defaults)
			if decodeErr != nil {
				return nil, nil, fmt.Errorf("group %q defaults: %w", tg.Name, decodeErr)
			}
			defaults = &d
		}
		groups = append(groups, profile.ProfileGroup{
			ID:            tg.ID,
			ParentGroupID: tg.ParentGroupID,
			Name:          tg.Name,
			Icon:          tg.Icon,
			Color:         tg.Color,
			Defaults:      defaults,
			Editable:      true,
		})
	}

	// Build per-entry preview lists.
	profileEntries := make([]ProfileEntry, 0, len(profiles))
	groupNames := make([]string, 0, len(groups))
	secretEntries := make([]SecretEntry, 0, len(credentials))

	// Determine which profiles collide (for setting their action).
	existingProfileIDs := make(map[string]bool)
	existingProfs, _ := svc.ListProfiles()
	for _, p := range existingProfs {
		existingProfileIDs[p.ID] = true
	}
	for _, p := range profiles {
		action := "new"
		if p.ID != "" && existingProfileIDs[p.ID] {
			action = "overwrite"
		}
		// No import-time credential linking remains (ADR-0017): a profile's
		// secret references are backend-owned and imports carry none.
		profileEntries = append(profileEntries, ProfileEntry{Name: p.Name, Action: action})
	}
	for _, g := range groups {
		groupNames = append(groupNames, g.Name)
	}
	for _, cp := range credentials {
		typ := "password"
		if cp.isPassphrase {
			typ = "passphrase"
		}
		secretEntries = append(secretEntries, SecretEntry{Name: cp.name, Type: typ})
	}

	// Build preview response with collision info.
	preview := &TabbyPreviewResponse{
		ProfilesToImport: len(profiles),
		GroupsToImport:   len(groups),
		SecretsToImport:  len(credentials),
		ProfileEntries:   profileEntries,
		GroupNames:       groupNames,
		SecretEntries:    secretEntries,
		SkippedSecrets:   skipped,
	}

	// Detect collisions by checking against current store state.
	existingIDs := make(map[string]bool, len(existingProfs))
	for _, p := range existingProfs {
		existingIDs[p.ID] = true
	}
	for _, p := range profiles {
		if p.ID != "" && existingIDs[p.ID] {
			preview.Collisions = append(preview.Collisions, CollisionInfo{
				Kind:   "profile",
				Name:   p.Name,
				Policy: "overwrite",
			})
		}
	}

	existingGroups, _ := svc.ListGroups()
	existingGIDs := make(map[string]bool, len(existingGroups))
	for _, g := range existingGroups {
		existingGIDs[g.ID] = true
	}
	for _, g := range groups {
		if g.ID != "" && existingGIDs[g.ID] {
			preview.Collisions = append(preview.Collisions, CollisionInfo{
				Kind:   "group",
				Name:   g.Name,
				Policy: "overwrite",
			})
		}
	}

	// Determine secret provider.
	preview.SecretProvider = h.providerName(ctx)

	// Build the plan and store it.
	plan := &importPlan{
		profiles: profiles,
		groups:   groups,
		creds:    credentials,
	}
	token, err := h.plans.storePlan(plan)
	if err != nil {
		return nil, nil, fmt.Errorf("store plan: %w", err)
	}
	preview.PlanToken = token

	return plan, preview, nil
}

// buildConfigOp constructs the ONE config-domain operation (AD-8: one owner
// of endpoint resolution — agent.ask's refusal check shares it with the
// config handlers; a second construction would be a second wiring). It
// returns whether the endpoint repository is wired, the "endpoints not
// available" gate the handlers check first.
func (s *WSServer) buildConfigOp(lane, configGate, vaultGate control.Admission) (capability.ConfigOperation, bool) {
	// Endpoints ride the profile store (ADR-0030): the same JSON document,
	// so the profile store satisfies the endpoint repository. The nil guard
	// is real: the type assertion panics on a nil interface, and profiles
	// may simply not be wired.
	var endpointsRepo profile.EndpointRepository
	if s.profiles != nil {
		if er, ok := s.profiles.(profile.EndpointRepository); ok {
			endpointsRepo = er
		}
	}
	return capability.NewConfigOperation(
		configGate, vaultGate, lane,
		s.profiles, s.groups, endpointsRepo, s.profileSvc, s.settings,
		s.vaultRowResolver(), s.vaultEndpointSecrets(),
	), endpointsRepo != nil
}

// configSpecs declares the config-domain control methods. The ConfigOperation
// is built ONCE by buildConfigOp (in buildControlPlane) and shared with the
// agent specs; the handler families receive it.
func (s *WSServer) configSpecs(lane control.Admission, configGate, vaultGate control.Admission, configOp capability.ConfigOperation, endpointWired bool) []methodSpec {
	profilesWired := s.profiles != nil
	groupsWired := s.groups != nil
	settingsWired := s.settings != nil
	snippetWired := s.snippets != nil
	executeWired := profilesWired && groupsWired && s.credentials != nil && s.profileSvc != nil

	snippetOp := capability.NewSnippetOperation(configGate, lane, s.snippets)
	noteWired := s.notes != nil
	noteOp := capability.NewNoteOperation(configGate, lane, s.notes)
	uiStateWired := s.uiState != nil
	uiStateOp := capability.NewUIStateOperation(configGate, lane, s.uiState)
	var tabbyOp capability.TabbyImportOperation
	if profilesWired || groupsWired || s.credentials != nil {
		tabbyOp = capability.NewTabbyImportOperation(
			configGate, vaultGate, lane,
			s.profiles, s.groups, s.profileSvc,
			s.vaultSecretSeam(), s.credentials,
		)
	}
	configSub := s.operationQueue("config")
	tabbySub := s.operationQueue("tabby")

	specs := []methodSpec{
		regResponder(configSub, "profiles.list", noParams(), func(r Responder) handlerFunc {
			h := profileHandlers{op: configOp, wired: profilesWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "profiles.create", params(validateProfileCreateRaw), func(r Responder) handlerFunc {
			h := profileHandlers{op: configOp, wired: profilesWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "profiles.update", params(validateProfileUpdateRaw), func(r Responder) handlerFunc {
			h := profileHandlers{op: configOp, wired: profilesWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "profiles.delete", params(validateProfileDeleteRaw), func(r Responder) handlerFunc {
			h := profileHandlers{op: configOp, wired: profilesWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "profiles.effective", params(validateEffectiveRaw), func(r Responder) handlerFunc {
			h := profileHandlers{op: configOp, wired: profilesWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "profiles.patch", params(validatePatchRaw), func(r Responder) handlerFunc {
			h := profileHandlers{op: configOp, wired: profilesWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "endpoints.list", noParams(), func(r Responder) handlerFunc {
			h := endpointHandlers{op: configOp, wired: endpointWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "endpoints.create", params(validateEndpointCreateRaw), func(r Responder) handlerFunc {
			h := endpointHandlers{op: configOp, wired: endpointWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "endpoints.update", params(validateEndpointUpdateRaw), func(r Responder) handlerFunc {
			h := endpointHandlers{op: configOp, wired: endpointWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "endpoints.delete", params(validateEndpointDeleteRaw), func(r Responder) handlerFunc {
			h := endpointHandlers{op: configOp, wired: endpointWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		// The assistant's methods (nocx-edio): endpoints.probe is the Test
		// button — a streaming probe that can take tens of seconds, so it
		// owns a capacity-one admission off the read loop exactly like
		// connections.test; agent.status is a fast config read under the
		// config queue.
		regResponder(s.agentProbeSub, "endpoints.probe", params(validateProbeParamsRaw), func(r Responder) handlerFunc {
			// op + secrets are the credential resolution (nocx-reu5): the
			// probe names a saved endpoint and the backend resolves the
			// credential it owns — the same seams agent.status holds.
			h := assistantProbeHandlers{
				op: configOp, secrets: s.credentialResolver(),
				client: s.assistantClient, probes: s.assistantProbes,
				wired: s.assistantClient != nil, r: r,
			}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleEndpointProbe(ctx, req) }
		}),
		regResponder(configSub, "agent.status", noParams(), func(r Responder) handlerFunc {
			h := assistantStatusHandlers{op: configOp, secrets: s.credentialResolver(), probes: s.assistantProbes, wired: endpointWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleAgentStatus(ctx, req) }
		}),
		regResponder(configSub, "groups.list", noParams(), func(r Responder) handlerFunc {
			h := groupHandlers{op: configOp, wired: groupsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "groups.create", params(validateGroupCreateRaw), func(r Responder) handlerFunc {
			h := groupHandlers{op: configOp, wired: groupsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "groups.update", params(validateGroupUpdateRaw), func(r Responder) handlerFunc {
			h := groupHandlers{op: configOp, wired: groupsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "groups.delete", params(validateGroupDeleteRaw), func(r Responder) handlerFunc {
			h := groupHandlers{op: configOp, wired: groupsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "groups.impact", params(validateGroupImpactRaw), func(r Responder) handlerFunc {
			h := groupHandlers{op: configOp, wired: groupsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleGroupImpact(ctx, req) }
		}),
		regResponder(configSub, "profiles.moveImpact", params(validateProfileMoveImpactRaw), func(r Responder) handlerFunc {
			h := groupHandlers{op: configOp, wired: groupsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleProfileMoveImpact(ctx, req) }
		}),
		regResponder(configSub, "groups.apply", params(validateGroupApplyRaw), func(r Responder) handlerFunc {
			h := groupHandlers{op: configOp, wired: groupsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleGroupApply(ctx, req) }
		}),
		regResponder(configSub, "settings.describe", noParams(), func(r Responder) handlerFunc {
			h := settingsHandlers{op: configOp, wired: settingsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "settings.getSnapshot", noParams(), func(r Responder) handlerFunc {
			h := settingsHandlers{op: configOp, wired: settingsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "settings.set", params(validateSettingsSetRaw), func(r Responder) handlerFunc {
			h := settingsHandlers{op: configOp, wired: settingsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "settings.reset", params(validateSettingsKeyRaw), func(r Responder) handlerFunc {
			h := settingsHandlers{op: configOp, wired: settingsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "settings.secretSet", params(validateSettingsSecretSetRaw), func(r Responder) handlerFunc {
			h := settingsHandlers{op: configOp, wired: settingsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "settings.secretDelete", params(validateSettingsKeyRaw), func(r Responder) handlerFunc {
			h := settingsHandlers{op: configOp, wired: settingsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "settings.secretExists", params(validateSettingsKeyRaw), func(r Responder) handlerFunc {
			h := settingsHandlers{op: configOp, wired: settingsWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "snippets.list", noParams(), func(r Responder) handlerFunc {
			h := snippetHandlers{op: snippetOp, wired: snippetWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "snippets.create", params(validateSnippetCreateRaw), func(r Responder) handlerFunc {
			h := snippetHandlers{op: snippetOp, wired: snippetWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "snippets.update", params(validateSnippetUpdateRaw), func(r Responder) handlerFunc {
			h := snippetHandlers{op: snippetOp, wired: snippetWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "snippets.delete", params(validateSnippetDeleteRaw), func(r Responder) handlerFunc {
			h := snippetHandlers{op: snippetOp, wired: snippetWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "snippets.reorder", params(validateSnippetReorderRaw), func(r Responder) handlerFunc {
			h := snippetHandlers{op: snippetOp, wired: snippetWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "notes.list", noParams(), func(r Responder) handlerFunc {
			h := noteHandlers{op: noteOp, wired: noteWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "notes.get", params(validateNoteIDRaw), func(r Responder) handlerFunc {
			h := noteHandlers{op: noteOp, wired: noteWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "notes.create", params(validateNoteCreateRaw), func(r Responder) handlerFunc {
			h := noteHandlers{op: noteOp, wired: noteWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "notes.update", params(validateNoteUpdateRaw), func(r Responder) handlerFunc {
			h := noteHandlers{op: noteOp, wired: noteWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "notes.delete", params(validateNoteIDRaw), func(r Responder) handlerFunc {
			h := noteHandlers{op: noteOp, wired: noteWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "notes.search", params(validateNoteSearchRaw), func(r Responder) handlerFunc {
			h := noteHandlers{op: noteOp, wired: noteWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "uistate.get", noParams(), func(r Responder) handlerFunc {
			h := uiStateHandlers{op: uiStateOp, wired: uiStateWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(configSub, "uistate.set", params(validateUIStateSetRaw), func(r Responder) handlerFunc {
			h := uiStateHandlers{op: uiStateOp, wired: uiStateWired, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMethod(ctx, req) }
		}),
		regResponder(tabbySub, "profiles.importTabby", params(validateTabbyImportRaw), func(r Responder) handlerFunc {
			h := tabbyHandlers{op: tabbyOp, configWired: profilesWired && groupsWired, executeWired: executeWired, storeWired: s.credentials != nil, plans: s, providerName: s.secretProviderName, log: s.log, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleImportTabby(ctx, req) }
		}),
		regResponder(tabbySub, "profiles.tabbyPreview", params(validateTabbyImportRaw), func(r Responder) handlerFunc {
			h := tabbyHandlers{op: tabbyOp, configWired: profilesWired && groupsWired, executeWired: executeWired, storeWired: s.credentials != nil, plans: s, providerName: s.secretProviderName, log: s.log, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleTabbyPreview(ctx, req) }
		}),
		regResponder(tabbySub, "profiles.tabbyExecute", params(validateTabbyExecuteRaw), func(r Responder) handlerFunc {
			h := tabbyHandlers{op: tabbyOp, configWired: profilesWired && groupsWired, executeWired: executeWired, storeWired: s.credentials != nil, plans: s, providerName: s.secretProviderName, log: s.log, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleTabbyExecute(ctx, req) }
		}),
	}
	return specs
}

// vaultRowResolver returns the RowResolver seam for the config write path —
// the vault's ResolveRow — or nil when no vault is wired (a config write
// carrying a row handle then fails loudly, the documented nil-RowResolver
// contract).
func (s *WSServer) vaultRowResolver() capability.RowResolver {
	if s.vaultLifecycle == nil {
		return nil
	}
	if rr, ok := s.vaultLifecycle.(capability.RowResolver); ok {
		return rr
	}
	return nil
}

// vaultSecretSeam returns the SecretVault seam for the tabby import (the
// vault's catalogue-aware secret surface), or nil when no vault is wired —
// CreateSecret then records namelessly through the plain store, exactly as
// before.
func (s *WSServer) vaultSecretSeam() capability.SecretVault {
	if s.vaultLifecycle == nil {
		return nil
	}
	if sv, ok := s.vaultLifecycle.(capability.SecretVault); ok {
		return sv
	}
	return nil
}

// vaultEndpointSecrets returns the EndpointSecrets seam for the endpoint
// write paths, or nil when no vault is wired — key-bearing endpoint writes
// and material deletes then fail loudly, the documented nil-seam contract.
func (s *WSServer) vaultEndpointSecrets() capability.EndpointSecrets {
	if s.vaultLifecycle == nil {
		return nil
	}
	if es, ok := s.vaultLifecycle.(capability.EndpointSecrets); ok {
		return es
	}
	return nil
}
