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

const (
	// initPollDelay is how long after startup the first refresh runs, so the
	// window can draw before the network does.
	initPollDelay = 2 * time.Second
	// buildingPoll caps the interval while a build is running.
	buildingPoll = 2 * time.Second
	// liveLogPoll is how often a watched deployment's events are re-fetched.
	liveLogPoll = 2 * time.Second
	// noteLifetime is how long a status line stays in the footer.
	noteLifetime = 3 * time.Second
	// detailGap and domainGap space the background batches out far enough to
	// stay clear of the API's rate limit.
	detailGap = 1200 * time.Millisecond
	domainGap = 400 * time.Millisecond
)

// targetPreset is one entry in the env form's target cycle.
type targetPreset struct {
	label  string
	values []string
}

var targetPresets = []targetPreset{
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
	return tea.Batch(fetchTeams(m.scopeCtx(), m.client), schedule(initPollDelay))
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

// fetchDetail fetches one deployment's full detail — the object that carries
// its project and aliases — and caches it by key. It is fired only for the
// selected row, one request at a time, which is all Vercel's rate limit
// reliably allows, and the cache is what makes returning to a row instant.
func (m Model) fetchDetail(d api.Deployment) tea.Cmd {
	ctx, c, key, team := m.scopeCtx(), m.client, d.Key(), m.teamID()
	return func() tea.Msg {
		full, err := c.Deployment(ctx, key, team)
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

// keyField and valueField index the env form's two text fields.
const (
	keyField = iota
	valueField
)

// typeEnvForm routes a keystroke to the form field being edited: the key while
// it is being typed, the value once the key is set or an existing var is being
// edited. It needs a pointer receiver — the form state lives in the model the
// key handler returns, not in a copy of it.
func (m *Model) typeEnvForm(msg tea.KeyMsg) {
	if m.envField == valueField || m.envEditID != "" {
		m.envValue, _ = typeText(m.envValue, msg)
		return
	}
	m.envKey, _ = typeText(m.envKey, msg)
}

// newEnvForm opens an empty form for a new variable.
func (m Model) newEnvForm() Model {
	m.envForm, m.envKey, m.envValue, m.envField, m.envPreset, m.envEditID = true, "", "", keyField, 0, ""
	return m
}

// editEnvForm opens the form on an existing variable. Editing starts on "keep
// the stored targets" (-1): changing a value leaves the targets alone unless
// the user cycles to one.
func (m Model) editEnvForm(v api.EnvVar) Model {
	m.envForm, m.envKey, m.envValue, m.envField, m.envPreset, m.envEditID = true, v.Key, "", valueField, -1, v.ID
	return m
}

// closeEnvForm discards the form.
func (m Model) closeEnvForm() Model {
	m.envForm, m.envKey, m.envValue, m.envField, m.envPreset, m.envEditID = false, "", "", keyField, 0, ""
	return m
}

// nextEnvPreset advances the target choice in the env form. While editing,
// cycling past the last preset wraps back to keeping the stored targets.
func (m Model) nextEnvPreset() int {
	next := m.envPreset + 1
	if next < len(targetPresets) {
		return next
	}
	if m.envEditID != "" {
		return -1
	}
	return 0
}

func (m Model) submitEnv() tea.Cmd {
	ctx, c, team := m.scopeCtx(), m.client, m.teamID()
	project := m.envProject.ID
	key, value := m.envKey, m.envValue
	editID := m.envEditID
	// editing starts on "keep the stored targets" (-1): only send a target
	// list once the user has actually picked one
	var targets []string
	if m.envPreset >= 0 {
		targets = targetPresets[m.envPreset].values
	}
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
		for _, tool := range candidates {
			cmd := exec.Command(tool[0], append(tool[1:], target)...)
			if err := cmd.Start(); err != nil {
				continue
			}
			// the browser outlives us; release the handle instead of keeping
			// a process entry for every press of "o"
			_ = cmd.Process.Release()
			return nil
		}
		return errMsg{errors.New("could not open browser")}
	}
}

// clipboardTools lists the commands that put stdin on the clipboard, most
// likely first for this platform.
func clipboardTools() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip"}}
	default:
		return [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		}
	}
}

func copyURL(url string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tools := clipboardTools()
		tried := make([]string, 0, len(tools))
		for _, bin := range tools {
			tried = append(tried, bin[0])
			cmd := exec.CommandContext(ctx, bin[0], bin[1:]...)
			cmd.Stdin = strings.NewReader(url)
			if err := cmd.Run(); err == nil {
				return actionMsg{text: "copied " + url}
			}
		}
		return errMsg{fmt.Errorf("no clipboard tool found (tried %s)", strings.Join(tried, ", "))}
	}
}

