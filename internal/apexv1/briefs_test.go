package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const briefText = "Build a greeter that says hello.\n"

func TestDeliveryLinks(t *testing.T) {
	cases := []struct {
		name, line string
		want       []string
	}{
		{"markdown, roadmap-relative", `Delivery: Phase 1 — "greet" ([brief](briefs/2026-10-16-greet.md))`, []string{"briefs/2026-10-16-greet.md"}},
		{"markdown, root-relative", `Delivery: [x](development/briefs/2026-10-16-greet.md)`, []string{"development/briefs/2026-10-16-greet.md"}},
		{"markdown, dot-slash", `Delivery: [x](./briefs/2026-10-16-greet.md)`, []string{"./briefs/2026-10-16-greet.md"}},
		{"markdown, angle brackets, title and fragment", `Delivery: [x](<briefs/2026-10-16-greet.md#scope> "the brief")`, []string{"briefs/2026-10-16-greet.md"}},
		{"bare", `Delivery: Phase 1 — see briefs/2026-10-16-greet.md`, []string{"briefs/2026-10-16-greet.md"}},
		{"bare in backticks", "Delivery: Phase 1 (`development/briefs/2026-10-16-greet.md`)", []string{"development/briefs/2026-10-16-greet.md"}},
		{"bare dot-slash", `Delivery: ./briefs/2026-10-16-greet.md`, []string{"./briefs/2026-10-16-greet.md"}},
		{
			"several", `Delivery: [a](briefs/2026-10-01-a.md), briefs/2026-10-02-b.md; [c](briefs/2026-10-03-c.md)`,
			[]string{"briefs/2026-10-01-a.md", "briefs/2026-10-03-c.md", "briefs/2026-10-02-b.md"},
		},
		{"bold label", `**Delivery:** [x](briefs/2026-10-16-greet.md)`, []string{"briefs/2026-10-16-greet.md"}},
		{"free text only", `Delivery: Phase 1 — "a greeter"`, nil},
		{"other links ignored", `Delivery: [spec](specs/greeting/spec.md) [site](https://x.org/briefs/a.md)`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, links := deliveryLinks("# Roadmap\n\n" + tc.line + "\n\n## M1 — x\n")
			require.Equal(t, []int{3}, lines)
			var got []string
			for _, l := range links {
				require.Equal(t, 3, l.Line)
				got = append(got, l.Target)
			}
			require.Equal(t, tc.want, got)
		})
	}
	lines, links := deliveryLinks("# Roadmap\n```\nDelivery: briefs/2026-10-16-x.md\n```\n")
	require.Empty(t, lines, "a Delivery: line in a code block is not one")
	require.Empty(t, links)
}

func TestValidBriefName(t *testing.T) {
	for name, ok := range map[string]bool{
		"2026-10-16-greet.md": true, "2026-10-16-a1-b2.md": true,
		"2026-13-01-x.md": false, "2026-02-30-x.md": false, "26-10-16-x.md": false,
		"2026-10-16-Greet.md": false, "2026-10-16-.md": false, "2026-10-16-x.txt": false, "brief.md": false,
	} {
		require.Equal(t, ok, validBriefName(name), name)
	}
}

func roadmapWithDelivery(line string) string {
	return "# Roadmap\n\n" + line + "\n" + roadmapApproved[len("# Roadmap\n"):]
}

func briefCodes(t *testing.T, p *Project) []string {
	t.Helper()
	findingMessages(p.Check()) // panics on an unclassified finding
	fs := ClassifyFindings(p.Check())
	codes := make([]string, 0, len(fs))
	for _, f := range fs {
		codes = append(codes, f.Code)
	}
	return codes
}

