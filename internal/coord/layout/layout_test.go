package layout

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite the generated tables in docs/reference/coordination.md")

const page = "../../../docs/reference/coordination.md"

// The page and the layout agree: each generated table on the page is
// exactly what the Go table renders. `go test ./internal/coord/layout
// -update` rewrites them.
func TestPageMatchesLayout(t *testing.T) {
	data, err := os.ReadFile(filepath.FromSlash(page))
	require.NoError(t, err)
	doc := string(data)
	for name, table := range map[string]string{"subjects": SubjectsTable(), "stores": StoresTable()} {
		begin, end := "<!-- BEGIN generated:"+name+" -->\n", "<!-- END generated:"+name+" -->"
		i, j := strings.Index(doc, begin), strings.Index(doc, end)
		require.True(t, i >= 0 && j > i, "the page has no %s markers", name)
		got := doc[i+len(begin) : j]
		if *update {
			doc = doc[:i+len(begin)] + table + doc[j:]
			continue
		}
		require.Equal(t, table, got, "the page's %s table differs from internal/coord/layout: run go test ./internal/coord/layout -update", name)
	}
	if *update {
		require.NoError(t, os.WriteFile(filepath.FromSlash(page), []byte(doc), 0o644))
	}
}

func TestKeysAndTokens(t *testing.T) {
	require.Equal(t, "milestone.taskforge.M2", MilestoneKey("taskforge", "M2"))
	require.Equal(t, "conductor.diego.atomix", ConductorKey("diego", "atomix"))
	require.Equal(t, "slot.mmq4.port-8080.0", SlotKey("mmq4", "port-8080", 0))
	require.Equal(t, "taskforge/c0ffee/42", MessageID("taskforge", "c0ffee", 42))
	require.Equal(t, "3.17", Token{3, 17}.String())
	require.True(t, Token{4, 1}.Newer(Token{3, 900}), "a newer epoch wins whatever the revision")
	require.True(t, Token{3, 18}.Newer(Token{3, 17}))
	require.False(t, Token{3, 17}.Newer(Token{3, 17}))
	// Every lease and slot key names a store that holds it.
	for _, k := range []string{"steward.<ens>", "coord.<ens>", "sup.leader", "milestone.<project>.<M>", "conductor.<user>.<ens>"} {
		require.Contains(t, Stores[1].Keys, k)
	}
}

func TestReservedTokens(t *testing.T) {
	for _, tok := range []string{"coord", "sup", "vm-12", "vm-"} {
		require.True(t, Reserved(tok), tok)
	}
	for _, tok := range []string{"diego", "coordinator", "supper", "v-m"} {
		require.False(t, Reserved(tok), tok)
	}
	require.Equal(t, "conductor-diego-atomix", ConductorConsumer("diego", "atomix"))
}
