package apecmd

import (
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Which bare words `--model` accepts is DATA — `internal/cost/prices.yaml`'s
// `aliases:` block — but six sentences in this package described that set in
// prose, and all six had drifted to "(sonnet, opus, haiku)" while `fable` and
// `mythos` resolved perfectly well. The cost is not cosmetic: a caller reading
// any of them is told a word ape WILL honour is not a family, and the
// framework's orchestrator reference correctly lists five, so a persona
// following it would write a model ape's own help denies.
//
// The fix derives all six from the alias table. This test is what stops the
// next person restating it by hand.
func TestModelFlag_NamesEveryFamilyTheAliasTableAccepts(t *testing.T) {
	t.Parallel()

	aliases := cost.FamilyAliases()
	require.NotEmpty(t, aliases, "the alias table must load, or this test proves nothing")

	for name, newCmd := range map[string]func() *cobra.Command{
		"task":   newTaskCmd,
		"prompt": newPromptCmd,
		"chat":   newChatCmd,
	} {
		flag := newCmd().Flags().Lookup("model")
		require.NotNil(t, flag, "`ape %s` must offer --model", name)
		for family := range aliases {
			require.Contains(t, flag.Usage, family,
				"`ape %s --model` help omits the family %q, which the alias table accepts", name, family)
		}
	}
}

// The unrecognized-model warning names the same set: it is the text a caller
// sees at the moment they got the word wrong, so a short list there sends
// them to a smaller menu than ape actually has.
func TestModelFamilyWords_IsTheWholeAliasTable(t *testing.T) {
	t.Parallel()

	words := modelFamilyWords()
	aliases := cost.FamilyAliases()
	require.NotEmpty(t, aliases)
	for family := range aliases {
		require.Contains(t, words, family, "modelFamilyWords() omits %q", family)
	}
	require.Equal(t, len(aliases), strings.Count(words, ",")+1,
		"the rendered list must name each family exactly once: %q", words)

	// The guard is only worth having if it rejects what shipped. This is
	// the exact phrase all six sentences carried before the fix.
	const stale = "sonnet, opus, haiku"
	var missedByStale []string
	for family := range aliases {
		if !strings.Contains(stale, family) {
			missedByStale = append(missedByStale, family)
		}
	}
	require.NotEmpty(t, missedByStale,
		"the pre-fix phrase must omit at least one real family, or this test proves nothing")
}
