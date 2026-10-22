package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sha is a string's sha256, hex.
func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// W6 (Q-E1): the advise slot's schema — {name, skill, when, load, for?,
// paths?, touches?}; paths:/touches: on every slot (Q-E2); what is wrong
// is tools.invalid, and only valid entries are kept.
func TestToolsConfig_AdviseSchema(t *testing.T) {
	isolatedClaudeDir(t)
	p := settingsProject(t, "")
	writeConfigForTest(t, p.Root, `tools:
  review:
    - {name: sec, skill: sec-lens, when: auth, paths: ["internal/auth/**"], touches: [auth]}
    - {name: badglob, skill: s, when: x, paths: ["/abs/**"]}
    - {name: badslug, skill: s, when: x, touches: [Not_A_Slug]}
    - {name: rload, skill: s, when: x, load: [a.md]}
  advise:
    - {name: nats, skill: "nats-expert:nats-expert", when: "NATS", paths: ["internal/bus/**"], touches: [messaging], load: [advise.md], for: [change, apply]}
    - {name: one, skill: kb, when: x, load: rules.md}
    - {name: noload, skill: kb, when: x}
    - {name: escape, skill: kb, when: x, load: [../secrets.md, /etc/passwd]}
    - {name: delegate, skill: kb, when: x, load: [a.md], for: [accept]}
    - {name: twice, skill: kb, when: x, load: [a.md], for: [apply, apply]}
    - {name: session, skill: kb, when: x, load: [a.md], model: sonnet}
`)
	cfg, err := LoadConfig(p.Root)
	require.NoError(t, err)
	require.Equal(t, []Tool{{Name: "sec", Skill: "sec-lens", When: "auth", Paths: []string{"internal/auth/**"}, Touches: []string{"auth"}}}, cfg.Tools.Review)
	require.Equal(t, []Tool{
		{Name: "nats", Skill: "nats-expert:nats-expert", When: "NATS", Paths: []string{"internal/bus/**"}, Touches: []string{"messaging"}, Load: []string{"advise.md"}, For: []string{"change", "apply"}},
		{Name: "one", Skill: "kb", When: "x", Load: []string{"rules.md"}},
	}, cfg.Tools.Advise)
	require.Equal(t, AdviseSessions, cfg.Tools.Advise[1].Sessions(), "for: unset is all three")

	msgs := findingMessages(p.CheckPluginContents(ctx(), nil))
	for _, want := range []string{
		`tools.review[1]: paths entry "/abs/**" is not a glob relative to the project root`,
		`tools.review[2]: touches entry "Not_A_Slug" is not a kebab-case capability slug`,
		`tools.review[3]: unknown key "load" — an entry is {name, skill, when, args?, model?, effort?, paths?, touches?} (load and for are an advise entry's)`,
		`tools.advise[2]: missing load`,
		`tools.advise[3]: load entry "../secrets.md" is not a file path inside the skill's folder`,
		`tools.advise[3]: load entry "/etc/passwd" is not a file path inside the skill's folder`,
		`tools.advise[4]: for "accept" is not one of change, apply, review`,
		`tools.advise[5]: for lists "apply" twice`,
		`tools.advise[6]: unknown key "model" — an entry is {name, skill, when, load, for?, paths?, touches?} (an advise entry starts no session)`,
	} {
		require.Contains(t, msgs, want)
	}
	require.NotContains(t, p.ChildSkillAllowList(cfg), "kb", "an advise skill starts no session")
	require.Contains(t, p.ChildSkillAllowList(cfg), "sec-lens")
}

// adviseProject is a git project with a project skill nats whose
// advise.md is body, and an advise entry naming it.
func adviseProject(t *testing.T, config, body string) *Project {
	t.Helper()
	isolatedClaudeDir(t)
	files := baseFiles()
	files["_apex/config.yaml"] = config
	files[".claude/skills/nats/SKILL.md"] = skillMD("nats", "disable-model-invocation: true\nuser-invocable: false\n")
	files[".claude/skills/nats/advise.md"] = body
	return fixture(t, files)
}

const adviseConfig = `tools:
  review:
    - {name: sec, skill: sec-lens, when: "auth", paths: ["src/auth/**"]}
  advise:
    - {name: nats, skill: nats, when: "NATS", paths: ["src/bus/**"], load: [advise.md]}
`

