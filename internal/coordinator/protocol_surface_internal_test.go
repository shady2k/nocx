package coordinator

// The protocol-surface tripwire (ADR-0101, expanded by ADR-0104).
//
// ADR-0101 and ADR-0104 make discovery ProtocolVersion the one compatibility
// boundary between a window and a coordinator, and that any change to the
// surface it covers must bump the constant. Said alone, that is a promise a
// reviewer makes; this file is the mechanism that keeps it: a fingerprint of
// the whole window<->coordinator surface, committed as a golden whose NAME
// carries the protocol version, asserted by a test on every run.
//
// The components of the surface, each through its existing owner:
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
//   - The shell integration's embedded scripts and their renderers — the
//     shell half of the authenticated lifecycle wire. Embed directives are
//     read from their owner, not mirrored in a second script list.
//   - lifecycle/protocol.go and lifecyclecodec's production sources — event
//     names, payloads, JSON tags and framing live here, not in contracts/.
//     Hashing source is deliberately conservative: even a comment-only edit
//     needs review and a bump rather than risking an unseen wire change. All
//     shell renderers are included: future generated-wrapper changes also
//     require a bump (ADR-0104), not merely changes to authored scripts.
//
// A change to any of them fails this test until the ProtocolVersion constant
// is bumped AND a new golden for the new version is committed — two
// deliberate acts, not one accident. Regenerate deliberately:
//
//	NOCX_PROTOCOL_SURFACE_UPDATE=1 go test ./internal/coordinator/ -run TestTheProtocolSurface
//
// and commit the new golden together with the bump, never before it. Update
// mode refuses a version at or below HEAD's committed ProtocolVersion, even
// if the working golden was edited or deleted. Refreshing a same-version
// fixture is not a substitute for a compatibility decision.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
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
		_, _ = fmt.Fprintf(h, "surface %s %s\n", name, hex.EncodeToString(sum[:]))
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

// contractJSONs reads the window/coordinator JSON contracts recursively:
// wire schemas, tools/ subschemas and OpenRPC. contracts/update is the signed
// artifact channel, not this protocol: build identity cannot require a
// window/coordinator compatibility bump (nocx-1ebrs.66.1.11).
func contractJSONs(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(contractsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == filepath.Join(contractsDir, "update") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".json") {
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
		files["contracts/"+filepath.ToSlash(rel)] = data
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

// protocolSurfaceFiles joins the surface's owners. Lifecycle envelopes are
// not JSON-RPC and therefore have no contracts/ schema; the codec is their
// actual wire owner. Keep the protocol declarations too: event values and
// bounds can change without changing the codec's own source.
func protocolSurfaceFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := contractJSONs(t)
	repo := filepath.Join("..", "..")
	add := func(name string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(name))) //nolint:gosec // repo-relative test input
		if err != nil {
			t.Fatalf("reading protocol surface %s: %v", name, err)
		}
		files[name] = data
	}

	// scripts.go owns both the embed declarations and the delivered-script
	// initialization. The other sources generate the wrapper block and strip
	// comments; hashing raw scripts alone would miss a changed renderer.
	for _, name := range []string{
		"internal/shellintegration/scripts.go",
		"internal/shellintegration/agents.go",
		"internal/shellintegration/fish_agents.go",
		"internal/shellintegration/strip.go",
		"internal/lifecycle/protocol.go",
	} {
		add(name)
	}
	owner := "internal/shellintegration/scripts.go"
	for _, name := range embeddedScriptPaths(t, filepath.Join(repo, "internal", "shellintegration"), files[owner]) {
		add("internal/shellintegration/" + name)
	}

	codec, err := filepath.Glob(filepath.Join(repo, "internal", "lifecyclecodec", "*.go"))
	if err != nil {
		t.Fatalf("listing lifecycle wire sources: %v", err)
	}
	count := 0
	for _, name := range codec {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		add("internal/lifecyclecodec/" + filepath.Base(name))
		count++
	}
	if count == 0 {
		t.Fatal("no lifecycle codec sources found — the tripwire has no wire owner")
	}
	return files
}

