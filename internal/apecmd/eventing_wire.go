package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/blobstore"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/nats-io/nats.go"
)

// Transcript upload_status values recorded on the manifest + run-end event.
const (
	uploadStatusOK      = "ok"
	uploadStatusPartial = "partial"
	uploadStatusFailed  = "failed"
)

// startEventing resolves the NATS config (flags → env), connects, and
// decodes the credential identity. It is fire-and-forget: a
// configured-but-unreachable cluster logs one stderr warning and returns a
// nil conn so the run proceeds local-only. With no URL configured it returns
// (nil, zero identity) silently.
func startEventing(ctx context.Context, cfg runConfig) (*nats.Conn, natsconn.Identity) {
	nc := natsconn.Resolve(cfg.natsURL, cfg.natsCreds)
	if !nc.Enabled() {
		return nil, natsconn.Identity{}
	}
	conn, err := natsconn.Connect(ctx, nc, "ape/"+Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ nats: %v — continuing local-only (no progress events / transcript upload)\n", err)
		return nil, natsconn.Identity{}
	}
	id, err := nc.Identity()
	if err != nil && !errors.Is(err, natsconn.ErrNoCreds) {
		fmt.Fprintf(os.Stderr, "⚠ nats: could not decode credential identity: %v\n", err)
	}
	return conn, id
}

// newEventPublisher builds the run's event publisher. The <id> subject
// segment is APE_JOB_ID when the daemon injected one (so child event
// subjects carry the job id), else the run id.
func newEventPublisher(conn *nats.Conn, id natsconn.Identity, projectRoot, runID string, cfg runConfig) *eventing.Publisher {
	jobID := runID
	if env := os.Getenv("APE_JOB_ID"); env != "" {
		jobID = env
	}
	kind := cfg.kind
	if kind == "" {
		kind = eventing.KindPipeline
	}
	return eventing.New(conn, eventing.Options{
		Identity: id,
		Project:  projectRoot,
		Kind:     kind,
		ID:       jobID,
		Prefix:   cfg.eventsPrefix,
	})
}

// finalizeRun performs the end-of-run eventing: a run-level error event (on
// failure), transcript upload (when enabled) with the manifest stamped, and
// the run-end event carrying manifest totals + the transcript-blob map.
//
// It never fails the run. Transcript stamping is independent of publishing:
// when upload was requested but NATS is unreachable, the manifest still
// records upload_status: failed (a local, durable record) even though no
// run-end event can be published. Warnings go to stderr.
func finalizeRun(ctx context.Context, pub *eventing.Publisher, conn *nats.Conn, runDir, projectRoot string, cfg runConfig, runErr error) {
	if runDir == "" {
		return
	}
	if pub != nil && runErr != nil {
		pub.Error(runErr.Error())
	}

	var (
		blobs        map[string]pipeline.TranscriptBlob
		uploadStatus string
	)
	if uploadEnabled(cfg) {
		if conn != nil {
			blobs, uploadStatus = uploadRunTranscripts(ctx, conn, runDir, projectRoot, cfg)
		} else {
			uploadStatus = uploadStatusFailed
			fmt.Fprintln(os.Stderr, "⚠ transcript upload requested but NATS is unavailable — recording upload_status: failed")
		}
		if err := pipeline.StampTranscriptBlobs(runDir, blobs, uploadStatus); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ eventing: could not stamp transcript blobs onto manifest: %v\n", err)
		}
	}

	if pub == nil {
		return // no NATS → no run-end event (upload_status already stamped locally)
	}
	m, err := pipeline.LoadManifest(runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ eventing: run-end skipped (manifest unreadable): %v\n", err)
		return
	}
	pub.RunEnd(string(m.Status), manifestTotalsToEvent(m.Totals), transcriptBlobsToEvent(blobs), uploadStatus)
}

// uploadEnabled resolves --upload-transcripts / APE_UPLOAD_TRANSCRIPTS.
func uploadEnabled(cfg runConfig) bool {
	if cfg.uploadTranscripts {
		return true
	}
	v := os.Getenv("APE_UPLOAD_TRANSCRIPTS")
	return v == "1" || strings.EqualFold(v, "true")
}

