package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// TestRenderSmoke_RealisticEvents reproduces the user-reported
// post-PLAN-7 left/right misalignment by populating the model with
// realistic long-bodied events through the hook-event renderer path.
func TestRenderSmoke_RealisticEvents(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))
	m = setWindowSize(t, &m, 90, 40)

	pre := loadHookFixture(t, "pretooluse_read.json")
	pre.Step = fakeAlphaStep
	post := loadHookFixture(t, "posttooluse_read_ok.json")
	post.Step = fakeAlphaStep
	for range 50 {
		for _, h := range []orchestrator.HookEvent{pre, post} {
			res, _ := m.Update(hookEventMsg{hook: h})
			m, _ = res.(pipelineModel)
		}
	}
	res, _ := m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)
	t.Logf("stage events=%d", len(m.stages[0].events))

	rightWidth := max(m.width/3, stageListWidthMin)
	leftWidth := max(m.width-rightWidth-panelBorderOverhead, eventPanelWidthMin)
	panelHeight := max(m.height-statusRowReserve, panelHeightMin)

	header := m.renderHeader()
	body := m.renderEventPanel(leftWidth-panelBorderOverhead, panelHeight-headerRowReserve)
	t.Logf("event panel body lines=%d", strings.Count(body, "\n")+1)
	maxVisible := 0
	for line := range strings.SplitSeq(body, "\n") {
		if w := lipgloss.Width(line); w > maxVisible {
			maxVisible = w
		}
	}
	t.Logf("widest visible line=%d (content area=%d)", maxVisible, leftWidth-2)

	leftPanel := pipelinePanelStyle.Width(leftWidth).Height(panelHeight).Render(
		composePanelBody(pipelineHeaderStyle.Render(header), body, panelHeight),
	)
	rightPanel := pipelinePanelStyle.Width(rightWidth).Height(panelHeight).Render(
		composePanelBody(pipelineHeaderStyle.Render("stages"), m.renderStageList(rightWidth-2), panelHeight),
	)
	leftRows := strings.Count(leftPanel, "\n") + 1
	rightRows := strings.Count(rightPanel, "\n") + 1
	t.Logf("leftPanel rows=%d rightPanel rows=%d (expected %d)",
		leftRows, rightRows, panelHeight+2)
	require.Equal(t, rightRows, leftRows, "left and right boxes must match")
}

// designSpec builds a pipeline.Spec mirroring the real `design`
// pipeline the user ran — long stage names that wrap in the narrow
// right column. Used to reproduce the post-PLAN-7 misalignment.
func designSpec(t *testing.T) *pipeline.Spec {
	t.Helper()
	dir := t.TempDir()
	pipelinesDir := filepath.Join(dir, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(pipelinesDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pipelinesDir, "design.yaml"),
		[]byte(`name: design
stages:
  create-prd:
    chain:
      - skill: apex-create-prd
  shard-prd:
    chain:
      - skill: apex-shard-prd
  create-ux-design:
    chain:
      - skill: apex-create-ux-design
  shard-ux-design:
    chain:
      - skill: apex-shard-ux-design
  create-architecture:
    chain:
      - skill: apex-create-architecture
  shard-architecture:
    chain:
      - skill: apex-shard-architecture
`),
		0o644,
	))
	spec, err := pipeline.LoadSpec("design", dir)
	require.NoError(t, err)
	return spec
}

