package config

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestModelDecodesFetchAndDraft verifies wt reads a model's fetch and draft
// tables: the repo of each side of the shared fixture's mlx_lm_server pairing,
// and a local_path. `wt model list` shows a local_path as the model's path and
// the pairing hint names the target and the draft; decoded as empty, the hint
// would tell the user to start a pairing with no target.
func TestModelDecodesFetchAndDraft(t *testing.T) {
	t.Setenv("WT_REGISTRY", "../../../docs/contracts/registry.sample.toml")
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
	t.Setenv("WT_REGISTRY", "")
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

// TestLoadToleratesAMalformedFetchOrDraft verifies a fetch or draft that is
// not a table, or whose repo or local_path is not a string, reads as absent
// and the registry still loads — and that an empty table and keys wt does not
// model load too. Models are edited by hand, and wt reads these two tables
// only to show a path and name a pairing; a slip such as `fetch = "org/S"`
// that failed the load would stop every wt command, launches included, over a
// key no launch reads.
func TestLoadToleratesAMalformedFetchOrDraft(t *testing.T) {
	const head = `
[[providers]]
id = "omlx"
location = "local"
[providers.auth]
type = "none"

[[models]]
id = "omlx/first"
family = "qwen"
provider_id = "omlx"
model_name = "first-4bit"
tags = ["code"]
`
	// A second, well-formed row after the malformed one: tolerance must not
	// cost the rows that follow.
	const tail = `
[[models]]
id = "omlx/second"
family = "qwen"
provider_id = "omlx"
model_name = "second-4bit"
[models.fetch]
repo = "org/second"
`
	cases := []struct {
		name         string
		body         string
		fetch, draft ModelArtifact
	}{
		{"a string", `fetch = "org/S"` + "\n" + `draft = "org/D"`, ModelArtifact{}, ModelArtifact{}},
		{"an integer", "fetch = 7\ndraft = 8", ModelArtifact{}, ModelArtifact{}},
		{"a boolean", "fetch = true", ModelArtifact{}, ModelArtifact{}},
		{"an array", `fetch = ["org/S"]` + "\n" + `draft = []`, ModelArtifact{}, ModelArtifact{}},
		{"an array of tables", "[[models.fetch]]\nrepo = \"org/S\"", ModelArtifact{}, ModelArtifact{}},
		{"a repo that is not a string", "[models.fetch]\nrepo = 7\nlocal_path = \"~/m/first\"\n[models.draft]\nrepo = [\"org/D\"]",
			ModelArtifact{LocalPath: "~/m/first"}, ModelArtifact{}},
		{"a local_path that is not a string", "[models.fetch]\nrepo = \"org/S\"\nlocal_path = false\n[models.draft]\nlocal_path = { a = 1 }",
			ModelArtifact{Repo: "org/S"}, ModelArtifact{}},
		{"an empty table", "[models.fetch]\n[models.draft]", ModelArtifact{}, ModelArtifact{}},
		{"an inline empty table", "fetch = {}", ModelArtifact{}, ModelArtifact{}},
		{"keys wt does not model", "[models.fetch]\nrepo = \"org/S\"\nfiles = [\"a\"]\nx_note = 3\n[models.fetch.quantizations]\nq4 = \"a\"\n[models.draft]\nlocal_path = \"/d\"\nx_note = \"kept\"",
			ModelArtifact{Repo: "org/S"}, ModelArtifact{LocalPath: "/d"}},
		{"well formed", "[models.fetch]\nrepo = \"org/S\"\nlocal_path = \"/s\"\n[models.draft]\nrepo = \"org/D\"",
			ModelArtifact{Repo: "org/S", LocalPath: "/s"}, ModelArtifact{Repo: "org/D"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WT_REGISTRY", "")
			writeRegistry(t, t.TempDir(), head+c.body+"\n"+tail)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() = %v, want the registry to load", err)
			}
			if len(cfg.Models) != 2 {
				t.Fatalf("got %d models, want 2", len(cfg.Models))
			}
			first := cfg.Models[0]
			if first.ID != "omlx/first" || first.ModelName != "first-4bit" || len(first.Tags) != 1 {
				t.Errorf("the rest of the row changed: %+v", first)
			}
			// What the two hold, apart from the record of what was malformed
			// (TestModelNamesItsMalformedFetchAndDraft).
			first.Fetch.Malformed, first.Draft.Malformed = ArtifactFaults{}, ArtifactFaults{}
			if first.Fetch != c.fetch {
				t.Errorf("Fetch = %+v, want %+v", first.Fetch, c.fetch)
			}
			if first.Draft != c.draft {
				t.Errorf("Draft = %+v, want %+v", first.Draft, c.draft)
			}
			if got := cfg.Models[1].Fetch; got != (ModelArtifact{Repo: "org/second"}) {
				t.Errorf("the next row's Fetch = %+v, want its repo", got)
			}
		})
	}
}

