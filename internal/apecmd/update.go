package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/updatecache"
	"github.com/minio/selfupdate"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

// updateResult is the structured outcome of `ape update`, shared by the
// command and its renderer. Package-scoped so printUpdateResult can type-
// switch on it (a function-local type never matched, breaking human output).
type updateResult struct {
	CurrentVersion string `json:"currentVersion" yaml:"currentVersion"`
	LatestVersion  string `json:"latestVersion"  yaml:"latestVersion"`
	Updated        bool   `json:"updated"        yaml:"updated"`
	Message        string `json:"message"        yaml:"message"`
}

func newUpdateCmd() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update ape to the latest version",
		Long: "Download and install the latest ape release from GitHub.\n\n" +
			"Downloads are verified before they are applied: the release's signed\n" +
			"SHA256 manifest is checked against its keyless-cosign Sigstore bundle\n" +
			"(pinning this repository's release workflow identity and the Fulcio\n" +
			"issuer), then the downloaded archive is checked against that trusted\n" +
			"manifest. Verification is fully offline against an embedded Sigstore\n" +
			"trusted root — no cosign binary is required.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd.Context(), Version, output.Format(outputFormat))
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

// runUpdate resolves the latest release, and if newer than the running
// binary, downloads + verifies + applies it. GITHUB_TOKEN is optional (raises
// the API rate limit when set).
func runUpdate(ctx context.Context, current string, format output.Format) error {
	token := os.Getenv("GITHUB_TOKEN")

	rel, err := latestRelease(ctx, token)
	if err != nil {
		if errors.Is(err, errNoRelease) {
			fmt.Fprintln(os.Stderr, "no release found")
			return nil
		}
		return fmt.Errorf("cannot detect latest version: %w", err)
	}
	latest := trimV(rel.TagName)

	if !isNewerVersion(current, latest) {
		updatecache.Save(latest)
		return printUpdateResult(updateResult{
			CurrentVersion: current,
			LatestVersion:  latest,
			Updated:        false,
			Message:        "already up to date",
		}, format)
	}

	if err := applyUpdate(ctx, rel, token); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	updatecache.Save(latest)
	return printUpdateResult(updateResult{
		CurrentVersion: current,
		LatestVersion:  latest,
		Updated:        true,
		Message:        "updated to " + latest,
	}, format)
}

// applyUpdate downloads the platform archive, the signed checksums manifest,
// and the cosign Sigstore bundle for the release; cosign-verifies the
// manifest; verifies the archive's SHA256 against the trusted manifest;
// extracts the ape binary; and applies the self-update. Any verification
// failure aborts before the running binary is touched.
func applyUpdate(ctx context.Context, rel *ghRelease, token string) error {
	archiveName := assetName(runtime.GOOS, runtime.GOARCH)

	archiveAsset, ok := rel.asset(archiveName)
	if !ok {
		return fmt.Errorf("release %s has no asset %s", rel.TagName, archiveName)
	}
	checksumsAssetInfo, ok := rel.asset(checksumsAsset)
	if !ok {
		return fmt.Errorf("release %s has no %s", rel.TagName, checksumsAsset)
	}
	bundleAssetInfo, ok := rel.asset(bundleAsset)
	if !ok {
		return fmt.Errorf("release %s has no signature bundle %s; refusing to update unverified", rel.TagName, bundleAsset)
	}

	checksums, err := downloadAsset(ctx, checksumsAssetInfo, token)
	if err != nil {
		return err
	}
	bundleJSON, err := downloadAsset(ctx, bundleAssetInfo, token)
	if err != nil {
		return err
	}
	archive, err := downloadAsset(ctx, archiveAsset, token)
	if err != nil {
		return err
	}

	// 1. cosign-verify the checksums manifest (establishes trust in it).
	if err := verifyChecksums(checksums, bundleJSON, rel.TagName); err != nil {
		return err
	}
	// 2. verify the downloaded archive against the now-trusted manifest.
	sums, err := parseChecksums(checksums)
	if err != nil {
		return err
	}
	if err := verifyAssetChecksum(archive, archiveName, sums); err != nil {
		return err
	}
	// 3. extract the binary and self-replace.
	bin, err := extractBinary(archive, archiveName, binaryName(runtime.GOOS))
	if err != nil {
		return err
	}
	// Preserve the outgoing binary at <exe>.bak so `ape rollback` can
	// restore it. minio/selfupdate removes the old binary by default; an
	// explicit OldSavePath keeps it. A failure to resolve the path is
	// non-fatal — the update still applies, only rollback is unavailable.
	opts := selfupdate.Options{}
	if exe, err := os.Executable(); err == nil {
		opts.OldSavePath = exe + ".bak"
	}
	if err := selfupdate.Apply(bytes.NewReader(bin), opts); err != nil {
		if rb := selfupdate.RollbackError(err); rb != nil {
			return errors.Join(fmt.Errorf("apply update: %w", err), fmt.Errorf("automatic rollback also failed: %w", rb))
		}
		return fmt.Errorf("apply update: %w", err)
	}
	return nil
}

func printUpdateResult(res updateResult, format output.Format) error {
	switch format {
	case output.FormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case output.FormatYAML:
		return output.Print(os.Stdout, output.FormatYAML, res)
	default:
		fmt.Printf("current: %s\n", res.CurrentVersion)
		fmt.Printf("latest:  %s\n", res.LatestVersion)
		fmt.Printf("message: %s\n", res.Message)
		return nil
	}
}

// trimV drops a single leading "v" so release tags (v0.0.44) match the
// stamped Version format (0.0.44).
func trimV(s string) string {
	if s != "" && (s[0] == 'v' || s[0] == 'V') {
		return s[1:]
	}
	return s
}

func isNewerVersion(current, latest string) bool {
	if current == "dev" || current == "" || latest == "" {
		return false
	}
	cur := current
	if cur[0] != 'v' {
		cur = "v" + cur
	}
	lat := latest
	if lat[0] != 'v' {
		lat = "v" + lat
	}
	return semver.Compare(lat, cur) > 0
}
