package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	modeActions
	modeLogs
	modeEnvs
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
	dir                    string // dir holding .vercel/project.json (link scope)

	mode    mode
	user    string
	teams   []api.Team // index 0 is the personal account
	teamIdx int

	deps         []api.Deployment
	depCursor    int
	actionCursor int
	grouped      bool   // default: one row per project, expandable
	expanded     string // project name currently expanded ("" = none)
	projects     []api.Project
	projCursor   int
	detail       *api.Deployment
	detailCache  map[string]api.Deployment // enriched detail by deployment key
	domainCache  map[string][]string       // project domains by project id
	logs         []string
	logScroll    int // lines back from the bottom; 0 means following

	envProject api.Project
	envs       []api.EnvVar
	envCursor  int
	envForm    bool
	envKey     string
	envValue   string
	envField   int // 0 = key, 1 = value
	envPreset  int
	envEditID  string // non-empty while editing an existing var

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
type detailsMsg struct{ byKey map[string]api.Deployment }
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
type projDomainsMsg struct{ domains map[string][]string }
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

func New(client *api.Client, authed bool, refresh time.Duration, link *config.ProjectLink, target, branch, dir string) Model {
	m := Model{
		client:     client,
		authed:     authed,
		refresh:    refresh,
		targetFlag: target,
		branchFlag: branch,
		dir:        dir,
		mode:       modeDeployments,
	}
	if m.dir == "" {
		m.dir = "."
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

// sleep is a command that simply waits; used to space out rate-limited fetches.
func sleep(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return struct{}{} })
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

// fetchAllDetails enriches every deployment in the list at once, so aliases
// fetchDetail fetches one deployment's full detail (aliases) and caches it
// keyed by id. It's fired only for the selected row — one request at a time,
// which is all Vercel's rate limit reliably allows. Cached so returning to a
// row is instant.
func (m Model) unlinkCmd() (Model, tea.Cmd) {
	// drop the persisted .vercel/project.json (written by L) and clear any
	// in-memory project filter so the deployments view shows every project.
	_ = os.Remove(filepath.Join(m.dir, ".vercel", "project.json"))
	m.projectID = ""
	return m, m.fetchDeps()
}

func (m Model) fetchDetail(d api.Deployment) tea.Cmd {
	c, id, team := m.client, d.UID, m.teamID()
	if d.UID == "" {
		id = d.ID
	}
	key := d.Key()
	return func() tea.Msg {
		full, err := c.Deployment(id, team)
		if err != nil {
			return errMsg{err}
		}
		return detailsMsg{byKey: map[string]api.Deployment{key: *full}}
	}
}

// fetchProjectDomains loads the domains bound to a project, for the top
// detail block. Keyed by project id so each project is fetched once.
func (m Model) fetchProjectDomains(projectID string) tea.Cmd {
	c, team := m.client, m.teamID()
	return func() tea.Msg {
		domains := map[string][]string{}
		if ds, err := c.ProjectDomains(projectID, team); err == nil {
			for _, d := range ds {
				domains[projectID] = append(domains[projectID], d.Name)
			}
		}
		return projDomainsMsg{domains: domains}
	}
}

// fetchNextDomains prefetches project domains in lockstep with the alias
// prefetch: for each project head whose enriched detail we already hold, it
// fetches up to 3 uncached project domains per batch (with a pause between
// each, staying under the rate limit) and chains to the next batch. Every
// project is fetched once and cached, so navigation never re-requests.
func (m Model) fetchNextDomains() tea.Cmd {
	c, team := m.client, m.teamID()
	var pids []string
	for _, g := range m.projectGroups() {
		if len(g.deployments) == 0 {
			continue
		}
		pid := ""
		if cached, ok := m.detailCache[g.deployments[0].Key()]; ok {
			pid = cached.Project.ID
		}
		if pid == "" || m.domainCache[pid] != nil {
			continue
		}
		pids = append(pids, pid)
		if len(pids) >= 3 {
			break
		}
	}
	if len(pids) == 0 {
		return nil
	}
	return func() tea.Msg {
		domains := map[string][]string{}
		for i, pid := range pids {
			if ds, err := c.ProjectDomains(pid, team); err == nil {
				for _, d := range ds {
					domains[pid] = append(domains[pid], d.Name)
				}
			}
			if i < len(pids)-1 {
				time.Sleep(1200 * time.Millisecond)
			}
		}
		return projDomainsMsg{domains: domains}
	}
}

// time (with a pause between each, staying under the rate limit), returning
// them in one message so aliases appear after each batch rather than at the
// very end. Chains to the next 3 on arrival.
func (m Model) fetchNextHeads() tea.Cmd {
	c, team := m.client, m.teamID()
	var heads []api.Deployment
	for _, g := range m.projectGroups() {
		if len(g.deployments) > 0 {
			d := g.deployments[0]
			if _, ok := m.detailCache[d.Key()]; !ok {
				heads = append(heads, d)
				if len(heads) >= 3 {
					break
				}
			}
		}
	}
	if len(heads) == 0 {
		return nil
	}
	return func() tea.Msg {
		byKey := map[string]api.Deployment{}
		for _, d := range heads {
			full, err := c.Deployment(d.Key(), team)
			if err != nil {
				continue
			}
			byKey[d.Key()] = *full
		}
		return detailsMsg{byKey: byKey}
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

// actionItem is one entry in the deployment actions menu.
type actionItem struct {
	key   string
	label string
}

// deploymentActions lists the actions available for the selected deployment.
func (m Model) deploymentActions() []actionItem {
	d := m.detail
	if d == nil {
		return nil
	}
	actions := []actionItem{
		{"l", "View logs"},
		{"R", "Redeploy same commit"},
		{"c", "Copy URL"},
		{"o", "Open in browser"},
	}
	if d.Status() == "building" {
		actions = append(actions, actionItem{"x", "Cancel build"})
	}
	if d.Status() == "ready" && d.Target == "production" {
		actions = append(actions, actionItem{"B", "Rollback to production"})
	}
	// delete is always available but last (destructive)
	actions = append(actions, actionItem{"D", "Delete deployment"})
	return actions
}

// runActionByKey invokes an action by its keybinding.
func (m Model) runActionByKey(key string) (tea.Model, tea.Cmd) {
	d := m.detail
	if d == nil {
		m.mode = modeDeployments
		return m, nil
	}
	m.mode = modeDeployments // leave the action overlay
	switch key {
	case "l":
		m.logs, m.logScroll = nil, 0
		m.mode = modeLogs
		return m, m.fetchLogs()
	case "o":
		return m, openBrowser("https://" + d.URL)
	case "c":
		return m, copyURL("https://" + d.URL)
	case "x":
		if d.Status() == "building" {
			m.pending, m.pendingDep, m.confirmInput = pendCancel, *d, ""
		}
		return m, nil
	case "R":
		m.pending, m.pendingDep, m.confirmInput = pendRedeploy, *d, ""
		return m, nil
	case "B":
		if d.Status() == "ready" && d.Target == "production" {
			m.pending, m.pendingDep, m.confirmInput = pendRollback, *d, ""
		}
		return m, nil
	case "D":
		m.pending, m.pendingDep, m.confirmInput = pendDelete, *d, ""
		return m, nil
	}
	return m, nil
}

func (m Model) loadCurrent() tea.Cmd {
	switch m.mode {
	case modeDeployments:
		return m.fetchDeps()
	case modeProjects:
		return m.fetchProjects()
	case modeEnvs:
		return m.fetchEnvs()
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
		// Fetch the selected row's detail immediately (1 fast request, always
		// reliable) so its aliases show right away, then stage the rest.
		if m.mode == modeDeployments && m.detailCache == nil {
			m.detailCache = map[string]api.Deployment{}
			if d := m.selectedDep(); d != nil {
				return m, tea.Batch(m.fetchDetail(*d), m.fetchNextHeads())
			}
			return m, m.fetchNextHeads()
		}

	case detailsMsg:
		if m.detailCache == nil {
			m.detailCache = map[string]api.Deployment{}
		}
		for k, d := range msg.byKey {
			m.detailCache[k] = d
		}
		// keep m.detail pointing at the selected deployment's enriched data
		if m.mode == modeDeployments {
			if cur := m.selectedDep(); cur != nil {
				if d, ok := m.detailCache[cur.Key()]; ok {
					m.detail = &d
				}
			}
		}
		// prefetch domains in lockstep with aliases: chain both, each in its
		// own staged batches, so every project's domains populate like aliases.
		if m.mode == modeDeployments {
			var cmds []tea.Cmd
			if h := m.fetchNextHeads(); h != nil {
				cmds = append(cmds, tea.Sequence(sleep(1200*time.Millisecond), h))
			}
			if d := m.fetchNextDomains(); d != nil {
				cmds = append(cmds, tea.Sequence(sleep(400*time.Millisecond), d))
			}
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
		}

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

	case projDomainsMsg:
		if m.domainCache == nil {
			m.domainCache = map[string][]string{}
		}
		for pid, names := range msg.domains {
			m.domainCache[pid] = names
		}
		if m.mode == modeDeployments {
			if cmd := m.fetchNextDomains(); cmd != nil {
				return m, tea.Sequence(sleep(400*time.Millisecond), cmd)
			}
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
		// fetchDetailCmd returns a command that enriches the selected row.
		// refreshDetail reads the cache (populated by the batch fetch) so
		// navigation is instant with no per-row requests.
		refreshDetail := func() tea.Cmd {
			d := m.selectedDep()
			if d == nil {
				return nil
			}
			if cached, ok := m.detailCache[d.Key()]; ok {
				m.detail = &cached // cache hit: instant, no request
			}
			var cmds []tea.Cmd
			if m.detail == nil {
				cmds = append(cmds, m.fetchDetail(*d))
			}
			// domains for the selected project, fetched once per project
			pid := ""
			if m.detail != nil {
				pid = m.detail.Project.ID
			} else if cached, ok := m.detailCache[d.Key()]; ok {
				pid = cached.Project.ID
			}
			if pid != "" {
				if _, ok := m.domainCache[pid]; !ok {
					cmds = append(cmds, m.fetchProjectDomains(pid))
				}
			}
			if len(cmds) == 0 {
				return nil
			}
			return tea.Batch(cmds...)
		}
		switch key {
		case "j", "down":
			m.depCursor = clamp(m.depCursor+1, 0, len(rows)-1)
			return m, refreshDetail()
		case "k", "up":
			m.depCursor = clamp(m.depCursor-1, 0, len(rows)-1)
			return m, refreshDetail()
		case "g", "home":
			m.depCursor = 0
			return m, refreshDetail()
		case "G", "end":
			m.depCursor = len(rows) - 1
			return m, refreshDetail()
		case "enter":
			if d := m.selectedDep(); d != nil {
				if m.detail == nil || m.detail.Key() != d.Key() {
					m.detail = d
				}
				m.mode = modeActions
				m.actionCursor = 0
			}
			return m, nil
		case "e":
			if d := m.selectedDep(); d != nil {
				pid := ""
				if cached, ok := m.detailCache[d.Key()]; ok {
					pid = cached.Project.ID
				}
				if pid == "" {
					return m, nil
				}
				m.envProject = api.Project{Name: d.Name, ID: pid}
				m.mode = modeEnvs
				m.envCursor = 0
				return m, m.fetchEnvs()
			}
		case "L":
			if d := m.selectedDep(); d != nil {
				pid := ""
				if cached, ok := m.detailCache[d.Key()]; ok {
					pid = cached.Project.ID
				}
				if pid == "" {
					return m, nil
				}
				p := api.Project{Name: d.Name, ID: pid}
				org := m.teamID()
				return m, func() tea.Msg {
					err := config.WriteProjectLink(m.dir, p.ID, org)
					return actionMsg{text: "linked " + p.Name + " (" + filepath.Join(m.dir, ".vercel/project.json") + ")", err: err}
				}
			}
		case "a":
			m.grouped = !m.grouped
			m.depCursor = 0
			m.expanded = ""
		case "U":
			if m.projectID != "" {
				return m.unlinkCmd()
			}
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
					err := config.WriteProjectLink(m.dir, p.ID, org)
					return actionMsg{text: "linked " + p.Name + " (" + filepath.Join(m.dir, ".vercel/project.json") + ")", err: err}
				}
			}
		case "U":
			if m.projCursor < len(m.projects) {
				return m.unlinkCmd()
			}
		}

	case modeActions:
		if m.detail == nil {
			m.mode = modeDeployments
			return m, nil
		}
		actions := m.deploymentActions()
		switch key {
		case "esc", "q":
			m.mode = modeDeployments
		case "j", "down":
			m.actionCursor = clamp(m.actionCursor+1, 0, len(actions)-1)
		case "k", "up":
			m.actionCursor = clamp(m.actionCursor-1, 0, len(actions)-1)
		case "g", "home":
			m.actionCursor = 0
		case "G", "end":
			m.actionCursor = len(actions) - 1
		case "enter":
			if m.actionCursor < len(actions) {
				return m.runActionByKey(actions[m.actionCursor].key)
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

	case modeLogs:
		maxScroll := max(len(m.logs)-(m.height-6), 0)
		switch key {
		case "esc":
			m.mode = modeDeployments
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
	last    bool            // last child of an expanded project; renders └──
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
				rows = append(rows, displayRow{dep: &list[i], indent: true, last: i == len(list)-1})
			}
		}
	}
	return rows
}

// projectGroups returns visible deployments grouped by project, newest
// group first (matching display order). Each group keeps its deployments
// newest-first.
type projectGroup struct {
	name        string
	deployments []api.Deployment
}

func (m Model) projectGroups() []projectGroup {
	deps := m.visibleDeps()
	var order []string
	byProj := map[string][]api.Deployment{}
	for _, d := range deps {
		if byProj[d.Name] == nil {
			order = append(order, d.Name)
		}
		byProj[d.Name] = append(byProj[d.Name], d)
	}
	groups := make([]projectGroup, 0, len(order))
	for _, name := range order {
		groups = append(groups, projectGroup{name: name, deployments: byProj[name]})
	}
	return groups
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
