package apecmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/usage"
	"github.com/spf13/cobra"
)

// W4 (v1.1): usage-aware launching. Every unattended session ape launches
// gets `ape usage record` as its status line (--settings; ape chat, the
// person's own session, does not); the recorder keeps the
// plan's rate limits as Claude Code reports them, and `ape usage` shows the
// last reading. internal/usage holds the files and the rules.

// usageNow is the clock the usage commands and the guard read (a test seam).
var usageNow = time.Now

// statusLineCommand is the spawn's status-line command: ape usage record,
// "" for a person's own session (ape chat).
func (s spawnSettings) statusLineCommand(apeBin string) string {
	if s.personSession {
		return ""
	}
	return usageRecordCommand(apeBin, s.usageKey)
}

// usageRecordCommand is the status-line command of a session ape launches:
// `<ape> usage record`, with --run <key> when the session belongs to a run
// whose readings are kept (an ape task run).
func usageRecordCommand(apeBin, key string) string {
	if apeBin == "" {
		return ""
	}
	cmd := apeBin + " usage record"
	if key != "" {
		cmd += " --run " + key
	}
	return cmd
}

const usageLongHelp = `The claude.ai plan's usage, as the sessions ape launches last saw it.

Every unattended Claude Code session ape launches (ape task, ape prompt, a
loop's children) gets ape's own status line in its --settings:
  statusLine: {type: command, command: "<ape> usage record [--run <key>]"}
It applies to that session only; the person's own settings files and their
status line are never written, and ape chat, the person's own interactive
session, gets none: their status line stays theirs. Claude Code hands the status line a JSON
object; from the session's first API response on it carries the plan's
rate_limits (five_hour and seven_day: used_percentage 0–100, resets_at in
epoch seconds; measured on Claude Code 2.1.306), and the recorder keeps the
reading in <user cache>/ape/usage.json (os.UserCacheDir: ~/.cache/ape on
Linux), per OS user. The session's status line shows "5h 14% · 7d 55%".

An ape task run's first and last reading are its manifest's
  usage: {before, after}
(before is taken at the session's first API response), and the history
limits.usage.share: auto learns from (<user cache>/ape/usage-history.json).

ape usage prints the last reading and its age: the windows as fractions,
when each resets, the run whose session recorded it and the Claude Code
version. With none yet: "usage: unknown". ape status shows the same in one
line. --output-format json prints {reading, age_seconds, path}, reading
null when there is none. Exit 0 either way.`

func newUsageCmd() *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show the plan's usage as the sessions ape launched last saw it",
		Long:  usageLongHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printUsage(cmd.OutOrStdout(), output.Format(outputFormat), usage.Load(), usageNow())
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.AddCommand(newUsageRecordCmd())
	return cmd
}

// usageReport is `ape usage --output-format json`.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type usageReport struct {
	Reading    *usage.Reading `json:"reading" yaml:"reading"`
	AgeSeconds *float64       `json:"age_seconds" yaml:"age_seconds"`
	Path       string         `json:"path" yaml:"path"`
}

func printUsage(w io.Writer, format output.Format, r *usage.Reading, now time.Time) error {
	if format == output.FormatJSON || format == output.FormatYAML {
		rep := usageReport{Reading: r, Path: usage.Path()}
		if r != nil {
			age := r.Age(now).Seconds()
			rep.AgeSeconds = &age
		}
		return output.Print(w, format, rep)
	}
	if r == nil {
		fmt.Fprintln(w, "usage: unknown — no reading yet (every unattended session ape launches records one from its status line)")
		return nil
	}
	fmt.Fprintf(w, "usage: %s (read %s)\n", r.Short(), usage.Ago(r.Age(now)))
	window := func(label string, v *float64, at *time.Time) {
		if v == nil {
			return
		}
		fmt.Fprintf(w, "  %-7s %.2f", label, *v)
		if at != nil {
			fmt.Fprintf(w, ", resets %s", at.Local().Format(time.RFC3339))
		}
		fmt.Fprintln(w)
	}
	window("5-hour", r.FiveHour, r.FiveHourResetsAt)
	window("7-day", r.SevenDay, r.SevenDayResetsAt)
	fmt.Fprintf(w, "  read    %s", r.At.Local().Format(time.RFC3339))
	if r.Run != "" {
		fmt.Fprintf(w, " by run %s", r.Run)
	}
	if r.ClaudeVersion != "" {
		fmt.Fprintf(w, " (Claude Code %s)", r.ClaudeVersion)
	}
	fmt.Fprintf(w, "\n  file    %s\n", usage.Path())
	return nil
}

// newUsageRecordCmd is the status-line command. Hidden: Claude Code runs it,
// not a person. It never fails the session: bad input prints nothing and
// exits 0.
func newUsageRecordCmd() *cobra.Command {
	var key string
	cmd := &cobra.Command{
		Use:    "record",
		Short:  "(internal) Record the plan's usage from Claude Code's status-line JSON on stdin",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runUsageRecord(cmd.InOrStdin(), cmd.OutOrStdout(), key)
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "run", "", "the launch key the run's readings are kept under")
	return cmd
}

// runUsageRecord reads the status-line JSON (at most 1 MiB), records it,
// and prints the status line: "5h 14% · 7d 55%", or nothing.
func runUsageRecord(stdin io.Reader, stdout io.Writer, key string) {
	data, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return
	}
	r, _ := usage.Record(data, key, usageNow())
	if r != nil {
		fmt.Fprintln(stdout, r.StatusLine())
	}
}

// beginRunUsage names the run a launch key's readings belong to, once the
// run has an id (onRunDir).
func beginRunUsage(key, runID, skill string) {
	if key == "" {
		return
	}
	if err := usage.BeginRun(key, runID, skill); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ usage: %v — this run's readings are not kept\n", err)
	}
}

// endRunUsage takes the run's first and last reading into its manifest
// (usage: {before, after}) and the share history.
func endRunUsage(key, skill, runDir string) {
	if key == "" {
		return
	}
	first, last := usage.EndRun(key)
	if first == nil || runDir == "" {
		return
	}
	_ = usage.AppendHistory(usage.HistoryEntry{Skill: skill, Run: runIDFromDir(runDir), At: usageNow().UTC(), Before: first, After: last})
	patchManifestAt(runDir, func(m *pipeline.Manifest) {
		if m.Usage == nil {
			m.Usage = &pipeline.UsageRecord{}
		}
		m.Usage.Before, m.Usage.After = manifestReading(first), manifestReading(last)
	})
}

// manifestReading is a reading in the manifest's shape.
func manifestReading(r *usage.Reading) *pipeline.UsageReading {
	if r == nil {
		return nil
	}
	return &pipeline.UsageReading{
		At: r.At, FiveHour: r.FiveHour, FiveHourResetsAt: r.FiveHourResetsAt,
		SevenDay: r.SevenDay, SevenDayResetsAt: r.SevenDayResetsAt,
	}
}
