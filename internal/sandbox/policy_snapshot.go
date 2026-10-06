package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

var ErrInvalidPolicy = errors.New("sandbox: invalid policy snapshot")

// EncodePolicy validates and encodes a bounded immutable policy with stable
// root ordering. The digest covers exactly the returned JSON bytes.
func EncodePolicy(policy Policy) ([]byte, string, error) {
	if err := validatePolicyFields(policy); err != nil {
		return nil, "", err
	}
	canonical := policy
	if canonical.Roots == nil {
		canonical.Roots = []Root{}
	}
	less := func(i, j int) bool {
		a, b := canonical.Roots[i], canonical.Roots[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Access != b.Access {
			return a.Access < b.Access
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Provenance != b.Provenance {
			return a.Provenance < b.Provenance
		}
		if a.Identity.Device != b.Identity.Device {
			return a.Identity.Device < b.Identity.Device
		}
		return a.Identity.Inode < b.Identity.Inode
	}
	if !sort.SliceIsSorted(canonical.Roots, less) {
		canonical.Roots = append([]Root{}, policy.Roots...)
		sort.Slice(canonical.Roots, less)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", fmt.Errorf("sandbox: encode policy: %w", err)
	}
	if len(encoded) > MaxPolicyBytes {
		return nil, "", fmt.Errorf("%w: encoded policy exceeds %d bytes", ErrInvalidPolicy, MaxPolicyBytes)
	}
	sum := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(sum[:]), nil
}

// DecodePolicy accepts only this version's complete typed schema and enforces
// the same envelope and enum bounds as EncodePolicy.
func DecodePolicy(encoded []byte) (Policy, error) {
	var policy Policy
	if len(encoded) == 0 || len(encoded) > MaxPolicyBytes {
		return policy, fmt.Errorf("%w: encoded size out of range", ErrInvalidPolicy)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("%w: decode: %v", ErrInvalidPolicy, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Policy{}, fmt.Errorf("%w: trailing JSON data", ErrInvalidPolicy)
	}
	if err := validatePolicyFields(policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func validatePolicyFields(policy Policy) error {
	if policy.Version != PolicyVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidPolicy, policy.Version)
	}
	if policy.Backend != LinuxLandlock && policy.Backend != MacOSSeatbelt {
		return fmt.Errorf("%w: unknown backend", ErrInvalidPolicy)
	}
	if policy.BackendVersion <= 0 || policy.WorkspaceID == "" || len(policy.WorkspaceID) > 256 || policy.WorkspaceRoot == "" || policy.Shell == "" || policy.Runner == "" {
		return fmt.Errorf("%w: required field is empty or over limit", ErrInvalidPolicy)
	}
	if invalidPolicyPath(policy.WorkspaceRoot) || invalidPolicyPath(policy.Shell) || invalidPolicyPath(policy.Runner) || invalidPolicyPath(policy.Runtime.Root) || invalidPolicyPath(policy.Runtime.Home) || invalidPolicyPath(policy.Runtime.Config) || invalidPolicyPath(policy.Runtime.Data) || invalidPolicyPath(policy.Runtime.Cache) || invalidPolicyPath(policy.Runtime.State) || invalidPolicyPath(policy.Runtime.Temp) {
		return fmt.Errorf("%w: path field is over limit or contains NUL", ErrInvalidPolicy)
	}
	if len(policy.Roots) > MaxEffectiveRoots {
		return fmt.Errorf("%w: too many roots", ErrInvalidPolicy)
	}
	for _, root := range policy.Roots {
		if invalidPolicyPath(root.Path) || root.Path == "" || root.Access != ReadOnly && root.Access != ReadWrite || root.Kind != DirectoryRoot && root.Kind != ArtifactRoot && root.Kind != DeviceRoot || !validProvenance(root.Provenance) {
			return fmt.Errorf("%w: invalid root", ErrInvalidPolicy)
		}
	}
	return nil
}

func invalidPolicyPath(value string) bool {
	return len(value) > MaxPathBytes || strings.IndexByte(value, 0) >= 0
}

func validProvenance(value Provenance) bool {
	switch value {
	case WorkspaceRoot, StandardRoot, WorkspaceProfileRoot, LaunchDeltaRoot, GitRoot, RuntimeRoot, SystemRoot, DependencyRoot, TrustedArtifactRoot, WritableDeviceRoot:
		return true
	default:
		return false
	}
}
