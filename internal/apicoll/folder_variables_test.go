package apicoll

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const folderVariablesJSON = `{"variables":[{"name":"baseUrl","value":"https://folder.example.test","enabled":true}]}`

func folderVariableFile(t *testing.T, root, rel, body string) {
	t.Helper()
	writeFile(t, root, filepath.Join(rel, folderVariablesFileName), body)
}

func TestFolderVariables_ListingNamesFoldersWithVariables(t *testing.T) {
	svc, h, root := openTestCollection(t)
	folderVariableFile(t, root, ".", folderVariablesJSON)
	folderVariableFile(t, root, "users", `{"variables":[{"name":"id","value":"42","enabled":true}]}`)
	writeFile(t, root, "users/get.json", requestJSON("1", "get", "GET", "https://example.test/users/{{id}}"))

	got, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(got.VariableFolders, []string{"", "users"}) {
		t.Fatalf("variable folders = %v, want root and users", got.VariableFolders)
	}
	if len(got.Malformed) != 0 {
		t.Fatalf("malformed = %+v, want no malformed folder variable files", got.Malformed)
	}
}

func TestFolderVariables_TheRequestWinsOverTheNearestFolder(t *testing.T) {
	svc, h, root := openTestCollection(t)
	folderVariableFile(t, root, "users", `{"variables":[{"name":"id","value":"folder","enabled":true}]}`)
	writeFile(t, root, "users/get.json", `{"id":"1","name":"get","method":"GET","url":"https://example.test/{{id}}","variables":[{"name":"id","value":"request","enabled":true}],"body":{"kind":"none"},"auth":{"kind":"none"}}`)

	r, err := svc.ReadRequest(h, "users/get.json")
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	own, err := RequestLookup(r, Environment{Values: map[string]string{"id": "environment"}})
	if err != nil {
		t.Fatalf("RequestLookup: %v", err)
	}
	got, err := Substitute(r, Chain(own))
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://example.test/request" {
		t.Fatalf("url = %q, want the request value", got.URL)
	}
}

func TestFolderVariables_TheNearestFolderWinsOverParentAndEnvironment(t *testing.T) {
	svc, h, root := openTestCollection(t)
	folderVariableFile(t, root, ".", `{"variables":[{"name":"id","value":"root","enabled":true}]}`)
	folderVariableFile(t, root, "users", `{"variables":[{"name":"id","value":"users","enabled":true}]}`)
	folderVariableFile(t, root, "users/private", `{"variables":[{"name":"id","value":"private","enabled":true}]}`)
	writeFile(t, root, "users/private/get.json", requestJSON("1", "get", "GET", "https://example.test/{{id}}"))

	r, err := svc.ReadRequest(h, "users/private/get.json")
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	own, err := RequestLookup(r, Environment{Values: map[string]string{"id": "environment"}})
	if err != nil {
		t.Fatalf("RequestLookup: %v", err)
	}
	got, err := Substitute(r, Chain(own))
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://example.test/private" {
		t.Fatalf("url = %q, want the nearest folder value", got.URL)
	}
}

func TestFolderVariables_ShadowingAnEnvironmentSecretUsesTheRequestRefusal(t *testing.T) {
	svc, h, root := openTestCollection(t)
	folderVariableFile(t, root, "users", `{"variables":[{"name":"token","value":"folder-secret","enabled":true}]}`)
	writeFile(t, root, "users/get.json", requestJSON("1", "get", "GET", "https://example.test/{{token}}"))

	r, err := svc.ReadRequest(h, "users/get.json")
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	_, err = RequestLookup(r, Environment{SecretVars: []string{"token"}})
	if !errors.Is(err, ErrSecretShadowed) {
		t.Fatalf("RequestLookup: %v, want ErrSecretShadowed", err)
	}
	const want = "apicoll: a request variable would shadow a name this environment declares secret: \"token\""
	if err.Error() != want {
		t.Fatalf("shadowing error = %q, want exact sentence %q", err, want)
	}
	if strings.Contains(err.Error(), "folder-secret") {
		t.Fatalf("shadowing error leaked the folder value: %v", err)
	}
}

func TestFolderVariables_ACollectionWithoutTheFileDoesNotWriteDuringListing(t *testing.T) {
	svc, h, root := openTestCollection(t)
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir before List: %v", err)
	}
	if _, listErr := svc.List(h); listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir after List: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("listing changed the collection from %d entries to %d", len(before), len(after))
	}
	if _, err := os.Lstat(filepath.Join(root, folderVariablesFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("listing created %s: %v", folderVariablesFileName, err)
	}
}

