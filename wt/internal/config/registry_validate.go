package config

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// ErrRegistryInvalid is returned by UpdateRegistry when a row the write
// touched would not load: in wt's own typed reader, or in modelman's, whose
// required-field and cost rules are ported here because modelman still reads
// the file wt writes. Nothing is written.
var ErrRegistryInvalid = errors.New("invalid registry entry")

// validateTouched checks the rows this write changed or added, and only
// those. A bad row elsewhere in the file is not this write's doing and must
// not block it: the write may be the very edit or removal that repairs the
// file.
func (d *RegistryDoc) validateTouched() error {
	for _, row := range d.touchedProviders {
		if err := validateProviderRow(row); err != nil {
			return fmt.Errorf("%w: provider %q: %w", ErrRegistryInvalid, rowID(row), err)
		}
	}
	for _, row := range d.touchedModels {
		if err := validateModelRow(row); err != nil {
			return fmt.Errorf("%w: model %q: %w", ErrRegistryInvalid, rowID(row), err)
		}
		// Its message names the model itself, as Config.Validate's does.
		if err := d.validateModelRefs(row); err != nil {
			return fmt.Errorf("%w: %w", ErrRegistryInvalid, err)
		}
	}
	return nil
}

// ErrNoProviderRow marks a model whose provider_id names no provider row, so a
// caller can choose its advice without matching on the message.
var ErrNoProviderRow = errors.New("names no provider row")

// validateModelRefs checks the two rules of Config.Validate that need other
// rows: the model's provider_id names a provider row, and its location — its
// own, or the one it inherits from that provider — resolves to "local" or
// "cloud". Without them `wt model edit --location mars` would be written, and
// every wt command would then refuse to run until the file was fixed by hand.
// ResolveLocation is the judge, as everywhere else; it is asked over the
// document's provider rows as they are after apply, so a provider row seeded
// in the same write counts. Errors are marked ErrRegistryEntry like
// Config.Validate's, so the repair hint names registry.toml.
func (d *RegistryDoc) validateModelRefs(row *tomlw.Table) error {
	str := func(t *tomlw.Table, key string) string {
		v, _ := t.Get(key)
		s, _ := v.(string)
		return s
	}
	cfg := &Config{}
	for _, p := range d.rows("providers") {
		cfg.Providers = append(cfg.Providers, Provider{ID: rowID(p), Location: Location(str(p, "location"))})
	}
	m := Model{ID: rowID(row), ProviderID: str(row, "provider_id"), Location: Location(str(row, "location"))}
	if cfg.ProviderByID(m.ProviderID) == nil {
		return registryEntryError(fmt.Errorf("model %q: provider_id %q %w", m.ID, m.ProviderID, ErrNoProviderRow))
	}
	if _, err := cfg.ResolveLocation(m); err != nil {
		return registryEntryError(err)
	}
	return nil
}

// validateProviderRow is modelman's _parse_provider (an id, and an auth table
// with a type) plus wt's typed decode.
func validateProviderRow(row *tomlw.Table) error {
	if err := requireStrings(row, "id"); err != nil {
		return err
	}
	auth, _ := row.Get("auth")
	authTable, ok := auth.(*tomlw.Table)
	if !ok {
		return errors.New("auth must be a table with a `type`")
	}
	if err := requireStrings(authTable, "type"); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	return typedDecode("providers", row, &struct {
		Providers []Provider `toml:"providers"`
	}{})
}

