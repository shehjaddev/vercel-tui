# vercel-tui

k9s-style terminal UI for Vercel: a cross-team overview of deployments,
live logs, and the handful of management actions people actually use.
Work in progress — the roadmap lives in `notes/PLAN.md` until the first
public release.

## Development

    go build ./cmd/vtui && ./vtui

Requires Go 1.27+. The current build shows a fake deployments list; the
real API client lands with the MVP.
