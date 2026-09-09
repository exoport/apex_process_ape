package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/contract"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/hookdrift"
	"github.com/exoport/apex_process_ape/internal/outputstyles"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/updatecache"
)

// LAST UPDATED: 2026-05-22 — list of Ubuntu majors known to be
// supported by current Playwright releases. Newer Playwright versions
// add Ubuntu support in patch / minor releases; bump this list when
// you cut a new ape release after Playwright catches up. Hosts not on
// this list get a WARN — they still work, but skills that need
// Playwright/Chromium (Excalidraw rendering) may bail out at the
// install-Chromium step until PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS
// is set or a compatible cache is pre-staged.
var playwrightSupportedUbuntuVersions = []string{"20.04", "22.04", "24.04"}

// checkClaudeBinary stats `claude` on PATH. Doesn't execute it — auth
// errors on a fresh install would surface as false failures here, and
// the user has separate workflows to diagnose claude itself.
func checkClaudeBinary(_ context.Context, _ doctorEnv) CheckResult {
	path, err := exec.LookPath("claude")
	if err != nil {
		return CheckResult{
			Status:      StatusFail,
			Message:     "not found on PATH",
			Remediation: "Install Claude Code — see https://docs.claude.com/claude-code",
		}
	}
	return CheckResult{Status: StatusOK, Message: path}
}

func checkGitBinary(_ context.Context, _ doctorEnv) CheckResult {
	path, err := exec.LookPath("git")
	if err != nil {
		return CheckResult{
			Status:      StatusFail,
			Message:     "not found on PATH",
			Remediation: "Install git via your package manager.",
		}
	}
	return CheckResult{Status: StatusOK, Message: path}
}

// checkNodeBinary is WARN-on-missing — node is only required by skills
// that render Excalidraw / run JS tooling. Without it, those skills
// log a skip-reason and the rest of the pipeline keeps running.
func checkNodeBinary(_ context.Context, _ doctorEnv) CheckResult {
	path, err := exec.LookPath("node")
	if err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     "not found on PATH",
			Remediation: "Skills that render Excalidraw (apex-create-event-storming, apex-create-wireframes, apex-create-mockups) require Node 18+. Install via your package manager or volta.sh.",
		}
	}
	return CheckResult{Status: StatusOK, Message: path}
}

func checkNpxBinary(_ context.Context, _ doctorEnv) CheckResult {
	path, err := exec.LookPath("npx")
	if err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     "not found on PATH",
			Remediation: "Ships with Node.js — installing node also provides npx.",
		}
	}
	return CheckResult{Status: StatusOK, Message: path}
}

// checkPlaywrightHostSupported flags Linux hosts whose Ubuntu major
// isn't yet recognised by Playwright's installer. macOS and Windows
// always return INFO — we don't probe their compatibility today
// because the Excalidraw-rendering skills run on Linux in CI.
func checkPlaywrightHostSupported(_ context.Context, env doctorEnv) CheckResult {
	if env.OS != "linux" {
		return CheckResult{
			Status:  StatusInfo,
			Message: fmt.Sprintf("not probed on %s/%s", env.OS, env.Arch),
		}
	}
	id := strings.ToLower(env.OSRelease["ID"])
	version := env.OSRelease["VERSION_ID"]
	if id == "" || version == "" {
		return CheckResult{
			Status:  StatusInfo,
			Message: "/etc/os-release missing or unreadable; can't verify Playwright support",
		}
	}
	if id != "ubuntu" {
		return CheckResult{
			Status:  StatusInfo,
			Message: fmt.Sprintf("non-Ubuntu Linux (%s %s); Playwright support not probed", id, version),
		}
	}
	for _, supported := range playwrightSupportedUbuntuVersions {
		if version == supported {
			return CheckResult{
				Status:  StatusOK,
				Message: fmt.Sprintf("Ubuntu %s on Playwright supported list", version),
			}
		}
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: fmt.Sprintf("Ubuntu %s not on Playwright supported list (current allowlist: %s)", version, strings.Join(playwrightSupportedUbuntuVersions, ", ")),
		Remediation: "Skills that render Excalidraw will skip their final step until Playwright supports this OS. " +
			"Workaround: set PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS=1, or pre-stage ~/.cache/ms-playwright/ from a supported host.",
		FixCommand: "export PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS=1",
	}
}

// checkPlaywrightCache stats the canonical Playwright cache location.
// Present cache means the conversion utility can skip the install
// step on next run; absent cache is informational, not a failure —
// the first Excalidraw-rendering pipeline run will install it.
func checkPlaywrightCache(_ context.Context, env doctorEnv) CheckResult {
	if env.Home == "" {
		return CheckResult{Status: StatusInfo, Message: "$HOME unresolved; cache location unknown"}
	}
	dir := filepath.Join(env.Home, ".cache", "ms-playwright")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return CheckResult{
				Status:  StatusInfo,
				Message: fmt.Sprintf("not present at %s (will be populated on first Excalidraw-rendering run)", dir),
			}
		}
		return CheckResult{Status: StatusWarn, Message: fmt.Sprintf("read %s: %v", dir, err)}
	}
	var chromiumBuilds int
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "chromium-") {
			chromiumBuilds++
		}
	}
	if chromiumBuilds == 0 {
		return CheckResult{
			Status:  StatusInfo,
			Message: fmt.Sprintf("cache dir exists at %s but holds no chromium-* build", dir),
		}
	}
	return CheckResult{
		Status:  StatusOK,
		Message: fmt.Sprintf("%d chromium build(s) cached at %s", chromiumBuilds, dir),
	}
}