func TestProjectCheck_DeliveryLink(t *testing.T) {
	cases := []struct {
		name, delivery string
		briefs         []string
		want           []string
	}{
		{"no line, no briefs: an older project", "", nil, nil},
		{"free text, no briefs", `Delivery: Phase 1 — "greet"`, nil, nil},
		{"roadmap-relative resolves", `Delivery: Phase 1 — [brief](briefs/2026-10-16-greet.md)`, []string{"2026-10-16-greet.md"}, nil},
		{"root-relative resolves", `Delivery: [b](development/briefs/2026-10-16-greet.md)`, []string{"2026-10-16-greet.md"}, nil},
		{"dot-slash resolves", `Delivery: ./briefs/2026-10-16-greet.md`, []string{"2026-10-16-greet.md"}, nil},
		{"unresolved", `Delivery: [b](briefs/2026-10-17-gone.md)`, []string{"2026-10-16-greet.md"}, []string{"roadmap.delivery-link"}},
		{"unresolved, no briefs", `Delivery: briefs/2026-10-17-gone.md`, nil, []string{"roadmap.delivery-link"}},
		{
			"one of two unresolved", `Delivery: [a](briefs/2026-10-16-greet.md) [b](briefs/2026-10-17-gone.md)`,
			[]string{"2026-10-16-greet.md"},
			[]string{"roadmap.delivery-link"},
		},
		{"briefs but free text", `Delivery: Phase 1 — "greet"`, []string{"2026-10-16-greet.md"}, []string{"roadmap.delivery-unlinked"}},
		{"briefs but no line", "", []string{"2026-10-16-greet.md"}, []string{"roadmap.delivery-unlinked"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := baseFiles()
			if tc.delivery != "" {
				files["development/roadmap.md"] = roadmapWithDelivery(tc.delivery)
			}
			for _, b := range tc.briefs {
				files["development/briefs/"+b] = briefText
			}
			p := fixture(t, files)
			require.ElementsMatch(t, tc.want, briefCodes(t, p), findingMessages(p.Check()))
		})
	}

	t.Run("the fix names the newest brief, relative to roadmap.md", func(t *testing.T) {
		files := baseFiles()
		files["development/briefs/2026-10-01-first.md"] = briefText
		files["development/briefs/2026-10-16-second.md"] = briefText
		p := fixture(t, files)
		fs := ClassifyFindings(p.Check())
		require.Len(t, fs, 1)
		require.Equal(t, "development/roadmap.md", fs[0].File)
		require.Contains(t, fs[0].Fix, "[2026-10-16-second.md](briefs/2026-10-16-second.md)")
	})
}

func TestProjectCheck_BriefNames(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = roadmapWithDelivery("Delivery: [b](briefs/2026-10-16-greet.md)")
	files["development/briefs/2026-10-16-greet.md"] = briefText
	files["development/briefs/notes.md"] = briefText
	files["development/briefs/2026-02-30-bad-date.md"] = briefText
	files["development/briefs/.gitkeep"] = ""
	p := fixture(t, files)
	fs := ClassifyFindings(p.Check())
	bad := make([]string, 0, len(fs))
	for _, f := range fs {
		require.Equal(t, "brief.bad-name", f.Code, f.String())
		bad = append(bad, f.File)
	}
	require.Equal(t, []string{"development/briefs/2026-02-30-bad-date.md", "development/briefs/notes.md"}, bad)
}

