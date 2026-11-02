package update

import (
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/version"
)

// The link a nightly offer carries is the GitHub compare page of the
// manifest's own repository (nocx-vh2hr.1.5). Nightlies have no release notes
// of their own, so the notes page is not a link a person can resolve; the
// range between the installed commit and the offered one is.
func TestCompareURL(t *testing.T) {
	from, to := strings.Repeat("a", 40), strings.Repeat("b", 40)
	const page = "https://github.com/shady2k/nocx/releases/tag/nightly-20261030-1"
	for _, tc := range []struct {
		name, page, from, to, want string
	}{
		{"a nightly release page", page, from, to, "https://github.com/shady2k/nocx/compare/" + from + "..." + to},
		{"a stable release page", "https://github.com/o/r/releases/tag/v0.8.1", from, to, "https://github.com/o/r/compare/" + from + "..." + to},
		{"a query and fragment are not part of the repository", "https://github.com/o/r/releases/tag/x?y=1#z", from, to, "https://github.com/o/r/compare/" + from + "..." + to},
		{"a short installed commit", page, "abc123", to, ""},
		{"a short offered commit", page, from, "def456", ""},
		{"one commit twice", page, from, from, ""},
		{"no offered commit", page, from, "", ""},
		{"no page at all", "", from, to, ""},
		{"a page with no repository path", "https://example.test/notes", from, to, ""},
		{"a non-http scheme", "file:///releases/tag/x", from, to, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareURL(tc.page, tc.from, tc.to); got != tc.want {
				t.Fatalf("compareURL(%q, %q, %q) = %q, want %q", tc.page, tc.from, tc.to, got, tc.want)
			}
		})
	}
}

const offerNotes = "https://github.com/shady2k/nocx/releases/tag/nightly-20261030-1"

// checkOffer runs the real Check against a signed manifest, with the running
// build stamped to the given identity.
func checkOffer(t *testing.T, channel Channel, currentVersion, currentCommit, currentBuiltAt, remoteVersion, remoteCommit, remoteBuiltAt, notes string) *UpdateInfo {
	t.Helper()
	private := channelTestKey(t)
	oldCommit, oldDate := version.Commit, version.Date
	version.Commit, version.Date = currentCommit, currentBuiltAt
	t.Cleanup(func() { version.Commit, version.Date = oldCommit, oldDate })
	body := []byte(fmt.Sprintf(`{"version":%q,"released":"2026-10-29T00:00:00Z","notesUrl":%q,"build":{"commit":%q,"builtAt":%q},"artifacts":[{"os":"linux","arch":"amd64","format":"AppImage","url":"https://example.test/nocx.AppImage","sha256":"%s","size":123},{"os":"linux","arch":"arm64","format":"AppImage","url":"https://example.test/nocx-arm64.AppImage","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":123},{"os":"darwin","arch":"universal","format":"zip","url":"https://example.test/nocx.zip","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":123}],"helpers":[]}`, remoteVersion, notes, remoteCommit, remoteBuiltAt, strings.Repeat("c", 64)))
	srv := signedChannelRelay(t, body, private)
	fetch := NewGitHubManifestFetcher(nil)
	fetch.BaseURL, fetch.NightlyBaseURL = srv.URL, srv.URL
	fetch.SetChannel(channel)
	pub, ok := private.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("the fixture key did not hand back an ed25519 public key")
	}
	ctx, logger := logtest.New(t)
	u := NewUpdater(UpdaterConfig{
		Platform:       NewPlatform(),
		Fetcher:        fetch,
		Keyring:        []ed25519.PublicKey{pub},
		CurrentVersion: currentVersion,
		Logger:         logger,
	})
	got, err := u.Check(ctx)
	if err != nil {
		t.Fatalf("real Check: %v", err)
	}
	return got
}

