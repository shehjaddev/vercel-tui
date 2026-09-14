package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbletea"

	"github.com/shehjaddev/vercel-tui/internal/api"
)

func TestRedeployTargetNormalization(t *testing.T) {
	for target, want := range map[string]string{
		"production":  "production",
		"preview":     "preview",
		"development": "development",
		"PRODUCTION":  "production",
		"":            "",
		"staging":     "",
	} {
		if got := redeployTarget(api.Deployment{Target: target}); got != want {
			t.Errorf("redeployTarget(%q) = %q, want %q", target, got, want)
		}
	}
}

func TestTeamsMsgSelectsLinkedTeam(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.orgID, m.projectID = "team_2", "prj_1"
	model, _ := m.Update(teamsMsg{user: "u", teams: []api.Team{{ID: "team_1", Name: "one"}, {ID: "team_2", Name: "two"}}})
	got := model.(Model)
	if got.teamIdx != 2 {
		t.Fatalf("teamIdx = %d, want 2 (linked team)", got.teamIdx)
	}
}

func TestTeamSwitchClearsForeignScope(t *testing.T) {
	newScoped := func() Model {
		m := New(api.New("tok"), true, 0, nil, "", "", ".")
		m.teams = []api.Team{{Name: "u (personal)"}, {ID: "team_1", Name: "one"}, {ID: "team_2", Name: "two"}}
		m.teamIdx = 1
		m.orgID, m.projectID = "team_1", "prj_1"
		m.teamSel, m.teamCursor = true, 2
		return m
	}
	model, _ := newScoped().Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := model.(Model)
	if got.teamIdx != 2 {
		t.Fatalf("teamIdx = %d, want 2", got.teamIdx)
	}
	if got.projectID != "" || got.orgID != "" {
		t.Fatalf("foreign scope not cleared: projectID=%q orgID=%q", got.projectID, got.orgID)
	}

	m := newScoped()
	m.teamSel, m.teamCursor = true, 1
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = model.(Model)
	if got.projectID != "prj_1" {
		t.Fatalf("matching scope dropped: projectID=%q", got.projectID)
	}
}

func TestStaleDepsMsgDropped(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.projectID = "prj_1"
	deps := []api.Deployment{{UID: "dpl_1", Name: "web"}}
	model, _ := m.Update(depsMsg{deps: deps, team: "other", project: "prj_1"})
	if got := model.(Model); len(got.deps) != 0 {
		t.Fatalf("stale deps applied: %+v", got.deps)
	}
	model, _ = m.Update(depsMsg{deps: deps, team: "", project: "prj_1"})
	if got := model.(Model); len(got.deps) != 1 {
		t.Fatalf("current deps dropped: %+v", got.deps)
	}
}

func TestStaleLogsMsgDropped(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.detail = &api.Deployment{UID: "dpl_1", Name: "web"}
	model, _ := m.Update(logsMsg{lines: []string{"old"}, id: "dpl_2"})
	if got := model.(Model); len(got.logs) != 0 {
		t.Fatalf("stale logs applied: %q", got.logs)
	}
	model, _ = m.Update(logsMsg{lines: []string{"new"}, id: "dpl_1"})
	if got := model.(Model); len(got.logs) != 1 {
		t.Fatalf("current logs dropped: %q", got.logs)
	}
}

func TestRefreshSetsLoading(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.width, m.height = 80, 24
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if got := model.(Model); !got.loading {
		t.Fatalf("loading not set on manual refresh")
	}
}

func TestLoginEnterValidates(t *testing.T) {
	m := New(api.New("tok"), false, 0, nil, "", "", ".")
	m.tokenBuf = "abc"
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := model.(Model)
	if !got.loading {
		t.Fatalf("loading not set while validating token")
	}
	if !strings.Contains(got.loginView(), "validating") {
		t.Fatalf("login view missing validating state:\n%s", got.loginView())
	}
}

