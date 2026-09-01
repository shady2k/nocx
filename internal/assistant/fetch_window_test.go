package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
)

type countingTextFetcher struct {
	doc   apifetch.TextDocument
	calls int
}

func (f *countingTextFetcher) FetchText(context.Context, apifetch.TextRequest) (apifetch.TextDocument, error) {
	f.calls++
	return f.doc, nil
}

func fetchWindowTestContext() context.Context {
	return withToolBound(context.Background(), agenttools.ResultBound{
		MaxBytes:   8,
		Truncation: agenttools.TruncationDropTail,
	})
}

func fetchWindowResult(t *testing.T, raw string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode fetch.url result: %v", err)
	}
	return result
}

func TestExecuteFetchURLWindowsAStableDocumentWithoutRefetching(t *testing.T) {
	fetcher := &countingTextFetcher{doc: apifetch.TextDocument{
		ContentType: "text/html",
		Text:        "0123456789abcdef",
	}}
	seams := toolSeams{fetcher: fetcher, snapshots: newRunSnapshots(), runID: "run-1"}
	scope := &agenttools.URLScope{URLs: []string{"https://example.test/page"}}

	first, err := executeFetchURL(fetchWindowTestContext(), scope, json.RawMessage(`{"url":"https://example.test/page"}`), seams)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	firstResult := fetchWindowResult(t, first)
	if firstResult["text"] != "01234567" || firstResult["truncated"] != true {
		t.Fatalf("first result = %s, want the bounded first window", first)
	}
	revision, ok := firstResult["revision"].(string)
	if !ok || revision == "" {
		t.Fatalf("first result = %s, want a revision", first)
	}
	window, ok := firstResult["window"].(map[string]any)
	if !ok {
		t.Fatalf("first result = %s, want a window object", first)
	}
	endValue, ok := window["end"].(float64)
	if !ok {
		t.Fatalf("first result = %s, want a numeric window end", first)
	}
	end := int(endValue)

	secondArgs, _ := json.Marshal(map[string]any{
		"url":      "https://example.test/page",
		"revision": revision,
		"start":    end,
	})
	second, err := executeFetchURL(fetchWindowTestContext(), scope, secondArgs, seams)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	secondResult := fetchWindowResult(t, second)
	if secondResult["text"] != "89abcdef" || secondResult["truncated"] != false {
		t.Fatalf("second result = %s, want the remaining window", second)
	}
	if fetcher.calls != 1 {
		t.Fatalf("HTTP fetch calls = %d, want one snapshot fetch", fetcher.calls)
	}
	firstText, firstOK := firstResult["text"].(string)
	secondText, secondOK := secondResult["text"].(string)
	if !firstOK || !secondOK || firstText+secondText != fetcher.doc.Text {
		t.Fatal("concatenated windows did not reproduce the document")
	}
}

func TestExecuteFetchURLDoesNotSplitAMultibyteRuneAtTheWindowBoundary(t *testing.T) {
	fetcher := &countingTextFetcher{doc: apifetch.TextDocument{Text: "1234567🙂tail"}}
	seams := toolSeams{fetcher: fetcher, snapshots: newRunSnapshots(), runID: "run-rune"}
	bound := withToolBound(context.Background(), agenttools.ResultBound{MaxBytes: 10, Truncation: agenttools.TruncationDropTail})
	out, err := executeFetchURL(bound, &agenttools.URLScope{URLs: []string{"https://example.test/rune"}}, json.RawMessage(`{"url":"https://example.test/rune"}`), seams)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	result := fetchWindowResult(t, out)
	if result["text"] != "1234567" {
		t.Fatalf("text = %q, want a complete-rune boundary before the emoji", result["text"])
	}
	window, ok := result["window"].(map[string]any)
	if !ok {
		t.Fatalf("result = %s, want a window object", out)
	}
	endValue, ok := window["end"].(float64)
	if !ok || int(endValue) != len("1234567") {
		t.Fatalf("window end = %v, want %d", window["end"], len("1234567"))
	}
}

func TestExecuteFetchURLRefusesExpiredRevisionWithoutRefetching(t *testing.T) {
	fetcher := &countingTextFetcher{doc: apifetch.TextDocument{Text: "ordinary"}}
	seams := toolSeams{fetcher: fetcher, snapshots: newRunSnapshots(), runID: "run-expired"}
	scope := &agenttools.URLScope{URLs: []string{"https://example.test/page"}}
	args := json.RawMessage(`{"url":"https://example.test/page","revision":"expired","start":0}`)
	_, err := executeFetchURL(fetchWindowTestContext(), scope, args, seams)
	if err == nil || !strings.Contains(err.Error(), "restart with start 0 and no revision") {
		t.Fatalf("error = %v, want explicit restart instruction", err)
	}
	if fetcher.calls != 0 {
		t.Fatalf("HTTP fetch calls = %d, want no refetch for an expired revision", fetcher.calls)
	}
}

