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
	selectedStyle = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("238"))
	headerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	valueStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("231"))
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

// column describes one table column. The header and the rows of a table are
// built from the same list, so a width can't drift away from its title.
type column struct {
	title string
	width int
}

// columns is a table's columns, in render order.
type columns []column

func (c columns) widths() []int {
	out := make([]int, len(c))
	for i, col := range c {
		out[i] = col.width
	}
	return out
}

func (c columns) titles() []string {
	out := make([]string, len(c))
	for i, col := range c {
		out[i] = col.title
	}
	return out
}

// The board is sized for its content: a name, a state, a branch, a short sha
// and two relative times, tight enough to read as one block.
var boardColumns = columns{
	{title: "", width: 2},
	{title: "PROJECT", width: 20},
	{title: "STATE", width: 10},
	{title: "BRANCH", width: 22},
	{title: "COMMIT", width: 9},
	{title: "AGE", width: 10},
	{title: "ACTIVITY", width: 11},
}

var envColumns = columns{
	{title: "", width: 2},
	{title: "KEY", width: 31},
	{title: "TARGETS", width: 29},
	{title: "TYPE", width: 11},
	{title: "UPDATED", width: 12},
}

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
		case modeActions:
			b.WriteString(m.actionsView())
		case modeLogs:
			b.WriteString(m.logsView())
		case modeEnvs:
			b.WriteString(m.envVarsView())
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
	if state := m.stateFilter(); m.mode == modeDeployments && state != "" {
		parts = append(parts, "state: "+state)
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
		right = dimStyle.Render("updated " + rel(time.Since(m.lastLoad)))
	}
	gap := max(m.width-lipgloss.Width(line)-lipgloss.Width(right), 1)
	return line + strings.Repeat(" ", gap) + right
}

func (m Model) deploymentsView() string {
	rows := m.displayRows()
	if len(rows) == 0 {
		return dimStyle.Render("no deployments match") + "\n"
	}

	// --- top detail block (pinned, 2-3 lines) ---
	detail := m.topDetail()

	// --- list ---
	widths := boardColumns.widths()
	var body []string
	body = append(body, headerStyle.Render(row(widths, boardColumns.titles()...)))
	maxRows := max(m.height-9, 1)
	start := clamp(m.depCursor-maxRows+2, 0, max(len(rows)-maxRows, 0))
	for i := start; i < min(start+maxRows, len(rows)); i++ {
		r := rows[i]
		sel := i == m.depCursor
		var cells []string
		switch {
		case r.project != "":
			// a head row summarises the project with its latest deployment
			cells = boardCells(trunc(r.project, widths[1]), *r.dep, widths, sel)
		case r.dep != nil:
			cells = boardCells(childIndent+trunc(r.dep.Name, widths[1]-6), *r.dep, widths, sel)
		}
		line := row(widths, cells...)
		if sel {
			line = selectedStyle.Render(line)
		}
		body = append(body, line)
	}

	list := strings.Join(body, "\n")

	return detail + m.topDetailSeparator() + list
}

// topDetail is the pinned block above the list: what the selected deployment
// is, what it was built from, and where it was published.
func (m Model) topDetail() string {
	d := m.selectedDep()
	if d == nil {
		return ""
	}
	// prefer the cached enriched detail for this selection
	if cached, ok := m.detailCache[d.Key()]; ok {
		d = &cached
	}

	head := []string{
		titleStyle.Render(d.Name),
		stateStyle[d.Status()].Render(strings.ToUpper(d.Status())),
		valueStyle.Render(d.Branch()),
		valueStyle.Render(d.ShortSHA()),
		valueStyle.Render(targetLabel(d.Target)),
	}
	if r := d.Repo(); r != "" {
		head = append(head, valueStyle.Render(r))
	}
	lines := []string{strings.Join(head, "  ")}

	if msg := d.Message(); msg != "" {
		// one line only: an embedded newline would break the block apart
		lines = append(lines, dimStyle.Render("commit")+" "+
			valueStyle.Render(trunc(strings.ReplaceAll(msg, "\n", " "), max(m.width-12, 24))))
	}

	meta := []string{valueStyle.Render(d.Creator.Username), valueStyle.Render(absTime(d.CreatedMs()))}
	if t := d.ReadyMs(); t > 0 {
		meta = append(meta, valueStyle.Render(absTime(t)+" · "+duration(d.Duration())))
	}
	lines = append(lines, dimStyle.Render("by")+" "+strings.Join(meta, "   "))

	if d.URL != "" {
		lines = append(lines, dimStyle.Render("url")+" "+
			valueStyle.Render(trunc("https://"+d.URL, max(m.width-8, 30))))
	}

	// domains are cached per project once the detail fetch has landed
	if names := m.domainCache[d.Project.ID]; len(names) > 0 {
		lines = append(lines, dimStyle.Render("domains")+" "+
			valueStyle.Render(trunc(strings.Join(names, ", "), max(m.width-8, 30))))
	}

	return strings.Join(lines, "\n")
}

