package cost

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// RepricedFile is one artefact a reprice pass examined.
//
//nolint:tagliatelle // snake_case matches the on-disk / wire contract
type RepricedFile struct {
	// Path is the artefact's path, relative to the project root.
	Path string `json:"path" yaml:"path"`
	// OldCostUSD / NewCostUSD are the run-level totals before and after.
	OldCostUSD float64 `json:"old_cost_usd" yaml:"old_cost_usd"`
	NewCostUSD float64 `json:"new_cost_usd" yaml:"new_cost_usd"`
	// Records is how many per-model blocks were recomputed.
	Records int `json:"records" yaml:"records"`
	// Unpriced lists model ids that still have no rate — repricing cannot
	// fix those, and the file's total remains a lower bound.
	Unpriced []string `json:"unpriced,omitempty" yaml:"unpriced,omitempty"`
	// Written is true when --write actually rewrote the file.
	Written bool `json:"written" yaml:"written"`
}

// Changed reports whether repricing moved the file's total.
func (f RepricedFile) Changed() bool { return !floatNear(f.OldCostUSD, f.NewCostUSD) }

// RepriceReport is the outcome of a reprice pass.
//
//nolint:tagliatelle // snake_case matches the on-disk / wire contract
type RepriceReport struct {
	Files         []RepricedFile `json:"files"                    yaml:"files"`
	Scanned       int            `json:"scanned"                  yaml:"scanned"`
	Changed       int            `json:"changed"                  yaml:"changed"`
	Written       int            `json:"written"                  yaml:"written"`
	OldTotal      float64        `json:"old_total_usd"            yaml:"old_total_usd"`
	NewTotal      float64        `json:"new_total_usd"            yaml:"new_total_usd"`
	StillUnpriced []string       `json:"still_unpriced,omitempty" yaml:"still_unpriced,omitempty"`
}

// Reprice recomputes stored cost figures from the token counts already on
// disk, using the current price table.
//
// This is what makes a stale-price incident recoverable rather than a
// permanent hole in the record. PLAN-10 D1 deliberately persisted the full
// per-model token breakdown — input, output, cache-read, and the 5m/1h
// cache-creation split — so a run's cost can be derived again exactly at
// any later date. The only input repricing cannot recover is the per-turn
// timestamp, so dated promotional windows resolve against the run's own
// start date, which is the correct bucket for every turn in a single run.
//
// Artefacts covered: _output/{pipelines,tasks}/<name>/<run-id>/manifest.yaml
// and _output/ape/prompts/<id>/prompt.yaml. Chat session.yaml carries no
// per-model breakdown and is skipped (nothing to reprice from).
//
// With write=false nothing is modified — the report is the preview. With
// write=true each file is rewritten through a yaml.Node round-trip that
// touches only the cost_usd scalars, preserving key order, comments, and
// every field this package does not model.
func Reprice(projectRoot string, write bool) (RepriceReport, error) {
	var rep RepriceReport
	unpriced := map[string]bool{}

	paths, err := repriceTargets(projectRoot)
	if err != nil {
		return rep, err
	}
	for _, p := range paths {
		f, ok := repriceFile(p, write)
		if !ok {
			continue
		}
		rel, relErr := filepath.Rel(projectRoot, p)
		if relErr == nil {
			f.Path = rel
		} else {
			f.Path = p
		}
		rep.Scanned++
		rep.OldTotal += f.OldCostUSD
		rep.NewTotal += f.NewCostUSD
		for _, m := range f.Unpriced {
			unpriced[m] = true
		}
		if f.Changed() {
			rep.Changed++
		}
		if f.Written {
			rep.Written++
		}
		if f.Changed() || len(f.Unpriced) > 0 {
			rep.Files = append(rep.Files, f)
		}
	}
	for m := range unpriced {
		rep.StillUnpriced = append(rep.StillUnpriced, m)
	}
	sort.Strings(rep.StillUnpriced)
	sort.Slice(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })
	return rep, nil
}

