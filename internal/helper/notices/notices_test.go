package notices

import (
	"bytes"
	"io/fs"
	"testing"
)

func TestDocumentPreservesPinnedArchiveNotices(t *testing.T) {
	pinned, err := fs.ReadFile(licensesFS, "licenses/"+FileName)
	if err != nil {
		t.Fatal(err)
	}
	document, err := Document()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(document, append(pinned, supplementaryNotices...)) {
		t.Fatal("document changed the pinned archive notices")
	}
}
