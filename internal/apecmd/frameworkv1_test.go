package apecmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// The APEX v1 half of `make check-framework` (D53). A v1 framework ships no
// command list — its _apex/ape-commands.yaml holds only min_ape_version —
// so the contract is what its skills and migration entries actually run:
// every `ape …` they invoke must resolve in the binary, and the floor must
// be declared. APEX_FRAMEWORK_V1 names the tree (a framework-v1/ source
// tree, a payload, or an installed project); APE_BIN optionally names a
// built binary to judge instead of this source tree's command set.
//
//	APEX_FRAMEWORK_V1=<tree> [APE_BIN=<ape>] go test ./internal/apecmd/ -run TestFrameworkV1 -v

// v1ApeCallRe is one `ape` invocation inside code: its command words, up to
// the first word that is not a command word (a flag, a placeholder, a quote).
var v1ApeCallRe = regexp.MustCompile(`(?:^|[\s;&|(]|\$\()ape((?:[ \t]+[a-z][a-z0-9-]*)+)`)

// v1CodeSegments are the parts of a Markdown file that are code: fenced
// blocks and inline code spans; for a migration entry, its command: and
// check: frontmatter values as well.
func v1CodeSegments(doc string) []string {
	var segs []string
	inFence := false
	var prose []string
	for line := range strings.SplitSeq(doc, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			segs = append(segs, line)
			continue
		}
		if v, ok := strings.CutPrefix(t, "command:"); ok {
			segs = append(segs, v)
		}
		if v, ok := strings.CutPrefix(t, "check:"); ok {
			segs = append(segs, v)
		}
		prose = append(prose, line)
	}
	for _, m := range regexp.MustCompile("`([^`\n]+)`").FindAllStringSubmatch(strings.Join(prose, "\n"), -1) {
		segs = append(segs, m[1])
	}
	return segs
}

// v1ApeCalls maps each invoked command (its words joined by a space) to
// the files that invoke it.
func v1ApeCalls(t *testing.T, tree string) map[string][]string {
	t.Helper()
	calls := map[string][]string{}
	var files []string
	for _, dir := range []string{".claude/skills", "_claude/skills"} {
		_ = filepath.WalkDir(filepath.Join(tree, filepath.FromSlash(dir)), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
				files = append(files, p)
			}
			return nil
		})
	}
	mig, _ := filepath.Glob(filepath.Join(tree, "_apex", "migrations", "*.md"))
	files = append(files, mig...)
	require.NotEmpty(t, files, "no skills or migration entries under %s: not a v1 framework tree", tree)
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		rel, _ := filepath.Rel(tree, f)
		for _, seg := range v1CodeSegments(string(data)) {
			for _, m := range v1ApeCallRe.FindAllStringSubmatch(seg, -1) {
				words := strings.Fields(m[1])
				if len(words) > 3 {
					words = words[:3]
				}
				key := strings.Join(words, " ")
				if !slices.Contains(calls[key], rel) {
					calls[key] = append(calls[key], rel)
				}
			}
		}
	}
	return calls
}

// v1Resolves judges one invocation: a command group must consume every
// word as a subcommand; a leaf command may take the rest as arguments.
func v1Resolves(words []string, bin string) bool {
	if bin != "" {
		out, err := exec.Command(bin, append(append([]string{}, words...), "--help")...).CombinedOutput()
		if err != nil || strings.Contains(string(out), "unknown command") {
			return false
		}
		// The command help reached: the path on its Usage line. A group asked
		// for an unknown subcommand prints its own help and exits 0, so exit
		// 0 alone proves nothing.
		path, group := helpPath(string(out))
		if len(path) > len(words) || strings.Join(path, " ") != strings.Join(words[:len(path)], " ") {
			return false
		}
		return len(path) == len(words) || !group
	}
	commandTreeMu.Lock()
	defer commandTreeMu.Unlock()
	cmd, rest, err := rootCmd.Find(words)
	if err != nil || cmd == nil || cmd == rootCmd {
		return false
	}
	return len(rest) == 0 || !cmd.HasSubCommands()
}

// helpPath is the command path a help text's first Usage line names (its
// words after "ape", up to a placeholder or flag), and whether it is a
// group (it lists Available Commands).
func helpPath(help string) ([]string, bool) {
	var path []string
	lines := strings.Split(help, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "Usage:" || i+1 >= len(lines) {
			continue
		}
		fields := strings.Fields(lines[i+1])
		for _, f := range fields[min(1, len(fields)):] {
			if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(f) {
				break
			}
			path = append(path, f)
		}
		break
	}
	return path, strings.Contains(help, "Available Commands:")
}

