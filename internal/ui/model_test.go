package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbletea"

	"github.com/shehjaddev/vercel-tui/internal/api"
)

// Regression for BUG-1: typed-confirm dialogs must collect keystrokes.
func TestConfirmDialogCollectsTyping(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "")
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
	m := New(api.New("tok"), true, 0, nil, "", "")
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
	m := New(api.New("tok"), true, 0, nil, "", "")
	m.width = 168
	m.deps = []api.Deployment{{
		UID: "dpl_1", Name: "shehjad", State: "READY", Target: "production",
		URL:  "shehjad-x.vercel.app",
		Meta: map[string]string{"githubCommitRef": "main", "githubCommitSha": "a7764a7", "githubCommitMessage": "msg"},
	}}
	m.depCursor = 0
	m.mode = modeDeployments
	model, _ := m.Update(detailMsg{d: &api.Deployment{
		UID: "dpl_1", Name: "shehjad", State: "READY", Target: "production",
		URL: "shehjad-x.vercel.app", Alias: []string{"www.shehjad.dev"},
		Meta: map[string]string{"githubCommitRef": "main", "githubCommitSha": "a7764a7", "githubCommitMessage": "msg"},
	}})
	m = model.(Model)
	out := m.topDetail()
	if !strings.Contains(out, "aliases") || !strings.Contains(out, "www.shehjad.dev") {
		t.Fatalf("aliases missing in top detail:\n%s", out)
	}
}
