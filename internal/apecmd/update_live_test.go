package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestLive_UpdateVerifiesPublishedRelease is an opt-in, live smoke test that
// exercises the real `ape update` download + verification chain against the
// latest PUBLISHED GitHub release: it resolves the release, downloads the
// platform archive + signed checksums manifest + cosign Sigstore bundle,
// cosign-verifies the manifest (pinning this repo's release identity + the
// Fulcio issuer, offline against the embedded trusted root), and verifies the
// archive's SHA256 against the trusted manifest. It stops short of applying
// the update — it verifies into a temp dir and never replaces the test binary.
//
// It is NOT hermetic (needs network + GitHub) so it is gated behind
// APE_UPDATE_LIVE=1 and skipped by default (never runs in `make test` / CI):
//
//	APE_UPDATE_LIVE=1 go test ./internal/apecmd/ -run TestLive_UpdateVerifiesPublishedRelease -v
//
// Releases from v0.0.44 onward ship the `ape_checksums.txt.bundle` asset. If
// the current latest release predates it (e.g. v0.0.43), the test skips with
// an explanatory message rather than failing.
func TestLive_UpdateVerifiesPublishedRelease(t *testing.T) {
	if os.Getenv("APE_UPDATE_LIVE") != "1" {
		t.Skip("set APE_UPDATE_LIVE=1 (needs network + GitHub) to run the live update-verification smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	token := os.Getenv("GITHUB_TOKEN")
	rel, err := latestRelease(ctx, token)
	if err != nil {
		t.Fatalf("resolve latest release: %v", err)
	}
	t.Logf("latest published release: %s", rel.TagName)

	bundleInfo, ok := rel.asset(bundleAsset)
	if !ok {
		t.Skipf("release %s has no %s yet (pre-bundle release); nothing to verify live", rel.TagName, bundleAsset)
	}
	checksumsInfo, ok := rel.asset(checksumsAsset)
	if !ok {
		t.Fatalf("release %s has no %s", rel.TagName, checksumsAsset)
	}
	archiveName := assetName(runtime.GOOS, runtime.GOARCH)
	archiveInfo, ok := rel.asset(archiveName)
	if !ok {
		t.Fatalf("release %s has no asset %s for this platform", rel.TagName, archiveName)
	}

	checksums, err := downloadAsset(ctx, checksumsInfo, token)
	if err != nil {
		t.Fatalf("download checksums: %v", err)
	}
	bundleJSON, err := downloadAsset(ctx, bundleInfo, token)
	if err != nil {
		t.Fatalf("download bundle: %v", err)
	}
	archive, err := downloadAsset(ctx, archiveInfo, token)
	if err != nil {
		t.Fatalf("download archive: %v", err)
	}

	// cosign-verify the manifest against the real release identity.
	if err := verifyChecksums(checksums, bundleJSON, rel.TagName); err != nil {
		t.Fatalf("cosign verification of the published %s failed: %v", checksumsAsset, err)
	}

	// checksum-verify the archive against the now-trusted manifest.
	sums, err := parseChecksums(checksums)
	if err != nil {
		t.Fatalf("parse checksums: %v", err)
	}
	if err := verifyAssetChecksum(archive, archiveName, sums); err != nil {
		t.Fatalf("archive checksum verification failed: %v", err)
	}

	// Extract into a temp dir to prove the archive yields a real binary,
	// WITHOUT applying the self-update over the test binary.
	bin, err := extractBinary(archive, archiveName, binaryName(runtime.GOOS))
	if err != nil {
		t.Fatalf("extract binary: %v", err)
	}
	if len(bin) == 0 {
		t.Fatal("extracted an empty binary")
	}
	out := filepath.Join(t.TempDir(), binaryName(runtime.GOOS))
	if err := os.WriteFile(out, bin, 0o600); err != nil {
		t.Fatalf("write extracted binary: %v", err)
	}
	t.Logf("verified + extracted %s (%d bytes) from %s", archiveName, len(bin), rel.TagName)
}
