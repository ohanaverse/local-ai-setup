package config

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// An alias is a registry `[[aliases]]` table (#596): a gateway id that names
// one model (an alias) or several in priority order (a group). It is never a
// model row: the typed model loops, cloud-sync, modeltiers and llmbench never
// see one. wt-model turns each into LiteLLM database rows (internal/litellm).
//
//	[[aliases]]
//	id = "alias/sonnet-main"
//	targets = [
//	    { model = "openrouter/x", order = 1 },
//	    { model = "ollama/y", order = 2 },
//	]
//
// A renamed alias (#687) keeps its old id as a redirect: `redirect_to =
// "alias/<new>"` in place of targets. The redirect has no targets of its own;
// Config.AliasTargets resolves them from the target alias at read time, so a
// later change to the target alias moves both ids and the two cannot drift.
// One hop only: the target must be a real alias that is not a redirect itself.
const (
	// AliasIDPrefix starts every alias id; no model id may start with it.
	AliasIDPrefix = "alias/"
	// AliasProvider is the provider id an alias's catalog row carries; no
	// provider row may have it.
	AliasProvider = "alias"
)

var (
	// ErrAliasNotFound: no alias has the id.
	ErrAliasNotFound = errors.New("alias not found")
	// ErrAliasExists: an alias already has the id.
	ErrAliasExists = errors.New("alias already exists")
	// ErrAliasRedirected: the alias is the target of a redirect, so it cannot
	// be removed; the error names the redirects.
	ErrAliasRedirected = errors.New("alias is a redirect target")
	// ErrModelInUse: the model is the target of an alias, so it cannot be
	// removed; the error names the aliases.
	ErrModelInUse = errors.New("model is an alias target")
)

// aliasNameRE is what follows AliasIDPrefix in an alias id.
var aliasNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// AliasTarget is one target of an alias: a registry model id and its LiteLLM
// deployment order (1 first; targets with equal order share the requests;
// measured on LiteLLM 1.109.1, a lower order is tried first).
// An order left out of the file reads as the target's 1-based position.
type AliasTarget struct {
	Model string `toml:"model"`
	Order int    `toml:"order,omitempty"`
}

// Alias is one `[[aliases]]` table. It never fails a load (the ServeArgs
// rule: a slip in a hand-edited registry must not stop every wt command): a
// table whose shape is wrong reads with Malformed naming the first fault, and
// with whatever of its id and targets could be read. Consumers treat a
// malformed alias as not launchable and the route path freezes its rows.
// The cross-table rules (AliasFault) are separate: they need the models.
type Alias struct {
	ID string `toml:"id"`
	// RedirectTo is the id of the alias this one redirects to (#687): "" for
	// an ordinary alias. A redirect has no Targets of its own; read them
	// through Config.AliasTargets, never from this field.
	RedirectTo string        `toml:"redirect_to"`
	Targets    []AliasTarget `toml:"targets"`
	Malformed  string        `toml:"-"`
}

// UnmarshalTOML reads an alias table and never fails; see Alias.
func (a *Alias) UnmarshalTOML(v any) error {
	*a = readAlias(v)
	return nil
}

