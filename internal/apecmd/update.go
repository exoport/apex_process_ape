package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/sandbox"
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
	var (
		outputFormat string
		pin          bool
	)

	cmd := &cobra.Command{
		Use: "update",
		// Takes none: without this, a stray argument is silently ignored
		// and the command answers 0 to an invocation nobody meant.
		Args:  cobra.NoArgs,
		Short: "Update ape to the latest version",
		Long: "Download and install the latest ape release from GitHub.\n\n" +
			"Downloads are verified before they are applied: the release's signed\n" +
			"SHA256 manifest is checked against its keyless-cosign Sigstore bundle\n" +
			"(pinning this repository's release workflow identity and the Fulcio\n" +
			"issuer), then the downloaded archive is checked against that trusted\n" +
			"manifest. Verification is fully offline against an embedded Sigstore\n" +
			"trusted root — no cosign binary is required.\n\n" +
			"It updates the binary that runs it — the ape on PATH — and never a\n" +
			"project's bingo pin (.bingo/ape.mod): that is a tracked, team-wide\n" +
			"change. Inside a pinned project whose pin is behind, it says how to\n" +
			"move the pin; --pin also moves it, committed on its own as\n" +
			"'chore(ape): update ape to vA.B.C'. A launcher that is itself a bingo\n" +
			"pin is not self-updated: --pin moves the project's pin instead.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd.Context(), Version, output.Format(outputFormat), pin)
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	cmd.Flags().BoolVar(&pin, "pin", false,
		"Also move this project's bingo pin (.bingo/ape.mod) to the latest release and commit it as chore(ape)")
	return cmd
}

// runUpdate resolves the latest release, and if newer than the running
// binary, downloads + verifies + applies it. GITHUB_TOKEN is optional (raises
// the API rate limit when set).
func runUpdate(ctx context.Context, current string, format output.Format, movePinToo bool) error {
	if err := refuseDeliveredSelfUpdate(); err != nil {
		return err
	}
	var projectPin *apePin
	if cwd, err := os.Getwd(); err == nil {
		projectPin, _ = findApePin(cwd)
	}
	if movePinToo && projectPin == nil {
		return usageErr(errors.New("--pin: no .bingo/ape.mod between here and the repo root, so there is no pin to move"))
	}
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
	launcher, launcherIsPin := runningBingoPin()

	res := updateResult{CurrentVersion: current, LatestVersion: latest, Message: "already up to date"}
	switch {
	case !isNewerVersion(current, latest):
	case launcherIsPin && movePinToo:
		// The launcher is bingo's own: the pin move below is its update.
		res.Message = "this ape is a bingo pin; moving the project's pin instead"
	case launcherIsPin:
		hint := ""
		if projectPin != nil {
			hint = "\n  or, in this project: ape update --pin"
		}
		return usageErr(fmt.Errorf("%w%s", refuseBingoSelfUpdate(latest), hint))
	default:
		if err := applyUpdate(ctx, rel, token); err != nil {
			return fmt.Errorf("update failed: %w", err)
		}
		res.Updated, res.Message = true, "updated to "+latest
	}
	updatecache.Save(latest)
	if err := printUpdateResult(res, format); err != nil {
		return err
	}
	if projectPin == nil || !isNewerVersion(projectPin.version, latest) {
		if movePinToo && projectPin != nil {
			fmt.Fprintf(os.Stderr, "ape: this project's pin is already %s\n", projectPin.version)
		}
		return nil
	}
	tag := "v" + latest
	if !movePinToo {
		fmt.Fprintf(os.Stderr, "ape: this project pins ape %s — to move it: bingo get %s@%s "+
			"(or `ape update --pin`, or `ape framework update --update-ape`), then commit .bingo/\n",
			projectPin.version, apeModulePath, tag)
		return nil
	}
	return movePinAndCommit(ctx, projectPin.root, tag, launcherIsPin && launcher.linked)
}

// movePinAndCommit is `ape update --pin`: the project's pin moves to tag in
// a commit of its own, and nothing else is staged.
func movePinAndCommit(ctx context.Context, root, tag string, link bool) error {
	if _, err := exec.LookPath("bingo"); err != nil {
		return usageErr(fmt.Errorf("--pin: bingo is not on PATH; run in %s: bingo get %s@%s, then commit .bingo/",
			root, apeModulePath, tag))
	}
	if err := movePin(ctx, os.Stderr, root, tag, link); err != nil {
		return failErr(err)
	}
	if err := commitPaths(ctx, root, []string{".bingo"}, pinCommitMessage(tag, "Generator: ape update --pin")); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ape: moved this project's pin to %s and committed it (chore(ape))\n", tag)
	return nil
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

// refuseDeliveredSelfUpdate stops an in-guest `ape update` before it fails on a
// read-only mount.
//
// Inside an `ape sandbox` workspace, `ape` is not installed — aped mounts it read-only
// from the node (PLAN-23), so the workspace runs the version matching the daemon that
// provisioned it. Self-updating there is not merely blocked, it is the wrong operation:
// even if the write succeeded it would last until the workspace was recreated, and it
// would put the guest out of step with its node on purpose. Left alone, selfupdate
// reports a permission error on a path the user never chose, which explains nothing.
//
// Detected by LOCATION rather than by trying the write: the mount point is aped's, and a
// binary running from it can only have got there one way.
func refuseDeliveredSelfUpdate() error {
	// An unresolvable path is not an error to report: it only means we cannot tell where
	// this binary lives, so the update proceeds and fails on its own terms if it must.
	exe, _ := os.Executable()
	if exe == "" {
		return nil
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	if exe != filepath.Join(sandbox.ApeBinDest, "ape") &&
		!strings.HasPrefix(exe, sandbox.ApeBinDest+string(filepath.Separator)) {
		return nil
	}
	return fmt.Errorf("this ape is delivered by aped, not installed here: it is mounted "+
		"read-only at %s so the workspace runs the same version as the node that provisioned "+
		"it. Update the NODE's ape instead (and its aped — they ship together), then recreate "+
		"or restart the workspace to pick it up. To pin a specific ape for THIS project, add it "+
		"to .bingo and call it by its version-stamped path", sandbox.ApeBinDest)
}
