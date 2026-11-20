package main

import (
	"strings"
	"testing"
)

// TestHelpNamesSushiWhereItListsProviders pins the wt-model help texts that
// enumerate providers: the ones a registry write seeds (`wt-model model init`
// and `wt-model model add`) and the ones it stops (`wt-model stop`). Each must
// name sushi. Without it a built-in provider added later is silently missing
// from the help the way sushi was.
func TestHelpNamesSushiWhereItListsProviders(t *testing.T) {
	root := rootCmdWith(fakeDeps, testHost())
	for _, path := range [][]string{{"model", "init"}, {"model", "add"}, {"stop"}} {
		c, _, err := root.Find(path)
		if err != nil || c == nil || c.Name() != path[len(path)-1] {
			t.Fatalf("command %v not found: %v", path, err)
		}
		if !strings.Contains(c.Long, "sushi") {
			t.Errorf("%s: Long does not name sushi:\n%s", c.CommandPath(), c.Long)
		}
	}
}
