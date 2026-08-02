package profile

import (
	"sort"
	"strings"
)

// SecretUsage maps a secret reference to the profiles that resolve to it.
// The reference is backend-owned (ADR-0011 §2): it never crosses the wire —
// the transport resolves the renderer's row handle to a reference, looks up
// the usage, and answers with the profiles alone.
type SecretUsage struct {
	SecretID string
	Profiles []ProfileRef
}

// ProfileRef identifies a profile that uses a secret and how it obtained it.
type ProfileRef struct {
	ProfileID   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	Source      string `json:"source"`              // "profile" or "group"
	GroupID     string `json:"groupId,omitempty"`   // set when source == "group"
	GroupName   string `json:"groupName,omitempty"` // set when source == "group"
}

// ProfileRefSource constants — the closed set of values Source may carry. The
// renderer switches on these, so adding one is a wire-contract change.
const (
	ProfileRefSourceProfile = "profile"
	ProfileRefSourceGroup   = "group"
	ProfileRefSourceGlobal  = "global"
)

// profileRefFields returns pointers to every secret-reference field on a
// resolved options block. One list, used by usage and by the transport's
// inventory inputs, so the two cannot disagree about where a reference may
// live.
func profileRefFields(o *SSHProfileOptions) []*string {
	return []*string{
		&o.PasswordSecret,
		&o.KeySecret,
		&o.KeyPassphraseSecret,
	}
}

// ComputeSecretUsage returns, for every secret reference a profile resolves
// to, the profiles that carry it — directly or through group inheritance.
//
// Resolution goes through ResolveEffectiveProfile, not a field scan. A
// profile that names its own secret does NOT also count against its group's
// secret of the same kind — precedence is already decided by the engine.
//
// A profile is counted once per secret: a profile whose password and key
// happen to be the same secret is one user of it, not two.
func ComputeSecretUsage(
	profiles []SSHProfile,
	groups []ProfileGroup,
	globalDefaults SparseSSHOptions,
) []SecretUsage {
	// Build group lookup by ID for name resolution.
	groupByID := make(map[string]ProfileGroup, len(groups))
	for _, g := range groups {
		groupByID[g.ID] = g
	}

	// usage maps secret reference -> list of ProfileRefs. seen guards the
	// "same secret in two fields" case (password + key are one secret).
	usage := make(map[string][]ProfileRef)
	seen := make(map[string]map[string]bool)

	// Resolve every profile and collect the secret references it resolves to.
	for _, p := range profiles {
		eff, err := ResolveEffectiveProfile(p, groups, globalDefaults)
		if err != nil {
			continue // skip unresolvable profiles
		}

		fields := profileRefFields(&eff.ResolvedOptions)
		names := []string{"passwordSecret", "keySecret", "keyPassphraseSecret"}
		for i, f := range fields {
			ref := *f
			if ref == "" {
				continue
			}
			if seen[ref] == nil {
				seen[ref] = make(map[string]bool)
			}
			if seen[ref][p.ID] {
				continue
			}
			seen[ref][p.ID] = true

			src, hasSrc := eff.Source[names[i]]
			if !hasSrc {
				// A reference can only have landed from a layer that set it:
				// the hardcoded defaults are port, user and
				// behaviorOnSessionEnd, none of which can supply a secret.
				continue
			}

			pref := ProfileRef{
				ProfileID:   p.ID,
				ProfileName: p.Name,
			}
			if string(src) == string(FieldSourceProfile) {
				pref.Source = ProfileRefSourceProfile
			} else if strings.HasPrefix(string(src), "group:") {
				pref.Source = ProfileRefSourceGroup
				gid := strings.TrimPrefix(string(src), "group:")
				pref.GroupID = gid
				if g, ok := groupByID[gid]; ok {
					pref.GroupName = g.Name
				}
			} else if string(src) == string(FieldSourceGlobal) {
				// Global defaults have no store yet (profile.go:245) and the
				// transport passes an empty layer, so this cannot occur in the
				// running product — but the engine supports it and this
				// function takes the layer as a parameter, so dropping it
				// would be a silent undercount the day a store appears. It is
				// a named third source, not an undocumented string leaking
				// into the field the renderer switches on. See nocx-p15s.
				pref.Source = ProfileRefSourceGlobal
			} else {
				// FieldSourceDefault is the only source left, and the
				// hardcoded defaults cannot supply a secret.
				continue
			}

			usage[ref] = append(usage[ref], pref)
		}
	}

	// Convert to sorted slice for deterministic output.
	result := make([]SecretUsage, 0, len(usage))
	for ref, refs := range usage {
		result = append(result, SecretUsage{SecretID: ref, Profiles: refs})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].SecretID < result[j].SecretID
	})
	for i := range result {
		sort.Slice(result[i].Profiles, func(a, b int) bool {
			return result[i].Profiles[a].ProfileID < result[i].Profiles[b].ProfileID
		})
	}

	return result
}