// readAlias is the one judge of an alias table's shape, for the reader
// (UnmarshalTOML) and, through it, the writer (validateAliasRow): an id
// "alias/<name>", then either a non-empty targets array of tables each with a
// non-empty string model and an optional order from 1, no target listed
// twice, or a redirect_to "alias/<name>" (never both), and no key but these
// (`notes` is reserved for later).
func readAlias(v any) Alias {
	var a Alias
	m, ok := v.(map[string]any)
	if !ok {
		a.Malformed = "an alias is not a table"
		return a
	}
	fault := func(format string, args ...any) {
		if a.Malformed == "" {
			a.Malformed = fmt.Sprintf(format, args...)
		}
	}
	id, isString := m["id"].(string)
	a.ID = id
	switch {
	case !isString || id == "":
		fault("id must be a non-empty string")
	case !strings.HasPrefix(id, AliasIDPrefix) || !aliasNameRE.MatchString(strings.TrimPrefix(id, AliasIDPrefix)):
		fault(`id %q is not "alias/<name>" (<name>: lowercase letters, digits, '.' and '-', starting with a letter or digit)`, id)
	}
	var unknown []string
	for k := range m {
		if k != "id" && k != "targets" && k != "redirect_to" {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		fault("unknown key %q (an alias holds id and targets, or id and redirect_to)", k)
	}
	raw, present := m["targets"]
	if rt, hasRedirect := m["redirect_to"]; hasRedirect {
		s, isString := rt.(string)
		switch {
		case present:
			fault("an alias holds targets or redirect_to, not both")
		case !isString || !strings.HasPrefix(s, AliasIDPrefix) || !aliasNameRE.MatchString(strings.TrimPrefix(s, AliasIDPrefix)):
			fault(`redirect_to must be an alias id "alias/<name>"`)
		case s == id:
			fault("redirect_to %q is the alias itself", s)
		default:
			a.RedirectTo = s
		}
		return a
	}
	rows, isRows := aliasTargetRows(raw)
	switch {
	case !present:
		fault("targets is required")
		return a
	case !isRows:
		fault("targets is not an array of tables")
		return a
	case len(rows) == 0:
		fault("targets must hold at least one target")
		return a
	}
	seen := map[string]bool{}
	for i, row := range rows {
		t := AliasTarget{Order: i + 1}
		if row == nil {
			fault("targets[%d] is not a table", i)
			continue
		}
		model, ok := row["model"].(string)
		if !ok || model == "" {
			fault("targets[%d].model must be a non-empty string", i)
		}
		t.Model = model
		if o, present := row["order"]; present {
			n, ok := o.(int64)
			if !ok || n < 1 || n > math.MaxInt32 {
				fault("targets[%d].order must be a whole number from 1", i)
			} else {
				t.Order = int(n)
			}
		}
		var extra []string
		for k := range row {
			if k != "model" && k != "order" {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		for _, k := range extra {
			fault("targets[%d]: unknown key %q (a target holds model and order)", i, k)
		}
		if model != "" && seen[model] {
			fault("target %q is listed more than once", model)
		}
		seen[model] = true
		a.Targets = append(a.Targets, t)
	}
	return a
}

// aliasTargetRows reads a decoded targets value: an inline array of inline
// tables ([]any of maps) or a [[aliases.targets]] array ([]map). An element
// that is not a table is a nil map.
func aliasTargetRows(v any) ([]map[string]any, bool) {
	switch x := v.(type) {
	case []map[string]any:
		return x, true
	case []any:
		out := make([]map[string]any, len(x))
		for i, e := range x {
			out[i], _ = e.(map[string]any)
		}
		return out, true
	}
	return nil, false
}

// AliasByID returns the first alias with the id, malformed ones included.
func (c *Config) AliasByID(id string) (Alias, bool) {
	for _, a := range c.Aliases {
		if a.ID == id {
			return a, true
		}
	}
	return Alias{}, false
}

// AliasTargets is the targets an alias routes to, the one reader every
// consumer uses instead of a.Targets (#687): an ordinary alias's own, or for a
// redirect the target alias's, resolved here at read time. nil for a redirect
// whose target is missing or is itself a redirect (AliasFault names that).
func (c *Config) AliasTargets(a Alias) []AliasTarget {
	if a.RedirectTo == "" {
		return a.Targets
	}
	if t, ok := c.AliasByID(a.RedirectTo); ok && t.RedirectTo == "" {
		return t.Targets
	}
	return nil
}

// AliasRedirectsTo lists, in file order, the ids of the aliases that redirect
// to id.
func (c *Config) AliasRedirectsTo(id string) []string {
	var out []string
	for _, a := range c.Aliases {
		if a.RedirectTo == id && !slices.Contains(out, a.ID) {
			out = append(out, a.ID)
		}
	}
	return out
}

// AliasesTargeting lists, in file order, the ids of the aliases (malformed
// ones included) with a target naming modelID.
func (c *Config) AliasesTargeting(modelID string) []string {
	var out []string
	for _, a := range c.Aliases {
		if slices.ContainsFunc(a.Targets, func(t AliasTarget) bool { return t.Model == modelID }) && !slices.Contains(out, a.ID) {
			out = append(out, a.ID)
		}
	}
	return out
}

// AliasLocalTarget is the alias's local target: the first target that names
// exactly one model whose location resolves to local. AliasFault allows at
// most one.
func (c *Config) AliasLocalTarget(a Alias) (Model, bool) {
	for _, t := range c.AliasTargets(a) {
		m, ok := c.oneModel(t.Model)
		if !ok {
			continue
		}
		if loc, err := c.ResolveLocation(m); err == nil && loc == LocationLocal {
			return m, true
		}
	}
	return Model{}, false
}

// oneModel is the model row id names when exactly one row has it.
func (c *Config) oneModel(id string) (Model, bool) {
	var found []Model
	for _, m := range c.Models {
		if m.ID == id {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		return Model{}, false
	}
	return found[0], true
}

// AliasFault is the one home of the alias rules that need the rest of the
// registry: the id is no model's id; every target names exactly one model
// row, is not an alias and is not native; and at most one target is local
// (a start then starts one model, as for a slot profile). A malformed alias
// is a fault too, naming its Malformed. nil when the alias is sound. Used by
// Config.validate, the registry writer and the route path.
func AliasFault(cfg *Config, a Alias) error {
	if a.Malformed != "" {
		return fmt.Errorf("alias %q is malformed: %s", a.ID, a.Malformed)
	}
	if slices.ContainsFunc(cfg.Models, func(m Model) bool { return m.ID == a.ID }) {
		return fmt.Errorf("alias %q: its id is also a model id; an alias id must not name a model", a.ID)
	}
	if a.RedirectTo != "" {
		return redirectFault(cfg, a)
	}
	var local []string
	for _, t := range a.Targets {
		if strings.HasPrefix(t.Model, AliasIDPrefix) || slices.ContainsFunc(cfg.Aliases, func(o Alias) bool { return o.ID == t.Model }) {
			return fmt.Errorf("alias %q: target %q is an alias; an alias cannot target an alias", a.ID, t.Model)
		}
		var providers []string
		var m Model
		for _, row := range cfg.Models {
			if row.ID == t.Model {
				providers = append(providers, row.ProviderID)
				m = row
			}
		}
		switch len(providers) {
		case 0:
			return fmt.Errorf("alias %q: target %q names no model", a.ID, t.Model)
		case 1:
		default:
			return fmt.Errorf("alias %q: target %s", a.ID, DuplicatedModelText(t.Model, providers))
		}
		if p := cfg.ProviderByID(m.ProviderID); m.Native || p != nil && p.Auth.Type == "native" {
			return fmt.Errorf("alias %q: target %q is a native model, which no gateway serves", a.ID, t.Model)
		}
		if loc, err := cfg.ResolveLocation(m); err == nil && loc == LocationLocal {
			local = append(local, t.Model)
		}
	}
	if len(local) > 1 {
		return fmt.Errorf("alias %q has %d local targets (%s); an alias has at most one local target",
			a.ID, len(local), strings.Join(local, ", "))
	}
	return nil
}

// redirectFault is AliasFault for a redirect (#687): the target must be a real
// alias that is not a redirect, and no other alias may redirect to this one
// (one hop only). The target alias's own targets are judged on the target
// alias, not repeated here.
func redirectFault(cfg *Config, a Alias) error {
	t, ok := cfg.AliasByID(a.RedirectTo)
	switch {
	case !ok:
		return fmt.Errorf("alias %q redirects to %q, which is not an alias", a.ID, a.RedirectTo)
	case t.RedirectTo != "":
		return fmt.Errorf("alias %q redirects to %q, which is itself a redirect; a redirect is one hop", a.ID, a.RedirectTo)
	}
	if by := cfg.AliasRedirectsTo(a.ID); len(by) > 0 {
		return fmt.Errorf("alias %q is a redirect and %s redirects to it; a redirect is one hop", a.ID, strings.Join(by, ", "))
	}
	return nil
}
