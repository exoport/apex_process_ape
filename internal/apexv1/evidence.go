package apexv1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Evidence (H23, D45 Q8). Check output lives in this checkout's ignored
// _apex/state/logs; state.json keeps exit codes and output sha256s only.
// A project that must show what ran — an audit, IEC 62304 — sets
//
//	evidence: archive    # off (the default) | archive
//
// and ape commits the output with the record it proves: at archive, the
// final verify's checks into development/archive/<date>-<id>/evidence/,
// inside the squash; at a green `ape milestone close`, the gate run's into
// development/archive/milestones/<M>/evidence/, with the close record. One
// file per check: what ran, its exit, duration and output sha256, then the
// output — capped at EvidenceMaxBytes, keeping its head and tail and
// naming the full log by path and sha256.

// EvidenceMode is _apex/config.yaml's evidence: "off" (also when absent)
// or "archive".
type EvidenceMode string

// EvidenceArchive is evidence: archive.
const EvidenceArchive EvidenceMode = "archive"

// UnmarshalYAML validates the value as it parses.
func (m *EvidenceMode) UnmarshalYAML(n *yaml.Node) error {
	v := strings.TrimSpace(n.Value)
	switch v {
	case "", "off", string(EvidenceArchive):
		*m = EvidenceMode(v)
		return nil
	}
	return fmt.Errorf("evidence: %q is not archive or off", n.Value)
}

// EvidenceMaxBytes caps one check's output in its evidence file.
const EvidenceMaxBytes = 256 << 10

// evidenceOn reports evidence: archive.
func (p *Project) evidenceOn() bool {
	cfg, err := LoadConfig(p.Root)
	return err == nil && cfg.Evidence == EvidenceArchive
}

// MilestoneEvidenceDir is where a closed milestone's gate evidence goes.
func (p *Project) MilestoneEvidenceDir(id string) string {
	return filepath.Join(p.ArchiveDir(), "milestones", id, "evidence")
}

var evidenceSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// evidenceName is check i's file name: its order and its ref, slugged.
func evidenceName(i int, ref string) string {
	slug := strings.Trim(evidenceSlugRe.ReplaceAllString(strings.ToLower(ref), "-"), "-")
	if len(slug) > 60 {
		slug = strings.TrimRight(slug[:60], "-")
	}
	if slug == "" {
		slug = "check"
	}
	return fmt.Sprintf("%02d-%s.txt", i+1, slug)
}

// writeEvidence replaces dir with one evidence file per result, and the
// extra files given (name → content).
func (p *Project) writeEvidence(dir string, results []CheckResult, extra map[string][]byte) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i := range results {
		path := filepath.Join(dir, evidenceName(i, results[i].Task))
		if err := writeFileAtomic(path, p.evidenceFile(&results[i])); err != nil {
			return err
		}
	}
	for name, data := range extra {
		path := filepath.Join(dir, name)
		if err := writeFileAtomic(path, string(data)); err != nil {
			return err
		}
	}
	return nil
}

// evidenceFile renders one check: its record, then its output from the
// log — only when the log still holds what the check recorded.
func (p *Project) evidenceFile(r *CheckResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "check: %s\n", r.Task)
	if r.Kind != "" {
		fmt.Fprintf(&b, "kind: %s\n", r.Kind)
	}
	fmt.Fprintf(&b, "command: %s\nexit: %d\nduration_secs: %.1f\noutput_sha256: %s\n", r.Cmd, r.Exit, r.Secs, r.OutputSHA256)
	if r.Log != "" {
		fmt.Fprintf(&b, "log: %s\n", r.Log)
	}
	data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(r.Log)))
	sum := sha256.Sum256(data)
	switch {
	case r.Log == "" || err != nil:
		b.WriteString("output: unavailable — the log is gone from this checkout\n")
		return b.String()
	case hex.EncodeToString(sum[:]) != r.OutputSHA256:
		b.WriteString("output: unavailable — the log no longer holds what this check recorded (a later run overwrote it)\n")
		return b.String()
	case len(data) <= EvidenceMaxBytes:
		fmt.Fprintf(&b, "output: complete, %d bytes\n\n", len(data))
		b.Write(data)
		return b.String()
	}
	half := EvidenceMaxBytes / 2
	fmt.Fprintf(&b, "output: %d bytes, over the %d KB cap — head and tail kept; the full output is %s (sha256 %s, in this checkout's ignored logs)\n\n",
		len(data), EvidenceMaxBytes>>10, r.Log, r.OutputSHA256)
	b.Write(data[:half])
	fmt.Fprintf(&b, "\n[… %d bytes omitted — full output: %s, sha256 %s …]\n", len(data)-2*half, r.Log, r.OutputSHA256)
	b.Write(data[len(data)-half:])
	return b.String()
}

// milestoneAcceptRed is milestones.json's accept_red object for id,
// verbatim, or nil. Read generically: the record is H22's, and only its
// presence and content matter here.
func (p *Project) milestoneAcceptRed(id string) json.RawMessage {
	data, err := os.ReadFile(p.MilestonesJSON())
	if err != nil {
		return nil
	}
	var recs map[string]map[string]json.RawMessage
	if json.Unmarshal(data, &recs) != nil || recs[id] == nil {
		return nil
	}
	raw := bytes.TrimSpace(recs[id]["accept_red"])
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// writeMilestoneEvidence writes a milestone gate run's evidence — every
// check it ran — and, when milestones.json records an accept_red for it,
// that record as accept-red.json. It returns the evidence folder,
// project-relative, for the close commit.
//
// INTEGRATION (H22): the `milestone close --accept-red` path closes on a
// red gate; it calls this as the green close does, after recording
// accept_red in milestones.json, so the evidence carries it.
func (p *Project) writeMilestoneEvidence(id string, checks []CheckResult) (string, error) {
	dir := p.MilestoneEvidenceDir(id)
	extra := map[string][]byte{}
	if raw := p.milestoneAcceptRed(id); raw != nil {
		var pretty bytes.Buffer
		if json.Indent(&pretty, raw, "", "  ") == nil {
			raw = pretty.Bytes()
		}
		extra["accept-red.json"] = append(append([]byte(nil), raw...), '\n')
	}
	if err := p.writeEvidence(dir, checks, extra); err != nil {
		return "", err
	}
	return p.Rel(dir), nil
}
