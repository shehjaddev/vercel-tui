package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/shehjaddev/vercel-tui/internal/api"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	headerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	stateStyle = map[string]lipgloss.Style{
		"ready":    okStyle,
		"building": warnStyle,
		"error":    errStyle,
		"canceled": dimStyle,
		"queued":   dimStyle,
	}
)

func (m Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	var b strings.Builder
	b.WriteString(m.statusBar())
	b.WriteString("\n\n")

	switch {
	case m.help:
		b.WriteString(m.helpView())
	case m.envForm:
		b.WriteString(m.envFormView())
	case m.pending != pendNone:
		b.WriteString(m.confirmView())
	case m.teamSel:
		b.WriteString(m.teamView())
	default:
		switch m.mode {
		case modeLogin:
			b.WriteString(m.loginView())
		case modeDeployments:
			b.WriteString(m.deploymentsView())
		case modeProjects:
			b.WriteString(m.projectsView())
		case modeDetail:
			b.WriteString(m.detailView())
		case modeLogs:
			b.WriteString(m.logsView())
		case modeEnvs:
			b.WriteString(m.envVarsView())
		case modeDomains:
			b.WriteString(m.domainsView())
		}
	}

	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) statusBar() string {
	parts := []string{titleStyle.Render("vercel-tui")}
	if m.authed {
		parts = append(parts, "team: "+m.teamName())
	}
	if m.filter != "" {
		parts = append(parts, "filter: "+m.filter)
	}
	if m.mode == modeDeployments && stateFilters[m.stateIdx] != "" {
		parts = append(parts, "state: "+stateFilters[m.stateIdx])
	}
	if m.branchFlag != "" {
		parts = append(parts, "branch: "+m.branchFlag)
	}
	if m.targetFlag != "" {
		parts = append(parts, "target: "+m.targetFlag)
	}
	line := strings.Join(parts, dimStyle.Render(" · "))

	right := ""
	if m.throttled {
		right = warnStyle.Render("throttled")
	} else if m.loading {
		right = dimStyle.Render("refreshing…")
	} else if !m.lastLoad.IsZero() {
		right = dimStyle.Render("updated " + rel(time.Now().Sub(m.lastLoad)))
	}
	gap := max(m.width-lipgloss.Width(line)-lipgloss.Width(right), 1)
	return line + strings.Repeat(" ", gap) + right
}

func (m Model) deploymentsView() string {
	groups := m.projectGroups()
	if len(groups) == 0 {
		return dimStyle.Render("no deployments match") + "\n"
	}

	// --- project status strip (signature element) ---
	var chips []string
	for _, g := range groups {
		chips = append(chips, m.projectChip(g))
	}
	strip := strings.Join(chips, "  ")
	strip = trunc(strip, max(m.width-4, 10))

	// --- body: grouped rows ---
	widths := m.boardWidths()
	var body []string
	body = append(body, headerStyle.Render(row(widths, "", "PROJECT", "STATE", "BRANCH", "COMMIT", "AGE")))
	maxRows := max(m.height-10, 1)
	start := clamp(m.depCursor-maxRows+2, 0, max(len(m.displayRows())-maxRows, 0))
	_ = start

	// simple sequential render of display rows (project heads + expanded children)
	rows := m.displayRows()
	for i := 0; i < len(rows); i++ {
		r := rows[i]
		var cells []string
		if r.project != "" {
			g := m.groupByName(r.project)
			var latest *api.Deployment
			if len(g.deployments) > 0 {
				latest = &g.deployments[0]
			}
			cells = m.boardHeadCells(r.project, latest, widths, m.expanded == r.project)
		} else if r.dep != nil {
			cells = m.boardChildCells(*r.dep, widths, i)
		}
		line := row(widths, cells...)
		if i == m.depCursor {
			line = selectedStyle.Render(line)
		}
		body = append(body, line)
	}

	list := strings.Join(body, "\n")

	// --- bottom detail panel; overview stays clean ---
	detail := m.boardDetailPane()

	head := titleStyle.Render("OPERATIONS") + dimStyle.Render("  ") + dimStyle.Render(rel(time.Now().Sub(m.lastLoad)))
	head += "\n" + strip

	return head + "\n\n" + list + "\n" + detail
}

