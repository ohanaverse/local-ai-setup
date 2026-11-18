package depcheck

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/ohanaverse/local-ai-setup/wt/"

// adminBanned is the set of packages the session library packages must not
// link: the model administration half (lifecycle, litellm, localmodels,
// cloudsync, spend, modeladmin, configeditor) and the model server's own
// packages.
var adminBanned = []string{
	"internal/lifecycle", "internal/litellm", "internal/localmodels",
	"internal/cloudsync", "internal/spend", "internal/modeladmin",
	"internal/configeditor", "internal/modelserver", "internal/modelgateway",
	"internal/modeljobs", "internal/sessionreg", "internal/candidates",
	"internal/hoststop", "internal/modelsetup",
}

// sessionBanned is the set cmd/wt-model must not link: the session half.
var sessionBanned = []string{
	"internal/tui", "internal/configeditor", "internal/cleanup",
	"internal/agents", "internal/rotation", "internal/rescue",
	"internal/rootmove", "internal/removal", "internal/modelclient",
}

// adminScreensBanned is what internal/configeditor (`wt config`: the Agents
// and Models tabs, the admin screens) must not link: the launcher's session
// screens. The two programs share styling through internal/themes and sizing
// through internal/tuilayout, never through each other.
var adminScreensBanned = []string{"internal/tui"}

// allowList names, per session library package, the banned packages it still
// links. It only shrinks: TestSessionLibrariesStayOffTheAdminPackages fails
// when an entry here is no longer linked, so a fixed edge has to be deleted.
// Never add an entry to make a failing build pass; remove the edge instead.
var allowList = map[string][]string{}

// sessionLibraries are the packages the session binary shares with the model
// server's clients; each must stay off adminBanned except through allowList.
var sessionLibraries = []string{
	"internal/cleanup", "internal/agents", "internal/modelclient",
	"internal/smoke", "internal/profiles", "internal/catalog",
	"internal/survey", "internal/tui",
}

// listError words a `go list` failure with the command's stderr, so a broken
// package is named instead of a bare "exit status 1".
func listError(err error) string {
	msg := err.Error()
	if ee, ok := err.(*exec.ExitError); ok {
		msg += ": " + string(ee.Stderr)
	}
	return msg
}

// closure returns the internal packages (module-relative paths) that pkg
// links, non-test, by asking `go list -deps`. It skips when go is not on PATH.
func closure(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH; the import-graph ratchet needs `go list`")
	}
	out, err := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", "github.com/ohanaverse/local-ai-setup/wt/"+pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %s", pkg, listError(err))
	}
	got := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		if rel, ok := strings.CutPrefix(line, modulePath); ok {
			got[rel] = true
		}
	}
	if !got[pkg] {
		t.Fatalf("go list -deps %s did not list %s itself; the ratchet would pass vacuously", pkg, pkg)
	}
	return got
}

// linked returns, sorted, the members of banned that set contains (exact
// import paths, never substrings).
func linked(set map[string]bool, banned []string) []string {
	var hit []string
	for _, b := range banned {
		if set[b] {
			hit = append(hit, b)
		}
	}
	sort.Strings(hit)
	return hit
}

// ratchetProblems returns the failures for one session library package: each
// banned package it links that allowed does not name, and each allowed one it
// no longer links. Pure, so a test can feed it a synthetic closure.
func ratchetProblems(pkg string, set map[string]bool, banned, allowedList []string) []string {
	allowed := map[string]bool{}
	for _, a := range allowedList {
		allowed[a] = true
	}
	var problems []string
	seen := map[string]bool{}
	for _, g := range linked(set, banned) {
		seen[g] = true
		if !allowed[g] {
			problems = append(problems, pkg+" links "+g+", which the session library packages must not; remove the import edge (do not add it to the allow-list)")
		}
	}
	entries := append([]string(nil), allowedList...)
	sort.Strings(entries)
	for _, a := range entries {
		if !seen[a] {
			problems = append(problems, pkg+" no longer links "+a+": remove it from the allow-list in depcheck_test.go (the allow-list only shrinks)")
		}
	}
	return problems
}

// TestSessionLibrariesStayOffTheAdminPackages is the ratchet of the
// wt-model/wt-session split: no session library package may link the admin
// packages except the offenders its allowList entry names, and an entry whose
// edge is gone must be removed, so the boundary only tightens.
func TestSessionLibrariesStayOffTheAdminPackages(t *testing.T) {
	for _, pkg := range sessionLibraries {
		t.Run(pkg, func(t *testing.T) {
			for _, p := range ratchetProblems(pkg, closure(t, pkg), adminBanned, allowList[pkg]) {
				t.Error(p)
			}
		})
	}
}