// THE BEAD (nocx-vh2hr.1.5): a nightly of 0.8.1 is offered to a build that is
// already 0.8.1, because the bytes differ. The offer must name the offered
// build and link the range between the two commits, not the version the
// person already runs.
func TestCheckNightlySameVersionOfferNamesTheBuildAndLinksTheRange(t *testing.T) {
	installed := strings.Repeat("a", 40)
	offered := strings.Repeat("b", 40)
	got := checkOffer(t, ChannelNightly, "0.8.1", installed, "2026-10-27T10:00:00Z", "0.8.1", offered, "2026-10-30T07:50:35Z", offerNotes)
	if got == nil {
		t.Fatal("a newer nightly build of the same version was not offered: the channel's whole rule is that the bytes differ")
	}
	if got.Version != "0.8.1" {
		t.Fatalf("version = %q, want the same 0.8.1 the installed build runs", got.Version)
	}
	if got.Channel != ChannelNightly.String() {
		t.Fatalf("channel = %q, want %q: the notice names the build only on the nightly channel", got.Channel, ChannelNightly)
	}
	if got.Commit != offered || got.BuiltAt != "2026-10-30T07:50:35Z" {
		t.Fatalf("offered build = %q built %q, want %q built 2026-10-30T07:50:35Z", got.Commit, got.BuiltAt, offered)
	}
	// THE BUILD NUMBER BESIDE THE VERSION (nocx-vh2hr.1.5): the signed
	// instant, in the one YYYYMMDDHHMM derivation, is what the notice names.
	if got.BuildNumber != "202610300750" {
		t.Fatalf("offered build number = %q, want 202610300750 from the signed builtAt", got.BuildNumber)
	}
	want := "https://github.com/shady2k/nocx/compare/" + installed + "..." + offered
	if got.NotesURL != want {
		t.Fatalf("link = %q, want the compare range %q", got.NotesURL, want)
	}
}

// A nightly that also moves the version carries the same build identity; the
// build is what a nightly reader is being offered either way.
func TestCheckNightlyNewerVersionOfferNamesTheBuild(t *testing.T) {
	offered := strings.Repeat("d", 40)
	got := checkOffer(t, ChannelNightly, "0.8.1", strings.Repeat("a", 40), "2026-10-27T10:00:00Z", "0.8.2", offered, "2026-10-31T07:50:35Z", offerNotes)
	if got == nil {
		t.Fatal("a newer nightly version was not offered")
	}
	if got.Version != "0.8.2" || got.Commit != offered {
		t.Fatalf("offer = %q build %q, want 0.8.2 build %q", got.Version, got.Commit, offered)
	}
}

// The stable channel is untouched: the manifest's own notes page is the link,
// and its version is the whole name.
func TestCheckStableOfferKeepsTheManifestNotesURL(t *testing.T) {
	const page = "https://github.com/shady2k/nocx/releases/tag/v0.8.1"
	offered := strings.Repeat("e", 40)
	got := checkOffer(t, ChannelStable, "0.8.0", strings.Repeat("a", 40), "2026-10-27T10:00:00Z", "0.8.1", offered, "2026-10-30T07:50:35Z", page)
	if got == nil {
		t.Fatal("a newer stable release was not offered")
	}
	if got.Channel != ChannelStable.String() {
		t.Fatalf("channel = %q, want stable", got.Channel)
	}
	if got.NotesURL != page {
		t.Fatalf("link = %q, want the manifest's own notes page %q", got.NotesURL, page)
	}
}

// An installed build with no complete identity cannot be compared with the
// offered one: the notice still names the offered build, and the link is
// absent rather than a notes page that says nothing about the change.
func TestCheckNightlyWithoutAComparableInstalledBuildLinksNothing(t *testing.T) {
	got := checkOffer(t, ChannelNightly, "0.8.1", "", "", "0.9.0", strings.Repeat("f", 40), "2026-10-31T07:50:35Z", offerNotes)
	if got == nil {
		t.Fatal("a newer nightly version was not offered")
	}
	if got.NotesURL != "" {
		t.Fatalf("link = %q, want none: no installed commit means no range to show", got.NotesURL)
	}
	if got.Commit != strings.Repeat("f", 40) {
		t.Fatalf("offered build = %q, want it named even without a range", got.Commit)
	}
}
