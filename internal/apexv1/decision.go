package apexv1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// DecisionResult is what `ape decision new` reports.
type DecisionResult struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// NewDecision allocates the next ADR number and writes the skeleton.
func (p *Project) NewDecision(ctx context.Context, slug, title string) (*DecisionResult, error) {
	if !slugRe.MatchString(slug) {
		return nil, &UsageError{Msg: fmt.Sprintf("decision slug %q must be kebab-case (e.g. use-sqlite)", slug)}
	}
	maxN := 0
	entries, _ := os.ReadDir(p.DecisionsDir())
	for _, e := range entries {
		if m := adrFileRe.FindStringSubmatch(e.Name()); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > maxN {
				maxN = n
			}
			if m[2] == slug {
				return nil, &FailError{Msg: "decision " + e.Name() + " already uses slug " + slug}
			}
		}
	}
	num := fmt.Sprintf("%04d", maxN+1)
	if title == "" {
		title = titleFromSlug(slug)
	}
	path := filepath.Join(p.DecisionsDir(), num+"-"+slug+".md")
	body := "# ADR-" + num + ": " + title + "\n" +
		"Status: proposed\n" +
		"Date: " + Today() + "\n\n" +
		"## Context\n<!-- the forces at play -->\n\n" +
		"## Decision\n<!-- what we decided -->\n\n" +
		"## Consequences\n<!-- what becomes easier or harder -->\n\n" +
		"Enforced by: review\n"
	if err := os.MkdirAll(p.DecisionsDir(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, &FailError{Msg: "cannot create " + p.Rel(path) + ": " + err.Error()}
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	res := &DecisionResult{ID: "ADR-" + num, Path: p.Rel(path)}
	if _, err := p.commitRecord(ctx, "apex: "+res.ID+" proposed", "", res.Path); err != nil {
		return res, &FailError{Msg: "decision written but its commit failed: " + err.Error()}
	}
	return res, nil
}
