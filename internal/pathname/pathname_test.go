package pathname

import (
	"strings"
	"testing"
)

// The paired success. Every other test in this file is a refusal, and a
// validator that refuses everything passes all of them: this is the one that
// says an ordinary name from an ordinary collection still goes through.
func TestCheckComponent_AcceptsTheNamesPeopleActuallyUse(t *testing.T) {
	for _, name := range []string{
		"users", "create-user.json", "Users", "acme-api", "v2", "_internal",
		"nocx-collection.json", "environments", "list_all", "café", "日本語",
		"api.example.com", "console", "com0", "com10", "lpt", "nul-ish",
		"conf", "auxiliary", strings.Repeat("x", MaxComponentBytes),
	} {
		if err := CheckComponent(name); err != nil {
			t.Errorf("CheckComponent(%q) = %v, want nil — this is a name a user may have", name, err)
		}
	}
}

// The device names. Windows refuses these as a file OR a folder, at any
// extension, in any case: a collection carrying one cannot be checked out
// there at all.
func TestCheckComponent_RefusesWindowsDeviceNames(t *testing.T) {
	for _, name := range []string{
		"con", "CON", "Con", "prn", "aux", "nul", "NUL",
		"com1", "COM9", "lpt1", "LPT9",
		"con.json", "CON.JSON", "aux.tar.gz", "com1.txt",
	} {
		err := CheckComponent(name)
		if err == nil {
			t.Errorf("CheckComponent(%q) = nil — Windows will not create it", name)
			continue
		}
		// A sentence a surface can show, naming the name.
		if !strings.Contains(err.Error(), "Windows") {
			t.Errorf("CheckComponent(%q) = %q, which does not say why", name, err)
		}
	}
}

func TestCheckComponent_RefusesATrailingDotOrSpace(t *testing.T) {
	for _, name := range []string{"docs.", "docs ", "docs. ", "a..", "users\t"} {
		if err := CheckComponent(name); err == nil {
			t.Errorf("CheckComponent(%q) = nil — Windows drops the trailing character rather than keeping it", name)
		}
	}
}

func TestCheckComponent_RefusesCharactersWindowsWillNotTake(t *testing.T) {
	for _, name := range []string{`a:b`, `a<b`, `a>b`, `a"b`, `a|b`, `a?b`, `a*b`, "a\x07b"} {
		if err := CheckComponent(name); err == nil {
			t.Errorf("CheckComponent(%q) = nil — Windows refuses that character in a name", name)
		}
	}
}

func TestCheckComponent_RefusesWhatIsNotOneBoundedComponent(t *testing.T) {
	for _, name := range []string{
		"", ".", "..", "a/b", `a\b`, "us\x00ers", strings.Repeat("x", MaxComponentBytes+1),
	} {
		if err := CheckComponent(name); err == nil {
			t.Errorf("CheckComponent(%q) = nil, want a refusal", name)
		}
	}
}

// The paired success for the path form.
func TestCheckRelPath_AcceptsAnOrdinaryPath(t *testing.T) {
	for _, rel := range []string{
		"req.json", "users/create.json", "environments/dev.json",
		"a/b/c/d/e/f.json", strings.Repeat("ab/", MaxDepth-1) + "x.json",
	} {
		if err := CheckRelPath(rel); err != nil {
			t.Errorf("CheckRelPath(%q) = %v, want nil", rel, err)
		}
	}
}

func TestCheckRelPath_RefusesAPathDeeperThanTheBound(t *testing.T) {
	rel := strings.Repeat("ab/", MaxDepth) + "x.json"
	err := CheckRelPath(rel)
	if err == nil {
		t.Fatalf("CheckRelPath with %d components = nil, want a refusal", MaxDepth+1)
	}
	if !strings.Contains(err.Error(), "32") {
		t.Errorf("the refusal is %q and does not name the bound", err)
	}
}

