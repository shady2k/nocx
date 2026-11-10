package vault

import "testing"

// A VAULT BUILT THE PRODUCTION WAY WRAPS AT THE PRODUCTION COST
// (nocx-ouru5.1.16). The cost is a constant inside this package now: there is
// no constructor argument, no option and no package variable a caller can
// reach, so this is what every shipped vault does — and it is checked here,
// where the parameters live, rather than trusted.
func TestVaultBuiltWithoutOptionsUsesTheProductionKDF(t *testing.T) {
	v, _, _ := testVault(t)
	mustSetup(t, v, "production-cost")

	env := v.doc.Passphrase
	if env == nil {
		t.Fatal("Setup stored no passphrase envelope")
	}
	want := defaultKDFParams()
	if env.Memory != want.MemoryKiB || env.Time != want.Time || env.Threads != want.Threads {
		t.Fatalf("envelope KDF = m=%d KiB t=%d p=%d, want the production m=%d t=%d p=%d",
			env.Memory, env.Time, env.Threads, want.MemoryKiB, want.Time, want.Threads)
	}
}
