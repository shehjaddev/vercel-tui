package ui

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shehjaddev/vercel-tui/internal/api"
	"github.com/shehjaddev/vercel-tui/internal/config"
)

type mode int

const (
	modeLogin mode = iota
	modeDeployments
	modeProjects
	modeDetail
	modeLogs
)

var stateFilters = []string{"", "building", "ready", "error", "canceled", "queued"}

type Model struct {
	client *api.Client
	authed bool
	refresh time.Duration

	projectID, orgID   string
	targetFlag, branchFlag string

	mode      mode
	user      string
	teams     []api.Team // index 0 is the personal account
	teamIdx   int

	deps       []api.Deployment
	depCursor  int
	projects   []api.Project
	projCursor int
	detail     *api.Deployment
	logs       []string
	logScroll  int // lines back from the bottom; 0 means following

	filterFocus bool
	filterBuf   string
	filter      string
	stateIdx    int

	tokenBuf string

	teamSel    bool
	teamCursor int
	help       bool

	err       string
	throttled bool
	loading   bool
	lastLoad  time.Time

	width, height int
}

type tickMsg struct{}
type teamsMsg struct {
	user  string
	teams []api.Team
}
type depsMsg struct{ deps []api.Deployment }
type projectsMsg struct{ projects []api.Project }
type detailMsg struct{ d *api.Deployment }
type logsMsg struct{ lines []string }
type tokenOkMsg struct {
	user  string
	token string
}
type errMsg struct{ err error }

func New(client *api.Client, authed bool, refresh time.Duration, link *config.ProjectLink, target, branch string) Model {
	m := Model{
		client:     client,
		authed:     authed,
		refresh:    refresh,
		targetFlag: target,
		branchFlag: branch,
		mode:       modeDeployments,
	}
	if link != nil {
		m.projectID = link.ProjectID
		m.orgID = link.OrgID
	}
	if !authed {
		m.mode = modeLogin
	}
	return m
}

func (m Model) Init() tea.Cmd {
	if m.mode == modeLogin {
		return nil
	}
	return tea.Batch(fetchTeams(m.client), schedule(2*time.Second))
}

