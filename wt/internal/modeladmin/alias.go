package modeladmin

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
	"github.com/ohanaverse/local-ai-setup/wt/internal/windows"
)

// The registry half of `wt-model alias list|set|rm` (#596): a registry alias
// is a top-level [[aliases]] table (config.Alias), written only here through
// config.UpdateRegistry. The LiteLLM rows follow from the route sync the
// command runs after the write; nothing here reaches LiteLLM.

// AliasRequest is one `alias set`: the alias id and its whole target list
// (it replaces whatever the alias had). An Order of 0 is written without an
// order key, which the reader takes as the target's 1-based position.
type AliasRequest struct {
	ID      string
	Targets []config.AliasTarget
}

// alias is r as the reader would read it: the shape judged by the reader's
// own judge (config.Alias.UnmarshalTOML), so the command and the writer
// refuse the same things.
func (r AliasRequest) alias() config.Alias {
	var a config.Alias
	_ = a.UnmarshalTOML(r.table())
	return a
}

// table is r as the decoded TOML table the reader judges: targets as inline
// tables, an order only where one was given.
func (r AliasRequest) table() map[string]any {
	targets := make([]any, 0, len(r.Targets))
	for _, t := range r.Targets {
		row := map[string]any{"model": t.Model}
		if t.Order != 0 {
			row["order"] = int64(t.Order)
		}
		targets = append(targets, row)
	}
	return map[string]any{"id": r.ID, "targets": targets}
}

// targetRows is r's targets as RegistryDoc.SetAliasTargets takes them.
func (r AliasRequest) targetRows() []map[string]any {
	rows := make([]map[string]any, 0, len(r.Targets))
	for _, t := range r.Targets {
		row := map[string]any{"model": t.Model}
		if t.Order != 0 {
			row["order"] = t.Order
		}
		rows = append(rows, row)
	}
	return rows
}

// ConfigWithAlias is a shallow copy of cfg whose aliases hold r in place of
// the alias with its id (appended when there is none), and r as the reader
// reads it: what the command plans the window and the dry run's rows over.
// cfg is not changed.
func ConfigWithAlias(cfg *config.Config, r AliasRequest) (*config.Config, config.Alias) {
	a := r.alias()
	out := *cfg
	out.Aliases = slices.Clone(cfg.Aliases)
	if i := slices.IndexFunc(out.Aliases, func(o config.Alias) bool { return o.ID == a.ID }); i >= 0 {
		out.Aliases[i] = a
	} else {
		out.Aliases = append(out.Aliases, a)
	}
	return &out, a
}

// CheckAlias refuses an alias request that the registry writer would refuse,
// before anything is asked or written: its shape (an id alias/<name>, at
// least one target, none twice, orders from 1) and the cross-table rules
// over cfg with the request in place (config.AliasFault: every target names
// exactly one model that is neither an alias nor native, at most one local
// target, an id no model has).
func CheckAlias(cfg *config.Config, r AliasRequest) error {
	if slices.ContainsFunc(r.Targets, func(t config.AliasTarget) bool { return t.Order < 0 }) {
		return fmt.Errorf("alias %q: an order must be a whole number from 1", r.ID)
	}
	after, a := ConfigWithAlias(cfg, r)
	if a.Malformed != "" {
		return fmt.Errorf("alias %q: %s", a.ID, a.Malformed)
	}
	return config.AliasFault(after, a)
}

// SetAlias writes r to the registry: a new [[aliases]] table, or the whole
// target list of the alias with its id replaced. changed is false when the
// file already held exactly that. The apply is pure (config.UpdateRegistry
// may run it up to three times): each run checks r again against the
// document it is handed (CheckAlias over RegistryDoc.RefConfig), so a model
// another program removed since the caller's check is refused here too.
func SetAlias(r AliasRequest) (changed bool, err error) {
	return config.UpdateRegistry(func(d *config.RegistryDoc) error {
		if err := CheckAlias(d.RefConfig(), r); err != nil {
			return err
		}
		exists := slices.ContainsFunc(d.Aliases(), func(t *tomlw.Table) bool {
			id, _ := t.Get("id")
			return id == r.ID
		})
		if !exists {
			row := tomlw.NewTable()
			row.Set("id", r.ID)
			row.Set("targets", []any{})
			if err := d.AddAlias(row); err != nil {
				return err
			}
		}
		return d.SetAliasTargets(r.ID, r.targetRows())
	})
}

// RemoveAliases removes the aliases ids from the registry in one write, all
// or nothing: an id the registry does not have (config.ErrAliasNotFound)
// removes none. It returns the ids removed, in the order given.
func RemoveAliases(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, errors.New("no alias to remove")
	}
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		for _, id := range ids {
			if _, err := d.RemoveAlias(id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return slices.Clone(ids), nil
}

// AliasView is one alias as `wt-model alias list` shows it: its targets in
// file order (an order the file leaves out reads as the position) and its
// window (windows.Alias: the smallest over every target, 0 when any is
// unknown, Limiting the target that sets or withholds it).
type AliasView struct {
	ID       string
	Targets  []config.AliasTarget
	Context  int
	Limiting string
}

// AliasViews is every registry alias, malformed ones included (the listing
// warns about them), in file order.
func AliasViews(cfg *config.Config, ix tiers.Index) []AliasView {
	out := make([]AliasView, 0, len(cfg.Aliases))
	for _, a := range cfg.Aliases {
		v := AliasView{ID: a.ID, Targets: slices.Clone(a.Targets)}
		for i := range v.Targets {
			if v.Targets[i].Order < 1 {
				v.Targets[i].Order = i + 1
			}
		}
		v.Context, v.Limiting = windows.Alias(cfg, a, ix)
		out = append(out, v)
	}
	return out
}
