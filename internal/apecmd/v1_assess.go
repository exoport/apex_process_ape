package apecmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/frontmatter"
)

// The assessor's launch (PLAN-V15-ASSESSOR step 4). `ape task apex-assess`
// runs in a batch folder the coordinator built (apexv1.BuildAssessFolder),
// and gets a session that can read that folder and write its notice.json,
// nothing else:
//
//   - claude --tools Read,Grep,Glob,Write: the built-in roster, an
//     allow-list (R3);
//   - pre-tool registered for every tool, an MCP tool included, under
//     APE_ASSESS = the folder: the paths of those four bound to it, and any
//     other tool denied and recorded beside the folder (apexv1.AssessGuard;
//     R2, R3);
//   - the inline deny rules for every other tool, which bind in bypass mode;
//   - --setting-sources user, so the snapshot's own .claude/settings.json
//     hooks never run (research 7). Measured on Claude Code 2.1.316, that
//     also drops the project's skills, so ape supplies apex-assess as the
//     plugin ape-assess, built beside the folder from the SKILL.md the
//     coordinator's checkout installed (--assess-skill-from; never main's
//     copy, P4-1), and types /ape-assess:apex-assess;
//   - no project plugins: the snapshot's config would bring their hooks;
//   - --strict-mcp-config with no --mcp-config: no MCP server at all, not
//     even ape's own mcp-bridge, whose reply the guard denies (V8h);
//   - APE_TOOLCHAIN=local, so the hooks are this ape, not a pinned one.

// assessDenyRules are the tools the assessor never has, denied inline as
// well as by the roster.
var assessDenyRules = []string{"Bash", "Monitor", "Edit", "MultiEdit", "NotebookEdit", "Agent", "Task", "WebFetch", "WebSearch", "Skill", "PowerShell"}

// assessPluginSuffix names the plugin folder beside the batch folder.
const assessPluginSuffix = ".plugin"

// prepareAssessTask makes `ape task apex-assess`'s launch, after
// prepareV1Task's: o.projectRoot must be a batch folder.
func prepareAssessTask(o *taskOptions, cfg *runConfig) error {
	folder, err := filepath.Abs(o.projectRoot)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(folder, apexv1.AssessInput)); err != nil {
		return fmt.Errorf("%s runs in a batch folder the coordinator built, and %s has no %s", apexv1.AssessSkill, folder, apexv1.AssessInput)
	}
	if _, err := os.Lstat(filepath.Join(folder, ".git")); err == nil {
		return fmt.Errorf("%s runs in a batch folder, a snapshot with no .git, and %s has one", apexv1.AssessSkill, folder)
	}
	plugin, err := buildAssessPlugin(folder, o.assessFrom)
	if err != nil {
		return err
	}
	cfg.ignoreProjectSettings = true
	cfg.spawnSettings.noMCPBridge = true // V8h: no MCP server, ape's bridge included
	cfg.spawnSettings.args = append(cfg.spawnSettings.args, "--tools", strings.Join(apexv1.AssessTools, ","), "--plugin-dir", plugin)
	cfg.spawnSettings.settings = withCredsDeny(map[string]any{"permissions": map[string]any{"deny": append([]string(nil), assessDenyRules...)}})
	for i := range cfg.spawnSettings.hooks {
		if cfg.spawnSettings.hooks[i].Event == "PreToolUse" {
			cfg.spawnSettings.hooks[i].Matcher = ""
		}
	}
	// APE_TOOLCHAIN=local: the hooks run this ape, never a pin the
	// snapshot's .bingo/ names, which may predate the assessor's guard.
	cfg.spawnEnv = append(cfg.spawnEnv, apexv1.EnvAssess+"="+folder, envToolchain+"=local")
	o.typedSkill = apexv1.AssessPlugin + ":" + apexv1.AssessSkill
	cfg.pluginSkills = func(name string) error {
		if name != o.typedSkill {
			return fmt.Errorf("the assessor's session has no plugin skill %s", name)
		}
		return nil
	}
	return nil
}

// assessSkillKeys are the frontmatter keys an assessor skill may carry
// (P4-1): a skill's own hooks: run as the host's person outside the
// roster and the guard (measured on 2.1.316: a --plugin-dir skill's
// frontmatter hooks fire under --setting-sources user). Not model:
// models.apex-assess alone decides the model (D762+).
var assessSkillKeys = map[string]bool{"name": true, "description": true, "argument-hint": true, "disable-model-invocation": true}

// buildAssessPlugin writes the plugin ape-assess beside folder, its one
// file the SKILL.md of apex-assess as checkout has it installed (P4-1:
// never main's copy, which any merged change can alter), refused when its
// frontmatter has a key outside assessSkillKeys or it holds a `!` before
// a backtick (a shell command run when the skill expands).
func buildAssessPlugin(folder, checkout string) (string, error) {
	if checkout == "" {
		return "", fmt.Errorf("%s needs --assess-skill-from <checkout>: the skill runs as the coordinator's checkout installed it, never as the batch's snapshot of main has it", apexv1.AssessSkill)
	}
	src := filepath.Join(checkout, ".claude", "skills", apexv1.AssessSkill, "SKILL.md")
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s has no regular file .claude/skills/%s/SKILL.md: install the framework's %s there", checkout, apexv1.AssessSkill, apexv1.AssessSkill)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := checkAssessSkill(src, data); err != nil {
		return "", err
	}
	plugin := filepath.Clean(folder) + assessPluginSuffix
	if err := os.RemoveAll(plugin); err != nil {
		return "", err
	}
	skillDir := filepath.Join(plugin, "skills", apexv1.AssessSkill)
	for _, d := range []string{filepath.Join(plugin, ".claude-plugin"), skillDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	manifest := `{"name":"` + apexv1.AssessPlugin + `","version":"0.0.0","description":"ape's assessor skill, as the coordinator's checkout installed it"}` + "\n"
	if err := os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		return "", err
	}
	return plugin, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), data, 0o600)
}

// checkAssessSkill refuses an assessor SKILL.md that could run anything
// (P4-1).
func checkAssessSkill(file string, data []byte) error {
	fm, _, err := frontmatter.Split(data)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	var keys map[string]any
	if err := yaml.Unmarshal(fm, &keys); err != nil {
		return fmt.Errorf("%s: its frontmatter does not parse: %w", file, err)
	}
	for k := range keys {
		if !assessSkillKeys[k] {
			return fmt.Errorf("%s: frontmatter key %q is not one an assessor skill may carry (name, description, argument-hint, disable-model-invocation; the model is models.apex-assess's): a skill's hooks run outside the session's confinement", file, k)
		}
	}
	if bytes.Contains(data, []byte("!`")) {
		return fmt.Errorf("%s: it holds `!` before a backtick, a shell command Claude Code runs when the skill expands", file)
	}
	return nil
}

// assessSkillFromFlag is ape task's hidden --assess-skill-from: the
// checkout whose installed apex-assess the assessor runs (P4-1).
const assessSkillFromFlag = "assess-skill-from"

func addAssessSkillFromFlag(cmd *cobra.Command) {
	cmd.Flags().String(assessSkillFromFlag, "", "apex-assess only: the checkout whose installed .claude/skills/apex-assess/SKILL.md the assessor runs (the coordinator's; P4-1)")
	_ = cmd.Flags().MarkHidden(assessSkillFromFlag)
}

func assessSkillFrom(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString(assessSkillFromFlag)
	return v
}
