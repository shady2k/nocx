package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/storage"
)

type resolverTextFetcher struct {
	responses map[string]string
	errors    map[string]error
	requests  []apifetch.TextRequest
}

func (f *resolverTextFetcher) FetchText(_ context.Context, request apifetch.TextRequest) (apifetch.TextDocument, error) {
	f.requests = append(f.requests, request)
	if err := f.errors[request.URL]; err != nil {
		return apifetch.TextDocument{}, err
	}
	text, ok := f.responses[request.URL]
	if !ok {
		return apifetch.TextDocument{}, fmt.Errorf("no fake forge response for %s", request.URL)
	}
	return apifetch.TextDocument{URL: request.URL, Text: text}, nil
}

func newFakeGitHubAdapter(fetcher apifetch.TextFetcher) *GitHubAdapter {
	adapter := NewGitHubAdapter(fetcher)
	adapter.apiBase = "https://fake-github.test/api"
	adapter.rawBase = "https://fake-github.test/raw"
	return adapter
}

func fakeForgeResponses(adapter *GitHubAdapter, tree string, documents map[string]string) map[string]string {
	responses := map[string]string{
		adapter.apiURL("repos/acme/tools"):                                `{"default_branch":"main"}`,
		adapter.apiURL("repos/acme/tools/commits/main"):                   `{"sha":"deadbeef"}`,
		adapter.apiURL("repos/acme/tools/git/trees/deadbeef?recursive=1"): tree,
	}
	for candidatePath, document := range documents {
		responses[adapter.rawURL("acme/tools", "deadbeef", candidatePath)] = document
	}
	return responses
}

const resolverSkill = "---\nname: deploy\ndescription: Deploy the service\n---\nRun the deployment.\n"

func TestGitHubAdapter_CanonicalizesSupportedAddresses(t *testing.T) {
	cases := []struct {
		address string
		repo    string
		ref     string
		path    string
	}{
		{"acme/tools", "acme/tools", "", ""},
		{"https://github.com/acme/tools", "acme/tools", "", ""},
		{"https://github.com/acme/tools/blob/main/agent/SKILL.md", "acme/tools", "main", "agent/SKILL.md"},
		{"https://raw.githubusercontent.com/acme/tools/main/agent/SKILL.md", "acme/tools", "main", "agent/SKILL.md"},
	}
	for _, tc := range cases {
		t.Run(tc.address, func(t *testing.T) {
			got, supported, err := canonicalizeGitHubAddress(tc.address)
			if err != nil || !supported {
				t.Fatalf("canonicalize = %+v/%v, %v; want supported", got, supported, err)
			}
			if got.repository != tc.repo || got.ref != tc.ref || got.path != tc.path {
				t.Fatalf("location = %+v, want repo=%q ref=%q path=%q", got, tc.repo, tc.ref, tc.path)
			}
		})
	}
	got, supported, err := canonicalizeGitHubAddress("https://gitlab.com/acme/tools")
	if err != nil || supported || got != (githubLocation{}) {
		t.Fatalf("unsupported forge = %+v/%v, %v; want no resolution and no error", got, supported, err)
	}
}

func TestGitHubAdapter_ResolvesDefaultBranchTreeAndFrontmatter(t *testing.T) {
	adapter := newFakeGitHubAdapter(nil)
	fetcher := &resolverTextFetcher{}
	adapter.fetcher = fetcher
	fetcher.responses = fakeForgeResponses(adapter, `{"truncated":false,"tree":[{"path":"SKILL.md","type":"blob"},{"path":"agent/SKILL.md","type":"blob"},{"path":"agent/README.md","type":"blob"},{"path":"nested/agent/SKILL.md","type":"blob"}]}`, map[string]string{
		"SKILL.md":       "---\nname: root\ndescription: Root skill\n---\nroot\n",
		"agent/SKILL.md": resolverSkill,
	})
	plan, err := adapter.Resolve(context.Background(), "https://github.com/acme/tools")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plan.Repository != "acme/tools" || plan.Ref != "main" || plan.Commit != "deadbeef" {
		t.Fatalf("plan = %+v, want canonical repository, default ref and commit", plan)
	}
	if len(plan.Candidates) != 2 || plan.Candidates[0].Path != "SKILL.md" || plan.Candidates[1].Path != "agent/SKILL.md" {
		t.Fatalf("candidates = %+v, want root and one-level candidates", plan.Candidates)
	}
	if plan.Candidates[1].Name != "deploy" || plan.Candidates[1].Description != "Deploy the service" {
		t.Fatalf("candidate metadata = %+v", plan.Candidates[1])
	}
	if len(fetcher.requests) != 5 {
		t.Fatalf("fetch count = %d, want metadata + ref + tree + 2 bounded candidate reads", len(fetcher.requests))
	}
	for _, request := range fetcher.requests[3:] {
		if request.MaxBytes != MaxFrontmatterBytes {
			t.Errorf("candidate request = %+v, want MaxFrontmatterBytes=%d", request, MaxFrontmatterBytes)
		}
	}
}

