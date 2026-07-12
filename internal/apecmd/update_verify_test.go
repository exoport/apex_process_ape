package apecmd

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureDir holds the real v0.0.43 release verification fixtures (see its
// README.md for provenance). They exercise the exact offline verification
// path with no network access.
const fixtureDir = "testdata/update/v0.0.43"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// TestVerifyChecksums_RealRelease is the payoff hermetic test: it verifies the
// genuine v0.0.43 keyless-cosign signature offline, against the embedded
// public-good trusted root, pinning this repo's release identity. It proves
// the production verification path end-to-end without a network or a cosign
// binary. Deterministic across time because sigstore-go validates the Fulcio
// cert against the Rekor integrated (signing) time, not the wall clock.
func TestVerifyChecksums_RealRelease(t *testing.T) {
	checksums := readFixture(t, "ape_checksums.txt")
	bundleJSON := readFixture(t, "ape_checksums.txt.bundle")

	if err := verifyChecksums(checksums, bundleJSON, "v0.0.43"); err != nil {
		t.Fatalf("verifyChecksums on real v0.0.43 artifacts: %v", err)
	}
}

// TestVerifyChecksums_WrongTagIdentity ensures identity pinning is tag-exact:
// the v0.0.43 signature must not verify when we expect a different tag's SAN.
func TestVerifyChecksums_WrongTagIdentity(t *testing.T) {
	checksums := readFixture(t, "ape_checksums.txt")
	bundleJSON := readFixture(t, "ape_checksums.txt.bundle")

	if err := verifyChecksums(checksums, bundleJSON, "v9.9.9"); err == nil {
		t.Fatal("expected verification to fail for a mismatched release identity, got nil")
	}
}

// TestVerifyChecksums_TamperedArtifact ensures the signature is bound to the
// checksums bytes: a single flipped byte must break verification (so a
// swapped-in malicious manifest cannot pass).
func TestVerifyChecksums_TamperedArtifact(t *testing.T) {
	checksums := readFixture(t, "ape_checksums.txt")
	bundleJSON := readFixture(t, "ape_checksums.txt.bundle")

	tampered := make([]byte, len(checksums))
	copy(tampered, checksums)
	tampered[0] ^= 0xff

	if err := verifyChecksums(tampered, bundleJSON, "v0.0.43"); err == nil {
		t.Fatal("expected verification to fail for a tampered artifact, got nil")
	}
}

// TestVerifyChecksums_CorruptBundle ensures a malformed bundle is rejected at
// parse time rather than silently skipping verification.
func TestVerifyChecksums_CorruptBundle(t *testing.T) {
	checksums := readFixture(t, "ape_checksums.txt")

	if err := verifyChecksums(checksums, []byte("{not a bundle"), "v0.0.43"); err == nil {
		t.Fatal("expected verification to fail for a corrupt bundle, got nil")
	}
}

func TestReleaseIdentity(t *testing.T) {
	got := releaseIdentity("v0.0.43")
	want := "https://github.com/exoport/apex_process_ape/.github/workflows/release.yml@refs/tags/v0.0.43"
	if got != want {
		t.Errorf("releaseIdentity = %q, want %q", got, want)
	}
}

func TestParseChecksums(t *testing.T) {
	data := readFixture(t, "ape_checksums.txt")
	sums, err := parseChecksums(data)
	if err != nil {
		t.Fatalf("parseChecksums: %v", err)
	}
	// The real manifest lists all six platform archives.
	if len(sums) != 6 {
		t.Errorf("parsed %d checksums, want 6", len(sums))
	}
	got, ok := sums["ape_linux_amd64.tar.gz"]
	if !ok {
		t.Fatal("missing ape_linux_amd64.tar.gz entry")
	}
	want := "b53555090a23340d2ea3a790d2199c56cb9cd9fd1dac2f99088570eb22c0810f"
	if got != want {
		t.Errorf("linux amd64 sum = %q, want %q", got, want)
	}
}

func TestParseChecksums_Malformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"single-field", "deadbeef\n"},
		{"three-fields", "deadbeef  file  extra\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseChecksums([]byte(tc.in)); err == nil {
				t.Errorf("parseChecksums(%q) = nil error, want error", tc.in)
			}
		})
	}
}

func TestVerifyAssetChecksum(t *testing.T) {
	sums := map[string]string{
		// sha256("hello\n")
		"ape_linux_amd64.tar.gz": "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
	}
	asset := []byte("hello\n")

	if err := verifyAssetChecksum(asset, "ape_linux_amd64.tar.gz", sums); err != nil {
		t.Errorf("matching checksum should pass: %v", err)
	}
	if err := verifyAssetChecksum([]byte("tampered"), "ape_linux_amd64.tar.gz", sums); err == nil {
		t.Error("mismatched checksum should fail")
	}
	if err := verifyAssetChecksum(asset, "ape_darwin_arm64.tar.gz", sums); err == nil {
		t.Error("missing checksum entry should fail")
	}
}
