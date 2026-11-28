package litellm

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestSyncAndWarnPrintsTheSyncLinesThenEveryWarning checks that SyncAndWarn
// prints the sync's stdout lines first, then every warning on stderr (the
// caller's warnings first, the sync's own last), because a registry write
// must never hide that its route sync failed.
func TestSyncAndWarnPrintsTheSyncLinesThenEveryWarning(t *testing.T) {
	var out, errOut bytes.Buffer
	sync := func(o, _ io.Writer) string { fmt.Fprintln(o, "synced"); return "LiteLLM routes not synced: x" }
	if err := SyncAndWarn(&out, &errOut, []string{"first"}, sync); err != nil {
		t.Fatal(err)
	}
	if out.String() != "synced\n" {
		t.Errorf("out = %q", out.String())
	}
	if errOut.String() != "warning: first\nwarning: LiteLLM routes not synced: x\n" {
		t.Errorf("errOut = %q", errOut.String())
	}
}

// TestSyncAfterWriteSkipsAMachineWithoutLiteLLM checks that SyncAfterWrite
// returns "" and never probes providers when config.yaml is absent, so
// `wt-model model` writes on a machine without LiteLLM neither warn nor probe.
func TestSyncAfterWriteSkipsAMachineWithoutLiteLLM(t *testing.T) {
	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	probed := false
	got := SyncAfterWrite(io.Discard, io.Discard, func(*config.Config) localmodels.Snapshot { probed = true; return localmodels.Snapshot{} }, nil)
	if got != "" || probed {
		t.Fatalf("got %q, probed %v; want \"\" and no probe", got, probed)
	}
}

// TestPlanSyncSaysWhyItRestartsForAffinity pins the dry run's explanation of
// the restart the affinity keys cost: a config.yaml lacking them plans a
// restart and the text names each key it would add in one line, while the JSON
// rendering stays exactly what it was (no new field, so the litellm-cli
// contract does not move), and a config that already has them says nothing.
func TestPlanSyncSaysWhyItRestartsForAffinity(t *testing.T) {
	o, restarts, p := opts(t, "model_list: []\n"+settledWithoutAffinity)
	plan, err := PlanSync(&config.Config{}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Restart || *restarts != 0 || readFile(t, p) != "model_list: []\n"+settledWithoutAffinity {
		t.Fatalf("plan restart=%v restarts=%d, want a planned restart and nothing written", plan.Restart, *restarts)
	}
	var out, errOut bytes.Buffer
	if err := ReportSyncPlan(&out, &errOut, plan, nil, false); err != nil {
		t.Fatal(err)
	}
	const line = "config.yaml: would add router_settings.optional_pre_call_checks, router_settings.deployment_affinity_ttl_seconds, router_settings.session_affinity_repin_on_failover (session affinity); the proxy would restart once\n"
	if out.String() != line || errOut.Len() != 0 {
		t.Fatalf("text out = %q err = %q, want exactly %q", out.String(), errOut.String(), line)
	}

	var withKeys, without bytes.Buffer
	if err := ReportSyncPlan(&withKeys, io.Discard, plan, nil, true); err != nil {
		t.Fatal(err)
	}
	bare := plan
	bare.affinityKeys = nil
	if err := ReportSyncPlan(&without, io.Discard, bare, nil, true); err != nil {
		t.Fatal(err)
	}
	if withKeys.String() != without.String() {
		t.Fatalf("JSON changed with affinityKeys:\n%s\nvs\n%s", withKeys.String(), without.String())
	}

	o2, _, _ := opts(t, "model_list: []\n"+settledSettings)
	settled, err := PlanSync(&config.Config{}, nil, o2)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ReportSyncPlan(&out, io.Discard, settled, nil, false); err != nil {
		t.Fatal(err)
	}
	if settled.Restart || out.Len() != 0 {
		t.Fatalf("settled plan restart=%v text %q, want no restart and no line", settled.Restart, out.String())
	}
}
