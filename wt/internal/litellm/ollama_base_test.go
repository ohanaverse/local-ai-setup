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

// TestOllamaAPIBaseFallsBackToTheDefaultAddress pins #206: with no ollama
// provider in the registry, or one with no base_url, the repair still runs,
// with the default address — the one wt itself dials for ollama in that case
// (localmodels.FamilyOrigin). It used to give up, which left every such row —
// and, through BuildEntry, every ollama row wt wrote — without an api_base,
// so LiteLLM started its second `ollama serve` exactly as in #202.
func TestOllamaAPIBaseFallsBackToTheDefaultAddress(t *testing.T) {
	for name, cfg := range map[string]*config.Config{
		"no ollama provider":                 {Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}}},
		"ollama provider without a base_url": {Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}}},
	} {
		if got := OllamaAPIBase(cfg); got != config.OllamaBaseURL {
			t.Errorf("%s: OllamaAPIBase = %q, want the default %q", name, got, config.OllamaBaseURL)
		}
		o, _, p := opts(t, ollamaBaseBody)
		res, err := ApplyChange(cfg, Change{}, o)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(p)
		if n := strings.Count(string(b), config.OllamaBaseURL); !res.Changed || len(res.Outcomes) != 3 || n != 4 {
			t.Errorf("%s: changed=%v outcomes=%+v, %d rows name %s; want the three empty rows repaired with it:\n%s", name, res.Changed, res.Outcomes, n, config.OllamaBaseURL, b)
		}
	}
}

// TestBuildEntryGivesAnOllamaRowTheDefaultAddress pins the other half of
// #206's fallback: the row wt builds for an ollama model whose provider names
// no base_url carries the default address, not no api_base at all. The repair
// and BuildEntry must agree, or wt would write a row and then repair it. A
// provider that is not ollama gets no invented address.
func TestBuildEntryGivesAnOllamaRowTheDefaultAddress(t *testing.T) {
	m := config.Model{ID: "ollama/gemma:9b", ProviderID: "ollama", ModelName: "gemma:9b", Location: config.LocationLocal}
	row, err := BuildEntry(m, config.Provider{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, row)["litellm_params"].(map[string]any)["api_base"]; got != config.OllamaBaseURL {
		t.Errorf("ollama row api_base = %v, want %q", got, config.OllamaBaseURL)
	}
	row, err = BuildEntry(config.Model{ID: "mtplx/Q", ProviderID: "mtplx", ModelName: "Q"}, config.Provider{ID: "mtplx", Auth: config.AuthConfig{Type: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := decode(t, row)["litellm_params"].(map[string]any)["api_base"]; ok {
		t.Errorf("mtplx row api_base = %v, want none: only ollama has a default", got)
	}
}

// ollamaServeBody holds the rows #206 is about: ones LiteLLM starts `ollama
// serve` for that wt's repair does not reach (the word ollama in a model that
// is not an ollama/ one), beside rows that look similar and are fine, and a
// row with no model_name.
const ollamaServeBody = `model_list:
  - model_name: hand/proxy
    litellm_params:
      model: openai/ollama-proxy
      api_key: none
  - model_name: hand/null
    litellm_params: {model: openrouter/ollama/x, api_base: null}
  - model_name: hand/based
    litellm_params: {model: openai/ollama-proxy, api_base: "http://box:4000/v1"}
  - model_name: hand/empty
    litellm_params: {model: openai/my-ollama, api_base: ""}
  - model_name: hand/merged
    litellm_params: &based
      model: openai/ollama-shared
      api_base: http://box:4000/v1
  - model_name: hand/inherits
    litellm_params:
      <<: *based
  - litellm_params:
      model: ollama/nameless
  - model_name: ollama/nameless
    litellm_params:
      model: ollama/nameless
  - model_name: keep/me
    litellm_params: {model: openai/gpt-4o}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`

// TestSyncWarnsAboutRowsThatStartOllamaServe pins #206 item 1. LiteLLM's test
// for starting its own `ollama serve` is the word "ollama" anywhere in the
// model with api_base unset — wider than the ollama/ and ollama_chat/ prefixes
// wt repairs. wt cannot fill such a row (openai/ollama-proxy is not an ollama
// endpoint, so the ollama address would be wrong), so sync and its dry run
// name it instead, and both name the same rows. A row with an address — its
// own, an empty string (not None to LiteLLM) or one inherited through a merge
// key — and a row the repair just fixed are not warned about, and none of
// them is changed. A targeted write (a start, stop or launch) does not warn.
func TestSyncWarnsAboutRowsThatStartOllamaServe(t *testing.T) {
	cfg := testConfig()
	want := []string{
		`row "hand/proxy" (model openai/ollama-proxy) has no api_base: LiteLLM starts its own "ollama serve" for it at proxy startup; give the row an api_base`,
		`row "hand/null" (model openrouter/ollama/x) has no api_base: LiteLLM starts its own "ollama serve" for it at proxy startup; give the row an api_base`,
	}
	o, _, p := opts(t, ollamaServeBody)
	plan, err := PlanSync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Warnings, want) {
		t.Errorf("dry run warnings = %q, want %q", plan.Warnings, want)
	}
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Warnings, want) {
		t.Errorf("sync warnings = %q, want %q", res.Warnings, want)
	}
	b, _ := os.ReadFile(p)
	for _, keep := range []string{"model: openai/ollama-proxy\n      api_key: none\n", "api_base: null", `api_base: ""`, "<<: *based"} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("sync changed a row it only warns about; lost %q:\n%s", keep, b)
		}
	}
	// Still warned about on a sync that changes nothing.
	res, err = Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || !slices.Equal(res.Warnings, want) {
		t.Errorf("second sync: changed=%v warnings=%q, want unchanged and the same warnings", res.Changed, res.Warnings)
	}
	o, _, _ = opts(t, ollamaServeBody)
	res, err = ApplyChange(cfg, Change{}, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("a targeted write warned %q, want none: only sync reports these rows", res.Warnings)
	}
}

