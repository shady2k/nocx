package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/version"
)

// Fixture trust is injected only through UpdaterConfig.Keyring, the same
// composition seam production uses. The shipped ReleaseKeyring is untouched.
func channelTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return ed25519.NewKeyFromSeed(seed)
}

// signedChannelRelay has no updater logic: it only serves signed bytes.
func signedChannelRelay(t *testing.T, body []byte, private ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, body)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(body)
		case "/manifest.json.sig":
			_, _ = w.Write(signature)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckNightlyBuildIdentity(t *testing.T) {
	private := channelTestKey(t)
	oldCommit, oldDate := version.Commit, version.Date
	version.Commit, version.Date = strings.Repeat("a", 40), "2026-10-27T10:00:00Z"
	t.Cleanup(func() { version.Commit, version.Date = oldCommit, oldDate })
	for _, tc := range []struct {
		name                           string
		channel                        Channel
		remoteVersion, commit, builtAt string
		want                           bool
	}{
		{"later nightly offered", ChannelNightly, "0.8.0", strings.Repeat("b", 40), "2026-10-28T10:00:00Z", true},
		{"stale nightly refused", ChannelNightly, "0.8.0", strings.Repeat("b", 40), "2026-10-26T10:00:00Z", false},
		{"same instant refused", ChannelNightly, "0.8.0", strings.Repeat("b", 40), "2026-10-27T10:00:00Z", false},
		{"same commit rebuilt refused", ChannelNightly, "0.8.0", strings.Repeat("a", 40), "2026-10-28T10:00:00Z", false},
		{"stable same version unchanged", ChannelStable, "0.8.0", strings.Repeat("b", 40), "2026-10-28T10:00:00Z", false},
		{"nightly lower version refused", ChannelNightly, "0.7.0", strings.Repeat("b", 40), "2026-10-28T10:00:00Z", false},
		{"nightly newer semver offered", ChannelNightly, "0.9.0", strings.Repeat("b", 40), "2026-10-26T10:00:00Z", true},
		{"stable newer semver offered", ChannelStable, "0.9.0", strings.Repeat("b", 40), "2026-10-26T10:00:00Z", true},
		{"malformed remote date refused", ChannelNightly, "0.8.0", strings.Repeat("b", 40), "tomorrow", false},
		{"missing remote commit refused", ChannelNightly, "0.8.0", "", "2026-10-28T10:00:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"version":%q,"released":"2026-10-29T00:00:00Z","notesUrl":"https://example.test/notes","build":{"commit":%q,"builtAt":%q},"artifacts":[{"os":"linux","arch":"amd64","format":"AppImage","url":"https://example.test/nocx.AppImage","sha256":"%s","size":123},{"os":"linux","arch":"arm64","format":"AppImage","url":"https://example.test/nocx-arm64.AppImage","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":123},{"os":"darwin","arch":"universal","format":"zip","url":"https://example.test/nocx.zip","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":123}],"helpers":[]}`, tc.remoteVersion, tc.commit, tc.builtAt, strings.Repeat("c", 64)))
			srv := signedChannelRelay(t, body, private)
			fetch := NewGitHubManifestFetcher(nil)
			fetch.BaseURL, fetch.NightlyBaseURL = srv.URL, srv.URL
			fetch.SetChannel(tc.channel)
			keys := []ed25519.PublicKey{ed25519.PublicKey(private[ed25519.SeedSize:])}
			ctx, logger := logtest.New(t)
			u := NewUpdater(UpdaterConfig{Platform: NewPlatform(), Fetcher: fetch, Keyring: keys, CurrentVersion: "0.8.0", Logger: logger})
			got, err := u.Check(ctx)
			if err != nil {
				t.Fatalf("real Check: %v", err)
			}
			if (got != nil) != tc.want {
				t.Fatalf("offered = %v, want %v", got != nil, tc.want)
			}
		})
	}
}

// Switching settings after the body request must not mix two signed channels
// or change the comparison rule for the check already in flight.
func TestCheckKeepsItsFetchedChannel(t *testing.T) {
	private := channelTestKey(t)
	oldCommit, oldDate := version.Commit, version.Date
	version.Commit, version.Date = strings.Repeat("a", 40), "2026-10-27T10:00:00Z"
	t.Cleanup(func() { version.Commit, version.Date = oldCommit, oldDate })
	for _, initial := range []Channel{ChannelStable, ChannelNightly} {
		t.Run(initial.String(), func(t *testing.T) {
			fetch := NewGitHubManifestFetcher(nil)
			fetch.SetChannel(initial)
			body := contractDocument()
			signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, body)))
			other := ChannelNightly
			if initial == ChannelNightly {
				other = ChannelStable
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/" + initial.String() + "/manifest.json":
					fetch.SetChannel(other)
					_, _ = w.Write(body)
				case "/" + initial.String() + "/manifest.json.sig":
					_, _ = w.Write(signature)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			fetch.BaseURL, fetch.NightlyBaseURL = srv.URL+"/stable", srv.URL+"/nightly"
			ctx, logger := logtest.New(t)
			u := NewUpdater(UpdaterConfig{Platform: NewPlatform(), Fetcher: fetch, Keyring: []ed25519.PublicKey{ed25519.PublicKey(private[ed25519.SeedSize:])}, CurrentVersion: "0.8.0", Logger: logger})
			got, err := u.Check(ctx)
			if err != nil {
				t.Fatalf("channel changed during Check: %v", err)
			}
			if (got != nil) != (initial == ChannelNightly) {
				t.Fatalf("offer followed the changed channel, not %s: %v", initial, got)
			}
		})
	}
}

func TestCheckNightlyRefusesUntrustedSignature(t *testing.T) {
	private := channelTestKey(t)
	stranger := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	srv := signedChannelRelay(t, contractDocument(), stranger)
	fetch := NewGitHubManifestFetcher(nil)
	fetch.BaseURL, fetch.NightlyBaseURL = srv.URL, srv.URL
	fetch.SetChannel(ChannelNightly)
	ctx, logger := logtest.New(t)
	u := NewUpdater(UpdaterConfig{Platform: NewPlatform(), Fetcher: fetch, Keyring: []ed25519.PublicKey{ed25519.PublicKey(private[ed25519.SeedSize:])}, CurrentVersion: "0.8.0", Logger: logger})
	if got, err := u.Check(ctx); err == nil || got != nil {
		t.Fatalf("untrusted channel accepted: got=%v err=%v", got, err)
	}
}
