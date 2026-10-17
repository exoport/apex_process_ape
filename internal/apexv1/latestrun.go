package apexv1

import (
	"os"
	"path/filepath"
	"time"

	"github.com/exoport/apex_process_ape/internal/runlog"
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
	return s
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
	r := &LatestRun{Skill: newestSkill, RunID: mf.RunID, Status: mf.Status}
	if r.RunID == "" {
		r.RunID = filepath.Base(filepath.Dir(newest))
	}
	if t := mf.Termination; t != nil {
		r.Stopped, r.Message, r.Resets, r.ResetsAt = t.Kind, t.Message, t.Resets, t.ResetsAt
	}
	return r
}
