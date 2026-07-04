package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/diegosz/apex_process_ape/internal/updatecache"
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
		RunE: func(_ *cobra.Command, _ []string) error {
			// GITHUB_TOKEN is optional now that the repo is public; if
			// set, it raises the GitHub API rate limit (60/h
			// unauthenticated → 5000/h authenticated). Empty token is
			// fine — go-selfupdate hits the public API.
			source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{
				APIToken: os.Getenv("GITHUB_TOKEN"),
			})
			if err != nil {
				return fmt.Errorf("cannot create GitHub source: %w", err)
			}

			updater, err := selfupdate.NewUpdater(selfupdate.Config{
				Source: source,
			})
			if err != nil {
				return fmt.Errorf("cannot create updater: %w", err)
			}

			ctx := context.Background()
			rel, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug("diegosz/apex_process_ape"))
			if err != nil {
				return fmt.Errorf("cannot detect latest version: %w", err)
			}
			if !found {
				fmt.Fprintln(os.Stderr, "no release found")
				return nil
			}

			latestVersion := rel.Version()

			if !isNewerVersion(Version, latestVersion) {
				updatecache.Save(latestVersion)
				res := updateResult{
					CurrentVersion: Version,
					LatestVersion:  latestVersion,
					Updated:        false,
					Message:        "already up to date",
				}
				return printUpdateResult(res, output.Format(outputFormat))
			}

			rel2, err := updater.UpdateSelf(ctx, Version, selfupdate.ParseSlug("diegosz/apex_process_ape"))
			if err != nil {
				return fmt.Errorf("update failed: %w", err)
			}

			updatecache.Save(rel2.Version())

			res := updateResult{
				CurrentVersion: Version,
				LatestVersion:  rel2.Version(),
				Updated:        true,
				Message:        "updated to " + rel2.Version(),
			}
			return printUpdateResult(res, output.Format(outputFormat))
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
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

func fetchLatestVersion(ctx context.Context, token string) (string, error) {
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{
		APIToken: token,
	})
	if err != nil {
		return "", err
	}

	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source: source,
	})
	if err != nil {
		return "", err
	}

	rel, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug("diegosz/apex_process_ape"))
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("no release found")
	}

	return rel.Version(), nil
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
