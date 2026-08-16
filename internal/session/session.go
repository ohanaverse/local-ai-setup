package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Session identifies a resumable session.
type Session struct {
	ID    string // file stem (claude) or dir name (opencode)
	Path  string // full path
	MTime time.Time
}

// Slug converts an absolute path to the session dir slug: every char outside
// [a-zA-Z0-9-] becomes '-'.
var nonSlug = regexp.MustCompile(`[^a-zA-Z0-9-]`)

func Slug(path string) string {
	return nonSlug.ReplaceAllString(path, "-")
}

// LatestClaude returns the most recently modified claude session for path.
func LatestClaude(path string) (*Session, error) {
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", Slug(path))
	return latestByExt(dir, ".jsonl", func(f os.FileInfo) string {
		return strings.TrimSuffix(f.Name(), ".jsonl")
	})
}

// OpenCodeProjectID returns the repo's root commit hash (opencode's project id).
// It runs git inside path so the id matches the worktree being resumed.
func OpenCodeProjectID(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "rev-list", "--max-parents=0", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// LatestOpenCode returns the most recently modified opencode session for path.
// OpenCode stores sessions as .json files (not .jsonl) under the project id.
func LatestOpenCode(path string) (*Session, error) {
	projectID, err := OpenCodeProjectID(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode",
		"storage", "session", projectID)
	return latestByExt(dir, ".json", func(f os.FileInfo) string {
		return f.Name()
	})
}

// latestByExt finds the newest file with the given extension in dir.
func latestByExt(dir, ext string, idOf func(os.FileInfo) string) (*Session, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil // no sessions
	}
	if err != nil {
		return nil, err
	}

	var sessions []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sessions = append(sessions, Session{
			ID:    idOf(info),
			Path:  filepath.Join(dir, e.Name()),
			MTime: info.ModTime(),
		})
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].MTime.After(sessions[j].MTime)
	})
	return &sessions[0], nil
}

// RelativeTime renders a duration as "just now", "5m ago", etc.
func RelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
	}
}

// LatestForAgent returns the newest session for agent in worktreePath.
func LatestForAgent(agent, worktreePath string) (*Session, error) {
	switch agent {
	case "claude":
		return LatestClaude(worktreePath)
	case "opencode":
		return LatestOpenCode(worktreePath)
	default:
		return nil, nil
	}
}