// An advise entry resolves to its skill's folder: plugins status lists its
// files with bytes and sha256, needs no slash-invocable skill, and reports
// a missing load file (tools.not-installed) and files over caps.advise
// (tools.too-big) on _apex/config.yaml.
func TestAdvise_EntryStatusAndCaps(t *testing.T) {
	body := "N1 NEVER fetch by count alone.\n"
	p := adviseProject(t, adviseConfig, body)
	write(t, p, ".claude/skills/sec-lens/SKILL.md", skillMD("sec-lens", ""))
	rep, err := p.PluginsStatus(ctx(), nil)
	require.NoError(t, err)
	var adv *ToolStatus
	for i := range rep.Tools {
		if rep.Tools[i].Slot == "advise" {
			adv = &rep.Tools[i]
		}
	}
	require.NotNil(t, adv)
	require.Equal(t, ToolOK, adv.State, "user-invocable: false is fine for advise")
	require.Equal(t, "project", adv.Source)
	require.Equal(t, AdviseSessions, adv.For)
	require.Equal(t, []AdviseFile{{
		Name: "nats", Skill: "nats", For: AdviseSessions, File: "advise.md",
		Path: filepath.Join(p.Root, ".claude", "skills", "nats", "advise.md"), Bytes: len(body), SHA256: sha(body),
	}}, adv.Files)
	require.Empty(t, rep.Findings, findingMessages(rep.Findings))

	write(t, p, ".claude/skills/nats/advise.md", strings.Repeat("x", 9<<10))
	fs := p.CheckPluginContents(ctx(), nil)
	require.Equal(t, []string{CodeToolsTooBig}, codesOf(fs))
	require.Contains(t, findingMessages(fs), "tool advise/nats: skill nats loads 9.0 KB, over the 8 KB cap (caps.advise)")
	require.Equal(t, ActionReplan, ActionFor(CodeToolsTooBig))

	write(t, p, "_apex/config.yaml", adviseConfig+"caps:\n  advise: 12KB\n")
	require.Empty(t, p.CheckPluginContents(ctx(), nil), "caps.advise raised")
	write(t, p, "_apex/config.yaml", adviseConfig+"caps:\n  advise: 20KB\n")
	require.Contains(t, findingMessages(p.Check()), "caps.advise: 20 KB is over its 16 KB ceiling")

	write(t, p, "_apex/config.yaml", strings.Replace(adviseConfig, "load: [advise.md]", "load: [advise.md, gone.md]", 1))
	fs = p.CheckPluginContents(ctx(), nil)
	require.Contains(t, codesOf(fs), "tools.not-installed")
	require.Contains(t, findingMessages(fs), "tool advise/nats: skill nats is not installed with its load file gone.md (missing: ")
}

// change.md's advise: is change check's: a name tools.advise does not
// declare is tools.unknown; an entry over caps.advise and a session over
// caps.advise_session are tools.too-big. project check reports the
// per-entry cap once, on the config, not per change.
func TestChangeCheck_Advise(t *testing.T) {
	p := adviseProject(t, adviseConfig+`    - {name: big1, skill: big1, when: x, load: [a.md]}
    - {name: big2, skill: big2, when: x, load: [a.md], for: [review]}
`, "rules\n")
	write(t, p, ".claude/skills/big1/SKILL.md", skillMD("big1", ""))
	write(t, p, ".claude/skills/big1/a.md", strings.Repeat("a", 7<<10))
	write(t, p, ".claude/skills/big2/SKILL.md", skillMD("big2", ""))
	write(t, p, ".claude/skills/big2/a.md", strings.Repeat("b", 7<<10))
	doc := func(advise string) string {
		return strings.Replace(waveChange("add-wave"), "touches: [greeting]", "touches: [greeting]\nadvise: ["+advise+"]", 1)
	}
	addChange(t, p, "add-wave", doc("nats, nope"))
	fs := p.CheckChange("add-wave")
	require.Equal(t, []string{"tools.unknown"}, codesOf(fs), findingMessages(fs))
	require.Contains(t, findingMessages(fs), "advise: nope is not declared in tools.advise of _apex/config.yaml — name a declared advise entry (nats, big1, big2), or drop it")

	// The review session reads all three, 14 KB and 6 bytes: under the
	// 16 KB default, over a 10 KB caps.advise_session.
	write(t, p, "development/changes/add-wave/change.md", doc("nats, big1, big2"))
	require.Empty(t, p.CheckChange("add-wave"))
	write(t, p, "_apex/config.yaml", adviseConfig+`    - {name: big1, skill: big1, when: x, load: [a.md]}
    - {name: big2, skill: big2, when: x, load: [a.md], for: [review]}
caps:
  advise_session: 10KB
`)
	fs = p.CheckChange("add-wave")
	require.Equal(t, []string{CodeToolsTooBig}, codesOf(fs), findingMessages(fs))
	require.Contains(t, findingMessages(fs), "advise: the review session would read 14.0 KB (nats, big1, big2), over the 10 KB cap (caps.advise_session)")

	write(t, p, ".claude/skills/big1/a.md", strings.Repeat("a", 9<<10))
	write(t, p, "_apex/config.yaml", adviseConfig+`    - {name: big1, skill: big1, when: x, load: [a.md]}
    - {name: big2, skill: big2, when: x, load: [a.md], for: [review]}
`)
	fs = p.CheckChange("add-wave")
	require.Contains(t, findingMessages(fs), "advise: big1 loads 9.0 KB, over the 8 KB cap (caps.advise)")
	require.Contains(t, findingMessages(fs), "advise: the review session would read 16.0 KB (nats, big1, big2)")
	var inProject []Finding
	for _, f := range ClassifyFindings(p.Check()) {
		if f.Code == CodeToolsTooBig {
			inProject = append(inProject, f)
		}
	}
	require.Len(t, inProject, 1, "project check's change pass: the session cap only; the entry cap is CheckPluginContents', on the config")
}

