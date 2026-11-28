package config

import (
	"slices"
	"testing"
)

// TestNativeTablesAreConsistent pins the internal shape of the leaf tables
// that derived eligibility reads: NativeOnlyAgents is a subset of
// NativeCapableAgents, neither lists a name twice, and SubscriptionAgents is a
// subset of NativeCapableAgents (the subscription is claude's own login, a
// native provider). A table that drifted out of this shape would make the
// eligibility rule offer a native row to an agent that has none.
func TestNativeTablesAreConsistent(t *testing.T) {
	for name, list := range map[string][]string{
		"NativeOnlyAgents":    NativeOnlyAgents,
		"NativeCapableAgents": NativeCapableAgents,
	} {
		seen := map[string]bool{}
		for _, n := range list {
			if seen[n] {
				t.Errorf("%s lists %q twice", name, n)
			}
			seen[n] = true
		}
	}
	for _, n := range NativeOnlyAgents {
		if !slices.Contains(NativeCapableAgents, n) {
			t.Errorf("NativeOnlyAgents has %q, which NativeCapableAgents lacks", n)
		}
	}
	for _, n := range SubscriptionAgents {
		if !slices.Contains(NativeCapableAgents, n) {
			t.Errorf("SubscriptionAgents has %q, which NativeCapableAgents lacks", n)
		}
	}
}
