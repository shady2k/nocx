package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProfileDecoderPreservesStandardCASMetadata(t *testing.T) {
	var doc StandardDocument
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"revision":41,"enabled":true,"readOnlyDirs":["/read"],"readWriteDirs":["/work"]}`), &doc); err != nil {
		t.Fatal(err)
	}
	store := &profileDocStore{}
	if err := store.Write(StandardDocumentName, doc); err != nil {
		t.Fatal(err)
	}
	repo := NewProfileRepository(store, StandardDocumentName, nil)
	if _, err := repo.UpdateStandard(41, false, ProfileRoots{ReadWriteDirs: []string{"/next"}}); err != nil {
		t.Fatalf("decoded revision did not remain authoritative for CAS: %v", err)
	}
	after, err := repo.GetStandard()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 42 || after.Enabled || len(after.ReadWriteDirs) != 1 || after.ReadWriteDirs[0] != "/next" {
		t.Fatal("standard decoder discarded schema/revision/enabled or roots")
	}
}

func TestProfileDecoderRefusesOversizedRootsAndUnknownAuthority(t *testing.T) {
	cases := []string{
		`{"readOnlyDirs":[` + strings.TrimSuffix(strings.Repeat(`"/r",`, MaxProfileRoots+1), ",") + `]}`,
		`{"readWriteDirs":["` + strings.Repeat("x", MaxPathBytes+1) + `"]}`,
		`{"readOnlyDirs":[],"launchId":"cannot-import-authority"}`,
	}
	for i, raw := range cases {
		var roots ProfileRoots
		if err := json.Unmarshal([]byte(raw), &roots); err == nil {
			t.Fatalf("invalid profile case %d accepted", i)
		}
	}
}