// topDetailSeparator renders the gap + divider between the detail block
// and the list, so the two read as separate regions.
func (m Model) topDetailSeparator() string {
	return "\n" + dimStyle.Render(strings.Repeat("─", min(m.width-2, 60))) + "\n"
}

// stateCell renders a deployment state. A selected row already carries the
// highlight, so its state keeps the plain text instead of its own color.
func stateCell(state string, selected bool) string {
	if selected {
		return strings.ToUpper(state)
	}
	return stateStyle[state].Render(strings.ToUpper(state))
}

// boardMarker is the marker for the board, where the rows that are not under
// the cursor carry one extra cell. That indent pulls the cursor row left of
// the names above and below it, which is what makes it stand out.
func boardMarker(selected bool) string {
	if selected {
		return marker(true)
	}
	return marker(false) + " "
}

// boardCells lays out one row of the deployments board. The project column is
// the project name on a head row, and the deployment's own name — indented
// under its project — on a child row.
func boardCells(projectColumn string, d api.Deployment, widths []int, selected bool) []string {
	return []string{
		boardMarker(selected),
		projectColumn,
		stateCell(d.Status(), selected),
		trunc(d.Branch(), widths[3]),
		trunc(d.ShortSHA(), widths[4]),
		relAge(d.CreatedMs()),
		relAge(d.LastActivityMs()),
	}
}

func (m Model) actionsView() string {
	d := m.detail
	if d == nil {
		return ""
	}
	actions := actionsFor(*d)
	var out strings.Builder
	out.WriteString(titleStyle.Render("Actions — "+d.Name) + "\n\n")
	for i, a := range actions {
		line := a.label + dimStyle.Render("  ("+a.key+")")
		if i == m.actionCursor {
			out.WriteString(selectedStyle.Render(marker(true)+line) + "\n")
		} else {
			out.WriteString(marker(false) + line + "\n")
		}
	}
	out.WriteString("\n" + dimStyle.Render("enter run · j/k move · esc back"))
	return out.String()
}

func (m Model) logsView() string {
	if m.detail == nil {
		return dimStyle.Render("waiting for events…") + "\n"
	}
	head := titleStyle.Render("logs — "+m.detail.Name) + dimStyle.Render("  ("+m.detail.ShortSHA()+")")
	if len(m.logs) == 0 {
		return head + "\n" + dimStyle.Render("waiting for events…") + "\n"
	}
	visible := m.logViewport()
	total := len(m.logs)
	end := total - m.logScroll
	start := max(end-visible, 0)
	follow := ""
	if m.logScroll > 0 {
		follow = dimStyle.Render(fmt.Sprintf("  scrolled (%d/%d)", m.logScroll, m.logMaxScroll()))
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
	rows := []string{headerStyle.Render(row(envColumns.widths(), envColumns.titles()...))}
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
		line := row(envColumns.widths(), cells...)
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
	keyLine := marker(!editing && m.envField == keyField) + "key:   " + m.envKey
	valueLine := marker(editing || m.envField == valueField) + "value: " + m.envValue
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
		"targets: " + warnStyle.Render(m.envTargetsLabel()) + dimStyle.Render("  (t cycles)"),
		"",
		dimStyle.Render("enter next/save · tab switch field · esc cancel"),
	}, "\n") + "\n"
}

