package framework

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TUIBootstrapper drives a two-phase Bubble Tea form to collect
// project_name + extensions on first install. Returns
// ErrBootstrapCancelled if the user presses Esc or Ctrl+C.
//
// Bubble Tea v2, not v1, and not for its features: v1's package init()
// asks the terminal for its background colour (OSC 11) in EVERY process
// that links it, before main runs, and waits up to 5 s for an answer when
// stdout is a tty and TERM is xterm-like. A terminal that never answers —
// a PTY nobody reads, as a test harness or a session driver gives a hook —
// cost every `ape` command 5 s, this form being the only thing in ape that
// needed Bubble Tea at all (W8b, D365). v2 has no such init: whatever it
// asks the terminal, it asks here, when the form runs.
type TUIBootstrapper struct{}

// Bootstrap implements Bootstrapper.
func (TUIBootstrapper) Bootstrap(ctx context.Context, defaultProjectName string, extensions []Extension) (BootstrapValues, error) {
	model := newBootstrapModel(defaultProjectName, extensions)
	program := tea.NewProgram(model, tea.WithContext(ctx))
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

// nameFieldWidth is the project-name field's width, in cells.
const nameFieldWidth = 40

func newBootstrapModel(defaultProjectName string, extensions []Extension) *bootstrapModel {
	ti := textinput.New()
	ti.Placeholder = defaultProjectName
	ti.SetValue(defaultProjectName)
	ti.Focus()
	ti.CharLimit = 64
	ti.SetWidth(nameFieldWidth)
	// v1's look: an unstyled prompt and a cursor in the terminal's own
	// colours, reversed. v2's defaults grey both.
	st := textinput.DefaultDarkStyles()
	st.Focused.Prompt, st.Blurred.Prompt = lipgloss.NewStyle(), lipgloss.NewStyle()
	st.Cursor.Color = nil
	ti.SetStyles(st)
	// Pre-select the standard set, so Enter on the checklist gives the
	// configuration the framework documents rather than none at all.
	selected := make([]bool, len(extensions))
	for i, e := range extensions {
		selected[i] = e.Standard
	}
	return &bootstrapModel{
		phase:      phaseProjectName,
		input:      ti,
		extensions: extensions,
		selected:   selected,
	}
}

// Init implements tea.Model.
func (m *bootstrapModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m *bootstrapModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if k := keyMsg.String(); k == "ctrl+c" || k == "esc" {
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

func (m *bootstrapModel) updateProjectName(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "enter" {
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

func (m *bootstrapModel) updateExtensions(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.extensions)-1 {
			m.cursor++
		}
	case "space":
		m.selected[m.cursor] = !m.selected[m.cursor]
	case "enter":
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

// View implements tea.Model: the form, on the alternate screen.
func (m *bootstrapModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render is the form's text.
func (m *bootstrapModel) render() string {
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
			fmt.Fprintf(&b, "%s%s %-18s %s\n", cursor, mark, ext.ID, hintStyle.Render(ext.Description))
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
