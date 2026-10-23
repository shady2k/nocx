package coordinator

// The protocol-surface tripwire (ADR-0101).
//
// ADR-0101 says the discovery ProtocolVersion is the one compatibility
// boundary between a window and a coordinator, and that any change to the
// surface it covers must bump the constant. Said alone, that is a promise a
// reviewer makes; this file is the mechanism that keeps it: a fingerprint of
// the whole window<->coordinator surface, committed as a golden whose NAME
// carries the protocol version, asserted by a test on every run.
//
// The three components of the surface, each through its existing owner:
//
//   - contracts/*.json — every wire schema, hashed by content. The OpenRPC
//     manifest (contracts/openrpc.json) is one of them, and transport's
//     TestOpenRPCManifestMatchesRegisteredMethods already enforces that the
//     manifest's method list IS the registered method set, exactly, in both
//     directions — so the registered JSON-RPC method names reach this
//     fingerprint through the registry's own published mirror rather than
//     through a second list here.
//   - The discovery shapes — the five handshake types in handshake.go,
//     dumped by reflection so a field added, renamed or retagged anywhere in
//     them moves the fingerprint without anybody remembering to update a
//     fixture.
//
// A change to any of them fails this test until the ProtocolVersion constant
// is bumped AND a new golden for the new version is committed — two
// deliberate acts, not one accident. Regenerate deliberately:
//
//	NOCX_PROTOCOL_SURFACE_UPDATE=1 go test ./internal/coordinator/ -run TestTheProtocolSurface
//
// and commit the new golden together with the bump, never before it.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// contractsDir is where the wire schemas live, relative to this package.
// The same convention transport's contract tests use; from here the tree
// root is two levels up.
var contractsDir = filepath.Join("..", "..", "contracts")

// surfaceDigest is the one fingerprint function: an ordered text of every
// component, hashed. Deterministic by construction — files are emitted in
// sorted name order and shape lines in the order the caller (a fixed list
// of types) provides.
func surfaceDigest(files map[string][]byte, shapeLines []string) string {
	h := sha256.New()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		//nolint:errcheck // hash.Hash never errors; a test cannot report better
		_, _ = fmt.Fprintf(h, "contract %s %s\n", name, hex.EncodeToString(sum[:]))
	}
	for _, line := range shapeLines {
		//nolint:errcheck // hash.Hash never errors; a test cannot report better
		_, _ = fmt.Fprintf(h, "shape %s\n", line)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// discoveryShapeLines dumps the JSON shape of the discovery handshake types:
// every field's name, its Go type and its JSON tag, recursing into nested
// structs and struct pointers. Values are deliberately absent — this is the
// SHAPE, so a new optional field moves it even though a zero value would not.
func discoveryShapeLines(prefix string, v any, out *[]string) {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
		*out = append(*out, prefix+" ->ptr "+t.String())
		prefix = prefix + "." + t.Name()
	}
	if t.Kind() != reflect.Struct {
		*out = append(*out, fmt.Sprintf("%s %s", prefix, t.String()))
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		line := fmt.Sprintf("%s.%s %s json:%q", prefix, f.Name, f.Type.String(), tag)
		*out = append(*out, line)
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			discoveryShapeLines(prefix+"."+f.Name, reflect.New(ft).Interface(), out)
		}
	}
}

// discoveryShapes is the fixed list of types the discovery socket speaks.
// handshake.go is their one owner; this list names them and nothing more.
func discoveryShapes() []string {
	var lines []string
	for _, v := range []any{Request{}, Response{}, Hello{}, ClientIdentity{}, Build{}} {
		discoveryShapeLines(reflect.TypeOf(v).Name(), v, &lines)
	}
	return lines
}

