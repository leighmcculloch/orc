package tui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leighmcculloch/orc/config"
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

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("236"))

	detailTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("205"))
)

type viewMode int

const (
	viewDashboard viewMode = iota
	viewTaskOutput
)

type model struct {
	orc      *orchestrator.Orchestrator
	events   []orchestrator.Event
	width    int
	height   int
	quitting bool

	mode     viewMode
	cursor   int
	taskList []state.Task // flattened task list for cursor navigation

	// Task output view
	viewTaskID   string
	outputLines  []string
	outputScroll int
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
		mode:   viewDashboard,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), waitForEvent(m.orc.Events()))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.mode {
		case viewDashboard:
			return m.updateDashboard(msg)
		case viewTaskOutput:
			return m.updateTaskOutput(msg)
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		m.refreshTaskList()
		if m.mode == viewTaskOutput {
			m.loadOutput()
		}
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

func (m *model) refreshTaskList() {
	store := m.orc.Store()
	tasks := store.AllTasks()
	var list []state.Task
	list = append(list, filterTasks(tasks, state.TaskRunning)...)
	list = append(list, filterTasks(tasks, state.TaskPending)...)
	list = append(list, filterRecentFinished(tasks, state.TaskCompleted, 10)...)
	list = append(list, filterRecentFinished(tasks, state.TaskFailed, 5)...)
	m.taskList = list
	if m.cursor >= len(m.taskList) && len(m.taskList) > 0 {
		m.cursor = len(m.taskList) - 1
	}
}

func (m model) updateDashboard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		m.orc.Stop()
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.taskList)-1 {
			m.cursor++
		}
	case "enter":
		if m.cursor < len(m.taskList) {
			m.viewTaskID = m.taskList[m.cursor].ID
			m.outputLines = nil
			m.outputScroll = 0
			m.loadOutput()
			m.mode = viewTaskOutput
		}
	}
	return m, nil
}

func (m model) updateTaskOutput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.mode = viewDashboard
		m.viewTaskID = ""
		m.outputLines = nil
	case "ctrl+c":
		m.quitting = true
		m.orc.Stop()
		return m, tea.Quit
	case "up", "k":
		if m.outputScroll > 0 {
			m.outputScroll--
		}
	case "down", "j":
		maxScroll := m.maxOutputScroll()
		if m.outputScroll < maxScroll {
			m.outputScroll++
		}
	case "G":
		m.outputScroll = m.maxOutputScroll()
	case "g":
		m.outputScroll = 0
	}
	return m, nil
}

