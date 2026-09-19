package apecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/spf13/cobra"
)

// `ape config pin` writes a framework fallback into the project's config
// so that it stops being a fallback.
//
// It is the one place ape implements a rule the framework owns, and it
// exists for one key and one migration. `evidence_folder` has been
// optional: every reader resolved the same three-step order, so a
// declared key and an undeclared one produced the same path. The
// maintenance lane ends that — `ape change` has to know which paths are
// evidence BEFORE it dispatches, because a fix's commit may touch no
// {development_folder} path while its evidence commit touches nothing
// else. Two readers resolving that differently would put product code in
// an evidence commit.
//
// The verb retires with the fallback prose it implements.
//
// It is a migration's `command:`, and its `--check` is that migration's
// `check:`. That makes the exit codes load-bearing in a specific way:
// the runner reads 0 as applied, 1 as not applied, and ANYTHING ELSE as
// "the check itself failed". So a missing config keeps exit 4 and a
// malformed one keeps exit 2 — neither is "not applied", and a migration
// must never try to repair a config it cannot read.

// pinnableKeys are the keys this verb knows how to resolve. One, today.
var pinnableKeys = map[string]bool{"evidence_folder": true}

func newConfigPinCmd() *cobra.Command {
	var (
		cwdFlag   string
		checkFlag bool
	)
	cmd := &cobra.Command{
		Use:   "pin <key>",
		Short: "Write a framework-resolved fallback into _apex/config.yaml",
		Long: `Resolve a key the framework has been falling back for, and append it to
the project's own config so every reader sees the same value.

Only evidence_folder today. The value written is the path the project's
skills were already resolving — an existing {governance_folder}/evidence
where the project keeps its evidence there, else the literal evidence —
so no install silently moves. It is a no-op when the base config or the
config.local.yaml overlay already sets the key.

--check writes nothing and answers in its exit code: 0 when the key is
set, 1 when it is not. Those two, and only those two, are answers about
the KEY. A missing config still exits 4 and a malformed one still exits
2, because a migration runner reads anything but 0 or 1 as "the check
failed" — and a config ape cannot read is not a config it should be
repairing.`,
		Args: cobra.ExactArgs(1),
		Example: `  ape config pin evidence_folder
  ape config pin evidence_folder --check`,
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if !pinnableKeys[key] {
				return usageErr(fmt.Errorf("%q is not a pinnable key: ape pins evidence_folder, "+
					"the one variable the framework resolves by fallback", key))
			}
			// resolveProjectConfig exits 4 for an absent config and 2 for
			// a malformed one, which is exactly what the migration runner
			// must see: neither is "not applied".
			cfg := resolveProjectConfig(cwdFlag)

			if checkFlag {
				if strings.TrimSpace(cfg.EvidenceFolder) != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s is set to %s\n", key, cfg.EvidenceFolder)
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s is not set\n", key)
				return reportedErr(ExitRunFailed, fmt.Errorf("%s is not set", key))
			}

			if strings.TrimSpace(cfg.EvidenceFolder) != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already set to %s — nothing to do\n",
					key, cfg.EvidenceFolder)
				return nil
			}
			value := resolveEvidenceFallback(cfg)
			if err := appendConfigKey(cfg.ConfigPath, key, value); err != nil {
				return failErr(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s written to %s\n", key, value, cfg.ConfigPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().BoolVar(&checkFlag, "check", false, "Answer in the exit code and write nothing: 0 set, 1 unset")
	return cmd
}

// resolveEvidenceFallback is the framework's own order, applied once:
// an existing {governance_folder}/evidence where the project already
// keeps one, else the literal `evidence`.
//
// The point is that nothing moves. Every skill writing gate transcripts
// has been resolving this same order, so the value written is where
// that project's evidence already is.
func resolveEvidenceFallback(cfg *apexcfg.Resolved) string {
	if cfg.Paths.Governance != "" {
		candidate := filepath.Join(cfg.Paths.Governance, "evidence")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			if rel, relErr := filepath.Rel(cfg.Root, candidate); relErr == nil {
				return filepath.ToSlash(rel)
			}
		}
	}
	return "evidence"
}

// appendConfigKey adds `key: value` to the end of the config.
//
// Appended rather than rewritten through a YAML round-trip, because
// this file is one a person authored: a marshal would reorder the keys,
// drop every comment and requote what it felt like. The write is
// atomic, since os.WriteFile truncates first and an interrupted call
// would leave the project with no config at all.
func appendConfigKey(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	body := string(data)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += fmt.Sprintf("%s: %s\n", key, value)
	if err := atomicfile.Write(path, []byte(body)); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
