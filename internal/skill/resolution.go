package skill

import (
	"context"
	"errors"
	"fmt"
)

// Resolve turns a forge address into an immutable, short-lived install plan.
// Unsupported addresses return (nil, nil): callers keep the existing plain URL
// install path for sources this package does not enumerate.
func (s *Store) Resolve(ctx context.Context, address string) (*Resolution, error) {
	if s == nil {
		return nil, errUnavailable
	}
	if s.resolver == nil {
		return nil, nil
	}
	// A REFUSED ATTEMPT NO LONGER REVOKES ANYBODY. It had to while there was
	// one slot: a failed resolution that left the previous one in place left
	// an earlier approval spendable through a slot the caller believed it had
	// taken. Now every resolution is keyed by its own handle, so a failure
	// simply records nothing and can leave nothing behind — and revoking a
	// DIFFERENT caller's live approval because this one failed would be the
	// bug nocx-aesm2 is about, from the other end.
	plan, err := s.resolver.Resolve(ctx, address)
	if err != nil || plan == nil {
		return nil, err
	}
	handle := fmt.Sprintf("resolution-%d", s.seq.Add(1))
	s.rememberResolution(plan, handle)
	result := plan.Resolution
	result.Handle = handle
	result.Candidates = append([]ResolutionCandidate(nil), plan.Candidates...)
	return &result, nil
}

// PreviewResolved fetches the selected candidates and their bundles at the
// pinned commit. Its return carries the bytes and findings an approval window
// shows; only the digests remain in the server-side one-slot handle record.
func (s *Store) PreviewResolved(ctx context.Context, handle string, paths []string) ([]PreviewResult, error) {
	if s == nil {
		return nil, errUnavailable
	}
	if handle == "" {
		return nil, errors.New("that resolution handle is empty: resolve the repository before reading a skill")
	}
	if len(paths) == 0 {
		return nil, errors.New("skills.resolve preview requires at least one explicit candidate path")
	}
	for _, candidatePath := range paths {
		if candidatePath == "" {
			return nil, errors.New("skills.resolve preview cannot contain an empty candidate path")
		}
	}
	if duplicate := duplicatePath(paths); duplicate != "" {
		return nil, fmt.Errorf("skills.resolve preview names candidate %q more than once", duplicate)
	}
	plan, _, ok := s.approvedResolution(handle, paths)
	if !ok {
		if s.ResolutionDisplaced(handle) {
			return nil, errors.New("that resolution was displaced by newer ones:" + displacedNextStep)
		}
		return nil, errors.New("that resolution handle is no longer valid: resolve the repository again before reading a skill")
	}
	if s.resolver == nil || s.fetcher == nil {
		return nil, errors.New("reading a resolved skill is unavailable: this backend has no fetch seam wired")
	}

	previews := make([]PreviewResult, 0, len(paths))
	digests := make(map[string]string, len(paths))
	for _, candidatePath := range paths {
		rawURL, text, err := s.resolver.fetchCandidate(ctx, plan, candidatePath)
		if err != nil {
			return nil, err
		}
		document, err := documentPreview(text, rawURL)
		if err != nil {
			return nil, err
		}
		files, err := s.fetchBundle(ctx, rawURL, document.Body)
		if err != nil {
			return nil, err
		}
		whole := wholeBundle(text, files)
		document.Bundle = whole
		document.Files = bundleManifest(whole)
		document.Digest = digestOfBundle(whole)
		document.Findings = append(document.Findings, scanBundleFiles(files)...)
		// Keep the fetched candidate address in the server-built approval
		// window. The resolution handle remains the only install authority;
		// task 3 decides what address, if any, is passed to the model.
		document.URL = rawURL
		previews = append(previews, document)
		digests[candidatePath] = document.Digest
	}
	if !s.rememberResolvedDigests(handle, paths, digests) {
		return nil, errors.New("that resolution handle was replaced before its preview could be recorded")
	}
	return previews, nil
}

// InstallResolved adopts the explicitly chosen candidates from a resolution.
// It refuses unless PreviewResolved recorded every selected candidate first.
// The server uses the handle to recover each repository, commit and original
// address; the caller supplies only candidate paths it chose.
func (s *Store) InstallResolved(ctx context.Context, handle string, paths []string) ([]InstallResult, error) {
	if s == nil {
		return nil, errUnavailable
	}
	if handle == "" {
		return nil, errors.New("that resolution handle is empty: resolve the repository before installing a skill")
	}
	if len(paths) == 0 {
		return nil, errors.New("skills.install with a resolution requires at least one explicit candidate path")
	}
	for _, candidatePath := range paths {
		if candidatePath == "" {
			return nil, errors.New("skills.install with a resolution cannot contain an empty candidate path")
		}
	}
	if duplicate := duplicatePath(paths); duplicate != "" {
		return nil, fmt.Errorf("skills.install with a resolution names candidate %q more than once", duplicate)
	}
	plan, _, ok := s.approvedResolution(handle, paths)
	if !ok {
		if s.ResolutionDisplaced(handle) {
			return nil, errors.New("that resolution was displaced by newer ones:" + displacedNextStep)
		}
		return nil, errors.New("that resolution handle is no longer valid: resolve the repository again before installing a skill")
	}
	digests, approved := s.approvedResolvedDigests(handle, paths)
	if !approved {
		return nil, errors.New("nothing has been read from that resolution in this session, so there is nothing to install: read the chosen skill first, then install what you read")
	}
	if s.resolver == nil || s.fetcher == nil {
		return nil, errors.New("installing a resolved skill is unavailable: this backend has no fetch seam wired")
	}

	results := make([]InstallResult, 0, len(paths))
	for _, candidatePath := range paths {
		rawURL, err := s.resolver.candidateURL(plan, candidatePath)
		if err != nil {
			return results, fmt.Errorf("resolved install stopped after %d of %d candidates; those candidates remain installed: %w", len(results), len(paths), err)
		}
		if !s.armResolvedPath(handle, candidatePath, rawURL, digests[candidatePath]) {
			return results, fmt.Errorf("resolved install stopped after %d of %d candidates; those candidates remain installed: the resolution handle was replaced before its install could finish", len(results), len(paths))
		}
		installed, err := s.Install(ctx, rawURL)
		if err != nil {
			return results, fmt.Errorf("resolved install stopped after %d of %d candidates; those candidates remain installed: %w", len(results), len(paths), err)
		}
		results = append(results, installed)
	}
	return results, nil
}

func duplicatePath(paths []string) string {
	seen := make(map[string]struct{}, len(paths))
	for _, candidatePath := range paths {
		if candidatePath == "" {
			return ""
		}
		if _, exists := seen[candidatePath]; exists {
			return candidatePath
		}
		seen[candidatePath] = struct{}{}
	}
	return ""
}