// embeddedScriptPaths follows the real embed declarations, including future
// file additions and glob matches, rather than maintaining a shell-name list.
func embeddedScriptPaths(t *testing.T, dir string, source []byte) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "scripts.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing shell embed owner: %v", err)
	}
	names := map[string]bool{}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !strings.HasPrefix(comment.Text, "//go:embed ") {
				continue
			}
			for _, pattern := range strings.Fields(strings.TrimPrefix(comment.Text, "//go:embed ")) {
				// Current owner uses unquoted file paths. Fail closed if its
				// syntax grows rather than silently omitting an embed input.
				if strings.ContainsAny(pattern, "\"`") || strings.HasPrefix(pattern, "all:") {
					t.Fatalf("unsupported embed pattern %q — extend the surface reader", pattern)
				}
				matches, globErr := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pattern)))
				if globErr != nil || len(matches) == 0 {
					t.Fatalf("resolving embed pattern %q: matches=%v error=%v", pattern, matches, globErr)
				}
				for _, match := range matches {
					info, statErr := os.Stat(match)
					if statErr != nil || info.IsDir() {
						t.Fatalf("embed input %s must be a readable file: %v", match, statErr)
					}
					rel, relErr := filepath.Rel(dir, match)
					if relErr != nil {
						t.Fatalf("relative embed input %s: %v", match, relErr)
					}
					names[filepath.ToSlash(rel)] = true
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("no embedded shell scripts found — the tripwire has no shell half")
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// committedProtocolVersion reads the immutable comparison point, not the
// working constant or golden. A caller cannot erase the required bump by
// deleting its working fixture, editing it, or supplying update mode again.
// No git write occurs, and an unavailable committed baseline fails closed.
func committedProtocolVersion() (int, error) {
	source, err := exec.Command("git", "show", "HEAD:internal/coordinator/handshake.go").Output()
	if err != nil {
		return 0, fmt.Errorf("reading committed ProtocolVersion: %w", err)
	}
	return protocolVersionFromSource(source)
}

func protocolVersionFromSource(source []byte) (int, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "handshake.go", source, 0)
	if err != nil {
		return 0, fmt.Errorf("parsing committed ProtocolVersion: %w", err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if name.Name != "ProtocolVersion" || i >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.INT {
					return 0, fmt.Errorf("committed ProtocolVersion must be an integer literal")
				}
				version, parseErr := strconv.Atoi(literal.Value)
				if parseErr != nil || version < 1 {
					return 0, fmt.Errorf("invalid committed ProtocolVersion %q", literal.Value)
				}
				return version, nil
			}
		}
	}
	return 0, fmt.Errorf("committed handshake has no ProtocolVersion constant")
}

// updateSurfaceGolden requires a forward bump relative to committed HEAD
// BEFORE touching any fixture. Its baseline is explicit so mutation tests
// exercise this same writer without changing the repository or its history.
func updateSurfaceGolden(dir string, version, committedVersion int, digest string) error {
	if committedVersion < 1 || version <= committedVersion {
		return fmt.Errorf("refusing protocol %d golden refresh: committed ProtocolVersion is %d; bump ProtocolVersion before regenerating", version, committedVersion)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir testdata: %w", err)
	}
	golden := filepath.Join(dir, fmt.Sprintf("protocol-surface-v%d.golden", version))
	if err := os.WriteFile(golden, []byte(digest+"\n"), 0o600); err != nil { //nolint:gosec // repo-relative test fixture
		return fmt.Errorf("writing %s: %w", golden, err)
	}
	stale, err := filepath.Glob(filepath.Join(dir, "protocol-surface-v*.golden"))
	if err != nil {
		return fmt.Errorf("listing goldens: %w", err)
	}
	for _, old := range stale {
		if old != golden {
			if err := os.Remove(old); err != nil {
				return fmt.Errorf("removing retired golden %s: %w", old, err)
			}
		}
	}
	return nil
}

