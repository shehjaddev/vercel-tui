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
	modeEnvs
	modeDomains
)

var stateFilters = []string{"", "building", "ready", "error", "canceled", "queued"}

var targetPresets = []struct {
	label  string
	values []string
}{
	{"production", []string{"production"}},
	{"preview", []string{"preview"}},
	{"development", []string{"development"}},
	{"all", []string{"production", "preview", "development"}},
}

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
	grouped    bool   // default: one row per project, expandable
	expanded   string // project name currently expanded ("" = none)
	projects   []api.Project
	projCursor int
	detail     *api.Deployment
	logs       []string
	logScroll  int // lines back from the bottom; 0 means following

	envProject api.Project
	envs       []api.EnvVar
	envCursor  int
	envForm    bool
	envKey     string
	envValue   string
	envField   int // 0 = key, 1 = value
	envPreset  int
	envEditID  string // non-empty while editing an existing var
	domains    []api.Domain

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
	pendingEnv   api.EnvVar
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
type actionMsg struct {
	text   string
	err    error
	reload bool // whether success should trigger a data refresh
}
type envsMsg struct{ envs []api.EnvVar }
type domainsMsg struct{ domains []api.Domain }
type errMsg struct{ err error }

type pendingAction int

const (
	pendNone pendingAction = iota
	pendCancel
	pendDelete
	pendRedeploy
	pendRollback
	pendDeleteEnv
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
	m.grouped = true
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

func (m Model) fetchEnvs() tea.Cmd {
	m.loading = true
	c, team, project := m.client, m.teamID(), m.envProject.ID
	return func() tea.Msg {
		envs, err := c.EnvVars(project, team)
		if err != nil {
			return errMsg{err}
		}
		return envsMsg{envs}
	}
}

func (m Model) fetchDomains() tea.Cmd {
	m.loading = true
	c, team, project := m.client, m.teamID(), m.projectID
	return func() tea.Msg {
		domains, err := c.ProjectDomains(project, team)
		if err != nil {
			domains, err = c.TeamDomains(team)
			if err != nil {
				return errMsg{err}
			}
		}
		return domainsMsg{domains}
	}
}

func (m Model) submitEnv() tea.Cmd {
	c, team := m.client, m.teamID()
	project := m.envProject.ID
	key, value := m.envKey, m.envValue
	targets := targetPresets[m.envPreset].values
	editID := m.envEditID
	return func() tea.Msg {
		var err error
		if editID != "" {
			err = c.UpdateEnvValue(project, team, editID, value, targets)
		} else {
			err = c.CreateEnv(project, team, key, value, targets)
		}
		text := "env var saved"
		if editID != "" {
			text = "env var updated"
		}
		return actionMsg{text: text, err: err, reload: true}
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
				return actionMsg{text: "copied " + url}
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
		pa := m.pending
		required := ""
		if pa == pendDelete {
			required = m.pendingDep.Name
		} else if pa == pendDeleteEnv {
			required = m.pendingEnv.Key
		}
		if required != "" && m.confirmInput != required {
			return m, nil // exact name/key required
		}
		m.pending = pendNone
		m.confirmInput = ""
		if pa == pendDeleteEnv {
			return m, m.runEnvDelete()
		}
		return m, m.runAction(pa, m.pendingDep)
	default:
		if len(key) == 1 {
			m.confirmInput += key
		}
	}
	return m, nil
}

func (m Model) runEnvDelete() tea.Cmd {
	c, team := m.client, m.teamID()
	project, key := m.envProject.ID, m.pendingEnv.Key
	id := m.pendingEnv.ID
	return func() tea.Msg {
		err := c.DeleteEnv(project, team, id)
		return actionMsg{text: "deleted " + key, err: err, reload: true}
	}
}

func (m Model) runAction(pa pendingAction, dep api.Deployment) tea.Cmd {
	c, team := m.client, m.teamID()
	id := dep.UID
	switch pa {
	case pendCancel:
		return func() tea.Msg {
			_, err := c.CancelDeployment(id, team)
			return actionMsg{text: "build canceled", err: err, reload: true}
		}
	case pendDelete:
		return func() tea.Msg {
			err := c.DeleteDeployment(id, team)
			return actionMsg{text: "deployment deleted", err: err, reload: true}
		}
	case pendRedeploy:
		name := dep.Name
		target := dep.Target // keep production redeploys in production
		return func() tea.Msg {
			var git *api.GitSource
			if ref := dep.Branch(); ref != "" {
				p, err := c.ProjectByName(name, team)
				if err != nil {
					return errMsg{err}
				}
				if p.Link.Repo != "" {
					git = &api.GitSource{Type: strings.ToLower(p.Link.Type), Org: p.Link.Org, Repo: p.Link.Repo, Ref: ref}
				}
			}
			_, err := c.Redeploy(name, id, team, git, target)
			return actionMsg{text: "redeploy of " + name + " started", err: err, reload: true}
		}
	case pendRollback:
		name := dep.Name
		return func() tea.Msg {
			p, err := c.ProjectByName(name, team)
			if err != nil {
				return errMsg{err}
			}
			err = c.Promote(p.ID, id, team)
			return actionMsg{text: "promoting " + id + " to production", err: err, reload: true}
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
	case modeEnvs:
		return m.fetchEnvs()
	case modeDomains:
		return m.fetchDomains()
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
		m.loading, m.throttled = false, false
		m.lastLoad = time.Now()
		m.depCursor = clamp(m.depCursor, 0, max(len(m.displayRows())-1, 0))

	case projectsMsg:
		m.projects = msg.projects
		m.loading, m.throttled = false, false
		m.lastLoad = time.Now()
		m.projCursor = clamp(m.projCursor, 0, max(len(m.projects)-1, 0))

	case envsMsg:
		m.envs = msg.envs
		m.loading, m.throttled = false, false
		m.lastLoad = time.Now()
		m.envCursor = clamp(m.envCursor, 0, max(len(m.envs)-1, 0))

	case domainsMsg:
		m.domains = msg.domains
		m.loading, m.throttled = false, false
		m.lastLoad = time.Now()

	case detailMsg:
		m.detail = msg.d
		m.loading, m.throttled = false, false
		if m.mode == modeDeployments {
			m.mode = modeDetail
		}

	case logsMsg:
		m.logs = msg.lines
		m.loading, m.throttled = false, false

	case tokenOkMsg:
		m.client = api.New(msg.token)
		m.authed = true
		m.user = msg.user
		m.teams = []api.Team{{Name: msg.user + " (personal)"}}
		m.mode = modeDeployments
		m.tokenBuf = ""
		return m, tea.Batch(fetchTeams(m.client), m.fetchDeps())

	case actionMsg:
		m.loading = false
		if msg.err != nil {
			if errors.Is(msg.err, api.ErrThrottled) {
				m.throttled = true
			} else {
				m.err = msg.err.Error()
			}
			return m, m.loadCurrent()
		}
		m.note, m.noteAt = msg.text, time.Now()
		if !msg.reload {
			return m, nil
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
	m.err = "" // any keypress dismisses an action error

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

	if m.envForm {
		switch key {
		case "esc":
			m.envForm, m.envKey, m.envValue, m.envField, m.envEditID = false, "", "", 0, ""
		case "tab":
			if m.envEditID == "" {
				m.envField = (m.envField + 1) % 2
			}
		case "t":
			m.envPreset = (m.envPreset + 1) % len(targetPresets)
		case "enter":
			if m.envField == 0 && m.envEditID == "" {
				if m.envKey != "" {
					m.envField = 1
				}
			} else if m.envValue != "" {
				m.envForm = false
				return m, m.submitEnv()
			}
		case "backspace":
			buf := &m.envKey
			if m.envField == 1 || m.envEditID != "" {
				buf = &m.envValue
			}
			if r := []rune(*buf); len(r) > 0 {
				*buf = string(r[:len(r)-1])
			}
		default:
			if len(key) == 1 {
				if m.envField == 0 && m.envEditID == "" {
					m.envKey += key
				} else {
					m.envValue += key
				}
			}
		}
		return m, nil
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
	case "3":
		m.mode = modeDomains
		return m, m.fetchDomains()
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
		rows := m.displayRows()
		switch key {
		case "j", "down":
			m.depCursor = clamp(m.depCursor+1, 0, len(rows)-1)
		case "k", "up":
			m.depCursor = clamp(m.depCursor-1, 0, len(rows)-1)
		case "g", "home":
			m.depCursor = 0
		case "G", "end":
			m.depCursor = len(rows) - 1
		case "enter":
			if d := m.selectedDep(); d != nil {
				return m, m.fetchDetail(*d)
			}
		case "e":
			if m.depCursor < len(rows) && rows[m.depCursor].project != "" {
				if m.expanded == rows[m.depCursor].project {
					m.expanded = ""
				} else {
					m.expanded = rows[m.depCursor].project
				}
				return m, nil
			}
		case "a":
			m.grouped = !m.grouped
			m.depCursor = 0
			m.expanded = ""
		case "x":
			if d := m.selectedDep(); d != nil && d.Status() == "building" {
				m.pending, m.pendingDep, m.confirmInput = pendCancel, *d, ""
			}
		case "D":
			if d := m.selectedDep(); d != nil {
				m.pending, m.pendingDep, m.confirmInput = pendDelete, *d, ""
			}
		case "R":
			if d := m.selectedDep(); d != nil {
				m.pending, m.pendingDep, m.confirmInput = pendRedeploy, *d, ""
			}
		case "B":
			if d := m.selectedDep(); d != nil && d.Status() == "ready" && d.Target == "production" {
				m.pending, m.pendingDep, m.confirmInput = pendRollback, *d, ""
			}
		case "l":
			if d := m.selectedDep(); d != nil {
				m.detail = d
				m.logs, m.logScroll = nil, 0
				m.mode = modeLogs
				return m, m.fetchLogs()
			}
		case "o":
			if d := m.selectedDep(); d != nil {
				return m, openBrowser("https://" + d.URL)
			}
		case "c":
			if d := m.selectedDep(); d != nil {
				return m, copyURL("https://" + d.URL)
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
		case "e":
			if m.projCursor < len(m.projects) {
				m.envProject = m.projects[m.projCursor]
				m.mode = modeEnvs
				m.envCursor = 0
				return m, m.fetchEnvs()
			}
		case "L":
			if m.projCursor < len(m.projects) {
				p := m.projects[m.projCursor]
				org := m.teamID()
				return m, func() tea.Msg {
					err := config.WriteProjectLink(".", p.ID, org)
					return actionMsg{text: "linked " + p.Name + " to ./vercel/project.json", err: err}
				}
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

	case modeEnvs:
		switch key {
		case "esc":
			m.mode = modeProjects
		case "j", "down":
			m.envCursor = clamp(m.envCursor+1, 0, len(m.envs)-1)
		case "k", "up":
			m.envCursor = clamp(m.envCursor-1, 0, len(m.envs)-1)
		case "n":
			m.envForm, m.envKey, m.envValue, m.envField, m.envPreset, m.envEditID = true, "", "", 0, 0, ""
		case "e":
			if m.envCursor < len(m.envs) {
				v := m.envs[m.envCursor]
				m.envForm, m.envValue, m.envField, m.envEditID = true, "", 1, v.ID
				if len(v.Target) == 1 {
					for i, p := range targetPresets {
						if p.label == v.Target[0] {
							m.envPreset = i
						}
					}
				}
			}
		case "d":
			if m.envCursor < len(m.envs) {
				m.pending, m.pendingEnv, m.confirmInput = pendDeleteEnv, m.envs[m.envCursor], ""
			}
		}

	case modeDomains:
		// read-only

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

// displayRow is one renderable row in the deployments view.
type displayRow struct {
	project string          // non-empty for a project head row
	dep     *api.Deployment // set for a child deployment row
	count   int             // child count, only for head rows
	indent  bool            // child row; render with a tree indent glyph
}

// displayRows returns the rows to show: when grouped, one head row per
// project (its latest deployment as the summary) plus the expanded
// project's deployments; otherwise the flat list.
func (m Model) displayRows() []displayRow {
	deps := m.visibleDeps()
	if !m.grouped {
		rows := make([]displayRow, 0, len(deps))
		for i := range deps {
			rows = append(rows, displayRow{dep: &deps[i]})
		}
		return rows
	}
	// group by project, preserving newest-first order of first appearance
	var order []string
	byProj := map[string][]api.Deployment{}
	for _, d := range deps {
		if byProj[d.Name] == nil {
			order = append(order, d.Name)
		}
		byProj[d.Name] = append(byProj[d.Name], d)
	}
	var rows []displayRow
	for _, name := range order {
		list := byProj[name]
		rows = append(rows, displayRow{project: name, count: len(list), dep: &list[0]})
		if name == m.expanded {
			for i := range list {
				rows = append(rows, displayRow{dep: &list[i], indent: true})
			}
		}
	}
	return rows
}

// selectedDep returns the deployment at the cursor, or nil if the cursor
// is on a project head row.
func (m Model) selectedDep() *api.Deployment {
	rows := m.displayRows()
	if m.depCursor < 0 || m.depCursor >= len(rows) {
		return nil
	}
	return rows[m.depCursor].dep
}
