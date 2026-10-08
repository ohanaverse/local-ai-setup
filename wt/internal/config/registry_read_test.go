package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestReadRegistryDocIsAReadOnlyView pins the document a planner reads
// before anything is decided: it holds the rows as written (a key wt does
// not model included), and getting it leaves no trace — no lock file, no
// write — so a `--dry-run` really changes nothing. A change made to it goes
// nowhere.
func TestReadRegistryDocIsAReadOnlyView(t *testing.T) {
	const content = `# a comment a write would drop
[[models]]
id = "ollama/x:cloud"
family = "x"
provider_id = "ollama"
model_name = "x:cloud"
catalog_name = "x"
`
	path := scratchRegistry(t, content)
	doc, err := ReadRegistryDoc()
	if err != nil {
		t.Fatalf("ReadRegistryDoc: %v", err)
	}
	rows := doc.Models()
	if len(rows) != 1 {
		t.Fatalf("models = %d, want 1", len(rows))
	}
	if v, _ := rows[0].Get("catalog_name"); v != "x" {
		t.Errorf("catalog_name = %v, want the key wt's typed reader does not model", v)
	}
	if err := doc.PatchModel("ollama/x:cloud", map[string]any{"family": "changed"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != content {
		t.Errorf("reading the registry changed it:\n%s", got)
	}
	if names := dirNames(t, filepath.Dir(path)); !reflect.DeepEqual(names, []string{"registry.toml"}) {
		t.Errorf("reading the registry left files behind: %v", names)
	}
}

// TestReadRegistryDocRefusesWhatAWriteWouldRefuse pins that a dry run meets
// the same refusals the apply would: a registry that is missing (with the
// command that creates one), one that is not TOML, and one with a top-level
// key a write will not accept. Without this a dry run would print a plan the
// real run can never apply.
func TestReadRegistryDocRefusesWhatAWriteWouldRefuse(t *testing.T) {
	cases := []struct {
		name, content string
		want          error
		says          string
	}{
		{"missing", "", ErrRegistryMissing, "wt model init"},
		{"not TOML", "not [ valid toml", ErrRegistryFile, "parse"},
		{"an unknown top-level key", "[[model]]\nid = \"x\"\n", ErrRegistryTopLevel, "`model`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scratchRegistry(t, tc.content)
			_, err := ReadRegistryDoc()
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("err = %v, want %v naming %q", err, tc.want, tc.says)
			}
		})
	}
}

// TestModelPricingUpdatedReadsEverySpelling pins that pricing_updated_at
// never fails the registry load, whatever a hand edit left there. wt stamps
// a string; a TOML date-time or a typo in that key must not turn every
// launch into a config error over a value only the stale-price notice reads.
func TestModelPricingUpdatedReadsEverySpelling(t *testing.T) {
	scratchRegistry(t, `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"

[[models]]
id = "openrouter/stamped"
family = "x"
provider_id = "openrouter"
model_name = "vendor/stamped"
pricing_updated_at = "2026-10-04T02:31:25+00:00"

[[models]]
id = "openrouter/datetime"
family = "x"
provider_id = "openrouter"
model_name = "vendor/datetime"
pricing_updated_at = 2026-10-04T02:31:25Z

[[models]]
id = "openrouter/no-offset"
family = "x"
provider_id = "openrouter"
model_name = "vendor/no-offset"
pricing_updated_at = "2026-10-04T02:31:25"

[[models]]
id = "openrouter/date"
family = "x"
provider_id = "openrouter"
model_name = "vendor/date"
pricing_updated_at = "2026-10-04"

[[models]]
id = "openrouter/typo"
family = "x"
provider_id = "openrouter"
model_name = "vendor/typo"
pricing_updated_at = "yesterday"

[[models]]
id = "openrouter/number"
family = "x"
provider_id = "openrouter"
model_name = "vendor/number"
pricing_updated_at = 20261004

[[models]]
id = "openrouter/none"
family = "x"
provider_id = "openrouter"
model_name = "vendor/none"
`)
	_, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	at := time.Date(2026, 10, 4, 2, 31, 25, 0, time.UTC)
	want := map[string]*time.Time{
		"openrouter/stamped": &at, "openrouter/datetime": &at, "openrouter/no-offset": &at,
		"openrouter/date": ptrTime(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)),
		"openrouter/typo": nil, "openrouter/number": nil, "openrouter/none": nil,
	}
	if len(models) != len(want) {
		t.Fatalf("models = %d, want %d", len(models), len(want))
	}
	for _, m := range models {
		got, ok := m.PricingUpdated()
		switch w := want[m.ID]; {
		case w == nil && ok:
			t.Errorf("%s: PricingUpdated = %v, want not said", m.ID, got)
		case w != nil && (!ok || !got.Equal(*w)):
			t.Errorf("%s: PricingUpdated = (%v, %v), want %v", m.ID, got, ok, *w)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