func TestProjectCheck_BriefHistory(t *testing.T) {
	const rel = "development/briefs/2026-10-16-greet.md"
	linked := func() map[string]string {
		files := baseFiles()
		files["development/roadmap.md"] = roadmapWithDelivery("Delivery: [b](briefs/2026-10-16-greet.md)")
		files[rel] = briefText
		return files
	}
	t.Run("committed and unchanged is clean", func(t *testing.T) {
		require.Empty(t, briefCodes(t, fixture(t, linked())))
	})
	t.Run("an uncommitted edit", func(t *testing.T) {
		p := fixture(t, linked())
		write(t, p, rel, briefText+"And farewells.\n")
		fs := ClassifyFindings(p.Check())
		require.Len(t, fs, 1)
		require.Equal(t, "brief.edited", fs[0].Code)
		require.Equal(t, rel, fs[0].File)
		require.Contains(t, fs[0].Fix, "git show ")
		require.Contains(t, fs[0].Fix, ":./"+rel+" > "+rel)
		require.Contains(t, fs[0].Fix, "development/briefs/YYYY-MM-DD-<slug>.md")
	})
	t.Run("an edit in a later commit, and its restore", func(t *testing.T) {
		p := fixture(t, linked())
		added := run(t, p.Root, "rev-parse", "HEAD")
		write(t, p, rel, briefText+"And farewells.\n")
		commitAll(t, p, "edit the brief")
		require.Equal(t, []string{"brief.edited"}, briefCodes(t, p))

		// The fix the finding gives restores it; committing the restore is clean.
		require.NoError(t, os.WriteFile(filepath.Join(p.Root, rel), []byte(run(t, p.Root, "show", added[:12]+":./"+rel)+"\n"), 0o644))
		require.Empty(t, briefCodes(t, p))
		commitAll(t, p, "restore the brief")
		require.Empty(t, briefCodes(t, p))
	})
	t.Run("an uncommitted brief", func(t *testing.T) {
		p := fixture(t, linked())
		write(t, p, "development/briefs/2026-10-17-more.md", briefText)
		fs := ClassifyFindings(p.Check())
		require.Len(t, fs, 1, findingMessages(fs))
		require.Equal(t, "brief.uncommitted", fs[0].Code)
		require.Equal(t, "development/briefs/2026-10-17-more.md", fs[0].File)
	})
	t.Run("a renamed brief keeps its text", func(t *testing.T) {
		files := linked()
		files["development/briefs/notes.md"] = briefText
		p := fixture(t, files)
		run(t, p.Root, "mv", "development/briefs/notes.md", "development/briefs/2026-10-15-notes.md")
		commitAll(t, p, "rename")
		require.Empty(t, briefCodes(t, p))
	})
}

func TestPreTool_Briefs(t *testing.T) {
	const rel = "development/briefs/2026-10-16-greet.md"
	files := baseFiles()
	files[rel] = briefText
	p := fixture(t, files)
	for _, tc := range []struct {
		tool, path string
		deny       bool
	}{
		{"Edit", rel, true},
		{"Write", rel, true},
		{"MultiEdit", filepath.Join(p.Root, filepath.FromSlash(rel)), true},
		{"NotebookEdit", rel, true},
		{"Write", "development/briefs/2026-10-17-next.md", false},
		{"Write", "development/briefs/sub/2026-10-17-next.md", false},
		{"Write", "development/briefs", false},
		{"Edit", "development/project.md", false},
	} {
		d := PreTool(editPayload(t, p.Root, tc.tool, tc.path))
		require.Equal(t, tc.deny, d.Deny, "%s %s", tc.tool, tc.path)
		if tc.deny {
			require.Equal(t, rel, d.Path)
			require.Contains(t, d.Reason, rel+" is a brief")
			require.Contains(t, d.Reason, "a new or changed request is a new dated brief (development/briefs/YYYY-MM-DD-<slug>.md)")
			require.Contains(t, d.Reason, "Delivery:")
		}
	}

	t.Run("a symlinked parent is still briefs/", func(t *testing.T) {
		require.NoError(t, os.Symlink(p.BriefsDir(), filepath.Join(p.Root, "briefs-link")))
		require.True(t, PreTool(editPayload(t, p.Root, "Edit", "briefs-link/2026-10-16-greet.md")).Deny)
		require.False(t, PreTool(editPayload(t, p.Root, "Write", "briefs-link/2026-10-17-new.md")).Deny)
	})
	t.Run("a link to the brief file itself", func(t *testing.T) {
		require.NoError(t, os.Symlink(filepath.Join(p.Root, filepath.FromSlash(rel)), filepath.Join(p.Root, "brief-link.md")))
		require.True(t, PreTool(editPayload(t, p.Root, "Edit", "brief-link.md")).Deny)
	})
	t.Run("a symlinked briefs folder", func(t *testing.T) {
		q := fixture(t, baseFiles())
		elsewhere := filepath.Join(q.Root, "elsewhere")
		require.NoError(t, os.MkdirAll(elsewhere, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "2026-10-16-x.md"), []byte(briefText), 0o644))
		require.NoError(t, os.Symlink(elsewhere, q.BriefsDir()))
		require.True(t, PreTool(editPayload(t, q.Root, "Edit", "development/briefs/2026-10-16-x.md")).Deny)
	})
}

