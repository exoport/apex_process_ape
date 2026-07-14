package apecmd

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// trustedRootJSON is the Sigstore public-good trusted root (Fulcio CA roots,
// Rekor + CT log keys, TSA roots). It is embedded so `ape update` verifies
// releases fully offline — no TUF fetch, no cosign binary, no ~/.sigstore
// cache. Refresh it (fetch the current `trusted_root.json` TUF target from
// https://tuf-repo-cdn.sigstore.dev) only if Sigstore rotates its roots; the
// current public-good roots are valid for years and retain historical key
// material with validity windows, so old releases keep verifying.
//
//go:embed trusted_root.json
var trustedRootJSON []byte

const (
	// fulcioOIDCIssuer is the OIDC issuer our release workflow authenticates
	// to Fulcio with — GitHub Actions' token issuer.
	fulcioOIDCIssuer = "https://token.actions.githubusercontent.com"

	// checksumsAsset is the signed SHA256 manifest; bundleAsset is its
	// keyless-cosign Sigstore bundle (cert + signature + SCT + Rekor proof).
	checksumsAsset = "ape_checksums.txt"
	bundleAsset    = "ape_checksums.txt.bundle"
)

// releaseIdentity is the exact Fulcio certificate SAN our release workflow's
// OIDC identity carries for a given tag. Pinning the full string (not a
// regex) means a signature made by any other workflow, repo, or ref is
// rejected.
func releaseIdentity(tag string) string {
	return fmt.Sprintf("https://github.com/%s/%s/.github/workflows/release.yml@refs/tags/%s", repoOwner, repoName, tag)
}

// verifyChecksums cosign-verifies the checksums file against its Sigstore
// bundle, offline, against the embedded trusted root. It pins the release.yml
// SAN identity for the resolved tag and the Fulcio OIDC issuer, and binds the
// signature to the checksums bytes (WithArtifact). It requires a verified
// Fulcio cert chain, an SCT (certificate transparency), a Rekor inclusion
// proof, and an observer (Rekor integrated) timestamp — the full keyless
// guarantee, matching `cosign verify-blob`.
func verifyChecksums(checksums, bundleJSON []byte, tag string) error {
	trustedRoot, err := root.NewTrustedRootFromJSON(trustedRootJSON)
	if err != nil {
		return fmt.Errorf("load embedded trusted root: %w", err)
	}

	verifier, err := verify.NewVerifier(
		trustedRoot,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithObserverTimestamps(1),
		verify.WithTransparencyLog(1),
	)
	if err != nil {
		return fmt.Errorf("create verifier: %w", err)
	}

	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("parse signature bundle: %w", err)
	}

	certID, err := verify.NewShortCertificateIdentity(fulcioOIDCIssuer, "", releaseIdentity(tag), "")
	if err != nil {
		return fmt.Errorf("build identity policy: %w", err)
	}

	_, err = verifier.Verify(&b, verify.NewPolicy(
		verify.WithArtifact(bytes.NewReader(checksums)),
		verify.WithCertificateIdentity(certID),
	))
	if err != nil {
		return fmt.Errorf("cosign verification of %s failed: %w", checksumsAsset, err)
	}
	return nil
}

// parseChecksums parses the sha256sum-format manifest (one "<hex>  <name>"
// per line) into a name→hex map.
func parseChecksums(data []byte) (map[string]string, error) {
	sums := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// "<hex>  <name>" (sha256sum uses two spaces / " *"); Fields
		// collapses whitespace and the "*" binary-mode marker never
		// appears in goreleaser output, so exactly two fields is correct.
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed checksum line: %q", line)
		}
		sums[fields[1]] = strings.ToLower(fields[0])
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(sums) == 0 {
		return nil, errors.New("no checksums parsed")
	}
	return sums, nil
}

// verifyAssetChecksum confirms the asset bytes match the trusted SHA256 for
// its filename. sums must come from a cosign-verified checksums file.
func verifyAssetChecksum(asset []byte, name string, sums map[string]string) error {
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("no checksum for %s in the signed manifest", name)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(asset))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return nil
}
