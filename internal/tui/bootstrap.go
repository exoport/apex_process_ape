package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/diegosz/apex_process_ape/internal/trait"
)

// BootstrapConfig holds the result of the TUI session.
type BootstrapConfig struct {
	Traits              []string
	ConflictResolutions map[string]string
}

var (
	selectedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	unselectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	headerStyle     = lipgloss.NewStyle().Bold(true).Underline(true).MarginBottom(1)
	hintStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	borderStyle     = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			BorderForeground(lipgloss.Color("62"))
)

const (
	keyCtrlC = "ctrl+c"
	keyEnter = "enter"
	keyEsc   = "esc"
)

type screen int

const (
	screenSelect screen = iota
	screenConflict
	screenSummary
)

type model struct {
	catalog   *trait.Catalog
	items     []traitItem
	cursor    int
	screen    screen
	conflicts []trait.Conflict
	confirmed bool
	cancelled bool
	err       error
}

type traitItem struct {
	ref      trait.TraitRef
	selected bool
}

func initialModel(catalog *trait.Catalog) model {
	items := make([]traitItem, len(catalog.Traits))
	for i, t := range catalog.Traits {
		items[i] = traitItem{ref: t}
	}
	return model{
		catalog: catalog,
		items:   items,
		screen:  screenSelect,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch m.screen {
		case screenSelect:
			return m.updateSelect(km)
		case screenConflict:
			return m.updateConflict(km)
		case screenSummary:
			return m.updateSummary(km)
		}
	}
	return m, nil
}

func (m model) updateSelect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyCtrlC, "q", keyEsc:
		m.cancelled = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case " ":
		m.items[m.cursor].selected = !m.items[m.cursor].selected
	case keyEnter:
		var selected []string
		for _, item := range m.items {
			if item.selected {
				selected = append(selected, item.ref.Name)
			}
		}
		if len(selected) == 0 {
			return m, nil
		}

		catalog, err := trait.LoadCatalog()
		if err != nil {
			m.err = err
			m.cancelled = true
			return m, tea.Quit
		}
		resolver := trait.NewResolver(catalog)
		result, err := resolver.Resolve(selected)
		if err != nil {
			m.err = err
			m.cancelled = true
			return m, tea.Quit
		}

		m.conflicts = result.Conflicts
		if len(m.conflicts) > 0 {
			m.screen = screenConflict
		} else {
			m.screen = screenSummary
		}
	}
	return m, nil
}

func (m model) updateConflict(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyCtrlC, "q", keyEsc:
		m.cancelled = true
		return m, tea.Quit
	case "b":
		m.screen = screenSelect
	case keyEnter, "c":
		m.screen = screenSummary
	}
	return m, nil
}

func (m model) updateSummary(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyCtrlC, "q", keyEsc:
		m.cancelled = true
		return m, tea.Quit
	case "b":
		m.screen = screenSelect
	case keyEnter, "y":
		m.confirmed = true
		return m, tea.Quit
	case "n":
		m.cancelled = true
		return m, tea.Quit
	}
	return m, nil
}

func (m model) View() string {
	switch m.screen {
	case screenSelect:
		return m.viewSelect()
	case screenConflict:
		return m.viewConflict()
	case screenSummary:
		return m.viewSummary()
	}
	return ""
}

func (m model) viewSelect() string {
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("APE Bootstrap — Select Traits"))
	sb.WriteString("\n\n")

	for i, item := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = "→ "
		}

		checkbox := "[ ]"
		if item.selected {
			checkbox = "[x]"
		}

		line := fmt.Sprintf("%s%s %s", cursor, checkbox, item.ref.Name)
		if item.ref.Description != "" {
			line += "  " + hintStyle.Render("— "+item.ref.Description)
		}

		if item.selected {
			sb.WriteString(selectedStyle.Render(line))
		} else {
			sb.WriteString(unselectedStyle.Render(line))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(hintStyle.Render("↑/↓ move  space toggle  enter confirm  q quit"))
	return borderStyle.Render(sb.String())
}

func (m model) viewConflict() string {
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("Conflicts Detected"))
	sb.WriteString("\n\n")

	for _, c := range m.conflicts {
		fmt.Fprintf(&sb, "  category %q is owned by:\n", c.Category)
		for _, o := range c.Owners {
			fmt.Fprintf(&sb, "    - %s\n", o)
		}
		sb.WriteString("\n")
	}

	sb.WriteString(hintStyle.Render("Conflicts will be resolved using the --on-conflict strategy.\n"))
	sb.WriteString("\n")
	sb.WriteString(hintStyle.Render("enter/c continue  b back  q quit"))
	return borderStyle.Render(sb.String())
}

func (m model) viewSummary() string {
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("Summary"))
	sb.WriteString("\n\n")

	sb.WriteString("Selected traits:\n")
	for _, item := range m.items {
		if item.selected {
			fmt.Fprintf(&sb, "  - %s\n", item.ref.Name)
		}
	}
	sb.WriteString("\n")
	sb.WriteString("Proceed with bootstrap? [y/n/b]\n")
	sb.WriteString(hintStyle.Render("y confirm  n cancel  b back"))
	return borderStyle.Render(sb.String())
}

// RunBootstrap launches the TUI and returns the user's selections.
func RunBootstrap(catalog *trait.Catalog) (*BootstrapConfig, error) {
	m := initialModel(catalog)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return nil, err
	}

	fm, ok := finalModel.(model)
	if !ok {
		return nil, errors.New("unexpected model type")
	}

	if fm.cancelled {
		return nil, errors.New("bootstrap cancelled")
	}

	if fm.err != nil {
		return nil, fm.err
	}

	var selected []string
	for _, item := range fm.items {
		if item.selected {
			selected = append(selected, item.ref.Name)
		}
	}

	return &BootstrapConfig{
		Traits:              selected,
		ConflictResolutions: map[string]string{},
	}, nil
}
