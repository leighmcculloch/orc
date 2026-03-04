package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/leighmcculloch/orc/orchestrator"
	"github.com/leighmcculloch/orc/state"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

	sectionStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39")).
			MarginTop(1)

	runningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("46"))

	pendingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("226"))

	completedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("82"))

	failedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	logStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250"))
)

type model struct {
	orc      *orchestrator.Orchestrator
	events   []orchestrator.Event
	width    int
	height   int
	quitting bool
}

type tickMsg time.Time
type eventMsg orchestrator.Event

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func waitForEvent(events <-chan orchestrator.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-events
		if !ok {
			return nil
		}
		return eventMsg(evt)
	}
}

func NewModel(orc *orchestrator.Orchestrator) model {
	return model{
		orc:    orc,
		events: []orchestrator.Event{},
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), waitForEvent(m.orc.Events()))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			m.orc.Stop()
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		return m, tickCmd()
	case eventMsg:
		m.events = append(m.events, orchestrator.Event(msg))
		if len(m.events) > 50 {
			m.events = m.events[len(m.events)-50:]
		}
		return m, waitForEvent(m.orc.Events())
	}
	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return "Shutting down orc...\n"
	}

	var b strings.Builder

	b.WriteString(titleStyle.Render("🏰 Orc — Claude Code Orchestrator"))
	b.WriteString("\n")

	store := m.orc.Store()
	tasks := store.AllTasks()

	// Running tasks
	running := filterTasks(tasks, state.TaskRunning)
	b.WriteString(sectionStyle.Render(fmt.Sprintf("▶ Running (%d)", len(running))))
	b.WriteString("\n")
	if len(running) == 0 {
		b.WriteString(dimStyle.Render("  No tasks running"))
		b.WriteString("\n")
	}
	for _, t := range running {
		elapsed := ""
		if t.StartedAt != nil {
			elapsed = fmt.Sprintf(" (%s)", time.Since(*t.StartedAt).Truncate(time.Second))
		}
		b.WriteString(runningStyle.Render(fmt.Sprintf("  ● %s %s%s", t.ID, truncate(t.Prompt, 60), elapsed)))
		b.WriteString("\n")
	}

	// Pending tasks
	pending := filterTasks(tasks, state.TaskPending)
	b.WriteString(sectionStyle.Render(fmt.Sprintf("◌ Pending (%d)", len(pending))))
	b.WriteString("\n")
	if len(pending) == 0 {
		b.WriteString(dimStyle.Render("  No tasks pending"))
		b.WriteString("\n")
	}
	for _, t := range pending {
		sched := ""
		if t.Schedule != "" {
			sched = fmt.Sprintf(" [%s]", t.Schedule)
		}
		b.WriteString(pendingStyle.Render(fmt.Sprintf("  ○ %s %s%s", t.ID, truncate(t.Prompt, 60), sched)))
		b.WriteString("\n")
	}

	// Recently completed
	completed := filterRecentFinished(tasks, state.TaskCompleted, 10)
	b.WriteString(sectionStyle.Render(fmt.Sprintf("✓ Completed Recently (%d)", len(completed))))
	b.WriteString("\n")
	if len(completed) == 0 {
		b.WriteString(dimStyle.Render("  No tasks completed yet"))
		b.WriteString("\n")
	}
	for _, t := range completed {
		ago := ""
		if t.FinishedAt != nil {
			ago = fmt.Sprintf(" (%s ago)", time.Since(*t.FinishedAt).Truncate(time.Second))
		}
		b.WriteString(completedStyle.Render(fmt.Sprintf("  ✓ %s %s%s", t.ID, truncate(t.Prompt, 50), ago)))
		b.WriteString("\n")
	}

	// Failed
	failed := filterRecentFinished(tasks, state.TaskFailed, 5)
	if len(failed) > 0 {
		b.WriteString(sectionStyle.Render(fmt.Sprintf("✗ Failed (%d)", len(failed))))
		b.WriteString("\n")
		for _, t := range failed {
			b.WriteString(failedStyle.Render(fmt.Sprintf("  ✗ %s %s: %s", t.ID, truncate(t.Prompt, 40), t.Error)))
			b.WriteString("\n")
		}
	}

	// Recent events / log
	b.WriteString(sectionStyle.Render("📋 Activity Log"))
	b.WriteString("\n")
	start := 0
	if len(m.events) > 15 {
		start = len(m.events) - 15
	}
	if start >= len(m.events) {
		b.WriteString(dimStyle.Render("  No activity yet"))
		b.WriteString("\n")
	}
	for _, evt := range m.events[start:] {
		icon := "·"
		switch evt.Type {
		case orchestrator.EventTaskAdded:
			icon = "+"
		case orchestrator.EventTaskStarted:
			icon = "▶"
		case orchestrator.EventTaskCompleted:
			icon = "✓"
		case orchestrator.EventTaskFailed:
			icon = "✗"
		case orchestrator.EventTaskRemoved:
			icon = "-"
		}
		ts := evt.Time.Format("15:04:05")
		b.WriteString(logStyle.Render(fmt.Sprintf("  %s %s %s %s", ts, icon, evt.TaskID, truncate(evt.Message, 60))))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("Press q to quit"))
	b.WriteString("\n")

	return b.String()
}

func filterTasks(tasks []state.Task, status state.TaskStatus) []state.Task {
	var result []state.Task
	for _, t := range tasks {
		if t.Status == status {
			result = append(result, t)
		}
	}
	return result
}

func filterRecentFinished(tasks []state.Task, status state.TaskStatus, limit int) []state.Task {
	var result []state.Task
	for _, t := range tasks {
		if t.Status == status {
			result = append(result, t)
		}
	}
	// Take last N
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func Run(orc *orchestrator.Orchestrator) error {
	p := tea.NewProgram(NewModel(orc), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
