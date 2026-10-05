package litellm

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ollamaBaseBody is the shape that caused #202: three hand-written ollama rows
// whose api_base is missing, null or empty, beside one that names a host of
// its own, one wt row, and a row that is not ollama at all.
const ollamaBaseBody = `# hand-written header comment
model_list:
  - model_name: ollama/q8
    litellm_params:
      model: ollama_chat/ornith:8b
      api_base:
      additional_drop_params:
        - reasoning_effort
  - model_name: ollama/o35
    litellm_params:
      model: ollama_chat/ornith:35b
      additional_drop_params: [reasoning_effort]
  - model_name: ollama/blank
    litellm_params: {model: ollama/blank:1b, api_base: ""}
  - model_name: ollama/elsewhere
    litellm_params: {model: ollama_chat/far:1b, api_base: "http://gpu-box:11434", additional_drop_params: [reasoning_effort]}
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b, api_base: "http://localhost:11434", additional_drop_params: [reasoning_effort]}
    model_info: {wt_managed: true}
  - model_name: keep/me
    litellm_params:
      model: openai/gpt-4o # no api_base, and not ollama
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`

// TestApplyChangeGivesOllamaRowsAnAPIBase pins the fix for #202. LiteLLM
// starts its own `ollama serve` at proxy startup for every row whose model is
// an ollama one and whose api_base is unset. That second server binds
// 127.0.0.1:11434 beside the one already running, clients then reach different
// servers depending on how "localhost" resolves, a model is loaded in both,
// and wt stops it in one and reports success. Every write of config.yaml
// therefore gives such a row the registry's ollama address — hand-written
// rows included, in this one field and only when it is empty. A row that
// names its own address, and every row that is not ollama, is left alone.
func TestApplyChangeGivesOllamaRowsAnAPIBase(t *testing.T) {
	o, restarts, p := opts(t, ollamaBaseBody)
	res, err := ApplyChange(testConfig(), Change{}, o)
	if err != nil {
		t.Fatal(err)
	}
	var set []string
	for _, oc := range res.Outcomes {
		if oc.Action == "api_base set" {
			set = append(set, oc.ID)
		}
	}
	if want := []string{"ollama/q8", "ollama/o35", "ollama/blank"}; !slices.Equal(set, want) || len(res.Outcomes) != len(want) {
		t.Fatalf("outcomes = %+v, want exactly %v reported as \"api_base set\"", res.Outcomes, want)
	}
	if !res.Changed || *restarts != 1 {
		t.Fatalf("changed=%v restarts=%d, want the repair written and one restart", res.Changed, *restarts)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	if n := strings.Count(got, "http://localhost:11434"); n != 4 {
		t.Errorf("config names http://localhost:11434 %d times, want 4 (three repaired rows and wt's own):\n%s", n, got)
	}
	for _, keep := range []string{"# hand-written header comment", "http://gpu-box:11434", "model: openai/gpt-4o # no api_base, and not ollama", "- reasoning_effort"} {
		if !strings.Contains(got, keep) {
			t.Errorf("config lost %q:\n%s", keep, got)
		}
	}
	// The repaired rows stay the user's: filling one field must not mark them
	// as wt's, or the next sync would remove a row it did not write.
	want := []RowInfo{{ID: "ollama/q8"}, {ID: "ollama/o35"}, {ID: "ollama/blank"}, {ID: "ollama/elsewhere"}, {ID: "ollama/gemma:9b", Managed: true}, {ID: "keep/me"}}
	if rows := readRows(t, p); !slices.Equal(rows, want) {
		t.Errorf("rows = %v, want %v", rows, want)
	}

	// A second write finds nothing to repair: no outcome, no write, no restart.
	*restarts = 0
	res, err = ApplyChange(testConfig(), Change{}, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 0 || res.Changed || *restarts != 0 {
		t.Fatalf("second write: outcomes=%+v changed=%v restarts=%d, want a silent no-op", res.Outcomes, res.Changed, *restarts)
	}
}

// TestOllamaAPIBaseNeedsARegistryAddress pins the fail-safe: with no ollama
// provider in the registry, or one with no base_url, wt has no address to
// give and leaves the rows as they are rather than inventing one.
func TestOllamaAPIBaseNeedsARegistryAddress(t *testing.T) {
	for name, cfg := range map[string]*config.Config{
		"no ollama provider":                 {Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}}},
		"ollama provider without a base_url": {Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}}},
	} {
		o, restarts, p := opts(t, ollamaBaseBody)
		res, err := ApplyChange(cfg, Change{}, o)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(p)
		if res.Changed || *restarts != 0 || len(res.Outcomes) != 0 || string(b) != ollamaBaseBody {
			t.Errorf("%s: changed=%v restarts=%d outcomes=%+v, want config.yaml untouched", name, res.Changed, *restarts, res.Outcomes)
		}
	}
}

// TestPlanSyncReportsTheAPIBaseRepair pins that a dry run says which rows the
// real sync would repair: the write changes rows the user wrote by hand, so
// it must not come as a surprise.
func TestPlanSyncReportsTheAPIBaseRepair(t *testing.T) {
	o, _, p := opts(t, ollamaBaseBody)
	cfg := testConfig()
	plan, err := PlanSync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ollama/q8", "ollama/o35", "ollama/blank"}; !slices.Equal(plan.Repair, want) {
		t.Fatalf("plan.Repair = %v, want %v", plan.Repair, want)
	}
	if b, _ := os.ReadFile(p); string(b) != ollamaBaseBody {
		t.Fatal("a dry run rewrote config.yaml")
	}
}