// Q-E2: tools.unmatched is a notice of change check — an entry the change
// does not list whose paths: meet its planned paths: or whose touches:
// it names — and never a finding.
func TestChangeNotices_Unmatched(t *testing.T) {
	cfg := adviseConfig + "    - {name: msg, skill: nats, when: messaging, touches: [greeting], load: [advise.md]}\n"
	p := adviseProject(t, cfg, "rules\n")
	doc := strings.Replace(waveChange("add-wave"), "touches: [greeting]", "touches: [greeting]\npaths: [\"src/bus/*.go\", \"docs/**\"]", 1)
	addChange(t, p, "add-wave", doc)
	commitAll(t, p, "plan")
	ns := p.ChangeNotices(ctx(), "add-wave")
	require.Len(t, ns, 2)
	for _, n := range ns {
		require.Equal(t, SeverityNotice, n.Severity)
		require.Equal(t, CodeToolsUnmatched, n.Code)
		require.Equal(t, ActionReplan, n.Action)
		require.Equal(t, "development/changes/add-wave/change.md", n.File)
	}
	require.Equal(t, `tools.advise entry nats matches this change (paths: src/bus/*.go ~ src/bus/**) and the change does not list it`, ns[0].Message)
	require.Equal(t, `if its when: ("NATS") applies, list it: advise: [nats]; if not, leave it: a warning, never a refusal`, ns[0].Fix)
	require.Contains(t, ns[1].Message, "tools.advise entry msg matches this change (touches: greeting)")
	require.Empty(t, p.CheckChange("add-wave"), "a notice is not a finding")

	write(t, p, "development/changes/add-wave/change.md", strings.Replace(doc, "touches: [greeting]", "touches: [greeting]\nadvise: [nats, msg]", 1))
	commitAll(t, p, "plan: list them")
	require.Empty(t, p.ChangeNotices(ctx(), "add-wave"))

	for _, tc := range []struct {
		a, b string
		meet bool
	}{
		{"internal/bus/**", "internal/bus/**", true},
		{"internal/**", "internal/bus/x.go", true},
		{"**/*jetstream*.go", "internal/bus/jetstream.go", true},
		{"internal/bus/**", "cmd/web/**", false},
		{"**/*jetstream*.go", "cmd/web/**", false},
	} {
		require.Equal(t, tc.meet, globsMeet(tc.a, tc.b), "%s ~ %s", tc.a, tc.b)
	}
}

