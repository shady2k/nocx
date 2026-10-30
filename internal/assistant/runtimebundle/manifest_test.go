package runtimebundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/coordinator"
)

func fixtureObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		t.Fatalf("fixture needs a JSON object, got %T", value)
	}
	return object
}

func fixtureArray(t *testing.T, value any) []any {
	t.Helper()
	array, ok := value.([]any)
	if !ok || array == nil {
		t.Fatalf("fixture needs a JSON array, got %T", value)
	}
	return array
}

func bundleFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	files := []any{}
	for _, file := range []struct {
		name, body string
		executable bool
	}{
		{InterpreterPath, "#!/bin/sh\nexit 99\n", true}, {EntrypointPath, "raise RuntimeError('must not execute')\n", false}, {"python/lib/library.py", "package data\n", false},
	} {
		name := filepath.Join(root, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if file.executable {
			mode = 0o700
		}
		if err := os.WriteFile(name, []byte(file.body), mode); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(file.body))
		files = append(files, map[string]any{"path": file.name, "sha256": hex.EncodeToString(sum[:]), "size": len(file.body), "executable": file.executable})
	}
	if err := os.Symlink("python3.13", filepath.Join(root, "python/bin/python")); err != nil {
		t.Fatal(err)
	}
	files = append(files, map[string]any{"path": "python/bin/python", "link": "python3.13"})
	manifest := map[string]any{"formatVersion": 1, "metadata": map[string]any{"bridgeVersion": 1, "target": "linux/amd64", "interpreter": InterpreterPath, "entrypoint": EntrypointPath, "pythonVersion": "3.13.16", "lockSha256": strings.Repeat("a", 64), "packages": []any{map[string]any{"name": "pydantic-ai-slim", "version": "1.100.0"}}, "provenance": map[string]any{"source": "unit fixture"}}, "files": files}
	writeManifest(t, root, manifest)
	return root, manifest
}

