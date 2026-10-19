package apecmd

import (
	"bytes"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// Q87: verify's human output says it skipped the regression command for a
// docs-only change, or why a change otherwise docs-only ran it.
func TestPrintDocsOnly(t *testing.T) {
	var b bytes.Buffer
	printDocsOnly(&b, &apexv1.RegressionVerdict{Cmd: "make test", Skipped: apexv1.RegressionSkippedDocsOnly, Paths: []string{"README.md", "docs/a.md"}, PathsTotal: 2})
	require.Equal(t, "  regression skipped (docs-only): the change touched only docs and the development folder — README.md, docs/a.md  make test\n", b.String())

	b.Reset()
	printDocsOnly(&b, &apexv1.RegressionVerdict{Cmd: "make test", Skipped: apexv1.RegressionSkippedDocsOnly})
	require.Contains(t, b.String(), "— only the development folder  make test")

	b.Reset()
	printDocsOnly(&b, &apexv1.RegressionVerdict{Cmd: "test -f README.md", NotSkipped: "the regression command names README.md"})
	require.Equal(t, "  regression ran, not skipped as docs-only: the regression command names README.md\n", b.String())

	b.Reset()
	printDocsOnly(&b, &apexv1.RegressionVerdict{Cmd: "make test"})
	printDocsOnly(&b, nil)
	require.Empty(t, b.String())
}
