package cloudsync

import "fmt"

// The blocks of a plan's Format that both flows print the same way. The plan's
// text is the plan: the command prints it for review and compares it with the
// re-plan made under the registry lock, so a change here changes both flows'
// text together.

// graduatedLines is the "Graduated (manual -> curated)" block, none when no
// manual model graduates.
func graduatedLines(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return idBlock(fmt.Sprintf("Graduated (manual -> curated) (%d):", len(ids)), ids)
}

// keptLines is the "Kept" block for the manual models the source does not
// list (source names it: "openrouter.ai", "ollama.com/pricing"), none when
// there are none.
func keptLines(source string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return idBlock(fmt.Sprintf("Kept (manual, not on %s) (%d):", source, len(ids)), ids)
}

// digestLine is the line that names the removal digest a reviewed dry run
// printed, which `--yes --approve-removals` takes.
func digestLine(digest string) string {
	return fmt.Sprintf("Removal digest: %s (apply non-interactively with `--yes --approve-removals %s`)", digest, digest)
}

// idBlock is a header followed by one two-space-indented id per line.
func idBlock(header string, ids []string) []string {
	lines := []string{header}
	for _, id := range ids {
		lines = append(lines, "  "+id)
	}
	return lines
}
