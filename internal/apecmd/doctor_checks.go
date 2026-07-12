package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/pipeline"
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