func writeManifest(t *testing.T, root string, manifest map[string]any) string {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestManifestRejectsInvalidAuthority(t *testing.T) {
	cases := map[string]func(*testing.T, map[string]any){
		"unknown root": func(_ *testing.T, m map[string]any) { m["command"] = "python" },
		"case variant root": func(_ *testing.T, m map[string]any) {
			m["FormatVersion"] = m["formatVersion"]
			delete(m, "formatVersion")
		},
		"case variant metadata": func(t *testing.T, m map[string]any) {
			fields := fixtureObject(t, m["metadata"])
			fields["Target"] = fields["target"]
			delete(fields, "target")
		},
		"case variant package": func(t *testing.T, m map[string]any) {
			fields := fixtureObject(t, fixtureArray(t, fixtureObject(t, m["metadata"])["packages"])[0])
			fields["Name"] = fields["name"]
			delete(fields, "name")
		},
		"case variant file": func(t *testing.T, m map[string]any) {
			fields := fixtureObject(t, fixtureArray(t, m["files"])[0])
			fields["Path"] = fields["path"]
			delete(fields, "path")
		},
		"null files":       func(_ *testing.T, m map[string]any) { m["files"] = nil },
		"wrong version":    func(_ *testing.T, m map[string]any) { m["formatVersion"] = 2 },
		"unknown metadata": func(t *testing.T, m map[string]any) { fixtureObject(t, m["metadata"])["args"] = []string{"-c", "evil"} },
		"wrong target":     func(t *testing.T, m map[string]any) { fixtureObject(t, m["metadata"])["target"] = "darwin/arm64" },
		"wrong bridge":     func(t *testing.T, m map[string]any) { fixtureObject(t, m["metadata"])["bridgeVersion"] = 2 },
		"arbitrary interpreter": func(t *testing.T, m map[string]any) {
			fixtureObject(t, m["metadata"])["interpreter"] = "/usr/bin/python3"
		},
		"arbitrary entrypoint": func(t *testing.T, m map[string]any) { fixtureObject(t, m["metadata"])["entrypoint"] = "other.py" },
		"missing lock":         func(t *testing.T, m map[string]any) { delete(fixtureObject(t, m["metadata"]), "lockSha256") },
		"null provenance":      func(t *testing.T, m map[string]any) { fixtureObject(t, m["metadata"])["provenance"] = nil },
		"unknown file":         func(t *testing.T, m map[string]any) { fixtureObject(t, fixtureArray(t, m["files"])[0])["mode"] = 511 },
		"missing size":         func(t *testing.T, m map[string]any) { delete(fixtureObject(t, fixtureArray(t, m["files"])[0]), "size") },
		"missing false executable": func(t *testing.T, m map[string]any) {
			delete(fixtureObject(t, fixtureArray(t, m["files"])[1]), "executable")
		},
		"null executable": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[1])["executable"] = nil
		},
		"negative size": func(t *testing.T, m map[string]any) { fixtureObject(t, fixtureArray(t, m["files"])[0])["size"] = -1 },
		"uppercase hash": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[0])["sha256"] = strings.Repeat("A", 64)
		},
		"absolute path": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[2])["path"] = "/escape"
		},
		"parent path": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[2])["path"] = "python/../../escape"
		},
		"backslash path": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[2])["path"] = "python\\escape"
		},
		"duplicate file": func(t *testing.T, m map[string]any) {
			files := fixtureArray(t, m["files"])
			m["files"] = append(files, files[0])
		},
		"self listed manifest": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[2])["path"] = ManifestName
		},
		"escaping symlink": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[3])["link"] = "../../../escape"
		},
		"absolute symlink": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[3])["link"] = "/etc/passwd"
		},
		"cyclic symlink": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[3])["link"] = "python"
		},
		"dangling symlink": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[3])["link"] = "missing"
		},
		"mixed link shape": func(t *testing.T, m map[string]any) {
			fixtureObject(t, fixtureArray(t, m["files"])[3])["sha256"] = strings.Repeat("a", 64)
		},
		"symlink ancestor": func(t *testing.T, m map[string]any) {
			m["files"] = append(fixtureArray(t, m["files"]), map[string]any{"path": "python/bin/python/child", "link": "../python3.13"})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root, m := bundleFixture(t)
			mutate(t, m)
			writeManifest(t, root, m)
			if _, err := ReadManifest(context.Background(), osView{}, root, "", "linux/amd64"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid manifest: %v", err)
			}
		})
	}
}

func TestManifestRejectsDuplicateTrailingAndOversizedJSON(t *testing.T) {
	for _, kind := range []string{"duplicate", "trailing", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := bundleFixture(t)
			name := filepath.Join(root, ManifestName)
			data, err := os.ReadFile(name) //nolint:gosec // fixed manifest name beneath the test-created bundle directory
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "duplicate":
				data = append([]byte(`{"formatVersion":1,`), data[1:]...)
			case "trailing":
				data = append(data, []byte(` {}`)...)
			case "oversized":
				data = []byte(strings.Repeat(" ", maxManifestBytes+1))
			}
			if err = os.WriteFile(name, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = ReadManifest(context.Background(), osView{}, root, "", "linux/amd64"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted %s: %v", kind, err)
			}
		})
	}
}

func TestVerifyTreeRejectsUnlistedChangedAndEscapingFiles(t *testing.T) {
	cases := map[string]func(*testing.T, string){
		"unlisted code": func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "app/injected.py"), []byte("evil"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"same size corruption": func(t *testing.T, root string) {
			name := filepath.Join(root, "python/lib/library.py")
			data, err := os.ReadFile(name) //nolint:gosec // fixed library path beneath the test-created bundle directory
			if err != nil {
				t.Fatal(err)
			}
			data[0] ^= 1
			if err = os.WriteFile(name, data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink substitution": func(t *testing.T, root string) {
			name := filepath.Join(root, "python/bin/python")
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/usr/bin/python3", name); err != nil {
				t.Fatal(err)
			}
		},
		"symlink ancestor": func(t *testing.T, root string) {
			name := filepath.Join(root, "app")
			outside := t.TempDir()
			if err := os.Rename(name, filepath.Join(outside, "app")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "app"), name); err != nil {
				t.Fatal(err)
			}
		},
		"missing file": func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, EntrypointPath)); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root, _ := bundleFixture(t)
			manifest, err := ReadManifest(context.Background(), osView{}, root, "", "linux/amd64")
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, root)
			if _, err = VerifyTree(context.Background(), osView{}, root, manifest); err == nil {
				t.Fatal("accepted modified tree")
			}
		})
	}
}

