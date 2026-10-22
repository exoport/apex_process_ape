package apexv1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/usage"
)

// W4: limits.usage parses with its defaults, and a value ape cannot use
// fails the parse (as every limit does).
func TestLimitsUsage(t *testing.T) {
	var l Limits
	require.NoError(t, yaml.Unmarshal([]byte("budget: off\n"), &l))
	require.Equal(t, usage.DefaultGuard(), l.UsageGuard())

	require.NoError(t, yaml.Unmarshal([]byte("usage: {seven_day: 0.8, five_hour: 0.9, ceiling: 0.9, share: 0.1, max_age: 15m, off: true}\n"), &l))
	g := l.UsageGuard()
	require.InDelta(t, 0.8, g.SevenDay, 1e-9)
	require.InDelta(t, 0.9, g.FiveHour, 1e-9)
	require.InDelta(t, 0.9, g.Ceiling, 1e-9)
	require.NotNil(t, g.Share)
	require.InDelta(t, 0.1, *g.Share, 1e-9)
	require.Equal(t, 15*time.Minute, g.MaxAge)
	require.True(t, g.Off)

	require.NoError(t, yaml.Unmarshal([]byte("usage:\n  share: auto\n  off: false\n"), &l))
	require.Nil(t, l.UsageGuard().Share)
	require.False(t, l.UsageGuard().Off)
	out, err := yaml.Marshal(l.Usage)
	require.NoError(t, err)
	require.Contains(t, string(out), "share: auto")

	for bad, msg := range map[string]string{
		"usage: {seven_day: 1.5}\n":   "limits.usage.seven_day: 1.5 is not a fraction",
		"usage: {five_hour: 0}\n":     "limits.usage.five_hour: 0 is not a fraction",
		"usage: {ceiling: -1}\n":      "limits.usage.ceiling: -1 is not a fraction",
		"usage: {share: most}\n":      `limits.usage.share: "most" is not auto or a fraction`,
		"usage: {share: 2}\n":         "limits.usage.share: 2 is not auto or a fraction",
		"usage: {max_age: soon}\n":    `limits.usage.max_age: "soon" is not a duration`,
		"usage: {max_age: \"-1m\"}\n": `limits.usage.max_age: "-1m" is not a duration`,
	} {
		var l Limits
		err := yaml.Unmarshal([]byte(bad), &l)
		require.ErrorContains(t, err, msg, bad)
	}
}

// project check reports a key of limits.usage ape does not know.
func TestProjectCheck_UsageUnknownKey(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: x\nlimits:\n  usage:\n    seven_days: 0.8\n"
	p := fixture(t, files)
	var got []Finding
	for _, f := range ClassifyFindings(p.Check()) {
		if f.Code == CodeConfigUnknownKey {
			got = append(got, f)
		}
	}
	require.Len(t, got, 1, "%v", got)
	require.Contains(t, got[0].Text(), "limits.usage.seven_days is not a key of limits: usage, so it limits nothing — the keys are seven_day, five_hour, ceiling, share, max_age, off")
}
