package sandbox

import (
	"bytes"
	"strings"
	"testing"
)

func policyFixture() Policy {
	return Policy{
		Version: PolicyVersion, Backend: LinuxLandlock, BackendVersion: 9,
		WorkspaceID: "ws", WorkspaceRoot: "/work", StandardRevision: 3, WorkspaceRevision: 5,
		Shell: "/bin/sh", Runner: "/app/nocx-sandbox-runner",
		Roots: []Root{
			{Path: "/work/z", Access: ReadWrite, Kind: DirectoryRoot, Provenance: WorkspaceRoot},
			{Path: "/usr", Access: ReadOnly, Kind: DirectoryRoot, Provenance: SystemRoot},
		},
	}
}

func TestPolicySnapshotIsDeterministicAndRoundTrips(t *testing.T) {
	policy := policyFixture()
	first, digest, err := EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.Roots[0], policy.Roots[1] = policy.Roots[1], policy.Roots[0]
	second, secondDigest, err := EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || digest != secondDigest {
		t.Fatal("equivalent policies did not produce stable bytes and digest")
	}
	decoded, err := DecodePolicy(first)
	if err != nil {
		t.Fatal(err)
	}
	third, thirdDigest, err := EncodePolicy(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, third) || thirdDigest != digest {
		t.Fatal("round-trip changed canonical policy")
	}
}

func TestPolicySnapshotRejectsUnknownShapeVersionAndOversize(t *testing.T) {
	policy := policyFixture()
	policy.Version++
	if _, _, err := EncodePolicy(policy); err == nil {
		t.Fatal("unknown policy version was accepted")
	}
	if _, err := DecodePolicy([]byte(`{"version":1,"unexpected":true}`)); err == nil {
		t.Fatal("unknown policy field was accepted")
	}
	if _, err := DecodePolicy([]byte(strings.Repeat(" ", MaxPolicyBytes+1))); err == nil {
		t.Fatal("oversize policy was accepted")
	}
}
