package rootclaim

import (
	"os/exec"
	"testing"
)

// gitRun runs one git command in dir and fails the test when it fails.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestDriftTable pins every message shape Drift renders and their precedence:
// `root is on <branch>, not <default>` is the one interpolated line (the same
// text the launch note and the root row's reason detail carry), `root is
// detached` is literal, and "" means no drift to speak of. Why the precedence
// rows: with no default branch known there is nothing to be off of — no drift
// is invented there (Review Focus 1), so "" wins even over a detached HEAD
// rather than guessing a default.
func TestDriftTable(t *testing.T) {
	cases := []struct {
		name string
		site Site
		def  string
		want string
	}{
		{"root on the default", Site{Path: "/r", Branch: "main"}, "main", ""},
		{"root on another branch", Site{Path: "/r", Branch: "topic"}, "main", "root is on topic, not main"},
		{"detached root", Site{Path: "/r"}, "main", "root is detached"},
		{"no default branch known", Site{Path: "/r", Branch: "topic"}, "", ""},
		{"detached root, no default branch known", Site{Path: "/r"}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Drift(tc.site, tc.def); got != tc.want {
				t.Fatalf("Drift(%+v, %q) = %q, want %q", tc.site, tc.def, got, tc.want)
			}
		})
	}
}

// TestDriftIsLive reads the drift twice around a branch switch in one
// repository and requires the second read to follow the switch. Why: drift is
// computed from what git holds now, never cached — a stale first answer would
// keep saying `root is on topic, not main` after the root moved back to the
// default, and a list or rescan built on it would say the same.
func TestDriftIsLive(t *testing.T) {
	root := repo(t)
	gitRun(t, root, "checkout", "-q", "-b", "topic")

	first, err := LocateSite(root)
	if err != nil {
		t.Fatalf("LocateSite: %v", err)
	}
	if got := Drift(first, "main"); got != "root is on topic, not main" {
		t.Fatalf("first read = %q, want %q", got, "root is on topic, not main")
	}

	gitRun(t, root, "checkout", "-q", "main")

	second, err := LocateSite(root)
	if err != nil {
		t.Fatalf("LocateSite: %v", err)
	}
	if got := Drift(second, "main"); got != "" {
		t.Fatalf("second read = %q, want %q: the branch switch must be seen", got, "")
	}
}
