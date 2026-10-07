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
		{"time_prices that are not rows", map[string]any{"cost.time_prices": "off-peak"}, nil, "time_prices must be an array of tables"},
		{"a time price with an unknown zone", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "Mars/Olympus", "windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "01:00"}}}}}, nil, `timezone "Mars/Olympus" is not a known IANA timezone`},
		{"a time price with no zone", map[string]any{"cost.time_prices": []map[string]any{{"windows": []map[string]any{{"days": []string{"mon"}, "start": "00:00", "end": "01:00"}}}}}, nil, `timezone "" is not a known IANA timezone`},
		{"a time price with no windows", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC"}}}, nil, "windows must be a non-empty array of tables"},
		{"a window on an unknown day", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"funday"}, "start": "00:00", "end": "01:00"}}}}}, nil, "days must be drawn from"},
		{"a window that ends before it starts", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "12:00", "end": "09:00"}}}}}, nil, "start must be before end"},
		{"a window time that is not HH:MM", map[string]any{"cost.time_prices": []map[string]any{{"timezone": "UTC", "windows": []map[string]any{{"days": []string{"mon"}, "start": "9am", "end": "12:00"}}}}}, nil, "start must be HH:MM"},
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
		{"fetch and draft tables", map[string]any{"fetch.repo": "org/beta", "draft": map[string]any{"repo": "org/draft"}}, "[models.draft]"},
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