// validateModelRow is modelman's _parse_model and _parse_cost plus wt's
// typed decode and its one rule of its own, a non-empty model_name.
func validateModelRow(row *tomlw.Table) error {
	if err := requireStrings(row, "id", "family", "provider_id", "model_name"); err != nil {
		return err
	}
	if cost, ok := row.Get("cost"); ok {
		table, isTable := cost.(*tomlw.Table)
		if !isTable {
			return errors.New("cost must be a table")
		}
		if err := validateCost(table); err != nil {
			return fmt.Errorf("cost: %w", err)
		}
	}
	// wt's reader takes a malformed fetch or draft for an absent one
	// (ModelArtifact.UnmarshalTOML), so the typed decode below does not
	// notice it; this is where it is named.
	for _, k := range []string{"fetch", "draft"} {
		if v, ok := row.Get(k); ok {
			if err := validateArtifact(k, v); err != nil {
				return err
			}
		}
	}
	return typedDecode("models", row, &struct {
		Models []Model `toml:"models"`
	}{})
}

// validateArtifact checks a fetch or draft value against the shape wt reads
// (artifactFields): a table whose repo and local_path, when present, are
// strings. modelman reads the two with dict methods, so anything but a table
// crashes its load rather than reports. An empty table and keys wt does not
// model pass, as they load.
func validateArtifact(key string, v any) error {
	table, isTable := v.(*tomlw.Table)
	if !isTable {
		return fmt.Errorf("%s must be a table", key)
	}
	for _, f := range artifactFields {
		if fv, ok := table.Get(f.key); ok {
			if _, isString := fv.(string); !isString {
				return fmt.Errorf("%s.%s must be a string", key, f.key)
			}
		}
	}
	return nil
}

// requireStrings checks that each key is a non-empty string. modelman only
// requires the keys to be present; an empty id or model_name is wt's own
// validation error (Config.validate), and an empty family or provider_id
// names nothing.
func requireStrings(row *tomlw.Table, keys ...string) error {
	for _, k := range keys {
		v, ok := row.Get(k)
		if !ok {
			return fmt.Errorf("%s is required", k)
		}
		if s, isString := v.(string); !isString || s == "" {
			return fmt.Errorf("%s must be a non-empty string", k)
		}
	}
	return nil
}

// typedDecode runs one row through the struct decode wt's reader uses, so a
// value of the wrong type (tags = "code", a string price) is caught before
// it is written rather than on the next load.
func typedDecode(key string, row *tomlw.Table, into any) error {
	doc := tomlw.NewTable()
	doc.Set(key, []any{row})
	text, err := tomlw.Encode(doc)
	if err != nil {
		return err
	}
	if _, err := toml.Decode(string(text), into); err != nil {
		return err
	}
	return nil
}

var (
	priceKeys           = []string{"input_price_per_million", "cache_price_per_million", "output_price_per_million"}
	subscriptionPeriods = []string{"month", "year"}
	legacyCostKinds     = []string{"free", "per_token", "subscription"}
	weekDays            = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	hhmm                = regexp.MustCompile(`^([0-9]{2}):([0-9]{2})$`)
)

// validateCost ports modelman's cost rules (registry.py _parse_cost and
// _validate_cost, time_pricing.py parse_time_prices).
func validateCost(cost *tomlw.Table) error {
	if kind, ok := cost.Get("kind"); ok {
		// The legacy shape, which modelman still migrates on load. It reads
		// the price and the period under their old names, and holds them to
		// the same rules as the new ones.
		s, _ := kind.(string)
		if !slices.Contains(legacyCostKinds, s) {
			return fmt.Errorf("kind must be free/per_token/subscription, got %v", kind)
		}
		switch s {
		case "per_token":
			if err := checkPrice(cost, "price_per_million_tokens"); err != nil {
				return err
			}
		case "subscription":
			if err := checkPrice(cost, "price_per_period"); err != nil {
				return err
			}
			if err := checkPeriod(cost, "price_per_period", "period"); err != nil {
				return err
			}
		}
	} else {
		for _, k := range append(slices.Clone(priceKeys), "subscription_price") {
			if err := checkPrice(cost, k); err != nil {
				return err
			}
		}
		if err := checkPeriod(cost, "subscription_price", "subscription_period"); err != nil {
			return err
		}
	}
	raw, ok := cost.Get("time_prices")
	if !ok {
		return nil
	}
	rows, isRows := tableArray(raw)
	if !isRows {
		return errors.New("time_prices must be an array of tables")
	}
	for i, row := range rows {
		if err := validateTimePrice(row); err != nil {
			return fmt.Errorf("time_prices[%d]: %w", i, err)
		}
	}
	return nil
}

