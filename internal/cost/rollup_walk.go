package cost

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RebuildRollup walks <project>/_output/pipelines/<name>/<run-id>/manifest.yaml,
// <project>/_output/tasks/<skill>/<run-id>/manifest.yaml (PLAN-11), and
// <project>/_output/ape/chats/<chat-id>/session.yaml, folds every row
// into a fresh Rollup, and saves it. Used by `ape costs roll`. PLAN-5 / C7.
//
// Best-effort: parse errors on individual artefacts are skipped (so a
// half-written manifest doesn't abort the walk). The rollup is rebuilt
// from scratch, not merged with the existing file — that's what makes
// it a "roll" (resync the cache from the durable record).
func RebuildRollup(projectRoot string) (*Rollup, error) {
	r := &Rollup{
		Pipelines: map[string]Bucket{},
		Tasks:     map[string]Bucket{},
		ByDay:     map[string]Totals{},
	}
	r.Chats.Runs = map[string]Totals{}

	if err := walkManifestTree(filepath.Join(projectRoot, "_output", "pipelines"), r.FoldPipelineRun); err != nil {
		return nil, err
	}
	if err := walkManifestTree(filepath.Join(projectRoot, "_output", "tasks"), r.FoldTaskRun); err != nil {
		return nil, err
	}
	if err := walkChats(projectRoot, r); err != nil {
		return nil, err
	}
	if err := SaveRollup(projectRoot, r); err != nil {
		return nil, err
	}
	return r, nil
}

// manifestForRollup mirrors the cost-relevant subset of
// pipeline.Manifest. Defined here so the cost package doesn't import
// pipeline (which would couple the two and complicate testing).
type manifestForRollup struct {
	RunID     string    `yaml:"run_id"`
	StartedAt time.Time `yaml:"started_at"`
	Totals    struct {
		CostUSD             float64                     `yaml:"cost_usd"`
		TokensInput         int                         `yaml:"tokens_input"`
		TokensOutput        int                         `yaml:"tokens_output"`
		TokensCacheRead     int                         `yaml:"tokens_cache_read"`
		TokensCacheCreation int                         `yaml:"tokens_cache_creation"`
		NumTurns            int                         `yaml:"num_turns"`
		ModelUsage          map[string]modelUsageRecord `yaml:"model_usage"`
	} `yaml:"totals"`
}

// modelUsageRecord mirrors pipeline.ModelUsageRecord's on-disk shape
// (the per-model block in a manifest's totals). PLAN-10 D5.
type modelUsageRecord struct {
	CostUSD             float64 `yaml:"cost_usd"`
	TokensInput         int     `yaml:"tokens_input"`
	TokensOutput        int     `yaml:"tokens_output"`
	TokensCacheRead     int     `yaml:"tokens_cache_read"`
	TokensCacheCreation int     `yaml:"tokens_cache_creation"`
	NumTurns            int     `yaml:"num_turns"`
}

// walkManifestTree walks a <root>/<name>/<run-id>/manifest.yaml tree
// (the shared layout of _output/pipelines and _output/tasks) and folds
// every readable manifest via fold(name, runID, day, totals).
func walkManifestTree(root string, fold func(name, runID string, day time.Time, totals Totals, perModel map[string]Totals)) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		nameDir := filepath.Join(root, name)
		runs, err := os.ReadDir(nameDir)
		if err != nil {
			continue
		}
		for _, runEnt := range runs {
			if !runEnt.IsDir() || runEnt.Name() == "latest" {
				continue
			}
			manifestPath := filepath.Join(nameDir, runEnt.Name(), "manifest.yaml")
			m, ok := loadManifestForRollup(manifestPath)
			if !ok {
				continue
			}
			day := m.StartedAt
			if day.IsZero() {
				// Fallback: parse run-id prefix YYYYMMDD-HHMMSS-<hash>.
				if t, err := time.Parse("20060102-150405", strings.Split(m.RunID, "-")[0]+"-"+pickHHMMSS(m.RunID)); err == nil {
					day = t
				} else {
					day = time.Now().UTC()
				}
			}
			totals := Totals{
				CostUSD:             m.Totals.CostUSD,
				InputTokens:         m.Totals.TokensInput,
				OutputTokens:        m.Totals.TokensOutput,
				CacheReadTokens:     m.Totals.TokensCacheRead,
				CacheCreationTokens: m.Totals.TokensCacheCreation,
				NumTurns:            m.Totals.NumTurns,
			}
			fold(name, m.RunID, day, totals, perModelTotals(m.Totals.ModelUsage))
		}
	}
	return nil
}

// perModelTotals converts a manifest's per-model usage records into the
// rollup's map[model]Totals shape. Returns nil for empty input so the
// fold leaves PerModel unallocated when there's nothing to add.
func perModelTotals(mu map[string]modelUsageRecord) map[string]Totals {
	if len(mu) == 0 {
		return nil
	}
	out := make(map[string]Totals, len(mu))
	for model, u := range mu {
		out[model] = Totals{
			CostUSD:             u.CostUSD,
			InputTokens:         u.TokensInput,
			OutputTokens:        u.TokensOutput,
			CacheReadTokens:     u.TokensCacheRead,
			CacheCreationTokens: u.TokensCacheCreation,
			NumTurns:            u.NumTurns,
		}
	}
	return out
}

