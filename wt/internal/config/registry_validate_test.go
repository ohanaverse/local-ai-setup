package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestUpdateRegistryValidatesOnlyTheRowsItTouched pins both halves of the
// validation rule. A row this write did not touch may be as broken as it
// likes — the write may be the edit or removal that repairs the file, and a
// hand-edited bad row must not block every price refresh. A row this write
// did touch must pass wt's typed decode and modelman's row rules, or nothing
// is written. (The rules that need other rows — a location that resolves, a
// provider_id that names a provider — are not checked here yet.)
func TestUpdateRegistryValidatesOnlyTheRowsItTouched(t *testing.T) {
	// ollama/broken has no family and a negative price: modelman refuses the
	// first, both tools the second.
	const withBadRow = docRegistry + `
[[models]]
id = "ollama/broken"
provider_id = "ollama"
model_name = "broken"

[models.cost]
input_price_per_million = -1
`
	t.Run("a bad row elsewhere does not block the write", func(t *testing.T) {
		path := scratchRegistry(t, withBadRow)
		if changed, err := UpdateRegistry(setFamily("ollama/beta", "fine")); err != nil || !changed {
			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
		}
		if got := readFile(t, path); !strings.Contains(got, `family = "fine"`) || !strings.Contains(got, `id = "ollama/broken"`) {
			t.Errorf("the good row should be written and the bad row kept:\n%s", got)
		}
	})
	t.Run("and the bad row can be removed", func(t *testing.T) {
		path := scratchRegistry(t, withBadRow)
		_, err := UpdateRegistry(func(d *RegistryDoc) error {
			_, err := d.RemoveModel("ollama/broken")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, path); got != docRegistry {
			t.Errorf("removing the bad row should leave the original registry:\n%s", got)
		}
	})
	t.Run("touching the bad row without fixing it is refused", func(t *testing.T) {
		path := scratchRegistry(t, withBadRow)
		_, err := UpdateRegistry(func(d *RegistryDoc) error {
			return d.PatchModel("ollama/broken", map[string]any{"tags": []string{"code"}}, nil)
		})
		if !errors.Is(err, ErrRegistryInvalid) || !strings.Contains(err.Error(), `model "ollama/broken": family is required`) {
			t.Fatalf("err = %v, want ErrRegistryInvalid naming the row and the missing key", err)
		}
		if got := readFile(t, path); got != withBadRow {
			t.Error("a refused write changed the file")
		}
	})

	// A hand-edited fetch or draft the reader tolerates (it reads as absent):
	// the writer is where the slip is named, and only for the row it sits in.
	const withBadFetch = docRegistry + `
[[models]]
id = "ollama/slip"
family = "fam"
provider_id = "ollama"
model_name = "slip"
fetch = "org/slip"

[models.draft]
repo = 7
`
	t.Run("a malformed fetch elsewhere does not block the write", func(t *testing.T) {
		path := scratchRegistry(t, withBadFetch)
		if changed, err := UpdateRegistry(setFamily("ollama/beta", "fine")); err != nil || !changed {
			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
		}
		if got := readFile(t, path); !strings.Contains(got, `fetch = "org/slip"`) || !strings.Contains(got, "repo = 7") {
			t.Errorf("the untouched row's fetch and draft should be kept as written:\n%s", got)
		}
	})
	t.Run("touching the row with the malformed fetch names the key", func(t *testing.T) {
		path := scratchRegistry(t, withBadFetch)
		_, err := UpdateRegistry(setFamily("ollama/slip", "other"))
		if !errors.Is(err, ErrRegistryInvalid) || !strings.Contains(err.Error(), `model "ollama/slip": fetch must be a table`) {
			t.Fatalf("err = %v, want ErrRegistryInvalid naming the row and fetch", err)
		}
		_, err = UpdateRegistry(func(d *RegistryDoc) error {
			return d.PatchModel("ollama/slip", nil, []string{"fetch"})
		})
		if !errors.Is(err, ErrRegistryInvalid) || !strings.Contains(err.Error(), `model "ollama/slip": draft.repo must be a string`) {
			t.Fatalf("err = %v, want ErrRegistryInvalid naming the row and draft.repo", err)
		}
		if got := readFile(t, path); got != withBadFetch {
			t.Error("a refused write changed the file")
		}
	})
	t.Run("and the edit that repairs it is written", func(t *testing.T) {
		path := scratchRegistry(t, withBadFetch)
		changed, err := UpdateRegistry(func(d *RegistryDoc) error {
			return d.PatchModel("ollama/slip", map[string]any{"draft.repo": "org/draft"}, []string{"fetch"})
		})
		if err != nil || !changed {
			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
		}
		if got := readFile(t, path); strings.Contains(got, "org/slip") || !strings.Contains(got, `repo = "org/draft"`) {
			t.Errorf("the repaired row should be written:\n%s", got)
		}
	})

	invalid := []struct {
		name  string
		set   map[string]any
		unset []string
		want  string
	}{
		{"a required key deleted", nil, []string{"model_name"}, "model_name is required"},
		{"an empty model name", map[string]any{"model_name": ""}, nil, "model_name must be a non-empty string"},
		{"tags that are not a list", map[string]any{"tags": "code"}, nil, "tags"},
		{"cost that is not a table", map[string]any{"cost": "free"}, nil, "cost must be a table"},
		{"a negative price", map[string]any{"cost.input_price_per_million": -0.5}, nil, "input_price_per_million must be non-negative"},
		{"a price that is not a number", map[string]any{"cost.output_price_per_million": "3"}, nil, "output_price_per_million must be a number"},
		{"a boolean price", map[string]any{"cost.cache_price_per_million": true}, nil, "cache_price_per_million must be a number"},
		{"a subscription with no period", map[string]any{"cost.subscription_price": 20}, nil, "subscription_period must be month or year"},
		{"a subscription period that is not one", map[string]any{"cost.subscription_price": 20, "cost.subscription_period": "week"}, nil, "subscription_period must be month or year"},
		{"an unknown legacy cost kind", map[string]any{"cost.kind": "metered"}, nil, "kind must be free/per_token/subscription"},
		{"a negative legacy per-token price", map[string]any{"cost.kind": "per_token", "cost.price_per_million_tokens": -1}, nil, "price_per_million_tokens must be non-negative"},
		{"a legacy subscription price that is not a number", map[string]any{"cost.kind": "subscription", "cost.price_per_period": "20", "cost.period": "month"}, nil, "price_per_period must be a number"},
		{"a legacy subscription with no period", map[string]any{"cost.kind": "subscription", "cost.price_per_period": 20}, nil, "period must be month or year"},
		{"a legacy period that is not a string", map[string]any{"cost.kind": "subscription", "cost.period": 12}, nil, "period must be a string"},
		{"fetch that is not a table", map[string]any{"fetch": "org/beta"}, nil, "fetch must be a table"},
		{"draft that is not a table", map[string]any{"draft": []string{"org/draft"}}, nil, "draft must be a table"},
		{"a fetch repo that is not a string", map[string]any{"fetch.repo": 7}, nil, "fetch.repo must be a string"},
		{"a fetch local_path that is not a string", map[string]any{"fetch": map[string]any{"repo": "org/beta", "local_path": true}}, nil, "fetch.local_path must be a string"},
		{"a draft repo that is not a string", map[string]any{"draft.repo": []string{"org/draft"}}, nil, "draft.repo must be a string"},
		{"a draft local_path that is not a string", map[string]any{"draft.local_path": 1.5}, nil, "draft.local_path must be a string"},
		{"time_prices that are not rows", map[string]any{"cost.time_prices": "off-peak"}, nil, "time_prices must be an array of tables"},
		{"a time price with an unknown zone", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "Mars/Olympus", "windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "01:00"}}}}}, nil, `timezone "Mars/Olympus" is not a known IANA timezone`},
		{"a time price with no zone", map[string]any{"cost.time_prices": []map[string]any{{"windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "01:00"}}}}}, nil, `timezone "" is not a known IANA timezone`},
		{"a time price with no windows", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC"}}}, nil, "windows must be a non-empty array of tables"},
		{"a window on an unknown day", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"funday"}, "start": "00:00", "end": "01:00"}}}}}, nil, "days must be drawn from"},
		{"a window that ends before it starts", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "12:00", "end": "09:00"}}}}}, nil, "start must be before end"},
		{"a window time that is not HH:MM", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "9am", "end": "12:00"}}}}}, nil, "start must be HH:MM"},
		{"a location that is neither local nor cloud", map[string]any{"location": "mars"}, nil, `has location "mars"; expected "local" or "cloud"`},
		{"a provider with no row", map[string]any{"provider_id": "nope"}, nil, `provider_id "nope" names no provider row`},
		{"a window past midnight", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "24:30"}}}}}, nil, "end must be HH:MM between 00:00 and 24:00"},
	}
	for _, c := range invalid {
		t.Run(c.name, func(t *testing.T) {
			path := scratchRegistry(t, docRegistry)
			changed, err := UpdateRegistry(func(d *RegistryDoc) error {
				return d.PatchModel("ollama/beta", c.set, c.unset)
			})
			if !errors.Is(err, ErrRegistryInvalid) || changed || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryInvalid mentioning %q", changed, err, c.want)
			}
			if !strings.Contains(err.Error(), `model "ollama/beta"`) {
				t.Errorf("error %q should name the row", err)
			}
			if got := readFile(t, path); got != docRegistry {
				t.Error("a refused write changed the file")
			}
		})
	}
	// The accepting side: every shape of cost row modelman loads must get
	// through, or a price refresh or an edit of a good row is refused.
	offPeak := []map[string]any{{
		"label": "off-peak", "timezone": "UTC", "input_price_per_million": 1.5,
		"windows": []map[string]any{{"days": []string{"sat", "sun"}, "start": "00:00", "end": "24:00"}},
	}}
	valid := []struct {
		name string
		set  map[string]any
		want string
	}{
		{"integer and float prices", map[string]any{"cost.input_price_per_million": 3, "cost.cache_price_per_million": 0.3, "cost.output_price_per_million": 0}, "cache_price_per_million = 0.3"},
		{"a subscription by the month", map[string]any{"cost.subscription_price": 20, "cost.subscription_period": "month"}, `subscription_period = "month"`},
		{"a period with no price", map[string]any{"cost.subscription_period": "year"}, `subscription_period = "year"`},
		{"an empty cost table", map[string]any{"cost": map[string]any{}}, "[models.cost]"},
		{"a time price", map[string]any{"cost.time_prices": offPeak}, `timezone = "UTC"`},
		{"the legacy free kind", map[string]any{"cost.kind": "free"}, `kind = "free"`},
		{"a legacy per-token price", map[string]any{"cost.kind": "per_token", "cost.price_per_million_tokens": 2.5}, "price_per_million_tokens = 2.5"},
		{"a legacy subscription", map[string]any{"cost.kind": "subscription", "cost.price_per_period": 20, "cost.period": "year"}, `period = "year"`},
		{"a location of its own", map[string]any{"location": "cloud"}, `location = "cloud"`},
		{"fetch and draft tables", map[string]any{"fetch.repo": "org/beta", "draft": map[string]any{"repo": "org/draft"}}, "[models.draft]"},
		{"an empty fetch table", map[string]any{"fetch": map[string]any{}}, "[models.fetch]"},
		{"fetch keys wt does not model", map[string]any{"fetch": map[string]any{"repo": "org/beta", "files": []string{"a"}, "x_note": 3}}, "x_note = 3"},
	}
	for _, c := range valid {
		t.Run("accepted: "+c.name, func(t *testing.T) {
			path := scratchRegistry(t, docRegistry)
			changed, err := UpdateRegistry(func(d *RegistryDoc) error {
				return d.PatchModel("ollama/beta", c.set, nil)
			})
			if err != nil || !changed {
				t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
			}
			if got := readFile(t, path); !strings.Contains(got, c.want) {
				t.Errorf("the written file should contain %q:\n%s", c.want, got)
			}
		})
	}

	t.Run("a provider row that would not load", func(t *testing.T) {
		path := scratchRegistry(t, docRegistry)
		for _, c := range []struct {
			row  map[string]any
			want string
		}{
			{map[string]any{"id": "p1", "name": "P"}, "auth must be a table"},
			{map[string]any{"id": "p2", "name": "P", "auth": map[string]any{"base_url": "http://x"}}, "auth: type is required"},
			{map[string]any{"id": "p3", "name": "P", "auth": map[string]any{"type": "none"}, "protocols": "openai-chat"}, "protocols"},
		} {
			changed, err := UpdateRegistry(func(d *RegistryDoc) error { return d.AddProvider(c.row) })
			if !errors.Is(err, ErrRegistryInvalid) || changed || !strings.Contains(err.Error(), fmt.Sprintf("provider %q", c.row["id"])) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("AddProvider(%v) = (%v, %v), want ErrRegistryInvalid naming the row and %q", c.row, changed, err, c.want)
			}
			if got := readFile(t, path); got != docRegistry {
				t.Errorf("a refused provider row %q changed the file", c.row["id"])
			}
		}
	})
}