// checkPeriod: the period key is a string when present, and month or year
// when the price key beside it is set.
func checkPeriod(cost *tomlw.Table, priceKey, periodKey string) error {
	period, hasPeriod := cost.Get(periodKey)
	if _, isString := period.(string); hasPeriod && !isString {
		return fmt.Errorf("%s must be a string", periodKey)
	}
	if cost.Has(priceKey) {
		if s, _ := period.(string); !slices.Contains(subscriptionPeriods, s) {
			return fmt.Errorf("%s must be month or year when %s is set, got %q", periodKey, priceKey, s)
		}
	}
	return nil
}

// checkPrice: a number (never a bool), finite, not negative.
func checkPrice(t *tomlw.Table, key string) error {
	v, ok := t.Get(key)
	if !ok {
		return nil
	}
	var f float64
	switch n := v.(type) {
	case int64:
		f = float64(n)
	case float64:
		f = n
	default:
		return fmt.Errorf("%s must be a number", key)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("%s must be finite", key)
	}
	if f < 0 {
		return fmt.Errorf("%s must be non-negative", key)
	}
	return nil
}

func validateTimePrice(row *tomlw.Table) error {
	tz, _ := row.Get("timezone")
	name, _ := tz.(string)
	// time.LoadLocation accepts "" (UTC) and "Local"; Python's ZoneInfo, which
	// modelman validates with, accepts neither.
	if name == "" || name == "Local" {
		return fmt.Errorf("timezone %q is not a known IANA timezone", name)
	}
	// Known limit: LoadLocation reads the host's zone database and wt does not
	// embed one (time/tzdata), so on a host without it every zone but UTC is
	// refused here. macOS, wt's platform, ships one.
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("timezone %q is not a known IANA timezone", name)
	}
	for _, k := range priceKeys {
		if err := checkPrice(row, k); err != nil {
			return err
		}
	}
	raw, _ := row.Get("windows")
	windows, ok := tableArray(raw)
	if !ok || len(windows) == 0 {
		return errors.New("windows must be a non-empty array of tables")
	}
	for i, w := range windows {
		if err := validateWindow(w); err != nil {
			return fmt.Errorf("windows[%d]: %w", i, err)
		}
	}
	return nil
}

func validateWindow(w *tomlw.Table) error {
	raw, _ := w.Get("days")
	days, ok := raw.([]any)
	if !ok || len(days) == 0 {
		return errors.New("days must be a non-empty list")
	}
	for _, d := range days {
		if s, _ := d.(string); !slices.Contains(weekDays, s) {
			return fmt.Errorf("days must be drawn from %v, got %v", weekDays, d)
		}
	}
	start, err := minutesOfDay(w, "start")
	if err != nil {
		return err
	}
	end, err := minutesOfDay(w, "end")
	if err != nil {
		return err
	}
	if start >= 24*60 {
		return errors.New("start must be before 24:00")
	}
	if start >= end {
		return errors.New("start must be before end")
	}
	return nil
}

// minutesOfDay reads an "HH:MM" key between 00:00 and 24:00.
func minutesOfDay(w *tomlw.Table, key string) (int, error) {
	v, _ := w.Get(key)
	s, _ := v.(string)
	m := hhmm.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%s must be HH:MM, got %v", key, v)
	}
	hours, _ := strconv.Atoi(m[1])
	minutes, _ := strconv.Atoi(m[2])
	total := hours*60 + minutes
	if minutes > 59 || total > 24*60 {
		return 0, fmt.Errorf("%s must be HH:MM between 00:00 and 24:00, got %q", key, s)
	}
	return total, nil
}
