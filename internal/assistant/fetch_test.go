package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
)

type fakeTextFetcher struct {
	result apifetch.TextResult
}

func (f fakeTextFetcher) FetchText(context.Context, string, int64) (apifetch.TextResult, error) {
	return f.result, nil
}

func TestExecuteFetchURLReturnsTheFetchedPage(t *testing.T) {
	out, err := executeFetchURL(
		context.WithValue(context.Background(), toolBoundContextKey{}, agenttools.ResultBound{
			MaxBytes: 64 << 10, Truncation: agenttools.TruncationDropTail,
		}),
		&agenttools.URLScope{URLs: []string{"https://example.test/page"}},
		json.RawMessage(`{"url":"https://example.test/page"}`),
		toolSeams{fetcher: fakeTextFetcher{result: apifetch.TextResult{
			URL: "https://example.test/page", ContentType: "text/html", Text: "Hello from the page",
		}}},
	)
	if err != nil {
		t.Fatalf("executeFetchURL: %v", err)
	}
	if out != `{"url":"https://example.test/page","contentType":"text/html","text":"Hello from the page","truncated":false,"omitted":0,"lossy":false}` {
		t.Fatalf("result = %s", out)
	}
}
