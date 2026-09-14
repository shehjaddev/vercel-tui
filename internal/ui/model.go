package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	detail       *api.Deployment
	detailCache  map[string]api.Deployment // enriched detail by deployment key
	detailTried  map[string]bool           // keys whose enrichment was attempted
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

	loadCtx    context.Context
	loadCancel context.CancelFunc

	width, height int
}

type tickMsg struct{}
type teamsMsg struct {
	user  string
	teams []api.Team
}
type depsMsg struct {
	deps          []api.Deployment
	team, project string
}
type detailMsg struct{ d *api.Deployment }
type detailsMsg struct {
	byKey map[string]api.Deployment
	tried []string // keys the batch attempted, including the ones that failed
}
type logsMsg struct {
	lines []string
	id    string
}
type tokenOkMsg struct {
	user  string
	token string
}
type actionMsg struct {
	text   string
	err    error
	reload bool // whether success should trigger a data refresh
}
type envsMsg struct {
	envs          []api.EnvVar
	team, project string
}
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
	m.loadCtx, m.loadCancel = context.WithCancel(context.Background())
	return m
}

// scopeCtx returns the context for loads in the current team/scope.
func (m Model) scopeCtx() context.Context {
	if m.loadCtx == nil {
		return context.Background()
	}
	return m.loadCtx
}

// rescope cancels in-flight loads and starts a fresh scope, so late
// arrivals from the previous team or project are dropped, not rendered.
func (m *Model) rescope() {
	if m.loadCancel != nil {
		m.loadCancel()
	}
	m.loadCtx, m.loadCancel = context.WithCancel(context.Background())
}

func (m Model) Init() tea.Cmd {
	if m.mode == modeLogin {
		return nil
	}
	return tea.Batch(fetchTeams(m.scopeCtx(), m.client), schedule(2*time.Second))
}

func schedule(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// sleep is a command that simply waits; used to space out rate-limited fetches.
func sleep(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return struct{}{} })
}

func fetchTeams(ctx context.Context, c *api.Client) tea.Cmd {
	return func() tea.Msg {
		u, err := c.User(ctx)
		if err != nil {
			return errMsg{err}
		}
		teams, _ := c.Teams(ctx) // tolerate failure; personal scope still works
		return teamsMsg{user: u.Username, teams: teams}
	}
}

func (m Model) teamID() string {
	if m.teamIdx > 0 && m.teamIdx < len(m.teams) {
		return m.teams[m.teamIdx].ID
	}
	return ""
}

func (m Model) teamName() string {
	if len(m.teams) == 0 {
		return "…"
	}
	if m.teamIdx < 0 || m.teamIdx >= len(m.teams) {
		return m.teams[0].Name
	}
	return m.teams[m.teamIdx].Name
}

func (m *Model) fetchDeps() tea.Cmd {
	m.loading = true
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	project, target := m.projectID, m.targetFlag
	return func() tea.Msg {
		deps, err := c.Deployments(ctx, project, team, target, 100)
		if err != nil {
			return errMsg{err}
		}
		return depsMsg{deps: deps, team: team, project: project}
	}
}

func (m Model) unlinkCmd() (Model, tea.Cmd) {
	// drop the persisted .vercel/project.json (written by L) and clear any
	// in-memory project filter so the deployments view shows every project.
	if err := os.Remove(filepath.Join(m.dir, ".vercel", "project.json")); err != nil && !os.IsNotExist(err) {
		m.projectID, m.orgID = "", ""
		return m, func() tea.Msg { return errMsg{err} }
	}
	m.projectID, m.orgID = "", ""
	m.rescope()
	cmd := m.fetchDeps()
	return m, cmd
}

