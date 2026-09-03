package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shehjaddev/vercel-tui/internal/api"
	"github.com/shehjaddev/vercel-tui/internal/config"
	"github.com/shehjaddev/vercel-tui/internal/ui"
)

func main() {
	var (
		tokenFlag = flag.String("token", "", "vercel api token")
		target    = flag.String("target", "", "filter by target: production or preview")
		branch    = flag.String("branch", "", "filter by git branch")
		refresh   = flag.Duration("refresh", 5*time.Second, "poll interval, 0 disables")
		dir       = flag.String("dir", ".", "directory holding .vercel/project.json")
	)
	flag.Parse()

	token := config.ResolveToken(*tokenFlag)
	link, _ := config.LoadProjectLink(*dir)

	client := api.New(token)
	// always wire the refresh: it only fires on 403, and it reads the CLI's
	// refresh token (absent for VERCEL_TOKEN/--token, where it's a no-op).
	client.SetRefresh(config.RefreshVercelToken)
	m := ui.New(client, token != "", *refresh, link, *target, *branch)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
