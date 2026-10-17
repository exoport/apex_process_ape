package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// D77: one template grammar for tools.* and human_review.
func TestValidateArgsTemplate(t *testing.T) {
	for _, ok := range []string{"", "--task {task} --change {id}", "{change} {assets}", "--mode strict {id}"} {
		require.NoError(t, ValidateArgsTemplate(ok), ok)
	}
	for tmpl, want := range map[string]string{
		"--task {tsk}":  "holds a placeholder other than {id}, {task}, {assets}, {change}",
		"{id":           "holds a placeholder other than",
		"--x }":         "holds a placeholder other than",
		"{ID}":          "holds a placeholder other than",
		"a\nb":          "must be one line",
		"{id} {branch}": "holds a placeholder other than",
	} {
		require.ErrorContains(t, ValidateArgsTemplate(tmpl), want, tmpl)
	}
}

func TestFillArgs(t *testing.T) {
	v := ArgsVarsFor("add-wave", "T2", "development/changes/add-wave")
	require.Equal(t, "development/changes/add-wave/assets", v.Assets)
	require.Equal(t, "development/changes/add-wave/change.md", v.Change)

	got, err := FillArgs("--task {task} --change {id}", v)
	require.NoError(t, err)
	require.Equal(t, "--task T2 --change add-wave", got)
	got, err = FillArgs("  {change}   --out {assets}/accept-{task}.md ", v)
	require.NoError(t, err)
	require.Equal(t, "development/changes/add-wave/change.md --out development/changes/add-wave/assets/accept-T2.md", got)

	// {task} may be empty only when the template does not use it.
	v.Task = ""
	got, err = FillArgs("--change {id}", v)
	require.NoError(t, err)
	require.Equal(t, "--change add-wave", got)
	_, err = FillArgs("--task {task}", v)
	require.ErrorContains(t, err, "uses {task}, and there is no value for it")

	// A value is one shell-safe token.
	for _, bad := range []string{"has space", `q"uote`, "x;rm", "a$b", "back`tick", "it's"} {
		_, err = FillArgs("{change}", ArgsVars{ID: "x", Change: bad})
		require.ErrorContains(t, err, "is not one shell-safe token", bad)
	}
	// An unused unsafe value does not matter.
	got, err = FillArgs("{id}", ArgsVars{ID: "x", Change: "has space"})
	require.NoError(t, err)
	require.Equal(t, "x", got)

	_, err = FillArgs("{nope}", v)
	require.ErrorContains(t, err, "holds a placeholder other than")
}
