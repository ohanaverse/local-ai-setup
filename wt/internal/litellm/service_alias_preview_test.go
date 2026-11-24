package litellm

import (
	"fmt"
	"slices"
	"strings"
	"testing"
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
