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
	// Backoff is the pause before each retry of a URL fetch, so its length is
	// the number of retries: four with the default schedule, declared by
	// defaultBackoff below. Only a failure that asking again could change is
	// retried — a 5xx, a 429, a connection that died, a body cut off mid-stream
	// — while a 4xx is an answer and a wrong hash is the tamper check. Nil
	// means the default.
	Backoff []time.Duration
	// Sleep is the pause itself. Nil means time.Sleep; a test injects a
	// recorder so the schedule is asserted rather than waited out.
	Sleep func(time.Duration)
	// Log, when set, is told about each retry with the URL and the status that
	// caused it. Fetch lends it the fetch's Log when it has none, so one sink
	// carries the whole job's log.
	Log func(format string, args ...any)
}

// defaultBackoff is four retries — 2 s, 10 s, 30 s, 60 s of waiting, 102 s in
// all — and the schedule is the whole budget: a job gives up after one file has
// spent it. The number is sized on the incident that bought it
// (nocx-atpw5.1.44): on 2026-11-05 GitHub's release download answered 504 on
// and off for about seven minutes, on a different asset in every job, and ten
// jobs across two pull requests went red for code that had not changed. The
// previous schedule (2 s, 10 s) would now be fourteen seconds of patience
// against that; four retries costs a dead release 102 s before it reddens the
// job, which is cheaper than the rerun it saves when the 504 is a bad minute
// rather than a bad release.
var defaultBackoff = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second}

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

// assetURL is where an HTTP base holds one asset, and the only place a release
// URL is built.
func (f *Fetcher) assetURL(a Asset) string {
	if strings.Contains(f.Base, "{asset}") {
		return strings.ReplaceAll(f.Base, "{asset}", a.Name)
	}
	return strings.TrimSuffix(f.Base, "/") + "/" + a.Name
}

// openLocal returns a reader for an asset in a directory of published files and
// a human-readable description of where it came from.
func (f *Fetcher) openLocal(a Asset) (io.ReadCloser, string, error) {
	dir, err := localBase(f.Base)
	if err != nil {
		return nil, f.Base, err
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

// download performs one whole attempt at one asset over HTTP, and says whether
// asking again could give a different answer. A 5xx, a 429 and a connection
// that died before or during the body are the server's hiccup and say nothing
// about the release; a 4xx is the release's answer, and bytes that hash wrong
// are the tamper check. Retrying either of those only delays the red.
func (f *Fetcher) download(target string, a Asset, dest string) (bool, error) {
	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Get(target)
	if err != nil {
		return true, fmt.Errorf("GET %s: %w", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("GET %s: %s", target, resp.Status)
		return resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests, err
	}
	return f.store(resp.Body, a, target, resp.Status, dest)
}

// store streams one asset to dest, hashing as it goes, and puts it in place
// only when it is the pinned file — so a failed verification leaves the
// previous, possibly correct, file untouched and never leaves a half-written
// archive where a link would find it.
//
// from names the source for the error a reader acts on; via is the HTTP status
// line when the bytes came over HTTP, empty for a directory. Its bool has
// download's meaning: a body that ended early is worth asking again, a hash
// that disagrees is not.
func (f *Fetcher) store(body io.Reader, a Asset, from, via, dest string) (bool, error) {
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
		if via == "" {
			return true, fmt.Errorf("%s: %w", from, copyErr)
		}
		return true, fmt.Errorf("GET %s: %s: %w", from, via, copyErr)
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
	return false, nil
}

// EnsureFile makes dest hold exactly the pinned asset, downloading it if it is
// absent or wrong. It returns whether it fetched.
//
// A download is retried after each pause in Backoff, and only for a failure
// that asking again could change: a 5xx, a 429, a connection that died, or a
// body cut off mid-stream. A 404 and a hash mismatch are returned at once —
// they are answers, and a second ask gets the same one. The file is verified
// BEFORE it is put in place, so no number of attempts can put substituted
// bytes there.
func (f *Fetcher) EnsureFile(a Asset, dest string) (bool, error) {
	if sum, _, err := HashFile(dest); err == nil && sum == a.SHA256 {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return false, err
	}
	if !isURL(f.Base) {
		body, from, err := f.openLocal(a)
		if err != nil {
			return false, err
		}
		defer func() { _ = body.Close() }()
		if _, err := f.store(body, a, from, "", dest); err != nil {
			return false, err
		}
		return true, nil
	}
	target := f.assetURL(a)
	backoff := f.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	for attempts := 1; ; attempts++ {
		retry, err := f.download(target, a, dest)
		if err == nil {
			return true, nil
		}
		if !retry || attempts > len(backoff) {
			if attempts == 1 {
				return false, err
			}
			return false, fmt.Errorf("%w (after %d attempts)", err, attempts)
		}
		wait := backoff[attempts-1]
		f.logf("retry %d of %d in %s: %v", attempts, len(backoff), wait, err)
		f.sleep(wait)
	}
}

// logf passes a line to the fetcher's log, if it has one.
func (f *Fetcher) logf(format string, args ...any) {
	if f.Log != nil {
		f.Log(format, args...)
	}
}

// sleep waits, through the injected pause when a test supplied one.
func (f *Fetcher) sleep(d time.Duration) {
	if f.Sleep != nil {
		f.Sleep(d)
		return
	}
	time.Sleep(d)
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
	// The retry line is the fetcher's, but its sink is the fetch's: one line per
	// attempt belongs in the log the job is already reading.
	if f.Log == nil {
		f.Log = logf
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
