package localmodels

import (
	"slices"
	"testing"
)

// TestOmlxPoolReadsSizesAndFlags verifies the pool reading carries what the
// eviction plan needs: each model's size, pin and last access, and the pool's
// ceiling and total. Without them wt cannot tell a load that fits from one
// that unloads a model a session is using.
func TestOmlxPoolReadsSizesAndFlags(t *testing.T) {
	srv := &fakeOmlx{
		listed: []string{"A", "B"}, pool: map[string]bool{"A": true, "B": false},
		sizes: map[string]int64{"A": 20, "B": 5}, pinned: map[string]bool{"A": true},
		lastAccess: map[string]float64{"A": 100}, ceiling: 64, inUse: 20,
	}
	p, err := OmlxPool(omlxCfgAt(srv.serve(t), ""), testClient)
	if err != nil {
		t.Fatal(err)
	}
	if !p.SizesKnown || p.Ceiling != 64 || p.InUse != 20 {
		t.Errorf("pool = %+v, want sizes known, ceiling 64, in use 20", p)
	}
	a, ok := p.Find("A")
	if !ok || !a.Loaded || !a.Pinned || a.Size != 20 || a.LastAccess != 100 {
		t.Errorf("A = %+v ok=%v, want loaded, pinned, size 20, last access 100", a, ok)
	}
	if got := p.LoadedIDs(); !slices.Equal(got, []string{"A"}) {
		t.Errorf("loaded = %v, want [A]", got)
	}
}

// TestOmlxPoolSizesAnUnloadedModelFromItsEstimate verifies a model omlx has no
// resident size for — one that is not loaded — is sized by estimated_size.
// The start target is such a model: sized zero, every load would be predicted
// to fit, and wt would unload a model in use without asking.
func TestOmlxPoolSizesAnUnloadedModelFromItsEstimate(t *testing.T) {
	srv := &fakeOmlx{
		listed: []string{"A", "B"}, pool: map[string]bool{"A": true, "B": false},
		sizes: map[string]int64{"A": 20}, estimated: map[string]int64{"A": 18, "B": 30},
	}
	p, err := OmlxPool(omlxCfgAt(srv.serve(t), ""), testClient)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := p.Find("A")
	b, ok := p.Find("B")
	if !ok || b.Size != 30 || a.Size != 20 {
		t.Errorf("A size = %d, B = %+v ok=%v; want A's resident 20 and B's estimate 30", a.Size, b, ok)
	}
}

// TestOmlxPoolFallbackNamesALoadedModel verifies the fallback reading — an
// omlx without the status endpoint — still names the model it has loaded, as
// loaded, and says it has no sizes. That is what makes wt ask before a second
// load on such a server instead of assuming it fits.
func TestOmlxPoolFallbackNamesALoadedModel(t *testing.T) {
	srv := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true}, noStatus: true}
	p, err := OmlxPool(omlxCfgAt(srv.serve(t), ""), testClient)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := p.Find("A")
	if p.SizesKnown || !ok || !a.Loaded || !slices.Equal(p.LoadedIDs(), []string{"A"}) {
		t.Errorf("pool = %+v, want A loaded and no sizes", p)
	}
}

// TestOmlxPoolReportsOnDiskIDsForAnAliasedModel pins #213 item 5. omlx lists
// an aliased model under its alias, and wt matches models by directory name,
// so reading "all loaded" off the list made an aliased model read as stopped:
// the picker offered to start a model that was already serving.
func TestOmlxPoolReportsOnDiskIDsForAnAliasedModel(t *testing.T) {
	srv := &fakeOmlx{listed: []string{"my-alias"}, pool: map[string]bool{"Qwen-4bit": true}}
	p, err := OmlxPool(omlxCfgAt(srv.serve(t), ""), testClient)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.LoadedIDs(); !slices.Equal(got, []string{"Qwen-4bit"}) {
		t.Errorf("loaded = %v, want the on-disk id [Qwen-4bit], not the alias", got)
	}
}

// TestOmlxPoolFallsBackWhenStatusIsRefused verifies a keyed omlx whose key the
// registry does not name still yields the loaded set from /health and the
// list, marked as having no sizes. wt must then ask before a second load
// instead of assuming it fits.
func TestOmlxPoolFallsBackWhenStatusIsRefused(t *testing.T) {
	srv := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": false}, key: "sk-omlx"}
	p, err := OmlxPool(omlxCfgAt(srv.serve(t), ""), testClient)
	if err != nil {
		t.Fatalf("idle keyed pool, no key: %v", err)
	}
	if p.SizesKnown || len(p.LoadedIDs()) != 0 {
		t.Errorf("pool = %+v, want no sizes and nothing loaded", p)
	}
}

// TestInventoryCarriesTheOmlxPool verifies the snapshot hands the pool reading
// to its consumers, so the picker's replace question and the start engine's
// plan are computed from the same probe instead of two that can disagree.
func TestInventoryCarriesTheOmlxPool(t *testing.T) {
	srv := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true}, ceiling: 64, inUse: 20}
	cfg := omlxCfgAt(srv.serve(t), "")
	cfg.Providers[0].ModelDir = t.TempDir()
	snap := inventory(cfg, testClient)
	if snap.OmlxPool == nil || snap.OmlxPool.Ceiling != 64 {
		t.Fatalf("snapshot pool = %+v, want the reading with ceiling 64", snap.OmlxPool)
	}
}
