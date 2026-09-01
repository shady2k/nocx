package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/apifetch"
)

// snapshotMaxBytes is the absolute size of one in-memory decoded document.
// It is separate from the 64 KiB result window so windowing cannot turn a
// bounded fetch into an unbounded allocation. The acquisition seam receives
// this ceiling and refuses while reading, before retaining a larger body.
const snapshotMaxBytes int64 = 8 << 20

var (
	ErrSnapshotTooLarge = errors.New("fetch.url: snapshot exceeds its absolute size ceiling")
	ErrSnapshotExpired  = errors.New("fetch.url: snapshot expired; restart with start 0 and no revision")
)

type fetchURLResult struct {
	URL         string         `json:"url"`
	ContentType string         `json:"contentType"`
	Total       int64          `json:"total"`
	Revision    string         `json:"revision"`
	Window      fetchURLWindow `json:"window"`
	Returned    int64          `json:"returned"`
	Text        string         `json:"text"`
	Truncated   bool           `json:"truncated"`
	Dropped     int64          `json:"dropped"`
	Remaining   int64          `json:"remaining"`
	Lossy       bool           `json:"lossy"`
}

type fetchURLWindow struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type runSnapshot struct {
	URL         string
	ContentType string
	Text        string
	Lossy       bool
}

// runSnapshots is process-lifetime, keyed by run id and opaque revision. A
// snapshot exists from the first fetch of a URL in a run until that run
// terminalizes; Discard is the closing event. It is mutex-guarded because one
// assistant client serves concurrent runs. Nothing is persisted: a restart
// legitimately loses suspended snapshots, just as it loses checkpoints.
type runSnapshots struct {
	mu   sync.Mutex
	docs map[string]map[string]runSnapshot
}

func newRunSnapshots() *runSnapshots {
	return &runSnapshots{docs: make(map[string]map[string]runSnapshot)}
}

func (s *runSnapshots) Fetch(ctx context.Context, fetcher apifetch.TextFetcher, runID, rawURL string, start int64, revision string, maxBytes int64) (fetchURLResult, error) {
	if start < 0 {
		return fetchURLResult{}, errors.New("fetch.url: start must not be negative")
	}
	if maxBytes <= 0 {
		return fetchURLResult{}, errors.New("fetch.url: result window must be positive")
	}
	if revision != "" {
		doc, ok := s.lookup(runID, revision)
		if !ok {
			return fetchURLResult{}, ErrSnapshotExpired
		}
		if doc.URL != rawURL {
			return fetchURLResult{}, fmt.Errorf("fetch.url: revision belongs to a different URL")
		}
		return makeFetchURLResult(rawURL, revision, doc, start, maxBytes)
	}
	if start != 0 {
		return fetchURLResult{}, errors.New("fetch.url: start beyond the first window requires revision")
	}
	if fetcher == nil {
		return fetchURLResult{}, errors.New("fetch.url: URL fetcher is unavailable")
	}
	doc, err := fetcher.FetchText(ctx, apifetch.TextRequest{URL: rawURL, MaxBytes: snapshotMaxBytes})
	if err != nil {
		return fetchURLResult{}, err
	}
	text := renderFetchedDocument(doc)
	if int64(len(text)) > snapshotMaxBytes {
		return fetchURLResult{}, ErrSnapshotTooLarge
	}
	revision, err = newSnapshotRevision()
	if err != nil {
		return fetchURLResult{}, fmt.Errorf("fetch.url: mint snapshot revision: %w", err)
	}
	snapshot := runSnapshot{
		URL:         rawURL,
		ContentType: doc.ContentType,
		Text:        text,
		Lossy:       doc.Lossy,
	}
	s.store(runID, revision, snapshot)
	return makeFetchURLResult(rawURL, revision, snapshot, start, maxBytes)
}

func (s *runSnapshots) lookup(runID, revision string) (runSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	docs := s.docs[runID]
	doc, ok := docs[revision]
	return doc, ok
}

func (s *runSnapshots) store(runID, revision string, doc runSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.docs[runID] == nil {
		s.docs[runID] = make(map[string]runSnapshot)
	}
	s.docs[runID][revision] = doc
}

func (s *runSnapshots) Discard(runID string) {
	if runID == "" {
		return
	}
	s.mu.Lock()
	delete(s.docs, runID)
	s.mu.Unlock()
}

func newSnapshotRevision() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func makeFetchURLResult(rawURL, revision string, doc runSnapshot, start, maxBytes int64) (fetchURLResult, error) {
	total := int64(len(doc.Text))
	if start > total {
		return fetchURLResult{}, fmt.Errorf("fetch.url: start %d is beyond document length %d", start, total)
	}
	end := windowEnd(doc.Text, start, maxBytes)
	text := doc.Text[start:end]
	return fetchURLResult{
		URL:         rawURL,
		ContentType: doc.ContentType,
		Total:       total,
		Revision:    revision,
		Window:      fetchURLWindow{Start: start, End: end},
		Returned:    end - start,
		Text:        text,
		Truncated:   end < total,
		Dropped:     total - end,
		Remaining:   total - end,
		Lossy:       doc.Lossy,
	}, nil
}

func windowEnd(text string, start, maxBytes int64) int64 {
	total := int64(len(text))
	end := start + maxBytes
	if end < start || end > total {
		end = total
	}
	if end < total {
		for end > start && !utf8.ValidString(text[start:end]) {
			end--
		}
		if line := strings.LastIndexByte(text[start:end], '\n'); line >= 0 {
			end = start + int64(line) + 1
		}
	}
	if end == start && start < total {
		_, size := utf8.DecodeRuneInString(text[start:])
		end = start + int64(size)
	}
	return end
}