// sessionForRollup mirrors the cost-relevant subset of session.yaml.
type sessionForRollup struct {
	ChatID    string    `yaml:"chat_id"`
	StartedAt time.Time `yaml:"started_at"`
	CostUSD   float64   `yaml:"cost_usd"`
	TokensIn  int       `yaml:"tokens_input"`
	TokensOut int       `yaml:"tokens_output"`
}

func walkChats(projectRoot string, r *Rollup) error {
	root := filepath.Join(projectRoot, "_output", "ape", "chats")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		path := filepath.Join(root, ent.Name(), "session.yaml")
		s, ok := loadSessionForRollup(path)
		if !ok {
			continue
		}
		day := s.StartedAt
		if day.IsZero() {
			day = time.Now().UTC()
		}
		totals := Totals{
			CostUSD:      s.CostUSD,
			InputTokens:  s.TokensIn,
			OutputTokens: s.TokensOut,
		}
		// session.yaml has no per-model block today; pass nil.
		r.FoldChat(s.ChatID, day, totals, nil)
	}
	return nil
}

// RunManifest is one run's cost summary, read by `ape costs run <id>`.
// PLAN-10 D5.
type RunManifest struct {
	Name     string            `json:"name"` // pipeline name or task skill
	RunID    string            `json:"run_id"`
	Kind     string            `json:"kind"` // "pipeline" or "task"
	Totals   Totals            `json:"totals"`
	PerModel map[string]Totals `json:"per_model,omitempty"`
}

// FindRunManifest locates a run by run-id under _output/pipelines/ and
// _output/tasks/ and returns its cost summary. ok=false when no manifest
// with that run-id exists. PLAN-10 D5 (restores `ape costs run`).
func FindRunManifest(projectRoot, runID string) (RunManifest, bool) {
	for _, tree := range []struct{ root, kind string }{
		{filepath.Join(projectRoot, "_output", "pipelines"), "pipeline"},
		{filepath.Join(projectRoot, "_output", "tasks"), "task"},
	} {
		entries, err := os.ReadDir(tree.root)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			m, ok := loadManifestForRollup(filepath.Join(tree.root, ent.Name(), runID, "manifest.yaml"))
			if !ok {
				continue
			}
			return RunManifest{
				Name:  ent.Name(),
				RunID: m.RunID,
				Kind:  tree.kind,
				Totals: Totals{
					CostUSD:             m.Totals.CostUSD,
					InputTokens:         m.Totals.TokensInput,
					OutputTokens:        m.Totals.TokensOutput,
					CacheReadTokens:     m.Totals.TokensCacheRead,
					CacheCreationTokens: m.Totals.TokensCacheCreation,
					NumTurns:            m.Totals.NumTurns,
				},
				PerModel: perModelTotals(m.Totals.ModelUsage),
			}, true
		}
	}
	return RunManifest{}, false
}

// ChatSession is one chat session's cost summary, read by
// `ape costs chat <id>`. PLAN-10 D5.
type ChatSession struct {
	ChatID  string    `json:"chat_id"`
	Totals  Totals    `json:"totals"`
	Started time.Time `json:"started_at"`
}

// FindChatSession reads _output/ape/chats/<chatID>/session.yaml.
// ok=false when the session is absent. PLAN-10 D5.
func FindChatSession(projectRoot, chatID string) (ChatSession, bool) {
	path := filepath.Join(projectRoot, "_output", "ape", "chats", chatID, "session.yaml")
	s, ok := loadSessionForRollup(path)
	if !ok {
		return ChatSession{}, false
	}
	return ChatSession{
		ChatID:  s.ChatID,
		Started: s.StartedAt,
		Totals:  Totals{CostUSD: s.CostUSD, InputTokens: s.TokensIn, OutputTokens: s.TokensOut},
	}, true
}

func loadManifestForRollup(path string) (manifestForRollup, bool) {
	var m manifestForRollup
	bs, err := os.ReadFile(path)
	if err != nil {
		return m, false
	}
	if err := yaml.Unmarshal(bs, &m); err != nil {
		return m, false
	}
	if m.RunID == "" {
		return m, false
	}
	return m, true
}

func loadSessionForRollup(path string) (sessionForRollup, bool) {
	var s sessionForRollup
	bs, err := os.ReadFile(path)
	if err != nil {
		return s, false
	}
	if err := yaml.Unmarshal(bs, &s); err != nil {
		return s, false
	}
	if s.ChatID == "" {
		return s, false
	}
	return s, true
}

// pickHHMMSS extracts the time component from a run-id of the form
// YYYYMMDD-HHMMSS-<hash>. Returns "" if the shape doesn't match.
func pickHHMMSS(runID string) string {
	parts := strings.Split(runID, "-")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