// W5 (Q-S1): a dated brief folder is one brief. The name rule is the
// folder's, brief.md is its text, every file in it is history, and
// Delivery: links its brief.md.
func TestProjectCheck_BriefFolders(t *testing.T) {
	const folder = "development/briefs/2026-10-21-greeter"
	withFolder := func(delivery string) map[string]string {
		files := baseFiles()
		files["development/roadmap.md"] = roadmapWithDelivery(delivery)
		files[folder+"/brief.md"] = briefText
		files[folder+"/source.txt"] = "spike: /x @ 0123456789ab\n"
		files[folder+"/assets/home.svg"] = "<svg/>\n"
		files[folder+"/assets/deep/notes.md"] = "notes\n"
		files[folder+"/.hidden"] = "x\n"
		return files
	}
	linked := func() map[string]string {
		return withFolder("Delivery: Phase 1 — [brief](briefs/2026-10-21-greeter/brief.md)")
	}

	t.Run("a dated folder with brief.md, linked, is clean", func(t *testing.T) {
		require.Empty(t, briefCodes(t, fixture(t, linked())))
	})
	t.Run("a bare path to its brief.md resolves too", func(t *testing.T) {
		require.Empty(t, briefCodes(t, fixture(t, withFolder("Delivery: briefs/2026-10-21-greeter/brief.md"))))
	})
	t.Run("it counts as one brief, and is the current one", func(t *testing.T) {
		files := withFolder(`Delivery: Phase 1 — "greet"`)
		files["development/briefs/2026-10-01-first.md"] = briefText
		p := fixture(t, files)
		fs := ClassifyFindings(p.Check())
		require.Len(t, fs, 1, findingMessages(fs))
		require.Equal(t, "roadmap.delivery-unlinked", fs[0].Code)
		require.Contains(t, fs[0].Text(), "development/briefs/ holds 2 —")
		require.Contains(t, fs[0].Text(), "[2026-10-21-greeter/brief.md](briefs/2026-10-21-greeter/brief.md)")
	})
	t.Run("a newer single-file brief is the current one over an older folder", func(t *testing.T) {
		files := withFolder(`Delivery: Phase 1 — "greet"`)
		files["development/briefs/2026-10-22-later.md"] = briefText
		fs := ClassifyFindings(fixture(t, files).Check())
		require.Len(t, fs, 1, findingMessages(fs))
		require.Contains(t, fs[0].Text(), "[2026-10-22-later.md](briefs/2026-10-22-later.md)")
	})
	t.Run("a link to a missing brief.md names the folder's", func(t *testing.T) {
		fs := ClassifyFindings(fixture(t, withFolder("Delivery: [b](briefs/2026-10-21-gone/brief.md)")).Check())
		require.Len(t, fs, 1, findingMessages(fs))
		require.Equal(t, "roadmap.delivery-link", fs[0].Code)
		require.Contains(t, fs[0].Fix, "[2026-10-21-greeter/brief.md](briefs/2026-10-21-greeter/brief.md)")
	})
	t.Run("a link to the folder is no brief link", func(t *testing.T) {
		require.Equal(t, []string{"roadmap.delivery-unlinked"},
			briefCodes(t, fixture(t, withFolder("Delivery: [b](briefs/2026-10-21-greeter/)"))))
	})
	t.Run("a bad folder name is one brief.bad-name, on the folder", func(t *testing.T) {
		for _, name := range []string{"2026-02-30-greeter", "2026-10-21-Greeter", "greeter"} {
			files := baseFiles()
			files["development/roadmap.md"] = roadmapWithDelivery("Delivery: [b](briefs/" + name + "/brief.md)")
			files["development/briefs/"+name+"/brief.md"] = briefText
			files["development/briefs/"+name+"/assets/home.svg"] = "<svg/>\n"
			fs := ClassifyFindings(fixture(t, files).Check())
			require.Len(t, fs, 1, "%s: %v", name, findingMessages(fs))
			require.Equal(t, "brief.bad-name", fs[0].Code)
			require.Equal(t, "development/briefs/"+name, fs[0].File)
			require.Contains(t, fs[0].Message, "brief folder name is not YYYY-MM-DD-<slug>")
		}
	})
	t.Run("a dated folder without brief.md is brief.bad-name", func(t *testing.T) {
		files := baseFiles()
		files["development/roadmap.md"] = roadmapWithDelivery("Delivery: [b](briefs/2026-10-16-greet.md)")
		files["development/briefs/2026-10-16-greet.md"] = briefText
		files[folder+"/assets/home.svg"] = "<svg/>\n"
		fs := ClassifyFindings(fixture(t, files).Check())
		require.Len(t, fs, 1, findingMessages(fs))
		require.Equal(t, "brief.bad-name", fs[0].Code)
		require.Equal(t, folder, fs[0].File)
		require.Contains(t, fs[0].Message, "brief folder holds no brief.md")
	})
	t.Run("an undated folder without brief.md keeps v1.0's rule for its files", func(t *testing.T) {
		files := linked()
		files["development/briefs/archive/2025-01-01-old.md"] = briefText
		files["development/briefs/archive/notes.md"] = briefText
		fs := ClassifyFindings(fixture(t, files).Check())
		require.Len(t, fs, 1, findingMessages(fs))
		require.Equal(t, "brief.bad-name", fs[0].Code)
		require.Equal(t, "development/briefs/archive/notes.md", fs[0].File)
	})
	t.Run("every file in it is history", func(t *testing.T) {
		p := fixture(t, linked())
		write(t, p, folder+"/assets/home.svg", "<svg>changed</svg>\n")
		fs := ClassifyFindings(p.Check())
		require.Len(t, fs, 1, findingMessages(fs))
		require.Equal(t, "brief.edited", fs[0].Code)
		require.Equal(t, folder+"/assets/home.svg", fs[0].File)
		commitAll(t, p, "edit an asset")
		require.Equal(t, []string{"brief.edited"}, briefCodes(t, p))
	})
	t.Run("an uncommitted file in it is brief.uncommitted", func(t *testing.T) {
		p := fixture(t, linked())
		write(t, p, folder+"/assets/later.svg", "<svg/>\n")
		fs := ClassifyFindings(p.Check())
		require.Len(t, fs, 1, findingMessages(fs))
		require.Equal(t, "brief.uncommitted", fs[0].Code)
		require.Equal(t, folder+"/assets/later.svg", fs[0].File)
	})
	t.Run("pre-tool denies an edit to any file in it, allows a new one", func(t *testing.T) {
		p := fixture(t, linked())
		for _, rel := range []string{folder + "/brief.md", folder + "/source.txt", folder + "/assets/deep/notes.md"} {
			d := PreTool(editPayload(t, p.Root, "Edit", rel))
			require.True(t, d.Deny, rel)
			require.Contains(t, d.Reason, rel+" is a brief")
		}
		require.False(t, PreTool(editPayload(t, p.Root, "Write", folder+"/assets/new.svg")).Deny)
		require.False(t, PreTool(editPayload(t, p.Root, "Write", "development/briefs/2026-10-22-next/brief.md")).Deny)
	})
}

func TestCurrentBrief(t *testing.T) {
	file := func(name string) brief {
		return brief{Text: "/b/" + name, Files: []string{"/b/" + name}}
	}
	folder := func(name string, withText bool) brief {
		b := brief{Text: "/b/" + name + "/brief.md", Folder: "/b/" + name, Files: []string{"/b/" + name + "/assets/x.svg"}}
		if withText {
			b.Files = append(b.Files, b.Text)
		}
		return b
	}
	require.Empty(t, currentBrief(nil))
	require.Equal(t, "/b/2026-10-21-x/brief.md", currentBrief([]brief{file("2026-10-01-a.md"), folder("2026-10-21-x", true)}))
	require.Equal(t, "/b/2026-10-22-b.md", currentBrief([]brief{file("2026-10-22-b.md"), folder("2026-10-21-x", true)}))
	require.Equal(t, "/b/2026-10-01-a.md", currentBrief([]brief{file("2026-10-01-a.md"), folder("2026-10-21-x", false)}),
		"a folder without its brief.md is not well named")
	require.Equal(t, "/b/notes/brief.md", currentBrief([]brief{file("x.md"), folder("notes", true)}), "none well named: the last")
}