// checkFrameworkMetadata probes <projectRoot>/_apex/framework.yaml.
// Outside a project (no _apex/ directory at all) we return INFO so
// fresh-install users see a friendly hint, not a failure.
func checkFrameworkMetadata(_ context.Context, env doctorEnv) CheckResult {
	if env.ProjectRoot == "" {
		return CheckResult{Status: StatusInfo, Message: "no project root resolved"}
	}
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{
			Status:  StatusInfo,
			Message: fmt.Sprintf("%s does not look like an ape project (no _apex/ or .git)", env.ProjectRoot),
		}
	}
	meta, err := framework.ReadMetadata(env.ProjectRoot)
	if err != nil {
		var notInstalled *framework.NotInstalledError
		if errors.As(err, &notInstalled) {
			return CheckResult{
				Status:      StatusWarn,
				Message:     "framework metadata not found",
				Remediation: "Run `ape framework setup` to install the canonical skills + pipelines.",
				FixCommand:  "ape framework setup",
			}
		}
		return CheckResult{Status: StatusFail, Message: fmt.Sprintf("read metadata: %v", err)}
	}
	ref := meta.Framework.VersionTag
	if ref == "" {
		ref = meta.Framework.GitHash
	}
	return CheckResult{
		Status:  StatusOK,
		Message: fmt.Sprintf("framework %s installed (schema %s)", ref, meta.ConfigSchemaVersion),
	}
}

// operatingRulesManaged reads framework.yaml and reports whether this
// project's install manages the operating-rules fragment
// (Sources.OperatingRules.Managed). The operating_rules.* checks self-gate
// on it: only a managed install can hard-FAIL (the fragment / import /
// skill genuinely went missing). Non-projects, framework versions that
// predate the fragment, and legacy installs that predate this feature all
// return managed=false, keeping `ape doctor` green (INFO/WARN, not FAIL).
func operatingRulesManaged(root string) (managed, inProject bool) {
	if !isProjectRoot(root) {
		return false, false
	}
	meta, err := framework.ReadMetadata(root)
	if err != nil {
		return false, true // framework.metadata check surfaces the real state
	}
	return meta.Sources.OperatingRules.Managed, true
}

// checkOperatingRulesFragment verifies the always-on operating-rules
// fragment (PLAN-47 Workstream C) is present. Required, but only hard-fails
// when the install records it as managed yet the file is gone.
func checkOperatingRulesFragment(_ context.Context, env doctorEnv) CheckResult {
	managed, inProject := operatingRulesManaged(env.ProjectRoot)
	if !inProject {
		return CheckResult{Status: StatusInfo, Message: "not in a framework project — operating-rules check skipped"}
	}
	if !managed {
		return CheckResult{
			Status:      StatusWarn,
			Message:     "operating-rules fragment not managed by this install",
			Remediation: "Run `ape framework update` against a framework that ships _apex/apex-operating-rules.md to install the always-on APEX rules.",
			FixCommand:  "ape framework update",
		}
	}
	path := filepath.Join(env.ProjectRoot, framework.ProjectOperatingRules)
	if _, err := os.Stat(path); err != nil {
		return CheckResult{
			Status:      StatusFail,
			Message:     fmt.Sprintf("managed fragment missing at %s", framework.ProjectOperatingRules),
			Remediation: "Run `ape framework update` to reinstall the operating-rules fragment.",
			FixCommand:  "ape framework update",
		}
	}
	return CheckResult{Status: StatusOK, Message: framework.ProjectOperatingRules}
}

// checkOperatingRulesImport verifies the repo-root CLAUDE.md carries the
// managed @import of the fragment. This is a syntactic check — it proves
// the import line is present inside the markers, not that Claude Code
// resolved it at runtime (drive a real session to verify that).
func checkOperatingRulesImport(_ context.Context, env doctorEnv) CheckResult {
	managed, inProject := operatingRulesManaged(env.ProjectRoot)
	if !inProject {
		return CheckResult{Status: StatusInfo, Message: "not in a framework project — CLAUDE.md import check skipped"}
	}
	if !managed {
		return CheckResult{Status: StatusInfo, Message: "operating-rules not managed on this install (see operating_rules.fragment)"}
	}
	path := filepath.Join(env.ProjectRoot, framework.ProjectClaudeMd)
	data, err := os.ReadFile(path)
	if err != nil {
		return CheckResult{
			Status:      StatusFail,
			Message:     fmt.Sprintf("repo-root %s unreadable: %v", framework.ProjectClaudeMd, err),
			Remediation: "Run `ape framework update` to recreate the CLAUDE.md managed block.",
			FixCommand:  "ape framework update",
		}
	}
	body, ok, err := framework.FindManagedBlock(data)
	if err != nil {
		return CheckResult{
			Status:      StatusFail,
			Message:     fmt.Sprintf("%s has malformed apex:managed markers: %v", framework.ProjectClaudeMd, err),
			Remediation: "Fix or remove the apex:managed markers in CLAUDE.md, then run `ape framework update`.",
		}
	}
	if !ok || !strings.Contains(body, framework.OperatingRulesImport) {
		return CheckResult{
			Status:      StatusFail,
			Message:     fmt.Sprintf("%s is missing the managed operating-rules import", framework.ProjectClaudeMd),
			Remediation: "Run `ape framework update` to write the managed block.",
			FixCommand:  "ape framework update",
		}
	}
	return CheckResult{Status: StatusOK, Message: fmt.Sprintf("%s imports %s", framework.ProjectClaudeMd, framework.OperatingRulesImport)}
}

// checkOrchestratorSkill verifies the apex-orchestrator persona skill is
// installed. It rides the generic skill-install path; this check ties it
// into the operating-rules contract.
func checkOrchestratorSkill(_ context.Context, env doctorEnv) CheckResult {
	managed, inProject := operatingRulesManaged(env.ProjectRoot)
	if !inProject {
		return CheckResult{Status: StatusInfo, Message: "not in a framework project — orchestrator-skill check skipped"}
	}
	if !managed {
		return CheckResult{Status: StatusInfo, Message: "operating-rules not managed on this install (see operating_rules.fragment)"}
	}
	dir := filepath.Join(framework.ProjectSkillsPath(env.ProjectRoot), framework.OrchestratorSkill)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return CheckResult{
			Status:      StatusFail,
			Message:     fmt.Sprintf("%s skill not installed at %s", framework.OrchestratorSkill, dir),
			Remediation: "Run `ape framework update` to install the apex-orchestrator persona skill.",
			FixCommand:  "ape framework update",
		}
	}
	return CheckResult{Status: StatusOK, Message: framework.OrchestratorSkill + " installed"}
}

