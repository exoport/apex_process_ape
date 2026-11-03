package apecmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
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
//     plugin ape-assess, built beside the folder from the snapshot's own
//     copy of the skill, and types /ape-assess:apex-assess;
//   - no project plugins: the snapshot's config would bring their hooks;
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
	plugin, err := buildAssessPlugin(folder)
	if err != nil {
		return err
	}
	cfg.ignoreProjectSettings = true
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

// buildAssessPlugin writes the plugin ape-assess beside folder, its one
// skill the snapshot's own .claude/skills/apex-assess: the framework main
// has installed.
func buildAssessPlugin(folder string) (string, error) {
	src := filepath.Join(folder, ".claude", "skills", apexv1.AssessSkill)
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return "", fmt.Errorf("the batch's snapshot of main has no .claude/skills/%s/SKILL.md: install the framework's %s on main", apexv1.AssessSkill, apexv1.AssessSkill)
	}
	plugin := filepath.Clean(folder) + assessPluginSuffix
	if err := os.RemoveAll(plugin); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0o700); err != nil {
		return "", err
	}
	manifest := `{"name":"` + apexv1.AssessPlugin + `","version":"0.0.0","description":"ape's assessor skill, from the batch's snapshot"}` + "\n"
	if err := os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		return "", err
	}
	if err := copyRegularTree(src, filepath.Join(plugin, "skills", apexv1.AssessSkill)); err != nil {
		return "", err
	}
	return plugin, nil
}

// copyRegularTree copies src's regular files and directories to dst
// (the snapshot holds no links: BuildAssessFolder dropped them), read
// through an os.Root so nothing outside src is.
func copyRegularTree(src, dst string) error {
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dst, filepath.FromSlash(rel))
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o700)
		case d.Type().IsRegular():
			data, err := root.ReadFile(rel)
			if err != nil {
				return err
			}
			return os.WriteFile(to, data, 0o600)
		default:
			return errors.New(filepath.Join(src, rel) + " is not a regular file")
		}
	})
}
