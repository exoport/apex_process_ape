package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/diegosz/apex_process_ape/internal/framework"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/updatecache"
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
