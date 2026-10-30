package update

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/version"
)

// Python's proof preflight consumes these same cases. The updater remains the
// ordering owner; this contract catches drift in either language's predicate.
func TestNightlyOrderingProofContract(t *testing.T) {
	data, err := os.ReadFile("testdata/nightly-ordering.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string `json:"name"`
		Previous struct {
			Version string                `json:"version"`
			Build   version.BuildIdentity `json:"build"`
		} `json:"previous"`
		Candidate Manifest `json:"candidate"`
		Accepted  bool     `json:"accepted"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			u := &updater{currentVersion: tc.Previous.Version, currentBuild: tc.Previous.Build}
			got := IsNewer(tc.Previous.Version, tc.Candidate.Version) || u.newerNightly(ChannelNightly, &tc.Candidate)
			if got != tc.Accepted {
				t.Fatalf("updater accepted=%v, proof contract wants %v", got, tc.Accepted)
			}
		})
	}
}