func checkSkillsProject(_ context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "not in a project — project skill check skipped"}
	}
	dir := framework.ProjectSkillsPath(env.ProjectRoot)
	names, err := framework.ListInstalledSkills(dir)
	if err != nil {
		return CheckResult{Status: StatusFail, Message: fmt.Sprintf("list skills: %v", err)}
	}
	if len(names) == 0 {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("no skills installed at %s", dir),
			Remediation: "Run `ape framework setup` (first time) or `ape framework update` (existing project).",
		}
	}
	framework, custom := splitFrameworkAndCustomSkills(names)
	return CheckResult{
		Status: StatusOK,
		Message: fmt.Sprintf("%d skills at %s (%d framework + %d custom)",
			len(names), dir, framework, custom),
	}
}

func checkSkillsUser(_ context.Context, env doctorEnv) CheckResult {
	dir := framework.UserSkillsPath()
	if dir == "" {
		return CheckResult{Status: StatusInfo, Message: "$HOME unresolved; user skills location unknown"}
	}
	names, err := framework.ListInstalledSkills(dir)
	if err != nil {
		return CheckResult{Status: StatusWarn, Message: fmt.Sprintf("list user skills: %v", err)}
	}
	if len(names) == 0 {
		return CheckResult{
			Status:  StatusInfo,
			Message: fmt.Sprintf("0 skills at %s (project-scoped install only)", dir),
		}
	}
	return CheckResult{
		Status:  StatusOK,
		Message: fmt.Sprintf("%d skills at %s", len(names), dir),
	}
}

func checkPipelinesProject(_ context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "not in a project — pipeline check skipped"}
	}
	dir := pipeline.PipelinesDir(env.ProjectRoot)
	names := pipeline.AvailablePipelines(env.ProjectRoot)
	if len(names) == 0 {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("no pipelines installed at %s", dir),
			Remediation: "Run `ape framework setup` or `ape framework update` to install the canonical pipelines.",
		}
	}
	// Validate each spec's `model:` values while we are here. A typo in a
	// checked-in spec otherwise stays invisible until a run reaches that step
	// and claude rejects it — which can be many minutes in.
	var modelIssues, specIssues, styleNotes []string
	for _, name := range names {
		spec, err := pipeline.LoadSpec(name, env.ProjectRoot)
		if err != nil {
			continue // spec-load failures are the loader's story, not this check's
		}
		for _, w := range spec.ModelWarnings() {
			modelIssues = append(modelIssues, fmt.Sprintf("%s → %s: %q", name, w.Location, w.Model))
		}
		// A stage that declares two models is not a typo — every value in
		// it is real — but the run can only honour the first, so it
		// belongs in the same on-demand report as a model ape cannot
		// resolve. Both are "the spec says something the run will not do".
		for _, c := range spec.StageModelConflicts() {
			for _, s := range c.Steps {
				specIssues = append(specIssues, fmt.Sprintf(
					"%s → stage %q step %d (%s) declares %q but the stage runs on %q",
					name, c.Stage, s.Index, s.Skill, s.Declared, c.Launch))
			}
		}
		for _, k := range spec.UnknownKeyWarnings() {
			specIssues = append(specIssues, fmt.Sprintf(
				"%s → %s: unknown key %q (line %d)", name, k.Location, k.Key, k.Line))
		}
		// The pipeline YAML is the only surface where a checked-in style
		// name is looked at by anything: the per-skill CSV has its own
		// doctor check, and the flag is echoed at spawn. Nothing else
		// reads these, and Claude Code ignores a name it cannot resolve
		// in silence — so an unnoticed edit here enrols nothing and
		// passes every gate in ape and the framework both.
		for _, decl := range spec.OutputStyleDeclarations() {
			if _, builtin := config.BuiltinOutputStyleSpelling(decl.Style); !builtin {
				styleNotes = append(styleNotes, fmt.Sprintf(
					"%s → %s: output-style %q names no built-in", name, decl.Location, decl.Style))
			}
		}
	}
	if len(specIssues) > 0 {
		return CheckResult{
			Status: StatusWarn,
			Message: fmt.Sprintf("%d pipelines at %s; %s",
				len(names), dir, strings.Join(append(modelIssues, specIssues...), "; ")),
			Remediation: "A stage is one claude session launched with its first step's model, so a later " +
				"step's `model:` cannot be applied — split the stage at its model boundaries. An unknown " +
				"key is silently ignored: check the spelling, or upgrade ape if the framework is newer.",
		}
	}
	if len(modelIssues) > 0 {
		return CheckResult{
			Status:  StatusWarn,
			Message: fmt.Sprintf("%d pipelines at %s; unrecognized model(s): %s", len(names), dir, strings.Join(modelIssues, "; ")),
			Remediation: "Those `model:` values are not ones ape recognizes — likely typos. Accepted forms: a bare " +
				"family (sonnet, opus, haiku) for its current generation, or an explicit id " +
				"(sonnet-5, claude-sonnet-5, opus[1m]). A model newer than this ape build is passed through unchanged.",
		}
	}
	if len(styleNotes) > 0 {
		// Info, not Warn: a custom style installed on the machine is a
		// legitimate declaration and ape cannot see it from here. The
		// line exists so a typo is READABLE, not so it is convicted.
		return CheckResult{
			Status: StatusInfo,
			Message: fmt.Sprintf("%d pipelines at %s; %s",
				len(names), dir, strings.Join(styleNotes, "; ")),
			Remediation: fmt.Sprintf(
				"Built-in styles are %s. A name outside that set applies only if the machine has a custom "+
					"style by it; otherwise Claude Code ignores it silently and the stage runs %s.",
				strings.Join(config.BuiltinOutputStyles(), ", "), config.DefaultOutputStyle),
		}
	}
	return CheckResult{
		Status:  StatusOK,
		Message: fmt.Sprintf("%d pipelines at %s: %s", len(names), dir, strings.Join(names, ", ")),
	}
}

