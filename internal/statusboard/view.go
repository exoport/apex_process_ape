package statusboard

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// statColumns is how many counter cells sit per row; the ui grid collapses
// to one column under 700px regardless.
const statColumns = 4

// outsideFlowRows caps the outside-the-flow table: the count is on the
// card, and `ape status --json` has every commit.
const outsideFlowRows = 10

// The board colour TOKENS this view uses — never a hex. There are two
// themes and a project may patch either, so a colour chosen here would be
// right in one and illegible in the others, and nothing would report it.
const (
	toneAccent = "accent"
	toneMark   = "mark"
	toneAgent  = "agent"
	toneDanger = "danger"
	toneMuted  = "muted"
	toneDim    = "dim"
)

// changeTones maps a change status to a tone. blocked is danger on purpose:
// it is the one a person has to see.
var changeTones = map[string]string{
	apexv1.StatusDraft:      toneMuted,
	apexv1.StatusInProgress: toneAccent,
	apexv1.StatusVerified:   toneAgent,
	apexv1.StatusReviewed:   toneMark,
	apexv1.StatusBlocked:    toneDanger,
	apexv1.StatusArchived:   toneDim,
	apexv1.StatusAnswered:   toneAccent,
	"invalid":               toneDanger,
}

// milestoneTones maps a milestone status to a tone.
var milestoneTones = map[string]string{
	apexv1.MilestoneProposed: toneMuted,
	apexv1.MilestoneApproved: toneAccent,
	apexv1.MilestoneDone:     toneAgent,
}

// dataRefreshed is the state.data path the freshness stamp is bound from.
// It and dataQuietSet are all of data, the only part of the view the
// unchanged-view comparison ignores (statusboard.go sameView).
const dataRefreshed = "refreshed"

// dataQuietSet marks that ape has set the tab quiet once (statusboard.go
// QUIET); like the freshness stamp, the view comparison ignores it.
const dataQuietSet = "quietSet"

// buildState renders a status report as a `ui` component tree: the same
// figures `ape status --json` reports, and nothing computed beyond them.
//
// FRESHNESS IS IN THE TREE, bound from state.data: `heartbeat` and
// `readOnly` are declared by other tab types and read by nothing in the ui
// renderer, so `aboard apply` would warn about both on every write. The
// tree holds no `field` and no `button`, so there is nothing on it to edit.
func buildState(r *apexv1.StatusReport, now time.Time) map[string]any {
	children := []any{
		headerCard(r),
		milestoneCard(r),
		changesCard(r),
		personCard(r),
		tweaksCard(r),
		roadmapCard(r),
		roadmapNextCard(r),
		outsideFlowCard(r),
		latestRunCard(r),
		freshnessCard(r),
	}
	return map[string]any{
		"root": map[string]any{"type": "col", "gap": "16px", "children": compact(children)},
		"data": map[string]any{dataRefreshed: now.UTC().Format(time.RFC3339), dataQuietSet: true},
	}
}

// compact drops the cards a section left out (nil).
func compact(xs []any) []any {
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		if m, ok := x.(map[string]any); ok && m != nil {
			out = append(out, x)
		}
	}
	return out
}

func headerCard(r *apexv1.StatusReport) map[string]any {
	pairs := []any{kv("project", r.Project)}
	if r.Branch != "" {
		pairs = append(pairs, kv("branch", r.Branch))
	}
	if u := r.BaseUpstream; u != nil {
		pairs = append(pairs, kv("base", fmt.Sprintf("%s: %d ahead, %d behind %s", u.Branch, u.Ahead, u.Behind, u.Upstream)))
	}
	pairs = append(pairs,
		kv("archived", strconv.Itoa(r.Archived)),
		kv("spend", fmt.Sprintf("$%.2f over %d run(s)", r.Spend.TotalUSD, r.Spend.Runs)))
	children := []any{
		map[string]any{"type": "notice", "label": "Next:", "value": orDash(r.Next), "tone": toneAccent},
		map[string]any{"type": "kv", "pairs": pairs},
	}
	if j := r.ArchiveJournal; j != nil {
		state := "interrupted — the next writing command rolls it back"
		if r.ArchiveRunning {
			state = "in flight"
		}
		children = append(children, map[string]any{
			"type": "notice", "label": "Archive " + state + ":", "value": j.ID, "tone": toneDanger,
		})
	}
	if !r.Initialized {
		children = append(children, map[string]any{
			"type": "notice", "label": "Not initialized:", "value": "run `ape project init`", "tone": toneDanger,
		})
	}
	return card("APEX — "+r.Project, toneAccent, children)
}

