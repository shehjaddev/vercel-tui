package ui

import (
	"errors"
	"fmt"
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
	client  *api.Client
	authed  bool
	refresh time.Duration

	projectID, orgID       string
	targetFlag, branchFlag string

	mode    mode
	user    string
	teams   []api.Team // index 0 is the personal account
	teamIdx int

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

	searchFocus bool
	searchBuf   string
	search      string
	lastMatch   int
	note        string
	noteAt      time.Time

	tokenBuf string

	pending      pendingAction
	pendingDep   api.Deployment
	confirmInput string

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
type statusMsg struct{ text string }
type actionMsg struct {
	text string
	err  error
}
type errMsg struct{ err error }

type pendingAction int

const (
	pendNone pendingAction = iota
	pendCancel
	pendDelete
	pendRedeploy
	pendRollback
)

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

var clipboardTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
	{"pbcopy"},
}

func copyURL(url string) tea.Cmd {
	return func() tea.Msg {
		for _, bin := range clipboardTools {
			cmd := exec.Command(bin[0], bin[1:]...)
			cmd.Stdin = strings.NewReader(url)
			if err := cmd.Run(); err == nil {
				return statusMsg{"copied " + url}
			}
		}
		return errMsg{errors.New("no clipboard tool found (wl-copy, xclip, xsel, pbcopy)")}
	}
}

// handleConfirm runs the typed-confirmation dialog for destructive actions.
func (m Model) handleConfirm(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.pending = pendNone
		m.confirmInput = ""
	case "backspace":
		if r := []rune(m.confirmInput); len(r) > 0 {
			m.confirmInput = string(r[:len(r)-1])
		}
	case "enter":
		pa, dep := m.pending, m.pendingDep
		if pa == pendDelete && m.confirmInput != dep.Name {
			return m, nil // exact project name required
		}
		m.pending = pendNone
		m.confirmInput = ""
		return m, m.runAction(pa, dep)
	default:
		if len(key) == 1 && !m.confirmTyped() {
			m.confirmInput += key
		}
	}
	return m, nil
}

// confirmTyped reports whether the dialog wants free typing at all;
// non-destructive confirms only take enter.
func (m Model) confirmTyped() bool { return m.pending == pendDelete }

func (m Model) runAction(pa pendingAction, dep api.Deployment) tea.Cmd {
	c, team := m.client, m.teamID()
	id := dep.UID
	switch pa {
	case pendCancel:
		return func() tea.Msg {
			_, err := c.CancelDeployment(id, team)
			return actionMsg{"build canceled", err}
		}
	case pendDelete:
		return func() tea.Msg {
			err := c.DeleteDeployment(id, team)
			return actionMsg{"deployment deleted", err}
		}
	case pendRedeploy:
		name := dep.Name
		return func() tea.Msg {
			_, err := c.Redeploy(name, id, team)
			return actionMsg{"redeploy of " + name + " started", err}
		}
	case pendRollback:
		name := dep.Name
		return func() tea.Msg {
			p, err := c.ProjectByName(name, team)
			if err != nil {
				return actionMsg{"", err}
			}
			err = c.Promote(p.ID, id, team)
			return actionMsg{"promoting " + id + " to production", err}
		}
	}
	return nil
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
		if !m.noteAt.IsZero() && time.Since(m.noteAt) > 3*time.Second {
			m.note = ""
		}
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

	case statusMsg:
		m.note = msg.text
		m.noteAt = time.Now()

	case actionMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.note, m.noteAt = msg.text, time.Now()
			m.err = ""
		}
		return m, m.loadCurrent()

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

	if m.pending != pendNone {
		return m.handleConfirm(key)
	}

	if m.searchFocus {
		switch key {
		case "enter":
			m.search, m.searchFocus = m.searchBuf, false
			m.lastMatch = max(m.logTopIndex()-1, -1)
			m.searchNext()
		case "esc":
			m.searchBuf, m.searchFocus = "", false
		case "backspace":
			if r := []rune(m.searchBuf); len(r) > 0 {
				m.searchBuf = string(r[:len(r)-1])
			}
		default:
			if len(key) == 1 {
				m.searchBuf += key
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
		switch m.mode {
		case modeDeployments, modeProjects:
			m.filterFocus, m.filterBuf = true, m.filter
		case modeLogs:
			m.searchFocus, m.searchBuf = true, m.search
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
		case "x":
			if m.depCursor < len(deps) && deps[m.depCursor].Status() == "building" {
				m.pending, m.pendingDep, m.confirmInput = pendCancel, deps[m.depCursor], ""
			}
		case "D":
			if m.depCursor < len(deps) {
				m.pending, m.pendingDep, m.confirmInput = pendDelete, deps[m.depCursor], ""
			}
		case "R":
			if m.depCursor < len(deps) {
				m.pending, m.pendingDep, m.confirmInput = pendRedeploy, deps[m.depCursor], ""
			}
		case "B":
			if m.depCursor < len(deps) {
				d := deps[m.depCursor]
				if d.Status() == "ready" && d.Target == "production" {
					m.pending, m.pendingDep, m.confirmInput = pendRollback, d, ""
				}
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
		case "c":
			if m.depCursor < len(deps) {
				return m, copyURL("https://" + deps[m.depCursor].URL)
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
		case "c":
			return m, copyURL("https://" + m.detail.URL)
		case "x":
			if m.detail.Status() == "building" {
				m.pending, m.pendingDep, m.confirmInput = pendCancel, *m.detail, ""
			}
		case "D":
			m.pending, m.pendingDep, m.confirmInput = pendDelete, *m.detail, ""
		case "R":
			m.pending, m.pendingDep, m.confirmInput = pendRedeploy, *m.detail, ""
		case "B":
			if m.detail.Status() == "ready" && m.detail.Target == "production" {
				m.pending, m.pendingDep, m.confirmInput = pendRollback, *m.detail, ""
			}
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
		case "n":
			m.searchNext()
		case "c":
			if m.detail != nil {
				return m, copyURL("https://" + m.detail.URL)
			}
		}
	}
	return m, nil
}

// logTopIndex is the absolute index of the topmost visible log line.
func (m Model) logTopIndex() int {
	visible := max(m.height-6, 1)
	start := len(m.logs) - visible - m.logScroll
	if start < 0 {
		return 0
	}
	return start
}

// searchNext jumps to the next line matching the search term, wrapping.
func (m *Model) searchNext() {
	if m.search == "" || len(m.logs) == 0 {
		return
	}
	q := strings.ToLower(m.search)
	total := len(m.logs)
	visible := max(m.height-6, 1)
	maxScroll := max(total-visible, 0)
	from := m.lastMatch + 1
	for i := 0; i < total; i++ {
		idx := (from + i) % total
		if strings.Contains(strings.ToLower(m.logs[idx]), q) {
			m.lastMatch = idx
			m.logScroll = clamp(total-visible-idx, 0, maxScroll)
			m.note = fmt.Sprintf("match at line %d", idx+1)
			m.noteAt = time.Now()
			return
		}
	}
	m.note = "no match"
	m.noteAt = time.Now()
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