type countingView struct {
	View
	opened []string
}

func (v *countingView) Open(name string) (io.ReadCloser, error) {
	v.opened = append(v.opened, name)
	return v.View.Open(name)
}

func TestLocatorReadsOnlyManifestAndNeverExecutes(t *testing.T) {
	root, m := bundleFixture(t)
	digest := writeManifest(t, root, m)
	root = stagedFixture(t, root, digest)
	view := &countingView{View: osView{}}
	locator := New(root, digest, "linux/amd64")
	t.Cleanup(func() { _ = locator.Close() })
	locator.view = view
	resolved, err := locator.Locate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.opened) != 1 || view.opened[0] != filepath.Join(root, ManifestName) {
		t.Fatalf("read installed libraries: %v", view.opened)
	}
	if resolved.ManifestDigest != digest || resolved.Version != digest || resolved.Directory != filepath.Join(root, "app") || resolved.Interpreter != filepath.Join(root, InterpreterPath) || resolved.Entrypoint != filepath.Join(root, EntrypointPath) {
		t.Fatalf("wrong identity: %#v", resolved)
	}
	// A library byte failure is the installer's responsibility, not a hidden
	// subprocess or an expensive complete library read on every readiness.
	if err = os.WriteFile(filepath.Join(root, "python/lib/library.py"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = locator.Locate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = New(root, strings.Repeat("b", 64), "linux/amd64").Locate(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ignored launcher digest: %v", err)
	}
	if err = os.Remove(filepath.Join(root, InterpreterPath)); err != nil {
		t.Fatal(err)
	}
	if _, err = locator.Locate(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing interpreter not visible: %v", err)
	}
}

func TestPackagedPathsAndExplicitEmptyHandoff(t *testing.T) {
	for _, tc := range []struct{ os, arch, exe, want string }{
		{"linux", "amd64", "/mounted/usr/bin/nocx", "/mounted/usr/lib/nocx/agent-runtime/linux-amd64"},
		{"linux", "amd64", "/project/build/bin/nocx-server", "/project/build/lib/nocx/agent-runtime/linux-amd64"},
		{"darwin", "arm64", "/App.app/Contents/MacOS/nocx", "/App.app/Contents/Resources/agent-runtime/darwin-arm64"},
		{"darwin", "amd64", "/App.app/Contents/MacOS/nocx", "/App.app/Contents/Resources/agent-runtime/darwin-amd64"},
	} {
		got, err := PackagedRoot(tc.exe, tc.os, tc.arch)
		if err != nil || got != tc.want {
			t.Fatalf("%s/%s path=%q error=%v", tc.os, tc.arch, got, err)
		}
	}
	root, _ := bundleFixture(t)
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"absent", map[string]string{}, "/project/build/lib/nocx/agent-runtime/linux-amd64"},
		{"empty", map[string]string{coordinator.AgentRuntimeRootEnv: ""}, ""},
		{"explicit", map[string]string{coordinator.AgentRuntimeRootEnv: root, coordinator.AgentRuntimeDigestEnv: "pin"}, root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locator := locatorForExecutable("/project/build/bin/nocx-server", "linux", "amd64", func(key string) (string, bool) { value, ok := tc.env[key]; return value, ok })
			if locator.root != tc.want {
				t.Fatalf("handoff=%q want %q", locator.root, tc.want)
			}
			if tc.name == "empty" {
				if _, err := locator.Locate(context.Background()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("empty handoff searched elsewhere: %v", err)
				}
			}
		})
	}
	if _, err := PackagedRoot("/bin/nocx", "linux", "arm64"); err == nil {
		t.Fatal("accepted unsupported platform")
	}
}