func TestRunSnapshotsDiscardAndRejectsOversizeDocuments(t *testing.T) {
	fetcher := &countingTextFetcher{doc: apifetch.TextDocument{Text: strings.Repeat("x", int(snapshotMaxBytes)+1)}}
	store := newRunSnapshots()
	_, err := store.Fetch(context.Background(), fetcher, "run-ceiling", "https://example.test/page", 0, "", 8)
	if err == nil || !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("oversize error = %v, want named snapshot ceiling refusal", err)
	}

	fetcher.doc.Text = "small"
	if _, fetchErr := store.Fetch(context.Background(), fetcher, "run-discard", "https://example.test/page", 0, "", 8); fetchErr != nil {
		t.Fatalf("seed snapshot: %v", fetchErr)
	}
	store.Discard("run-discard")
	_, err = store.Fetch(context.Background(), fetcher, "run-discard", "https://example.test/page", 0, "missing", 8)
	if err == nil || !strings.Contains(err.Error(), "restart with start 0 and no revision") {
		t.Fatalf("discarded revision error = %v, want expiration refusal", err)
	}
}

func TestExecuteFetchURLReportsByteAccountingForEachWindow(t *testing.T) {
	fetcher := &countingTextFetcher{doc: apifetch.TextDocument{Text: "0123456789abcdef"}}
	seams := toolSeams{fetcher: fetcher, snapshots: newRunSnapshots(), runID: "run-accounting"}
	scope := &agenttools.URLScope{URLs: []string{"https://example.test/page"}}
	out, err := executeFetchURL(fetchWindowTestContext(), scope, json.RawMessage(`{"url":"https://example.test/page"}`), seams)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	result := fetchWindowResult(t, out)
	if result["total"] != float64(16) || result["returned"] != float64(8) ||
		result["dropped"] != float64(8) || result["remaining"] != float64(8) {
		t.Fatalf("result = %s, want decoded byte accounting for window one", out)
	}
}

func TestExecuteFetchURLHandlesStartBounds(t *testing.T) {
	fetcher := &countingTextFetcher{doc: apifetch.TextDocument{Text: "ordinary"}}
	store := newRunSnapshots()
	seams := toolSeams{fetcher: fetcher, snapshots: store, runID: "run-bounds"}
	scope := &agenttools.URLScope{URLs: []string{"https://example.test/page"}}
	first, err := executeFetchURL(fetchWindowTestContext(), scope, json.RawMessage(`{"url":"https://example.test/page"}`), seams)
	if err != nil {
		t.Fatalf("seed fetch: %v", err)
	}
	firstResult := fetchWindowResult(t, first)
	revision, ok := firstResult["revision"].(string)
	if !ok || revision == "" {
		t.Fatalf("seed result = %s, want a revision", first)
	}

	_, err = executeFetchURL(fetchWindowTestContext(), scope, json.RawMessage(`{"url":"https://example.test/page","start":-1}`), seams)
	if err == nil || !strings.Contains(err.Error(), "start must not be negative") {
		t.Fatalf("negative start error = %v, want explicit refusal", err)
	}
	_, err = executeFetchURL(fetchWindowTestContext(), scope, json.RawMessage(`{"url":"https://example.test/page","revision":"`+revision+`","start":9}`), seams)
	if err == nil || !strings.Contains(err.Error(), "beyond document length") {
		t.Fatalf("past-end start error = %v, want explicit refusal", err)
	}
	end, err := executeFetchURL(fetchWindowTestContext(), scope, json.RawMessage(`{"url":"https://example.test/page","revision":"`+revision+`","start":8}`), seams)
	if err != nil {
		t.Fatalf("exact-end start: %v", err)
	}
	endResult := fetchWindowResult(t, end)
	if endResult["text"] != "" || endResult["truncated"] != false ||
		endResult["returned"] != float64(0) || endResult["remaining"] != float64(0) {
		t.Fatalf("exact-end result = %s, want an empty complete window", end)
	}
	if fetcher.calls != 1 {
		t.Fatalf("HTTP fetch calls = %d, want one", fetcher.calls)
	}
}

func TestRunSnapshotsSurviveSuspensionUntilTerminalDiscard(t *testing.T) {
	fetcher := &countingTextFetcher{doc: apifetch.TextDocument{Text: "0123456789"}}
	store := newRunSnapshots()
	first, err := store.Fetch(context.Background(), fetcher, "run-suspended", "https://example.test/page", 0, "", 5)
	if err != nil {
		t.Fatalf("initial fetch: %v", err)
	}
	if _, continuationErr := store.Fetch(context.Background(), fetcher, "run-suspended", "https://example.test/page", first.Window.End, first.Revision, 5); continuationErr != nil {
		t.Fatalf("continuation across suspension: %v", continuationErr)
	}
	if fetcher.calls != 1 {
		t.Fatalf("HTTP fetch calls while suspended = %d, want one", fetcher.calls)
	}
	store.Discard("run-suspended")
	_, err = store.Fetch(context.Background(), fetcher, "run-suspended", "https://example.test/page", 0, first.Revision, 5)
	if !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("post-discard error = %v, want expired snapshot", err)
	}
	if fetcher.calls != 1 {
		t.Fatalf("HTTP fetch calls after discard = %d, want no transparent refetch", fetcher.calls)
	}
}