// TestDryRunRepairMatchesTheRealSync pins that a dry run lists as "repair"
// exactly the rows the real sync reports as "api_base set". A row the sync
// adopts is rebuilt from the registry with an api_base, and a row it removes
// is gone, so neither is repaired; the dry run used to read the file as it
// was before the plan and promised a "would set api_base" line for both,
// beside their "would adopt" / "would unroute" lines, that the real sync
// never printed.
func TestDryRunRepairMatchesTheRealSync(t *testing.T) {
	// ollama/gemma:9b is a registry id with an unmarked row (adopted),
	// mtplx/Youssofal--Q an unmarked row of a registry model that is not
	// running (removed), ollama/mine a hand-written row (repaired). All three
	// lack an api_base.
	const body = `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b, additional_drop_params: [reasoning_effort]}
  - model_name: mtplx/Youssofal--Q
    litellm_params: {model: ollama_chat/legacy, additional_drop_params: [reasoning_effort]}
  - model_name: ollama/mine
    litellm_params: {model: ollama_chat/mine, additional_drop_params: [reasoning_effort]}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`
	o, _, p := opts(t, body)
	cfg := testConfig()
	local := localFor(cfg, "ollama/gemma:9b")
	plan, err := PlanSync(cfg, local, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Adopt, []string{"ollama/gemma:9b"}) || !slices.Equal(plan.Remove, []string{"mtplx/Youssofal--Q"}) {
		t.Fatalf("plan adopt %v remove %v: the fixture no longer adopts one row and removes another", plan.Adopt, plan.Remove)
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Fatal("a dry run rewrote config.yaml")
	}
	res, err := Sync(cfg, local, o)
	if err != nil {
		t.Fatal(err)
	}
	var repaired []string
	for _, oc := range res.Outcomes {
		if oc.Action == ActionAPIBaseSet {
			repaired = append(repaired, oc.ID)
		}
	}
	if want := []string{"ollama/mine"}; !slices.Equal(plan.Repair, want) || !slices.Equal(repaired, want) {
		t.Fatalf("dry run planned repair %v, real sync repaired %v; want %v for both", plan.Repair, repaired, want)
	}
}

// TestOllamaAPIBaseSeesThroughMergeKeys pins that a row taking its api_base
// from a YAML merge ("<<: *defaults") counts as naming an address. LiteLLM's
// loader resolves the merge, so such a row has an api_base and starts no
// second server; a key written beside the merge overrides it, which silently
// repointed a row at a remote ollama host to the local one. A row whose merge
// supplies only its model is still an ollama row and is still repaired.
func TestOllamaAPIBaseSeesThroughMergeKeys(t *testing.T) {
	const body = `remote: &remote
  api_base: http://gpu-box:11434
shared: &shared
  model: ollama_chat/shared:1b
  additional_drop_params: [reasoning_effort]
model_list:
  - model_name: ollama/far
    litellm_params:
      <<: *remote
      model: ollama_chat/far:1b
      additional_drop_params: [reasoning_effort]
  - model_name: ollama/far-list
    litellm_params:
      <<: [*shared, *remote]
  - model_name: ollama/near
    litellm_params:
      <<: *shared
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`
	o, _, p := opts(t, body)
	res, err := ApplyChange(testConfig(), Change{}, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].ID != "ollama/near" || res.Outcomes[0].Action != ActionAPIBaseSet {
		t.Fatalf("outcomes = %+v, want only ollama/near repaired", res.Outcomes)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"ollama/far":      "http://gpu-box:11434",
		"ollama/far-list": "http://gpu-box:11434",
		"ollama/near":     "http://localhost:11434",
	} {
		var row struct {
			Params map[string]any `yaml:"litellm_params"`
		}
		if err := f.row(id).Decode(&row); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got := row.Params["api_base"]; got != want {
			t.Errorf("%s: api_base = %v, want %s", id, got, want)
		}
	}
}

// TestOllamaAPIBaseNamesARepairedIDOnce pins that two rows sharing a
// model_name, both repaired, are reported as one id: the report names routes,
// and the same line printed twice reads as two different changes (planSync
// deduplicates a removal for the same reason).
func TestOllamaAPIBaseNamesARepairedIDOnce(t *testing.T) {
	const body = `model_list:
  - model_name: ollama/dup
    litellm_params: {model: ollama_chat/dup:1b, additional_drop_params: [reasoning_effort]}
  - model_name: ollama/dup
    litellm_params: {model: ollama_chat/dup:2b, additional_drop_params: [reasoning_effort]}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`
	o, _, p := opts(t, body)
	res, err := ApplyChange(testConfig(), Change{}, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].ID != "ollama/dup" {
		t.Fatalf("outcomes = %+v, want ollama/dup once", res.Outcomes)
	}
	b, _ := os.ReadFile(p)
	if n := strings.Count(string(b), "http://localhost:11434"); n != 2 {
		t.Errorf("config names http://localhost:11434 %d times, want both rows repaired:\n%s", n, b)
	}
}
