package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/apifetch"
)

const (
	githubAPIBaseURL        = "https://api.github.com"
	githubRawBaseURL        = "https://raw.githubusercontent.com"
	maxGitHubTreeBytes      = 8 << 20
	maxResolutionCandidates = 128
)

// Resolution is the immutable plan returned by a forge resolver. The handle
// is the only value an install needs to refer back to this plan; the address
// and bytes it came from remain server-side.
type Resolution struct {
	Handle     string                `json:"handle"`
	Repository string                `json:"repository"`
	Ref        string                `json:"ref"`
	Commit     string                `json:"commit"`
	Candidates []ResolutionCandidate `json:"candidates"`
}

// ResolutionCandidate is one installable SKILL.md found in a resolved tree.
type ResolutionCandidate struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type githubLocation struct {
	repository string
	ref        string
	path       string
}

type githubResolutionPlan struct {
	Resolution
	address string
}

// GitHubAdapter resolves public GitHub repositories through apifetch's text
// seam. It deliberately has no HTTP client of its own: apifetch owns address
// policy, redirects, limits and transport for every request here.
type GitHubAdapter struct {
	fetcher apifetch.TextFetcher
	apiBase string
	rawBase string
}

// NewGitHubAdapter builds a resolver over the existing guarded fetch seam.
func NewGitHubAdapter(fetcher apifetch.TextFetcher) *GitHubAdapter {
	return &GitHubAdapter{fetcher: fetcher, apiBase: githubAPIBaseURL, rawBase: githubRawBaseURL}
}

// Resolve canonicalizes a GitHub address, pins its ref to a commit, enumerates
// the tree and reads bounded frontmatter from every candidate. A non-GitHub
// address returns (nil, nil), allowing the caller to retain the plain URL
// install path.
func (a *GitHubAdapter) Resolve(ctx context.Context, rawAddress string) (*githubResolutionPlan, error) {
	if a == nil || a.fetcher == nil {
		return nil, errors.New("GitHub resolution is unavailable: this backend has no fetch seam wired")
	}
	location, supported, err := canonicalizeGitHubAddress(rawAddress)
	if err != nil {
		return nil, err
	}
	if !supported {
		return nil, nil
	}

	ref := location.ref
	if ref == "" {
		var metadata struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := a.fetchJSON(ctx, a.apiURL("repos/"+location.repository), &metadata); err != nil {
			return nil, fmt.Errorf("GitHub repository metadata: %w", err)
		}
		if metadata.DefaultBranch == "" {
			return nil, errors.New("GitHub repository metadata did not name a default branch")
		}
		ref = metadata.DefaultBranch
	}

	var commit struct {
		SHA string `json:"sha"`
	}
	if err := a.fetchJSON(ctx, a.apiURL("repos/"+location.repository+"/commits/"+url.PathEscape(ref)), &commit); err != nil {
		return nil, fmt.Errorf("GitHub ref %q: %w", ref, err)
	}
	if commit.SHA == "" {
		return nil, fmt.Errorf("GitHub ref %q resolved without a commit SHA", ref)
	}

	var tree struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := a.fetchJSON(ctx, a.apiURL("repos/"+location.repository+"/git/trees/"+url.PathEscape(commit.SHA)+"?recursive=1"), &tree); err != nil {
		return nil, fmt.Errorf("GitHub commit %s tree: %w", commit.SHA, err)
	}
	if tree.Truncated {
		return nil, errors.New("GitHub tree enumeration bound reached: the repository tree was truncated")
	}

	paths := make([]string, 0)
	for _, entry := range tree.Tree {
		if entry.Type != "" && entry.Type != "blob" {
			continue
		}
		if entry.Path == "SKILL.md" || (strings.HasSuffix(entry.Path, "/SKILL.md") && strings.Count(entry.Path, "/") == 1) {
			paths = append(paths, entry.Path)
		}
	}
	sort.Strings(paths)
	if len(paths) >= maxResolutionCandidates {
		return nil, fmt.Errorf("GitHub resolution candidate bound reached: repository has %d SKILL.md candidates, the limit is %d", len(paths), maxResolutionCandidates)
	}

	address := rawAddress
	if !strings.Contains(address, "://") {
		address = "https://github.com/" + location.repository
	}
	plan := &githubResolutionPlan{
		Resolution: Resolution{Repository: location.repository, Ref: ref, Commit: commit.SHA},
		address:    address,
	}
	plan.Candidates = make([]ResolutionCandidate, 0, len(paths))
	for _, candidatePath := range paths {
		doc, err := a.fetcher.FetchText(ctx, apifetch.TextRequest{
			URL:      a.rawURL(location.repository, commit.SHA, candidatePath),
			MaxBytes: MaxFrontmatterBytes,
		})
		if err != nil {
			if errors.Is(err, apifetch.ErrTooLarge) {
				return nil, fmt.Errorf("GitHub resolution frontmatter read bound reached for %q: the first %d bytes were not enough", candidatePath, MaxFrontmatterBytes)
			}
			return nil, fmt.Errorf("GitHub candidate %q: %w", candidatePath, err)
		}
		fm, _, ok := parseFrontmatter([]byte(doc.Text))
		if !ok {
			return nil, fmt.Errorf("GitHub candidate %q has no complete SKILL.md frontmatter", candidatePath)
		}
		name := strings.TrimSpace(fm.Name)
		if name == "" || !skillNamePattern.MatchString(name) {
			return nil, fmt.Errorf("GitHub candidate %q has an unusable frontmatter name %q", candidatePath, name)
		}
		description := sanitizeDescription(fm.Description)
		if description == "" {
			return nil, fmt.Errorf("GitHub candidate %q has no description", candidatePath)
		}
		if err := checkDescriptionLength(name, description); err != nil {
			return nil, fmt.Errorf("GitHub candidate %q: %w", candidatePath, err)
		}
		plan.Candidates = append(plan.Candidates, ResolutionCandidate{
			Path:        candidatePath,
			Name:        name,
			Description: description,
		})
	}
	return plan, nil
}

