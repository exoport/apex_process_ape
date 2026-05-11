package pipeline

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	secondsPerMinute  = 60
	thousandThreshold = 1000
)

// renderReport produces pipeline-report.md from a finalized Manifest.
// The output is intentionally plain — no Markdown extensions, no HTML
// fallback. Renders deterministically; the same manifest always yields
// byte-identical output.
func renderReport(m *Manifest) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Pipeline run: %s\n\n", m.Pipeline.Name)
	fmt.Fprintf(&b, "- ape version: `%s`\n", m.ApeVersion)
	fmt.Fprintf(&b, "- run id: `%s`\n", m.RunID)
	fmt.Fprintf(&b, "- started: %s\n", m.StartedAt.Format("2006-01-02 15:04:05 UTC"))
	if !m.EndedAt.IsZero() {
		fmt.Fprintf(&b, "- ended: %s\n", m.EndedAt.Format("2006-01-02 15:04:05 UTC"))
	}
	fmt.Fprintf(&b, "- duration: %s\n", formatDuration(m.DurationSecs))
	fmt.Fprintf(&b, "- status: **%s**\n", m.Status)
	if m.Pipeline.Source != "" {
		fmt.Fprintf(&b, "- source: `%s`\n", m.Pipeline.Source)
	}
	if m.Pipeline.Digest != "" {
		fmt.Fprintf(&b, "- digest: `%s`\n", m.Pipeline.Digest)
	}
	b.WriteString("\n## Totals\n\n")
	b.WriteString("| Metric | Value |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| cost | $%.4f |\n", m.Totals.CostUSD)
	fmt.Fprintf(&b, "| tokens in | %s |\n", formatInt(m.Totals.TokensInput))
	fmt.Fprintf(&b, "| tokens out | %s |\n", formatInt(m.Totals.TokensOutput))
	fmt.Fprintf(&b, "| cache read | %s |\n", formatInt(m.Totals.TokensCacheRead))
	fmt.Fprintf(&b, "| cache creation | %s |\n", formatInt(m.Totals.TokensCacheCreation))
	fmt.Fprintf(&b, "| steps run | %d |\n", m.Totals.StepsRun)
	fmt.Fprintf(&b, "| steps failed | %d |\n", m.Totals.StepsFailed)

	for si := range m.Stages {
		stage := &m.Stages[si]
		fmt.Fprintf(&b, "\n## Stage %02d — %s\n\n", stage.Index, stage.Name)
		fmt.Fprintf(&b, "- duration: %s\n", formatDuration(stage.DurationSecs))
		fmt.Fprintf(&b, "- status: **%s**\n\n", stage.Status)
		if len(stage.Steps) == 0 {
			b.WriteString("_(no steps recorded)_\n")
			continue
		}
		b.WriteString("| # | Skill | Agent | Duration | Cost | Tokens (in/out) | Status |\n")
		b.WriteString("| - | --- | --- | --- | --- | --- | --- |\n")
		for pi := range stage.Steps {
			step := &stage.Steps[pi]
			agent := step.Agent
			if agent == "" {
				agent = "—"
			}
			fmt.Fprintf(&b, "| %d | `%s` | `%s` | %s | $%.4f | %s / %s | %s |\n",
				step.Index, step.Skill, agent,
				formatDuration(step.DurationSecs), step.CostUSD,
				formatInt(step.TokensInput), formatInt(step.TokensOutput),
				step.Status,
			)
		}
		for pi := range stage.Steps {
			step := &stage.Steps[pi]
			if step.EventsPath != "" {
				fmt.Fprintf(&b, "\n- step %d events: `%s`\n", step.Index, step.EventsPath)
			}
		}
	}
	return b.String()
}

func formatDuration(secs float64) string {
	if secs >= secondsPerMinute {
		mins := int(secs) / secondsPerMinute
		rem := secs - float64(mins*secondsPerMinute)
		return fmt.Sprintf("%dm %.1fs", mins, rem)
	}
	return fmt.Sprintf("%.1fs", secs)
}

func formatInt(n int) string {
	if n < thousandThreshold {
		return strconv.Itoa(n)
	}
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
