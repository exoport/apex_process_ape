package apecmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/memory"
	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/exoport/apex_process_ape/internal/story"
)

// The PLAN-25 D11 project-data checks. Each returns INFO outside a project
// root — absence of a project is not a failure — and each reports what it
// found rather than what it fixed.

// checkConfigResolved is Required: nothing else in this family can see the
// project's artifacts without it, and a malformed config.local.yaml is the
// failure most likely to send every other check looking at the wrong tree.
func checkConfigResolved(_ context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "not in a project — nothing to resolve"}
	}
	cfg, err := apexcfg.ResolveAt(env.ProjectRoot, nil)
	if err != nil {
		if errors.Is(err, apexcfg.ErrNotFound) {
			return CheckResult{
				Status:      StatusInfo,
				Message:     "no _apex/config.yaml — not an APEX project",
				Remediation: "Run `ape framework setup` to install the framework and seed the config.",
				FixCommand:  "ape framework setup",
			}
		}
		return CheckResult{
			Status:      StatusFail,
			Message:     err.Error(),
			Remediation: "Fix the YAML it names. Every skill resolves this file as its first act, so a broken override sends a whole pipeline at the wrong folders.",
			FixCommand:  "ape config resolve",
		}
	}
	overlay := "no local overlay"
	if cfg.LocalOverlayApplied {
		overlay = fmt.Sprintf("local overlay replaced %v", cfg.OverlaidKeys)
	}
	return CheckResult{
		Status:  StatusOK,
		Message: fmt.Sprintf("%s resolved; %s", cfg.ProjectName, overlay),
	}
}

// checkRegistryDrift reports the four registry checks. Not Required: drift
// is a finding for a person, and a warn keeps `ape doctor` usable on a
// project that has some.
func checkRegistryDrift(_ context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	report, err := registry.Verify(cfg, registry.VerifyOptions{})
	if err != nil {
		return CheckResult{Status: StatusWarn, Message: err.Error()}
	}
	if report.OK() {
		return CheckResult{
			Status: StatusOK,
			Message: fmt.Sprintf("%d record(s) across %d famil%s, no drift",
				report.Summary.Records, report.Summary.Families-report.Summary.Skipped,
				pluralY(report.Summary.Families-report.Summary.Skipped)),
		}
	}
	return CheckResult{
		Status:      StatusWarn,
		Message:     fmt.Sprintf("%d finding(s): %v", report.Summary.Findings, report.Summary.ByCheck),
		Remediation: "`ape registry verify --all` lists them; `ape registry sync --all` repairs the ones a tool can.",
		FixCommand:  "ape registry verify --all",
	}
}

// checkStoryFrontmatter reports the story corpus's frontmatter findings.
func checkStoryFrontmatter(_ context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	if cfg.Paths.Implementation == "" {
		return CheckResult{Status: StatusInfo, Message: "implementation_folder is not configured"}
	}
	report, err := story.VerifyCorpus(cfg)
	if err != nil {
		return CheckResult{Status: StatusWarn, Message: err.Error()}
	}
	if report.OK() {
		return CheckResult{
			Status: StatusOK,
			Message: fmt.Sprintf("%d stor%s checked, no findings",
				report.Summary.StoriesChecked, pluralY(report.Summary.StoriesChecked)),
		}
	}
	return CheckResult{
		Status: StatusWarn,
		Message: fmt.Sprintf("%d finding(s) across %d stor%s",
			report.Summary.Findings, report.Summary.StoriesChecked,
			pluralY(report.Summary.StoriesChecked)),
		Remediation: "`ape story verify` lists them. Frontmatter is authored, so these are fixed by hand.",
		FixCommand:  "ape story verify",
	}
}