// repriceTargets enumerates the artefacts a reprice pass can act on, deduped
// by the path they actually resolve to.
//
// The dedupe is load-bearing, not defensive. Every pipeline and task name
// carries a `latest` symlink to its most recent run dir (see runner.go), so
// `*/*/manifest.yaml` matches the same file twice — once by run id and once
// through the link. Left in, that double-counts every recent run in the
// report: the preview a caller reads before deciding to --write would show
// twice the real delta. rollup_walk.go skips the `latest` name for the same
// reason; resolving instead of name-matching also covers any other symlink.
func repriceTargets(projectRoot string) ([]string, error) {
	globs := []string{
		filepath.Join(projectRoot, "_output", "pipelines", "*", "*", "manifest.yaml"),
		filepath.Join(projectRoot, "_output", "tasks", "*", "*", "manifest.yaml"),
		filepath.Join(projectRoot, "_output", "ape", "prompts", "*", "prompt.yaml"),
	}
	var out []string
	seen := map[string]bool{}
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			return nil, fmt.Errorf("cost.Reprice: glob: %w", err)
		}
		for _, m := range matches {
			key := m
			if resolved, rerr := filepath.EvalSymlinks(m); rerr == nil {
				key = resolved
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out, nil
}

// repriceFile recomputes one artefact. ok=false when the file is
// unreadable or is not a mapping document.
func repriceFile(path string, write bool) (RepricedFile, bool) {
	res := RepricedFile{}
	bs, err := os.ReadFile(path)
	if err != nil {
		return res, false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(bs, &doc); err != nil {
		return res, false
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return res, false
	}
	root := doc.Content[0]

	// A run's own start date buckets every turn in it for dated windows.
	day := nodeTime(root, "started_at")

	res.OldCostUSD = nodeFloat(root)
	if t := mapValue(root, "totals"); t != nil {
		res.OldCostUSD = nodeFloat(t)
	}

	changed := repriceNode(root, day, &res)
	if !changed {
		res.NewCostUSD = res.OldCostUSD
		return res, true
	}
	res.NewCostUSD = nodeFloat(root)
	if t := mapValue(root, "totals"); t != nil {
		res.NewCostUSD = nodeFloat(t)
	}

	if write && res.Changed() {
		out, err := yaml.Marshal(&doc)
		if err != nil {
			return res, true
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, out, 0o644); err != nil { //nolint:gosec // matches the artefact's existing world-readable mode
			return res, true
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return res, true
		}
		res.Written = true
	}
	return res, true
}

// repriceNode walks a YAML mapping tree and recomputes every cost it can
// attribute to a model.
//
// The rule is uniform and needs no knowledge of the manifest schema: any
// mapping that owns a per-model breakdown (`model_usage:` or `per_model:`)
// has each of its model records recomputed from that record's own token
// fields, and then its own `cost_usd` set to the sum of those records. That
// covers run totals, per-step blocks, and per-session blocks identically,
// and it leaves alone any node with no model attribution — there is nothing
// to reprice from there, and guessing would be worse than the stale value.
func repriceNode(node *yaml.Node, day time.Time, res *RepricedFile) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	changed := false

	for _, key := range []string{"model_usage", "per_model"} {
		breakdown := mapValue(node, key)
		if breakdown == nil || breakdown.Kind != yaml.MappingNode {
			continue
		}
		var sum float64
		for i := 0; i+1 < len(breakdown.Content); i += 2 {
			model := breakdown.Content[i].Value
			rec := breakdown.Content[i+1]
			if rec.Kind != yaml.MappingNode {
				continue
			}
			price, src := LookupSourceAt(model, day)
			if !src.Priced() {
				res.Unpriced = appendUnique(res.Unpriced, NormalizeModel(model))
				sum += nodeFloat(rec)
				continue
			}
			cost := TurnCost(usageFromNode(rec), price)
			if setNodeFloat(rec, "cost_usd", cost) {
				changed = true
			}
			res.Records++
			sum += cost
		}
		if setNodeFloat(node, "cost_usd", sum) {
			changed = true
		}
	}

	// Recurse into every child mapping / sequence so nested steps and
	// sessions are reached wherever the schema puts them.
	for i := 0; i+1 < len(node.Content); i += 2 {
		val := node.Content[i+1]
		switch val.Kind {
		case yaml.MappingNode:
			if repriceNode(val, day, res) {
				changed = true
			}
		case yaml.SequenceNode:
			for _, item := range val.Content {
				if repriceNode(item, day, res) {
					changed = true
				}
			}
		case yaml.DocumentNode, yaml.ScalarNode, yaml.AliasNode:
			// Scalars hold no nested cost records; documents and aliases do
			// not appear inside a manifest mapping.
		}
	}
	return changed
}

// usageFromNode reads a stored per-model record's token counts back into a
// UsageBlock so TurnCost can re-derive its cost. The 5m/1h split is stored
// separately (PLAN-10 D1); when only the summed field is present — an older
// record written before the split — the whole amount is attributed to the
// 5m tier, which is the cheaper of the two and keeps the result a lower
// bound rather than an overstatement.
func usageFromNode(rec *yaml.Node) UsageBlock {
	u := UsageBlock{
		InputTokens:  nodeInt(rec, "tokens_input"),
		OutputTokens: nodeInt(rec, "tokens_output"),
		CacheRead:    nodeInt(rec, "tokens_cache_read"),
	}
	u.CacheCreation.Ephemeral5m = nodeInt(rec, "tokens_cache_creation_5m")
	u.CacheCreation.Ephemeral1h = nodeInt(rec, "tokens_cache_creation_1h")
	if u.CacheCreation.Ephemeral5m == 0 && u.CacheCreation.Ephemeral1h == 0 {
		u.CacheCreation.Ephemeral5m = nodeInt(rec, "tokens_cache_creation")
	}
	return u
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// nodeFloat reads this mapping's own cost_usd scalar, or 0 when absent.
func nodeFloat(node *yaml.Node) float64 {
	v := mapValue(node, "cost_usd")
	if v == nil {
		return 0
	}
	var f float64
	if err := v.Decode(&f); err != nil {
		return 0
	}
	return f
}

func nodeInt(node *yaml.Node, key string) int {
	v := mapValue(node, key)
	if v == nil {
		return 0
	}
	var i int
	if err := v.Decode(&i); err != nil {
		return 0
	}
	return i
}

func nodeTime(node *yaml.Node, key string) time.Time {
	v := mapValue(node, key)
	if v == nil {
		return time.Time{}
	}
	var t time.Time
	if err := v.Decode(&t); err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// setNodeFloat writes a float into an existing scalar under key. Returns
// true when the stored value actually changed. Absent keys are not created
// — a record that never carried a cost_usd is not one this pass owns.
//
// Node.Encode is used rather than a hand-formatted string so a repriced
// value is byte-identical to what ape's own manifest writer would have
// emitted for the same number (`4.83`, not `4.830000`). A reprice should be
// invisible in a diff apart from the figures that actually moved.
func setNodeFloat(node *yaml.Node, key string, val float64) bool {
	v := mapValue(node, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return false
	}
	var cur float64
	_ = v.Decode(&cur)
	if floatNear(cur, val) {
		return false
	}
	return v.Encode(val) == nil
}

// floatNear compares two dollar amounts below the sub-cent noise floor.
func floatNear(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

func appendUnique(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}
