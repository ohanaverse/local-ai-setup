package lifecycle

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// TestBackendsMatchProviderTable keeps the lifecycle engine and internal/provider's
// table describing the same providers: every family the table says wt can start
// has a backend whose tenancy matches the table, every family it says wt cannot
// start has none, and no backend exists for a family the table does not know.
// Before this, a new single-model backend could be startable while the table (or
// the occupancy check) reported no occupant, replacing a running model without a
// confirmation.
func TestBackendsMatchProviderTable(t *testing.T) {
	for _, family := range provider.Families() {
		spec, _ := provider.FamilyByName(family)
		b, hasBackend := backendsByFamily[family]
		if spec.Tenancy == NoTenancy {
			if hasBackend {
				t.Errorf("family %q has a lifecycle backend but the table gives it no tenancy", family)
			}
		} else {
			if !hasBackend {
				t.Errorf("family %q has tenancy %d in the table but no lifecycle backend", family, spec.Tenancy)
			} else if b.tenancy() != spec.Tenancy {
				t.Errorf("family %q: backend tenancy %d, table says %d", family, b.tenancy(), spec.Tenancy)
			}
		}
		for _, id := range provider.FamilyProviderIDs(family) {
			if got, want := Startable(id), spec.Tenancy != NoTenancy; got != want {
				t.Errorf("Startable(%q) = %v, want %v", id, got, want)
			}
		}
	}
	for family := range backendsByFamily {
		if _, ok := provider.FamilyByName(family); !ok {
			t.Errorf("lifecycle has a backend for %q, which is not in internal/provider's table", family)
		}
	}
}
