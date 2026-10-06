// Package sandbox owns filesystem launch policy and mutable profile configuration.
// A profile is a future-launch default, never authority for an existing process.
package sandbox

const (
	PolicyVersion        = 1
	ProfileSchemaVersion = 1
	MaxProfileRoots      = 32
	MaxEffectiveRoots    = 1024
	MaxPolicyBytes       = 64 << 10
	MaxPathBytes         = 4096
)

type Access string

const (
	ReadOnly  Access = "ro"
	ReadWrite Access = "rw"
)

type Backend string

const (
	LinuxLandlock Backend = "linux-landlock"
	MacOSSeatbelt Backend = "macos-seatbelt"
)

type RootKind string

const (
	DirectoryRoot RootKind = "directory"
	ArtifactRoot  RootKind = "artifact"
	DeviceRoot    RootKind = "device"
)

type Provenance string

const (
	WorkspaceRoot        Provenance = "workspace"
	StandardRoot         Provenance = "standard"
	WorkspaceProfileRoot Provenance = "workspace-profile"
	LaunchDeltaRoot      Provenance = "launch-delta"
	GitRoot              Provenance = "git"
	RuntimeRoot          Provenance = "runtime"
	SystemRoot           Provenance = "system"
	DependencyRoot       Provenance = "dependency"
	TrustedArtifactRoot  Provenance = "trusted-artifact"
	WritableDeviceRoot   Provenance = "writable-device"
)

// FileIdentity binds validation to an object, not a descriptor number or spelling.
type FileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type Root struct {
	Path       string       `json:"path"`
	Access     Access       `json:"access"`
	Kind       RootKind     `json:"kind"`
	Provenance Provenance   `json:"provenance"`
	Identity   FileIdentity `json:"identity"`
}

type ProfileRoots struct {
	ReadOnlyDirs  []string `json:"readOnlyDirs"`
	ReadWriteDirs []string `json:"readWriteDirs"`
}

// WorkspaceProfile keeps a revision even when Override is nil (inheritance).
type WorkspaceProfile struct {
	WorkspaceID string        `json:"workspaceId"`
	Revision    uint64        `json:"revision"`
	Override    *ProfileRoots `json:"override"`
}

type StandardDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Revision      uint64 `json:"revision"`
	Enabled       bool   `json:"enabled"`
	ProfileRoots
}

type RuntimePaths struct {
	Root   string `json:"root"`
	Home   string `json:"home"`
	Config string `json:"config"`
	Data   string `json:"data"`
	Cache  string `json:"cache"`
	State  string `json:"state"`
	Temp   string `json:"temp"`
}

// Policy is an immutable launch snapshot. Its digest is SHA-256 of its bounded
// canonical JSON, kept outside this value so there is no self-referential digest.
// Native descriptor numbers, observations and mutable defaults are not authority.
type Policy struct {
	Version           int          `json:"version"`
	Backend           Backend      `json:"backend"`
	BackendVersion    int          `json:"backendVersion"`
	WorkspaceID       string       `json:"workspaceId"`
	WorkspaceRoot     string       `json:"workspaceRoot"`
	StandardRevision  uint64       `json:"standardRevision"`
	WorkspaceRevision uint64       `json:"workspaceRevision"`
	Shell             string       `json:"shell"`
	Runner            string       `json:"runner"`
	Runtime           RuntimePaths `json:"runtime"`
	Roots             []Root       `json:"roots"`
}