func TestFrameworkV1_SkillCommandsResolve(t *testing.T) {
	tree := strings.TrimSpace(os.Getenv("APEX_FRAMEWORK_V1"))
	if tree == "" {
		t.Skip("set APEX_FRAMEWORK_V1 to a v1 framework tree (framework-v1/, a payload, or an installed project)")
	}
	bin := strings.TrimSpace(os.Getenv("APE_BIN"))
	calls := v1ApeCalls(t, tree)
	keys := make([]string, 0, len(calls))
	for k := range calls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var missing []string
	for _, k := range keys {
		ok := v1Resolves(strings.Fields(k), bin)
		t.Logf("%-8s ape %-24s %s", map[bool]string{true: "ok", false: "MISSING"}[ok], k, strings.Join(calls[k], ", "))
		if !ok {
			missing = append(missing, "ape "+k+" ("+strings.Join(calls[k], ", ")+")")
		}
	}
	require.NotEmpty(t, keys, "the tree's skills invoke no ape command: the extraction found nothing to judge")
	require.Empty(t, missing, "the v1 framework invokes commands this binary does not provide")
}

func TestFrameworkV1_Floor(t *testing.T) {
	tree := strings.TrimSpace(os.Getenv("APEX_FRAMEWORK_V1"))
	if tree == "" {
		t.Skip("set APEX_FRAMEWORK_V1 to a v1 framework tree")
	}
	manifest, err := framework.LoadApeCommands(tree)
	require.NoError(t, err, "the v1 tree's _apex/ape-commands.yaml does not parse")
	require.NotNil(t, manifest, "the v1 tree ships no _apex/ape-commands.yaml: it declares no ape floor (H1)")
	require.NotEmpty(t, manifest.MinApeVersion, "the v1 tree's _apex/ape-commands.yaml declares no min_ape_version (H1)")
	version := Version
	if bin := strings.TrimSpace(os.Getenv("APE_BIN")); bin != "" {
		out, err := exec.Command(bin, "version").Output()
		require.NoError(t, err)
		first, _, _ := strings.Cut(string(out), "\n")
		version = strings.TrimSpace(strings.TrimPrefix(first, "ape "))
	}
	switch framework.CompareApeVersion(version, manifest.MinApeVersion) {
	case framework.ApeVersionBelow:
		t.Fatalf("ape %s is below the framework's min_ape_version %s", version, manifest.MinApeVersion)
	case framework.ApeVersionUnknown:
		t.Logf("floor declared: min_ape_version %s; NOT compared — ape %s is a dev build or a pseudo-version (a released or rc build compares)", manifest.MinApeVersion, version)
	case framework.ApeVersionOK:
		t.Logf("floor: ape %s satisfies min_ape_version %s", version, manifest.MinApeVersion)
	case framework.ApeVersionNoMinimum:
		t.Fatal("no min_ape_version") // unreachable after the NotEmpty above
	}
}

// The extractor reads code, never prose: a sentence that starts with "ape"
// is not an invocation.
func TestFrameworkV1_ExtractsCodeOnly(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, ".claude", "skills", "apex-x", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skill), 0o755))
	require.NoError(t, os.WriteFile(skill, []byte("Run `ape change start <id>` then `ape status --json`.\n"+
		"When ape blocked it, stop.\n```sh\nape next add \"x\" --from y && ape milestone gate M1\n```\n"), 0o644))
	mig := filepath.Join(dir, "_apex", "migrations", "v1.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(mig), 0o755))
	require.NoError(t, os.WriteFile(mig, []byte("---\nid: x\ncommand: ape project lift --from-update\n---\nape enforces the rest.\n"), 0o644))
	calls := v1ApeCalls(t, dir)
	keys := make([]string, 0, len(calls))
	for k := range calls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	require.Equal(t, []string{"change start", "milestone gate", "next add", "project lift", "status"}, keys)
	require.True(t, v1Resolves([]string{"change", "start"}, ""))
	require.True(t, v1Resolves([]string{"task", "apex-plan"}, ""), "a leaf takes arguments")
	require.False(t, v1Resolves([]string{"change", "nope"}, ""), "a group must consume the word as a subcommand")
	require.False(t, v1Resolves([]string{"story", "verify"}, ""), "a retired v0 command")

	path, group := helpPath("Usage:\n  ape project [command]\n\nAvailable Commands:\n  init  x\n")
	require.Equal(t, []string{"project"}, path)
	require.True(t, group)
	path, group = helpPath("Usage:\n  ape task <skill> [flags]\n")
	require.Equal(t, []string{"task"}, path)
	require.False(t, group)
}
