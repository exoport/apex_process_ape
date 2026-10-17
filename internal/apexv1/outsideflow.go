package apexv1

import (
	"context"
	"strings"
)

// Work outside the change flow (H7). A base-branch commit that touches
// code — paths outside the development folder and _apex/ — with no
// `Change:` trailer and no `apex:` subject reached the base without a
// change: no plan, no checks, no review. `ape status --json` lists those
// since the last archive squash, else since the v1 epoch (the newest
// `apex: project init` or `apex: lift` commit; D47 Q5 exempts everything
// before it). Shown, never failed: v1 has no warning tier.
//
// One more exemption: apex-release's commit (`release: <version>`, trailer
// `Release: <version>`) is the person-reviewed release record, made on the
// base outside any change. It is not outside the flow when the only code
// path it touches is the changelog (docs.changelog, else CHANGELOG.md). A
// release commit that touches anything else is listed like any other.

// OutsideFlow is one such commit.
type OutsideFlow struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	// Files are the paths it touched outside the development folder and
	// _apex/ (at most outsideFlowFiles).
	Files []string `json:"files"`
}

const outsideFlowFiles = 10

// v1EpochSubjects start the commit that makes a project v1.
var v1EpochSubjects = []string{"apex: project init", "apex: lift"}

// v1Epoch is the newest commit on rev whose subject starts the v1 epoch,
// "" when there is none.
func (p *Project) v1Epoch(ctx context.Context, rev string) string {
	out, err := git(ctx, p.Root, "log", "--format=%H%x1f%s", "-E", "--grep=^apex: (project init|lift)", rev)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(out, "\n") {
		sha, subject, _ := strings.Cut(line, "\x1f")
		for _, s := range v1EpochSubjects {
			if strings.HasPrefix(subject, s) {
				return sha
			}
		}
	}
	return ""
}

// outsideFlowFields are a log record's fields: sha, subject, Change
// trailer, files.
const outsideFlowFields = 5

// outsideFlow lists base's commits outside the change flow, newest first.
func (p *Project) outsideFlow(ctx context.Context, base string) []OutsideFlow {
	out := []OutsideFlow{}
	if base == "" || !p.isWorkTree(ctx) || !p.branchExists(ctx, base) {
		return out
	}
	rev := "refs/heads/" + base
	epoch := p.v1Epoch(ctx, rev)
	if epoch == "" {
		return out
	}
	// --relative: paths relative to the project, which may sit below the
	// repository's root.
	log, err := gitRaw(ctx, p.Root, "log", "--format=%x1e%H%x1f%s%x1f%(trailers:key=Change,valueonly,separator=%x2C)%x1f%(trailers:key=Release,valueonly,separator=%x2C)%x1f",
		"--name-only", "--relative", epoch+".."+rev)
	if err != nil {
		return out
	}
	archive := p.devPrefix() + "archive/"
	changelog := p.releaseChangelog()
	for rec := range strings.SplitSeq(log, "\x1e") {
		f := strings.SplitN(rec, "\x1f", outsideFlowFields)
		if len(f) < outsideFlowFields {
			continue
		}
		sha, subject, change, release := f[0], f[1], strings.TrimSpace(f[2]), strings.TrimSpace(f[3])
		var files []string
		squash := false
		for path := range strings.SplitSeq(f[4], "\n") {
			path = strings.TrimSpace(path)
			switch {
			case path == "":
			case strings.HasPrefix(path, archive):
				squash = true
			case !p.notCode(path) && path != "_apex" && !strings.HasPrefix(path, "_apex/"):
				files = append(files, path)
			}
		}
		if change != "" && squash && !strings.HasPrefix(subject, "apex:") {
			break // the last archive squash: what precedes it was reported before it
		}
		if change != "" || strings.HasPrefix(subject, "apex:") || len(files) == 0 {
			continue
		}
		if release != "" && len(files) == 1 && files[0] == changelog {
			continue // apex-release's changelog commit
		}
		out = append(out, OutsideFlow{SHA: sha, Subject: subject, Files: limit(files, outsideFlowFiles)})
	}
	return out
}

// releaseChangelog is the changelog an apex-release commit may touch
// without being outside the flow: docs.changelog, else CHANGELOG.md.
func (p *Project) releaseChangelog() string {
	if cfg, err := LoadConfig(p.Root); err == nil && cfg.Docs.Changelog != "" {
		return strings.TrimPrefix(cfg.Docs.Changelog, "./")
	}
	return "CHANGELOG.md"
}
