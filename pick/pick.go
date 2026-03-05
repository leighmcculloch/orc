package pick

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Item struct {
	ID    string
	Label string
}

type model struct {
	items    []Item
	cursor   int
	selected Item
	done     bool
	aborted  bool
	title    string
}

func Run(title string, items []Item) (Item, bool) {
	if len(items) == 0 {
		return Item{}, false
	}
	m := model{items: items, title: title}
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return Item{}, false
	}
	final := result.(model)
	if final.aborted {
		return Item{}, false
	}
	return final.selected, final.done
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.aborted = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = m.items[m.cursor]
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).MarginBottom(1)
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	normalStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
)

func (m model) View() string {
	s := titleStyle.Render(m.title) + "\n"
	for i, item := range m.items {
		cursor := "  "
		style := normalStyle
		if i == m.cursor {
			cursor = "> "
			style = selectedStyle
		}
		s += fmt.Sprintf("%s%s\n", cursor, style.Render(item.Label))
	}
	s += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("↑/↓ navigate • enter select • esc quit")
	return s
}
