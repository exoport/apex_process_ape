package registry

import (
	"gopkg.in/yaml.v3"
)

// BackfillFill is one existing entry that gained fields.
type BackfillFill struct {
	Family string   `json:"family"         yaml:"family"`
	ID     string   `json:"id"             yaml:"id"`
	File   string   `json:"file,omitempty" yaml:"file,omitempty"`
	Fields []string `json:"fields"         yaml:"fields"`
}

// BackfillGap is an existing entry still short of a required field after
// the backfill, because its record has no value to copy.
type BackfillGap struct {
	Family string   `json:"family"           yaml:"family"`
	ID     string   `json:"id"               yaml:"id"`
	File   string   `json:"file,omitempty"   yaml:"file,omitempty"`
	Fields []string `json:"fields,omitempty" yaml:"fields,omitempty"`
	Reason string   `json:"reason"           yaml:"reason"`
}

// BackfillResult reports what a backfill did (or would do under Check).
type BackfillResult struct {
	Families []SyncFamilyResult `json:"families" yaml:"families"`
	Fills    []BackfillFill     `json:"fills"    yaml:"fills"`
	// Gaps are NOT pending work: nothing on disk can close them, so a
	// re-run finds the same set. That is why they never make --check exit
	// 1 — a migration whose check can never pass is one that re-runs on
	// every update.
	Gaps  []BackfillGap `json:"gaps"  yaml:"gaps"`
	Check bool          `json:"check" yaml:"check"`
}

// absentRequired lists the required, non-file fields entry has no key for.
func absentRequired(entry *yaml.Node, fields []entryField) []string {
	var out []string
	for _, f := range fields {
		if !f.Required || f.IsFile {
			continue
		}
		if mappingValue(entry, f.Key) == nil {
			out = append(out, f.Key)
		}
	}
	return out
}
