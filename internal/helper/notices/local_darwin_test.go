//go:build darwin && nocx_local_ssh

package notices

import (
	"bytes"
	"io/fs"
	"os"
	"testing"
)

func TestLocalDarwinDocumentCarriesPuregoLicense(t *testing.T) {
	document, err := Document()
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := fs.ReadFile(licensesFS, "licenses/"+FileName)
	if err != nil {
		t.Fatal(err)
	}
	license, err := os.ReadFile("purego-LICENSE.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(document, pinned) {
		t.Fatal("local helper lost the pinned archive notices")
	}
	if !bytes.Contains(document[len(pinned):], license) {
		t.Fatal("local helper is missing purego's license")
	}
}
