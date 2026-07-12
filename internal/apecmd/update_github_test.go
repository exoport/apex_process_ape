package apecmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newReleaseServer stands up an httptest server that serves a
// /releases/latest payload plus asset downloads, and points githubAPIBase at
// it for the duration of the test.
func newReleaseServer(t *testing.T, status int, body string, gotAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		switch {
		case r.URL.Path == "/repos/exoport/apex_process_ape/releases/latest":
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, body)
		case r.URL.Path == "/download/asset.bin":
			_, _ = fmt.Fprint(w, "asset-bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	orig := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() {
		githubAPIBase = orig
		srv.Close()
	})
	return srv
}

func TestLatestRelease(t *testing.T) {
	var gotAuth string
	newReleaseServer(t, http.StatusOK, `{
		"tag_name": "v0.0.44",
		"prerelease": false,
		"draft": false,
		"assets": [
			{"name": "ape_linux_amd64.tar.gz", "browser_download_url": "https://example.invalid/a", "size": 11},
			{"name": "ape_checksums.txt", "browser_download_url": "https://example.invalid/b", "size": 11}
		]
	}`, &gotAuth)

	rel, err := latestRelease(context.Background(), "tok-123")
	if err != nil {
		t.Fatalf("latestRelease: %v", err)
	}
	if rel.TagName != "v0.0.44" {
		t.Errorf("tag = %q, want v0.0.44", rel.TagName)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", gotAuth)
	}
	if _, ok := rel.asset("ape_checksums.txt"); !ok {
		t.Error("expected ape_checksums.txt asset")
	}
	if _, ok := rel.asset("nope"); ok {
		t.Error("did not expect a nonexistent asset to resolve")
	}
}

func TestLatestRelease_NotFound(t *testing.T) {
	newReleaseServer(t, http.StatusNotFound, `{"message":"Not Found"}`, nil)
	_, err := latestRelease(context.Background(), "")
	if !errors.Is(err, errNoRelease) {
		t.Errorf("got %v, want errNoRelease", err)
	}
}

func TestLatestRelease_ServerError(t *testing.T) {
	newReleaseServer(t, http.StatusInternalServerError, `boom`, nil)
	_, err := latestRelease(context.Background(), "")
	if err == nil || errors.Is(err, errNoRelease) {
		t.Errorf("got %v, want a hard error", err)
	}
}

func TestFetchLatestVersion_StripsV(t *testing.T) {
	newReleaseServer(t, http.StatusOK, `{"tag_name":"v1.2.3","assets":[]}`, nil)
	v, err := fetchLatestVersion(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchLatestVersion: %v", err)
	}
	if v != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3 (leading v stripped)", v)
	}
}

func TestDownloadAsset(t *testing.T) {
	srv := newReleaseServer(t, http.StatusOK, `{}`, nil)
	data, err := downloadAsset(context.Background(), ghAsset{
		Name:               "asset.bin",
		BrowserDownloadURL: srv.URL + "/download/asset.bin",
	}, "")
	if err != nil {
		t.Fatalf("downloadAsset: %v", err)
	}
	if string(data) != "asset-bytes" {
		t.Errorf("downloaded %q, want asset-bytes", data)
	}
}

func TestDownloadAsset_NonOK(t *testing.T) {
	srv := newReleaseServer(t, http.StatusOK, `{}`, nil)
	_, err := downloadAsset(context.Background(), ghAsset{
		Name:               "missing.bin",
		BrowserDownloadURL: srv.URL + "/download/missing.bin",
	}, "")
	if err == nil {
		t.Error("expected error for a 404 asset download")
	}
}
