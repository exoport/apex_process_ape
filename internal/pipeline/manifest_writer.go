package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// manifestWriter owns the on-disk layout for one pipeline run. It is
// constructed at the start of Run(), updated after each step, and
// finalized at the end.
//
// Disk layout under <baseDir>/<pipelineName>/<runID>/:
//
//	manifest.yaml
//	pipeline-report.md       (rendered at Finalize)
//	stages/01-<stage_name>/step-NN-<skill>.ndjson
//
// All writes go through atomic rename (write tmp, then os.Rename).
type manifestWriter struct {
	baseDir      string // e.g. <projectRoot>/_output/pipelines
	pipelineName string
	runID        string
	runDir       string // baseDir/pipelineName/runID
	manifest     Manifest
	stepLogs     []io.Closer // open NDJSON files awaiting close on Finalize
}

// newManifestWriter constructs a writer rooted at baseDir, computes the
// run_id, and creates the run directory + initial partial manifest on
// disk. Returns the writer ready to record per-step events. The caller
// is responsible for calling Finalize when the pipeline ends; an
// abandoned writer leaves a manifest with status: running on disk,
// which downstream consumers should treat as "abandoned mid-run."
func newManifestWriter(
	baseDir, pipelineName, projectRoot, sourcePath, apeVersion string,
	startedAt time.Time,
) (*manifestWriter, error) {
	runID := computeRunID(startedAt, pipelineName, projectRoot)
	runDir := filepath.Join(baseDir, pipelineName, runID)
	if err := os.MkdirAll(filepath.Join(runDir, "stages"), 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}

	digest, err := fileDigest(sourcePath)
	if err != nil {
		// Non-fatal: record an empty digest rather than aborting the
		// run. Worst case the manifest is slightly less reproducible.
		digest = ""
	}

	w := &manifestWriter{
		baseDir:      baseDir,
		pipelineName: pipelineName,
		runID:        runID,
		runDir:       runDir,
		manifest: Manifest{
			SchemaVersion: ManifestSchemaVersion,
			ApeVersion:    apeVersion,
			Pipeline: Ref{
				Name:   pipelineName,
				Source: sourcePath,
				Digest: digest,
			},
			ProjectRoot: projectRoot,
			RunID:       runID,
			StartedAt:   startedAt.UTC(),
			Status:      StatusRunning,
		},
	}
	if err := w.persist(); err != nil {
		return nil, err
	}
	w.updateLatestSymlink()
	return w, nil
}

// BeginStage records a stage start. Returns the stage's index inside
// the manifest, used as the handle for subsequent step/end calls.
func (w *manifestWriter) BeginStage(name string, at time.Time) int {
	idx := len(w.manifest.Stages) + 1
	w.manifest.Stages = append(w.manifest.Stages, StageRecord{
		Index:     idx,
		Name:      name,
		StartedAt: at.UTC(),
		Status:    StatusRunning,
	})
	return idx
}

// OpenStepLog creates the per-step NDJSON file and returns a WriteCloser
// the runner tees claude's line stream into. The returned events_path
// (relative to runDir) is recorded on the StepRecord.
func (w *manifestWriter) OpenStepLog(stageIdx, stepIdx int, stageName, skill string) (io.WriteCloser, string, error) {
	rel := filepath.Join(
		"stages",
		fmt.Sprintf("%02d-%s", stageIdx, sanitizeFsName(stageName)),
		fmt.Sprintf("step-%02d-%s.ndjson", stepIdx, sanitizeFsName(skill)),
	)
	full := filepath.Join(w.runDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, "", fmt.Errorf("create step dir: %w", err)
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("open step log: %w", err)
	}
	w.stepLogs = append(w.stepLogs, f)
	return f, rel, nil
}

// RecordStep appends a fully populated StepRecord to the given stage,
// updates running totals, and persists the manifest atomically.
func (w *manifestWriter) RecordStep(stageIdx int, rec StepRecord) error { //nolint:gocritic // StepRecord is a small per-step struct passed once per step; pointer-passing here would complicate caller sites without meaningful gain
	if stageIdx < 1 || stageIdx > len(w.manifest.Stages) {
		return fmt.Errorf("invalid stage index %d", stageIdx)
	}
	stage := &w.manifest.Stages[stageIdx-1]
	rec.Index = len(stage.Steps) + 1
	stage.Steps = append(stage.Steps, rec)
	w.manifest.Totals.StepsRun++
	if rec.Status == StatusFailed {
		w.manifest.Totals.StepsFailed++
	}
	w.manifest.Totals.CostUSD += rec.CostUSD
	w.manifest.Totals.TokensInput += rec.TokensInput
	w.manifest.Totals.TokensOutput += rec.TokensOutput
	w.manifest.Totals.TokensCacheRead += rec.TokensCacheRead
	w.manifest.Totals.TokensCacheCreation += rec.TokensCacheCreation
	return w.persist()
}

