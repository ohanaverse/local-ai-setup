package cloudsync

import "testing"

// TestCloudSyncIgnoresSubscriptionProvider pins that cloud-sync never touches
// a claude-subscription model: no flow covers the provider (so an add writes
// no source key), OpenRouter does not price it, and the price and catalog
// plans count, change and remove nothing of it. It has no price to refresh
// and nothing that lists it, so a removal would delete a model the owner
// added by hand.
func TestCloudSyncIgnoresSubscriptionProvider(t *testing.T) {
	e := Entry{ID: "claude-haiku-5-5", Family: "claude", ProviderID: "claude-subscription", ModelName: "claude-haiku-5-5", Location: "cloud"}
	if CoveredBySync(e.ProviderID, e.Location, e.ModelName) || CoveredBySync(e.ProviderID, "", e.ModelName) {
		t.Error("CoveredBySync covers claude-subscription")
	}
	if OpenRouterPriced(e) {
		t.Error("OpenRouterPriced prices claude-subscription")
	}
	plan := PlanPrices([]Entry{e}, map[string]APIPrice{})
	if plan.Candidates != 0 || len(plan.Removals) != 0 || len(plan.Matched) != 0 || len(plan.Kept) != 0 {
		t.Errorf("PlanPrices = %+v, want nothing planned for a subscription model", plan)
	}
	cat := PlanCatalog([]Entry{e}, Catalog{}, nil, nil)
	if cat != nil && (len(cat.Removals) != 0) {
		t.Errorf("PlanCatalog = %+v, want no removal of a subscription model", cat)
	}
}
