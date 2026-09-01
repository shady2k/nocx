package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
)

func executeFetchedText(t *testing.T, contentType, text string) map[string]any {
	t.Helper()
	out, err := executeFetchURL(
		withToolBound(context.Background(), agenttools.ResultBound{MaxBytes: 64 << 10, Truncation: agenttools.TruncationDropTail}),
		&agenttools.URLScope{URLs: []string{"https://example.test/feed"}},
		json.RawMessage(`{"url":"https://example.test/feed"}`),
		toolSeams{
			fetcher:   fakeTextFetcher{result: apifetch.TextDocument{URL: "https://example.test/feed", ContentType: contentType, Text: text}},
			snapshots: newRunSnapshots(),
			runID:     "run-feed-test",
		},
	)
	if err != nil {
		t.Fatalf("executeFetchURL: %v", err)
	}
	return fetchWindowResult(t, out)
}

func fetchedText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["text"].(string)
	if !ok {
		t.Fatalf("result text has type %T", result["text"])
	}
	return text
}

func TestExecuteFetchURLRendersRSSAsAList(t *testing.T) {
	result := executeFetchedText(t, "text/xml", `<?xml version="1.0"?><rss version="2.0"><channel><title>News</title><item><title>First &amp; foremost</title><pubDate>Mon, 01 Jan 2024 12:00:00 GMT</pubDate><description><![CDATA[Read <b>this</b> and &lt;that&gt; now &amp; always]]></description><link>https://example.test/first</link></item></channel></rss>`)
	text := fetchedText(t, result)
	for _, want := range []string{"# News", "First & foremost", "Mon, 01 Jan 2024 12:00:00 GMT", "Read this and <that> now & always", "https://example.test/first"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered text = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "<item>") || strings.Contains(text, "<![CDATA[") || strings.Contains(text, "<rss") {
		t.Fatalf("rendered text retained raw feed markup: %q", text)
	}
}

func TestExecuteFetchURLRendersAtomAsAList(t *testing.T) {
	result := executeFetchedText(t, "application/atom+xml", `<feed xmlns="http://www.w3.org/2005/Atom"><title>Atom News</title><entry><title>Atom item</title><updated>2024-01-02T03:04:05Z</updated><summary>Summary text</summary><link href="https://example.test/atom"/></entry></feed>`)
	text := fetchedText(t, result)
	for _, want := range []string{"# Atom News", "Atom item", "2024-01-02T03:04:05Z", "Summary text", "https://example.test/atom"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered text = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "<entry>") || strings.Contains(text, "<link") {
		t.Fatalf("rendered text retained raw Atom markup: %q", text)
	}
}

func TestExecuteFetchURLMakesFeedItemCapVisible(t *testing.T) {
	var body strings.Builder
	body.WriteString(`<rss><channel><title>Many items</title>`)
	for i := 1; i <= 12; i++ {
		body.WriteString(`<item><title>Item `)
		body.WriteString(strconv.Itoa(i))
		body.WriteString(`</title></item>`)
	}
	body.WriteString(`</channel></rss>`)

	result := executeFetchedText(t, "application/rss+xml", body.String())
	text := fetchedText(t, result)
	if !strings.Contains(text, "Item 10") || strings.Contains(text, "Item 11") {
		t.Fatalf("rendered items = %q, want only first ten", text)
	}
	if !strings.Contains(text, "2 more") {
		t.Fatalf("rendered text = %q, want visible omitted-item count", text)
	}
}

func TestExecuteFetchURLLeavesNonFeedXMLAndMalformedFeedsRaw(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "non-feed XML", contentType: "text/xml", body: `<root><rss-not-a-feed/></root>`},
		{name: "malformed feed", contentType: "application/rss+xml", body: `<rss><channel><title>cut off`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := executeFetchedText(t, tc.contentType, tc.body)
			if got := fetchedText(t, result); got != tc.body {
				t.Fatalf("text = %q, want raw body %q", got, tc.body)
			}
		})
	}
}

func TestExecuteFetchURLDoesNotTreatHTMLFeedMarkerAsFeed(t *testing.T) {
	const body = `<html><body>The article says <rss is not a tag here.</body></html>`
	result := executeFetchedText(t, "text/html", body)
	if got := fetchedText(t, result); got != body {
		t.Fatalf("text = %q, want raw HTML body %q", got, body)
	}
}