// projectChip renders one project's recent deploy-state distribution.
func (m Model) projectChip(g projectGroup) string {
	max := 6
	if len(g.deployments) < max {
		max = len(g.deployments)
	}
	var b strings.Builder
	for i := 0; i < max; i++ {
		st := g.deployments[i].Status()
		glyph := "●"
		if st == "canceled" || st == "queued" {
			glyph = "○"
		}
		b.WriteString(stateStyle[st].Render(glyph))
	}
	return dimStyle.Render(g.name) + " " + b.String()
}

func (m Model) groupByName(name string) projectGroup {
	for _, g := range m.projectGroups() {
		if g.name == name {
			return g
		}
	}
	return projectGroup{name: name}
}

// boardWidths sizes the board columns; content-driven, modest, so the
// overview reads tight rather than spread out and cluttered.
func (m Model) boardWidths() []int {
	return []int{1, 20, 10, 22, 9, 10}
}

func (m Model) boardHeadCells(project string, latest *api.Deployment, widths []int, expanded bool) []string {
	flag := "▸"
	if expanded {
		flag = "▾"
	}
	state, branch, sha, age := "", "", "", ""
	if latest != nil {
		st := latest.Status()
		state = stateStyle[st].Render(strings.ToUpper(st))
		branch = latest.Branch()
		sha = latest.ShortSHA()
		age = relAge(latest.CreatedMs())
	}
	return []string{
		flag,
		trunc(project, widths[1]),
		state,
		trunc(branch, widths[3]),
		trunc(sha, widths[4]),
		age,
	}
}

func (m Model) boardChildCells(d api.Deployment, widths []int, index int) []string {
	st := d.Status()
	return []string{
		marker(index == m.depCursor),
		"  " + trunc(d.Name, widths[1]-6),
		stateStyle[st].Render(strings.ToUpper(st)),
		trunc(d.Branch(), widths[3]),
		trunc(d.ShortSHA(), widths[4]),
		relAge(d.CreatedMs()),
	}
}

func (m Model) boardDetailPane() string {
	d := m.selectedDep()
	if d == nil {
		return ""
	}
	sep := dimStyle.Render(strings.Repeat("─", 40))
	var out strings.Builder
	out.WriteString(sep + "\n")
	out.WriteString(titleStyle.Render(d.Name) + "  " + dimStyle.Render(d.ShortSHA()) + "\n")
	out.WriteString(dimStyle.Render(trunc(d.Message(), 48)) + "\n")
	out.WriteString("\n")
	label := func(k, v string) string {
		return dimStyle.Render(fmt.Sprintf("%-10s", k)) + "  " + v + "\n"
	}
	out.WriteString(label("Status", stateStyle[d.Status()].Render(strings.ToUpper(d.Status()))))
	out.WriteString(label("Target", targetLabel(d.Target)))
	out.WriteString(label("Branch", d.Branch()))
	out.WriteString(label("Author", d.Creator.Username))
	out.WriteString(label("Created", absTime(d.CreatedMs())))
	if t := d.ReadyMs(); t > 0 {
		out.WriteString(label("Duration", duration(d.Duration())))
	}
	return out.String()
}

// depWidths returns column widths, distributing spare horizontal space into
// the flexible PROJECT and BRANCH columns so the table fills the terminal.
func (m Model) depWidths() []int {
	base := []int{1, 19, 11, 10, 25, 9, 11, 10, 8}
	if m.width <= 0 {
		return base
	}
	total := 0
	for _, w := range base {
		total += w
	}
	if spare := m.width - total - 1; spare > 0 {
		// widen only the PROJECT column (names genuinely vary); leave the
		// rest content-sized so short values like branch names don't create
		// a huge internal void.
		base[1] += spare
	}
	return base
}

