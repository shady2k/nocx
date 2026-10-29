package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The channel names the manifest a check fetches (ADR-0097): stable follows
// GitHub's `latest` redirect, nightly follows the moving `nightly` prerelease
// tag. One table owns both spellings — a second definition anywhere else is a
// second answer waiting to drift.
func TestChannelManifestBaseURL(t *testing.T) {
	cases := []struct {
		channel Channel
		want    string
	}{
		{ChannelStable, "https://github.com/shady2k/nocx/releases/latest/download"},
		{ChannelNightly, "https://github.com/shady2k/nocx/releases/download/nightly"},
	}
	for _, c := range cases {
		if got := c.channel.ManifestBaseURL(); got != c.want {
			t.Errorf("%s manifest base URL = %q, want %q", c.channel, got, c.want)
		}
	}
}

func TestParseChannel(t *testing.T) {
	for _, ok := range []struct {
		in   string
		want Channel
	}{{"stable", ChannelStable}, {"nightly", ChannelNightly}} {
		got, err := ParseChannel(ok.in)
		if err != nil {
			t.Fatalf("ParseChannel(%q): %v", ok.in, err)
		}
		if got != ok.want {
			t.Errorf("ParseChannel(%q) = %q, want %q", ok.in, got, ok.want)
		}
	}
	if _, err := ParseChannel("beta"); err == nil {
		t.Fatal("ParseChannel accepted an unknown channel — the renderer must learn it refused, not guess")
	}
}

// The fetcher must actually FETCH the selected channel's manifest: a stored
// setting that does not move the request disproves the channel feature
// (nocx-1ebrs.35.1). One server, two roots; the path names the channel that
// was asked for.
func TestFetcherFetchesTheSelectedChannelsManifest(t *testing.T) {
	f := NewGitHubManifestFetcher(nil)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	f.BaseURL = srv.URL + "/stable"
	f.NightlyBaseURL = srv.URL + "/nightly"

	_, _ = f.Fetch(context.Background())
	if got := hits.Load(); got != 1 {
		t.Fatalf("stable fetch made %d requests, want 1", got)
	}

	f.SetChannel(ChannelNightly)
	_, _ = f.Fetch(context.Background())
	if got := hits.Load(); got != 2 {
		t.Fatalf("nightly fetch made %d requests total, want 2", got)
	}
}

// SetChannel is called from the window binding while a check may be in
// flight; the selected channel must still be read whole.
func TestFetcherChannelSwitchesAtomically(t *testing.T) {
	f := NewGitHubManifestFetcher(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			f.SetChannel(Channel(i % 2))
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = f.selected.get()
	}
	<-done
}
