package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// aliasListFixture is `wt-model alias list --json`'s contract fixture at the
// monorepo root (style guide J3): one document in the shape the command
// prints, with an alias whose window is known, routed and in use, and one
// whose window is withheld (null), not routed and unused.
const aliasListFixture = "../../../docs/contracts/alias-list.sample.json"

// TestAliasListJSONMatchesFixture pins `wt-model alias list --json` to its
// fixture byte for byte: the same registry, database rows and client
// sessions give exactly the fixture's document (version, the aliases with
// id, targets {model, order}, context, sessions and routed, and the
// warnings array always present, two-space indent). A key added or renamed
// fails here until the fixture changes in the same change; and the fixture
// holds no litellm_params, which no alias output may carry.
func TestAliasListJSONMatchesFixture(t *testing.T) {
	h, fake, _ := aliasEnv(t, aliasRegistry+`
[[aliases]]
id = "alias/main"
targets = [{ model = "openrouter/big" }, { model = "openrouter/small", order = 2 }]

[[aliases]]
id = "alias/spare"
targets = [{ model = "openrouter/unknown" }]
`)
	fake.seedAliasRow("alias/main", "openrouter/big", 1)
	fake.seedAliasRow("alias/main", "openrouter/small", 2)
	stubSessions(h, map[string]int{"alias/main": 2})
	out, errOut, err := execAlias(t, h, "alias", "list", "--json")
	if err != nil || errOut != "" {
		t.Fatalf("err %v stderr %q", err, errOut)
	}
	want, err := os.ReadFile(aliasListFixture)
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Fatalf("alias list --json =\n%s\nwant the fixture\n%s", out, want)
	}
	var doc map[string]any
	if err := json.Unmarshal(want, &doc); err != nil {
		t.Fatal(err)
	}
	keys := jsonKeySet(t, want)
	if !reflect.DeepEqual(keys, []string{"aliases", "version", "warnings"}) {
		t.Errorf("top-level keys %v", keys)
	}
}