// TestRepairLabelsARowWithNoModelName pins #206 item 3: a repaired row that
// has no model_name is reported under a label that says so and names its
// model, in the dry run and the real sync alike. It used to be reported with
// an empty id — `: api_base set` in text, {"id": ""} in JSON. The label is not
// the bare model value, which here is also another row's model_name.
func TestRepairLabelsARowWithNoModelName(t *testing.T) {
	cfg := testConfig()
	want := []string{"(no model_name: ollama/nameless)", "ollama/nameless"}
	o, _, _ := opts(t, ollamaServeBody)
	plan, err := PlanSync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Repair, want) {
		t.Errorf("plan.Repair = %q, want %q", plan.Repair, want)
	}
	res, err := ApplyChange(cfg, Change{}, o)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, oc := range res.Outcomes {
		if oc.Action == ActionAPIBaseSet {
			got = append(got, oc.ID)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("repaired = %q, want %q", got, want)
	}
}

// TestOutcomeSaysWhetherTheRowWasWritten pins what #206 item 4 rests on: a
// "routed" outcome is reported for every row a change sets, identical or not,
// and Result.Changed is about the whole file, so neither says whether the
// model's own route changed. Outcome.Written does: true for a row that was
// added or differs from the one on disk, false when the file changed only
// because another row was repaired or a setting was enforced.
func TestOutcomeSaysWhetherTheRowWasWritten(t *testing.T) {
	cfg := testConfig()
	seed := "model_list:\n  - model_name: hand/other\n    litellm_params:\n      model: ollama/other\n      api_base: http://h:1\n"
	o, _, p := opts(t, seed)
	apply := func(label string, edit func(string) string, wantChanged, wantWritten bool) {
		t.Helper()
		if edit != nil {
			b, _ := os.ReadFile(p)
			after := edit(string(b))
			if after == string(b) {
				t.Fatalf("%s: the fixture edit matched nothing", label)
			}
			if err := os.WriteFile(p, []byte(after), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		res, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		var routed *Outcome
		for i := range res.Outcomes {
			if res.Outcomes[i].ID == "ollama/gemma:9b" && res.Outcomes[i].Action == "routed" {
				routed = &res.Outcomes[i]
			}
		}
		if routed == nil {
			t.Fatalf("%s: no routed outcome in %+v", label, res.Outcomes)
		}
		if res.Changed != wantChanged || routed.Written != wantWritten {
			t.Errorf("%s: changed=%v written=%v, want %v/%v", label, res.Changed, routed.Written, wantChanged, wantWritten)
		}
	}
	apply("a new route", nil, true, true)
	apply("nothing to do", nil, false, false)
	apply("another row repaired", func(s string) string { return strings.Replace(s, "      api_base: http://h:1\n", "", 1) }, true, false)
	apply("a setting enforced", func(s string) string {
		return strings.Replace(s, "  use_chat_completions_url_for_anthropic_messages: true\n", "", 1)
	}, true, false)
	apply("the route itself differs", func(s string) string {
		return strings.Replace(s, "api_base: http://localhost:11434\n      additional", "api_base: http://stale:1\n      additional", 1)
	}, true, true)
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