// Regression for BUG-1: typed-confirm dialogs must collect keystrokes.
func TestConfirmDialogCollectsTyping(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.width, m.height = 80, 24
	m.pending, m.pendingDep = pendDelete, api.Deployment{Name: "web", UID: "dpl_1"}
	for _, k := range strings.Split("we", "") {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = model.(Model)
	}
	if m.confirmInput != "we" {
		t.Fatalf("confirmInput = %q, want %q", m.confirmInput, "we")
	}
	// wrong/incomplete text must not fire the action
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(Model)
	if m.pending != pendDelete || cmd != nil {
		t.Fatalf("delete fired with incomplete confirm input")
	}
	// complete the name, then enter must fire
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = model.(Model)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("delete did not fire with exact name")
	}
}

// Regression for BUG-2: enter on a deployment opens the actions menu,
// not a detail view.
func TestEnterOpensActions(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.width, m.height = 80, 24
	m.mode = modeDeployments
	m.deps = []api.Deployment{{Name: "web", UID: "dpl_1", URL: "web.vercel.sh", State: "READY", Target: "production"}}
	m.depCursor = 0
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := model.(Model)
	if got.mode != modeActions {
		t.Fatalf("mode = %v, want modeActions", got.mode)
	}
	if got.detail == nil || got.detail.Name != "web" {
		t.Fatalf("detail not set for actions menu")
	}
}

// The top detail block must surface aliases from the enriched detail.
func TestAliasInTopDetail(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.width = 168
	m.deps = []api.Deployment{{
		UID: "dpl_1", Name: "shehjad", State: "READY", Target: "production",
		URL:  "shehjad-x.vercel.app",
		Meta: api.StringMap{"githubCommitRef": "main", "githubCommitSha": "a7764a7", "githubCommitMessage": "msg"},
	}}
	m.depCursor = 0
	m.mode = modeDeployments
	model, _ := m.Update(detailsMsg{byKey: map[string]api.Deployment{"dpl_1": {
		UID: "dpl_1", Name: "shehjad", State: "READY", Target: "production",
		URL: "shehjad-x.vercel.app", Alias: []string{"www.shehjad.dev"},
		Meta: api.StringMap{"githubCommitRef": "main", "githubCommitSha": "a7764a7", "githubCommitMessage": "msg"},
	}}})
	m = model.(Model)
	// aliases are intentionally not rendered in the top detail anymore, but
	// the data is still cached so it can be re-added later.
	if strings.Contains(m.topDetail(), "aliases") {
		t.Fatalf("aliases should not render in top detail:\n%s", m.topDetail())
	}
	if cached, ok := m.detailCache["dpl_1"]; !ok || len(cached.Alias) == 0 {
		t.Fatalf("alias data should remain in the cache")
	}
}

func TestUnlinkClearsScopeAndFile(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, ".vercel", "project.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte(`{"projectId":"prj_x","orgId":"org_y"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(api.New("tok"), true, 0, nil, "", "", dir)
	m.projectID, m.orgID = "prj_x", "org_y"
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'U'}})
	got := model.(Model)
	if got.projectID != "" || got.orgID != "" {
		t.Fatalf("scope not cleared: projectID=%q orgID=%q", got.projectID, got.orgID)
	}
	if _, err := os.Stat(link); !os.IsNotExist(err) {
		t.Fatalf("link file should be removed: %v", err)
	}
}

// The enrichment chains run in the background for every project on the
// board: a batch that makes no progress must end the chain instead of
// asking the API for the same thing on every refresh.
func TestPrefetchStopsWhenNothingNew(t *testing.T) {
	head := api.Deployment{UID: "d1", Name: "web", State: "READY"}
	newModel := func() Model {
		m := New(api.New("tok"), true, 0, nil, "", "", ".")
		m.width, m.height = 80, 24
		m.deps = []api.Deployment{head}
		return m
	}

	// a project with no domains is answered, not retried
	m := newModel()
	m.detailCache = map[string]api.Deployment{"d1": {UID: "d1", Project: api.Project{ID: "prj_1"}}}
	if cmd := m.fetchNextDomains(); cmd == nil {
		t.Fatal("first domain batch should fetch")
	}
	model, cmd := m.Update(projDomainsMsg{domains: map[string][]string{"prj_1": {}}})
	if cmd != nil {
		t.Fatal("domain chain kept running after the project was answered")
	}
	if names, ok := model.(Model).domainCache["prj_1"]; !ok || len(names) != 0 {
		t.Fatalf("empty domain list not cached: %v", model.(Model).domainCache)
	}

	// a failed enrichment is spent too, so it is not retried every refresh
	m = newModel()
	if cmd := m.fetchNextHeads(); cmd == nil {
		t.Fatal("first head batch should fetch")
	}
	model, cmd = m.Update(detailsMsg{byKey: map[string]api.Deployment{}, tried: []string{"d1"}})
	if cmd != nil {
		t.Fatal("head chain kept running after a failed enrichment")
	}
	if cmd := model.(Model).fetchNextHeads(); cmd != nil {
		t.Fatal("failed head requested again")
	}

	// and an enriched head is not requested twice
	model, _ = newModel().Update(detailsMsg{byKey: map[string]api.Deployment{
		"d1": {UID: "d1", Project: api.Project{ID: "prj_1"}},
	}})
	if cmd := model.(Model).fetchNextHeads(); cmd != nil {
		t.Fatal("enriched head requested again")
	}
}