func checkPermissionsHomeClaude(_ context.Context, env doctorEnv) CheckResult {
	if env.Home == "" {
		return CheckResult{Status: StatusInfo, Message: "$HOME unresolved; can't probe write permissions"}
	}
	dir := filepath.Join(env.Home, ".claude")
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return CheckResult{
				Status:  StatusInfo,
				Message: fmt.Sprintf("%s does not yet exist (claude creates it on first run)", dir),
			}
		}
		return CheckResult{Status: StatusWarn, Message: fmt.Sprintf("stat %s: %v", dir, err)}
	}
	tmp, err := os.CreateTemp(dir, ".ape-doctor-write-probe-*")
	if err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("not writable: %v", err),
			Remediation: fmt.Sprintf("Fix ownership/permissions on %s — claude needs to write session state here.", dir),
		}
	}
	probePath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(probePath)
	return CheckResult{Status: StatusOK, Message: fmt.Sprintf("%s is writable", dir)}
}

func checkApeUpdateAvailable(_ context.Context, _ doctorEnv) CheckResult {
	entry := updatecache.Load()
	if entry == nil {
		return CheckResult{
			Status:  StatusInfo,
			Message: "no cached update check — background probe will refresh on next ape invocation",
		}
	}
	if isNewerVersion(Version, entry.LatestVersion) {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("update available: %s → %s", Version, entry.LatestVersion),
			Remediation: "Run `ape update` to install the latest release.",
			FixCommand:  "ape update",
		}
	}
	return CheckResult{
		Status:  StatusOK,
		Message: fmt.Sprintf("on latest (%s)", entry.LatestVersion),
	}
}

// ---- Sandbox (Kata VM workspace) checks -----------------------------------
//
// These probe the host prerequisites for `ape sandbox` (PLAN-16 D8). They
// are non-required and degrade to INFO on non-Linux hosts and when the
// sandbox toolchain isn't installed, so `ape doctor` stays green for users
// who don't use workspaces while still surfacing setup gaps for those who do.
// Probes are deliberately non-blocking (stat + LookPath, no daemon
// round-trips) so doctor never hangs on an unresponsive containerd.

// checkKVMAvailable verifies /dev/kvm exists and is openable by the current
// user (the kvm group). Kata microVMs need KVM.
// checkPriceTableCoverage compares ape's built-in price table against the
// model ids the locally-installed Claude Code is actually writing into its
// transcripts.
//
// ape's prices are hand-curated (Anthropic publishes no price API) and
// Claude Code releases on a schedule ape does not control, so a model id
// can change under a shipped ape binary at any time. When it does, token
// counts stay correct and cost silently goes to zero — the failure mode
// that went unnoticed from 2026-07-14. A release-time check cannot catch a
// model that ships afterwards; this one runs against whatever Claude Code
// is installed at the moment it is invoked, which is the only place the
// answer is current.
//
// WARN, not FAIL: an out-of-date price table misreports cost, it does not
// stop any pipeline from running.
func checkPriceTableCoverage(_ context.Context, env doctorEnv) CheckResult {
	// Cached: a full sweep reads every transcript in the window — hundreds of
	// megabytes on an active machine — and doctor reports rather than gates.
	// `ape costs coverage` and `make check-prices` always sweep fresh.
	rep, age, fromCache, err := cost.ObserveModelsCached(env.Home, time.Now().Add(-cost.DefaultCoverageWindow))
	if err != nil {
		return CheckResult{Status: StatusSkip, Message: fmt.Sprintf("could not read transcripts: %v", err)}
	}
	// Never let a cached verdict pass as a fresh one — keyed on fromCache, not
	// on age, which is ~0 for a cache hit written moments ago.
	cached := ""
	if fromCache {
		cached = fmt.Sprintf(" [cached %s ago; `ape costs coverage` re-checks]", age.Round(time.Second))
	}
	// A dropped override row is worth reporting even when no transcripts were
	// found: it is a fact about the local config, not about observed usage.
	badOverrides := cost.RejectedOverrides()
	if !rep.Observed() && len(badOverrides) == 0 {
		return CheckResult{
			Status:  StatusSkip,
			Message: "no Claude Code transcripts in the last 30 days — coverage not verified",
		}
	}
	if rep.OK() && len(badOverrides) == 0 {
		return CheckResult{
			Status:  StatusOK,
			Message: rep.Summary() + cached,
		}
	}
	gaps := rep.Gaps()
	names := make([]string, 0, len(gaps)+len(rep.AliasDrifts)+len(badOverrides)+1)
	names = append(names, badOverrides...)
	for _, g := range gaps {
		names = append(names, fmt.Sprintf("%s (%s, %d turns)", g.Model, g.Source, g.Turns))
	}
	for _, d := range rep.AliasDrifts {
		names = append(names, fmt.Sprintf("alias %s→%s superseded by %s",
			d.Alias, d.Target, strings.Join(d.Newer, "/")))
	}
	// Carried on the warn path too. The OK path gets it via rep.Summary();
	// without this the gap would be visible only while the price table was
	// healthy, which is the wrong way round.
	if s := rep.WindowGapSummary(); s != "" {
		names = append(names, s)
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: strings.Join(names, ", ") + cached,
		Remediation: "ape's price table does not cover these exactly, so reported costs are estimated or zero. " +
			"An alias drift additionally means a bare `sonnet` / `opus` in a spec or --model resolves to a " +
			"generation you are not running. Confirm rates at https://platform.claude.com/docs/en/about-claude/pricing, " +
			"then upgrade ape, or persist rates locally with `ape costs update --from <file>` and " +
			"`ape costs reprice --write`.",
		FixCommand: "ape costs coverage",
	}
}