// goldenPath names the golden for a protocol version. The version is IN THE
// FILENAME, so a bump needs a new file and the old one removed — two
// deliberate acts.
func goldenPath(version int) string {
	return filepath.Join("testdata", fmt.Sprintf("protocol-surface-v%d.golden", version))
}

// TestTheProtocolSurfaceGoldenMatchesTheCommittedSurface is the tripwire.
func TestTheProtocolSurfaceGoldenMatchesTheCommittedSurface(t *testing.T) {
	digest := surfaceDigest(protocolSurfaceFiles(t), discoveryShapes())
	golden := goldenPath(ProtocolVersion)

	if os.Getenv("NOCX_PROTOCOL_SURFACE_UPDATE") == "1" {
		committed, err := committedProtocolVersion()
		if err != nil {
			t.Fatal(err)
		}
		if err := updateSurfaceGolden("testdata", ProtocolVersion, committed, digest); err != nil {
			t.Fatal(err)
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
			"A contract, discovery shape, embedded shell script/renderer, or lifecycle wire source moved. Either revert it, or this is a protocol change: bump ProtocolVersion in handshake.go, regenerate the golden with NOCX_PROTOCOL_SURFACE_UPDATE=1 go test ./internal/coordinator/ -run TestTheProtocolSurface, and commit both together (ADR-0104).",
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

// TestTheSurfaceShellAndLifecycleMutationsRequireACompatibilityBump uses
// the collected repository inputs, not stand-in map keys: it detects a
// collector that accidentally drops either half of the lifecycle wire.
func TestTheSurfaceShellAndLifecycleMutationsRequireACompatibilityBump(t *testing.T) {
	files := protocolSurfaceFiles(t)
	shapes := discoveryShapes()
	baseline := surfaceDigest(files, shapes)
	var scripts []string
	for name := range files {
		if strings.HasPrefix(name, "internal/shellintegration/scripts/") {
			scripts = append(scripts, name)
		}
	}
	if len(scripts) == 0 {
		t.Fatal("no embedded scripts collected")
	}
	sort.Strings(scripts)
	components := append(scripts,
		"internal/shellintegration/scripts.go",
		"internal/shellintegration/agents.go",
		"internal/shellintegration/fish_agents.go",
		"internal/shellintegration/strip.go",
		"internal/lifecycle/protocol.go",
		"internal/lifecyclecodec/codec.go",
		"internal/lifecyclecodec/raw.go",
	)
	for _, name := range components {
		t.Run(name, func(t *testing.T) {
			original, ok := files[name]
			if !ok {
				t.Fatalf("surface omits its owner %s", name)
			}
			mutated := make(map[string][]byte, len(files))
			for key, data := range files {
				mutated[key] = data
			}
			mutated[name] = append(append([]byte(nil), original...), '\n')
			digest := surfaceDigest(mutated, shapes)
			if digest == baseline {
				t.Fatalf("changing %s did not move the fingerprint", name)
			}

			// The committed version is independent of the working fixture.
			// Try both a deleted golden and a manually refreshed one; neither
			// is permission for the real update writer to skip the bump.
			const committed = 4
			for _, fixture := range []string{"committed", "deleted", "manually-refreshed"} {
				t.Run(fixture, func(t *testing.T) {
					dir := t.TempDir()
					golden := filepath.Join(dir, "protocol-surface-v4.golden")
					want := baseline + "\n"
					if fixture == "manually-refreshed" {
						want = digest + "\n"
					}
					if fixture != "deleted" {
						if err := os.WriteFile(golden, []byte(want), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					err := updateSurfaceGolden(dir, committed, committed, digest)
					if err == nil || !strings.Contains(err.Error(), "bump ProtocolVersion") {
						t.Fatalf("same-version update error = %v, want required compatibility bump", err)
					}
					got, readErr := os.ReadFile(golden) //nolint:gosec // private test fixture
					if fixture == "deleted" {
						if !os.IsNotExist(readErr) {
							t.Fatalf("refused update recreated deleted fixture: %v", readErr)
						}
					} else if readErr != nil || string(got) != want {
						t.Fatalf("refused update touched fixture: got %q, error %v, want %q", got, readErr, want)
					}

					// The same mutation can be recorded with a deliberate
					// forward bump, and the retired golden is removed.
					if err := updateSurfaceGolden(dir, committed+1, committed, digest); err != nil {
						t.Fatalf("forward bump refused: %v", err)
					}
					entries, globErr := filepath.Glob(filepath.Join(dir, "protocol-surface-v*.golden"))
					newGolden := filepath.Join(dir, "protocol-surface-v5.golden")
					if globErr != nil || len(entries) != 1 || entries[0] != newGolden {
						t.Fatalf("forward bump goldens = %v, error %v, want only %s", entries, globErr, newGolden)
					}
					got, readErr = os.ReadFile(newGolden) //nolint:gosec // private test fixture
					if readErr != nil || string(got) != digest+"\n" {
						t.Fatalf("bumped fixture = %q, error %v, want %s", got, readErr, digest)
					}
				})
			}
		})
	}
}

func TestTheSurfaceGoldenUpdateRefusesDowngradesAndUnknownBaselines(t *testing.T) {
	for _, pair := range [][2]int{{3, 4}, {5, 0}} {
		dir := t.TempDir()
		if err := updateSurfaceGolden(dir, pair[0], pair[1], "changed"); err == nil {
			t.Fatalf("update accepted working version %d against committed %d", pair[0], pair[1])
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("refused update wrote files: %v, error %v", entries, err)
		}
	}
}

func TestTheSurfaceCommittedVersionIsParsedFromItsConstant(t *testing.T) {
	for _, source := range []string{
		"package coordinator\nconst ProtocolVersion = 4\n",
		"package coordinator\n// const ProtocolVersion = 99\nconst (\nOther = 123\nProtocolVersion int = 4\n)\n",
	} {
		got, err := protocolVersionFromSource([]byte(source))
		if err != nil || got != 4 {
			t.Errorf("committed version = %d, error %v, want 4", got, err)
		}
	}
	for _, source := range []string{
		"package coordinator\n",
		"package coordinator\nvar ProtocolVersion = 4\n",
		"package coordinator\nconst ProtocolVersion = 0\n",
		"package coordinator\nconst ProtocolVersion = anotherConstant\n",
		"not Go source",
	} {
		if _, err := protocolVersionFromSource([]byte(source)); err == nil {
			t.Errorf("invalid committed baseline accepted: %q", source)
		}
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

// Artifact contracts live beside the wire schemas for conformance checks, not
// on the window/coordinator protocol. Changing a nightly's identity must never
// demand a discovery protocol bump; an actual wire schema still must.
func TestArtifactManifestIsOutsideTheCoordinatorProtocol(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "update"), 0o750); err != nil {
		t.Fatal(err)
	}
	wire := filepath.Join(dir, "session.schema.json")
	artifact := filepath.Join(dir, "update", "manifest.schema.json")
	if err := os.WriteFile(wire, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte(`{"build":"yesterday"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := contractsDir
	contractsDir = dir
	t.Cleanup(func() { contractsDir = old })
	baseline := surfaceDigest(contractJSONs(t), nil)
	if err := os.WriteFile(artifact, []byte(`{"build":"tonight"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if surfaceDigest(contractJSONs(t), nil) != baseline {
		t.Fatal("artifact identity changed the window/coordinator protocol fingerprint")
	}
	if err := os.WriteFile(wire, []byte(`{"type":"array"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if surfaceDigest(contractJSONs(t), nil) == baseline {
		t.Fatal("actual wire contract no longer changes the protocol fingerprint")
	}
}
