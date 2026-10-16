package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// Plugins handled by ape (backlog 36): see internal/apexv1/plugins.go.

// pluginCLIOrNil is the `claude plugin` CLI, nil when claude is not on
// PATH (project check then judges settings.json only).
func pluginCLIOrNil(bin string) apexv1.PluginCLI {
	if bin == "" {
		bin = "claude"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil
	}
	return apexv1.ClaudePluginCLI{Bin: bin}
}

// v1TaskPlugins is `ape task`'s plugin preflight in a v1 project: every
// plugin the config lists installed at its ref (settings.json untouched),
// and the --settings fragment that enables them for the session. A plugin
// still missing afterwards refuses the dispatch with the command to run.
func v1TaskPlugins(ctx context.Context, p *apexv1.Project, claudeBin string) (map[string]any, error) {
	cfg, err := apexv1.LoadConfig(p.Root)
	if err != nil {
		return nil, err
	}
	settings := apexv1.PluginSettings(cfg, p.Root)
	if settings == nil {
		return nil, nil //nolint:nilnil // no plugins configured: nothing to pass, and nothing wrong
	}
	if _, err := p.SyncPlugins(ctx, apexv1.ClaudePluginCLI{Bin: claudeBin}, apexv1.SyncOptions{}); err != nil {
		if fe, ok := errors.AsType[*apexv1.FailError](err); ok {
			return nil, fmt.Errorf("%s: %s", fe.Code, fe.Msg)
		}
		return nil, err
	}
	return settings, nil
}

// syncPluginsReport runs the full plugin sync — settings.json written,
// plugins installed, the settings change committed when commit is set —
// and prints what it did. A project with no plugin config prints nothing.
func syncPluginsReport(ctx context.Context, w io.Writer, p *apexv1.Project, commit bool) (*apexv1.PluginSync, error) {
	res, err := p.SyncPlugins(ctx, apexv1.ClaudePluginCLI{}, apexv1.SyncOptions{WriteSettings: true, Commit: commit})
	if res != nil {
		for _, a := range res.Actions {
			fmt.Fprintln(w, "plugin   "+a)
		}
		if len(res.Settings) > 0 {
			fmt.Fprintln(w, "plugin   settings.json: "+strings.Join(res.Settings, ", "))
		}
	}
	return res, err
}

// checkPluginsProject is doctor's plugins.project row.
func checkPluginsProject(ctx context.Context, env doctorEnv) CheckResult {
	res := CheckResult{Name: "plugins.project"}
	p, ok := v1ProjectAt(env.ProjectRoot)
	if !ok {
		res.Status, res.Message = StatusSkip, "not an APEX v1 project"
		return res
	}
	cfg, err := apexv1.LoadConfig(p.Root)
	if err != nil || apexv1.PluginSettings(cfg, p.Root) == nil {
		res.Status, res.Message = StatusSkip, "_apex/config.yaml lists no plugins"
		return res
	}
	cli := pluginCLIOrNil("")
	fs := p.CheckPlugins(ctx, cli)
	if len(fs) == 0 {
		res.Status, res.Message = StatusOK, fmt.Sprintf("%d plugin(s) installed at their refs, settings.json in step", len(cfg.Plugins))
		if cli == nil {
			res.Status, res.Message = StatusWarn, "settings.json in step; claude is not on PATH, so installs were not checked"
		}
		return res
	}
	msgs := make([]string, 0, len(fs))
	for _, f := range fs {
		msgs = append(msgs, f.Message)
	}
	res.Status, res.Message = StatusFail, strings.Join(msgs, "; ")
	res.Remediation = "ape doctor --fix installs and updates the plugins _apex/config.yaml lists and writes their settings"
	res.FixCommand = "ape doctor --fix --cwd " + p.Root
	return res
}