// envByID is the variable the form is editing, if the list still holds it.
func (m Model) envByID(id string) (api.EnvVar, bool) {
	for _, e := range m.envs {
		if e.ID == id {
			return e, true
		}
	}
	return api.EnvVar{}, false
}

func (m Model) envKeyLabel() string {
	if e, ok := m.envByID(m.envEditID); ok {
		return e.Key
	}
	return "?"
}

// envTargetsLabel describes the target choice the env form will save. While
// editing, the default choice is to keep whatever the variable already has.
func (m Model) envTargetsLabel() string {
	if m.envPreset >= 0 {
		return targetPresets[m.envPreset].label
	}
	if names := m.envTargets(); len(names) > 0 {
		return strings.Join(names, ", ") + " (unchanged)"
	}
	return "(unchanged)"
}

// envTargets returns the stored targets of the variable being edited.
func (m Model) envTargets() []string {
	e, _ := m.envByID(m.envEditID)
	return e.Target
}

func (m Model) loginView() string {
	status := "token: " + m.tokenBuf
	if m.loading {
		status = dimStyle.Render("validating token…")
	}
	return strings.Join([]string{
		titleStyle.Render("Login to Vercel"),
		"",
		"No token found. Press o to open vercel.com/account/tokens in your browser,",
		"create a token, then paste it below and press enter.",
		"It will be stored under ~/.config/vtui/token.",
		"",
		status,
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
		line := marker(i == m.teamCursor) + t.Name
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
		"j k g G  navigate",
		"a        toggle: grouped-by-project vs all deployments",
		"E        expand the selected project's deployments",
		"/        filter (lists) or log search   n  next match",
		"s        cycle state filter",
		"enter    open actions for selected deployment",
		"l        live logs of the selected deployment",
		"e        env vars of selected project",
		"L        link selected project to ./vercel/project.json",
		"U        unlink: clear the project scope",
		"x        cancel a building deployment",
		"R        redeploy the same commit",
		"B        instant rollback (production)",
		"D        delete (type the project name to confirm)",
		"c        copy deployment URL to clipboard",
		"o        open in browser",
		"t        switch team",
		"r        refresh now",
		"?        close this overlay",
		"q        quit",
	}, "\n") + "\n"
}

func (m Model) footer() string {
	hints := map[mode]string{
		modeLogin:       "type/paste token · enter save · o open browser · q quit",
		modeDeployments: "j/k move · a all/list · E expand · / filter · s state · enter actions · l logs · e env vars · L link to dir · U unlink · R redeploy · D delete · c copy · o open · t team · ? help · q quit",
		modeActions:     "j/k move · enter run · esc back · q quit",
		modeEnvs:        "j/k move · n new · e edit value · d delete · esc back · q quit",
	}
	modeLogsHints := "j/k scroll · / search · n next match · c copy url · esc back · q quit"
	hint := hints[m.mode]
	// actions that come and go advertise themselves only when they can fire
	if d := m.selectedDep(); d != nil && (m.mode == modeDeployments || m.mode == modeActions) {
		for _, a := range actionsFor(*d) {
			if a.hint != "" && a.needs != nil {
				hint += " · " + a.key + " " + a.hint
			}
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

func row(widths []int, cells ...string) string {
	var b strings.Builder
	for i, c := range cells {
		w := widths[min(i, len(widths)-1)]
		b.WriteString(pad(c, w))
	}
	return strings.TrimRight(b.String(), " ")
}

// pad left-aligns s and spaces it out to w visible columns, ignoring ANSI
// escape codes.
func pad(s string, w int) string {
	visible := lipgloss.Width(s) // strips ANSI so color escapes don't inflate width
	if visible >= w {
		return s
	}
	return s + strings.Repeat(" ", w-visible)
}

// childIndent sets an expanded project's deployments in from the header.
const childIndent = "  "

// markerGap separates a row's marker from its first character.
const markerGap = " "

// marker is the cursor marker in front of a selected row: the glyph and a gap,
// or the same two cells of blank, so rows line up whether or not they carry it.
func marker(selected bool) string {
	if selected {
		return "❯" + markerGap
	}
	return " " + markerGap
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
	if d < 0 {
		d = 0
	}
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