// A refresh can return fewer lines than the scroll offset the user was on;
// the view must not slice past the end of the new log.
func TestLogsShrinkClampsScroll(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.width, m.height = 80, 24
	m.mode = modeLogs
	m.detail = &api.Deployment{UID: "d1", Name: "web"}
	m.logs = make([]string, 500)
	m.logScroll = 400

	model, _ := m.Update(logsMsg{id: "d1", lines: []string{"one", "two"}})
	got := model.(Model)
	if got.logScroll != 0 {
		t.Fatalf("logScroll = %d, want 0 after the log shrank", got.logScroll)
	}
	if view := got.View(); !strings.Contains(view, "one") {
		t.Fatalf("shrunken log not rendered:\n%s", view)
	}
}

// Editing a value must not rewrite targets the user never chose: a variable
// on production and preview kept coming back on production alone.
func TestEnvEditKeepsStoredTargets(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.width, m.height = 80, 24
	m.mode = modeEnvs
	m.envProject = api.Project{Name: "web", ID: "prj_1"}
	m.envs = []api.EnvVar{{
		ID: "env_1", Key: "API_KEY", Type: "encrypted",
		Target: []string{"production", "preview"},
	}}

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	got := model.(Model)
	if got.envPreset != -1 {
		t.Fatalf("envPreset = %d, want -1 (keep the stored targets)", got.envPreset)
	}
	if label := got.envTargetsLabel(); label != "production, preview (unchanged)" {
		t.Fatalf("target label = %q", label)
	}

	// cycling reaches every preset and wraps back to keeping the stored ones
	for _, want := range []int{0, 1, 2, 3, -1} {
		model, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
		got = model.(Model)
		if got.envPreset != want {
			t.Fatalf("cycle: envPreset = %d, want %d", got.envPreset, want)
		}
	}
}

// Dialogs and text fields read keystrokes themselves, which used to trap
// ctrl+c: no mode could be left except with esc and a quit was unreachable.
func TestCtrlCQuitsFromEveryInputMode(t *testing.T) {
	base := func() Model {
		m := New(api.New("tok"), true, 0, nil, "", "", ".")
		m.width, m.height = 80, 24
		return m
	}
	cases := map[string]func() Model{
		"deployments":    func() Model { return base() },
		"login":          func() Model { return New(api.New("tok"), false, 0, nil, "", "", ".") },
		"confirm dialog": func() Model { m := base(); m.pending = pendDelete; return m },
		"list filter":    func() Model { m := base(); m.filterFocus = true; return m },
		"log search":     func() Model { m := base(); m.searchFocus = true; return m },
		"env form":       func() Model { m := base(); m.envForm = true; return m },
		"env edit form":  func() Model { m := base(); m.envForm, m.envEditID = true, "env_1"; return m },
		"team picker":    func() Model { m := base(); m.teamSel = true; return m },
		"help overlay":   func() Model { m := base(); m.help = true; return m },
		"actions menu":   func() Model { m := base(); m.mode = modeActions; return m },
	}
	for name, build := range cases {
		_, cmd := build().Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil {
			t.Errorf("%s: ctrl+c does nothing", name)
			continue
		}
		if _, quit := cmd().(tea.QuitMsg); !quit {
			t.Errorf("%s: ctrl+c produced %T, want tea.QuitMsg", name, cmd())
		}
	}
}

