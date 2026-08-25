package sandbox

import (
	"encoding/json"
	"testing"
)

func TestDecodeGrantPayloadEnvelope(t *testing.T) {
	raw, err := json.Marshal(GrantPayload{
		Realized: &SessionInfo{
			Backend:         BackendLandlock,
			Workspace:       "/home/user/work",
			WritableRoots:   []string{"/home/user/work"},
			ReadOnlyRoots:   []string{"/usr"},
			HomeProjections: []HomeProjection{{HostPath: "/home/user/work", RelativePath: "work"}},
		},
		Provenance: &GrantProvenance{
			WorkspaceID:     "ws-1",
			ProfileSource:   ProfileSourceWorkspace,
			ProfileRevision: new(int64(42)),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	realized, provenance, err := DecodeGrantPayload(raw)
	if err != nil {
		t.Fatalf("DecodeGrantPayload: %v", err)
	}
	if realized == nil || realized.Backend != BackendLandlock || realized.Workspace != "/home/user/work" {
		t.Fatalf("realized = %#v", realized)
	}
	if provenance.WorkspaceID != "ws-1" || provenance.ProfileSource != ProfileSourceWorkspace ||
		provenance.ProfileRevision == nil || *provenance.ProfileRevision != 42 {
		t.Fatalf("provenance = %#v", provenance)
	}
}

func TestDecodeGrantPayloadStandardSourceCarriesSettingsRevision(t *testing.T) {
	raw := []byte(`{"realized":{"backend":"landlock","workspace":"/w","writableRoots":[],"readOnlyRoots":[],"homeProjections":[]},"provenance":{"workspaceId":"ws-1","profileSource":"standard","profileRevision":0}}`)
	realized, provenance, err := DecodeGrantPayload(raw)
	if err != nil {
		t.Fatalf("DecodeGrantPayload: %v", err)
	}
	if provenance.ProfileSource != ProfileSourceStandard ||
		provenance.ProfileRevision == nil || *provenance.ProfileRevision != 0 {
		t.Fatalf("provenance = %#v, want standard + settings revision 0", provenance)
	}
	if realized == nil || realized.Backend != BackendLandlock {
		t.Fatalf("realized = %#v", realized)
	}
}

func TestDecodeGrantPayloadLegacySessionInfo(t *testing.T) {
	raw, err := json.Marshal(SessionInfo{
		Backend:         BackendLandlock,
		Workspace:       "/home/user/work",
		WritableRoots:   []string{"/home/user/work"},
		ReadOnlyRoots:   []string{"/usr"},
		HomeProjections: nil,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	realized, provenance, err := DecodeGrantPayload(raw)
	if err != nil {
		t.Fatalf("DecodeGrantPayload: %v", err)
	}
	if realized == nil || realized.Workspace != "/home/user/work" {
		t.Fatalf("realized = %#v", realized)
	}
	if provenance.ProfileSource != ProfileSourceLegacy || provenance.ProfileRevision != nil {
		t.Fatalf("provenance = %#v, want legacy + null revision", provenance)
	}
}

func TestDecodeGrantPayloadRejectsGarbage(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`[]`),
		[]byte(`{"realized":null}`),
		[]byte(`{"realized":{"backend":"landlock"},"provenance":null}`),
		[]byte(``),
	} {
		if _, _, err := DecodeGrantPayload(raw); err == nil {
			t.Fatalf("DecodeGrantPayload(%s) = nil error, want refusal", raw)
		}
	}
}