// fetchDetail fetches one deployment's full detail (aliases) and caches it
// keyed by id. It's fired only for the selected row — one request at a time,
// which is all Vercel's rate limit reliably allows. Cached so returning to a
// row is instant.
func (m Model) fetchDetail(d api.Deployment) tea.Cmd {
	ctx, c, id, team := m.scopeCtx(), m.client, d.Key(), m.teamID()
	key := d.Key()
	return func() tea.Msg {
		full, err := c.Deployment(ctx, id, team)
		if err != nil {
			return errMsg{err}
		}
		return detailsMsg{byKey: map[string]api.Deployment{key: *full}}
	}
}

// fetchProjectDomains loads the domains bound to a project, for the top
// detail block. Keyed by project id so each project is fetched once.
func (m Model) fetchProjectDomains(projectID string) tea.Cmd {
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	return func() tea.Msg {
		domains := map[string][]string{}
		if ds, err := c.ProjectDomains(ctx, projectID, team); err == nil {
			for _, d := range ds {
				domains[projectID] = append(domains[projectID], d.Name)
			}
		}
		return projDomainsMsg{domains: domains}
	}
}

// fetchNextDomains prefetches project domains in lockstep with the detail
// prefetch: for each project head whose enriched detail we already hold, it
// fetches up to 3 uncached project domains per batch and chains to the next
// batch (the chain delay between batches keeps us under the rate limit).
// The batch records an entry for every project it attempts — empty for a
// project with no domains, and empty for a request that failed — so the
// candidates only ever shrink and the chain always ends.
func (m Model) fetchNextDomains() tea.Cmd {
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	var pids []string
	for _, g := range m.projectGroups() {
		if len(g.deployments) == 0 {
			continue
		}
		pid := ""
		if cached, ok := m.detailCache[g.deployments[0].Key()]; ok {
			pid = cached.Project.ID
		}
		if pid == "" {
			continue
		}
		if _, done := m.domainCache[pid]; done {
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
		domains := make(map[string][]string, len(pids))
		for _, pid := range pids {
			names := []string{}
			if ds, err := c.ProjectDomains(ctx, pid, team); err == nil {
				for _, d := range ds {
					names = append(names, d.Name)
				}
			}
			domains[pid] = names
		}
		return projDomainsMsg{domains: domains}
	}
}

// fetchNextHeads enriches up to 3 uncached project heads per batch,
// returning them in one message so the board fills in batch by batch rather
// than all at once at the end. Chains to the next 3 on arrival, and reports
// every head it attempted so a failing one is dropped instead of being
// requested again on every refresh.
func (m Model) fetchNextHeads() tea.Cmd {
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	var heads []api.Deployment
	for _, g := range m.projectGroups() {
		if len(g.deployments) == 0 {
			continue
		}
		d := g.deployments[0]
		if _, ok := m.detailCache[d.Key()]; ok {
			continue
		}
		if m.detailTried[d.Key()] {
			continue
		}
		heads = append(heads, d)
		if len(heads) >= 3 {
			break
		}
	}
	if len(heads) == 0 {
		return nil
	}
	return func() tea.Msg {
		byKey := make(map[string]api.Deployment, len(heads))
		tried := make([]string, 0, len(heads))
		for _, d := range heads {
			tried = append(tried, d.Key())
			full, err := c.Deployment(ctx, d.Key(), team)
			if err != nil {
				continue
			}
			byKey[d.Key()] = *full
		}
		return detailsMsg{byKey: byKey, tried: tried}
	}
}

func (m *Model) fetchLogs() tea.Cmd {
	m.loading = true
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	id := ""
	if m.detail != nil {
		id = m.detail.Key()
	}
	return func() tea.Msg {
		events, err := c.Events(ctx, id, team)
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
		return logsMsg{lines: lines, id: id}
	}
}

func (m *Model) fetchEnvs() tea.Cmd {
	m.loading = true
	ctx, c, team, project := m.scopeCtx(), m.client, m.teamID(), m.envProject.ID
	return func() tea.Msg {
		envs, err := c.EnvVars(ctx, project, team)
		if err != nil {
			return errMsg{err}
		}
		return envsMsg{envs: envs, team: team, project: project}
	}
}

func (m Model) submitEnv() tea.Cmd {
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	project := m.envProject.ID
	key, value := m.envKey, m.envValue
	targets := targetPresets[m.envPreset].values
	editID := m.envEditID
	return func() tea.Msg {
		var err error
		if editID != "" {
			err = c.UpdateEnvValue(ctx, project, team, editID, value, targets)
		} else {
			err = c.CreateEnv(ctx, project, team, key, value, targets)
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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		u, err := api.New(token).User(ctx)
		if err != nil {
			return errMsg{err}
		}
		if err := config.StoreToken(token); err != nil {
			return errMsg{err}
		}
		return tokenOkMsg{user: u.Username, token: token}
	}
}

func openBrowser(target string) tea.Cmd {
	return func() tea.Msg {
		var candidates [][]string
		if bin := os.Getenv("BROWSER"); bin != "" {
			if fields := strings.Fields(bin); len(fields) > 0 {
				candidates = append(candidates, fields)
			}
		}
		switch runtime.GOOS {
		case "darwin":
			candidates = append(candidates, []string{"open"})
		case "windows":
			candidates = append(candidates, []string{"rundll32", "url.dll,FileProtocolHandler"})
		default:
			candidates = append(candidates, []string{"xdg-open"})
		}
		for _, c := range candidates {
			if err := exec.Command(c[0], append(c[1:], target)...).Start(); err == nil {
				return nil
			}
		}
		return errMsg{errors.New("could not open browser")}
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, bin := range clipboardTools {
			cmd := exec.CommandContext(ctx, bin[0], bin[1:]...)
			cmd.Stdin = strings.NewReader(url)
			if err := cmd.Run(); err == nil {
				return actionMsg{text: "copied " + url}
			}
		}
		return errMsg{errors.New("no clipboard tool found (wl-copy, xclip, xsel, pbcopy)")}
	}
}

// handleConfirm runs the typed-confirmation dialog for destructive actions.
func (m Model) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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
		if msg.Type == tea.KeyRunes {
			m.confirmInput += string(msg.Runes)
		}
	}
	return m, nil
}

