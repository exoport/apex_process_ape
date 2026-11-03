package apecmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// The coordinator's assessor (PLAN-V15-ASSESSOR steps 3–5): for each
// closed batch, and each of its projects with a checkout here, build the
// batch folder, run `ape task apex-assess` in it, judge its notice.json
// and publish the outcome through the coordinator (fenced, once). The
// batch's input.md, notice.json, denials and the session's envelope are
// kept with the outcome, for the read-out (D762+); the snapshot is not.

// assessSessionCap is one assessor session's wall-clock ceiling (step 7's
// token line rides on it).
const assessSessionCap = 10 * time.Minute

// Assessor outcomes, result.json's outcome.
const (
	assessPublished = "published"
	assessRefused   = "refused"
	assessFailed    = "assessor-failed"
	assessFenced    = "fenced"
)

// assessRunner runs the coordinator's batches.
type assessRunner struct {
	// dir holds the batch folders while they run and kept/ after.
	dir string
	// checkouts maps a project's subject token to the coordinator's
	// checkout of it.
	checkouts map[string]string
	// session runs one assessor session: `ape task apex-assess` (a seam).
	session func(ctx context.Context, folder, checkout string) (envelope []byte, err error)
	logf    func(format string, args ...any)
}

// assessResult is result.json.
//
//nolint:tagliatelle // ape's records are snake_case
type assessResult struct {
	Batch     string                `json:"batch"`
	Project   string                `json:"project"`
	Outcome   string                `json:"outcome"`
	Reason    string                `json:"reason,omitempty"`
	Detail    string                `json:"detail,omitempty"`
	Published []string              `json:"published,omitempty"`
	Duplicate []string              `json:"duplicate,omitempty"`
	Events    []string              `json:"events"`
	Folder    *apexv1.AssessFolder  `json:"folder,omitempty"`
	Denials   []apexv1.AssessDenial `json:"denials,omitempty"`
	// SkillSHA256 is the plugin SKILL.md the session ran (D762+, P4-1).
	SkillSHA256 string               `json:"skill_sha256,omitempty"`
	Notices     []coordinator.Notice `json:"notices,omitempty"`
	Started     string               `json:"started"`
	Ended       string               `json:"ended"`
}

// keptDir is where a batch's record of project is kept.
func (r *assessRunner) keptDir(batch, project string) string {
	return filepath.Join(r.dir, "kept", batch+"-"+project)
}

// onBatch is coordinator.AssessorConfig.OnBatch.
func (r *assessRunner) onBatch(ctx context.Context, b coordinator.Batch, pub coordinator.Publisher) {
	for _, project := range batchProjects(b) {
		res := r.runProject(ctx, b, project, pub)
		r.logf("assessor: batch %s, %s: %s %s", b.ID, project, res.Outcome, res.Reason)
	}
}

