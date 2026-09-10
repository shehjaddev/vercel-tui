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