func (m Model) runEnvDelete() tea.Cmd {
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	project, key := m.envProject.ID, m.pendingEnv.Key
	id := m.pendingEnv.ID
	return func() tea.Msg {
		err := c.DeleteEnv(ctx, project, team, id)
		return actionMsg{text: "deleted " + key, err: err, reload: true}
	}
}

// redeployTarget keeps production redeploys in production (the API
// defaults to preview) while only sending documented target values.
func redeployTarget(dep api.Deployment) string {
	switch strings.ToLower(dep.Target) {
	case "production", "preview", "development":
		return strings.ToLower(dep.Target)
	}
	return ""
}

func (m Model) runAction(pa pendingAction, dep api.Deployment) tea.Cmd {
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	id := dep.Key()
	switch pa {
	case pendCancel:
		return func() tea.Msg {
			_, err := c.CancelDeployment(ctx, id, team)
			return actionMsg{text: "build canceled", err: err, reload: true}
		}
	case pendDelete:
		return func() tea.Msg {
			err := c.DeleteDeployment(ctx, id, team)
			return actionMsg{text: "deployment deleted", err: err, reload: true}
		}
	case pendRedeploy:
		name := dep.Name
		target := redeployTarget(dep)
		return func() tea.Msg {
			var git *api.GitSource
			if ref := dep.Branch(); ref != "" {
				p, err := c.ProjectByName(ctx, name, team)
				if err != nil {
					return errMsg{err}
				}
				if p.Link.Repo != "" {
					git = &api.GitSource{Type: strings.ToLower(p.Link.Type), Org: p.Link.Org, Repo: p.Link.Repo, Ref: ref}
				}
			}
			_, err := c.Redeploy(ctx, name, id, team, git, target)
			return actionMsg{text: "redeploy of " + name + " started", err: err, reload: true}
		}
	case pendRollback:
		name := dep.Name
		return func() tea.Msg {
			p, err := c.ProjectByName(ctx, name, team)
			if err != nil {
				return errMsg{err}
			}
			err = c.Promote(ctx, p.ID, id, team)
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
		cmd := m.fetchLogs()
		return m, cmd
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

func (m *Model) loadCurrent() tea.Cmd {
	switch m.mode {
	case modeDeployments:
		return m.fetchDeps()
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
		if m.orgID != "" {
			for i := range m.teams {
				if m.teams[i].ID == m.orgID {
					m.teamIdx, m.teamCursor = i, i
					break
				}
			}
		}
		cmd := m.loadCurrent()
		return m, cmd

	case depsMsg:
		if msg.team != m.teamID() || msg.project != m.projectID {
			return m, nil // stale scope; a newer load is in flight
		}
		m.deps = msg.deps
		m.loading, m.throttled = false, false
		m.lastLoad = time.Now()
		m.depCursor = clamp(m.depCursor, 0, max(len(m.displayRows())-1, 0))
		// Fetch the selected row's detail immediately (1 fast request, always
		// reliable) so its aliases show right away, then stage the rest.
		// Runs on every refresh so newly arrived deployments get enriched.
		if m.mode == modeDeployments {
			if m.detailCache == nil {
				m.detailCache = map[string]api.Deployment{}
			}
			var cmds []tea.Cmd
			if d := m.selectedDep(); d != nil {
				if _, ok := m.detailCache[d.Key()]; !ok {
					cmds = append(cmds, m.fetchDetail(*d))
				}
			}
			if h := m.fetchNextHeads(); h != nil {
				cmds = append(cmds, h)
			}
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
		}

	case detailsMsg:
		if m.detailCache == nil {
			m.detailCache = map[string]api.Deployment{}
		}
		if m.detailTried == nil {
			m.detailTried = map[string]bool{}
		}
		for k, d := range msg.byKey {
			m.detailCache[k] = d
		}
		// successful and failed attempts are both spent; only a fresh list
		// brings a new deployment key to try
		for k := range msg.byKey {
			m.detailTried[k] = true
		}
		for _, k := range msg.tried {
			m.detailTried[k] = true
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

	case envsMsg:
		if msg.team != m.teamID() || msg.project != m.envProject.ID {
			return m, nil // stale scope; a newer load is in flight
		}
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
		if m.detail == nil || msg.id != m.detail.Key() {
			return m, nil // stale deployment; a newer log load is in flight
		}
		m.logs = msg.lines
		// a refresh can return fewer lines than the offset the user had
		// scrolled to, which would slice past the end of the new log
		m.logScroll = clamp(m.logScroll, 0, m.logMaxScroll())
		m.loading, m.throttled = false, false

	case tokenOkMsg:
		m.client = api.New(msg.token)
		m.authed = true
		m.loading = false
		m.user = msg.user
		m.teams = []api.Team{{Name: msg.user + " (personal)"}}
		m.mode = modeDeployments
		m.tokenBuf = ""
		m.rescope()
		depsCmd := m.fetchDeps()
		return m, tea.Batch(fetchTeams(m.scopeCtx(), m.client), depsCmd)

	case actionMsg:
		m.loading = false
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				return m, nil // superseded by a scope switch; tick refreshes
			}
			if errors.Is(msg.err, api.ErrThrottled) {
				m.throttled = true
			} else {
				m.err = msg.err.Error()
			}
			retryCmd := m.loadCurrent()
			return m, retryCmd
		}
		m.note, m.noteAt = msg.text, time.Now()
		if !msg.reload {
			return m, nil
		}
		reloadCmd := m.loadCurrent()
		return m, reloadCmd

	case errMsg:
		m.loading = false
		if errors.Is(msg.err, context.Canceled) {
			break // superseded by a scope switch; tick refreshes
		}
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
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			if m.tokenBuf != "" {
				m.loading = true
				return m, validateToken(m.tokenBuf)
			}
		case "o":
			if m.tokenBuf == "" {
				return m, openBrowser("https://vercel.com/account/tokens")
			}
			m.tokenBuf += key
		case "q":
			if m.tokenBuf == "" {
				return m, tea.Quit
			}
			m.tokenBuf += key
		case "backspace":
			if r := []rune(m.tokenBuf); len(r) > 0 {
				m.tokenBuf = string(r[:len(r)-1])
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.tokenBuf += string(msg.Runes)
			}
		}
		return m, nil
	}

	if m.filterFocus {
		switch key {
		case "enter":
			m.filter, m.filterFocus, m.depCursor = m.filterBuf, false, 0
		case "esc":
			m.filterBuf, m.filterFocus = "", false
		case "backspace":
			if r := []rune(m.filterBuf); len(r) > 0 {
				m.filterBuf = string(r[:len(r)-1])
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.filterBuf += string(msg.Runes)
			}
		}
		return m, nil
	}

	if m.pending != pendNone {
		return m.handleConfirm(msg)
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
			if msg.Type == tea.KeyRunes {
				if m.envField == 0 && m.envEditID == "" {
					m.envKey += string(msg.Runes)
				} else {
					m.envValue += string(msg.Runes)
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
			if msg.Type == tea.KeyRunes {
				m.searchBuf += string(msg.Runes)
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
				m.depCursor = 0
				m.teamSel = false
				if m.projectID != "" && m.teamID() != m.orgID {
					m.projectID = ""
					m.orgID = ""
				}
				m.rescope()
				teamCmd := m.loadCurrent()
				return m, teamCmd
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
	case "t":
		if len(m.teams) > 1 {
			m.teamSel, m.teamCursor = true, m.teamIdx
		}
		return m, nil
	case "r":
		refreshCmd := m.loadCurrent()
		return m, refreshCmd
	case "/":
		switch m.mode {
		case modeDeployments:
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
			_, cached := m.detailCache[d.Key()]
			if cached {
				c := m.detailCache[d.Key()]
				m.detail = &c // cache hit: instant, no request
			}
			var cmds []tea.Cmd
			if !cached {
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
				envsCmd := m.fetchEnvs()
				return m, envsCmd
			}
		case "E":
			if m.depCursor < len(rows) && rows[m.depCursor].project != "" {
				if m.expanded == rows[m.depCursor].project {
					m.expanded = ""
				} else {
					m.expanded = rows[m.depCursor].project
				}
				return m, nil
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
				logsCmd := m.fetchLogs()
				return m, logsCmd
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

	case modeActions:
		if m.detail == nil {
			m.mode = modeDeployments
			return m, nil
		}
		actions := m.deploymentActions()
		switch key {
		case "esc":
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
			m.mode = modeDeployments
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
		maxScroll := m.logMaxScroll()
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

// logViewport is how many log lines fit on screen at once.
func (m Model) logViewport() int { return max(m.height-6, 1) }

// logMaxScroll is the largest scroll offset that still shows the first line.
func (m Model) logMaxScroll() int { return max(len(m.logs)-m.logViewport(), 0) }

// logTopIndex is the absolute index of the topmost visible log line.
func (m Model) logTopIndex() int {
	start := len(m.logs) - m.logViewport() - m.logScroll
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
	visible := m.logViewport()
	maxScroll := m.logMaxScroll()
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
	state := ""
	if m.stateIdx >= 0 && m.stateIdx < len(stateFilters) {
		state = strings.ToLower(stateFilters[m.stateIdx])
	}
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

// selectedDep returns the deployment at the cursor. Project head rows
// carry their latest deployment, so this is nil only off-list.
func (m Model) selectedDep() *api.Deployment {
	rows := m.displayRows()
	if m.depCursor < 0 || m.depCursor >= len(rows) {
		return nil
	}
	return rows[m.depCursor].dep
}