func milestoneCard(r *apexv1.StatusReport) map[string]any {
	m := r.Milestone
	if m == nil {
		return card("Milestone", "", []any{caption("No current milestone: every milestone is done, or the roadmap has none.")})
	}
	gate, gateTone := "not run", toneMuted
	if g := m.GateLast; g != nil {
		gate, gateTone = "green", toneAgent
		if !g.Green() {
			gate, gateTone = "red", toneDanger
		}
	}
	blocksTone := toneMuted
	if m.ConsecutiveBlocks > 0 {
		blocksTone = toneMark
	}
	if m.ConsecutiveBlocksCap > 0 && m.ConsecutiveBlocks >= m.ConsecutiveBlocksCap {
		blocksTone = toneDanger
	}
	spent := fmt.Sprintf("$%.2f", m.SpentUSD)
	if m.Budget != nil {
		spent += fmt.Sprintf(" / $%.2f", *m.Budget)
	}
	children := []any{
		map[string]any{"type": "grid", "columns": statColumns, "gap": "14px", "children": []any{
			stat(m.Status, "status", milestoneTones[m.Status]),
			stat(gate, "gate", gateTone),
			stat(fmt.Sprintf("%d/%d", m.ConsecutiveBlocks, m.ConsecutiveBlocksCap), "blocks in a row / cap", blocksTone),
			stat(spent, "spent", ""),
		}},
		map[string]any{"type": "kv", "pairs": []any{kv("gate command", orDash(m.Gate))}},
	}
	if g := m.GateLast; g != nil {
		pairs := []any{kv("last gate run", fmt.Sprintf("%s at %.7s, exit %d", g.At, g.SHA, g.Exit))}
		if !g.Green() {
			pairs = append(pairs, kv("why", g.Why()))
		}
		if len(g.Flaky) > 0 {
			pairs = append(pairs, kv("flaky", strings.Join(g.Flaky, ", ")))
		}
		children = append(children, map[string]any{"type": "kv", "pairs": pairs})
	}
	for _, d := range m.Diagnostics {
		children = append(children, map[string]any{
			"type": "notice", "label": d.Code + " (" + d.Action + "):", "value": d.Message, "tone": toneDanger,
		})
	}
	return card(m.ID+" — "+m.Title, toneAccent, children)
}

func changesCard(r *apexv1.StatusReport) map[string]any {
	if len(r.Changes) == 0 {
		return card("Changes — 0", "", []any{caption("No change in flight.")})
	}
	rows := make([]any, 0, len(r.Changes))
	for i := range r.Changes {
		c := &r.Changes[i]
		rows = append(rows, map[string]any{
			"id":     c.ID,
			"change": c.ID,
			"lane":   c.Lane,
			"status": c.Status,
			"branch": c.Branch,
			"why":    changeWhy(c),
			"person": strings.Join(c.AwaitingHuman, ", "),
			"flaky":  strings.Join(c.Flaky, ", "),
		})
	}
	return card(fmt.Sprintf("Changes — %d", len(r.Changes)), "", []any{
		map[string]any{"type": "grid", "columns": statColumns, "gap": "14px", "children": changeCounts(r.Changes)},
		map[string]any{
			"type": "table",
			"columns": []any{
				col("change", "Change"), col("lane", "Lane"), col("status", "Status"), col("branch", "Branch"),
				col("why", "Blocked / failing / answer"), col("person", "Awaiting a person"), col("flaky", "Flaky"),
			},
			"rows": rows,
		},
	})
}

