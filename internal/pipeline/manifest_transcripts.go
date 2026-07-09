package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ManifestPath returns the manifest.yaml path for a run directory.
func ManifestPath(runDir string) string {
	return filepath.Join(runDir, "manifest.yaml")
}

// LoadManifest reads and parses a run's manifest.yaml. Used at run finalize
// to source the run-end event's totals + status without re-deriving them.
func LoadManifest(runDir string) (Manifest, error) {
	data, err := os.ReadFile(ManifestPath(runDir))
	if err != nil {
		return Manifest{}, fmt.Errorf("pipeline: read manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("pipeline: parse manifest: %w", err)
	}
	return m, nil
}

// StampTranscriptBlobs records the transcript-blob digest map + upload
// status onto an already-finalized manifest (PLAN-13 D3). The upload runs
// after the runner has written the terminal manifest, so this re-reads,
// sets the additive fields, and re-persists atomically. A no-op when both
// inputs are empty.
func StampTranscriptBlobs(runDir string, blobs map[string]TranscriptBlob, uploadStatus string) error {
	if len(blobs) == 0 && uploadStatus == "" {
		return nil
	}
	m, err := LoadManifest(runDir)
	if err != nil {
		return err
	}
	if len(blobs) > 0 {
		m.TranscriptBlobs = blobs
	}
	if uploadStatus != "" {
		m.UploadStatus = uploadStatus
	}
	data, err := yaml.Marshal(&m)
	if err != nil {
		return fmt.Errorf("pipeline: marshal manifest: %w", err)
	}
	return writeAtomic(ManifestPath(runDir), data)
}