func schedule(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func fetchTeams(c *api.Client) tea.Cmd {
	return func() tea.Msg {
		u, err := c.User()
		if err != nil {
			return errMsg{err}
		}
		teams, _ := c.Teams() // tolerate failure; personal scope still works
		return teamsMsg{user: u.Username, teams: teams}
	}
}

func (m Model) teamID() string {
	if m.teamIdx > 0 && m.teamIdx < len(m.teams) {
		return m.teams[m.teamIdx].ID
	}
	if m.orgID != "" && m.teamIdx == 0 {
		return ""
	}
	return ""
}

func (m Model) teamName() string {
	if len(m.teams) == 0 {
		return "…"
	}
	return m.teams[m.teamIdx].Name
}

func (m Model) fetchDeps() tea.Cmd {
	m.loading = true
	c, team := m.client, m.teamID()
	project, target := m.projectID, m.targetFlag
	return func() tea.Msg {
		deps, err := c.Deployments(project, team, target, 100)
		if err != nil {
			return errMsg{err}
		}
		return depsMsg{deps}
	}
}

func (m Model) fetchProjects() tea.Cmd {
	m.loading = true
	c, team := m.client, m.teamID()
	return func() tea.Msg {
		ps, err := c.Projects(team, 200)
		if err != nil {
			return errMsg{err}
		}
		return projectsMsg{ps}
	}
}

func (m Model) fetchDetail(d api.Deployment) tea.Cmd {
	m.loading = true
	c, id, team := m.client, d.UID, m.teamID()
	return func() tea.Msg {
		full, err := c.Deployment(id, team)
		if err != nil {
			return errMsg{err}
		}
		return detailMsg{full}
	}
}

func (m Model) fetchLogs() tea.Cmd {
	c, team := m.client, m.teamID()
	id := ""
	if m.detail != nil {
		id = m.detail.UID
	}
	return func() tea.Msg {
		events, err := c.Events(id, team)
		if err != nil {
			return errMsg{err}
		}
		var lines []string
		for _, e := range events {
			for _, ln := range strings.Split(strings.TrimRight(e.Payload.Text, "\n"), "\n") {
				if ln != "" {
					lines = append(lines, ln)
				}
			}
		}
		return logsMsg{lines}
	}
}

func validateToken(token string) tea.Cmd {
	return func() tea.Msg {
		u, err := api.New(token).User()
		if err != nil {
			return errMsg{err}
		}
		if err := config.StoreToken(token); err != nil {
			return errMsg{err}
		}
		return tokenOkMsg{user: u.Username, token: token}
	}
}

func openBrowser(url string) tea.Cmd {
	return func() tea.Msg {
		bin := os.Getenv("BROWSER")
		if bin == "" {
			bin = "xdg-open"
		}
		exec.Command(bin, url).Start()
		return nil
	}
}

func (m Model) loadCurrent() tea.Cmd {
	switch m.mode {
	case modeDeployments:
		return m.fetchDeps()
	case modeProjects:
		return m.fetchProjects()
	case modeLogs:
		return m.fetchLogs()
	}
	return nil
}

func (m Model) nextInterval() time.Duration {
	if m.mode == modeLogs {
		return 2 * time.Second
	}
	if m.refresh == 0 {
		return 0
	}
	for _, d := range m.deps {
		if d.Status() == "building" {
			return min(m.refresh, 2*time.Second)
		}
	}
	return m.refresh
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tickMsg:
		var cmds []tea.Cmd
		if m.authed && !m.loading {
			if cmd := m.loadCurrent(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if next := m.nextInterval(); next > 0 {
			cmds = append(cmds, schedule(next))
		}
		return m, tea.Batch(cmds...)

	case teamsMsg:
		m.user = msg.user
		m.teams = append([]api.Team{{Name: m.user + " (personal)"}}, msg.teams...)
		m.err = ""
		return m, m.loadCurrent()

	case depsMsg:
		m.deps = msg.deps
		m.loading, m.throttled, m.err = false, false, ""
		m.lastLoad = time.Now()
		m.depCursor = clamp(m.depCursor, 0, max(len(m.visibleDeps())-1, 0))

	case projectsMsg:
		m.projects = msg.projects
		m.loading, m.throttled, m.err = false, false, ""
		m.lastLoad = time.Now()
		m.projCursor = clamp(m.projCursor, 0, max(len(m.projects)-1, 0))

	case detailMsg:
		m.detail = msg.d
		m.loading, m.throttled, m.err = false, false, ""

	case logsMsg:
		m.logs = msg.lines
		m.loading, m.throttled, m.err = false, false, ""

	case tokenOkMsg:
		m.client = api.New(msg.token)
		m.authed = true
		m.user = msg.user
		m.teams = []api.Team{{Name: msg.user + " (personal)"}}
		m.mode = modeDeployments
		m.tokenBuf = ""
		return m, tea.Batch(fetchTeams(m.client), m.fetchDeps())

	case errMsg:
		m.loading = false
		if errors.Is(msg.err, api.ErrThrottled) {
			m.throttled = true
		} else {
			m.err = msg.err.Error()
		}

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.mode == modeLogin {
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.tokenBuf != "" {
				return m, validateToken(m.tokenBuf)
			}
		case "o":
			return m, openBrowser("https://vercel.com/account/tokens")
		case "backspace":
			if r := []rune(m.tokenBuf); len(r) > 0 {
				m.tokenBuf = string(r[:len(r)-1])
			}
		default:
			if len(key) == 1 {
				m.tokenBuf += key
			}
		}
		return m, nil
	}

	if m.filterFocus {
		switch key {
		case "enter":
			m.filter, m.filterFocus, m.depCursor, m.projCursor = m.filterBuf, false, 0, 0
		case "esc":
			m.filterBuf, m.filterFocus = "", false
		case "backspace":
			if r := []rune(m.filterBuf); len(r) > 0 {
				m.filterBuf = string(r[:len(r)-1])
			}
		default:
			if len(key) == 1 {
				m.filterBuf += key
			}
		}
		return m, nil
	}

	if m.help {
		m.help = false
		return m, nil
	}

	if m.teamSel {
		switch key {
		case "esc":
			m.teamSel = false
		case "j", "down":
			if m.teamCursor < len(m.teams)-1 {
				m.teamCursor++
			}
		case "k", "up":
			if m.teamCursor > 0 {
				m.teamCursor--
			}
		case "enter":
			if m.teamCursor != m.teamIdx {
				m.teamIdx = m.teamCursor
				m.depCursor, m.projCursor = 0, 0
				m.teamSel = false
				return m, m.loadCurrent()
			}
			m.teamSel = false
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
	case "1":
		m.mode = modeDeployments
		return m, m.loadCurrent()
	case "2":
		m.mode = modeProjects
		return m, m.loadCurrent()
	case "t":
		if len(m.teams) > 1 {
			m.teamSel, m.teamCursor = true, m.teamIdx
		}
		return m, nil
	case "r":
		return m, m.loadCurrent()
	case "/":
		if m.mode == modeDeployments || m.mode == modeProjects {
			m.filterFocus, m.filterBuf = true, m.filter
		}
		return m, nil
	case "s":
		if m.mode == modeDeployments {
			m.stateIdx = (m.stateIdx + 1) % len(stateFilters)
			m.depCursor = 0
		}
		return m, nil
	}

	switch m.mode {
	case modeDeployments:
		deps := m.visibleDeps()
		switch key {
		case "j", "down":
			m.depCursor = clamp(m.depCursor+1, 0, len(deps)-1)
		case "k", "up":
			m.depCursor = clamp(m.depCursor-1, 0, len(deps)-1)
		case "g", "home":
			m.depCursor = 0
		case "G", "end":
			m.depCursor = len(deps) - 1
		case "enter":
			if m.depCursor < len(deps) {
				return m, m.fetchDetail(deps[m.depCursor])
			}
		case "l":
			if m.depCursor < len(deps) {
				d := deps[m.depCursor]
				m.detail = &d
				m.logs, m.logScroll = nil, 0
				m.mode = modeLogs
				return m, m.fetchLogs()
			}
		case "o":
			if m.depCursor < len(deps) {
				return m, openBrowser("https://" + deps[m.depCursor].URL)
			}
		}

	case modeProjects:
		switch key {
		case "j", "down":
			m.projCursor = clamp(m.projCursor+1, 0, len(m.projects)-1)
		case "k", "up":
			m.projCursor = clamp(m.projCursor-1, 0, len(m.projects)-1)
		case "enter":
			if m.projCursor < len(m.projects) {
				m.projectID = m.projects[m.projCursor].ID
				m.mode = modeDeployments
				m.depCursor = 0
				return m, m.fetchDeps()
			}
		}

	case modeDetail:
		switch key {
		case "esc":
			m.mode = modeDeployments
		case "l":
			m.logs, m.logScroll = nil, 0
			m.mode = modeLogs
			return m, m.fetchLogs()
		case "o":
			return m, openBrowser("https://" + m.detail.URL)
		}

	case modeLogs:
		maxScroll := max(len(m.logs)-(m.height-6), 0)
		switch key {
		case "esc":
			m.mode = modeDetail
			if m.detail == nil {
				m.mode = modeDeployments
			}
		case "j", "down":
			m.logScroll = clamp(m.logScroll-1, 0, maxScroll)
		case "k", "up":
			m.logScroll = clamp(m.logScroll+1, 0, maxScroll)
		case "pgdown":
			m.logScroll = clamp(m.logScroll-(m.height/2), 0, maxScroll)
		case "pgup":
			m.logScroll = clamp(m.logScroll+(m.height/2), 0, maxScroll)
		case "G", "end":
			m.logScroll = 0
		case "g", "home":
			m.logScroll = maxScroll
		}
	}
	return m, nil
}

func (m Model) visibleDeps() []api.Deployment {
	var out []api.Deployment
	state := strings.ToLower(stateFilters[m.stateIdx])
	q := strings.ToLower(m.filter)
	for _, d := range m.deps {
		if state != "" && d.Status() != state {
			continue
		}
		if m.branchFlag != "" && d.Branch() != m.branchFlag {
			continue
		}
		if q != "" {
			hay := strings.ToLower(d.Name + " " + d.Branch() + " " + d.SHA() + " " +
				d.Creator.Username + " " + d.URL + " " + d.Message())
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, d)
	}
	return out
}