func (m Model) projectsView() string {
	if len(m.projects) == 0 {
		return dimStyle.Render("no projects") + "\n"
	}
	widths := m.projWidths()
	rows := []string{headerStyle.Render(row(widths, "", "PROJECT", "FRAMEWORK", "REPO", "LAST ACTIVITY"))}
	maxRows := max(m.height-8, 1)
	start := clamp(m.projCursor-maxRows+2, 0, max(len(m.projects)-maxRows, 0))
	for i := start; i < min(start+maxRows, len(m.projects)); i++ {
		p := m.projects[i]
		cells := []string{
			marker(i == m.projCursor),
			trunc(p.Name, widths[1]),
			trunc(p.Framework, widths[2]),
			trunc(p.Repo(), widths[3]),
			relAge(p.UpdatedAt),
		}
		line := row(widths, cells...)
		if i == m.projCursor {
			line = selectedStyle.Render(line)
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n") + "\n"
}

// projWidths spreads spare width into PROJECT and REPO so long repo paths
// and empty cells read as aligned columns rather than ragged holes.
func (m Model) projWidths() []int {
	base := []int{1, 27, 17, 33, 12}
	if m.width <= 0 {
		return base
	}
	total := 0
	for _, w := range base {
		total += w
	}
	if spare := m.width - total - 1; spare > 0 {
		base[1] += spare
	}
	return base
}

func (m Model) detailView() string {
	d := m.detail
	if d == nil {
		return ""
	}
	kv := func(k, v string) string {
		return headerStyle.Render(fmt.Sprintf("%-12s", k)) + trunc(v, max(m.width-14, 20))
	}
	lines := []string{
		titleStyle.Render(d.Name),
		"",
		kv("state", stateStyle[d.Status()].Render(d.Status())),
		kv("target", targetLabel(d.Target)),
		kv("url", "https://"+d.URL),
	}
	if len(d.Alias) > 0 {
		lines = append(lines, kv("aliases", strings.Join(d.Alias, ", ")))
	}
	lines = append(lines,
		kv("branch", d.Branch()+" ("+d.ShortSHA()+")"),
		kv("commit", d.Message()),
		kv("author", d.Creator.Username),
		kv("created", absTime(d.CreatedMs())),
	)
	if t := d.ReadyMs(); t > 0 {
		lines = append(lines, kv("ready", absTime(t)+" ("+duration(d.Duration())+")"))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m Model) logsView() string {
	head := titleStyle.Render("logs — "+m.detail.Name) + dimStyle.Render("  ("+m.detail.ShortSHA()+")")
	if len(m.logs) == 0 {
		return head + "\n" + dimStyle.Render("waiting for events…") + "\n"
	}
	visible := max(m.height-6, 1)
	total := len(m.logs)
	end := total - m.logScroll
	start := max(end-visible, 0)
	follow := ""
	if m.logScroll > 0 {
		follow = dimStyle.Render(fmt.Sprintf("  scrolled (%d/%d)", m.logScroll, max(total-visible, 0)))
	}
	count := dimStyle.Render(fmt.Sprintf("  %d lines", total))
	search := ""
	if m.searchFocus {
		search = "\n" + "/" + m.searchBuf + "█"
	} else if m.search != "" {
		search = "\n" + dimStyle.Render("search: "+m.search+"  (n next, / re-edit)")
	}
	return head + count + follow + search + "\n" + strings.Join(m.logs[start:end], "\n") + "\n"
}

func (m Model) envVarsView() string {
	head := titleStyle.Render("env vars — " + m.envProject.Name)
	if len(m.envs) == 0 {
		return head + "\n" + dimStyle.Render("no environment variables (n to create)") + "\n"
	}
	rows := []string{headerStyle.Render(row(envWidths, "", "KEY", "TARGETS", "TYPE", "UPDATED"))}
	maxRows := max(m.height-8, 1)
	start := clamp(m.envCursor-maxRows+2, 0, max(len(m.envs)-maxRows, 0))
	for i := start; i < min(start+maxRows, len(m.envs)); i++ {
		e := m.envs[i]
		targets := trunc(strings.Join(e.Target, ", "), 28)
		if e.Sensitive() {
			targets += dimStyle.Render(" ·write-only")
		}
		cells := []string{
			marker(i == m.envCursor),
			trunc(e.Key, 30),
			targets,
			e.Type,
			relAge(int64(e.UpdatedAt)),
		}
		line := row(envWidths, cells...)
		if i == m.envCursor {
			line = selectedStyle.Render(line)
		}
		rows = append(rows, line)
	}
	return head + "\n" + strings.Join(rows, "\n") + "\n"
}

func (m Model) envFormView() string {
	editing := m.envEditID != ""
	title := titleStyle.Render("New environment variable for " + m.envProject.Name)
	if editing {
		title = titleStyle.Render("Edit value of " + m.envKeyLabel() +
			" on " + m.envProject.Name)
	}
	keyLine := marker(!editing && m.envField == 0) + " key:   " + m.envKey
	valueLine := marker(editing || m.envField == 1) + " value: " + m.envValue
	hint := ""
	if editing {
		keyLine = dimStyle.Render("  key:   " + m.envKeyLabel())
		hint = dimStyle.Render("  (the stored value is not readable; type the new one)")
	}
	return strings.Join([]string{
		title,
		"",
		keyLine,
		valueLine + hint,
		"targets: " + warnStyle.Render(targetPresets[m.envPreset].label) + dimStyle.Render("  (t cycles)"),
		"",
		dimStyle.Render("enter next/save · tab switch field · esc cancel"),
	}, "\n") + "\n"
}

func (m Model) envKeyLabel() string {
	for _, e := range m.envs {
		if e.ID == m.envEditID {
			return e.Key
		}
	}
	return "?"
}

func (m Model) domainsView() string {
	scope := "team domains"
	if m.projectID != "" {
		scope = "domains of scoped project"
	}
	head := titleStyle.Render("domains — " + scope)
	if len(m.domains) == 0 {
		return head + "\n" + dimStyle.Render("no domains") + "\n"
	}
	rows := []string{headerStyle.Render(row(domWidths, "NAME", "VERIFIED", "CREATED"))}
	for _, d := range m.domains {
		verified := okStyle.Render("yes")
		if !d.Verified {
			verified = errStyle.Render("NO")
		}
		rows = append(rows, row(domWidths, trunc(d.Name, 40), verified, relAge(int64(d.CreatedAt))))
	}
	return head + "\n" + strings.Join(rows, "\n") + "\n"
}

func (m Model) loginView() string {
	return strings.Join([]string{
		titleStyle.Render("Login to Vercel"),
		"",
		"No token found. Press o to open vercel.com/account/tokens in your browser,",
		"create a token, then paste it below and press enter.",
		"It will be stored under ~/.config/vtui/token.",
		"",
		"token: " + m.tokenBuf,
	}, "\n") + "\n"
}

func (m Model) confirmView() string {
	var title, body string
	switch m.pending {
	case pendCancel:
		title = "Cancel build"
		body = "Cancel the running build of " + m.pendingDep.Name + " (" + m.pendingDep.ShortSHA() + ")?"
	case pendDelete:
		title = errStyle.Render("Delete deployment")
		body = "This permanently removes " + m.pendingDep.Name + " (" + m.pendingDep.ShortSHA() + ").\n" +
			"Type \"" + m.pendingDep.Name + "\" to confirm: " + m.confirmInput
	case pendRedeploy:
		title = "Redeploy"
		body = "Rebuild the same commit of " + m.pendingDep.Name + " (" + m.pendingDep.ShortSHA() + ")?"
	case pendRollback:
		title = warnStyle.Render("Instant rollback — PRODUCTION")
		body = "Promote " + m.pendingDep.ShortSHA() + " back to production?\n" +
			"Traffic switches immediately. Enter to confirm, esc to abort."
	case pendDeleteEnv:
		title = errStyle.Render("Delete environment variable")
		body = "Remove " + m.pendingEnv.Key + " from " + m.envProject.Name + "?\n" +
			"Type \"" + m.pendingEnv.Key + "\" to confirm: " + m.confirmInput
	}
	return strings.Join([]string{
		titleStyle.Render(title),
		"",
		body,
		"",
		dimStyle.Render("enter confirm · esc cancel"),
	}, "\n") + "\n"
}

func (m Model) teamView() string {
	rows := []string{titleStyle.Render("Switch team"), ""}
	for i, t := range m.teams {
		line := marker(i == m.teamCursor) + " " + t.Name
		if i == m.teamCursor {
			line = selectedStyle.Render(line)
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n") + "\n"
}

func (m Model) helpView() string {
	return strings.Join([]string{
		titleStyle.Render("Keys"),
		"",
		"1/2/3    deployments / projects / domains",
		"j k g G  navigate",
		"enter    open selected deployment detail",
		"e        expand the selected project's deployments",
		"a        toggle: grouped-by-project vs all deployments",
		"l        live logs of the selected deployment",
		"/        filter (lists) or log search   n  next match",
		"e        env vars of selected project",
		"L        link selected project to ./vercel/project.json",
		"x        cancel building deployment      D  delete (typed confirm)",
		"R        redeploy same commit            B  instant rollback (prod)",
		"c        copy deployment URL to clipboard",
		"t        switch team             r  refresh now",
		"o        open in browser         q  quit",
		"?        close this overlay",
	}, "\n") + "\n"
}

func (m Model) footer() string {
	hints := map[mode]string{
		modeLogin:       "type/paste token · enter save · o open browser · q quit",
		modeDeployments: "j/k move · e expand · enter detail · l logs · a all/list · x cancel · R redeploy · B rollback · D delete · / filter · s state · t team · c copy · o open · ? help · q quit",
		modeProjects:    "j/k move · enter scope · e env vars · L link to dir · / filter · t team · ? help · q quit",
		modeDetail:      "esc back · l logs · R redeploy · D delete · c copy url · o open · q quit",
		modeEnvs:        "j/k move · n new · e edit value · d delete · esc back · q quit",
		modeDomains:     "esc back · q quit",
	}
	modeLogsHints := "j/k scroll · / search · n next match · c copy url · esc back · q quit"
	hint := hints[m.mode]
	// context-dependent actions only advertise when they can fire
	addHint := func(k, label string) { hint += " · " + k + " " + label }
	if m.mode == modeDeployments && m.depCursor < len(m.visibleDeps()) {
		d := m.visibleDeps()[m.depCursor]
		if d.Status() == "building" {
			addHint("x", "cancel")
		}
		if d.Status() == "ready" && d.Target == "production" {
			addHint("B", "rollback")
		}
	}
	if m.mode == modeDetail && m.detail != nil {
		if m.detail.Status() == "building" {
			addHint("x", "cancel")
		}
		if m.detail.Status() == "ready" && m.detail.Target == "production" {
			addHint("B", "rollback")
		}
	}
	line := dimStyle.Render(hint)
	if m.mode == modeLogs {
		line = dimStyle.Render(modeLogsHints)
	}
	if m.note != "" {
		line = okStyle.Render(m.note)
	}
	if m.err != "" {
		line = errStyle.Render(trunc(m.err, max(m.width-4, 20)))
	}
	return line
}

var envWidths = []int{1, 31, 29, 11, 12}
var domWidths = []int{41, 9, 12}

func row(widths []int, cells ...string) string {
	var b strings.Builder
	for i, c := range cells {
		w := widths[min(i, len(widths)-1)]
		b.WriteString(pad(c, w))
	}
	return strings.TrimRight(b.String(), " ")
}

// pad right-aligns to a visible width, ignoring ANSI escape codes.
func pad(s string, w int) string {
	visible := lipgloss.Width(s) // strips ANSI so color escapes don't inflate width
	if visible >= w {
		return s
	}
	return s + strings.Repeat(" ", w-visible)
}

func marker(selected bool) string {
	if selected {
		return ">"
	}
	return ""
}

func targetLabel(t string) string {
	if t == "" {
		return "preview"
	}
	return t
}

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}

func relAge(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return rel(time.Since(time.UnixMilli(ms)))
}

func rel(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func absTime(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return time.UnixMilli(ms).Format("Jan 2 15:04")
}

func duration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