func TestLocatorAllowsOnlyDeclaredInternalInterpreterLinks(t *testing.T) {
	root, m := bundleFixture(t)
	if err := os.Rename(filepath.Join(root, InterpreterPath), filepath.Join(root, "python/bin/python-real")); err != nil {
		t.Fatal(err)
	}
	fixtureObject(t, fixtureArray(t, m["files"])[0])["path"] = "python/bin/python-real"
	if err := os.Symlink("python-real", filepath.Join(root, InterpreterPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(root, "app/root")); err != nil {
		t.Fatal(err)
	}
	m["files"] = append(fixtureArray(t, m["files"]), map[string]any{"path": InterpreterPath, "link": "python-real"}, map[string]any{"path": "app/root", "link": ".."})
	digest := writeManifest(t, root, m)
	root = stagedFixture(t, root, digest)
	locator := New(root, digest, "linux/amd64")
	t.Cleanup(func() { _ = locator.Close() })
	manifest, err := ReadManifest(context.Background(), osView{}, root, digest, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyTree(context.Background(), osView{}, root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = locator.Locate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, InterpreterPath)); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("/usr/bin/python3", filepath.Join(root, InterpreterPath)); err != nil {
		t.Fatal(err)
	}
	if _, err = locator.Locate(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted interpreter escape: %v", err)
	}
}

// Unit locator fixtures use the same immutable hash-named boundary as real
// staging; only their tiny file contents are synthetic, not the lease logic.
func stagedFixture(t *testing.T, root, digest string) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // root is the test-created runtime directory; owner execute permission permits traversal
		t.Fatal(err)
	}
	destination := filepath.Join(parent, digest)
	if err := os.Rename(root, destination); err != nil {
		t.Fatal(err)
	}
	return destination
}

func TestManifestRejectsComposedIntermediateSymlinkEscape(t *testing.T) {
	root, manifest := bundleFixture(t)
	if err := os.Rename(filepath.Join(root, InterpreterPath), filepath.Join(root, "python/bin/python3.13-real")); err != nil {
		t.Fatal(err)
	}
	fixtureObject(t, fixtureArray(t, manifest["files"])[0])["path"] = "python/bin/python3.13-real"
	for name, target := range map[string]string{"python/bin/root": "../..", InterpreterPath: "root/../python3.13-real"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
		manifest["files"] = append(fixtureArray(t, manifest["files"]), map[string]any{"path": name, "link": target})
	}
	digest := writeManifest(t, root, manifest)
	if _, err := ReadManifest(context.Background(), osView{}, root, digest, "linux/amd64"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted composed escape: %v", err)
	}
	root = stagedFixture(t, root, digest)
	locator := New(root, digest, "linux/amd64")
	defer func() {
		if closeErr := locator.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if _, err := locator.Locate(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("selected outside interpreter: %v", err)
	}
}

func TestLocatorChecksDirectoriesRemovedByDotDot(t *testing.T) {
	root, manifest := bundleFixture(t)
	if err := os.Rename(filepath.Join(root, InterpreterPath), filepath.Join(root, "python/bin/python3.13-real")); err != nil {
		t.Fatal(err)
	}
	fixtureObject(t, fixtureArray(t, manifest["files"])[0])["path"] = "python/bin/python3.13-real"
	if err := os.Symlink("../lib/../bin/python3.13-real", filepath.Join(root, InterpreterPath)); err != nil {
		t.Fatal(err)
	}
	manifest["files"] = append(fixtureArray(t, manifest["files"]), map[string]any{"path": InterpreterPath, "link": "../lib/../bin/python3.13-real"})
	digest := writeManifest(t, root, manifest)
	root = stagedFixture(t, root, digest)
	locator := New(root, digest, "linux/amd64")
	defer func() {
		if closeErr := locator.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if _, err := locator.Locate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "python/lib"), filepath.Join(t.TempDir(), "lib")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../..", filepath.Join(root, "python/lib")); err != nil {
		t.Fatal(err)
	}
	if _, err := locator.Locate(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ignored transient path component: %v", err)
	}
}