func TestCheckRelPath_RefusesAPathLongerThanTheBound(t *testing.T) {
	// Four components, each legal on its own, and 400 bytes together.
	rel := strings.Join([]string{
		strings.Repeat("a", 99), strings.Repeat("b", 99),
		strings.Repeat("c", 99), strings.Repeat("d", 94) + ".json",
	}, "/")
	if len(rel) <= MaxRelPathBytes {
		t.Fatalf("the fixture is %d bytes, which does not exceed the %d-byte bound", len(rel), MaxRelPathBytes)
	}
	err := CheckRelPath(rel)
	if err == nil {
		t.Fatalf("CheckRelPath with %d bytes = nil, want a refusal", len(rel))
	}
	if !strings.Contains(err.Error(), "200") {
		t.Errorf("the refusal is %q and does not name the bound", err)
	}
}

func TestCheckRelPath_HoldsEveryComponentToTheNameRule(t *testing.T) {
	for _, rel := range []string{
		"con/req.json", "users/con.json", "docs./req.json", "users/req.json ",
		"", "users//req.json", "/users/req.json", "users/../req.json",
	} {
		if err := CheckRelPath(rel); err == nil {
			t.Errorf("CheckRelPath(%q) = nil, want a refusal", rel)
		}
	}
}

// The minting side. Whatever goes in, what comes out is a name the validator
// in this same file accepts — that is the whole contract, and it is what
// keeps a minter from producing something the store then refuses.
func TestPortable_ProducesOnlyWhatCheckComponentAccepts(t *testing.T) {
	cases := []struct{ in, want string }{
		{"users", "users"},
		{"create-user", "create-user"},
		{"con", "_con"},
		{"CON", "_CON"},
		{"com1", "_com1"},
		{"docs.", "docs"},
		{"docs ", "docs"},
		{"a:b", "ab"},
		{"a<b>c", "abc"},
		{"a\x00b", "ab"},
		{`a\b`, "ab"},
		{"a/b", "ab"},
		{".", ""},
		{"..", ""},
		{"", ""},
		{"...", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		got := Portable(c.in, MaxComponentBytes)
		if got != c.want {
			t.Errorf("Portable(%q) = %q, want %q", c.in, got, c.want)
		}
		if got != "" {
			if err := CheckComponent(got); err != nil {
				t.Errorf("Portable(%q) minted %q, which CheckComponent refuses: %v", c.in, got, err)
			}
		}
	}
}

// A budget smaller than the component limit is what a caller passes when an
// extension and a collision suffix are still to be appended.
func TestPortable_HonoursABudgetAndStaysPortableInside_It(t *testing.T) {
	// 60 runes of Japanese are 180 bytes: a rune bound is not a byte bound,
	// and this is the shape that let a minter produce a name the store
	// refused.
	long := strings.Repeat("あ", 60)
	if len(long) <= MaxComponentBytes {
		t.Fatalf("the fixture is %d bytes and does not need truncating", len(long))
	}
	got := Portable(long, MaxComponentBytes)
	if len(got) > MaxComponentBytes {
		t.Errorf("Portable minted %d bytes, over the %d-byte limit", len(got), MaxComponentBytes)
	}
	if err := CheckComponent(got); err != nil {
		t.Errorf("Portable minted a name CheckComponent refuses: %v", err)
	}
	if !strings.HasPrefix(long, got) {
		t.Errorf("Portable(%q) = %q, which is not a prefix of it — a rune was cut in half", long, got)
	}

	// Truncation can CREATE a reserved name: "console" cut to three bytes is
	// "con". The minted name is still one the validator accepts.
	if got := Portable("console", 3); CheckComponent(got) != nil || len(got) > 3 {
		t.Errorf("Portable(%q, 3) = %q, which is not a legal 3-byte name", "console", got)
	}
}

func TestPortable_LeavesRoomForWhatIsAppended(t *testing.T) {
	const ext = ".json"
	base := Portable(strings.Repeat("x", 500), MaxComponentBytes-len(ext))
	if err := CheckComponent(base + ext); err != nil {
		t.Errorf("a base minted with room for %q became %d bytes with it: %v", ext, len(base+ext), err)
	}
}
