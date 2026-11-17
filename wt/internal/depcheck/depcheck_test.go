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
}

// sessionBanned is the set cmd/wt-model must not link: the session half.
var sessionBanned = []string{
	"internal/tui", "internal/configeditor", "internal/cleanup",
	"internal/agents", "internal/rotation", "internal/rescue",
	"internal/rootmove", "internal/removal", "internal/modelclient",
}

// allowList names, per session library package, the banned packages it still
// links. It only shrinks: TestSessionLibrariesStayOffTheAdminPackages fails
// when an entry here is no longer linked, so a fixed edge has to be deleted.
// Never add an entry to make a failing build pass; remove the edge instead.
var allowList = map[string][]string{
	"internal/cleanup": {"internal/localmodels"},
	"internal/smoke":   {"internal/localmodels"},
	"internal/catalog": {"internal/localmodels"},
	"internal/survey":  {"internal/lifecycle", "internal/litellm", "internal/localmodels"},
	"internal/tui":     {"internal/lifecycle", "internal/litellm", "internal/localmodels"},
}

// sessionLibraries are the packages the session binary shares with the model
// server's clients; each must stay off adminBanned except through allowList.
var sessionLibraries = []string{
	"internal/cleanup", "internal/agents", "internal/modelclient",
	"internal/smoke", "internal/profiles", "internal/catalog",
	"internal/survey", "internal/tui",
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
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			msg += ": " + string(ee.Stderr)
		}
		t.Fatalf("go list -deps %s: %s", pkg, msg)
	}
	got := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		if rel, ok := strings.CutPrefix(line, modulePath); ok {
			got[rel] = true
		}
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

// TestSessionLibrariesStayOffTheAdminPackages is the ratchet of the
// wt-model/wt-session split: no session library package may link the admin
// packages except the offenders its allowList entry names, and an entry whose
// edge is gone must be removed, so the boundary only tightens.
func TestSessionLibrariesStayOffTheAdminPackages(t *testing.T) {
	for _, pkg := range sessionLibraries {
		t.Run(pkg, func(t *testing.T) {
			allowed := map[string]bool{}
			for _, a := range allowList[pkg] {
				allowed[a] = true
			}
			got := linked(closure(t, pkg), adminBanned)
			seen := map[string]bool{}
			for _, g := range got {
				seen[g] = true
				if !allowed[g] {
					t.Errorf("%s links %s, which the session library packages must not; remove the import edge (do not add it to the allow-list)", pkg, g)
				}
			}
			for a := range allowed {
				if !seen[a] {
					t.Errorf("%s no longer links %s: remove it from the allow-list in depcheck_test.go (the allow-list only shrinks)", pkg, a)
				}
			}
		})
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
