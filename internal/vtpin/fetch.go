package vtpin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MismatchError is a published file whose bytes are not the ones the manifest
// pins. It is the one error this package exists to raise: a tampered or
// substituted archive must stop a build, not link into it.
type MismatchError struct {
	Asset   string
	Source  string
	Want    string
	Got     string
	Missing bool
}

func (e *MismatchError) Error() string {
	if e.Missing {
		return fmt.Sprintf("%s: %s is not there", e.Asset, e.Source)
	}
	return fmt.Sprintf("%s: %s is not the pinned file: manifest says sha256 %s, the file is %s", e.Asset, e.Source, e.Want, e.Got)
}

// Fetcher resolves an asset name to bytes, from a release URL or from a local
// directory. The local form is not a convenience for developers only: it is
// what makes the tamper check testable — tests serve the archives from an
// httptest server and from a directory, and a fetch that can only speak to
// GitHub is a fetch nobody can test.
type Fetcher struct {
	// Base is either an http(s) URL prefix (optionally containing {asset}),
	// a file:// URL, or a local directory path.
	Base string
	// HTTP is used for the URL forms; nil means http.DefaultClient with a
	// timeout, because a build that hangs on a stalled download is worse
	// than one that fails.
	HTTP *http.Client
}

// NewFetcher returns a fetcher for a base location.
func NewFetcher(base string) *Fetcher {
	return &Fetcher{
		Base: base,
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

// ResolveAsset renders the manifest's URL template for one asset, and is the
// only place a release URL is constructed. It is exported so a test can assert
// the URL a fetch would use without performing one — the publish command and
// the fetch must name the same file.
func (m *Manifest) ResolveAsset(a Asset) string {
	return strings.ReplaceAll(m.Release.AssetURLTemplate, "{asset}", a.Name)
}

// isURL reports whether a base is remote (http/https) rather than a directory.
func isURL(base string) bool {
	return strings.HasPrefix(base, "http://") || strings.HasPrefix(base, "https://")
}

// localBase turns a base into a directory path: file:// URLs and plain paths
// both mean "a directory holding the assets under their published names".
func localBase(base string) (string, error) {
	if !strings.HasPrefix(base, "file://") {
		return base, nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return u.Path, nil
}

// open returns a reader for an asset and a human-readable description of where
// it came from.
func (f *Fetcher) open(a Asset) (io.ReadCloser, string, error) {
	base := f.Base
	if isURL(base) {
		target := strings.TrimSuffix(base, "/") + "/" + a.Name
		if strings.Contains(base, "{asset}") {
			target = strings.ReplaceAll(base, "{asset}", a.Name)
		}
		client := f.HTTP
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Minute}
		}
		resp, err := client.Get(target)
		if err != nil {
			return nil, target, err
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, target, fmt.Errorf("GET %s: %s", target, resp.Status)
		}
		return resp.Body, target, nil
	}
	dir, err := localBase(base)
	if err != nil {
		return nil, base, err
	}
	path := filepath.Join(dir, a.Name)
	file, err := os.Open(path) //nolint:gosec // the local form of a fetch: a directory of assets the caller named
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, path, &MismatchError{Asset: a.Name, Source: path, Missing: true}
		}
		return nil, path, err
	}
	return file, path, nil
}

// EnsureFile makes dest hold exactly the pinned asset, downloading it if it is
// absent or wrong. It returns whether it fetched.
//
// The file is verified BEFORE it is put in place: it is streamed to a
// temporary neighbour, hashed, and renamed only on a match, so a failed
// verification leaves the previous — possibly correct — file untouched and
// never leaves a half-written archive where a link would find it.
func (f *Fetcher) EnsureFile(a Asset, dest string) (bool, error) {
	if sum, _, err := HashFile(dest); err == nil && sum == a.SHA256 {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return false, err
	}
	body, from, err := f.open(a)
	if err != nil {
		return false, err
	}
	defer func() { _ = body.Close() }()

	tmp := dest + ".part"
	out, err := os.Create(tmp) //nolint:gosec // dest is the path the caller asked this fetch to populate
	if err != nil {
		return false, err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, h), body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return false, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return false, closeErr
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != a.SHA256 {
		_ = os.Remove(tmp)
		return false, &MismatchError{Asset: a.Name, Source: from, Want: a.SHA256, Got: got}
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// FetchOptions tunes what a fetch materialises.
type FetchOptions struct {
	// Log, when set, is told what happened — what was verified in place and
	// what was downloaded. Silence would make "it fetched nothing" and "it
	// verified everything" indistinguishable in a build log.
	Log func(format string, args ...any)
}

// Fetch verifies (and, where needed, downloads) everything a build of the
// pinned library needs, into the layout Layout names.
func (m *Manifest) Fetch(root string, f *Fetcher, opts FetchOptions) error {
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for _, t := range m.Targets {
		dir := VendorDir(root, t.Name)
		// The archive and include paths come from the layout functions, not
		// from a second spelling of them here: they are the contract the CGo
		// link files name, and a fetch that wrote somewhere else would be a
		// build that links nothing.
		archive := VendorArchivePath(root, t.Name)
		fetched, err := f.EnsureFile(t.Archive, archive)
		if err != nil {
			return err
		}
		logf("%s: archive %s (%s)", t.Name, describe(fetched, "fetched", "verified"), humanBytes(t.Archive.Bytes))

		bundle := filepath.Join(dir, t.Headers.Name)
		fetched, err = f.EnsureFile(t.Headers, bundle)
		if err != nil {
			return err
		}
		logf("%s: headers %s", t.Name, describe(fetched, "fetched", "verified"))

		if err := m.materialiseHeaders(t, root, bundle); err != nil {
			return err
		}
	}
	// The licenses come with the archives and are verified the same way: a
	// license document that is not the pinned bytes is not the license for
	// these archives, and shipping it beside a binary would be shipping a
	// statement nobody can check.
	licenses := m.LicensesPath(root)
	fetched, err := f.EnsureFile(m.Licenses, licenses)
	if err != nil {
		return err
	}
	logf("licenses: %s (%s)", describe(fetched, "fetched", "verified"), humanBytes(m.Licenses.Bytes))
	return nil
}

// materialiseHeaders extracts the verified bundle into the target's include
// directory, and skips the work when the directory was already extracted from
// EXACTLY this bundle. The marker is the bundle's own sha256, so a re-pin
// re-extracts and a bundle that merely looks current does not.
func (m *Manifest) materialiseHeaders(t Target, root, bundle string) error {
	include := VendorIncludeDir(root, t.Name)
	marker := filepath.Join(VendorDir(root, t.Name), "include.sha256")
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == t.Headers.SHA256 { //nolint:gosec // the marker sits beside the bundle the fetch verified
		if _, err := os.Stat(filepath.Join(include, "ghostty")); err == nil {
			return nil
		}
	}
	staging := include + ".part"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := UnpackHeaders(bundle, staging); err != nil {
		return err
	}
	if err := os.RemoveAll(include); err != nil {
		return err
	}
	if err := os.Rename(staging, include); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(t.Headers.SHA256+"\n"), 0o600)
}

func describe(fetched bool, whenTrue, whenFalse string) string {
	if fetched {
		return whenTrue
	}
	return whenFalse
}

func humanBytes(n int64) string {
	const unit = 1 << 20
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/unit)
}
