package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestCleanupPlainSanitisesToolDetails prints, as --plain does, a report
// whose unpushed, gates and claims unavailable details hold git's raw
// stderr (ESC ] ... BEL, a bidi override, CR, LF). Why: those details went
// to the terminal whole, so a hostile or garbled message could retitle it
// or split a row; each row must stay one clean line.
func TestCleanupPlainSanitisesToolDetails(t *testing.T) {
	const hostile = "cleanup: git status: exit 128: fatal: x\x1b]0;pwn\x07 \u202eevil\rmore\nline2"
	r := cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"},
		Fetch: cleanup.FetchResult{OK: true, At: time.Now()}, GH: cleanup.GHOK}
	codes := []string{cleanup.ReasonUnpushedUnknown, cleanup.ReasonGatesUnavailable, cleanup.ReasonClaimsUnavailable}
	for _, code := range codes {
		r.Rows = append(r.Rows, cleanup.Row{Kind: cleanup.KindBranch, Name: "topic-" + code, Branch: "topic-" + code,
			Status: cleanup.StatusLessSure, Unknown: true, Reasons: []cleanup.Reason{{Code: code, Detail: hostile}}})
	}
	var b strings.Builder
	if err := printCleanupPlain(&b, r, time.Now()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, bad := range []string{"\x1b", "\x07", "\u202e", "\r"} {
		if strings.Contains(out, bad) {
			t.Errorf("--plain output holds %q:\n%q", bad, out)
		}
	}
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "line2") || strings.HasPrefix(line, "more") {
			t.Errorf("git's text broke onto its own line %q:\n%s", line, out)
		}
		if strings.Contains(line, "topic-") {
			rows++
			if !strings.Contains(line, "fatal: x]0;pwn evil more line2") {
				t.Errorf("row lost git's words: %q", line)
			}
		}
	}
	if rows != len(codes) {
		t.Errorf("found %d row lines, want %d:\n%s", rows, len(codes), out)
	}
}