// TestRatchetReportsBothFailureBranches feeds ratchetProblems a synthetic
// closure: a new edge outside the allow-list fails, a stale allow-list entry
// fails, and an exact match is clean.
func TestRatchetReportsBothFailureBranches(t *testing.T) {
	banned := []string{"internal/lifecycle", "internal/litellm"}
	set := map[string]bool{"internal/lifecycle": true, "internal/config": true}
	if got := ratchetProblems("p", set, banned, nil); len(got) != 1 || !strings.Contains(got[0], "p links internal/lifecycle") {
		t.Errorf("a new banned edge: got %v", got)
	}
	if got := ratchetProblems("p", set, banned, []string{"internal/lifecycle", "internal/litellm"}); len(got) != 1 || !strings.Contains(got[0], "no longer links internal/litellm") {
		t.Errorf("a stale allow-list entry: got %v", got)
	}
	if got := ratchetProblems("p", set, banned, []string{"internal/lifecycle"}); len(got) != 0 {
		t.Errorf("an exact match: got %v", got)
	}
}

// otherPackages are the internal packages that are neither a session library
// nor in either banned set: leaves and shared packages the ratchet leaves
// alone. TestEveryInternalPackageIsClassified makes a new package pick a side.
var otherPackages = []string{
	"internal/config", "internal/depcheck", "internal/dirtsnap",
	"internal/exitcode", "internal/gitenv", "internal/gittest",
	"internal/guard", "internal/initseed", "internal/launchwarn",
	"internal/modelapi", "internal/provider", "internal/rootclaim",
	"internal/scratchdir", "internal/sessionclaim", "internal/slotedit",
	"internal/themes", "internal/tiers", "internal/tokenfile",
	"internal/tomlw", "internal/tuilayout", "internal/usage",
	"internal/worktree",
}

// TestEveryInternalPackageIsClassified keeps the ratchet's lists complete:
// every package under internal/ is a session library, banned (either set) or
// named in otherPackages, and no list names a package that is gone.
func TestEveryInternalPackageIsClassified(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH; the import-graph ratchet needs `go list`")
	}
	out, err := exec.Command(goBin, "list", modulePath+"internal/...").Output()
	if err != nil {
		t.Fatalf("go list internal/...: %s", listError(err))
	}
	existing := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		if rel, ok := strings.CutPrefix(line, modulePath); ok {
			existing[rel] = true
		}
	}
	known := map[string]bool{}
	for _, list := range [][]string{adminBanned, sessionBanned, sessionLibraries, otherPackages} {
		for _, p := range list {
			known[p] = true
			if !existing[p] {
				t.Errorf("%s is named in depcheck_test.go but no longer exists (a renamed or deleted package would make the ratchet pass vacuously); update the lists", p)
			}
		}
	}
	for p := range existing {
		if !known[p] {
			t.Errorf("%s is not classified in depcheck_test.go: add it to sessionLibraries, a banned set or otherPackages", p)
		}
	}
}

// TestAllowListNamesBannedPackages keeps the allow-list honest: it may only
// name packages in the admin set, for packages under the ratchet.
func TestAllowListNamesBannedPackages(t *testing.T) {
	inBanned := map[string]bool{}
	for _, b := range adminBanned {
		inBanned[b] = true
	}
	lib := map[string]bool{}
	for _, l := range sessionLibraries {
		lib[l] = true
	}
	for pkg, offenders := range allowList {
		if !lib[pkg] {
			t.Errorf("allow-list names %s, which is not a session library package under the ratchet", pkg)
		}
		for _, o := range offenders {
			if !inBanned[o] {
				t.Errorf("allow-list entry %s -> %s is not in the banned set", pkg, o)
			}
		}
	}
}

// TestModelServerBinaryStaysOffTheSessionPackages keeps cmd/wt-model from
// linking the session half (screens, agents, cleanup, the model client).
func TestModelServerBinaryStaysOffTheSessionPackages(t *testing.T) {
	for _, hit := range linked(closure(t, "cmd/wt-model"), sessionBanned) {
		t.Errorf("cmd/wt-model links %s, which belongs to the session half", hit)
	}
}

// TestAdminScreensStayOffTheSessionScreens keeps internal/configeditor from
// linking internal/tui: the admin screens and the launcher's screens are two
// programs that must not depend on each other, so a screen can move between
// binaries (Phase 4) without dragging the other's UI along. It used to link
// it for two styling helpers, which now live in internal/themes.
func TestAdminScreensStayOffTheSessionScreens(t *testing.T) {
	for _, hit := range linked(closure(t, "internal/configeditor"), adminScreensBanned) {
		t.Errorf("internal/configeditor links %s; share styling through internal/themes or internal/tuilayout instead", hit)
	}
}