// checkSprintDivergence reports tracker-vs-disk divergence. Never more
// than a warn: which side is right is judgment, so this is information for
// a person and nothing else.
func checkSprintDivergence(_ context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	report, err := sprint.RunCheck(cfg)
	if err != nil {
		return CheckResult{Status: StatusWarn, Message: err.Error()}
	}
	if report.OK() {
		return CheckResult{
			Status: StatusOK,
			Message: fmt.Sprintf("%d story row(s) agree with %d story file(s)",
				report.Summary.StoryRows, report.Summary.StoriesOnDisk),
		}
	}
	return CheckResult{
		Status:      StatusWarn,
		Message:     fmt.Sprintf("%d divergence(s): %v", report.Summary.Findings, report.Summary.ByCheck),
		Remediation: "`ape sprint check` names both sides of each one. Neither is assumed correct — a person decides.",
		FixCommand:  "ape sprint check",
	}
}

// checkMemorySize is Required, and that is the whole point.
//
// runDoctor silently downgrades a non-required FAIL to WARN, so a
// non-required check could never surface the hard ceiling — the state where
// team-memory.md has passed the Read cap and become unreadable by its own
// writer. `ape memory check` deliberately exits 0 in that state (a failing
// exit there would abort the retrospective at exactly the moment
// compaction is due); this is where the breach becomes non-ignorable
// instead.
func checkMemorySize(_ context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	if cfg.Paths.TeamMemory == "" {
		return CheckResult{Status: StatusInfo, Message: "development_folder is not configured"}
	}
	c := memory.CheckSize(cfg.Paths.TeamMemory, 0, 0)
	switch c.State {
	case memory.StateAbsent:
		return CheckResult{Status: StatusOK, Message: "no team-memory.md yet"}
	case memory.StateOK:
		return CheckResult{
			Status:  StatusOK,
			Message: fmt.Sprintf("%s of %s", humanBytes(c.Bytes), humanBytes(c.SoftBudget)),
		}
	case memory.StateOverSoft:
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("%s over the %s soft budget — compaction is due", humanBytes(c.Bytes), humanBytes(c.SoftBudget)),
			Remediation: "The next retrospective should fold older entries. Nothing is broken yet.",
			FixCommand:  "ape memory check",
		}
	case memory.StateOverHard:
		return CheckResult{
			Status: StatusFail,
			Message: fmt.Sprintf("%s is past the %s hard ceiling — the file is unreadable by its own writer",
				humanBytes(c.Bytes), humanBytes(c.HardCeiling)),
			Remediation: "Compact it now, and treat the miss as a bug report: the soft gate should have caught this several retrospectives ago.",
			FixCommand:  "ape memory check",
		}
	default:
		return CheckResult{Status: StatusInfo, Message: string(c.State)}
	}
}

// checkMigrationPending makes --no-migrate leave a visible state rather
// than a silent one.
func checkMigrationPending(_ context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "not in a project"}
	}
	pending := pendingMigrations(env.ProjectRoot)
	if len(pending) == 0 {
		return CheckResult{Status: StatusInfo, Message: "no project config — nothing to migrate"}
	}
	var names []string
	for _, st := range pending {
		if st.Pending {
			names = append(names, st.Name)
		}
	}
	if len(names) == 0 {
		return CheckResult{Status: StatusOK, Message: "no project-data migration pending"}
	}
	return CheckResult{
		Status:      StatusWarn,
		Message:     fmt.Sprintf("pending: %v", names),
		Remediation: "`ape framework update` runs them, or `ape deferred migrate --dry-run` to look first. Nothing is committed either way.",
		FixCommand:  "ape deferred migrate --dry-run",
	}
}

// projectDataConfig resolves the config for a check, or returns the
// CheckResult to report instead. Outside a project, or with no config, the
// answer is INFO — absence of evidence is not a finding.
func projectDataConfig(env doctorEnv) (*apexcfg.Resolved, *CheckResult) {
	if !isProjectRoot(env.ProjectRoot) {
		return nil, &CheckResult{Status: StatusInfo, Message: "not in a project"}
	}
	cfg, err := apexcfg.ResolveAt(env.ProjectRoot, nil)
	if err != nil {
		// config.resolved owns reporting the real state; duplicating its
		// verdict on five more rows would just be noise.
		return nil, &CheckResult{Status: StatusInfo, Message: "config not resolvable — see config.resolved"}
	}
	return cfg, nil
}