// Every text field goes through the same editor; each one still has to take
// the keystrokes the user types and give back the last rune on backspace.
func TestTypedInputReachesEveryField(t *testing.T) {
	fields := []struct {
		name  string
		build func() Model
		read  func(Model) string
	}{
		{"login", func() Model { return New(api.New("tok"), false, 0, nil, "", "", ".") },
			func(m Model) string { return m.tokenBuf }},
		{"list filter", func() Model { m := newTestModel(); m.filterFocus = true; return m },
			func(m Model) string { return m.filterBuf }},
		{"log search", func() Model { m := newTestModel(); m.searchFocus = true; return m },
			func(m Model) string { return m.searchBuf }},
		{"delete confirm", func() Model { m := newTestModel(); m.pending = pendDelete; return m },
			func(m Model) string { return m.confirmInput }},
		{"env form", func() Model { return newTestModel().newEnvForm() },
			func(m Model) string { return m.envKey }},
	}

	for _, f := range fields {
		m := f.build()
		for _, r := range "ab" {
			model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = model.(Model)
		}
		if got := f.read(m); got != "ab" {
			t.Errorf("%s: buffer = %q after typing \"ab\", want \"ab\"", f.name, got)
		}
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = model.(Model)
		if got := f.read(m); got != "a" {
			t.Errorf("%s: buffer = %q after backspace, want \"a\"", f.name, got)
		}
	}
}

// newTestModel is a model wired to a throwaway client, sized so the list and
// log views have room to lay out.
func newTestModel() Model {
	m := New(api.New("tok"), true, 0, nil, "", "", ".")
	m.width, m.height = 80, 24
	return m
}

// key builds the message a printable keypress produces.
func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// Each view reads its own keys, so the dispatch has to land on the right one.
func TestKeysReachTheirOwnView(t *testing.T) {
	t.Run("deployments", func(t *testing.T) {
		m := newTestModel()
		m.deps = []api.Deployment{{UID: "d1", Name: "a"}, {UID: "d2", Name: "b"}}
		model, _ := m.Update(key('j'))
		if got := model.(Model); got.depCursor != 1 {
			t.Fatalf("depCursor = %d, want 1", got.depCursor)
		}
	})

	t.Run("actions", func(t *testing.T) {
		m := newTestModel()
		m.mode = modeActions
		m.detail = &api.Deployment{UID: "d1", Name: "a", State: "READY"}
		model, _ := m.Update(key('j'))
		if got := model.(Model); got.actionCursor != 1 {
			t.Fatalf("actionCursor = %d, want 1", got.actionCursor)
		}
	})

	t.Run("envs", func(t *testing.T) {
		m := newTestModel()
		m.mode = modeEnvs
		m.envs = []api.EnvVar{{ID: "e1", Key: "A"}, {ID: "e2", Key: "B"}}
		model, _ := m.Update(key('j'))
		if got := model.(Model); got.envCursor != 1 {
			t.Fatalf("envCursor = %d, want 1", got.envCursor)
		}
	})

	t.Run("logs", func(t *testing.T) {
		m := newTestModel()
		m.mode = modeLogs
		m.detail = &api.Deployment{UID: "d1", Name: "a"}
		m.logs = make([]string, 100)
		model, _ := m.Update(key('k'))
		if got := model.(Model); got.logScroll != 1 {
			t.Fatalf("logScroll = %d, want 1", got.logScroll)
		}
	})
}

// Navigating onto a row whose detail is already cached must not ask for it
// again, and must show the enriched detail straight away.
func TestNavigationUsesCachedDetail(t *testing.T) {
	m := newTestModel()
	m.deps = []api.Deployment{{UID: "d1", Name: "web", State: "READY"}}
	m.detailCache = map[string]api.Deployment{"d1": {UID: "d1", Project: api.Project{ID: "prj_1"}}}
	m.domainCache = map[string][]string{"prj_1": {"example.com"}}

	model, cmd := m.Update(key('j'))
	got := model.(Model)
	if cmd != nil {
		t.Fatal("navigating onto a cached row issued a request")
	}
	if got.detail == nil || got.detail.Key() != "d1" {
		t.Fatalf("detail = %+v, want the cached d1", got.detail)
	}
}
