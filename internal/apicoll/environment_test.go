package apicoll

// The environment answers WHERE and HOW TO REACH IT (design §6.5): the
// address and the route are ONE RECORD, so a production request cannot
// accidentally go out around its bastion. The tests here are of two kinds:
// the reader for `environments/*.json`, and one assertion about the MODEL —
// that a per-request route is inexpressible, not merely absent from the UI.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeEnv drops one environment file into an opened collection.
func writeEnv(t *testing.T, root, file, body string) {
	t.Helper()
	dir := filepath.Join(root, "environments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir environments: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
}

// newEnvCollection makes a collection folder and opens it.
func newEnvCollection(t *testing.T) (Collections, HandleID, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ManifestName),
		[]byte(`{"schemaVersion":1,"name":"c"}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	svc := NewCollections(nil)
	op, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := op.Handle
	return svc, h, root
}

// TestListEnvironments_ReadsTheFolder is the happy path: two environments,
// each carrying its values, the names of its secret variables and its
// route.
func TestListEnvironments_ReadsTheFolder(t *testing.T) {
	svc, h, root := newEnvCollection(t)
	writeEnv(t, root, "dev.json",
		`{"name":"dev","values":{"baseUrl":"http://localhost:3000"},"route":{"kind":"direct"}}`)
	writeEnv(t, root, "prod.json",
		`{"name":"prod","values":{"baseUrl":"https://api.internal"},"secretVars":["token"],`+
			`"route":{"kind":"connection","profileId":"prod-bastion"}}`)

	envs, bad, err := svc.ListEnvironments(h)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("malformed = %v, want none", bad)
	}
	if len(envs) != 2 {
		t.Fatalf("got %d environments, want 2", len(envs))
	}
	// Lexical by file path, so two runs compare.
	if envs[0].RelPath != "environments/dev.json" || envs[1].RelPath != "environments/prod.json" {
		t.Errorf("paths = %q, %q, want them ordered lexically", envs[0].RelPath, envs[1].RelPath)
	}
	dev, prod := envs[0].Environment, envs[1].Environment
	if dev.Values["baseUrl"] != "http://localhost:3000" || dev.Route.Kind != RouteDirect {
		t.Errorf("dev = %+v, want the local address dialled directly", dev)
	}
	if prod.Route.Kind != RouteConnection || prod.Route.ProfileID != "prod-bastion" {
		t.Errorf("prod route = %+v, want it through prod-bastion", prod.Route)
	}
	if len(prod.SecretVars) != 1 || prod.SecretVars[0] != "token" {
		t.Errorf("prod secretVars = %v, want [token]", prod.SecretVars)
	}
	// §6.3: the file names its secret variables and holds no values for
	// them. This is the model's half of that claim.
	if v, ok := prod.Value("token"); ok {
		t.Errorf("the environment answered token = %q; a secret value is never in the file", v)
	}
}

// TestListEnvironments_OneBadFileDoesNotHideTheRest is the same rule the
// request listing already obeys: one bad file is a bad file, never a
// collection whose environments will not list.
func TestListEnvironments_OneBadFileDoesNotHideTheRest(t *testing.T) {
	svc, h, root := newEnvCollection(t)
	writeEnv(t, root, "good.json", `{"name":"good","route":{"kind":"direct"}}`)
	writeEnv(t, root, "broken.json", `{"name":`)
	writeEnv(t, root, "unknown-field.json", `{"name":"x","route":{"kind":"direct"},"secretId":"keychain:abc"}`)

	envs, bad, err := svc.ListEnvironments(h)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(envs) != 1 || envs[0].Environment.Name != "good" {
		t.Fatalf("environments = %+v, want only the good one", envs)
	}
	if len(bad) != 2 {
		t.Fatalf("malformed = %+v, want both bad files named", bad)
	}
	for _, m := range bad {
		if m.RelPath == "" || m.Reason == "" {
			t.Errorf("malformed entry %+v names neither the file nor the reason", m)
		}
	}
}

// TestListEnvironments_AFileNamingASecretIsMalformed: §8 says a collection
// file cannot name a secret AT ALL, and the reason is the FORMAT rather
// than a check. This pins the format half — an unknown field is refused, so
// there is no field in which an identifier could be smuggled and later
// read.
func TestListEnvironments_AFileNamingASecretIsMalformed(t *testing.T) {
	svc, h, root := newEnvCollection(t)
	writeEnv(t, root, "hostile.json",
		`{"name":"hostile","route":{"kind":"direct"},"secretIds":{"token":"keychain:ssh-prod"}}`)

	envs, bad, err := svc.ListEnvironments(h)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(envs) != 0 {
		t.Fatalf("environments = %+v, want the hostile file refused", envs)
	}
	if len(bad) != 1 || !strings.Contains(bad[0].Reason, "secretIds") {
		t.Fatalf("malformed = %+v, want the unknown field named", bad)
	}
}

// TestReadEnvironment_ReadsOne is the handle-addressed read.
func TestReadEnvironment_ReadsOne(t *testing.T) {
	svc, h, root := newEnvCollection(t)
	writeEnv(t, root, "dev.json", `{"name":"dev","values":{"a":"1"},"route":{"kind":"direct"}}`)

	got, err := svc.ReadEnvironment(h, "environments/dev.json")
	if err != nil {
		t.Fatalf("ReadEnvironment: %v", err)
	}
	if got.Name != "dev" || got.Values["a"] != "1" {
		t.Errorf("environment = %+v, want dev with a=1", got)
	}
}

// TestReadEnvironment_RefusesAPathThatIsNotAnEnvironment: the environments
// surface may not be a second way to read a request file, the manifest, or
// anything outside the folder (§13.1).
func TestReadEnvironment_RefusesAPathThatIsNotAnEnvironment(t *testing.T) {
	svc, h, _ := newEnvCollection(t)
	cases := map[string]string{
		"a request":            "req.json",
		"the manifest":         ManifestName,
		"absolute":             "/etc/passwd",
		"climbing out":         "environments/../../.ssh/id_ed25519",
		"not clean":            "environments/./dev.json",
		"nested":               "environments/sub/dev.json",
		"not json":             "environments/dev.yaml",
		"the directory itself": "environments",
		"empty":                "",
	}
	for name, rel := range cases {
		if _, err := svc.ReadEnvironment(h, rel); err == nil {
			t.Errorf("ReadEnvironment(%s: %q) succeeded, want it refused", name, rel)
		}
	}
}

// TestReadEnvironment_NeverFollowsASymlink pairs with the request reader's
// rule: a collection arriving in a pull request must not reach a file
// outside itself, and an environment file is as good a place to point
// elsewhere as a request file is.
func TestReadEnvironment_NeverFollowsASymlink(t *testing.T) {
	svc, h, root := newEnvCollection(t)
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(`{"name":"stolen","route":{"kind":"direct"}}`), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "environments"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "environments", "linked.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := svc.ReadEnvironment(h, "environments/linked.json"); err == nil {
		t.Error("ReadEnvironment followed a symlink out of the collection")
	}
	// And the listing names it rather than reading it — a listing that
	// opened it would have read the file before anybody clicked anything.
	envs, bad, err := svc.ListEnvironments(h)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(envs) != 0 {
		t.Errorf("environments = %+v, want the symlink not followed", envs)
	}
	if len(bad) != 1 || !strings.Contains(bad[0].Reason, "symlink") {
		t.Errorf("malformed = %+v, want the symlink named", bad)
	}
}

// TestReadEnvironment_RefusesARouteThatDoesNotSayHowToGetThere is §6.5's
// third consequence, which is its reason: a production request cannot
// accidentally go out around the bastion. An environment whose route is
// missing, unknown, or names a connection without naming which one, is
// REFUSED — never quietly treated as direct, which is exactly the send the
// route exists to prevent.
func TestReadEnvironment_RefusesARouteThatDoesNotSayHowToGetThere(t *testing.T) {
	cases := map[string]string{
		"no route at all":       `{"name":"e"}`,
		"empty kind":            `{"name":"e","route":{"kind":""}}`,
		"unknown kind":          `{"name":"e","route":{"kind":"carrier-pigeon"}}`,
		"connection, no id":     `{"name":"e","route":{"kind":"connection"}}`,
		"direct with a profile": `{"name":"e","route":{"kind":"direct","profileId":"prod-bastion"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			svc, h, root := newEnvCollection(t)
			writeEnv(t, root, "e.json", body)
			if _, err := svc.ReadEnvironment(h, "environments/e.json"); err == nil {
				t.Error("ReadEnvironment accepted it; a route that does not say how to get there is refused")
			}
			envs, bad, err := svc.ListEnvironments(h)
			if err != nil {
				t.Fatalf("ListEnvironments: %v", err)
			}
			if len(envs) != 0 || len(bad) != 1 {
				t.Errorf("environments = %+v, malformed = %+v, want it named as malformed", envs, bad)
			}
		})
	}
}

// TestReadEnvironment_AcceptsBothRoutesOnAnOrdinaryMachine is the pair to
// the refusals above (AGENTS.md testing rule 3): for every "returns an
// error when…", one that succeeds.
func TestReadEnvironment_AcceptsBothRoutesOnAnOrdinaryMachine(t *testing.T) {
	svc, h, root := newEnvCollection(t)
	writeEnv(t, root, "dev.json", `{"name":"dev","route":{"kind":"direct"}}`)
	writeEnv(t, root, "prod.json", `{"name":"prod","route":{"kind":"connection","profileId":"p"}}`)

	for _, rel := range []string{"environments/dev.json", "environments/prod.json"} {
		if _, err := svc.ReadEnvironment(h, rel); err != nil {
			t.Errorf("ReadEnvironment(%s): %v", rel, err)
		}
	}
}

// TestEnvironments_RefuseAReplacedRoot: the handle is re-validated per
// operation (§13.1's fourth rule), and the environments half is not a way
// round it.
func TestEnvironments_RefuseAReplacedRoot(t *testing.T) {
	svc, h, root := newEnvCollection(t)
	writeEnv(t, root, "dev.json", `{"name":"dev","route":{"kind":"direct"}}`)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	if _, _, err := svc.ListEnvironments(h); err == nil {
		t.Error("ListEnvironments answered out of a root that is gone")
	}
	if _, err := svc.ReadEnvironment(h, "environments/dev.json"); err == nil {
		t.Error("ReadEnvironment answered out of a root that is gone")
	}
}

// TestListEnvironments_NoEnvironmentsFolder: a collection with no
// environments is a collection, not an error. Postman exports without one
// are ordinary.
func TestListEnvironments_NoEnvironmentsFolder(t *testing.T) {
	svc, h, _ := newEnvCollection(t)
	envs, bad, err := svc.ListEnvironments(h)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(envs) != 0 || len(bad) != 0 {
		t.Errorf("got %+v / %+v, want both empty", envs, bad)
	}
}

// TestListEnvironments_UnreadableFolderIsReported is §12.1 for the one
// external call this reader makes that a temp directory can be made to
// fail: reading the directory itself.
func TestListEnvironments_UnreadableFolderIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny a read")
	}
	svc, h, root := newEnvCollection(t)
	dir := filepath.Join(root, "environments")
	writeEnv(t, root, "dev.json", `{"name":"dev","route":{"kind":"direct"}}`)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	//nolint:gosec // G302: this is a DIRECTORY being restored to the mode the collection folder uses; 0600 has no execute bit and would leave it unenterable for the cleanup that follows
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, _, err := svc.ListEnvironments(h); err == nil {
		t.Error("ListEnvironments succeeded on an unreadable environments folder")
	}
}