// changeCounts is one stat per status present, in the lifecycle's order.
func changeCounts(cs []apexv1.ChangeStatus) []any {
	order := []string{
		apexv1.StatusDraft, apexv1.StatusInProgress, apexv1.StatusVerified, apexv1.StatusReviewed,
		apexv1.StatusBlocked, apexv1.StatusAnswered, apexv1.StatusArchived, "invalid",
	}
	n := map[string]int{}
	for i := range cs {
		n[cs[i].Status]++
	}
	out := []any{}
	for _, s := range order {
		if n[s] > 0 {
			out = append(out, stat(strconv.Itoa(n[s]), s, changeTones[s]))
			delete(n, s)
		}
	}
	// A status outside the vocabulary: named, never folded in. Sorted, so
	// the view (and so whether it changed) does not depend on map order.
	rest := make([]string, 0, len(n))
	for s := range n {
		rest = append(rest, s)
	}
	sort.Strings(rest)
	for _, s := range rest {
		out = append(out, stat(strconv.Itoa(n[s]), s, toneDanger))
	}
	return out
}

// changeWhy is what a person reading the row needs: the blocked reason,
// the red checks, the answer to a block, or an unreadable change's error.
func changeWhy(c *apexv1.ChangeStatus) string {
	var parts []string
	if c.Error != "" {
		parts = append(parts, c.Error)
	}
	if c.BlockedReason != "" {
		parts = append(parts, "blocked: "+c.BlockedReason)
	}
	if len(c.Failing) > 0 {
		parts = append(parts, "failing: "+strings.Join(c.Failing, ", "))
	}
	if c.AttemptsCap > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d attempts", c.Attempts, c.AttemptsCap))
	}
	if a := c.Answer; a != nil {
		parts = append(parts, fmt.Sprintf("answered by %s: %s", a.By, a.Text))
	}
	return strings.Join(parts, " · ")
}

// personCard is what waits for a person: human-review tasks not accepted
// (awaiting_human), and tweaks awaiting approval. Rendered when empty too:
// an ABSENT panel cannot be told from one that stopped being computed.
func personCard(r *apexv1.StatusReport) map[string]any {
	var items []any
	for i := range r.Changes {
		c := &r.Changes[i]
		if len(c.AwaitingHuman) > 0 {
			items = append(items, fmt.Sprintf("%s: accept %s — `ape change accept %s`", c.ID, strings.Join(c.AwaitingHuman, ", "), c.ID))
		}
		if c.Lane == apexv1.LaneTweak && c.Approved == nil && c.Status != apexv1.StatusArchived {
			items = append(items, fmt.Sprintf("%s: a tweak awaiting approval — `ape change approve %s --by <who>`", c.ID, c.ID))
		}
	}
	body := []any{caption("Nothing waiting on a person.")}
	tone := ""
	if len(items) > 0 {
		body, tone = []any{map[string]any{"type": "list", "items": items}}, toneMark
	}
	return card(fmt.Sprintf("Awaiting a person — %d", len(items)), tone, body)
}

func tweaksCard(r *apexv1.StatusReport) map[string]any {
	body := []any{caption("No approved tweak waiting.")}
	if len(r.Tweaks) > 0 {
		items := make([]any, len(r.Tweaks))
		for i, id := range r.Tweaks {
			items[i] = id
		}
		body = []any{
			map[string]any{"type": "list", "items": items},
			caption("In the order they run; the first runs ahead of milestone work at the next change boundary."),
		}
	}
	return card(fmt.Sprintf("Tweaks queue — %d", len(r.Tweaks)), "", body)
}

func roadmapCard(r *apexv1.StatusReport) map[string]any {
	if len(r.Milestones) == 0 {
		return card("Roadmap", "", []any{caption("No milestone in roadmap.md.")})
	}
	rows := make([]any, 0, len(r.Milestones))
	for _, m := range r.Milestones {
		note := ""
		if a := m.AcceptRed; a != nil {
			note = fmt.Sprintf("accepted red by %s: %s", a.By, a.Reason)
		}
		rows = append(rows, map[string]any{"id": m.ID, "m": m.ID, "title": m.Title, "status": m.Status, "note": note})
	}
	return card("Roadmap", "", []any{map[string]any{
		"type":    "table",
		"columns": []any{col("m", "Milestone"), col("title", "Title"), col("status", "Status"), col("note", "Accepted red")},
		"rows":    rows,
	}})
}

