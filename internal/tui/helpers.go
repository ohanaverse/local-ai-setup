package tui

import "github.com/ohanaverse/agent-worktree/internal/config"

// firstOrDefault returns the first comma-delimited tag from s, or
// fallback if s is empty. It mirrors cmd/wt/launch.go's firstOrDefault
// by design — both paths derive the rotation slot's tag component from
// the same filter semantics. Kept in the tui package to avoid a
// cmd/wt → internal/tui import cycle (launch.go lives in package main).
func firstOrDefault(s, fallback string) string {
	parts := config.ParseFilterList(s)
	if len(parts) == 0 {
		return fallback
	}
	return parts[0]
}
