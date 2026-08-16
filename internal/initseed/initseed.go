// Package initseed seeds project-level agent instruction files.
package initseed

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Result reports what seeding did.
type Result struct {
	Created []string // files created
	Skipped []string // files that already existed
}

// Root returns the current repo's working-tree root.
func Root() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git working tree")
	}
	return strings.TrimSpace(string(out)), nil
}

// Seed creates AGENTS.md (if missing) and an agent pointer file if the agent
// supports one. Never overwrites existing files.
func Seed(agent, repoRoot string) (*Result, error) {
	res := &Result{}

	agentsPath := filepath.Join(repoRoot, "AGENTS.md")
	created, err := writeIfMissing(agentsPath, agentsTemplate)
	if err != nil {
		return nil, err
	}
	track(res, created, "AGENTS.md")

	pointer, ok := pointerFor(agent)
	if ok {
		ptrPath := filepath.Join(repoRoot, pointer.path)
		created, err := writeIfMissing(ptrPath, pointer.content)
		if err != nil {
			return nil, err
		}
		track(res, created, pointer.path)
	}
	return res, nil
}

func track(res *Result, created bool, name string) {
	if created {
		res.Created = append(res.Created, name)
	} else {
		res.Skipped = append(res.Skipped, name)
	}
}

// writeIfMissing writes content to path only if path does not exist.
func writeIfMissing(path, content string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil // exists — skip
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

type pointer struct {
	path    string
	content string
}

// pointerFor returns the pointer file for an agent, and whether it has one.
func pointerFor(agent string) (pointer, bool) {
	switch agent {
	case "claude":
		return pointer{"CLAUDE.md", "@AGENTS.md\n"}, true
	case "copilot":
		return pointer{".github/copilot-instructions.md", "Read AGENTS.md and follow all instructions in it.\n"}, true
	default:
		return pointer{}, false
	}
}

const agentsTemplate = `# Agent Instructions

> **Uninitialized.** If this is your first time reading this file in a new
> project, ask the user about the project and fill in the sections below.
> Remove this notice once the file has been customized.

## Project

<!-- What does this project do? -->

## Stack

<!-- Language, framework, key dependencies -->

## Commands

<!-- Build, test, lint, deploy -->

## Conventions

<!-- Code style, naming patterns, important rules -->

## Architecture

<!-- Key directories, modules, data flow -->
`
