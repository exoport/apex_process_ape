package apecmd

import (
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// D188: --defect goes only with --verdict fail.
func TestChangeAccept_DefectOnlyWithAFail(t *testing.T) {
	root := v1Repo(t)
	for _, args := range [][]string{
		{"T2", "--verdict", "pass", "--reason", "fine", "--defect", "docs/guide.md"},
		{"T2", "--by", "diego", "--defect", "docs/guide.md"},
		{"T2", "--delegate", "--defect", "docs/guide.md"},
	} {
		stdout, _, err := v1Exec(t, newChangeCmd, append([]string{"accept", "add-wave"}, append(args, "--cwd", root, "--json")...)...)
		require.Error(t, err, "%v", args)
		require.Contains(t, stdout, `"code": "usage.delegate"`, "%v", args)
	}
}

// D188: why a delegate's fail stays a person's, in delegate.fail's
// message.
func TestDelegateToPerson(t *testing.T) {
	ret := apexv1.DelegateReturn{Run: "run-1", Reason: "the guide shows <TAB>"}
	for _, c := range []struct {
		o    apexv1.DelegateOutcome
		want string
	}{
		{apexv1.DelegateOutcome{ToPerson: apexv1.ToPersonSecondFail, Returns: []apexv1.DelegateReturn{ret}}, "its second fail: the delegate returned it to the change once already (run run-1: the guide shows <TAB>)"},
		{apexv1.DelegateOutcome{ToPerson: apexv1.ToPersonArchived}, "the change is archived"},
		{apexv1.DelegateOutcome{ToPerson: apexv1.ToPersonNoDefect, DefectsOutside: []string{"spec.md"}}, "not in the change's own diff (spec.md)"},
		{apexv1.DelegateOutcome{ToPerson: apexv1.ToPersonNoDefect, Reason: "wrong"}, "names no defect in the change's own diff (--defect)"},
		{apexv1.DelegateOutcome{ToPerson: apexv1.ToPersonNoDefect, Reason: apexv1.NoVerdictReason}, ""},
	} {
		got := delegateToPerson(&c.o)
		if c.want == "" {
			require.Empty(t, got)
			continue
		}
		require.Contains(t, got, c.want)
	}
	require.Contains(t, delegateFixOption(&apexv1.DelegateOutcome{ToPerson: apexv1.ToPersonArchived}), "fix change")
	require.Empty(t, delegateFixOption(&apexv1.DelegateOutcome{ToPerson: apexv1.ToPersonSecondFail}))
}
