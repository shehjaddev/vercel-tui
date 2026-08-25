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

// Regression for BUG-2: detailMsg must switch to the detail view.
func TestDetailMsgShowsDetail(t *testing.T) {
	m := New(api.New("tok"), true, 0, nil, "", "")
	m.width, m.height = 80, 24
	m.mode = modeDeployments
	model, _ := m.Update(detailMsg{d: &api.Deployment{Name: "web", URL: "web.vercel.sh"}})
	if got := model.(Model).mode; got != modeDetail {
		t.Fatalf("mode = %v, want modeDetail", got)
	}
}
