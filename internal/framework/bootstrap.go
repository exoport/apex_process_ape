package framework

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TUIBootstrapper drives a two-phase Bubble Tea form to collect
// project_name + extensions on first install. Returns
// ErrBootstrapCancelled if the user presses Esc or Ctrl+C.
type TUIBootstrapper struct{}

// Bootstrap implements Bootstrapper.
func (TUIBootstrapper) Bootstrap(ctx context.Context, defaultProjectName string, extensions []Extension) (BootstrapValues, error) {
	model := newBootstrapModel(defaultProjectName, extensions)
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen())
	final, err := program.Run()
	if err != nil {
		return BootstrapValues{}, fmt.Errorf("bootstrap TUI: %w", err)
	}
	bm, ok := final.(*bootstrapModel)
	if !ok {
		return BootstrapValues{}, errors.New("bootstrap TUI: unexpected final model")
	}
	if bm.cancelled {
		return BootstrapValues{}, ErrBootstrapCancelled
	}
	if !bm.submitted {
		return BootstrapValues{}, ErrBootstrapCancelled
	}
	return bm.Values(), nil
}

type bootstrapPhase int

const (
	phaseProjectName bootstrapPhase = iota
	phaseExtensions
)

type bootstrapModel struct {
	phase      bootstrapPhase
	input      textinput.Model
	extensions []Extension
	selected   []bool
	cursor     int
	submitted  bool
	cancelled  bool
}

func newBootstrapModel(defaultProjectName string, extensions []Extension) *bootstrapModel {
	ti := textinput.New()
	ti.Placeholder = defaultProjectName
	ti.SetValue(defaultProjectName)
	ti.Focus()
	ti.CharLimit = 64
	ti.Width = 40
	return &bootstrapModel{
		phase:      phaseProjectName,
		input:      ti,
		extensions: extensions,
		selected:   make([]bool, len(extensions)),
	}
}

// Init implements tea.Model.
func (m *bootstrapModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m *bootstrapModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if keyMsg.Type == tea.KeyCtrlC || keyMsg.Type == tea.KeyEsc {
		m.cancelled = true
		return m, tea.Quit
	}
	switch m.phase {
	case phaseProjectName:
		return m.updateProjectName(keyMsg)
	case phaseExtensions:
		return m.updateExtensions(keyMsg)
	}
	return m, nil
}

func (m *bootstrapModel) updateProjectName(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEnter {
		// Snap empty input to the placeholder default.
		if strings.TrimSpace(m.input.Value()) == "" {
			m.input.SetValue(m.input.Placeholder)
		}
		m.phase = phaseExtensions
		m.input.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *bootstrapModel) updateExtensions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type { //nolint:exhaustive // bubbletea defines ~100 KeyType values; we only react to a few — others are intentional no-ops
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyDown:
		if m.cursor < len(m.extensions)-1 {
			m.cursor++
		}
	case tea.KeySpace:
		m.selected[m.cursor] = !m.selected[m.cursor]
	case tea.KeyEnter:
		m.submitted = true
		return m, tea.Quit
	}
	return m, nil
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	hintStyle   = lipgloss.NewStyle().Faint(true)
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
)

// View implements tea.Model.
func (m *bootstrapModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("APEX project bootstrap") + "\n\n")
	switch m.phase {
	case phaseProjectName:
		b.WriteString("Project name:\n")
		b.WriteString("  " + m.input.View() + "\n\n")
		b.WriteString(hintStyle.Render("Default suggested from go.mod or directory name. Enter to continue, Esc to cancel."))
	case phaseExtensions:
		b.WriteString("Project name: " + m.input.Value() + "\n\n")
		b.WriteString("Extensions to enable:\n")
		for i, ext := range m.extensions {
			cursor := "  "
			if i == m.cursor {
				cursor = cursorStyle.Render("▸ ")
			}
			mark := "[ ]"
			if m.selected[i] {
				mark = "[x]"
			}
			b.WriteString(fmt.Sprintf("%s%s %-18s %s\n", cursor, mark, ext.ID, hintStyle.Render(ext.Description)))
		}
		b.WriteString("\n")
		b.WriteString(hintStyle.Render("Space toggle, ↑↓ navigate, Enter to confirm, Esc to cancel."))
	}
	return b.String()
}

// Values returns the bootstrap values the user submitted. Only valid
// after Update returned tea.Quit with submitted=true.
func (m *bootstrapModel) Values() BootstrapValues {
	bv := BootstrapValues{ProjectName: strings.TrimSpace(m.input.Value())}
	for i, sel := range m.selected {
		if sel {
			bv.Extensions = append(bv.Extensions, m.extensions[i].ID)
		}
	}
	return bv
}