// contractJSONs reads every *.json under contracts/, recursively — the
// schemas, the tools/ subschemas and the OpenRPC manifest alike.
func contractJSONs(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(contractsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, relErr := filepath.Rel(contractsDir, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // a repo-relative test path
		if readErr != nil {
			return readErr
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", contractsDir, err)
	}
	if len(files) == 0 {
		t.Fatalf("no *.json contracts found under %s — the tripwire has nothing to guard", contractsDir)
	}
	return files
}

// goldenPath names the golden for a protocol version. The version is IN THE
// FILENAME, so a bump needs a new file and the old one removed — two
// deliberate acts.
func goldenPath(version int) string {
	return filepath.Join("testdata", fmt.Sprintf("protocol-surface-v%d.golden", version))
}

// TestTheProtocolSurfaceGoldenMatchesTheCommittedSurface is the tripwire.
func TestTheProtocolSurfaceGoldenMatchesTheCommittedSurface(t *testing.T) {
	digest := surfaceDigest(contractJSONs(t), discoveryShapes())
	golden := goldenPath(ProtocolVersion)

	if os.Getenv("NOCX_PROTOCOL_SURFACE_UPDATE") == "1" {
		// The deliberate half of a bump: write the new golden and remove the
		// one the bump retires, so the verify mode below can insist that
		// exactly one exists.
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		// This is a public source fingerprint, not secret material. Preserve
		// readability for the non-root CI user over a rootless bind mount.
		if err := os.WriteFile(golden, []byte(digest+"\n"), 0o644); err != nil { //nolint:gosec // a public, repo-relative test fixture
			t.Fatalf("writing %s: %v", golden, err)
		}
		// WriteFile does not change an existing file's private mode.
		if err := os.Chmod(golden, 0o644); err != nil { //nolint:gosec // a public, repo-relative test fixture
			t.Fatalf("making %s readable: %v", golden, err)
		}
		stale, err := filepath.Glob(filepath.Join("testdata", "protocol-surface-v*.golden"))
		if err != nil {
			t.Fatalf("listing goldens: %v", err)
		}
		for _, old := range stale {
			if old == golden {
				continue
			}
			if err := os.Remove(old); err != nil {
				t.Fatalf("removing the retired golden %s: %v", old, err)
			}
		}
		t.Fatalf("wrote %s — review it against the surface change, and commit it WITH the ProtocolVersion bump", golden)
	}

	matches, err := filepath.Glob(filepath.Join("testdata", "protocol-surface-v*.golden"))
	if err != nil {
		t.Fatalf("listing goldens: %v", err)
	}
	sort.Strings(matches)
	if len(matches) != 1 || matches[0] != golden {
		t.Fatalf("testdata holds goldens %v, want exactly [%s] — a protocol bump replaces the golden it leaves behind", matches, golden)
	}

	recorded, err := os.ReadFile(golden) //nolint:gosec // a repo-relative test path
	if err != nil {
		t.Fatalf("reading %s: %v\n\nThe window<->coordinator surface has no committed fingerprint for protocol %d. If the surface genuinely changed, bump ProtocolVersion and regenerate with NOCX_PROTOCOL_SURFACE_UPDATE=1 go test ./internal/coordinator/ -run TestTheProtocolSurface, committing the new golden in the same change.", golden, err, ProtocolVersion)
	}
	if got := strings.TrimSpace(string(recorded)); got != digest {
		t.Fatalf("the window<->coordinator surface changed without a protocol bump.\n"+
			"committed golden (%s): %s\n"+
			"surface as it stands:    %s\n\n"+
			"Something under contracts/, the registered method list (contracts/openrpc.json), or the discovery shapes in handshake.go moved. Either revert it, or this is a protocol change: bump ProtocolVersion in handshake.go, regenerate the golden with NOCX_PROTOCOL_SURFACE_UPDATE=1 go test ./internal/coordinator/ -run TestTheProtocolSurface, and commit both together (ADR-0101).",
			golden, got, digest)
	}
}

// TestTheSurfaceFingerprintMovesWhenEachComponentMoves proves the tripwire's
// mechanism rather than its current data: a fingerprint that cannot feel a
// component change guards nothing.
func TestTheSurfaceFingerprintMovesWhenEachComponentMoves(t *testing.T) {
	base := map[string][]byte{
		"openrpc.json":            []byte(`{"methods":[{"name":"a.b"}]}`),
		"a.b.schema.json":         []byte(`{"type":"object"}`),
		"tools/fetch.schema.json": []byte(`{"type":"string"}`),
	}
	shapes := []string{
		"Request.Type string json:\"type\"",
		"Request.Client *coordinator.ClientIdentity json:\"client,omitempty\"",
	}
	baseline := surfaceDigest(base, shapes)

	// A contract's bytes.
	mutated := map[string][]byte{}
	for k, v := range base {
		mutated[k] = append([]byte(nil), v...)
	}
	mutated["a.b.schema.json"] = []byte(`{"type":"array"}`)
	if surfaceDigest(mutated, shapes) == baseline {
		t.Error("a changed contract schema left the fingerprint unmoved")
	}

	// The contract set — a method's schema removed, or a new one added.
	removed := map[string][]byte{}
	for k, v := range base {
		if k != "a.b.schema.json" {
			removed[k] = v
		}
	}
	if surfaceDigest(removed, shapes) == baseline {
		t.Error("a removed contract left the fingerprint unmoved")
	}

	// The registered method list, through the manifest that transport's own
	// test holds equal to it.
	methods := map[string][]byte{}
	for k, v := range base {
		methods[k] = append([]byte(nil), v...)
	}
	methods["openrpc.json"] = []byte(`{"methods":[{"name":"a.b"},{"name":"c.d"}]}`)
	if surfaceDigest(methods, shapes) == baseline {
		t.Error("a new registered method left the fingerprint unmoved")
	}

	// A discovery shape — a field added, renamed or retagged.
	added := append(append([]string(nil), shapes...), "Request.Extra string json:\"extra\"")
	if surfaceDigest(base, added) == baseline {
		t.Error("a new discovery field left the fingerprint unmoved")
	}
	retagged := []string{
		"Request.Type string json:\"kind\"",
		shapes[1],
	}
	if surfaceDigest(base, retagged) == baseline {
		t.Error("a retagged discovery field left the fingerprint unmoved")
	}
}

// TestTheDiscoveryShapesCoverTheHandshakeTypes is the reflection's own check:
// the dump of the real handshake types names every field of every type, so a
// field nobody told this file about still moves the fingerprint. The
// assertion is against the types themselves — counted by reflection — not a
// hand-copied list.
func TestTheDiscoveryShapesCoverTheHandshakeTypes(t *testing.T) {
	lines := discoveryShapes()
	var fields int
	for _, v := range []any{Request{}, Response{}, Hello{}, ClientIdentity{}, Build{}} {
		tt := reflect.TypeOf(v)
		for i := 0; i < tt.NumField(); i++ {
			fields++
			want := fmt.Sprintf(".%s %s", tt.Field(i).Name, tt.Field(i).Type.String())
			found := false
			for _, line := range lines {
				if strings.Contains(line, want) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("the shape dump misses %s.%s — the tripwire cannot see it", tt.Name(), tt.Field(i).Name)
			}
		}
	}
	if fields == 0 {
		t.Fatal("the handshake types carry no fields?")
	}
}