func (a *GitHubAdapter) fetchCandidate(ctx context.Context, plan *githubResolutionPlan, candidatePath string) (string, string, error) {
	rawURL, err := a.candidateURL(plan, candidatePath)
	if err != nil {
		return "", "", err
	}
	doc, err := a.fetcher.FetchText(ctx, apifetch.TextRequest{URL: rawURL, MaxBytes: MaxReadBytes})
	if err != nil {
		return "", "", fmt.Errorf("GitHub candidate %q: %w", candidatePath, err)
	}
	return rawURL, doc.Text, nil
}

func (a *GitHubAdapter) candidateURL(plan *githubResolutionPlan, candidatePath string) (string, error) {
	if plan == nil || plan.Repository == "" || plan.Commit == "" {
		return "", errors.New("GitHub resolution is missing its repository or commit")
	}
	for _, candidate := range plan.Candidates {
		if candidate.Path == candidatePath {
			return a.rawURL(plan.Repository, plan.Commit, candidatePath), nil
		}
	}
	return "", fmt.Errorf("GitHub resolution does not contain candidate %q", candidatePath)
}

func (a *GitHubAdapter) fetchJSON(ctx context.Context, rawURL string, into any) error {
	doc, err := a.fetcher.FetchText(ctx, apifetch.TextRequest{URL: rawURL, MaxBytes: maxGitHubTreeBytes})
	if err != nil {
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "rate limit"), strings.Contains(message, "answered 403"), strings.Contains(message, "403 forbidden"):
			return fmt.Errorf("GitHub API rate limit reached; try again later: %w", err)
		case strings.Contains(message, "answered 404"), strings.Contains(message, "404 not found"):
			return fmt.Errorf("GitHub repository does not exist or is not public: %w", err)
		default:
			return fmt.Errorf("GitHub API request failed: %w", err)
		}
	}
	var envelope struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(doc.Text), &envelope) == nil && strings.Contains(strings.ToLower(envelope.Message), "rate limit") {
		return errors.New("GitHub API rate limit reached; try again later")
	}
	if err := json.Unmarshal([]byte(doc.Text), into); err != nil {
		return fmt.Errorf("GitHub API returned invalid JSON: %w", err)
	}
	return nil
}

func (a *GitHubAdapter) apiURL(suffix string) string {
	return strings.TrimRight(a.apiBase, "/") + "/" + strings.TrimLeft(suffix, "/")
}

func (a *GitHubAdapter) rawURL(repository, commit, candidatePath string) string {
	u, _ := url.Parse(strings.TrimRight(a.rawBase, "/"))
	u.Path = path.Join(u.Path, repository, commit, candidatePath)
	return u.String()
}

func canonicalizeGitHubAddress(raw string) (githubLocation, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return githubLocation{}, false, errors.New("a GitHub address cannot be empty")
	}
	if !strings.Contains(value, "://") {
		parts := strings.Split(strings.Trim(value, "/"), "/")
		if len(parts) == 2 && validGitHubSegment(parts[0]) && validGitHubSegment(parts[1]) {
			return githubLocation{repository: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")}, true, nil
		}
		return githubLocation{}, false, nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return githubLocation{}, false, fmt.Errorf("that GitHub address is not valid: %w", err)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return githubLocation{}, false, errors.New("a GitHub address may not carry credentials, a query, or a fragment")
	}
	parts := splitURLPath(u.Path)
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "www.github.com":
		if len(parts) < 2 || !validGitHubSegment(parts[0]) || !validGitHubSegment(parts[1]) {
			return githubLocation{}, false, errors.New("a GitHub address must name an owner and repository")
		}
		repository := parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
		if len(parts) == 2 {
			return githubLocation{repository: repository}, true, nil
		}
		if len(parts) >= 4 && (parts[2] == "blob" || parts[2] == "tree") {
			if len(parts) < 5 {
				return githubLocation{}, false, errors.New("a GitHub blob or tree address must name a ref and path")
			}
			return githubLocation{repository: repository, ref: parts[3], path: strings.Join(parts[4:], "/")}, true, nil
		}
		return githubLocation{}, false, nil
	case "raw.githubusercontent.com":
		if len(parts) < 4 || !validGitHubSegment(parts[0]) || !validGitHubSegment(parts[1]) || parts[3] == "" {
			return githubLocation{}, false, errors.New("a raw GitHub address must name an owner, repository, ref, and path")
		}
		return githubLocation{repository: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), ref: parts[2], path: strings.Join(parts[3:], "/")}, true, nil
	default:
		return githubLocation{}, false, nil
	}
}

func splitURLPath(raw string) []string {
	parts := strings.Split(strings.Trim(raw, "/"), "/")
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func validGitHubSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
