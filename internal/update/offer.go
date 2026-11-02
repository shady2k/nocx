package update

import (
	"net/url"
	"strings"

	"github.com/shady2k/nocx/internal/version"
)

// offerLink is the link an offer carries.
//
// A stable release has release notes, and the signed manifest names them: that
// page is the link, unchanged (ADR-0097 keeps stable as it was).
//
// A nightly has no notes of its own — the manifest's page for a nightly is the
// nightly release, which describes none of the change between two nightlies —
// so a notes link sends a person to a page that cannot answer what they would
// get. What can answer it is the commit range between the installed build and
// the offered one, which is what the nightlies actually differ by
// (nocx-vh2hr.1.5, ADR-79b6cy61). When either end of that range is unknown
// there is nothing honest to link: the offer is still made, and the notice
// shows no link.
func offerLink(channel Channel, m *Manifest, installed version.BuildIdentity) string {
	if channel != ChannelNightly {
		return m.NotesURL
	}
	return compareURL(m.NotesURL, installed.Commit, m.Build.Commit)
}

// compareURL turns a release-page URL into the repository's compare range
// between two full commits, or "" when it cannot: a short or missing commit,
// one commit at both ends, or a URL that does not name a repository path.
//
// The host and repository come from the manifest's own signed URL — the same
// bytes that named the release — rather than a second copy of the repository's
// address in the app. The page's own tail is dropped: only its first two path
// segments are the repository.
func compareURL(releasePage, from, to string) string {
	if !fullCommit.MatchString(from) || !fullCommit.MatchString(to) || from == to {
		return ""
	}
	u, err := url.Parse(releasePage)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/" + parts[0] + "/" + parts[1] + "/compare/" + from + "..." + to
}