// TestModelNamesItsMalformedFetchAndDraft verifies the loader records what it
// tolerated: Model.Malformed names each fetch or draft value that is not a
// table and each repo or local_path that is not a string, fetch before draft
// and repo before local_path, and names nothing for a well-formed, empty or
// absent table. Tolerating a slip such as `fetch = "~/models/x"` in silence
// leaves a row that reads "missing" with no hint why; `wt model list` prints
// these phrases, so their wording and order are what the user sees. The
// record must not change what Fetch and Draft hold.
func TestModelNamesItsMalformedFetchAndDraft(t *testing.T) {
	const head = `
[[providers]]
id = "omlx"
location = "local"
[providers.auth]
type = "none"

[[models]]
id = "omlx/first"
family = "qwen"
provider_id = "omlx"
model_name = "first-4bit"
`
	// A well-formed row after the malformed one must report nothing: the
	// record belongs to its own row.
	const tail = `
[[models]]
id = "omlx/second"
family = "qwen"
provider_id = "omlx"
model_name = "second-4bit"
[models.fetch]
repo = "org/second"
`
	cases := []struct {
		name string
		body string
		want []string
		// fetch and draft are what the two still hold.
		fetch, draft string
	}{
		{"a string", `fetch = "~/models/x"`, []string{"fetch is not a table"}, "", ""},
		{"an integer", "fetch = 7", []string{"fetch is not a table"}, "", ""},
		{"an array", `fetch = ["org/S"]`, []string{"fetch is not a table"}, "", ""},
		{"an array of tables", "[[models.fetch]]\nrepo = \"org/S\"", []string{"fetch is not a table"}, "", ""},
		{"a repo that is not a string", "[models.fetch]\nrepo = 7\nlocal_path = \"/m/first\"",
			[]string{"fetch.repo is not a string"}, "/m/first", ""},
		{"a local_path that is not a string", "[models.fetch]\nrepo = \"org/S\"\nlocal_path = false",
			[]string{"fetch.local_path is not a string"}, "org/S", ""},
		{"both keys bad", "[models.fetch]\nlocal_path = 1\nrepo = [\"org/S\"]",
			[]string{"fetch.repo is not a string", "fetch.local_path is not a string"}, "", ""},
		{"fetch bad and draft fine", "fetch = \"org/S\"\n[models.draft]\nrepo = \"org/D\"",
			[]string{"fetch is not a table"}, "", "org/D"},
		{"draft bad and fetch fine", "draft = 7\n[models.fetch]\nrepo = \"org/S\"",
			[]string{"draft is not a table"}, "org/S", ""},
		{"a draft key that is not a string", "[models.fetch]\nrepo = \"org/S\"\n[models.draft]\nlocal_path = { a = 1 }",
			[]string{"draft.local_path is not a string"}, "org/S", ""},
		{"both malformed", "draft = true\n[models.fetch]\nlocal_path = 3\nrepo = 2",
			[]string{"fetch.repo is not a string", "fetch.local_path is not a string", "draft is not a table"}, "", ""},
		{"well formed", "[models.fetch]\nrepo = \"org/S\"\nlocal_path = \"/s\"\nfiles = [\"a\"]\n[models.draft]\nrepo = \"org/D\"", nil, "/s", "org/D"},
		{"empty tables", "[models.fetch]\n[models.draft]", nil, "", ""},
		{"absent", "", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WT_REGISTRY", "")
			writeRegistry(t, t.TempDir(), head+c.body+"\n"+tail)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() = %v, want the registry to load", err)
			}
			if len(cfg.Models) != 2 {
				t.Fatalf("got %d models, want 2", len(cfg.Models))
			}
			first := cfg.Models[0]
			if got := first.Malformed(); !slices.Equal(got, c.want) {
				t.Errorf("Malformed() = %q, want %q", got, c.want)
			}
			if got := first.Fetch.Target(); got != c.fetch {
				t.Errorf("Fetch.Target() = %q, want %q", got, c.fetch)
			}
			if got := first.Draft.Target(); got != c.draft {
				t.Errorf("Draft.Target() = %q, want %q", got, c.draft)
			}
			if got := cfg.Models[1].Malformed(); got != nil {
				t.Errorf("the next, well-formed row reports %q, want nothing", got)
			}
		})
	}
}

// TestMalformedRecordIsNeverSerialised verifies the record of a malformed
// fetch or draft stays in memory: neither the TOML encoder (wt's config.toml
// writers encode a Config) nor encoding/json prints it. It describes how a
// value was written, not a value, and a key for it in a file would be read
// back later as data: a setting nobody set, in a file a person edits.
func TestMalformedRecordIsNeverSerialised(t *testing.T) {
	m := Model{
		ID: "omlx/x", Family: "q", ProviderID: "omlx", ModelName: "x",
		Fetch: ModelArtifact{LocalPath: "/m/x", Malformed: ArtifactFaults{Repo: true}},
		Draft: ModelArtifact{Malformed: ArtifactFaults{NotTable: true}},
	}
	if got := m.Malformed(); !slices.Equal(got, []string{"fetch.repo is not a string", "draft is not a table"}) {
		t.Fatalf("Malformed() = %q: the model under test must carry a record", got)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(struct {
		Models []Model `toml:"models"`
	}{[]Model{m}}); err != nil {
		t.Fatal(err)
	}
	js, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"TOML": buf.String(), "JSON": string(js)} {
		if !strings.Contains(text, "/m/x") {
			t.Errorf("%s output lost the model's own fields:\n%s", name, text)
		}
		for _, leak := range []string{"alformed", "NotTable", "not_table", "true"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s output carries %q:\n%s", name, leak, text)
			}
		}
	}
}