// ───────────────────────────────────────────────────────────────────────────
// The model, not only the UI.
// ───────────────────────────────────────────────────────────────────────────

// TestRequest_HasNoRouteField is §6.5's first consequence made structural:
// "There is no connection dropdown on the request. One concept, not two."
//
// A dropdown that is merely absent from the renderer can be added back by
// anybody; a FIELD that does not exist cannot be set by a file, by a
// renderer, or by a future surface. The route lives on the environment so
// that the address and the route are one record and cannot drift from each
// other — which is the failure AGENTS.md's rule about two derivations of
// one fact is written against.
//
// The walk is recursive because a route smuggled onto Body or Auth would be
// a per-request route just the same.
func TestRequest_HasNoRouteField(t *testing.T) {
	routeType := reflect.TypeOf(Route{})
	// Names that would be a second answer to "where does this go out
	// from". ProfileID is here because a request naming an SSH profile is a
	// route however it is spelled.
	forbidden := map[string]bool{
		"route": true, "routes": true, "routeid": true,
		"connection": true, "connectionid": true,
		"profile": true, "profileid": true,
		"via": true, "through": true, "tunnel": true, "bastion": true,
	}

	var visited []string
	var walk func(t reflect.Type, path string, seen map[reflect.Type]bool)
	walk = func(rt reflect.Type, path string, seen map[reflect.Type]bool) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			at := path + "." + f.Name
			visited = append(visited, at)
			if f.Type == routeType {
				t.Errorf("%s is an apicoll.Route — the route lives on the environment, never on a request (§6.5)", at)
			}
			if forbidden[strings.ToLower(f.Name)] {
				t.Errorf("%s names a route on the request; §6.5 says the environment owns it, so that the address and the route are ONE record", at)
			}
			walk(f.Type, at, seen)
		}
	}
	walk(reflect.TypeOf(Request{}), "Request", map[reflect.Type]bool{})

	// The walk has to have gone somewhere, and it has to have gone INTO the
	// nested types: a guard that silently visits nothing passes forever. A
	// route smuggled onto Body or Auth is a per-request route just the same,
	// so both must appear in what was checked.
	if len(visited) < len(reflect.VisibleFields(reflect.TypeOf(Request{}))) {
		t.Fatalf("the walk visited %d fields (%v) — it did not descend", len(visited), visited)
	}
	nested := map[string]bool{"Request.Body.Kind": false, "Request.Auth.Kind": false, "Request.Headers.Name": false}
	for _, v := range visited {
		if _, ok := nested[v]; ok {
			nested[v] = true
		}
	}
	for name, seen := range nested {
		if !seen {
			t.Errorf("the walk never reached %s; it checked only the top level", name)
		}
	}
}

// TestEnvironment_OwnsTheRoute is the other half, and it is what makes the
// test above evidence rather than a tautology: the field exists, on the
// environment, so its absence from Request is a placement and not an
// omission.
func TestEnvironment_OwnsTheRoute(t *testing.T) {
	f, ok := reflect.TypeOf(Environment{}).FieldByName("Route")
	if !ok {
		t.Fatal("Environment has no Route field — §6.5 puts the route on the environment")
	}
	if f.Type != reflect.TypeOf(Route{}) {
		t.Errorf("Environment.Route is %v, want apicoll.Route", f.Type)
	}
}

// TestCollections_SatisfiesBothHalves: the concrete service is the whole
// surface. Without this the package can compile while the environments half
// belongs to nothing anybody constructs.
func TestCollections_SatisfiesBothHalves(t *testing.T) {
	var _ Service = NewCollections(nil)
	var _ EnvironmentReader = NewCollections(nil)
	var _ Collections = newService()
}
