package client_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem/hostsvc"
	filesystemlocal "github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// The fs service's half of contracts/README.md's third check: the REAL service
// over a REAL local provider, answering on the REAL host, read off the socket
// by the REAL client. A payload this test built would prove the struct
// marshals; this proves what the helper sends.
//
// It lives beside the session service's own contract test for the same reason
// that one does: the client package already owns `newFakeConn`, `hostFor`,
// `mustMarshal`, `loadHelperSchema` and `validateHelperJSON`, and a second
// copy of any of them would be a second owner of the harness.

// hostedFS builds a helper peer whose fs service is the shipped one, rooted at
// a temporary directory so every answer is about a tree this test owns.
func hostedFS(t *testing.T, root string) *client.Client {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, svcErr := hostsvc.New(filesystemlocal.New(filesystemlocal.WithRoot(root)),
		hostsvc.JournalConfig{
			Root:       t.TempDir(),
			Hostname:   "contract-host",
			InstanceID: strings.Repeat("a", 32),
		})
	if svcErr != nil {
		t.Fatalf("new fs service: %v", svcErr)
	}
	t.Cleanup(func() { _ = svc.Close() })
	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		h := hostFor(in, out, log)
		h.Register(svc)
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	})
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
		Log: log,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func fsTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestFSReadOpsOverTheWireConformToTheirContracts drives all five read ops
// through the socket, validating both halves of each contract against the
// schema that freezes it.
func TestFSReadOpsOverTheWireConformToTheirContracts(t *testing.T) {
	dir := fsTempDir(t)
	file := filepath.Join(dir, "note.txt")
	content := []byte("the bytes a version names\n")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "note-link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	c := hostedFS(t, dir)

	for _, tc := range []struct {
		name   string
		op     string
		params any
	}{
		{name: "root", op: proto.OpRoot, params: proto.FSRootParams{}},
		{name: "stat", op: proto.OpStat, params: proto.FSPathParams{Path: file}},
		{name: "list", op: proto.OpList, params: proto.FSListParams{Path: dir, Offset: 0, Limit: 10}},
		{name: "canonical", op: proto.OpCanonical, params: proto.FSPathParams{Path: link}},
		{name: "read", op: proto.OpRead, params: proto.FSReadParams{Path: file, Offset: 0, Length: 1 << 20, Whole: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paramsSchema := loadHelperSchema(t, "fs."+tc.name+".params.schema.json")
			if err := validateHelperJSON(paramsSchema, mustMarshal(t, tc.params)); err != nil {
				t.Fatalf("the params the test sends do not satisfy the contract: %v", err)
			}
			var raw json.RawMessage
			if err := c.Call(context.Background(), proto.ServiceFS, tc.op, tc.params, &raw); err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if err := validateHelperJSON(loadHelperSchema(t, "fs."+tc.name+".schema.json"), raw); err != nil {
				t.Fatalf("the %s result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", tc.name, err, raw)
			}

			if tc.name == "read" {
				var got proto.FSReadResult
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("decode read: %v", err)
				}
				if string(got.Data) != string(content) {
					t.Fatalf("read data = %q, want %q", got.Data, content)
				}
				if got.Version == nil {
					t.Fatal("a whole-file read off the socket carried no version")
				}
				sum := sha256.Sum256(content)
				if got.Version.SHA256 != hex.EncodeToString(sum[:]) {
					t.Fatalf("version sha256 = %q, want the digest of the returned bytes", got.Version.SHA256)
				}
			}
		})
	}

	// A range read is the same op with a different contract: no version, and
	// an identity that says which object the bytes came from.
	var rangeRaw json.RawMessage
	rangeIn := proto.FSReadParams{Path: file, Offset: 4, Length: 5}
	if err := validateHelperJSON(loadHelperSchema(t, "fs.read.params.schema.json"), mustMarshal(t, rangeIn)); err != nil {
		t.Fatalf("the range params do not satisfy the contract: %v", err)
	}
	if err := c.Call(context.Background(), proto.ServiceFS, proto.OpRead, rangeIn, &rangeRaw); err != nil {
		t.Fatalf("range read: %v", err)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "fs.read.schema.json"), rangeRaw); err != nil {
		t.Fatalf("the range result off the socket does not satisfy its contract: %v", err)
	}
	var rangeResult proto.FSReadResult
	if err := json.Unmarshal(rangeRaw, &rangeResult); err != nil {
		t.Fatalf("decode range read: %v", err)
	}
	if rangeResult.Version != nil {
		t.Fatal("a range read carried a version")
	}
	if rangeResult.Identity == "" {
		t.Fatal("a range read carried no identity on a unix build")
	}
	if string(rangeResult.Data) != string(content[4:9]) {
		t.Fatalf("range data = %q, want %q", rangeResult.Data, content[4:9])
	}

	// The case a hand-built fixture always gets right and a real encoder gets
	// wrong: an empty directory must marshal as [] and not as null.
	var emptyRaw json.RawMessage
	emptyIn := proto.FSListParams{Path: filepath.Join(dir, "empty"), Offset: 0, Limit: 10}
	if err := c.Call(context.Background(), proto.ServiceFS, proto.OpList, emptyIn, &emptyRaw); err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if string(emptyRaw) != `{"path":"`+filepath.Join(dir, "empty")+`","canonical":"`+filepath.Join(dir, "empty")+`","entries":[],"offset":0,"total":0,"hasMore":false,"rev":"`+emptyRev(t, emptyRaw)+`"}` {
		t.Fatalf("an empty listing came off the socket as %s", emptyRaw)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "fs.list.schema.json"), emptyRaw); err != nil {
		t.Fatalf("the empty listing does not satisfy its contract: %v", err)
	}
}

