package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/content"
)

// endpointCapability is the narrowed fetch.url authority these tests run
// under, in the form a grant actually produces: the ENDPOINT the policy
// names, never the document at it (design §5.4).
func endpointCapability(ids ...string) *agenttools.URLScope {
	scope := &agenttools.URLScope{}
	for _, id := range ids {
		scope.Endpoints = append(scope.Endpoints, content.GrantScope{Kind: content.ResourceDestination, ID: id})
	}
	return scope
}

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
		endpointCapability("https://example.test"),
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