// TestRenderSmoke_DesignPipelineWrap reproduces the user's screenshot
// scenario: design pipeline with long stage names + long durations
// that wrap in the narrow right column. The wrap inflates the right
// panel's rendered height past panelHeight + 2, but composePanelBody
// only pads to LOGICAL line count — it can't account for visual
// wraps lipgloss will perform when rendering. PLAN-7 / F0 didn't
// cover this case.
func TestRenderSmoke_DesignPipelineWrap(t *testing.T) {
	spec := designSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))
	m = setWindowSize(t, &m, 90, 40)

	for i := range m.stages {
		m.stages[i].state = stateDone
		m.stages[i].startedAt = time.Now().Add(-(7*time.Minute + 36*time.Second))
		m.stages[i].endedAt = time.Now()
	}

	res, _ := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)
	m.cursorIdx = 4 // "create-architecture", per user's screenshot

	// Long-bodied events on the cursor stage.
	pre := loadHookFixture(t, "pretooluse_read.json")
	pre.Step = "create-architecture/0-apex-create-architecture"
	post := loadHookFixture(t, "posttooluse_read_ok.json")
	post.Step = "create-architecture/0-apex-create-architecture"
	for range 50 {
		for _, h := range []orchestrator.HookEvent{pre, post} {
			res, _ := m.Update(hookEventMsg{hook: h})
			m, _ = res.(pipelineModel)
		}
	}
	res, _ = m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)

	rightWidth := max(m.width/3, stageListWidthMin)
	leftWidth := max(m.width-rightWidth-panelBorderOverhead, eventPanelWidthMin)
	panelHeight := max(m.height-statusRowReserve, panelHeightMin)

	leftPanel := pipelinePanelStyle.Width(leftWidth).Height(panelHeight).Render(
		composePanelBody(
			pipelineHeaderStyle.Render(m.renderHeader()),
			m.renderEventPanel(leftWidth-panelBorderOverhead, panelHeight-headerRowReserve),
			panelHeight,
		),
	)
	rightPanel := pipelinePanelStyle.Width(rightWidth).Height(panelHeight).Render(
		composePanelBody(
			pipelineHeaderStyle.Render("stages"),
			m.renderStageList(rightWidth-2),
			panelHeight,
		),
	)
	leftRows := strings.Count(leftPanel, "\n") + 1
	rightRows := strings.Count(rightPanel, "\n") + 1
	t.Logf("leftWidth=%d rightWidth=%d panelHeight=%d", leftWidth, rightWidth, panelHeight)
	t.Logf("leftPanel rows=%d rightPanel rows=%d (expected %d)",
		leftRows, rightRows, panelHeight+2)

	stageList := m.renderStageList(rightWidth - 2)
	for i, line := range strings.Split(stageList, "\n") {
		if line != "" {
			t.Logf("  stage row %d: width=%d %q", i, lipgloss.Width(line), line)
		}
	}
	require.Equal(t, rightRows, leftRows, "boxes must match")
}

