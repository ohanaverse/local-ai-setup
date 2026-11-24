package litellm

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// previewTargets is the "<target>@<order>" of each row a preview would write,
// in the form fakeDBServer.aliasTargets uses for the rows a write did.
func previewTargets(rows []DBModel) []string {
	var out []string
	for _, m := range rows {
		target, _ := m.Info[AliasTargetKey].(string)
		out = append(out, fmt.Sprintf("%s@%v", target, m.Params["order"]))
	}
	slices.Sort(out)
	return out
}

// TestPreviewAliasAgreesWithTheRealSyncForASubscriptionTarget pins #703: the
// preview of `alias set --dry-run` used a local-only liveness and no
// subscription check, so it said "not routed" for a Claude subscription
// target the real write routed. PreviewAlias asks the same planner as the
// write, so under a clean check both route the target, and under a hazard
// both leave it unrouted.
func TestPreviewAliasAgreesWithTheRealSyncForASubscriptionTarget(t *testing.T) {
	reg := subAliasCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	o = cleanProxy(o)
	a := reg.Aliases[0]

	rows, skips, err := PreviewAlias(reg, nil, o, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) != 0 || !slices.Equal(previewTargets(rows), []string{subID + "@1"}) {
		t.Fatalf("clean check: preview rows %v skips %v, want the subscription target routed", previewTargets(rows), skips)
	}
	if len(fake.aliasIDs()) != 0 {
		t.Fatalf("the preview wrote alias rows: %v", fake.aliasIDs())
	}
	if _, err := Sync(reg, nil, o); err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets(a.ID); !slices.Equal(got, previewTargets(rows)) {
		t.Errorf("the real write routed %v, the preview said %v", got, previewTargets(rows))
	}

	// A hazard: the preview leaves the target unrouted and says why, as the write does.
	reg2 := subAliasCfg()
	o2, _, _, fake2 := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg2)
	o2 = cleanProxy(o2)
	o2.ProxyEnv = func() ProxyEnv { return ProxyEnvOf("test env", map[string]string{"ANTHROPIC_API_KEY": "wtSENTINEL"}) }
	rows, skips, err = PreviewAlias(reg2, nil, o2, reg2.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || len(skips) != 1 || skips[0].Target != subID || !strings.Contains(skips[0].Reason, "hazard") {
		t.Fatalf("hazard: preview rows %v skips %+v, want the target skipped for the hazard", previewTargets(rows), skips)
	}
	if _, err := Sync(reg2, nil, o2); err != nil {
		t.Fatal(err)
	}
	if got := fake2.aliasIDs(); len(got) != 0 {
		t.Errorf("the real write routed %v under a hazard", got)
	}
}

// TestPreviewAliasQuotesTheUnroutedModelsReason pins the faulty provider row
// (a clean gate over an auth.base_url row): the write's skip is the frozen
// build fault itself, which names the provider row, so the preview's skip
// carries the write's reason verbatim.
func TestPreviewAliasQuotesTheUnroutedModelsReason(t *testing.T) {
	reg := subAliasCfg()
	reg.Providers[len(reg.Providers)-1].Auth.BaseURL = "https://relay.example"
	o, _, _, _ := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	o = cleanProxy(o)
	rows, skips, err := PreviewAlias(reg, nil, o, reg.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || len(skips) != 1 || skips[0].Target != subID {
		t.Fatalf("rows %v skips %+v, want the one unrouted target", previewTargets(rows), skips)
	}
	if !strings.HasPrefix(skips[0].Reason, `model "claude-haiku-5-5": provider `) || !strings.Contains(skips[0].Reason, "auth.base_url") {
		t.Errorf("skip reason = %q, want the frozen build fault naming the provider row", skips[0].Reason)
	}
}

// TestPreviewAliasSaysWhyThePlanCoversNoAliases pins the unlisted database:
// with the proxy pair set but the listing failing, the write reports "alias
// routes unavailable (...)" and writes no rows, so the preview must error
// with that line, not a bare "no plan entry" that names no cause.
func TestPreviewAliasSaysWhyThePlanCoversNoAliases(t *testing.T) {
	reg := subAliasCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	o = cleanProxy(o)
	fake.failList = true
	rows, skips, err := PreviewAlias(reg, nil, o, reg.Aliases[0])
	if err == nil {
		t.Fatalf("rows %v skips %+v, want an error naming the database half", previewTargets(rows), skips)
	}
	if !strings.Contains(err.Error(), "alias routes unavailable (") || strings.Contains(err.Error(), "no plan entry") {
		t.Errorf("err = %v, want the plan's own unavailable line", err)
	}
	if n := apiWrites(fake); n != 0 {
		t.Errorf("the preview made %d API writes", n)
	}
}

// TestPreviewAliasAgreesWithTheRealSyncForALocalTarget pins the local halves
// of the same agreement for a stopped and a running model: with nothing
// running the preview plans no row and says "not running" (the write's skip
// for a local target off the route names it leaves), and with the model
// running both the preview and the write route it.
func TestPreviewAliasAgreesWithTheRealSyncForALocalTarget(t *testing.T) {
	const target = "ollama/gemma:9b"
	reg := testConfig()
	reg.Aliases = []config.Alias{{ID: "alias/gl", Targets: []config.AliasTarget{{Model: target, Order: 1}}}}
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	o = cleanProxy(o)

	rows, skips, err := PreviewAlias(reg, nil, o, reg.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || len(skips) != 1 || skips[0].Target != target || skips[0].Reason != "not running" {
		t.Fatalf("stopped: rows %v skips %+v, want one not-running skip", previewTargets(rows), skips)
	}
	if _, err := Sync(reg, nil, o); err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/gl"); len(got) != 0 {
		t.Fatalf("stopped: the write routed %v, the preview said none", got)
	}

	local := localFor(reg, target)
	rows, skips, err = PreviewAlias(reg, local, o, reg.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) != 0 || !slices.Equal(previewTargets(rows), []string{target + "@1"}) {
		t.Fatalf("running: rows %v skips %+v, want the target routed", previewTargets(rows), skips)
	}
	if _, err := Sync(reg, local, o); err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/gl"); !slices.Equal(got, previewTargets(rows)) {
		t.Errorf("running: the write routed %v, the preview said %v", got, previewTargets(rows))
	}
}