func roadmapNextCard(r *apexv1.StatusReport) map[string]any {
	body := []any{caption("Nothing noticed out of scope.")}
	if len(r.RoadmapNext) > 0 {
		items := make([]any, len(r.RoadmapNext))
		for i, it := range r.RoadmapNext {
			items[i] = it
		}
		body = []any{
			map[string]any{"type": "list", "items": items},
			caption("Findings noticed out of scope, for apex-plan to triage at the next delivery (next.md)."),
		}
	}
	return card(fmt.Sprintf("Next — %d", len(r.RoadmapNext)), "", body)
}

func outsideFlowCard(r *apexv1.StatusReport) map[string]any {
	if len(r.OutsideFlow) == 0 {
		return card("Outside the change flow — 0", "", []any{caption("Every code commit on the base since the last archive came through a change.")})
	}
	rows := []any{}
	for i, o := range r.OutsideFlow {
		if i == outsideFlowRows {
			break
		}
		sha := o.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		rows = append(rows, map[string]any{"id": o.SHA, "sha": sha, "subject": o.Subject, "files": strings.Join(o.Files, ", ")})
	}
	children := []any{map[string]any{
		"type":    "table",
		"columns": []any{col("sha", "Commit"), col("subject", "Subject"), col("files", "Files")},
		"rows":    rows,
	}}
	if n := len(r.OutsideFlow); n > outsideFlowRows {
		children = append(children, caption(fmt.Sprintf("Newest %d of %d; `ape status --json` lists them all.", outsideFlowRows, n)))
	}
	children = append(children, caption("Code commits on the base with no Change: trailer. Shown, never failed."))
	return card(fmt.Sprintf("Outside the change flow — %d", len(r.OutsideFlow)), toneMark, children)
}

func latestRunCard(r *apexv1.StatusReport) map[string]any {
	l := r.LatestRun
	if l == nil {
		return card("Latest run", "", []any{caption("No ape task run yet.")})
	}
	tone := toneAgent
	if l.Stopped != "" || l.Status != "completed" {
		tone = toneMark
	}
	pairs := []any{kv("skill", l.Skill), kv("run", l.RunID), kv("status", l.Status)}
	if l.Stopped != "" {
		pairs = append(pairs, kv("stopped", l.Stopped))
	}
	if l.Resets != "" {
		pairs = append(pairs, kv("resets", l.Resets))
	}
	if l.Message != "" {
		pairs = append(pairs, kv("message", l.Message))
	}
	return card("Latest run", tone, []any{map[string]any{"type": "kv", "pairs": pairs}})
}

func freshnessCard(r *apexv1.StatusReport) map[string]any {
	return card("Freshness", "", []any{
		map[string]any{"type": "kv", "pairs": []any{
			map[string]any{"key": "last changed", "value": map[string]any{"bind": dataRefreshed}},
			kv("by", Actor+" (`ape status --board`, and after every state-changing v1 command)"),
			kv("source", "`ape status --json` in "+r.Root),
		}},
		map[string]any{
			"type": "caption",
			"value": "A view, not a record: the records are in git, and `ape status` derives this from them. " +
				"It changes only when the view does, and nothing on the board is read back.",
			"tone": toneDim,
		},
	})
}

func card(title, accent string, children []any) map[string]any {
	c := map[string]any{"type": "card", "title": title, "children": children}
	if accent != "" {
		c["accent"] = accent
	}
	return c
}

func stat(value, label, tone string) map[string]any {
	s := map[string]any{"type": "stat", "value": value, "label": label}
	if tone != "" {
		s["tone"] = tone
	}
	return s
}

func caption(text string) map[string]any { return map[string]any{"type": "caption", "value": text} }

func kv(key, value string) map[string]any { return map[string]any{"key": key, "value": value} }

func col(id, label string) map[string]any { return map[string]any{"id": id, "label": label} }

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
