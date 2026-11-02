package signing

import (
	"strings"
	"testing"
)

// The output below is the shape `codesign -d --verbose=4` prints for a signed
// Mach-O on macOS 15. The keys this package does not read are kept on purpose:
// the parser must ignore what it does not own.
const signedHelper = `Executable=/Users/runner/nocx-helper
Identifier=dev.nocx.helper
Format=Mach-O thin (arm64)
CodeDirectory v=20400 size=12345 flags=0x0(none) hashes=385+3 location=embedded
Signature size=8972
Authority=nocx Project Code Signing
Signed Time=Nov  2, 2026 at 10:13:00
Info.plist=not bound
TeamIdentifier=not set
Sealed Resources=none
Internal requirements count=1 size=188
`

// The failing run's helper: Go's linker leaves an ad-hoc signature whose
// identifier comes from the link output's file name.
const adhocHelper = `Executable=/Users/runner/.nocx/helper/abc/nocx-helper
Identifier=a.out
Format=Mach-O thin (arm64)
CodeDirectory v=20400 size=12345 flags=0x0(none) hashes=385+3 location=embedded
Signature=adhoc
Info.plist=not bound
TeamIdentifier=not set
Sealed Resources=none
Internal requirements count=0 size=12
`

func TestParse_ReadsIdentityAuthorityAndAdhoc(t *testing.T) {
	signed := Parse(signedHelper)
	if signed.Identifier != "dev.nocx.helper" {
		t.Fatalf("Identifier = %q", signed.Identifier)
	}
	if signed.Adhoc {
		t.Fatal("a certificate-signed helper parsed as ad-hoc")
	}
	if len(signed.Authority) != 1 || signed.Authority[0] != "nocx Project Code Signing" {
		t.Fatalf("Authority = %v", signed.Authority)
	}

	adhoc := Parse(adhocHelper)
	if !adhoc.Adhoc {
		t.Fatal("Signature=adhoc not read")
	}
	if adhoc.Identifier != "a.out" {
		t.Fatalf("ad-hoc Identifier = %q", adhoc.Identifier)
	}
	if len(adhoc.Authority) != 0 {
		t.Fatalf("ad-hoc Authority = %v, want none", adhoc.Authority)
	}
}

func TestCheck_AcceptsTheStableIdentity(t *testing.T) {
	if err := Check(Parse(signedHelper), HelperIdentifier, Authority); err != nil {
		t.Fatalf("the stable identity was refused: %v", err)
	}
}

func TestCheck_RefusesAdhocWhateverItsIdentifier(t *testing.T) {
	err := Check(Parse(adhocHelper), HelperIdentifier, Authority)
	if err == nil {
		t.Fatal("an ad-hoc helper was accepted")
	}
	if !strings.Contains(err.Error(), "ad-hoc") {
		t.Fatalf("the refusal does not name ad-hoc: %v", err)
	}
}

func TestCheck_RefusesAnotherIdentifier(t *testing.T) {
	err := Check(Parse(strings.Replace(signedHelper, "Identifier=dev.nocx.helper", "Identifier=com.nocx.server", 1)),
		HelperIdentifier, Authority)
	if err == nil {
		t.Fatal("a different identifier was accepted")
	}
	if !strings.Contains(err.Error(), "Identifier") {
		t.Fatalf("the refusal does not name the identifier: %v", err)
	}
}

func TestCheck_RefusesAnotherAuthority(t *testing.T) {
	err := Check(Parse(strings.Replace(signedHelper, "Authority=nocx Project Code Signing",
		"Authority=Somebody Else", 1)), HelperIdentifier, Authority)
	if err == nil {
		t.Fatal("another signer was accepted")
	}
	if !strings.Contains(err.Error(), "Somebody Else") {
		t.Fatalf("the refusal does not name the actual signer: %v", err)
	}
}

func TestCheck_RefusesAnUnsignedExecutable(t *testing.T) {
	// `codesign -d` on an unsigned file exits non-zero, but a caller that
	// reached Check with an empty report must still be refused: no
	// identifier, no authority, no signature is not the stable identity.
	if err := Check(Report{}, HelperIdentifier, Authority); err == nil {
		t.Fatal("an empty report was accepted")
	}
}

func TestCheck_AcceptsTheExpectedAuthorityAmongSeveral(t *testing.T) {
	output := strings.Replace(signedHelper, "Authority=nocx Project Code Signing",
		"Authority=nocx Project Code Signing\nAuthority=Some Intermediate", 1)
	if err := Check(Parse(output), HelperIdentifier, Authority); err != nil {
		t.Fatalf("an expected authority among several was refused: %v", err)
	}
}
