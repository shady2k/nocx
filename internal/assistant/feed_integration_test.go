package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/httppolicy"
)

func localFeedRoutes() apisend.Routes {
	return func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID != "" {
			return nil, errors.New("unexpected route")
		}
		return httppolicy.Local(), nil
	}
}

func legacyFeedResult(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	result, err := newRunSnapshots().Fetch(
		context.Background(),
		apifetch.New(localFeedRoutes(), nil),
		"legacy-feed-run",
		srv.URL,
		0,
		"",
		64<<10,
	)
	if err != nil {
		t.Fatalf("fetch legacy feed: %v", err)
	}
	return result.Text
}

func TestLegacyRSSDecodedByFetchTextRendersAsAList(t *testing.T) {
	body := append([]byte(`<?xml version="1.0" encoding="windows-1251"?><rss><channel><title>`), []byte{0xcd, 0xee, 0xe2, 0xee, 0xf1, 0xf2, 0xe8}...)
	body = append(body, []byte(`</title><item><title>`)...)
	body = append(body, []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}...)
	body = append(body, []byte(`</title><link>https://x/1</link></item></channel></rss>`)...)

	text := legacyFeedResult(t, body)
	if !strings.Contains(text, "# Новости") || !strings.Contains(text, "Привет") || strings.Contains(text, "<rss") {
		t.Fatalf("rendered legacy RSS = %q, want decoded list", text)
	}
}

func TestLegacyAtomDecodedByFetchTextRendersAsAList(t *testing.T) {
	body := append([]byte(`<?xml version="1.0" encoding="windows-1251"?><feed><title>`), []byte{0xcd, 0xee, 0xe2, 0xee, 0xf1, 0xf2, 0xe8}...)
	body = append(body, []byte(`</title><entry><title>`)...)
	body = append(body, []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}...)
	body = append(body, []byte(`</title><summary>Новости</summary><link href="https://x/1"/></entry></feed>`)...)

	text := legacyFeedResult(t, body)
	if !strings.Contains(text, "# Новости") || !strings.Contains(text, "Привет") || strings.Contains(text, "<feed") {
		t.Fatalf("rendered legacy Atom = %q, want decoded list", text)
	}
}

func TestFeedRendererAcceptsUnknownHistoricalDeclaration(t *testing.T) {
	const body = `<?xml version="1.0" encoding="x-unknown-feed-charset"?><rss><channel><title>News</title><item><title>Item</title></item></channel></rss>`
	text := renderFetchedDocument(apifetch.TextDocument{ContentType: "text/xml", Text: body})
	if !strings.Contains(text, "# News") || strings.Contains(text, "<rss") {
		t.Fatalf("rendered unknown-declaration feed = %q, want list rather than raw XML", text)
	}
}
