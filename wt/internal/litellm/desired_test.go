package litellm

import (
	"slices"
	"testing"
)

// TestDesiredIDsMatchWhatSyncWrites pins that DesiredIDs names exactly the
// model_names config.yaml holds after a real Sync of the same registry and
// local snapshot: a stopped local row and a stale marked row gone, a new
// local and the cloud rows added, a hand-written row kept. The Model server
// tab compares this list with the proxy's model list, so a drift from Sync
// would report a false "out of sync".
func TestDesiredIDsMatchWhatSyncWrites(t *testing.T) {
	const body = `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
  - model_name: gone/stale
    litellm_params: {model: openai/stale}
    model_info: {wt_managed: true}
  - model_name: keep/me
    litellm_params: {model: openai/gpt-4o}
`
	o, _, p := opts(t, body)
	cfg := testConfig()
	local := localFor(cfg, "mtplx/Youssofal--Q")

	before, _ := Open(p)
	beforeIDs := before.RoutedIDs()

	got, err := DesiredIDs(cfg, local, o)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := Open(p); !slices.Equal(after.RoutedIDs(), beforeIDs) {
		t.Fatal("DesiredIDs changed config.yaml")
	}
	if _, err := Sync(cfg, local, o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	want := f.RoutedIDs()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("DesiredIDs = %v, Sync wrote %v", got, want)
	}
	if slices.Contains(got, "gone/stale") || !slices.Contains(got, "keep/me") || !slices.Contains(got, "mtplx/Youssofal--Q") {
		t.Fatalf("DesiredIDs = %v: fixture no longer exercises remove/keep/add", got)
	}
}
