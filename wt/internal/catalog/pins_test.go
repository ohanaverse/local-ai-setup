// Tests that pin catalog's table-based answers to the packages that own the
// same rules. They are an external test package because those packages
// (lifecycle, localmodels) import catalog, and an in-package test may not
// import its importers.
package catalog_test

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// tableIDs is every family name and provider id the table knows, plus ids it
// does not, so a pin covers both sides.
func tableIDs() []string {
	ids := []string{"llamacpp", "nope", "", "omlx-6bit"}
	for _, f := range provider.Families() {
		ids = append(ids, f)
		ids = append(ids, provider.FamilyProviderIDs(f)...)
	}
	return ids
}

// TestStartableMatchesLifecycle pins catalog's table-based answer to the
// lifecycle engine's own: Row.Action must read ActionStart for exactly the
// providers lifecycle.Startable accepts, for every family member and for
// provider ids the table does not know. A drift would hide a model wt can
// start or offer one it cannot.
func TestStartableMatchesLifecycle(t *testing.T) {
	for _, id := range tableIDs() {
		if got, want := catalog.StartableForTest(id), lifecycle.Startable(id); got != want {
			t.Errorf("startable(%q) = %v, lifecycle.Startable = %v", id, got, want)
		}
	}
}

// TestRunningOnlyMatchesLocalmodels pins catalog's running-only answer, which
// picks a blocked row's wording (the mlx_lm_server pairing's start command or
// the provider's own tool), to localmodels.RunningOnly, which decides whether
// such a model gets a row at all. A drift would give a hidden pin and a
// blocked row two different reasons for the same model.
func TestRunningOnlyMatchesLocalmodels(t *testing.T) {
	for _, id := range tableIDs() {
		if got, want := catalog.RunningOnlyForTest(id), localmodels.RunningOnly(id); got != want {
			t.Errorf("runningOnly(%q) = %v, localmodels.RunningOnly = %v", id, got, want)
		}
	}
}
