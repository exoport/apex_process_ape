package apecmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The aboard skill in this repository (.claude/skills/aboard) is aboard's
// own skill at the version go.mod pins, with its commands as ape hosts
// them: `aboard <verb>` is `ape aboard <verb>`. Until D307's audit it was
// a hand-made copy of aboard v0.2.0 on a v0.4.0 pin — no `shot`, no
// `quiet`, a `make shot` this repository has no target for — and nothing
// said so but `ape aboard status`'s stale-stamp warning (F6, F7; D315).
//
// The two generated references stay exactly as aboard writes them
// (`ape aboard capabilities --format md`, `ape aboard recipes index` print
// the bare `aboard` they were generated with), so `ape aboard capabilities
// --check` passes on them, as SKILL.md says it does.
//
// APE_UPDATE_ABOARD_SKILL=1 rewrites the repository's copy from the module
// cache: the way to re-derive it after an aboard bump.
func TestContract_AboardSkillIsThePinnedModulesSkill(t *testing.T) {
	pinned := pinnedAboardVersion(t)
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	require.NoError(t, err)
	src := filepath.Join(strings.TrimSpace(string(out)), "github.com", "exoport", "aboard@"+pinned, ".claude", "skills", "aboard")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("aboard %s's skill is not in the module cache (%v) — run `go mod download`", pinned, err)
	}
	dst := filepath.Join("..", "..", ".claude", "skills", "aboard")
	want := map[string]string{}
	require.NoError(t, filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		b, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		want[filepath.ToSlash(rel)] = apeAboardSkill(filepath.ToSlash(rel), string(b))
		return nil
	}))
	require.NotEmpty(t, want)
	if os.Getenv("APE_UPDATE_ABOARD_SKILL") == "1" {
		require.NoError(t, os.RemoveAll(dst))
		for rel, body := range want {
			p := filepath.Join(dst, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		}
		t.Logf("rewrote %s from aboard %s's skill (%d files)", dst, pinned, len(want))
		return
	}
	got := map[string]bool{}
	require.NoError(t, filepath.WalkDir(dst, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dst, path)
		got[filepath.ToSlash(rel)] = true
		return nil
	}))
	for rel, body := range want {
		b, rerr := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		require.NoError(t, rerr, "%s is missing from .claude/skills/aboard: re-derive with APE_UPDATE_ABOARD_SKILL=1", rel)
		require.Equal(t, body, string(b), "%s differs from aboard %s's skill as ape hosts it: re-derive with APE_UPDATE_ABOARD_SKILL=1 go test ./internal/apecmd -run TestContract_AboardSkillIsThePinnedModulesSkill", rel, pinned)
		delete(got, rel)
	}
	require.Empty(t, got, "files in .claude/skills/aboard that aboard %s's skill does not have", pinned)
}

// aboardSkillGenerated are the references aboard generates, kept as written.
var aboardSkillGenerated = map[string]bool{"references/reference.generated.md": true, "references/recipes.md": true}

// aboardSkillCmdRe is aboard's command word before one of its verbs, a line
// break between them included (SKILL.md wraps "aboard\ncapabilities ui").
var aboardSkillCmdRe = regexp.MustCompile(`\baboard(\s+)(apply|boards|capabilities|export|history|init|journal|log|poke|recipes|rendered|requests|serve|shot|status|uploads|version|wait|watch)\b`)

// aboardSkillApeLines are the lines that name aboard's own checkout's make
// targets as commands to run: this repository has none of them.
var aboardSkillApeLines = map[string][2]string{
	"references/capabilities.md": {
		"make caps | e2e | shot | build | test | lint | status\n",
		"# make caps | e2e | shot | build | test | lint | status: targets of aboard's own checkout, not of a project that hosts it\n",
	},
}

// apeAboardSkill is aboard's skill file rel as ape hosts it.
func apeAboardSkill(rel, s string) string {
	if aboardSkillGenerated[rel] {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range aboardSkillCmdRe.FindAllStringIndex(s, -1) {
		if m[0] > 0 && strings.ContainsRune("/.-_", rune(s[m[0]-1])) || strings.HasSuffix(s[:m[0]], "ape ") {
			continue
		}
		b.WriteString(s[last:m[0]] + "ape ")
		last = m[0]
	}
	b.WriteString(s[last:])
	out := b.String()
	if r, ok := aboardSkillApeLines[rel]; ok {
		out = strings.Replace(out, r[0], r[1], 1)
	}
	return out
}