func (m *model) loadOutput() {
	logPath := filepath.Join(config.OrcDir(), "workdirs", m.viewTaskID, "output.log")
	f, err := os.Open(logPath)
	if err != nil {
		m.outputLines = []string{"(no output yet)"}
		return
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	m.outputLines = lines

	// Auto-scroll to bottom if already at bottom
	prevMax := len(m.outputLines) - m.outputViewHeight()
	if prevMax < 0 {
		prevMax = 0
	}
	if m.outputScroll >= prevMax-1 || m.outputScroll == 0 {
		m.outputScroll = m.maxOutputScroll()
	}
}

func (m model) outputViewHeight() int {
	h := m.height - 4 // title + help + margins
	if h < 1 {
		h = 20
	}
	return h
}

func (m model) maxOutputScroll() int {
	max := len(m.outputLines) - m.outputViewHeight()
	if max < 0 {
		return 0
	}
	return max
}

func (m model) View() string {
	if m.quitting {
		return "Shutting down orc...\n"
	}
	switch m.mode {
	case viewTaskOutput:
		return m.viewTaskOutputScreen()
	default:
		return m.viewDashboardScreen()
	}
}

func (m model) viewDashboardScreen() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("orc"))
	b.WriteString("\n")

	m.refreshTaskList()

	store := m.orc.Store()
	tasks := store.AllTasks()

	running := filterTasks(tasks, state.TaskRunning)
	pending := filterTasks(tasks, state.TaskPending)
	completed := filterRecentFinished(tasks, state.TaskCompleted, 10)
	failed := filterRecentFinished(tasks, state.TaskFailed, 5)

	idx := 0

	// Running
	b.WriteString(sectionStyle.Render(fmt.Sprintf("▶ Running (%d)", len(running))))
	b.WriteString("\n")
	if len(running) == 0 {
		b.WriteString(dimStyle.Render("  No tasks running"))
		b.WriteString("\n")
	}
	for _, t := range running {
		b.WriteString(m.renderTaskLine(idx, t, runningStyle, "●"))
		b.WriteString("\n")
		idx++
	}

	// Pending
	b.WriteString(sectionStyle.Render(fmt.Sprintf("◌ Pending (%d)", len(pending))))
	b.WriteString("\n")
	if len(pending) == 0 {
		b.WriteString(dimStyle.Render("  No tasks pending"))
		b.WriteString("\n")
	}
	for _, t := range pending {
		b.WriteString(m.renderTaskLine(idx, t, pendingStyle, "○"))
		b.WriteString("\n")
		idx++
	}

	// Completed
	if len(completed) > 0 {
		b.WriteString(sectionStyle.Render(fmt.Sprintf("✓ Completed (%d)", len(completed))))
		b.WriteString("\n")
		for _, t := range completed {
			b.WriteString(m.renderTaskLine(idx, t, completedStyle, "✓"))
			b.WriteString("\n")
			idx++
		}
	}

	// Failed
	if len(failed) > 0 {
		b.WriteString(sectionStyle.Render(fmt.Sprintf("✗ Failed (%d)", len(failed))))
		b.WriteString("\n")
		for _, t := range failed {
			b.WriteString(m.renderTaskLine(idx, t, failedStyle, "✗"))
			b.WriteString("\n")
			idx++
		}
	}

	// Activity log
	b.WriteString(sectionStyle.Render("Activity"))
	b.WriteString("\n")
	start := 0
	if len(m.events) > 10 {
		start = len(m.events) - 10
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
	b.WriteString(dimStyle.Render("↑/↓ navigate • enter view output • q quit"))
	b.WriteString("\n")

	return b.String()
}

func (m model) renderTaskLine(idx int, t state.Task, style lipgloss.Style, icon string) string {
	extra := ""
	if t.Status == state.TaskRunning && t.StartedAt != nil {
		extra = fmt.Sprintf(" (%s)", time.Since(*t.StartedAt).Truncate(time.Second))
	} else if t.Schedule != "" {
		extra = fmt.Sprintf(" [%s]", t.Schedule)
	} else if t.Status == state.TaskFailed && t.Error != "" {
		extra = fmt.Sprintf(": %s", truncate(t.Error, 30))
	} else if t.FinishedAt != nil {
		extra = fmt.Sprintf(" (%s ago)", time.Since(*t.FinishedAt).Truncate(time.Second))
	}

	line := fmt.Sprintf("  %s %s %s%s", icon, t.ID, truncate(t.Prompt, 55), extra)

	if idx == m.cursor {
		return selectedStyle.Render(style.Render(line))
	}
	return style.Render(line)
}

func (m model) viewTaskOutputScreen() string {
	var b strings.Builder

	// Find task for title
	prompt := m.viewTaskID
	store := m.orc.Store()
	if t, ok := store.GetTask(m.viewTaskID); ok {
		prompt = truncate(t.Prompt, 60)
	}

	b.WriteString(detailTitleStyle.Render(fmt.Sprintf("Task %s — %s", m.viewTaskID, prompt)))
	b.WriteString("\n\n")

	viewH := m.outputViewHeight()

	if len(m.outputLines) == 0 {
		b.WriteString(dimStyle.Render("(no output yet)"))
		b.WriteString("\n")
	} else {
		start := m.outputScroll
		end := start + viewH
		if end > len(m.outputLines) {
			end = len(m.outputLines)
		}
		if start > len(m.outputLines) {
			start = len(m.outputLines)
		}
		for _, line := range m.outputLines[start:end] {
			b.WriteString(logStyle.Render(line))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render("↑/↓ scroll • g top • G bottom • q back"))
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