// checkTerminalContracts reports whether the framework's per-skill
// terminal-contract table is installed, and how many skills it enrols.
//
// The check itself is silent at run time — an absent table simply enrols
// nothing — which is the right default (most projects have no table) but
// leaves no way to tell an inactive check from a passing one. That is the
// same "believed-present protection" trap checkHookContractDrift exists
// to avoid, so the answer lives here: silent during runs, discoverable on
// demand.
func checkTerminalContracts(_ context.Context, env doctorEnv) CheckResult {
	if env.ProjectRoot == "" || !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "no project root resolved"}
	}
	tbl, err := contract.Load(env.ProjectRoot)
	if err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("%s unreadable: %v", contract.TableFile, err),
			Remediation: "Terminal-contract checks are disabled until the table parses. Re-run `ape framework update` to restore it.",
			FixCommand:  "ape framework update",
		}
	}
	if tbl.Len() == 0 {
		return CheckResult{
			Status: StatusInfo,
			Message: fmt.Sprintf("%s not installed — no run is checked for a terminal contract",
				contract.TableFile),
			Remediation: "The framework declares which skills end a run with a machine-readable " +
				"return block. Without the table ape cannot tell a batch that finished from one " +
				"that silently stopped early. `ape framework update` installs it, if your " +
				"framework version ships one.",
			FixCommand: "ape framework update",
		}
	}
	msg := fmt.Sprintf("%d skill(s) enrolled: %s", tbl.Len(), strings.Join(tbl.Skills(), ", "))
	if len(tbl.Warnings) > 0 {
		return CheckResult{
			Status:      StatusWarn,
			Message:     msg + " — " + strings.Join(tbl.Warnings, "; "),
			Remediation: "Rows that do not parse are skipped, so those skills are unchecked.",
		}
	}
	return CheckResult{Status: StatusOK, Message: msg}
}

// checkOutputStyles reports which skills the project's framework enrols
// in a non-default output style, and what each one resolves to.
//
// Silent during runs, discoverable on demand — the same shape as
// checkTerminalContracts, and for the same reason. Nothing about a run
// tells you whether the style you believe is active actually is: a
// missing table, an unenrolled skill and a style name Claude Code could
// not resolve all produce the identical, unremarkable outcome of a
// session in the default style. This is the only place that distinguishes
// them.
func checkOutputStyles(_ context.Context, env doctorEnv) CheckResult {
	if env.ProjectRoot == "" || !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "no project root resolved"}
	}
	tbl, err := outputstyles.Load(env.ProjectRoot)
	if err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("%s unreadable: %v", outputstyles.TableFile, err),
			Remediation: "Every skill runs the pinned default until the table parses. Re-run `ape framework update` to restore it.",
			FixCommand:  "ape framework update",
		}
	}
	if tbl.Len() == 0 {
		return CheckResult{
			Status: StatusInfo,
			Message: fmt.Sprintf("%s not installed — every skill runs the %s output style",
				outputstyles.TableFile, config.DefaultOutputStyle),
			Remediation: "The framework declares which skills run under which output style. " +
				"`ape framework update` installs the table, if your framework version ships one.",
			FixCommand: "ape framework update",
		}
	}
	pairs := make([]string, 0, tbl.Len())
	var unknown []string
	for _, e := range tbl.Entries() {
		skill, style := e[0], e[1]
		canonical, builtin := config.BuiltinOutputStyleSpelling(style)
		pairs = append(pairs, skill+"→"+canonical)
		if !builtin {
			unknown = append(unknown, fmt.Sprintf("%s→%s", skill, style))
		}
	}
	msg := fmt.Sprintf("%d skill(s) enrolled: %s", tbl.Len(), strings.Join(pairs, ", "))
	if len(tbl.Warnings) > 0 {
		return CheckResult{
			Status:      StatusWarn,
			Message:     msg + " — " + strings.Join(tbl.Warnings, "; "),
			Remediation: "Rows that do not parse are skipped, so those skills run the pinned default.",
		}
	}
	// Reported, not judged. ape folds a built-in's case when it writes
	// the settings key, so a spelling difference is no longer a finding —
	// but a name matching no built-in is either a custom style the
	// machine has installed or a typo that will enrol nothing, and ape
	// cannot tell those apart. A human reading this line can.
	if len(unknown) > 0 {
		return CheckResult{
			Status: StatusInfo,
			Message: msg + fmt.Sprintf(" — not built-in style(s): %s",
				strings.Join(unknown, ", ")),
			Remediation: fmt.Sprintf(
				"Those name no built-in (%s). If the machine has a custom style by that name it applies; "+
					"if it is a typo, Claude Code ignores it silently and the skill runs %s. "+
					"ape cannot enumerate installed custom styles, so it reports rather than guesses.",
				strings.Join(config.BuiltinOutputStyles(), ", "), config.DefaultOutputStyle),
		}
	}
	return CheckResult{Status: StatusOK, Message: msg}
}

