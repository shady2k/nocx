package profile

import "testing"

func TestEndpointNeedsCredentialUsesExplicitNoKeyFact(t *testing.T) {
	if !validTestEndpoint().NeedsCredential() {
		t.Fatal("an endpoint without the noKey declaration must need a credential")
	}
	keyless := validTestEndpoint()
	keyless.NoKey = true
	if keyless.NeedsCredential() {
		t.Fatal("an endpoint declaring noKey must not need a credential")
	}
}

func TestValidateEndpoint_RefusesNoKeyWithCredentialReference(t *testing.T) {
	e := validTestEndpoint()
	e.NoKey = true
	e.CredentialRef = "sec:v1:endpoint-key"
	if err := ValidateEndpoint(e); err == nil {
		t.Fatal("an endpoint declaring noKey must not carry a credential reference")
	}
}