// Verify hashes the change's advise files and records them in state.json
// (no path), and warns tools.unmatched against the diff: a file the
// change's commits touched under an entry's paths:. An expert updated in
// the middle of the change shows in the next verify's record.
func TestVerify_AdviseRecordAndUnmatched(t *testing.T) {
	body := "A1 ALWAYS ack explicitly.\n"
	p := adviseProject(t, adviseConfig, body)
	doc := strings.Replace(waveChange("add-wave"), "touches: [greeting]", "touches: [greeting]\nadvise: [nats]", 1)
	addChange(t, p, "add-wave", doc)
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	write(t, p, "src/auth/login.go", "package auth\n")
	commitAll(t, p, "feat: wave")

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	path := filepath.Join(p.Root, ".claude", "skills", "nats", "advise.md")
	require.Equal(t, []AdviseFile{{Name: "nats", Skill: "nats", For: AdviseSessions, File: "advise.md", Path: path, Bytes: len(body), SHA256: sha(body)}}, v.Advise)
	require.Len(t, v.Notices, 1)
	require.Equal(t, "tools.review entry sec matches this change (src/auth/login.go matches src/auth/**) and the change does not list it", v.Notices[0].Message)
	st := state(t, p, "add-wave")
	require.Equal(t, []AdviseFile{{Name: "nats", Skill: "nats", File: "advise.md", Bytes: len(body), SHA256: sha(body)}}, st.Verify.Advise)
	raw, err := os.ReadFile(p.StateFile("add-wave"))
	require.NoError(t, err)
	require.NotContains(t, string(raw), p.Root, "state.json holds no machine path")

	r := p.Status()
	require.Equal(t, []AdviseFile{}, r.Changes[0].Advise, "Status alone resolves nothing")
	p.AttachAdvise(ctx(), r)
	require.Equal(t, v.Advise, r.Changes[0].Advise)

	updated := body + "A2 ALWAYS bound a fetch by bytes.\n"
	write(t, p, ".claude/skills/nats/advise.md", updated)
	commitAll(t, p, "chore: the expert moved on")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, sha(updated), state(t, p, "add-wave").Verify.Advise[0].SHA256)
}

// A plugin skill's advise files resolve in the listed plugin's install,
// found through the claude plugin CLI — asked only for such an entry.
func TestAdvise_PluginSkill(t *testing.T) {
	p, cli := statusProject(t, pluginConfig+`tools:
  advise:
    - {name: nats, skill: "nats-expert:nats", when: "NATS", load: [advise.md], for: [apply]}
`)
	dir := filepath.Dir(cli.paths["nats-expert@team-tools"])
	natsDir := filepath.Join(cli.paths["nats-expert@team-tools"], "skills", "nats")
	writeTree(t, natsDir, map[string]string{"advise.md": "rules\n"})
	asked := 0
	saved := adviseCLI
	adviseCLI = func() PluginCLI { asked++; return cli }
	t.Cleanup(func() { adviseCLI = saved })

	addChange(t, p, "plain", changeDoc("plain", "greeting", "none — chore", "- T1 x — check: true\n"))
	r := p.Status()
	p.AttachAdvise(ctx(), r)
	require.Equal(t, 0, asked, "no change names advise: the CLI is not asked")

	addChange(t, p, "add-bus", strings.Replace(changeDoc("add-bus", "greeting", "none — chore", "- T1 x — check: true\n"), "touches: [greeting]", "touches: [greeting]\nadvise: [nats]", 1))
	r = p.Status()
	p.AttachAdvise(ctx(), r)
	require.Equal(t, 1, asked)
	var got []AdviseFile
	for _, c := range r.Changes {
		if c.ID == "add-bus" {
			got = c.Advise
		}
	}
	require.Equal(t, []AdviseFile{{
		Name: "nats", Skill: "nats-expert:nats", For: []string{"apply"}, File: "advise.md",
		Path: filepath.Join(natsDir, "advise.md"), Bytes: 6, SHA256: sha("rules\n"),
	}}, got)
	require.DirExists(t, dir)

	adviseCLI = func() PluginCLI { return nil }
	r = p.Status()
	p.AttachAdvise(ctx(), r)
	for _, c := range r.Changes {
		if c.ID == "add-bus" {
			require.Len(t, c.Advise, 1)
			require.Equal(t, "skill nats-expert:nats is unchecked: claude is not on PATH: the installs were not read", c.Advise[0].Error)
		}
	}
}

// change tool refuses an advise entry: it starts no session.
func TestPrepareTool_AdviseEntry(t *testing.T) {
	p := adviseProject(t, adviseConfig, "rules\n")
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	_, err = p.PrepareTool(ctx(), "add-wave", "nats", "")
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "tool.unknown", fe.Code)
	require.Contains(t, fe.Msg, "tools.advise entry nats starts no session")
}
