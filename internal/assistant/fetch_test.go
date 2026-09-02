package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
)

type fakeTextFetcher struct {
	result apifetch.TextDocument
}

func (f fakeTextFetcher) FetchText(context.Context, apifetch.TextRequest) (apifetch.TextDocument, error) {
	return f.result, nil
}

func TestExecuteFetchURLReturnsTheFetchedPage(t *testing.T) {
	out, err := executeFetchURL(
		context.WithValue(context.Background(), toolBoundContextKey{}, agenttools.ResultBound{
			MaxBytes: 64 << 10, Truncation: agenttools.TruncationDropTail,
		}),
		&agenttools.URLScope{URLs: []string{"https://example.test/page"}},
		json.RawMessage(`{"url":"https://example.test/page"}`),
		toolSeams{
			fetcher:   fakeTextFetcher{result: apifetch.TextDocument{URL: "https://example.test/page", ContentType: "text/html", Text: "Hello from the page"}},
			snapshots: newRunSnapshots(),
			runID:     "run-fetch-test",
		},
	)
	if err != nil {
		t.Fatalf("executeFetchURL: %v", err)
	}
	result := fetchWindowResult(t, out)
	if result["url"] != "https://example.test/page" || result["contentType"] != "text/html" ||
		result["text"] != "Hello from the page" || result["truncated"] != false ||
		result["remaining"] != float64(0) || result["revision"] == "" {
		t.Fatalf("result = %s", out)
	}
}