// checkCommandSurface compares the command surface the installed framework
// declares it requires against the one this binary actually provides.
//
// The gap it closes: from framework v0.11.0 the skills shell out to ape
// subcommands with every fallback branch removed, so an ape that predates a
// command makes a skill fail deep inside a multi-hour stage. `framework.metadata`
// compares framework versions, not ape's, so nothing here noticed. An eval
// capture came within a hand-check of measuring eight hours of broken runs
// against exactly that mismatch.
//
// Resolution is a name diff against cobra's own tree rather than a version
// floor, because a locally-built ape reports a Go pseudo-version that a floor
// check skips as unstamped — it would pass on precisely the binary in question.
//
// Required, and FAIL when something is missing: a framework whose commands
// this binary cannot provide is not degraded, it is broken, and the whole
// point is that the failure otherwise surfaces hours later inside a stage.
func checkCommandSurface(_ context.Context, env doctorEnv) CheckResult {
	if env.ProjectRoot == "" || !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "no project root resolved"}
	}
	manifest, err := framework.LoadApeCommands(env.ProjectRoot)
	if err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     err.Error(),
			Remediation: "The command surface cannot be verified until the manifest parses. `ape framework update` reinstalls it.",
			FixCommand:  "ape framework update",
		}
	}
	if manifest == nil {
		return CheckResult{
			Status: StatusSkip,
			Message: fmt.Sprintf("%s not installed — framework predates the command-surface contract",
				framework.ProjectApeCommands),
		}
	}
	if len(manifest.Required) == 0 {
		return CheckResult{
			Status:  StatusSkip,
			Message: fmt.Sprintf("%s declares no required commands", framework.ProjectApeCommands),
		}
	}

	missing := missingCommands(manifest.Required)
	if len(missing) == 0 {
		return CheckResult{
			Status:  StatusOK,
			Message: fmt.Sprintf("%d required command(s) provided", len(manifest.Required)),
		}
	}
	// The whole difference, never the first miss: an operator on an old
	// binary wants one line naming everything, not a bisect.
	return CheckResult{
		Status: StatusFail,
		Message: fmt.Sprintf("%d of %d required command(s) missing: %s",
			len(missing), len(manifest.Required), strings.Join(missing, ", ")),
		Remediation: "The installed framework calls ape commands this binary does not provide, and " +
			"its skills have no fallback branch — each will fail mid-run. Upgrade ape; if they are " +
			"still missing on the latest, the framework requires an ape that does not exist yet.",
		FixCommand: "ape update",
	}
}

// checkAboardSkillReference reports capsHash drift between the board this
// binary mounts and a `.claude/skills/aboard/` reference copied into the
// project.
//
// It is deliberately the ONLY thing doctor says about the board. "Does ape
// provide `ape aboard`" would be tautological — the tree is compiled in, so
// the check could only assert that this binary contains a package it
// demonstrably contains. Drift is the opposite: the skill is a COPY, the
// renderers travel inside the binary, and the two move independently. An agent
// reading a stale reference writes state no renderer reads and the write still
// says "applied", which is the failure that otherwise looks like success.
//
// `ape aboard status` already reports this, but only from inside a project
// that has a board — which is exactly the project that has not been used yet
// when the reference goes stale. Doctor sees it either way.
//
// The verdict comes from aboard.Status rather than a local re-read: the
// stamped-hash parse and the manifest hash are the library's to define, and a
// second implementation here would be free to disagree with the board that
// serves it. Status probes a recorded board over localhost, but the Skill
// fields are resolved before that and never depend on it.
func checkAboardSkillReference(ctx context.Context, env doctorEnv) CheckResult {
	if env.ProjectRoot == "" || !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "no project root resolved"}
	}
	rep := aboard.Status(ctx, aboard.Root(env.ProjectRoot), "", aboard.WebFS())
	switch rep.Skill {
	case aboard.SkillAbsent:
		// Not drift. A project that never copied the skill has nothing to
		// be out of date, and most projects never copy it.
		return CheckResult{
			Status:  StatusSkip,
			Message: "no .claude/skills/aboard reference copied into this project",
		}
	case aboard.SkillCurrent:
		return CheckResult{
			Status:  StatusOK,
			Message: fmt.Sprintf("skill reference current (capsHash %s)", rep.CapsHash),
		}
	default:
		return CheckResult{
			Status: StatusWarn,
			Message: fmt.Sprintf("skill reference stamped %s, this binary serves %s",
				rep.SkillCapsHash, rep.CapsHash),
			Remediation: "The copied skill describes a board this binary no longer serves. An agent " +
				"reading it can set state no renderer reads, and the write still reports success. " +
				"Regenerate both generated references against this binary.",
			FixCommand: "ape aboard capabilities --format md > " +
				".claude/skills/aboard/references/reference.generated.md",
		}
	}
}

// commandTreeMu guards resolution against the shared root command.
//
// cobra.Command.Find is NOT read-only: it calls mergePersistentFlags, which
// lazily initialises and writes flag state on every command it walks. Two
// goroutines resolving against the same tree therefore race, which the race
// detector catches the moment two tests do it in parallel. runDoctor runs
// checks sequentially so production never hit it, but a helper that is only
// safe because of its caller's scheduling is a trap for the next caller.
var commandTreeMu sync.Mutex

// missingCommands resolves each manifest entry against the root command
// tree and returns those that do not fully resolve, in manifest order.
//
// A partial match counts as missing: `cobra.Find` returns the deepest
// command it could reach plus the leftover arguments, so `ape story fields`
// on a binary with `ape story` but no `fields` comes back as the parent with
// "fields" unconsumed. Treating that as present is the failure mode this
// check exists to prevent.
func missingCommands(required []string) []string {
	commandTreeMu.Lock()
	defer commandTreeMu.Unlock()

	var missing []string
	for _, entry := range required {
		path := framework.CommandPath(entry)
		if len(path) == 0 {
			continue
		}
		cmd, rest, err := rootCmd.Find(path)
		if err != nil || cmd == nil || len(rest) > 0 {
			missing = append(missing, entry)
		}
	}
	return missing
}

