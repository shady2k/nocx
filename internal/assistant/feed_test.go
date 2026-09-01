package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
)

func executeFetchedDocument(t *testing.T, doc apifetch.TextDocument) map[string]any {
	t.Helper()
	out, err := executeFetchURL(
		withToolBound(context.Background(), agenttools.ResultBound{MaxBytes: 64 << 10, Truncation: agenttools.TruncationDropTail}),
		&agenttools.URLScope{URLs: []string{"https://example.test/feed"}},
		json.RawMessage(`{"url":"https://example.test/feed"}`),
		toolSeams{
			fetcher:   fakeTextFetcher{result: doc},
			snapshots: newRunSnapshots(),
			runID:     "run-feed-test",
		},
	)
	if err != nil {
		t.Fatalf("executeFetchURL: %v", err)
	}
	return fetchWindowResult(t, out)
}

func executeFetchedText(t *testing.T, contentType, text string) map[string]any {
	t.Helper()
	return executeFetchedDocument(t, apifetch.TextDocument{URL: "https://example.test/feed", ContentType: contentType, Text: text})
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

func TestExecuteFetchURLExtractsHTMLWithoutTreatingFeedMarkerAsFeed(t *testing.T) {
	const body = `<html><body>The article says <rss is not a tag here.</body></html>`
	result := executeFetchedText(t, "text/html", body)
	if got := fetchedText(t, result); got != "The article says" {
		t.Fatalf("text = %q, want extracted HTML prose", got)
	}
}

func TestExecuteFetchURLPrettyPrintsJSONWithoutChangingItsValue(t *testing.T) {
	const body = ` {"name":"nocx","items":[1,true,{"nested":"value"}]} `
	result := executeFetchedText(t, "application/json", body)
	text := fetchedText(t, result)
	if !strings.Contains(text, "\"name\": \"nocx\"") || !strings.Contains(text, "      \"nested\": \"value\"") {
		t.Fatalf("formatted JSON = %q, want indentation", text)
	}
	var got, want any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("formatted JSON is invalid: %v", err)
	}
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatalf("test JSON is invalid: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formatted value = %#v, want %#v", got, want)
	}
	if result["lossy"] != false {
		t.Fatalf("lossy = %v, want false for JSON formatting", result["lossy"])
	}
}

func TestExecuteFetchURLPrettyPrintsBodySniffedJSON(t *testing.T) {
	result := executeFetchedText(t, "text/plain", "\n\t[{\"ok\":true}]")
	text := fetchedText(t, result)
	if !strings.Contains(text, "\n  {\n    \"ok\": true\n  }\n]") {
		t.Fatalf("formatted sniffed JSON = %q, want indentation", text)
	}
}

func TestExecuteFetchURLLeavesMalformedClaimedJSONUnchanged(t *testing.T) {
	const body = `<html><body>not JSON</body></html>`
	result := executeFetchedText(t, "application/json", body)
	if got := fetchedText(t, result); got != body {
		t.Fatalf("text = %q, want unchanged claimed JSON body %q", got, body)
	}
}

func TestExecuteFetchURLExtractsHTMLAndMarksItLossy(t *testing.T) {
	const body = `<!doctype html><!-- drop this --><html><head><style>.secret { color: red }</style><script>window.secretScript = "do not show";</script></head><body><p>First paragraph.</p><div>Second <strong>paragraph.</strong></div><noscript>disabled content</noscript></body></html>`
	result := executeFetchedText(t, "text/html", body)
	text := fetchedText(t, result)
	if !strings.Contains(text, "First paragraph.\n") || !strings.Contains(text, "Second paragraph.") {
		t.Fatalf("extracted HTML = %q, want block boundaries", text)
	}
	for _, unwanted := range []string{"window.secretScript", ".secret", "disabled content", "<!-- drop this -->"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("extracted HTML = %q, contains %q", text, unwanted)
		}
	}
	if result["lossy"] != true {
		t.Fatalf("lossy = %v, want true for HTML extraction", result["lossy"])
	}
}

func TestExecuteFetchURLExtractsHTMLFragment(t *testing.T) {
	result := executeFetchedText(t, "text/html", `<p>Fragment one</p><div>Fragment two</div>`)
	if got := fetchedText(t, result); got != "Fragment one\nFragment two" {
		t.Fatalf("fragment text = %q, want block-separated prose", got)
	}
}

func TestExecuteFetchURLLeavesPlainTextUnchanged(t *testing.T) {
	const body = "plain response with no special shape"
	result := executeFetchedText(t, "text/plain", body)
	if got := fetchedText(t, result); got != body {
		t.Fatalf("text = %q, want unchanged plain text %q", got, body)
	}
}
