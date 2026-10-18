package apecmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessions"
	"gopkg.in/yaml.v3"
)

// Commit attribution across concurrent sessions (eval rc.3 probe).
//
// A run's commits are the range its HEAD moved over (gitCommitsSince):
// git records no process, so a commit another ape session made in the same
// checkout while this run was going lands in this run's range too, and was
// claimed by it. Two runs whose windows overlap cannot be told apart by
// the range alone, so neither claims a commit made while the other ran: it
// is listed under commits_shared in both runs' records. Each run judges it
// at its own end — a run still going is a row of the live-session registry,
// a finished one is its manifest's window — so the run that ends first sees
// the other live, and the one that ends last sees the other's manifest.
//
// A run's own lineage is not "another session": an orchestrator's window
// holds its children's by design, so neither the parent run nor this run's
// children make a commit shared. Prompts and chats are seen while they run
// (the registry), not after: their records keep no end time. A person's
// commit from a terminal during the run is still the run's: nothing records
// it.

// runCommit is one commit of a run's range.
type runCommit struct {
	Subject string
	// At is the committer time, whole seconds as git keeps it.
	At time.Time
}

// gitCommitsSince returns the commits made after `before` (oldest first),
// each with its committer time — the run's complete commit trail, framework
// commits included, not just ape's boundary commit. Best-effort: nil when
// the log cannot be read.
//
// An empty `before` means the repository had no commits when the run
// started, so every commit reachable from HEAD was made BY the run. That
// case has to list them rather than return nothing: the commit-ownership
// assertion reads this list, and reporting an empty trail there would
// accuse a declared committer of suppressing the very commit it just
// made. A repo that still has no commits has no HEAD, the log fails, and
// the empty result is then correct.
func gitCommitsSince(ctx context.Context, projectRoot, before string) []runCommit {
	revRange := before + "..HEAD"
	if before == "" {
		revRange = "HEAD"
	}
	var stdout bytes.Buffer
	// revRange is a SHA rev-parse captured at run start, or "HEAD".
	cmd := exec.CommandContext(ctx, "git", "log", "--reverse", "--format=%ct %s", revRange)
	cmd.Dir = projectRoot
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil
	}
	var out []runCommit
	for line := range strings.SplitSeq(strings.TrimRight(stdout.String(), "\n"), "\n") {
		ts, subject, ok := strings.Cut(line, " ")
		secs, err := strconv.ParseInt(ts, 10, 64)
		if !ok || err != nil {
			continue
		}
		out = append(out, runCommit{Subject: subject, At: time.Unix(secs, 0)})
	}
	return out
}

// runWindow is another run's time in the checkout; End is zero while it
// runs.
type runWindow struct {
	Run, Parent string
	Start, End  time.Time
}

// holds reports whether a commit at t (whole seconds) was made while the
// run went: from the second it started to the second after it ended.
func (w runWindow) holds(t, now time.Time) bool {
	end := w.End
	if end.IsZero() {
		end = now
	}
	return !t.Before(w.Start.Truncate(time.Second)) && !t.After(end.Add(time.Second))
}

// splitSharedCommits splits a run's commits into those it claims and those
// made while another session's run in the same checkout went (subjects,
// oldest first). run is this run's id ("" when unknown: nothing is
// shared), parentRun its parent's.
func splitSharedCommits(projectRoot, run, parentRun string, commits []runCommit, now time.Time) (claimed, shared []string) {
	claimed = []string{}
	var others []runWindow
	if run != "" && len(commits) > 0 {
		others = otherRunWindows(projectRoot, run, parentRun)
	}
	for _, c := range commits {
		isShared := false
		for _, w := range others {
			if w.holds(c.At, now) {
				isShared = true
				break
			}
		}
		if isShared {
			shared = append(shared, c.Subject)
		} else {
			claimed = append(claimed, c.Subject)
		}
	}
	return claimed, shared
}

// otherRunWindows lists the runs in projectRoot's checkout that are not
// run, its parent or its children: the live sessions of the registry, and
// the finished pipeline and task runs of its run trees.
func otherRunWindows(projectRoot, run, parentRun string) []runWindow {
	mine := func(w runWindow) bool {
		return w.Run == run || (parentRun != "" && w.Run == parentRun) || w.Parent == run
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		abs = projectRoot
	}
	var out []runWindow
	seen := map[string]bool{}
	rows, _ := sessions.Alive(sessions.Path())
	for i := range rows {
		row := &rows[i]
		w := runWindow{Run: row.RunID, Parent: row.ParentRun, Start: row.StartedAt}
		if row.RunID == "" || filepath.Clean(row.CWD) != abs || mine(w) {
			continue
		}
		seen[w.Run] = true
		out = append(out, w)
	}
	for _, tree := range runlog.RunRoots(projectRoot) {
		if tree.Kind != runlog.KindPipeline && tree.Kind != runlog.KindTask {
			continue
		}
		paths, _ := filepath.Glob(filepath.Join(tree.Path, "*", "*", "manifest.yaml"))
		for _, path := range paths {
			if filepath.Base(filepath.Dir(path)) == "latest" {
				continue
			}
			w, ok := finishedRunWindow(path)
			if !ok || seen[w.Run] || mine(w) {
				continue
			}
			seen[w.Run] = true
			out = append(out, w)
		}
	}
	return out
}

// finishedRunWindow reads a finished run's window from its manifest. A run
// still marked running is judged by the registry alone: one whose ape is
// gone left its manifest running, and would otherwise share every later
// commit.
func finishedRunWindow(path string) (runWindow, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runWindow{}, false
	}
	//nolint:tagliatelle // the run manifest's on-disk keys (pipeline.Manifest), snake_case by contract
	var m struct {
		RunID     string    `yaml:"run_id"`
		ParentRun string    `yaml:"parent_run"`
		Status    string    `yaml:"status"`
		StartedAt time.Time `yaml:"started_at"`
		EndedAt   time.Time `yaml:"ended_at"`
	}
	if yaml.Unmarshal(data, &m) != nil || m.RunID == "" || m.StartedAt.IsZero() || m.EndedAt.IsZero() ||
		m.Status == string(pipeline.StatusRunning) {
		return runWindow{}, false
	}
	return runWindow{Run: m.RunID, Parent: m.ParentRun, Start: m.StartedAt, End: m.EndedAt}, true
}

// recordCommitsShared writes the shared commits into the run's manifest
// (commits_shared); nothing when there are none.
func recordCommitsShared(runDir string, shared []string) {
	if len(shared) == 0 {
		return
	}
	patchManifestAt(runDir, func(m *pipeline.Manifest) { m.CommitsShared = shared })
}
