package litellm

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDBModelFromRowOllama pins that a built ollama row becomes a database
// payload with its params intact, wt_managed forced true, and the stable
// "wt:" id carried in DBModel.ID (Add writes it into model_info.id), not
// duplicated inside Info.
func TestDBModelFromRowOllama(t *testing.T) {
	cfg := testConfig()
	row, err := BuildEntry(cfg.Models[0], cfg.Providers[0])
	if err != nil {
		t.Fatal(err)
	}
	db, err := DBModelFromRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if db.ModelName != "ollama/gemma:9b" || db.ID != "wt:ollama/gemma:9b" {
		t.Fatalf("name/id = %q/%q", db.ModelName, db.ID)
	}
	if db.Params["model"] != "ollama_chat/gemma:9b" || db.Params["api_base"] != "http://localhost:11434" {
		t.Fatalf("params = %v", db.Params)
	}
	if db.Info["wt_managed"] != true {
		t.Fatalf("info = %v, want wt_managed true", db.Info)
	}
	if _, has := db.Info["id"]; has {
		t.Fatalf("info = %v, must not carry id (DBModel.ID does)", db.Info)
	}
}

// TestDBModelFromRowKeepsOsEnvReferences pins that an os.environ/X api_key
// stays the literal reference and that a row's own model_info is merged
// while wt_managed is forced true even over a false in the row.
func TestDBModelFromRowKeepsOsEnvReferences(t *testing.T) {
	var row yaml.Node
	src := `
model_name: openrouter/x/y
litellm_params:
  model: openrouter/x/y
  api_key: os.environ/OPENROUTER_API_KEY
  extra_field: 7
model_info:
  supports_vision: true
  wt_managed: false
  id: other
`
	if err := yaml.Unmarshal([]byte(src), &row); err != nil {
		t.Fatal(err)
	}
	db, err := DBModelFromRow(row.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	if db.Params["api_key"] != "os.environ/OPENROUTER_API_KEY" || db.Params["extra_field"] != 7 {
		t.Fatalf("params = %v", db.Params)
	}
	if db.Info["supports_vision"] != true || db.Info["wt_managed"] != true {
		t.Fatalf("info = %v", db.Info)
	}
	if _, has := db.Info["id"]; has || db.ID != "wt:openrouter/x/y" {
		t.Fatalf("id = %q info = %v", db.ID, db.Info)
	}
}

// TestDBModelFromRowRejectsMissingName pins that a row without a model_name
// is an error rather than a payload with an id of just "wt:".
func TestDBModelFromRowRejectsMissingName(t *testing.T) {
	var row yaml.Node
	if err := yaml.Unmarshal([]byte("litellm_params: {model: x}\n"), &row); err != nil {
		t.Fatal(err)
	}
	if _, err := DBModelFromRow(row.Content[0]); err == nil {
		t.Fatal("want an error for a row with no model_name")
	}
}

// TestDBIDIsStable pins the id format, which the database rows are matched
// by on every later sync.
func TestDBIDIsStable(t *testing.T) {
	if got := DBID("ollama/gemma:9b"); got != "wt:ollama/gemma:9b" {
		t.Fatalf("DBID = %q", got)
	}
}
