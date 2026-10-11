package config

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// Errors the registry operations return, each wrapped with the id or keys it
// is about. Callers match them with errors.Is.
var (
	// ErrModelNotFound: no model row has the id.
	ErrModelNotFound = errors.New("model not found")
	// ErrModelExists: a model row already has the id.
	ErrModelExists = errors.New("model already exists")
	// ErrModelAmbiguous: more than one model row has the id, so an operation
	// that addresses a row by its id cannot tell which one is meant. The
	// error that matches it carries its own text (ambiguousModelErr): the id,
	// each row's provider, and where to repair it.
	ErrModelAmbiguous = errors.New("model id is in the registry more than once")
	// ErrProviderExists: a provider row already has the id.
	ErrProviderExists = errors.New("provider already exists")
	// ErrRegistryTopLevel: registry.toml holds a top-level key that is not
	// providers, families or models, or one of those is not an array of
	// tables. The write is refused: `[[model]]` for `[[models]]` parses and
	// reads as no models, so a write on top of it would hide the typo (#247).
	ErrRegistryTopLevel = errors.New("registry.toml has an unexpected top level")
)

// The keys of each kind of registry table, in schema order: the order the
// rows of a registry on disk already have them in. A key wt adds to a row
// goes at its place in these lists, so a row wt edits keeps a stable layout
// and reads like the rows around it. They order keys; they are not a list of
// what a row may hold.
var (
	registryTopLevelKeys = []string{"providers", "families", "models"}

	providerSchemas = map[string][]string{
		"":     {"id", "name", "location", "model_dir", "protocols", "auth"},
		"auth": {"type", "secret_ref", "base_url"},
	}
	modelSchemas = map[string][]string{
		"": {"id", "family", "provider_id", "model_name", "location", "source", "tags", "cost",
			"model_info", "fetch", "quantization", "pricing_updated_at", "draft"},
		"cost": {"input_price_per_million", "cache_price_per_million", "output_price_per_million",
			"subscription_price", "subscription_period", "time_prices"},
		"cost.time_prices": {"label", "timezone", "input_price_per_million", "cache_price_per_million",
			"output_price_per_million", "windows"},
		"cost.time_prices.windows": {"days", "start", "end"},
		"fetch":                    {"repo", "files", "quantizations", "local_path"},
		"draft":                    {"repo", "local_path"},
	}
)

// RegistryDoc is registry.toml as UpdateRegistry hands it to a writer: the
// whole document, with every key in place, and a small set of operations on
// it. The operations are patch-shaped — each names the keys it sets or
// deletes — and nothing here reads or writes a config.Model or
// config.Provider. That is what keeps a key wt does not model (fetch, draft,
// catalog_name, anything hand-added) out of harm's way, and why adding a
// field to config.Model cannot change what a write touches.
//
// A key is addressed by its dotted path inside the row:
// "cost.input_price_per_million", "fetch.repo". A key whose own name holds a
// dot cannot be addressed; set the table that holds it instead.
//
// A value may be a bool, string, int, int64, float64, time.Time, []string,
// []any, map[string]any, []map[string]any or *tomlw.Table. A map's keys are
// written in schema order, unknown ones first and sorted.
type RegistryDoc struct {
	root *tomlw.Table
	// touched are the rows an operation changed or added, by kind. Only
	// these are validated before the write.
	touchedModels    []*tomlw.Table
	touchedProviders []*tomlw.Table
}

