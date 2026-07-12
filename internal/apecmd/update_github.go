package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	repoOwner = "exoport"
	repoName  = "apex_process_ape"

	// maxAssetBytes caps any single release-asset download. The largest
	// asset (the linux tarball) is well under this; the cap defends the
	// updater against a hostile or corrupt Content-Length.
	maxAssetBytes = 512 << 20 // 512 MiB
)

// githubAPIBase is the GitHub REST API root. A var (not const) so hermetic
// tests can point it at an httptest server.
var githubAPIBase = "https://api.github.com"

// errNoRelease is returned when the repository has no published (non-draft,
// non-prerelease) release yet. Callers treat it as a soft "nothing to do"
// rather than a hard failure, matching the previous updater's behaviour.
var errNoRelease = errors.New("no release found")

// ghAsset is one uploaded file on a GitHub release.
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// ghRelease is the subset of the GitHub release payload the updater needs.
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Prerelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
	Assets     []ghAsset `json:"assets"`
}

// asset returns the named asset on the release, if present.
func (r *ghRelease) asset(name string) (ghAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

// latestRelease fetches the latest published release via the public GitHub
// REST API. GITHUB_TOKEN is optional — when set it raises the API rate limit
// (60/h unauthenticated → 5000/h authenticated); an empty token hits the
// public endpoint. The /releases/latest endpoint already excludes drafts and
// prereleases, so goreleaser's rc/prerelease builds are never selected.
func latestRelease(ctx context.Context, token string) (*ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", githubAPIBase, repoOwner, repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoRelease
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}

	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if rel.TagName == "" {
		return nil, errNoRelease
	}
	return &rel, nil
}

// downloadAsset fetches a release asset's bytes. Assets on a public repo need
// no auth, but the token is forwarded for rate-limit headroom. The response is
// capped at maxAssetBytes.
func downloadAsset(ctx context.Context, a ghAsset, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", a.Name, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	if int64(len(data)) > maxAssetBytes {
		return nil, fmt.Errorf("download %s: exceeds %d byte cap", a.Name, int64(maxAssetBytes))
	}
	return data, nil
}

// fetchLatestVersion returns the latest release's version string (no leading
// "v"), used by the background update probe (root.go) and the doctor
// update-available check via updatecache. Preserved as the single
// version-detection entry point.
func fetchLatestVersion(ctx context.Context, token string) (string, error) {
	rel, err := latestRelease(ctx, token)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}