func TestFolderVariables_FailureFilesAreReportedAndDoNotResolve(t *testing.T) {
	cases := map[string]string{
		"malformed JSON":         `{"variables":[`,
		"missing variables list": `{}`,
		"name is not a name":     `{"variables":[{"name":"not a name","value":"x","enabled":true}]}`,
		"value is not a string":  `{"variables":[{"name":"id","value":42,"enabled":true}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			svc, h, root := openTestCollection(t)
			folderVariableFile(t, root, "users", body)
			writeFile(t, root, "users/get.json", requestJSON("1", "get", "GET", "https://example.test/{{id}}"))
			listed, err := svc.List(h)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(listed.VariableFolders) != 0 {
				t.Fatalf("variable folders = %v, want no folder with a malformed variables file", listed.VariableFolders)
			}
			if len(listed.Malformed) != 1 || listed.Malformed[0].RelPath != filepath.Join("users", folderVariablesFileName) {
				t.Fatalf("malformed = %+v, want the folder variables file named", listed.Malformed)
			}

			if _, err := svc.ReadRequest(h, "users/get.json"); err == nil {
				t.Fatal("ReadRequest succeeded with a broken folder variables file")
			} else if !errors.Is(err, ErrMalformedFolderVariables) {
				t.Fatalf("ReadRequest error = %v, want ErrMalformedFolderVariables", err)
			}
		})
	}
}

func TestFolderVariables_AnUnreadableFileIsReported(t *testing.T) {
	svc, h, root := openTestCollection(t)
	full := filepath.Join(root, "users", folderVariablesFileName)
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatalf("mkdir unreadable folder variables path: %v", err)
	}
	writeFile(t, root, "users/get.json", requestJSON("1", "get", "GET", "https://example.test/{{id}}"))

	if _, err := svc.ReadRequest(h, "users/get.json"); err == nil {
		t.Fatal("ReadRequest succeeded with an unreadable folder variables file")
	} else if !errors.Is(err, ErrMalformedFolderVariables) {
		t.Fatalf("ReadRequest error = %v, want ErrMalformedFolderVariables", err)
	}
}

func TestFolderVariables_AnOrdinaryFileResolvesOnAnOrdinaryMachine(t *testing.T) {
	svc, h, root := openTestCollection(t)
	folderVariableFile(t, root, "users", `{"variables":[{"name":"id","value":"42","enabled":true}]}`)
	writeFile(t, root, "users/get.json", requestJSON("1", "get", "GET", "https://example.test/{{id}}"))

	r, err := svc.ReadRequest(h, "users/get.json")
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	own, err := RequestLookup(r, Environment{})
	if err != nil {
		t.Fatalf("RequestLookup: %v", err)
	}
	got, err := Substitute(r, Chain(own))
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if got.URL != "https://example.test/42" {
		t.Fatalf("url = %q, want the folder value", got.URL)
	}
}

func TestFolderVariables_ReadWriteAndDeleteRoundTrip(t *testing.T) {
	svc, h, root := openTestCollection(t)
	given := []Param{{Name: "baseUrl", Value: "https://folder.example.test", Enabled: true}}

	got, err := svc.ReadFolderVariables(h, "")
	if err != nil {
		t.Fatalf("ReadFolderVariables absent: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("absent variables = %#v, want empty", got)
	}
	written, err := svc.WriteFolderVariables(h, "", given)
	if err != nil {
		t.Fatalf("WriteFolderVariables: %v", err)
	}
	if !reflect.DeepEqual(written, given) {
		t.Fatalf("written = %#v, want %#v", written, given)
	}
	readBack, err := svc.ReadFolderVariables(h, "")
	if err != nil {
		t.Fatalf("ReadFolderVariables: %v", err)
	}
	if !reflect.DeepEqual(readBack, given) {
		t.Fatalf("read back = %#v, want %#v", readBack, given)
	}
	if _, err := os.Lstat(filepath.Join(root, folderVariablesFileName)); err != nil {
		t.Fatalf("folder variable file was not written: %v", err)
	}

	if deleted, err := svc.WriteFolderVariables(h, "", nil); err != nil {
		t.Fatalf("WriteFolderVariables empty: %v", err)
	} else if len(deleted) != 0 {
		t.Fatalf("deleted result = %#v, want empty", deleted)
	}
	if _, err := svc.ReadFolderVariables(h, ""); err != nil {
		t.Fatalf("ReadFolderVariables after delete: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, folderVariablesFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty write left %s: %v", folderVariablesFileName, err)
	}
}

func TestFolderVariables_WriterRefusesMissingFolder(t *testing.T) {
	svc, h, _ := openTestCollection(t)
	if _, err := svc.ReadFolderVariables(h, "missing"); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("ReadFolderVariables missing = %v, want ErrFolderNotFound", err)
	}
	if _, err := svc.WriteFolderVariables(h, "missing", []Param{{Name: "x", Value: "y", Enabled: true}}); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("WriteFolderVariables missing = %v, want ErrFolderNotFound", err)
	}
}

func TestFolderVariables_WriterRefusesMalformedExistingFile(t *testing.T) {
	svc, h, root := openTestCollection(t)
	folderVariableFile(t, root, ".", `{"variables":[`)
	if _, err := svc.ReadFolderVariables(h, ""); !errors.Is(err, ErrMalformedFolderVariables) {
		t.Fatalf("ReadFolderVariables malformed = %v, want ErrMalformedFolderVariables", err)
	}
	if _, err := svc.WriteFolderVariables(h, "", []Param{{Name: "x", Value: "y", Enabled: true}}); !errors.Is(err, ErrMalformedFolderVariables) {
		t.Fatalf("WriteFolderVariables malformed = %v, want ErrMalformedFolderVariables", err)
	}
}