// RecordStepCommit updates the just-recorded step's commit fields and
// bumps totals.commits_made when the commit succeeded. Called by the
// runner immediately after RecordStep, before the manifest is rewritten
// to disk. PLAN-4.
func (w *manifestWriter) RecordStepCommit(stageIdx, stepIdx int, sha, message string, status CommitStatus, errMsg string) error {
	if stageIdx < 1 || stageIdx > len(w.manifest.Stages) {
		return fmt.Errorf("invalid stage index %d", stageIdx)
	}
	stage := &w.manifest.Stages[stageIdx-1]
	if stepIdx < 1 || stepIdx > len(stage.Steps) {
		return fmt.Errorf("invalid step index %d", stepIdx)
	}
	step := &stage.Steps[stepIdx-1]
	step.CommitSHA = sha
	step.CommitMessage = message
	step.CommitStatus = status
	step.CommitError = errMsg
	if status == CommitStatusCommitted {
		w.manifest.Totals.CommitsMade++
	}
	return w.persist()
}

// EndStage records the stage's terminal state.
func (w *manifestWriter) EndStage(stageIdx int, status RunStatus, at time.Time) error {
	if stageIdx < 1 || stageIdx > len(w.manifest.Stages) {
		return fmt.Errorf("invalid stage index %d", stageIdx)
	}
	stage := &w.manifest.Stages[stageIdx-1]
	stage.EndedAt = at.UTC()
	stage.DurationSecs = at.Sub(stage.StartedAt).Seconds()
	stage.Status = status
	return w.persist()
}

// Finalize writes the terminal manifest + renders the human report.
// Closes any open per-step log files. The returned report path is the
// absolute path to pipeline-report.md.
func (w *manifestWriter) Finalize(status RunStatus, endedAt time.Time) (string, error) {
	for _, c := range w.stepLogs {
		_ = c.Close()
	}
	w.stepLogs = nil
	w.manifest.EndedAt = endedAt.UTC()
	w.manifest.DurationSecs = endedAt.Sub(w.manifest.StartedAt).Seconds()
	w.manifest.Status = status
	if err := w.persist(); err != nil {
		return "", err
	}
	reportPath := filepath.Join(w.runDir, "pipeline-report.md")
	if err := writeAtomic(reportPath, []byte(renderReport(&w.manifest))); err != nil {
		return reportPath, fmt.Errorf("write report: %w", err)
	}
	w.updateLatestSymlink()
	return reportPath, nil
}

// persist writes manifest.yaml atomically.
func (w *manifestWriter) persist() error {
	data, err := yaml.Marshal(&w.manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return writeAtomic(filepath.Join(w.runDir, "manifest.yaml"), data)
}

// updateLatestSymlink points <baseDir>/<pipelineName>/latest at the
// current run_id. Best-effort: failure is logged-by-omission, the run's
// own dir is still well-formed.
func (w *manifestWriter) updateLatestSymlink() {
	linkParent := filepath.Join(w.baseDir, w.pipelineName)
	link := filepath.Join(linkParent, "latest")
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(w.runID, tmp); err != nil {
		return
	}
	_ = os.Remove(link)
	_ = os.Rename(tmp, link)
}

// ReportRelativePath returns the run's report path relative to the
// project root, suitable for printing to stdout.
func (w *manifestWriter) ReportRelativePath(projectRoot string) string {
	abs := filepath.Join(w.runDir, "pipeline-report.md")
	if rel, err := filepath.Rel(projectRoot, abs); err == nil {
		return rel
	}
	return abs
}

// computeRunID is YYYYMMDD-HHMMSS-<7-char hash>. The hash mixes the
// nanosecond clock + pipeline + project so concurrent runs against the
// same project do not collide.
func computeRunID(at time.Time, pipelineName, projectRoot string) string {
	at = at.UTC()
	seed := strconv.FormatInt(at.UnixNano(), 10) + "|" + pipelineName + "|" + projectRoot
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%s-%s", at.Format("20060102-150405"), hex.EncodeToString(sum[:4])[:7])
}

// fileDigest returns sha256:<hex> of the file's content, or "" if the
// file cannot be read.
func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// writeAtomic writes data to <path>.tmp and renames it onto path.
// Windows callers tolerate the existing-target case via remove+rename.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			return err
		}
	}
	return nil
}

// sanitizeFsName makes a string safe for use as a path component.
// Replaces anything outside [A-Za-z0-9._-] with '_'.
func sanitizeFsName(s string) string {
	if s == "" {
		return "unnamed"
	}
	b := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z',
			c >= 'a' && c <= 'z',
			c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}