func TestGitHubAdapter_RefusesCandidateBoundInsteadOfTruncating(t *testing.T) {
	adapter := newFakeGitHubAdapter(nil)
	fetcher := &resolverTextFetcher{}
	adapter.fetcher = fetcher
	entries := make([]string, 0, maxResolutionCandidates+1)
	for i := 0; i <= maxResolutionCandidates; i++ {
		entries = append(entries, fmt.Sprintf(`{"path":"skill-%03d/SKILL.md","type":"blob"}`, i))
	}
	fetcher.responses = fakeForgeResponses(adapter, `{"truncated":false,"tree":[`+strings.Join(entries, ",")+`]}`, nil)
	_, err := adapter.Resolve(context.Background(), "acme/tools")
	if err == nil || !strings.Contains(err.Error(), "candidate bound") {
		t.Fatalf("Resolve error = %v, want named candidate bound refusal", err)
	}
	if len(fetcher.requests) != 3 {
		t.Fatalf("fetch count = %d, want metadata + ref + tree before bound refusal", len(fetcher.requests))
	}
}

func TestGitHubAdapter_RefusesCandidateReadBoundInsteadOfTruncating(t *testing.T) {
	adapter := newFakeGitHubAdapter(nil)
	fetcher := &resolverTextFetcher{}
	adapter.fetcher = fetcher
	candidateURL := adapter.rawURL("acme/tools", "deadbeef", "agent/SKILL.md")
	fetcher.responses = fakeForgeResponses(adapter, `{"truncated":false,"tree":[{"path":"agent/SKILL.md","type":"blob"}]}`, nil)
	fetcher.errors = map[string]error{candidateURL: fmt.Errorf("fake: %w", apifetch.ErrTooLarge)}
	_, err := adapter.Resolve(context.Background(), "acme/tools")
	if err == nil || !strings.Contains(err.Error(), "frontmatter read bound") {
		t.Fatalf("Resolve error = %v, want named per-candidate bound refusal", err)
	}
}

func TestGitHubAdapter_RateLimitNamesTheRateLimit(t *testing.T) {
	adapter := newFakeGitHubAdapter(nil)
	fetcher := &resolverTextFetcher{errors: map[string]error{
		adapter.apiURL("repos/acme/tools"): errors.New("fake forge answered 403 Forbidden"),
	}}
	adapter.fetcher = fetcher
	_, err := adapter.Resolve(context.Background(), "acme/tools")
	if err == nil || !strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Resolve error = %v, want a rate-limit refusal distinct from missing repository", err)
	}
}

