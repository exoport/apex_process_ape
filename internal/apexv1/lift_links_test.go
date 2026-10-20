package apexv1

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// RG-5, tenax on rc.7e: docs/explanation/multiplicative-concurrency-model.md:86
// linked `../../development/governance/adrs/adr-0062_….md`, and the lift
// wrote `../.development/decisions/0063-….md`: the path match began at
// the second `./` and the root-relative path took its place. The leading
// ./ and ../ chain is the link's own and is kept.
func TestADRRewriter_KeepsTheRelativePrefix(t *testing.T) {
	const slug = "cross-partition-key-guarantee-multiplicative-concurrency-model"
	rw := newADRRewriter(map[int]int{62: 63}, map[string]string{
		"development/governance/adrs/adr-0062_" + slug + ".md": "development/decisions/0063-" + slug + ".md",
	}, "development/governance/adrs")
	for _, c := range []struct{ name, in, want string }{
		{
			"tenax's line, verbatim",
			"[ADR-0062](../../development/governance/adrs/adr-0062_" + slug + ".md)",
			"[ADR-0063](../../development/decisions/0063-" + slug + ".md)",
		},
		{"../", "[x](../development/governance/adrs/adr-0062_" + slug + ".md)", "[x](../development/decisions/0063-" + slug + ".md)"},
		{"./", "[x](./development/governance/adrs/adr-0062_" + slug + ".md)", "[x](./development/decisions/0063-" + slug + ".md)"},
		{"./../", "[x](./../development/governance/adrs/adr-0062_" + slug + ".md#why)", "[x](./../development/decisions/0063-" + slug + ".md#why)"},
		{"bare, in prose", "see development/governance/adrs/adr-0062_" + slug + ".md.", "see development/decisions/0063-" + slug + ".md."},
		{"a record the lift did not map", "[x](../../development/governance/adrs/adr-0099_gone.md)", "[x](../../development/governance/adrs/adr-0099_gone.md)"},
	} {
		out, _, _ := rw.rewrite(c.in)
		require.Equal(t, c.want, out, c.name)
	}
}

// RG-5's in-record case: a record moves from governance/adrs/ to
// decisions/, one folder less deep; its own relative links are re-based
// so each still names the same file, a sibling record by its new name
// (tenax: `[ADR-0007](adr-0007_short-deadline-retry-on-durable-calls.md)`,
// `[…](../../planning/sprint-change-proposal-….md)`).
func TestRebaseLinks(t *testing.T) {
	paths := map[string]string{
		"development/governance/adrs/adr-0007_short.md":  "development/decisions/0008-short.md",
		"development/governance/adrs/adr-0031_stress.md": "development/decisions/0032-stress.md",
	}
	exists := func(rel string) bool {
		return rel == "development/planning/scp.md" || rel == "development/architecture.md" || rel == "docs/guide.md" || rel == "development/governance/adrs/README.md"
	}
	in := "per [ADR-0007](adr-0007_short.md), and [ADR-0031](./adr-0031_stress.md#decisions)\n" +
		"[SCP](../../planning/scp.md) · [arch](../../architecture.md) · [guide](../../../docs/guide.md) · [index](README.md)\n" +
		"[web](https://example.com/x.md) · [top](#context) · [gone](../../nowhere.md) · [abs](/etc/x.md) · [out](../../../../x.md)\n" +
		"[ref]: ../../planning/scp.md\n" +
		"```\n[code](../../architecture.md)\n```\n"
	want := "per [ADR-0007](0008-short.md), and [ADR-0031](./0032-stress.md#decisions)\n" +
		"[SCP](../planning/scp.md) · [arch](../architecture.md) · [guide](../../docs/guide.md) · [index](../governance/adrs/README.md)\n" +
		"[web](https://example.com/x.md) · [top](#context) · [gone](../../nowhere.md) · [abs](/etc/x.md) · [out](../../../../x.md)\n" +
		"[ref]: ../planning/scp.md\n" +
		"```\n[code](../../architecture.md)\n```\n"
	require.Equal(t, want, rebaseLinks(in, "development/governance/adrs/adr-0031_stress.md", "development/decisions/0032-stress.md", paths, exists))
}

// RG-5 end to end: a docs page two levels down linking an ADR, and a
// record linking a sibling and a file two levels up, lifted — every link
// resolves to a file that exists after the lift.
func TestLift_ADRLinksResolveAfterTheLift(t *testing.T) {
	root := liftFixture(t, false)
	write := func(rel, s string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(s), 0o644))
	}
	write("docs/explanation/limits.md", "See [ADR-0006](../../development/governance/adrs/adr-0006_limit-64.md) and [the old one](./../../development/governance/adrs/adr-0004_limit-names.md).\n")
	write("development/architecture.md", "# Architecture\n")
	adr7, err := os.ReadFile(filepath.Join(root, "development/governance/adrs/adr-0007_serve-https.md"))
	require.NoError(t, err)
	write("development/governance/adrs/adr-0007_serve-https.md", string(adr7)+"\nSee [ADR-0006](adr-0006_limit-64.md) and [the architecture](../../architecture.md).\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "links")
	run(t, root, "tag", "-a", LiftTag, "-m", "final")
	run(t, root, "push", "-q", "origin", LiftTag)

	_, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	page := read(t, &Project{Root: root}, "docs/explanation/limits.md")
	require.Equal(t, "See [ADR-0004](../../development/decisions/0004-limit-64.md) and [the old one](./../../development/decisions/0003-limit-names.md).\n", page)
	rec := read(t, &Project{Root: root}, "development/decisions/0005-serve-https.md")
	require.Contains(t, rec, "See [ADR-0004](0004-limit-64.md) and [the architecture](../architecture.md).\n")
	linkRe := regexp.MustCompile(`\]\(([^)#]+)`)
	for _, f := range []struct{ rel, doc string }{{"docs/explanation/limits.md", page}, {"development/decisions/0005-serve-https.md", rec}} {
		for _, m := range linkRe.FindAllStringSubmatch(f.doc, -1) {
			require.FileExists(t, filepath.Join(root, filepath.FromSlash(path.Join(path.Dir(f.rel), m[1]))), f.rel+": "+m[1])
		}
	}
}
