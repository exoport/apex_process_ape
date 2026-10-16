package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The precedence table of backlog 29: model flag > models.<skill> > parent
// session > claude's default; effort flag > models.<skill>.effort > parent
// session > top-level effort: > claude's default, with D22's exception.
func TestDecideLaunch_Precedence(t *testing.T) {
	full := SkillModel{Model: "claude-haiku-5-5", Effort: "low"}
	modelOnly := SkillModel{Model: "claude-haiku-5-5"}
	effortOnly := SkillModel{Effort: "medium"}
	for name, tc := range map[string]struct {
		flagModel, flagEffort string
		skill                 SkillModel
		top                   string
		pModel, pEffort       string
		want                  Launch
	}{
		"nothing: claude decides": {want: Launch{"", LaunchFromDefault, "", LaunchFromDefault}},
		"top-level effort only":   {top: "high", want: Launch{"", LaunchFromDefault, "high", LaunchFromConfigDefault}},
		"parent beats top-level": {
			top: "high", pModel: "sonnet", pEffort: "xhigh",
			want: Launch{"sonnet", LaunchFromParent, "xhigh", LaunchFromParent},
		},
		"parent model, no parent effort: top-level": {
			top: "high", pModel: "sonnet",
			want: Launch{"sonnet", LaunchFromParent, "high", LaunchFromConfigDefault},
		},
		"skill beats parent": {
			skill: full, top: "high", pModel: "sonnet", pEffort: "xhigh",
			want: Launch{"claude-haiku-5-5", LaunchFromConfig, "low", LaunchFromConfig},
		},
		"D22: skill model without effort skips the parent's effort": {
			skill: modelOnly, top: "high", pModel: "sonnet", pEffort: "xhigh",
			want: Launch{"claude-haiku-5-5", LaunchFromConfig, "high", LaunchFromConfigDefault},
		},
		"D22 with no top-level: claude's default": {
			skill: modelOnly, pEffort: "xhigh",
			want: Launch{"claude-haiku-5-5", LaunchFromConfig, "", LaunchFromDefault},
		},
		"skill effort without model: parent model, skill effort": {
			skill: effortOnly, pModel: "sonnet", pEffort: "xhigh",
			want: Launch{"sonnet", LaunchFromParent, "medium", LaunchFromConfig},
		},
		"flags beat everything": {
			flagModel: "opus", flagEffort: "max", skill: full, top: "high", pModel: "sonnet", pEffort: "xhigh",
			want: Launch{"opus", LaunchFromFlag, "max", LaunchFromFlag},
		},
		"flag model keeps the parent's effort": {
			flagModel: "opus", skill: modelOnly, pEffort: "xhigh",
			want: Launch{"opus", LaunchFromFlag, "xhigh", LaunchFromParent},
		},
		"flag effort only": {
			flagEffort: "low", skill: modelOnly, pModel: "sonnet",
			want: Launch{"claude-haiku-5-5", LaunchFromConfig, "low", LaunchFromFlag},
		},
	} {
		require.Equal(t, tc.want, DecideLaunch(tc.flagModel, tc.flagEffort, tc.skill, tc.top, tc.pModel, tc.pEffort), name)
	}
}

func TestConfig_TopLevelEffort(t *testing.T) {
	root := t.TempDir()
	writeConfigForTest(t, root, "effort: high\nmodels:\n  apex-review: sonnet\n")
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	require.Equal(t, "high", cfg.Effort)
}

func writeConfigForTest(t *testing.T, root, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath(root), []byte(body), 0o644))
}