func TestGitHubAdapter_EachForgeCallFailureIsNamed(t *testing.T) {
	cases := []struct {
		name    string
		address string
		failURL func(*GitHubAdapter) string
		want    string
	}{
		{"metadata", "acme/tools", func(a *GitHubAdapter) string { return a.apiURL("repos/acme/tools") }, "repository metadata"},
		{"ref", "https://github.com/acme/tools/blob/main/agent/SKILL.md", func(a *GitHubAdapter) string { return a.apiURL("repos/acme/tools/commits/main") }, "GitHub ref"},
		{"tree", "https://github.com/acme/tools/blob/main/agent/SKILL.md", func(a *GitHubAdapter) string { return a.apiURL("repos/acme/tools/git/trees/deadbeef?recursive=1") }, "commit deadbeef tree"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := newFakeGitHubAdapter(nil)
			fetcher := &resolverTextFetcher{responses: map[string]string{}, errors: map[string]error{}}
			adapter.fetcher = fetcher
			fetcher.responses = fakeForgeResponses(adapter, `{"truncated":false,"tree":[]}`, nil)
			fetcher.errors[tc.failURL(adapter)] = errors.New("fake forge unavailable")
			_, err := adapter.Resolve(context.Background(), tc.address)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Resolve error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestStore_UnsupportedResolutionFallsBackWithoutError(t *testing.T) {
	store := NewStore(nil, nil, nil, WithGitHubAdapter(newFakeGitHubAdapter(&resolverTextFetcher{})))
	resolution, err := store.Resolve(context.Background(), "https://gitlab.com/acme/tools")
	if err != nil || resolution != nil {
		t.Fatalf("Resolve unsupported = %+v, %v; want nil result and nil error", resolution, err)
	}
}

func TestStore_ResolutionHandleIsReplacedAndSpentByResolvedInstall(t *testing.T) {
	adapter := newFakeGitHubAdapter(nil)
	fetcher := &resolverTextFetcher{}
	adapter.fetcher = fetcher
	fetcher.responses = fakeForgeResponses(adapter, `{"truncated":false,"tree":[{"path":"agent/SKILL.md","type":"blob"}]}`, map[string]string{"agent/SKILL.md": resolverSkill})

	configDir := t.TempDir()
	installedDir := filepath.Join(configDir, "installed-skills")
	store := NewStore(OSFileSystem{}, []Root{{Dir: installedDir, Provenance: ProvenanceInstalled}}, storage.NewDocumentStore(configDir), WithGitHubAdapter(adapter))
	first, err := store.Resolve(context.Background(), "acme/tools")
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	second, err := store.Resolve(context.Background(), "acme/tools")
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if first.Handle == second.Handle {
		t.Fatalf("handles = %q and %q, want replacement", first.Handle, second.Handle)
	}
	if _, installErr := store.InstallResolved(context.Background(), first.Handle, []string{"agent/SKILL.md"}); installErr == nil || !strings.Contains(installErr.Error(), "no longer valid") {
		t.Fatalf("stale handle error = %v, want recoverable stale-handle refusal", installErr)
	}
	if _, installErr := store.InstallResolved(context.Background(), second.Handle, []string{"agent/SKILL.md"}); installErr == nil || !strings.Contains(installErr.Error(), "nothing has been read") {
		t.Fatalf("unread resolution error = %v, want read-before-install refusal", installErr)
	}
	previews, err := store.PreviewResolved(context.Background(), second.Handle, []string{"agent/SKILL.md"})
	if err != nil || len(previews) != 1 || previews[0].Body == "" || previews[0].URL != adapter.rawURL("acme/tools", "deadbeef", "agent/SKILL.md") {
		t.Fatalf("PreviewResolved = %+v, %v; want fetched candidate address and bytes", previews, err)
	}
	if _, installErr := store.InstallResolved(context.Background(), second.Handle, []string{"agent/SKILL.md"}); installErr != nil {
		t.Fatalf("InstallResolved: %v", installErr)
	}
	source, recorded, err := store.recordedSource("deploy")
	if err != nil || !recorded || source.URL != adapter.rawURL("acme/tools", "deadbeef", "agent/SKILL.md") || source.EntryURL != "https://github.com/acme/tools" || source.Path != "agent/SKILL.md" || source.Ref != "main" || source.Commit != "deadbeef" {
		t.Fatalf("recorded source = %+v/%v/%v, want fetched URL, entry URL, path, ref and commit", source, recorded, err)
	}
	if _, err := store.InstallResolved(context.Background(), second.Handle, []string{"agent/SKILL.md"}); err == nil || !strings.Contains(err.Error(), "no longer valid") {
		t.Fatalf("spent handle error = %v, want handle-spent refusal", err)
	}
}

func TestStore_ResolvedInstallAcceptsAnExplicitSet(t *testing.T) {
	adapter := newFakeGitHubAdapter(nil)
	fetcher := &resolverTextFetcher{}
	adapter.fetcher = fetcher
	fetcher.responses = fakeForgeResponses(adapter, `{"truncated":false,"tree":[{"path":"one/SKILL.md","type":"blob"},{"path":"two/SKILL.md","type":"blob"}]}`, map[string]string{
		"one/SKILL.md": "---\nname: one\ndescription: First skill\n---\nfirst\n",
		"two/SKILL.md": "---\nname: two\ndescription: Second skill\n---\nsecond\n",
	})
	configDir := t.TempDir()
	store := NewStore(OSFileSystem{}, []Root{{Dir: filepath.Join(configDir, "installed-skills"), Provenance: ProvenanceInstalled}}, storage.NewDocumentStore(configDir), WithGitHubAdapter(adapter))
	resolution, err := store.Resolve(context.Background(), "acme/tools")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	paths := []string{"one/SKILL.md", "two/SKILL.md"}
	previews, err := store.PreviewResolved(context.Background(), resolution.Handle, paths)
	if err != nil || len(previews) != 2 {
		t.Fatalf("PreviewResolved = %d/%v, want two previews", len(previews), err)
	}
	installed, err := store.InstallResolved(context.Background(), resolution.Handle, paths)
	if err != nil {
		t.Fatalf("InstallResolved: %v", err)
	}
	if len(installed) != 2 || installed[0].Name != "one" || installed[1].Name != "two" {
		t.Fatalf("installed = %+v, want both explicitly chosen skills", installed)
	}
	if _, err := store.InstallResolved(context.Background(), resolution.Handle, paths); err == nil || !strings.Contains(err.Error(), "no longer valid") {
		t.Fatalf("spent multi-candidate handle error = %v, want handle-spent refusal after the last candidate", err)
	}
}

func TestStore_ResolvedInstallReportsLandedCandidatesOnFailure(t *testing.T) {
	adapter := newFakeGitHubAdapter(nil)
	fetcher := &resolverTextFetcher{errors: map[string]error{}}
	adapter.fetcher = fetcher
	fetcher.responses = fakeForgeResponses(adapter, `{"truncated":false,"tree":[{"path":"one/SKILL.md","type":"blob"},{"path":"two/SKILL.md","type":"blob"},{"path":"three/SKILL.md","type":"blob"}]}`, map[string]string{
		"one/SKILL.md":   "---\nname: one\ndescription: First skill\n---\nfirst\n",
		"two/SKILL.md":   "---\nname: two\ndescription: Second skill\n---\nsecond\n",
		"three/SKILL.md": "---\nname: three\ndescription: Third skill\n---\nthird\n",
	})
	configDir := t.TempDir()
	installedDir := filepath.Join(configDir, "installed-skills")
	store := NewStore(OSFileSystem{}, []Root{{Dir: installedDir, Provenance: ProvenanceInstalled}}, storage.NewDocumentStore(configDir), WithGitHubAdapter(adapter))
	resolution, err := store.Resolve(context.Background(), "acme/tools")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	paths := []string{"one/SKILL.md", "two/SKILL.md", "three/SKILL.md"}
	if _, previewErr := store.PreviewResolved(context.Background(), resolution.Handle, paths); previewErr != nil {
		t.Fatalf("PreviewResolved: %v", previewErr)
	}
	fetcher.errors[adapter.rawURL("acme/tools", "deadbeef", "two/SKILL.md")] = errors.New("fake candidate unavailable")

	installed, err := store.InstallResolved(context.Background(), resolution.Handle, paths)
	if err == nil || !strings.Contains(err.Error(), "stopped after 1 of 3 candidates") {
		t.Fatalf("InstallResolved error = %v, want landed-count refusal", err)
	}
	if len(installed) != 1 || installed[0].Name != "one" {
		t.Fatalf("reported installed = %+v, want the first landed candidate", installed)
	}
	if _, err := os.Stat(filepath.Join(installedDir, "one", "SKILL.md")); err != nil {
		t.Fatalf("first candidate on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installedDir, "two", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("failed middle candidate on disk error = %v, want absent", err)
	}
	if source, recorded, err := store.recordedSource("one"); err != nil || !recorded || source.Path != "one/SKILL.md" {
		t.Fatalf("first source = %+v/%v/%v, want recorded landed candidate", source, recorded, err)
	}
	if _, recorded, err := store.recordedSource("two"); err != nil || recorded {
		t.Fatalf("failed source = %v/%v, want no source row", recorded, err)
	}
}