// typeText applies one keystroke to a text buffer: backspace drops the last
// rune, printable runes append, and anything else is not text (ok is false),
// so the caller can fall through to its own key handling.
func typeText(buf string, msg tea.KeyMsg) (string, bool) {
	switch msg.String() {
	case "backspace":
		if r := []rune(buf); len(r) > 0 {
			return string(r[:len(r)-1]), true
		}
		return buf, true
	}
	if msg.Type == tea.KeyRunes {
		return buf + string(msg.Runes), true
	}
	return buf, false
}

// handleConfirm runs the typed-confirmation dialog for destructive actions.
func (m Model) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.pending = pendNone
		m.confirmInput = ""
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
		m.confirmInput, _ = typeText(m.confirmInput, msg)
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

// deploymentAction is one thing you can do to a deployment. The menu, the
// list's own keybindings and the footer hints all read this table, so a key's
// name and the conditions it needs cannot drift apart.
type deploymentAction struct {
	key   string
	label string
	hint  string                               // footer wording, for actions that come and go
	pend  pendingAction                        // set when the action asks for confirmation
	run   func(*Model, api.Deployment) tea.Cmd // set when it runs straight away
	needs func(api.Deployment) bool
}

var deploymentActions = []deploymentAction{
	{
		key: "l", label: "View logs", hint: "logs",
		run: func(m *Model, d api.Deployment) tea.Cmd {
			m.detail = &d
			m.logs, m.logScroll = nil, 0
			m.mode = modeLogs
			return m.fetchLogs()
		},
	},
	{key: "R", label: "Redeploy same commit", pend: pendRedeploy},
	{
		key: "c", label: "Copy URL", hint: "copy",
		run: func(_ *Model, d api.Deployment) tea.Cmd { return copyURL("https://" + d.URL) },
	},
	{
		key: "o", label: "Open in browser", hint: "open",
		run: func(_ *Model, d api.Deployment) tea.Cmd { return openBrowser("https://" + d.URL) },
	},
	{key: "x", label: "Cancel build", hint: "cancel", pend: pendCancel, needs: api.Deployment.CanCancel},
	{key: "B", label: "Rollback to production", hint: "rollback", pend: pendRollback, needs: api.Deployment.CanRollback},
	{key: "D", label: "Delete deployment", pend: pendDelete}, // always last: destructive
}

// actionsFor lists the actions a deployment can take right now.
func actionsFor(d api.Deployment) []deploymentAction {
	var out []deploymentAction
	for _, a := range deploymentActions {
		if a.needs == nil || a.needs(d) {
			out = append(out, a)
		}
	}
	return out
}

// ask opens the confirmation dialog for a destructive action.
func (m *Model) ask(pa pendingAction, dep api.Deployment) {
	m.pending, m.pendingDep, m.confirmInput = pa, dep, ""
}

