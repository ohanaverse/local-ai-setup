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
