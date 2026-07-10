package service

import "time"

// subjectRootSvc is the PLAN-14 job-daemon subject root; the endpoint group
// is subjectRootSvc.<name>.<project-slug> (docs/reference/events.md).
const subjectRootSvc = "ape.svc"

// WireVersion is the service request/reply payload version. Payloads are
// additive-only; bump only for a breaking change (and document it in
// docs/reference/events.md). Requests may omit "v" (treated as current);
// replies always stamp it.
const WireVersion = 1

// Stable PLAN-14 error codes, returned via micro req.Error. They are an
// external contract frozen in docs/reference/events.md — never renamed or
// repurposed.
const (
	// CodeBusyExclusive: the exclusivity key holds an exclusive job.
	CodeBusyExclusive = "BUSY_EXCLUSIVE"
	// CodeBusyKey: the key holds nonexclusive jobs and an exclusive slot
	// was requested.
	CodeBusyKey = "BUSY_KEY"
	// CodeProjectNotAllowed: project_root is not in the daemon's allowlist.
	CodeProjectNotAllowed = "PROJECT_NOT_ALLOWED"
	// CodeValidation: the request shape is invalid, or the requested job
	// kind is not runnable on this build.
	CodeValidation = "VALIDATION"
	// CodeNotFound: no job with the requested id.
	CodeNotFound = "NOT_FOUND"
)

// Kind is the job kind an endpoint dispatches. It doubles as the eventing
// <kind> subject segment for the child run.
type Kind string

const (
	KindPipeline Kind = "pipeline"
	KindTask     Kind = "task"
	KindCommand  Kind = "command"
	KindScript   Kind = "script"
)

// RunRequest is the shared request body of the four *.run endpoints. Each
// endpoint reads the subset of fields its kind uses; the daemon maps them
// to `ape` child-process flags with a strict field→flag mapping (spawn.go)
// — request fields are never concatenated into a shell string. Every field
// is optional on the wire except project_root and the kind's own selector
// (pipeline / skill / …), which the handler validates.
//
//nolint:tagliatelle // snake_case is the stable, documented NATS wire contract
type RunRequest struct {
	V int `json:"v,omitempty"`

	// ProjectRoot is matched exactly against the daemon allowlist and used
	// as the child's working directory.
	ProjectRoot string `json:"project_root"`

	// Pipeline job selector + options (pipeline.run).
	Pipeline string `json:"pipeline,omitempty"`
	From     string `json:"from,omitempty"`

	// Task job selector + options (task.run).
	Skill      string `json:"skill,omitempty"`
	Args       string `json:"args,omitempty"`
	PromptFlag string `json:"prompt_flag,omitempty"`
	// TaskCommit opts into the task-layer commit. nil = no commit; a
	// non-nil pointer commits, using the string as the message (empty →
	// the daemon derives "ape:task/<skill>").
	TaskCommit *string `json:"task_commit,omitempty"`

	// Command job selectors (command.run — reserved; see the endpoint note).
	Handoff  string `json:"handoff,omitempty"`
	Workflow string `json:"workflow,omitempty"`

	// Script job selectors (script.run — reserved; see D5 + the endpoint note).
	ScriptPath   string `json:"script_path,omitempty"`
	ScriptSource string `json:"script_source,omitempty"`

	// Fields shared across kinds.
	Agent             string `json:"agent,omitempty"`
	Model             string `json:"model,omitempty"`
	Prompt            string `json:"prompt,omitempty"`
	NoCommit          bool   `json:"no_commit,omitempty"`
	CommitAllowDirty  bool   `json:"commit_allow_dirty,omitempty"`
	UploadTranscripts bool   `json:"upload_transcripts,omitempty"`

	// Admission controls (D3).
	Nonexclusive   bool   `json:"nonexclusive,omitempty"`
	ExclusivityKey string `json:"exclusivity_key,omitempty"`

	// SubmittedBy is advisory caller attribution echoed into job-accepted /
	// job-end events and job.status. Authoritative attribution of who
	// published the request is the NATS server's audit domain (per-user
	// creds + subject permissions) — see PLAN-14 Risks.
	SubmittedBy string `json:"submitted_by,omitempty"`
}

// RunReply is the accept response for a *.run request. A rejection is a
// micro req.Error (code + description), not this shape.
//
//nolint:tagliatelle // snake_case is the stable, documented NATS wire contract
type RunReply struct {
	V        int    `json:"v"`
	JobID    string `json:"job_id"`
	Accepted bool   `json:"accepted"`
}

// JobIDRequest is the body of job.status and job.stop.
//
//nolint:tagliatelle // snake_case is the stable, documented NATS wire contract
type JobIDRequest struct {
	V     int    `json:"v,omitempty"`
	JobID string `json:"job_id"`
}

// JobInfo is one job's public state, returned by job.status and listed by
// job.list.
//
//nolint:tagliatelle // snake_case is the stable, documented NATS wire contract
type JobInfo struct {
	JobID          string    `json:"job_id"`
	Kind           Kind      `json:"kind"`
	State          string    `json:"state"` // running | done | failed | stopped
	StartedAt      time.Time `json:"started_at"`
	PID            int       `json:"pid,omitempty"`
	ExclusivityKey string    `json:"exclusivity_key"`
	Exclusive      bool      `json:"exclusive"`
	SubmittedBy    string    `json:"submitted_by,omitempty"`
	LogPath        string    `json:"log_path,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"` // set once terminal
}

// JobStatusReply wraps a JobInfo (job.status).
type JobStatusReply struct {
	V int `json:"v"`
	JobInfo
}

// JobListReply is the job.list response.
type JobListReply struct {
	V    int       `json:"v"`
	Jobs []JobInfo `json:"jobs"`
}

// JobStopReply is the job.stop response.
type JobStopReply struct {
	V       int  `json:"v"`
	Stopped bool `json:"stopped"`
}

// Versions reports the daemon's tool versions for the status endpoint.
type Versions struct {
	Ape    string `json:"ape"`
	Claude string `json:"claude,omitempty"`
}

// StatusReply is the daemon `status` response.
//
//nolint:tagliatelle // snake_case is the stable, documented NATS wire contract
type StatusReply struct {
	V           int                  `json:"v"`
	RunningJobs int                  `json:"running_jobs"`
	HeldKeys    map[string]KeyStatus `json:"held_keys"`
	UptimeSecs  float64              `json:"uptime_seconds"`
	Versions    Versions             `json:"versions"`
	ProjectRoot string               `json:"project_root"`
	Allowlist   []string             `json:"allowlist"`
	Name        string               `json:"name"`
	Draining    bool                 `json:"draining"`
}

// HealthReply is the daemon `health` response — a cheap `ape doctor`
// subset. checks maps a probe name to its pass/fail.
type HealthReply struct {
	V      int             `json:"v"`
	OK     bool            `json:"ok"`
	Checks map[string]bool `json:"checks"`
}