// runActionKey runs the action bound to key against dep, leaving whatever menu
// the key came from. An action the deployment cannot take does nothing.
func (m Model) runActionKey(key string, dep api.Deployment) (tea.Model, tea.Cmd) {
	for _, a := range deploymentActions {
		if a.key != key {
			continue
		}
		if a.needs != nil && !a.needs(dep) {
			return m, nil
		}
		m.mode = modeDeployments // leave the action overlay
		if a.pend != pendNone {
			m.ask(a.pend, dep)
			return m, nil
		}
		if a.run != nil {
			return m, a.run(&m, dep)
		}
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
		return liveLogPoll
	}
	if m.refresh == 0 {
		return 0
	}
	for _, d := range m.deps {
		if d.CanCancel() { // a build is in progress: poll faster
			return min(m.refresh, buildingPoll)
		}
	}
	return m.refresh
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tickMsg:
		if !m.noteAt.IsZero() && time.Since(m.noteAt) > noteLifetime {
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
				cmds = append(cmds, tea.Sequence(sleep(detailGap), h))
			}
			if d := m.fetchNextDomains(); d != nil {
				cmds = append(cmds, tea.Sequence(sleep(domainGap), d))
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
				return m, tea.Sequence(sleep(domainGap), cmd)
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

// handleKey routes a keypress. Modes that read text or run a dialog get the
// key first; ctrl+c stays global so they can't trap the user.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	m.err = "" // any keypress dismisses an action error

	switch {
	case key == "ctrl+c":
		return m, tea.Quit
	case m.mode == modeLogin:
		return m.handleLoginKey(msg)
	case m.filterFocus:
		return m.handleFilterKey(msg)
	case m.pending != pendNone:
		return m.handleConfirm(msg)
	case m.envForm:
		return m.handleEnvFormKey(msg)
	case m.searchFocus:
		return m.handleSearchKey(msg)
	case m.help:
		m.help = false
		return m, nil
	case m.teamSel:
		return m.handleTeamKey(key)
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "t":
		if len(m.teams) > 1 {
			m.teamSel, m.teamCursor = true, m.teamIdx
		}
		return m, nil
	case "r":
		return m, m.loadCurrent()
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
		return m.handleDeploymentsKey(key)
	case modeActions:
		return m.handleActionsKey(key)
	case modeEnvs:
		return m.handleEnvsKey(key)
	case modeLogs:
		return m.handleLogsKey(key)
	}
	return m, nil
}

// projectIDFor is the project a deployment belongs to, when its enriched
// detail is already cached. Only the detail response carries the project; the
// list items leave it empty.
func (m Model) projectIDFor(d api.Deployment) string {
	cached, ok := m.detailCache[d.Key()]
	if !ok {
		return ""
	}
	return cached.Project.ID
}

// refreshDetail returns a command that enriches the selected row. It reads the
// cache the batch fetch fills, so navigation is instant and only asks for what
// is missing.
func (m *Model) refreshDetail() tea.Cmd {
	d := m.selectedDep()
	if d == nil {
		return nil
	}
	if cached, ok := m.detailCache[d.Key()]; ok {
		m.detail = &cached // cache hit: instant, no request
	}
	var cmds []tea.Cmd
	if _, ok := m.detailCache[d.Key()]; !ok {
		cmds = append(cmds, m.fetchDetail(*d))
	}
	// domains for the selected project, fetched once per project
	if pid := m.projectIDFor(*d); pid != "" {
		if _, ok := m.domainCache[pid]; !ok {
			cmds = append(cmds, m.fetchProjectDomains(pid))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// moveCursor moves the list cursor for a navigation key.
func (m *Model) moveCursor(key string, rows int) {
	switch key {
	case "j", "down":
		m.depCursor = clamp(m.depCursor+1, 0, rows-1)
	case "k", "up":
		m.depCursor = clamp(m.depCursor-1, 0, rows-1)
	case "g", "home":
		m.depCursor = 0
	case "G", "end":
		m.depCursor = rows - 1
	}
}

// handleLoginKey reads the token the user pastes or types.
func (m Model) handleLoginKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m, tea.Quit
	case "enter":
		if m.tokenBuf != "" {
			m.loading = true
			return m, validateToken(m.tokenBuf)
		}
	case "o":
		// a shortcut only while the field is empty; otherwise it is a
		// character like any other
		if m.tokenBuf == "" {
			return m, openBrowser("https://vercel.com/account/tokens")
		}
	case "q":
		if m.tokenBuf == "" {
			return m, tea.Quit
		}
	}
	m.tokenBuf, _ = typeText(m.tokenBuf, msg)
	return m, nil
}

// handleFilterKey edits the list filter.
func (m Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.filter, m.filterFocus, m.depCursor = m.filterBuf, false, 0
		return m, nil
	case "esc":
		m.filterBuf, m.filterFocus = "", false
		return m, nil
	}
	m.filterBuf, _ = typeText(m.filterBuf, msg)
	return m, nil
}

// handleEnvFormKey edits the environment variable form.
func (m Model) handleEnvFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m = m.closeEnvForm()
		return m, nil
	case "tab":
		if m.envEditID == "" {
			m.envField = (m.envField + 1) % 2
		}
		return m, nil
	case "t":
		m.envPreset = m.nextEnvPreset()
		return m, nil
	case "enter":
		switch {
		case m.envField == keyField && m.envEditID == "":
			if m.envKey != "" {
				m.envField = valueField
			}
		case m.envValue != "":
			m.envForm = false
			return m, m.submitEnv()
		}
		return m, nil
	}
	m.typeEnvForm(msg)
	return m, nil
}

// handleSearchKey edits the log search term.
func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.search, m.searchFocus = m.searchBuf, false
		m.lastMatch = max(m.logTopIndex()-1, -1)
		m.searchNext()
		return m, nil
	case "esc":
		m.searchBuf, m.searchFocus = "", false
		return m, nil
	}
	m.searchBuf, _ = typeText(m.searchBuf, msg)
	return m, nil
}

// handleTeamKey drives the team picker. Switching to another team drops a
// project scope that belonged to the old one, then reloads in the new scope.
func (m Model) handleTeamKey(key string) (tea.Model, tea.Cmd) {
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
		if m.teamCursor == m.teamIdx {
			m.teamSel = false
			return m, nil
		}
		m.teamIdx = m.teamCursor
		m.depCursor = 0
		m.teamSel = false
		if m.projectID != "" && m.teamID() != m.orgID {
			m.projectID = ""
			m.orgID = ""
		}
		m.rescope()
		return m, m.loadCurrent()
	}
	return m, nil
}

