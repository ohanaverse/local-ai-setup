package config

import "testing"

// TestModelDecodesFetchAndDraft verifies wt reads a model's fetch and draft
// tables: the repo of each side of the shared fixture's mlx_lm_server pairing,
// and a local_path. `wt model list` shows a local_path as the model's path and
// the pairing hint names the target and the draft; decoded as empty, the hint
// would tell the user to start a pairing with no target.
func TestModelDecodesFetchAndDraft(t *testing.T) {
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")
	_, models, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	i := IndexModelByID(models, "mlx_lm_server/contract-fixture:pair")
	if i < 0 {
		t.Fatal("the fixture has no mlx_lm_server pairing")
	}
	if got := models[i].Fetch.Target(); got != "org/contract-fixture-target" {
		t.Errorf("Fetch.Target() = %q, want the fixture's target repo", got)
	}
	if got := models[i].Draft.Target(); got != "org/contract-fixture-draft" {
		t.Errorf("Draft.Target() = %q, want the fixture's draft repo", got)
	}

	writeRegistry(t, t.TempDir(), `
[[providers]]
id = "omlx"
location = "local"
[providers.auth]
type = "none"

[[models]]
id = "omlx/mine"
family = "qwen"
provider_id = "omlx"
model_name = "mine-4bit"
[models.fetch]
repo = "org/base"
local_path = "~/models/mine-4bit"
files = ["a", "b"]
`)
	t.Setenv("MODELMAN_REGISTRY", "")
	_, models, err = loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := models[0].Fetch; got.LocalPath != "~/models/mine-4bit" || got.Target() != "~/models/mine-4bit" || got.Repo != "org/base" {
		t.Errorf("Fetch = %+v, want the local path to win over the repo", got)
	}
	if got := models[0].Draft.Target(); got != "" {
		t.Errorf("Draft.Target() = %q on a model with no draft, want \"\"", got)
	}
}
