package cloudsync

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// TestEntriesReadsSource verifies Entries carries a row's source key and
// Manual reports only the hand-added value. The sync flows protect a manual
// entry from removal, so a misread source would delete a model the user
// added by hand (or protect one the sync owns).
func TestEntriesReadsSource(t *testing.T) {
	root, err := tomlw.Decode([]byte(`[[models]]
id = "ollama/beam:cloud"
source = "manual"

[[models]]
id = "ollama/x:cloud"
source = "curated"

[[models]]
id = "ollama/y:cloud"
`))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := root.Get("models")
	var rows []*tomlw.Table
	for _, item := range v.([]any) {
		rows = append(rows, item.(*tomlw.Table))
	}
	got := Entries(rows)
	want := []struct {
		source string
		manual bool
	}{{"manual", true}, {"curated", false}, {"", false}}
	for i, w := range want {
		if got[i].Source != w.source || got[i].Manual() != w.manual {
			t.Errorf("entry %d: Source=%q Manual=%v, want %q %v", i, got[i].Source, got[i].Manual(), w.source, w.manual)
		}
	}
}

// TestCoveredBySync pins which models a sync flow can address: every
// openrouter model, and ollama models that are cloud. `wt model add` marks
// only these manual and `wt model edit --source` accepts only these, so a
// wrong answer would mark a local model that no sync would ever touch.
func TestCoveredBySync(t *testing.T) {
	cases := []struct {
		provider, location, name string
		want                     bool
	}{
		{"openrouter", "", "a/b", true},
		{"ollama", "cloud", "beam", true},
		{"ollama", "", "beam:cloud", true},
		{"ollama", "", "beam:120b-cloud", true},
		{"ollama", "local", "qwen3:8b", false},
		{"omlx", "", "x", false},
	}
	for _, c := range cases {
		if got := CoveredBySync(c.provider, c.location, c.name); got != c.want {
			t.Errorf("CoveredBySync(%q, %q, %q) = %v, want %v", c.provider, c.location, c.name, got, c.want)
		}
	}
}