// handleDeploymentsKey drives the deployment list.
func (m Model) handleDeploymentsKey(key string) (tea.Model, tea.Cmd) {
	rows := m.displayRows()
	switch key {
	case "j", "down", "k", "up", "g", "home", "G", "end":
		m.moveCursor(key, len(rows))
		cmd := m.refreshDetail()
		return m, cmd
	case "enter":
		if d := m.selectedDep(); d != nil {
			if m.detail == nil || m.detail.Key() != d.Key() {
				m.detail = d
			}
			m.mode = modeActions
			m.actionCursor = 0
		}
	case "e":
		if d := m.selectedDep(); d != nil {
			pid := m.projectIDFor(*d)
			if pid == "" {
				return m, nil
			}
			m.envProject = api.Project{Name: d.Name, ID: pid}
			m.mode = modeEnvs
			m.envCursor = 0
			return m, m.fetchEnvs()
		}
	case "E":
		if m.depCursor < len(rows) && rows[m.depCursor].project != "" {
			if m.expanded == rows[m.depCursor].project {
				m.expanded = ""
			} else {
				m.expanded = rows[m.depCursor].project
			}
		}
	case "L":
		if d := m.selectedDep(); d != nil {
			pid := m.projectIDFor(*d)
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
		if m.projectID == "" {
			return m, nil
		}
		return m.unlinkCmd()
	case "l", "R", "c", "o", "x", "B", "D":
		if d := m.selectedDep(); d != nil {
			return m.runActionKey(key, *d)
		}
	}
	return m, nil
}

// handleActionsKey drives the deployment actions menu.
func (m Model) handleActionsKey(key string) (tea.Model, tea.Cmd) {
	if m.detail == nil {
		m.mode = modeDeployments
		return m, nil
	}
	actions := actionsFor(*m.detail)
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
			return m.runActionKey(actions[m.actionCursor].key, *m.detail)
		}
	}
	return m, nil
}

// handleEnvsKey drives the environment variable list.
func (m Model) handleEnvsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = modeDeployments
	case "j", "down":
		m.envCursor = clamp(m.envCursor+1, 0, len(m.envs)-1)
	case "k", "up":
		m.envCursor = clamp(m.envCursor-1, 0, len(m.envs)-1)
	case "n":
		m = m.newEnvForm()
	case "e":
		if m.envCursor < len(m.envs) {
			m = m.editEnvForm(m.envs[m.envCursor])
		}
	case "d":
		if m.envCursor < len(m.envs) {
			m.pending, m.pendingEnv, m.confirmInput = pendDeleteEnv, m.envs[m.envCursor], ""
		}
	}
	return m, nil
}

// handleLogsKey drives the log view.
func (m Model) handleLogsKey(key string) (tea.Model, tea.Cmd) {
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

// stateFilter is the state the list is filtered to, empty when there is none.
// stateFilters[0] is the "no filter" entry, so index 0 means the same thing as
// being out of range.
func (m Model) stateFilter() string {
	if m.stateIdx <= 0 || m.stateIdx >= len(stateFilters) {
		return ""
	}
	return stateFilters[m.stateIdx]
}

func (m Model) visibleDeps() []api.Deployment {
	var out []api.Deployment
	state := m.stateFilter()
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

// displayRow is one row of the deployments board: a project head row (project
// set, dep is that project's latest deployment) or a deployment under an
// expanded project (dep only).
type displayRow struct {
	project string
	dep     *api.Deployment
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
	var rows []displayRow
	for _, g := range groupByProject(deps) {
		rows = append(rows, displayRow{project: g.name, dep: &g.deployments[0]})
		if g.name == m.expanded {
			for i := range g.deployments {
				rows = append(rows, displayRow{dep: &g.deployments[i]})
			}
		}
	}
	return rows
}

// projectGroup is one project and its deployments, newest first.
type projectGroup struct {
	name        string
	deployments []api.Deployment
}

// groupByProject groups deployments by project, newest group first (matching
// display order). Each group keeps its deployments newest-first.
func groupByProject(deps []api.Deployment) []projectGroup {
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

// projectGroups returns the visible deployments, grouped.
func (m Model) projectGroups() []projectGroup { return groupByProject(m.visibleDeps()) }

// selectedDep returns the deployment at the cursor. Project head rows
// carry their latest deployment, so this is nil only off-list.
func (m Model) selectedDep() *api.Deployment {
	rows := m.displayRows()
	if m.depCursor < 0 || m.depCursor >= len(rows) {
		return nil
	}
	return rows[m.depCursor].dep
}
