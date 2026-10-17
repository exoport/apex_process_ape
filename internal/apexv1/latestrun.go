package apexv1

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessions"
	"gopkg.in/yaml.v3"
)

// LatestRun is the newest `ape task` run's outcome, for `ape status`
// (H20): a run a usage limit stopped says so, with the reset, where the
// orchestrator and the operator already look.
type LatestRun struct {
	Skill  string `json:"skill"`
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	// Stopped is the manifest's termination kind (usage_limit, budget,
	// idle_timeout, …); "" for a run that ended on its own.
	Stopped string `json:"stopped,omitempty"`
	// Resets / ResetsAt: a usage-limit stop's reset, as Claude Code printed
	// it and parsed (RFC 3339).
	Resets   string     `json:"resets,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Message  string     `json:"message,omitempty"`
	// Depth / ParentRun: the run's place in the nesting rule (D62) — 2 and
	// the orchestrator's run id for a session an orchestrator started.
	Depth     int    `json:"depth,omitempty"`
	ParentRun string `json:"parent_run,omitempty"`
}

// Describe is the run as one status line: "stopped: usage-limit, resets
// 11pm (Europe/Madrid)" for a usage-limit stop.
func (r *LatestRun) Describe() string {
	s := r.Skill + " " + r.RunID + ": " + r.Status
	switch {
	case r.Stopped == "usage_limit":
		s += ", stopped: usage-limit"
		switch {
		case r.ResetsAt != nil:
			s += ", resets " + r.ResetsAt.Format(time.RFC3339)
		case r.Resets != "":
			s += ", resets " + r.Resets
		}
	case r.Stopped != "":
		s += ", stopped: " + r.Stopped
	}
	if r.ParentRun != "" {
		s += " (started by " + r.ParentRun + ")"
	}
	return s
}

// LiveSession is a model session ape launched that is running in the
// project now (the live-session registry, D62).
type LiveSession struct {
	RunID     string    `json:"run_id"`
	Kind      string    `json:"kind"`
	Skill     string    `json:"skill,omitempty"`
	Depth     int       `json:"depth"`
	ParentRun string    `json:"parent_run,omitempty"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// LiveSessions lists the live model sessions running in the project,
// oldest first.
func (p *Project) LiveSessions() []LiveSession {
	rows, _ := sessions.Alive(sessions.Path())
	out := []LiveSession{}
	for i := range rows {
		r := &rows[i]
		if r.RunID == "" || filepath.Clean(r.CWD) != filepath.Clean(p.Root) {
			continue
		}
		depth := r.Depth
		if depth <= 0 {
			depth = 1
		}
		out = append(out, LiveSession{RunID: r.RunID, Kind: r.Command, Skill: r.Skill, Depth: depth, ParentRun: r.ParentRun, PID: r.PID, StartedAt: r.StartedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// LatestRun reads the newest task run's manifest under the project's run
// records; nil when there is none.
func (p *Project) LatestRun() *LatestRun {
	matches, _ := filepath.Glob(filepath.Join(runlog.TasksRoot(p.Root), "*", "*", "manifest.yaml"))
	var newest, newestSkill string
	for _, m := range matches {
		run := filepath.Base(filepath.Dir(m))
		if run == "latest" {
			continue
		}
		if newest == "" || run > filepath.Base(filepath.Dir(newest)) {
			newest, newestSkill = m, filepath.Base(filepath.Dir(filepath.Dir(m)))
		}
	}
	if newest == "" {
		return nil
	}
	data, err := os.ReadFile(newest)
	if err != nil {
		return nil
	}
	var mf struct {
		RunID       string `yaml:"run_id"`
		Status      string `yaml:"status"`
		Depth       int    `yaml:"depth"`
		ParentRun   string `yaml:"parent_run"`
		Termination *struct {
			Kind     string     `yaml:"kind"`
			Message  string     `yaml:"message"`
			Resets   string     `yaml:"resets"`
			ResetsAt *time.Time `yaml:"resets_at"`
		} `yaml:"termination"`
	}
	if yaml.Unmarshal(data, &mf) != nil {
		return nil
	}
	r := &LatestRun{Skill: newestSkill, RunID: mf.RunID, Status: mf.Status, Depth: mf.Depth, ParentRun: mf.ParentRun}
	if r.RunID == "" {
		r.RunID = filepath.Base(filepath.Dir(newest))
	}
	if t := mf.Termination; t != nil {
		r.Stopped, r.Message, r.Resets, r.ResetsAt = t.Kind, t.Message, t.Resets, t.ResetsAt
	}
	return r
}