// newRegistryDoc wraps a decoded registry, refusing a top level a write
// cannot be trusted with.
func newRegistryDoc(root *tomlw.Table) (*RegistryDoc, error) {
	var unknown []string
	for _, k := range root.Keys() {
		if !slices.Contains(registryTopLevelKeys, k) {
			unknown = append(unknown, "`"+k+"`")
			continue
		}
		v, _ := root.Get(k)
		if _, ok := tableArray(v); !ok {
			return nil, fmt.Errorf("%w: `%s` is not an array of tables", ErrRegistryTopLevel, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("%w: unknown top-level key(s) %s: a registry holds providers/families/models only",
			ErrRegistryTopLevel, strings.Join(unknown, ", "))
	}
	return &RegistryDoc{root: root}, nil
}

// tableArray reads v as an array holding only tables.
func tableArray(v any) ([]*tomlw.Table, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	rows := make([]*tomlw.Table, len(arr))
	for i, e := range arr {
		t, ok := e.(*tomlw.Table)
		if !ok {
			return nil, false
		}
		rows[i] = t
	}
	return rows, true
}

func (d *RegistryDoc) rows(key string) []*tomlw.Table {
	v, _ := d.root.Get(key)
	rows, _ := tableArray(v)
	return rows
}

func (d *RegistryDoc) setRows(key string, rows []*tomlw.Table) {
	arr := make([]any, len(rows))
	for i := range rows {
		arr[i] = rows[i]
	}
	d.root.SetAt(key, arr, registryTopLevelKeys)
}

func rowID(row *tomlw.Table) string {
	v, _ := row.Get("id")
	id, _ := v.(string)
	return id
}

func findRow(rows []*tomlw.Table, id string) int {
	return slices.IndexFunc(rows, func(r *tomlw.Table) bool { return rowID(r) == id })
}

// ambiguousModelErr is the refusal of an id more than one model row carries.
// It matches ErrModelAmbiguous and nothing else: it is not a "not found", and
// it carries the registry hint itself, so no caller appends a second one.
type ambiguousModelErr struct {
	id        string
	providers []string
}

func (e ambiguousModelErr) Error() string {
	return fmt.Sprintf("%s; wt cannot tell which one you mean — %s",
		DuplicatedModelText(e.id, e.providers), RegistryFixHint(ErrRegistryEntry))
}

func (e ambiguousModelErr) Is(target error) bool { return target == ErrModelAmbiguous }

// DuplicatedModelText says that id is on more than one model row, naming
// each row's provider_id in file order: `model "x" is in the registry twice
// (providers ollama, omlx)`. A row with no provider_id is "(none)". It is the
// one wording for a duplicated id, shared by the writer's refusal
// (ErrModelAmbiguous) and `wt litellm sync`'s warning, which each add what
// they do about it.
func DuplicatedModelText(id string, providers []string) string {
	named := make([]string, len(providers))
	for i, p := range providers {
		if named[i] = p; p == "" {
			named[i] = "(none)"
		}
	}
	times := "twice"
	if len(named) != 2 {
		times = fmt.Sprintf("%d times", len(named))
	}
	return fmt.Sprintf("model %q is in the registry %s (providers %s)", id, times, strings.Join(named, ", "))
}

// oneModelError is the answer to "which row is id" from the provider_id of
// every row that carries it, in file order: nil for exactly one row,
// ErrModelNotFound for none, ErrModelAmbiguous for more.
func oneModelError(id string, providers []string) error {
	switch len(providers) {
	case 0:
		return fmt.Errorf("%w: %q", ErrModelNotFound, id)
	case 1:
		return nil
	}
	return ambiguousModelErr{id: id, providers: providers}
}

// CheckModelRow reports whether id names exactly one model of the loaded
// registry, with the errors the writer gives (RegistryDoc.Model). It is for
// a command that asks the user something before it writes: the write would
// refuse the id anyway, and this refuses it before the question.
func (c *Config) CheckModelRow(id string) error {
	var providers []string
	for _, m := range c.Models {
		if m.ID == id {
			providers = append(providers, m.ProviderID)
		}
	}
	return oneModelError(id, providers)
}

// modelRow finds the one model row with the id, for an operation that reads
// or changes it. Two rows under one id are a registry Config.Validate
// refuses, yet the commands that repair a registry (`wt model edit`, `wt
// model rm`) are let into it on purpose — so the row is never "the first
// one": which of them the user means cannot be told from the id, and the
// refusal says so (ErrModelAmbiguous). No row is ErrModelNotFound.
func (d *RegistryDoc) modelRow(id string) (rows []*tomlw.Table, i int, err error) {
	rows = d.rows("models")
	i = -1
	var providers []string
	for n, r := range rows {
		if rowID(r) != id {
			continue
		}
		if i < 0 {
			i = n
		}
		p, _ := r.Get("provider_id")
		name, _ := p.(string)
		providers = append(providers, name)
	}
	if err := oneModelError(id, providers); err != nil {
		return nil, -1, err
	}
	return rows, i, nil
}

// Model returns a copy of the model row id, with the errors every operation
// that addresses a row by its id gives: ErrModelNotFound when no row has
// it, ErrModelAmbiguous when more than one has. For a caller that reads the
// row to decide what to patch.
func (d *RegistryDoc) Model(id string) (*tomlw.Table, error) {
	rows, i, err := d.modelRow(id)
	if err != nil {
		return nil, err
	}
	return rows[i].Clone(), nil
}

func (d *RegistryDoc) touchModel(row *tomlw.Table) {
	if !slices.Contains(d.touchedModels, row) {
		d.touchedModels = append(d.touchedModels, row)
	}
}

// Models returns a copy of every model row, in file order. Changing a copy
// changes nothing in the document.
func (d *RegistryDoc) Models() []*tomlw.Table { return cloneRows(d.rows("models")) }

// Providers returns a copy of every provider row, in file order.
func (d *RegistryDoc) Providers() []*tomlw.Table { return cloneRows(d.rows("providers")) }

func cloneRows(rows []*tomlw.Table) []*tomlw.Table {
	out := make([]*tomlw.Table, len(rows))
	for i := range rows {
		out[i] = rows[i].Clone()
	}
	return out
}

// PatchModel sets and deletes named keys on the model row id; every other
// key stays as it is. A key whose value is already the one asked for is not
// rewritten — an integer price asked to be the same number as a float stays
// an integer. A new key goes at its schema position. The id itself cannot be
// patched: a row under a new id is CloneModel's job. An id more than one row
// has is refused (ErrModelAmbiguous), like every operation that finds a row
// by its id.
func (d *RegistryDoc) PatchModel(id string, set map[string]any, unset []string) error {
	rows, i, err := d.modelRow(id)
	if err != nil {
		return err
	}
	if _, ok := set["id"]; ok || slices.Contains(unset, "id") {
		return fmt.Errorf("model %q: the id cannot be patched", id)
	}
	// Patch a copy and swap it in only when every key applied: a patch that
	// fails on its second key must not leave the first one in the document,
	// where a caller that tolerates the error would write it unvalidated.
	work := rows[i].Clone()
	changed, err := patchRow(work, modelSchemas, set, unset)
	if err != nil {
		return fmt.Errorf("model %q: %w", id, err)
	}
	if changed {
		*rows[i] = *work
		d.touchModel(rows[i])
	}
	return nil
}

// AddModel appends a model row. The table needs a non-empty string id that
// no row has yet; an id one row or several already have is ErrModelExists.
func (d *RegistryDoc) AddModel(table map[string]any) error {
	id, _ := table["id"].(string)
	if id == "" {
		return errors.New("a model needs a non-empty string id")
	}
	rows := d.rows("models")
	if findRow(rows, id) >= 0 {
		return fmt.Errorf("%w: %q", ErrModelExists, id)
	}
	row, err := orderedTable(table, modelSchemas, "")
	if err != nil {
		return fmt.Errorf("model %q: %w", id, err)
	}
	d.setRows("models", append(rows, row))
	d.touchModel(row)
	return nil
}

// CloneModel appends a copy of the model row fromID with overrides set on it
// (dotted keys, as PatchModel's set). overrides must give the copy an id no
// row has; everything else, unknown keys included, is carried over. A fromID
// more than one row has is ErrModelAmbiguous: there is no telling which row
// to copy.
func (d *RegistryDoc) CloneModel(fromID string, overrides map[string]any) error {
	rows, i, err := d.modelRow(fromID)
	if err != nil {
		return err
	}
	row := rows[i].Clone()
	if _, err := patchRow(row, modelSchemas, overrides, nil); err != nil {
		return fmt.Errorf("clone of model %q: %w", fromID, err)
	}
	id := rowID(row)
	if id == "" {
		return fmt.Errorf("clone of model %q: the copy needs a non-empty string id", fromID)
	}
	if findRow(rows, id) >= 0 {
		return fmt.Errorf("%w: %q", ErrModelExists, id)
	}
	d.setRows("models", append(rows, row))
	d.touchModel(row)
	return nil
}

// RemoveModel deletes the model row id and returns it, so the caller can
// still read what it held (where its weights are, say). An id more than one
// row has is ErrModelAmbiguous and removes none of them: that is a registry
// to repair by hand, where the user can see which row is which.
func (d *RegistryDoc) RemoveModel(id string) (*tomlw.Table, error) {
	rows, i, err := d.modelRow(id)
	if err != nil {
		return nil, err
	}
	row := rows[i]
	d.setRows("models", slices.Delete(slices.Clone(rows), i, i+1))
	d.touchedModels = slices.DeleteFunc(d.touchedModels, func(r *tomlw.Table) bool { return r == row })
	return row, nil
}

// SetTimePrices replaces the model's cost.time_prices with rows; no rows
// deletes the key. Rows equal to the ones already there are not rewritten.
// It is a PatchModel, and refuses what that refuses.
func (d *RegistryDoc) SetTimePrices(id string, rows []map[string]any) error {
	if len(rows) == 0 {
		return d.PatchModel(id, nil, []string{"cost.time_prices"})
	}
	return d.PatchModel(id, map[string]any{"cost.time_prices": rows}, nil)
}

// AddProvider appends a provider row. The table needs a non-empty string id
// that no row has yet. Seeding is its only caller.
func (d *RegistryDoc) AddProvider(table map[string]any) error {
	id, _ := table["id"].(string)
	if id == "" {
		return errors.New("a provider needs a non-empty string id")
	}
	rows := d.rows("providers")
	if findRow(rows, id) >= 0 {
		return fmt.Errorf("%w: %q", ErrProviderExists, id)
	}
	row, err := orderedTable(table, providerSchemas, "")
	if err != nil {
		return fmt.Errorf("provider %q: %w", id, err)
	}
	d.setRows("providers", append(rows, row))
	d.touchedProviders = append(d.touchedProviders, row)
	return nil
}

// patchRow applies set (in sorted key order, so the result does not depend on
// map iteration) and then unset to row, and reports whether anything changed.
func patchRow(row *tomlw.Table, schemas map[string][]string, set map[string]any, unset []string) (bool, error) {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changed := false
	for _, key := range keys {
		did, err := setPath(row, schemas, key, set[key])
		if err != nil {
			return false, err
		}
		changed = changed || did
	}
	for _, key := range unset {
		did, err := unsetPath(row, key)
		if err != nil {
			return false, err
		}
		changed = changed || did
	}
	return changed, nil
}

func splitKey(key string) ([]string, error) {
	parts := strings.Split(key, ".")
	if slices.Contains(parts, "") {
		return nil, fmt.Errorf("%q is not a key", key)
	}
	return parts, nil
}

// setPath sets the dotted key on row, creating the tables on the way at
// their schema positions. It reports false when the key already had the
// value.
func setPath(row *tomlw.Table, schemas map[string][]string, key string, value any) (bool, error) {
	parts, err := splitKey(key)
	if err != nil {
		return false, err
	}
	val, err := orderedValue(value, schemas, key)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	cur := row
	for i, part := range parts[:len(parts)-1] {
		parent := strings.Join(parts[:i], ".")
		next, ok := cur.Get(part)
		if !ok {
			sub := tomlw.NewTable()
			cur.SetAt(part, sub, schemas[parent])
			cur = sub
			continue
		}
		sub, isTable := next.(*tomlw.Table)
		if !isTable {
			return false, fmt.Errorf("%s: %s is not a table", key, strings.Join(parts[:i+1], "."))
		}
		cur = sub
	}
	last := parts[len(parts)-1]
	if old, ok := cur.Get(last); ok && tomlw.Same(old, val) {
		return false, nil
	}
	cur.SetAt(last, val, schemas[strings.Join(parts[:len(parts)-1], ".")])
	return true, nil
}

// unsetPath deletes the dotted key from row and reports whether it was
// there. A table left empty by the delete is kept: an empty [models.cost]
// is a valid row, and removing it is the caller's to ask for.
func unsetPath(row *tomlw.Table, key string) (bool, error) {
	parts, err := splitKey(key)
	if err != nil {
		return false, err
	}
	cur := row
	for _, part := range parts[:len(parts)-1] {
		next, _ := cur.Get(part)
		sub, ok := next.(*tomlw.Table)
		if !ok {
			return false, nil
		}
		cur = sub
	}
	return cur.Delete(parts[len(parts)-1]), nil
}

// orderedValue converts a caller's value into a document value, giving every
// map the key order of the table at path.
func orderedValue(v any, schemas map[string][]string, path string) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		return orderedTable(x, schemas, path)
	case *tomlw.Table:
		if x == nil {
			return nil, errors.New("nil table")
		}
		return x.Clone(), nil
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			row, err := orderedTable(x[i], schemas, path)
			if err != nil {
				return nil, err
			}
			out[i] = row
		}
		return out, nil
	case []*tomlw.Table:
		// Cloned like a lone table, so the caller's rows are never the
		// document's: a later change through the caller's pointer must not
		// reach a row that was already validated.
		out := make([]*tomlw.Table, len(x))
		for i := range x {
			if x[i] == nil {
				return nil, errors.New("nil table")
			}
			out[i] = x[i].Clone()
		}
		return tomlw.Value(out)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			item, err := orderedValue(x[i], schemas, path)
			if err != nil {
				return nil, err
			}
			out[i] = item
		}
		return out, nil
	}
	return tomlw.Value(v)
}

// orderedTable builds the table at path from m: the keys the schema does not
// list first, sorted (unmodelled keys go before the modelled ones, as
// tomlw.Table.SetAt places one), then the schema's keys in schema order.
func orderedTable(m map[string]any, schemas map[string][]string, path string) (*tomlw.Table, error) {
	schema := schemas[path]
	var unknown []string
	for k := range m {
		if !slices.Contains(schema, k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	t := tomlw.NewTable()
	for _, k := range append(unknown, schema...) {
		v, ok := m[k]
		if !ok {
			continue
		}
		child := k
		if path != "" {
			child = path + "." + k
		}
		val, err := orderedValue(v, schemas, child)
		if err != nil {
			return nil, err
		}
		t.Set(k, val)
	}
	return t, nil
}