// uploadRunTranscripts uploads every transcript snapshot in the run dir
// (main + sub-agents, copied there by the interactive core) as
// content-addressed blobs. Returns the file→blob map + an upload_status of
// "ok" | "partial" | "failed". Warnings go to stderr; the run is never
// failed.
func uploadRunTranscripts(ctx context.Context, conn *nats.Conn, runDir, projectRoot string, cfg runConfig) (blobs map[string]pipeline.TranscriptBlob, status string) {
	store, err := newTranscriptStore(ctx, conn, projectRoot, runIDFromDir(runDir), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ transcript upload: %v\n", err)
		return nil, uploadStatusFailed
	}

	files, _ := filepath.Glob(filepath.Join(runDir, "transcripts", "*.jsonl"))
	sort.Strings(files)
	if len(files) == 0 {
		return nil, ""
	}

	blobs = make(map[string]pipeline.TranscriptBlob, len(files))
	var failures int
	for _, f := range files {
		res, uErr := blobstore.UploadFile(ctx, store, f)
		if uErr != nil {
			failures++
			fmt.Fprintf(os.Stderr, "⚠ transcript upload: %s: %v\n", filepath.Base(f), uErr)
			continue
		}
		base := filepath.Base(f)
		blobs[base] = pipeline.TranscriptBlob{
			SessionID: strings.TrimSuffix(base, ".jsonl"),
			Digest:    res.Digest.String(),
			URI:       res.URI,
			Bytes:     res.Size,
		}
	}

	switch {
	case failures == 0:
		return blobs, uploadStatusOK
	case failures == len(files):
		return blobs, uploadStatusFailed
	default:
		return blobs, uploadStatusPartial
	}
}

// newTranscriptStore selects the blob backend (--transcript-store /
// APE_TRANSCRIPT_STORE).
func newTranscriptStore(ctx context.Context, conn *nats.Conn, projectRoot, runID string, cfg runConfig) (blobstore.Store, error) {
	kind := cfg.transcriptStore
	if kind == "" {
		kind = os.Getenv("APE_TRANSCRIPT_STORE")
	}
	switch kind {
	case "", "nats-object":
		return blobstore.NewNATSObjectStore(ctx, conn, "")
	case "uri-offload":
		return blobstore.NewURIOffloadStore(conn, blobstore.URIOffloadConfig{
			Project: eventing.ProjectSlug(projectRoot),
			RunID:   runID,
		}), nil
	default:
		return nil, fmt.Errorf("unknown --transcript-store %q (want nats-object|uri-offload)", kind)
	}
}

// stepTelemetryToMetrics adapts the interactive core's transcript-derived
// telemetry onto the eventing step-end payload shape.
func stepTelemetryToMetrics(tele *pipeline.StepTelemetry) eventing.StepMetrics {
	m := eventing.StepMetrics{
		CostUSD:             tele.CostUSD,
		TokensInput:         tele.TokensInput,
		TokensOutput:        tele.TokensOutput,
		TokensCacheRead:     tele.TokensCacheRead,
		TokensCacheCreation: tele.TokensCacheCreation,
		NumTurns:            tele.NumTurns,
	}
	if len(tele.ModelUsage) > 0 {
		m.PerModel = make(map[string]eventing.ModelMetrics, len(tele.ModelUsage))
		for model, u := range tele.ModelUsage {
			m.PerModel[model] = eventing.ModelMetrics{
				CostUSD:              u.CostUSD,
				InputTokens:          u.TokensInput,
				OutputTokens:         u.TokensOutput,
				CacheReadInputTokens: u.TokensCacheRead,
				CacheCreation5m:      u.TokensCacheCreation5m,
				CacheCreation1h:      u.TokensCacheCreation1h,
				Turns:                u.NumTurns,
			}
		}
	}
	return m
}

func manifestTotalsToEvent(t pipeline.ManifestTotals) eventing.RunTotals {
	return eventing.RunTotals{
		CostUSD:             t.CostUSD,
		TokensInput:         t.TokensInput,
		TokensOutput:        t.TokensOutput,
		TokensCacheRead:     t.TokensCacheRead,
		TokensCacheCreation: t.TokensCacheCreation,
		NumTurns:            t.NumTurns,
		StepsRun:            t.StepsRun,
		StepsFailed:         t.StepsFailed,
		CommitsMade:         t.CommitsMade,
	}
}

func transcriptBlobsToEvent(in map[string]pipeline.TranscriptBlob) map[string]eventing.TranscriptBlob {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]eventing.TranscriptBlob, len(in))
	for k, v := range in {
		out[k] = eventing.TranscriptBlob{SessionID: v.SessionID, Digest: v.Digest, URI: v.URI, Bytes: v.Bytes}
	}
	return out
}

// runIDFromDir returns the run id (the run dir's base name).
func runIDFromDir(runDir string) string { return filepath.Base(runDir) }