// emptyRev reads the digest out of the empty listing so the whole-payload
// comparison above can be exact without the test re-deriving ComputeRev — the
// digest's own test owns that computation, and a second copy here would agree
// with the product by construction.
func emptyRev(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var listing proto.FSListResult
	if err := json.Unmarshal(raw, &listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(listing.Rev) != 64 {
		t.Fatalf("rev = %q, want a sha256 hex digest", listing.Rev)
	}
	return listing.Rev
}

// TestFSListPermissionDenialKeepsTheConnectionServing is scenario 14 through
// the transport: a directory without read permission is a typed refusal on
// THIS request, and the connection answers the next one — a refused op must
// never end Serve, or one unreadable directory would take every panel on the
// host down with it.
func TestFSListPermissionDenialKeepsTheConnectionServing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; a directory's permission bits do not refuse it")
	}
	dir := fsTempDir(t)
	closed := filepath.Join(dir, "closed")
	if err := os.Mkdir(closed, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(closed, "inside"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 — restoring the fixture directory's mode; cleanup needs its
	// traversal bits to remove what is inside it.
	t.Cleanup(func() { _ = os.Chmod(closed, 0o750) })

	c := hostedFS(t, dir)
	var raw json.RawMessage
	err := c.Call(context.Background(), proto.ServiceFS, proto.OpList,
		proto.FSListParams{Path: closed, Offset: 0, Limit: 10}, &raw)
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("list on an unreadable directory = %v, want a *client.RefusalError", err)
	}
	if refusal.Code != hostsvc.ErrCodePermissionDenied {
		t.Fatalf("code = %q, want %q", refusal.Code, hostsvc.ErrCodePermissionDenied)
	}

	var rootRaw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceFS, proto.OpRoot, proto.FSRootParams{}, &rootRaw); err != nil {
		t.Fatalf("the connection stopped serving after a refusal: %v", err)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "fs.root.schema.json"), rootRaw); err != nil {
		t.Fatalf("the root answer after a refusal does not satisfy its contract: %v", err)
	}
}

// TestFSInventoryConformsToItsContract freezes the op that reports what a
// generation could not clean up: its params, its result with entries, and the
// REAL answer off the socket — which must be an empty ARRAY and never null,
// because a coordinator that read null as "no answer" would report a clean host
// as an unknown one.
func TestFSInventoryConformsToItsContract(t *testing.T) {
	if err := validateHelperJSON(loadHelperSchema(t, "fs.inventory.params.schema.json"),
		mustMarshal(t, proto.FSInventoryParams{})); err != nil {
		t.Fatalf("the params the caller sends do not satisfy the contract: %v", err)
	}

	// The shape with entries: the two fields are both required, and a stranded
	// file without its reason would be a warning nobody can act on.
	stranded := proto.FSInventoryResult{Stranded: []proto.FSStrandedTemp{
		{Path: "/home/dev/project/.nocx-write-0123456789abcdef0123456789abcdef", Reason: "permission denied"},
	}}
	if err := validateHelperJSON(loadHelperSchema(t, "fs.inventory.schema.json"), mustMarshal(t, stranded)); err != nil {
		t.Fatalf("a stranded inventory does not satisfy the contract: %v", err)
	}

	c := hostedFS(t, fsTempDir(t))
	var raw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceFS, proto.OpInventory, proto.FSInventoryParams{}, &raw); err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "fs.inventory.schema.json"), raw); err != nil {
		t.Fatalf("the inventory off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", err, raw)
	}
	if string(raw) != `{"stranded":[]}` {
		t.Fatalf("an empty inventory came off the socket as %s, want {\"stranded\":[]}", raw)
	}
}
