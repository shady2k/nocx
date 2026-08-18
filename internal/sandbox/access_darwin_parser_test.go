//go:build darwin

package sandbox

import "testing"

func TestParseSeatbeltDenial(t *testing.T) {
	for _, tc := range []struct {
		message string
		access  AccessClass
		path    string
	}{
		{`nocx-sandbox-aabb: Sandbox: cat(123) deny(1) file-read-data /Users/me/private/file.txt`, AccessReadOnly, `/Users/me/private/file.txt`},
		{`Sandbox: python3(44) deny file-write-create /Users/me/output data/result.txt`, AccessReadWrite, `/Users/me/output data/result.txt`},
	} {
		_, _, path, access, ok := parseSeatbeltDenial(tc.message)
		if !ok || path != tc.path || access != tc.access {
			t.Fatalf("parseSeatbeltDenial(%q) = path %q access %q ok %v", tc.message, path, access, ok)
		}
	}
	if _, _, _, _, ok := parseSeatbeltDenial(`unrelated log line`); ok {
		t.Fatal("unrelated log parsed as denial")
	}
}