// checkHookContractDrift reports whether the hook-payload fields ape's
// step-completion gates depend on are still present in the events the
// locally-installed Claude Code is emitting.
//
// The failure this guards against is silent: a renamed field does not
// error, it just stops the gate firing, and ape goes back to reporting
// success on runs that did nothing. That is strictly worse than having no
// gate, because it turns an absent protection into a believed-present
// one. Same shape of problem as the price table drifting under a released
// binary, and the same answer — read what the harness is actually
// emitting rather than what it emitted at release time.
func checkHookContractDrift(_ context.Context, env doctorEnv) CheckResult {
	rep, err := hookdrift.Observe(env.ProjectRoot, time.Now().Add(-hookdrift.DefaultWindow))
	if err != nil {
		return CheckResult{Status: StatusSkip, Message: fmt.Sprintf("could not read run logs: %v", err)}
	}
	if !rep.Observed() {
		return CheckResult{
			Status:  StatusSkip,
			Message: "no interactive runs in the last 30 days — hook contract not verified",
		}
	}
	if rep.OK() {
		return CheckResult{Status: StatusOK, Message: rep.Summary()}
	}
	names := make([]string, 0, 2)
	for _, o := range rep.Drifted() {
		names = append(names, fmt.Sprintf("%s absent from all %d %s payload(s)", o.Field, o.Seen, o.Event))
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: strings.Join(names, "; ") + " — " + rep.Summary(),
		Remediation: "Claude Code no longer sends a hook field ape's step-completion gates rely on. " +
			"Those gates are now silently inactive: a run whose agent yields while a spawned " +
			"agent is still outstanding can again be reported as a success having done nothing. " +
			"Upgrade ape; if this persists on the latest ape, report it — the hook contract has moved.",
		FixCommand: "ape update",
	}
}

// checkRunLayoutLegacy reports run artifacts still sitting at the
// pre-`_output/ape` paths.
//
// `_output/` is the framework's output_folder — it holds handoffs, briefs
// and verify reports — and ape used to scatter run artifacts through it as
// siblings of that content. ape now owns exactly one subtree,
// `_output/ape/`. Until a project is moved across, everything reading the
// new paths (cost rollups, `ape costs run`, the hook-contract check) sees
// a project with no history.
//
// WARN rather than FAIL: nothing is broken or lost, the records are just
// somewhere ape no longer looks, and one command relocates them.
func checkRunLayoutLegacy(_ context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: msgNotInAProject}
	}
	if !runlog.Pending(env.ProjectRoot) {
		root := runlog.ApeRoot(env.ProjectRoot)
		if rel, err := filepath.Rel(env.ProjectRoot, root); err == nil {
			root = rel
		}
		return CheckResult{Status: StatusOK, Message: "run artifacts are under " + root}
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: "run artifacts still at the legacy _output/pipelines and/or _output/tasks paths",
		Remediation: "ape now keeps everything it writes under {output_folder}/ape/, so the output folder stays the " +
			"framework's. Until these move, cost rollups and the hook-contract check read a project " +
			"with no history. `ape framework update` relocates them: nothing is overwritten, and a run " +
			"whose destination is already taken is reported rather than merged.",
		FixCommand: "ape framework update",
	}
}

func checkKVMAvailable(_ context.Context, env doctorEnv) CheckResult {
	if env.OS != "linux" {
		return CheckResult{Status: StatusInfo, Message: fmt.Sprintf("sandbox workspaces are Linux-only; not probed on %s", env.OS)}
	}
	const dev = "/dev/kvm"
	if _, err := os.Stat(dev); err != nil {
		if os.IsNotExist(err) {
			return CheckResult{Status: StatusInfo, Message: dev + " absent (no KVM; Kata workspaces unavailable on this host)"}
		}
		return CheckResult{Status: StatusWarn, Message: fmt.Sprintf("stat %s: %v", dev, err)}
	}
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("%s present but not accessible (%v)", dev, err),
			Remediation: "Add your user to the kvm group so Kata can open /dev/kvm, then log out and back in.",
			FixCommand:  "sudo usermod -aG kvm $USER",
		}
	}
	_ = f.Close()
	return CheckResult{Status: StatusOK, Message: dev + " present and accessible"}
}

// containerdSocket is the default rootful containerd control socket. aped's
// root executor drives containerd here; its presence is a cheap, non-hanging
// proxy for "the daemon is up" (a stat, never a daemon round-trip that could
// block an unresponsive containerd).
const containerdSocket = "/run/containerd/containerd.sock"

// checkContainerdRunning verifies both halves of the driver ape needs: the
// nerdctl CLI on PATH (ape shells out to it) AND a live rootful containerd
// (its control socket present). The check is named containerd.running, so
// nerdctl-on-PATH alone is not enough — a stopped daemon must not read as OK.
// Everything is a stat/LookPath: no daemon round-trip, so doctor never hangs.
func checkContainerdRunning(_ context.Context, env doctorEnv) CheckResult {
	if env.OS != "linux" {
		return CheckResult{Status: StatusInfo, Message: "not probed on non-Linux"}
	}
	path, err := exec.LookPath("nerdctl")
	if err != nil {
		return CheckResult{
			Status:      StatusInfo,
			Message:     "nerdctl not on PATH (sandbox workspaces need containerd + nerdctl)",
			Remediation: "Install containerd and nerdctl — ape shells out to nerdctl to drive Kata.",
		}
	}
	if _, err := os.Stat(containerdSocket); err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("nerdctl at %s but %s absent (containerd not running?)", path, containerdSocket),
			Remediation: "Start the rootful containerd daemon so ape/aped can drive Kata.",
			FixCommand:  "sudo systemctl enable --now containerd",
		}
	}
	return CheckResult{Status: StatusOK, Message: fmt.Sprintf("%s + containerd at %s", path, containerdSocket)}
}

// checkKataRuntime looks for a Kata containerd shim on PATH — the lightweight
// proxy for "the io.containerd.kata-*.v2 runtime is installed".
func checkKataRuntime(_ context.Context, env doctorEnv) CheckResult {
	if env.OS != "linux" {
		return CheckResult{Status: StatusInfo, Message: "not probed on non-Linux"}
	}
	for _, shim := range []string{
		"containerd-shim-kata-clh-v2",
		"containerd-shim-kata-qemu-v2",
		"containerd-shim-kata-v2",
	} {
		if p, err := exec.LookPath(shim); err == nil {
			return CheckResult{Status: StatusOK, Message: p}
		}
	}
	return CheckResult{
		Status:      StatusInfo,
		Message:     "no Kata containerd shim on PATH (io.containerd.kata-*.v2)",
		Remediation: "Install Kata Containers (kata-deploy or distro packages) to provision workspaces.",
	}
}

