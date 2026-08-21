package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type deployment struct {
	project  string
	target   string
	state    string
	branch   string
	commit   string
	author   string
	age      string
	duration string
}

// Placeholder data until the real API client lands (M1).
var fakeDeployments = []deployment{
	{"api-gateway", "production", "Ready", "main", "a1b2c3d", "shehjad", "2m ago", "48s"},
	{"web-dashboard", "preview", "Building", "feat/usage-page", "9f8e7d6", "shehjad", "just now", "—"},
	{"docs-site", "production", "Ready", "main", "5c4b3a2", "ci-bot", "1h ago", "1m 12s"},
	{"worker-cron", "preview", "Error", "fix/schedule-drift", "0aa11bb", "shehjad", "3h ago", "22s"},
	{"marketing-site", "production", "Canceled", "main", "cc00ff1", "shehjad", "1d ago", "8s"},
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).MarginBottom(1)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	stateStyle    = map[string]lipgloss.Style{
		"Ready":    lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		"Building": lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		"Error":    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		"Canceled": lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	}
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).MarginTop(1)
)

type model struct {
	cursor int
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(fakeDeployments)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			m.cursor = len(fakeDeployments) - 1
		}
	}
	return m, nil
}

func (m model) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("vercel-tui — deployments (fake data)"))
	b.WriteString("\n")

	headers := []string{"", "PROJECT", "TARGET", "STATE", "BRANCH", "COMMIT", "AUTHOR", "AGE", "DURATION"}
	b.WriteString(headerStyle.Render(row(headers...)))
	b.WriteString("\n")

	for i, d := range fakeDeployments {
		cells := []string{
			marker(i == m.cursor),
			d.project,
			d.target,
			stateStyle[d.state].Render(d.state),
			d.branch,
			d.commit,
			d.author,
			d.age,
			d.duration,
		}
		line := row(cells...)
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString(footerStyle.Render("j/k move · q quit"))
	return b.String()
}

func marker(selected bool) string {
	if selected {
		return ">"
	}
	return ""
}

func row(cells ...string) string {
	widths := []int{1, 16, 10, 9, 20, 8, 8, 9, 8}
	var b strings.Builder
	for i, c := range cells {
		b.WriteString(fmt.Sprintf("%-*s", widths[i], c))
	}
	return strings.TrimRight(b.String(), " ")
}

func main() {
	if _, err := tea.NewProgram(model{}).Run(); err != nil {
		fmt.Println("error:", err)
	}
}