// TestRenderSmoke_PostCompletionWithReport reproduces the exact
// post-PLAN-7 user scenario: pipeline complete (phaseCompleted), so
// the right stage list has the synthetic "📊 final report" row AND a
// stage whose duration + name overflows the narrow column ("create-
// architecture 7m36s"). The user's screenshot shows the LEFT panel
// taller than the right, suggesting an asymmetric overflow.
func TestRenderSmoke_PostCompletionWithReport(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))
	m = setWindowSize(t, &m, 90, 40)

	// Mark stages as done with a long duration string to force wrapping
	// in the narrow right column.
	m.stages[0].state = stateDone
	m.stages[0].startedAt = time.Now().Add(-(7*time.Minute + 36*time.Second))
	m.stages[0].endedAt = time.Now()
	m.stages[1].state = stateDone
	m.stages[1].startedAt = time.Now().Add(-30 * time.Second)
	m.stages[1].endedAt = time.Now()

	// Long-bodied events on stage[0] — the realistic interactive
	// flow from the screenshot has Pre/Post pairs.
	pre := loadHookFixture(t, "pretooluse_read.json")
	pre.Step = fakeAlphaStep
	post := loadHookFixture(t, "posttooluse_read_ok.json")
	post.Step = fakeAlphaStep
	for range 50 {
		for _, h := range []orchestrator.HookEvent{pre, post} {
			res, _ := m.Update(hookEventMsg{hook: h})
			m, _ = res.(pipelineModel)
		}
	}
	res, _ := m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)

	// Transition to phaseCompleted; cursor lands on report row.
	res, _ = m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)
	// Move cursor BACK to stage 0 so the left panel shows its events
	// — that matches the user's screenshot (header reads stage name).
	m.cursorIdx = 0

	rightWidth := max(m.width/3, stageListWidthMin)
	leftWidth := max(m.width-rightWidth-panelBorderOverhead, eventPanelWidthMin)
	panelHeight := max(m.height-statusRowReserve, panelHeightMin)

	leftPanel := pipelinePanelStyle.Width(leftWidth).Height(panelHeight).Render(
		composePanelBody(
			pipelineHeaderStyle.Render(m.renderHeader()),
			m.renderEventPanel(leftWidth-panelBorderOverhead, panelHeight-headerRowReserve),
			panelHeight,
		),
	)
	rightPanel := pipelinePanelStyle.Width(rightWidth).Height(panelHeight).Render(
		composePanelBody(
			pipelineHeaderStyle.Render("stages"),
			m.renderStageList(rightWidth-2),
			panelHeight,
		),
	)
	leftRows := strings.Count(leftPanel, "\n") + 1
	rightRows := strings.Count(rightPanel, "\n") + 1
	t.Logf("leftWidth=%d rightWidth=%d panelHeight=%d", leftWidth, rightWidth, panelHeight)
	t.Logf("leftPanel rows=%d rightPanel rows=%d (expected %d)",
		leftRows, rightRows, panelHeight+2)

	// Inspect the stage list specifically for wraps.
	stageList := m.renderStageList(rightWidth - 2)
	for i, line := range strings.Split(stageList, "\n") {
		t.Logf("  stage list row %d: width=%d %q", i, lipgloss.Width(line), line)
	}
	require.Equal(t, rightRows, leftRows, "boxes must match")
}

// TestRenderSmoke_LeftRightSameHeight asserts the actual View()
// output produces a left and right panel of identical rendered
// height. Counts visible rows by stripping lipgloss styling and
// splitting on newlines.
func TestRenderSmoke_LeftRightSameHeight(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 90, 40)
	m = populateEvents(t, &m, "alpha", 200)

	rightWidth := max(m.width/3, stageListWidthMin)
	leftWidth := max(m.width-rightWidth-panelBorderOverhead, eventPanelWidthMin)
	panelHeight := max(m.height-statusRowReserve, panelHeightMin)

	header := m.renderHeader()
	leftPanel := pipelinePanelStyle.Width(leftWidth).Height(panelHeight).Render(
		composePanelBody(
			pipelineHeaderStyle.Render(header),
			m.renderEventPanel(leftWidth-panelBorderOverhead, panelHeight-headerRowReserve),
			panelHeight,
		),
	)
	rightPanel := pipelinePanelStyle.Width(rightWidth).Height(panelHeight).Render(
		composePanelBody(
			pipelineHeaderStyle.Render("stages"),
			m.renderStageList(rightWidth-2),
			panelHeight,
		),
	)
	leftRows := strings.Count(leftPanel, "\n") + 1
	rightRows := strings.Count(rightPanel, "\n") + 1
	t.Logf("leftWidth=%d rightWidth=%d panelHeight=%d", leftWidth, rightWidth, panelHeight)
	t.Logf("leftPanel rows=%d rightPanel rows=%d (expected panelHeight+2=%d)",
		leftRows, rightRows, panelHeight+2)
	// Also probe what lipgloss thinks of one event line visual width
	if len(m.stages[0].events) > 0 {
		ev := m.stages[0].events[0]
		line := truncateForWidth(ev.Glyph+" "+ev.Body, leftWidth-panelBorderOverhead)
		t.Logf("sample event truncated bytes=%d visual=%d str=%q",
			len(line), lipgloss.Width(line), line)
	}
	require.Equal(t, rightRows, leftRows,
		"left and right panel boxes must render to identical row counts")
}
