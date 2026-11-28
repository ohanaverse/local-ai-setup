package main

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// aliasRegistryForToml is aliasRegistry holding a good alias spelled without
// orders, a redirect to it and a faulty alias (its target is gone).
const aliasRegistryForToml = aliasRegistry + `
[[aliases]]
id = "alias/a"
targets = [{ model = "openrouter/big" }, { model = "openrouter/small" }]

[[aliases]]
id = "alias/old"
redirect_to = "alias/a"

[[aliases]]
id = "alias/gone"
targets = [{ model = "openrouter/missing" }]
`

// TestAliasListTOMLRoundTripsThroughApply pins the point of --toml: its
// stdout, saved to a file, is exactly what `alias apply` reads and judges
// "no change" for every printed alias (exit 0, no route sync), even though
// the registry spells orders differently. The stderr note names the two
// skipped aliases (the redirect and the faulty one) so the user knows what
// the file leaves out.
func TestAliasListTOMLRoundTripsThroughApply(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistryForToml)
	out, errOut, err := execAlias(t, h, "alias", "list", "--toml")
	if err != nil {
		t.Fatalf("list --toml: %v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(errOut, "wt-model: note: 2 aliases skipped: alias/old (") || !strings.Contains(errOut, "alias/gone (") {
		t.Errorf("stderr %q does not name the two skipped", errOut)
	}
	if strings.Contains(out, "alias/old") || strings.Contains(out, "alias/gone") || strings.Contains(out, "note:") {
		t.Errorf("stdout holds skipped aliases or the note:\n%s", out)
	}
	syncs := countRouteSync(h)
	applyOut, applyErr, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, out))
	if err != nil || applyOut != "no change: alias/a\n" || *syncs != 0 {
		t.Fatalf("apply: err %v stdout %q stderr %q syncs %d", err, applyOut, applyErr, *syncs)
	}
}

// TestAliasListTOMLIsExclusiveWithJSON pins that --toml and --json together
// are a usage error (exit 1, naming both flags, the usage text shown as for
// `alias set` with no target), not one silently winning; no TOML or table is
// printed.
func TestAliasListTOMLIsExclusiveWithJSON(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistryForToml)
	out, _, err := execAlias(t, h, "alias", "list", "--toml", "--json")
	if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), "--toml") || !strings.Contains(err.Error(), "--json") {
		t.Fatalf("err %v (exit %d) stdout %q", err, exitcode.Of(err), out)
	}
	if strings.Contains(out, "[[aliases]]") || strings.Contains(out, "ALIAS ") {
		t.Errorf("stdout holds output despite the error:\n%s", out)
	}
}

// TestAliasListTOMLWithNoAliasesPrintsNothing pins that a registry with no
// alias exports an empty stdout (not the "no aliases" hint line the table
// prints), so redirecting it to a file gives an empty file and no stderr note.
func TestAliasListTOMLWithNoAliasesPrintsNothing(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistry)
	out, errOut, err := execAlias(t, h, "alias", "list", "--toml")
	if err != nil || out != "" || errOut != "" {
		t.Fatalf("err %v stdout %q stderr %q", err, out, errOut)
	}
}
