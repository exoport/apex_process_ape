package eventing

// This file holds the typed lifecycle-event helpers. Each is a thin wrapper
// over Emit that names the event and shapes its kind-specific payload; all
// are nil-safe via Emit. Event tokens are the fixed PLAN-13 set
// (docs/reference/events.md): run-start | stage-start | step-start |
// step-end | stage-end | hook | commit | error | run-end.

// RunStart marks the beginning of a run.
func (p *Publisher) RunStart(pipeline string, stages int) {
	p.Emit("run-start", map[string]any{
		"pipeline": pipeline,
		"stages":   stages,
	})
}

// StageStart / StageEnd bracket one stage.
func (p *Publisher) StageStart(stage string) {
	p.Emit("stage-start", map[string]any{"stage": stage})
}

func (p *Publisher) StageEnd(stage string, durationSeconds float64, failed bool) {
	p.Emit("stage-end", map[string]any{
		"stage":            stage,
		"duration_seconds": durationSeconds,
		"failed":           failed,
	})
}

// StepStart marks a step beginning.
func (p *Publisher) StepStart(stage string, step int, skill, agent, model string) {
	p.Emit("step-start", map[string]any{
		"stage": stage,
		"step":  step,
		"skill": skill,
		"agent": agent,
		"model": model,
	})
}

// StepEnd marks a step's completion, carrying its transcript-derived
// telemetry (PLAN-10) and, when bound, the step's claude session id.
func (p *Publisher) StepEnd(stage string, step int, skill, sessionID string, durationSeconds float64, metrics StepMetrics) {
	extra := map[string]any{
		"stage":            stage,
		"step":             step,
		"skill":            skill,
		"duration_seconds": durationSeconds,
		"metrics":          metrics,
	}
	if sessionID != "" {
		extra["session_id"] = sessionID
	}
	p.Emit("step-end", extra)
}

// Hook reports a Claude Code hook event forwarded through the bridge.
func (p *Publisher) Hook(event, sessionID, agentID, step string) {
	extra := map[string]any{"hook": event}
	if step != "" {
		extra["step"] = step
	}
	if agentID != "" {
		extra["agent_id"] = agentID
	}
	if sessionID != "" {
		extra["session_id"] = sessionID
	}
	p.Emit("hook", extra)
}

// Commit reports a per-step git commit.
func (p *Publisher) Commit(stage string, step int, sha, message string) {
	p.Emit("commit", map[string]any{
		"stage":   stage,
		"step":    step,
		"sha":     sha,
		"message": message,
	})
}

// Error reports a run-level error (a failed/cancelled run).
func (p *Publisher) Error(message string) {
	p.Emit("error", map[string]any{"message": message})
}

// RunEnd marks the end of a run, carrying manifest totals, terminal status,
// and the transcript-blob digest map (empty when uploads were off/failed).
func (p *Publisher) RunEnd(status string, totals RunTotals, transcriptBlobs map[string]TranscriptBlob, uploadStatus string) {
	extra := map[string]any{
		"status": status,
		"totals": totals,
	}
	if len(transcriptBlobs) > 0 {
		extra["transcript_blobs"] = transcriptBlobs
	}
	if uploadStatus != "" {
		extra["upload_status"] = uploadStatus
	}
	p.Emit("run-end", extra)
}