// batchProjects are the projects (subject tokens) b has triggers of, in
// order of first appearance.
func batchProjects(b coordinator.Batch) []string {
	var out []string
	for _, e := range b.Events {
		if p := triggerProject(e.Subject); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// triggerProject is ape.evt.<user>.<project>.project.<M>.<event>'s project.
func triggerProject(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) == 7 && parts[4] == "project" {
		return parts[3]
	}
	return ""
}

// assessEvents are b's triggers of project as the folder takes them.
func assessEvents(b coordinator.Batch, project string) (evs []apexv1.AssessEvent, ids []string) {
	for _, e := range b.Events {
		if triggerProject(e.Subject) != project {
			continue
		}
		var t apexv1.Trigger
		_ = json.Unmarshal(e.Data, &t)
		if t.Event == "" {
			t.Event = e.Subject[strings.LastIndex(e.Subject, ".")+1:]
		}
		evs = append(evs, apexv1.AssessEvent{MsgID: e.MsgID, Event: t.Event, Milestone: t.Milestone, Change: t.Change, Commit: t.Commit, Paths: t.Paths, Gate: t.Gate})
		ids = append(ids, e.MsgID)
	}
	return evs, ids
}

// runProject is one project's part of a batch, from folder to outcome.
func (r *assessRunner) runProject(ctx context.Context, b coordinator.Batch, project string, pub coordinator.Publisher) (res assessResult) {
	evs, ids := assessEvents(b, project)
	res = assessResult{Batch: b.ID, Project: project, Events: ids, Started: time.Now().UTC().Format(time.RFC3339)}
	folder := filepath.Join(r.dir, b.ID+"-"+project)
	kept := r.keptDir(b.ID, project)
	defer func() { r.keep(&res, folder, kept) }()
	if _, err := os.Stat(kept); err == nil {
		// A replayed batch id (step 8 re-runs one): its record stands.
		res.Outcome, res.Reason = assessFailed, "batch "+b.ID+" of "+project+" already has a record; not run again"
		kept = ""
		return res
	}
	fail := func(outcome, reason, detail string) assessResult {
		res.Outcome, res.Reason, res.Detail = outcome, reason, detail
		if err := pub.Failed(ctx, b.ID, project, reason, ids); err != nil {
			res.Outcome = assessFenced
			res.Detail = strings.TrimSpace(res.Detail + "; assessor-failed not published: " + err.Error())
		}
		return res
	}
	checkout := r.checkouts[project]
	if checkout == "" {
		return fail(assessFailed, "no checkout of "+project+" on the coordinator's host", "")
	}
	p, err := apexv1.Open(checkout)
	if err != nil {
		return fail(assessFailed, "the checkout of "+project+" does not open", err.Error())
	}
	ms := map[string]bool{}
	for _, e := range evs {
		ms[e.Milestone] = true
	}
	f, err := p.BuildAssessFolder(ctx, folder, apexv1.AssessRequest{Batch: b.ID, Project: project, Events: evs})
	res.Folder = f
	if err != nil {
		return fail(assessFailed, "the batch folder could not be built", err.Error())
	}
	sctx, cancel := context.WithTimeout(ctx, assessSessionCap)
	envelope, err := r.session(sctx, folder, checkout)
	cancel()
	if data, rerr := os.ReadFile(filepath.Join(folder+assessPluginSuffix, "skills", apexv1.AssessSkill, "SKILL.md")); rerr == nil {
		sum := sha256.Sum256(data)
		res.SkillSHA256 = hex.EncodeToString(sum[:])
	}
	if len(envelope) > 0 {
		_ = os.WriteFile(folder+".task.json", envelope, 0o600)
	}
	if err != nil {
		return fail(assessFailed, "the session failed", err.Error())
	}
	if res.Denials = apexv1.AssessDenials(folder); len(res.Denials) > 0 {
		return fail(assessFailed, fmt.Sprintf("the session tried %d call(s) outside its roster or folder", len(res.Denials)), "")
	}
	data, err := os.ReadFile(filepath.Join(folder, apexv1.AssessNotice))
	if err != nil {
		return fail(assessFailed, "the session wrote no notice.json", "")
	}
	ns, err := coordinator.ValidateNotices(data, coordinator.NoticeCheck{
		Project: project, Open: f.Open, Own: slices.Sorted(maps.Keys(ms)), Snapshot: folder, DiffPaths: f.DiffPaths,
	})
	if ne, ok := errors.AsType[*coordinator.NoticeError](err); ok {
		return fail(assessRefused, "notice.json refused: "+ne.Rule, ne.Detail)
	}
	if err != nil {
		return fail(assessRefused, "notice.json refused", err.Error())
	}
	res.Notices = ns
	res.Published, res.Duplicate, err = pub.Notices(ctx, b.ID, project, ids, ns)
	if err != nil {
		res.Outcome, res.Reason, res.Detail = assessFenced, "not published", err.Error()
		return res
	}
	res.Outcome = assessPublished
	return res
}

// keep writes the batch's record to kept (none for "") — result.json,
// input.md, notice.json, the denials and the session's envelope — and
// removes the folder, its plugin and its side files.
func (r *assessRunner) keep(res *assessResult, folder, kept string) {
	res.Ended = time.Now().UTC().Format(time.RFC3339)
	if kept == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err == nil && os.Mkdir(kept, 0o700) == nil {
		keepFiles(res, folder, kept)
	}
	for _, p := range []string{folder, folder + assessPluginSuffix, apexv1.AssessDeniedPath(folder), folder + ".task.json"} {
		_ = os.RemoveAll(p)
	}
}

func keepFiles(res *assessResult, folder, kept string) {
	for from, to := range map[string]string{
		filepath.Join(folder, apexv1.AssessInput):  apexv1.AssessInput,
		filepath.Join(folder, apexv1.AssessNotice): apexv1.AssessNotice,
		apexv1.AssessDeniedPath(folder):            "denied.jsonl",
		folder + ".task.json":                      "task.json",
	} {
		if data, err := os.ReadFile(from); err == nil {
			_ = os.WriteFile(filepath.Join(kept, to), data, 0o600)
		}
	}
	if data, err := json.MarshalIndent(res, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(kept, "result.json"), append(data, '\n'), 0o600)
	}
}

// taskSession runs `ape task apex-assess` in folder as a child process:
// the session's own manifest, hooks and caps are ape task's.
func taskSession(apeBin string) func(ctx context.Context, folder, checkout string) ([]byte, error) {
	return func(ctx context.Context, folder, checkout string) ([]byte, error) {
		//nolint:gosec // apeBin is this ape (os.Executable); folder and checkout are the coordinator's own
		c := exec.CommandContext(ctx, apeBin, "task", apexv1.AssessSkill, "--cwd", folder,
			"--"+assessSkillFromFlag, checkout, "--output-format", "json", "--quiet",
			"--max-duration", assessSessionCap.String())
		c.Dir = folder
		var out, errb bytes.Buffer
		c.Stdout, c.Stderr = &out, &errb
		err := c.Run()
		var env struct {
			Success bool    `json:"success"`
			Error   *string `json:"error"`
		}
		if jerr := json.Unmarshal(out.Bytes(), &env); jerr == nil && err == nil && !env.Success {
			msg := "not successful"
			if env.Error != nil {
				msg = *env.Error
			}
			err = errors.New(msg)
		}
		if err != nil {
			return out.Bytes(), fmt.Errorf("%w: %s", err, tailLines(errb.String(), 5))
		}
		return out.Bytes(), nil
	}
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// assessCheckouts maps each --assess-checkout <project>=<path> by the
// project's subject token.
func assessCheckouts(specs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range specs {
		project, path, ok := strings.Cut(s, "=")
		if !ok || project == "" || path == "" {
			return nil, fmt.Errorf("--assess-checkout %q is not <project>=<path>", s)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		out[natsconn.SubjectToken(project)] = abs
	}
	return out, nil
}

// assessStateDir is the coordinator's assessor state for ens:
// $XDG_STATE_HOME/ape/coord/<ens>/assess (else ~/.local/state/…).
func assessStateDir(ens string) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ape", "coord", natsconn.SubjectToken(ens), "assess"), nil
}