// TestUpdateRegistryValidatesAModelAgainstItsProviderRow covers the two rules
// that need another row, which ResolveLocation judges everywhere else in wt:
// a model with no location of its own inherits its provider's, so a provider
// with none (or a mistyped one) makes the model unusable. A write that leaves
// a touched model in that state is refused; a provider row added in the same
// write counts, which is what lets `wt model add` seed and add at once.
func TestUpdateRegistryValidatesAModelAgainstItsProviderRow(t *testing.T) {
	const gaps = docRegistry + `
[[providers]]
id = "bare"

[providers.auth]
type = "none"

[[providers]]
id = "typo"
location = "Local"

[providers.auth]
type = "none"
`
	add := func(provider string, extra map[string]any) func(*RegistryDoc) error {
		return func(d *RegistryDoc) error {
			row := map[string]any{"id": provider + "/m", "family": "f", "provider_id": provider, "model_name": "m"}
			for k, v := range extra {
				row[k] = v
			}
			return d.AddModel(row)
		}
	}
	refused := []struct {
		name, provider, want string
	}{
		{"a provider row with no location", "bare", `model "bare/m": no location on model or provider "bare"`},
		{"a provider row with a mistyped location", "typo", `provider "typo" has location "Local"`},
		{"no provider row at all", "ghost", `model "ghost/m": provider_id "ghost" names no provider row`},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			path := scratchRegistry(t, gaps)
			changed, err := UpdateRegistry(add(c.provider, nil))
			if !errors.Is(err, ErrRegistryInvalid) || changed || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryInvalid mentioning %q", changed, err, c.want)
			}
			if strings.Count(err.Error(), `model "`+c.provider+`/m"`) != 1 {
				t.Errorf("error %q should name the model once", err)
			}
			if got := readFile(t, path); got != gaps {
				t.Error("a refused write changed the file")
			}
		})
	}
	t.Run("a missing provider row is ErrNoProviderRow and ErrRegistryEntry", func(t *testing.T) {
		scratchRegistry(t, gaps)
		_, err := UpdateRegistry(add("ghost", nil))
		if !errors.Is(err, ErrNoProviderRow) || !errors.Is(err, ErrRegistryEntry) {
			t.Fatalf("err = %v, want ErrNoProviderRow and ErrRegistryEntry", err)
		}
		_, err = UpdateRegistry(add("bare", nil))
		if errors.Is(err, ErrNoProviderRow) {
			t.Fatalf("err = %v, a missing location is not a missing provider row", err)
		}
	})
	t.Run("the model's own location stands in for the provider's", func(t *testing.T) {
		scratchRegistry(t, gaps)
		if changed, err := UpdateRegistry(add("bare", map[string]any{"location": "local"})); err != nil || !changed {
			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
		}
	})
	t.Run("a provider row added in the same write counts", func(t *testing.T) {
		scratchRegistry(t, gaps)
		changed, err := UpdateRegistry(func(d *RegistryDoc) error {
			if err := add("fresh", nil)(d); err != nil {
				return err
			}
			return d.AddProvider(map[string]any{"id": "fresh", "location": "cloud", "auth": map[string]any{"type": "none"}})
		})
		if err != nil || !changed {
			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
		}
	})
	t.Run("an untouched model with a gap does not block the write", func(t *testing.T) {
		scratchRegistry(t, gaps+`
[[models]]
id = "ghost/old"
family = "f"
provider_id = "ghost"
model_name = "old"
`)
		if changed, err := UpdateRegistry(setFamily("ollama/beta", "fine")); err != nil || !changed {
			t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
		}
	})
}