// checkSandboxImage reports how to confirm the official ape-sandbox image is
// pulled. It stays informational: inspecting the image store needs a
// containerd round-trip that may require privileges and could hang, which a
// health probe must not risk.
// checkSandboxCredentialACL verifies the host can grant aped access to a shared Claude
// credential, which is ACL-only by design: a group grant would hand the credential to
// every member of group `ape` (also the priv-socket gate), and `chgrp` additionally needs
// the group in the caller's ACTIVE session. There is no fallback, so a host without
// `setfacl` simply cannot share a session — better surfaced here than as a workspace that
// fails to start.
func checkSandboxCredentialACL(ctx context.Context, env doctorEnv) CheckResult {
	if env.OS != "linux" {
		return CheckResult{Status: StatusInfo, Message: "sandbox credential sharing is Linux-only; not probed on " + env.OS}
	}
	if _, err := exec.LookPath("setfacl"); err != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     "setfacl not found — 'ape sandbox credentials publish' cannot grant aped access to your Claude session",
			Remediation: "Install the acl package. Without it, workspaces cannot share your OAuth session (there is deliberately no broader fallback).",
			FixCommand:  "sudo apt install acl",
		}
	}
	// Present, but the filesystem holding the credential must also support ACLs — a
	// mount without them fails at publish time with "Operation not supported".
	home, err := os.UserHomeDir()
	if err != nil {
		return CheckResult{Status: StatusOK, Message: "setfacl present"}
	}
	probe := filepath.Join(home, ".claude")
	if _, serr := os.Stat(probe); serr != nil {
		return CheckResult{Status: StatusOK, Message: "setfacl present (no ~/.claude yet to probe)"}
	}
	if out, gerr := exec.CommandContext(ctx, "getfacl", "-cE", probe).CombinedOutput(); gerr != nil {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("ACLs unavailable on %s: %s", probe, strings.TrimSpace(string(out))),
			Remediation: "Mount the filesystem holding ~/.claude with the `acl` option, or keep credentials on one that has it.",
		}
	}
	return CheckResult{Status: StatusOK, Message: "setfacl present and ~/.claude supports ACLs"}
}

func checkSandboxImage(_ context.Context, env doctorEnv) CheckResult {
	if env.OS != "linux" {
		return CheckResult{Status: StatusInfo, Message: "not probed on non-Linux"}
	}
	if _, err := exec.LookPath("nerdctl"); err != nil {
		return CheckResult{Status: StatusInfo, Message: "nerdctl absent; cannot locate the ape-sandbox image"}
	}
	return CheckResult{
		Status:  StatusInfo,
		Message: fmt.Sprintf("confirm with `nerdctl images %s` (or set image: in the profile)", sandbox.DefaultImage),
	}
}

// checkSandboxApeDelivery reports whether this node can hand an `ape` to a workspace.
//
// aped mounts the `ape` beside itself into every workspace (PLAN-23) and refuses to start
// without one, so this check exists to answer the question BEFORE a failed restart: an
// operator who installs only `aped` gets a daemon that will not come up, and the fastest
// way to see why is `ape doctor` rather than journalctl.
//
// Deliberately shallow — it looks for the sibling, not for a matching version. Only the
// node's own aped knows its build identity, and duplicating that comparison here would
// give a second, weaker answer to a question the daemon already answers definitively.
func checkSandboxApeDelivery(_ context.Context, env doctorEnv) CheckResult {
	if env.OS != "linux" {
		return CheckResult{Status: StatusInfo, Message: "not probed on non-Linux (aped is Linux-only)"}
	}
	apedPath, err := exec.LookPath("aped")
	if err != nil {
		return CheckResult{Status: StatusInfo, Message: "aped not on PATH; delivery is a node-side concern"}
	}
	if resolved, rerr := filepath.EvalSymlinks(apedPath); rerr == nil {
		apedPath = resolved
	}
	sibling := filepath.Join(filepath.Dir(apedPath), "ape")
	st, err := os.Stat(sibling)
	if err != nil {
		return CheckResult{
			Status:  StatusWarn,
			Message: fmt.Sprintf("no ape beside %s — aped will refuse to start", apedPath),
			Remediation: fmt.Sprintf("Install both binaries together (the release archive ships both): "+
				"copy ape next to %s, or point the daemon at one with `aped front --ape-binary <path>`.", apedPath),
		}
	}
	if st.Mode().Perm()&0o002 != 0 {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("%s is world-writable (mode %v); aped will refuse it", sibling, st.Mode().Perm()),
			Remediation: fmt.Sprintf("chmod 0755 %s — it is executed inside every workspace on this node.", sibling),
		}
	}
	return CheckResult{
		Status:  StatusOK,
		Message: fmt.Sprintf("%s will be delivered to workspaces at %s", sibling, sandbox.ApeBinDest),
	}
}

// isProjectRoot uses a lightweight heuristic: the directory contains
// at least one of _apex/, .git, or .claude/. Lets doctor degrade
// project-scoped checks to INFO without false positives on the user's
// scratch directory.
func isProjectRoot(dir string) bool {
	if dir == "" {
		return false
	}
	for _, sub := range []string{"_apex", ".git", ".claude"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err == nil {
			return true
		}
	}
	return false
}

// splitFrameworkAndCustomSkills tallies framework-managed vs.
// user-installed skills under a project's .claude/skills/ tree.
func splitFrameworkAndCustomSkills(names []string) (fwk, custom int) {
	for _, n := range names {
		if framework.IsFrameworkSkill(n) {
			fwk++
		} else {
			custom++
		}
	}
	return fwk, custom
}
