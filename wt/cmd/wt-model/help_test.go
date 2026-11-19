package main

import (
	"strings"
	"testing"
)

// TestHelpNamesSushiWhereItListsProviders pins the two wt-model help texts
// that enumerate the providers a registry write seeds (`wt-model model init`
// and `wt-model model add`): each must name sushi. Without it a built-in
// provider added later is silently missing from the help the way sushi was.
// The `wt stop` half of this check is cmd/wt-session's test of the same name.
func TestHelpNamesSushiWhereItListsProviders(t *testing.T) {
	root := rootCmdWith(fakeDeps, testHost())
	for _, path := range [][]string{{"model", "init"}, {"model", "add"}} {
		c, _, err := root.Find(path)
		if err != nil || c == nil || c.Name() != path[len(path)-1] {
			t.Fatalf("command %v not found: %v", path, err)
		}
		if !strings.Contains(c.Long, "sushi") {
			t.Errorf("%s: Long does not name sushi:\n%s", c.CommandPath(), c.Long)
		}
	}
}
