package modeladmin

import (
	"errors"
	"fmt"
	"slices"
	"strings"

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
	if cur, ok := cfg.AliasByID(r.ID); ok && cur.RedirectTo != "" {
		return fmt.Errorf("alias %q is a redirect to %q (#687); set %s's targets instead", r.ID, cur.RedirectTo, cur.RedirectTo)
	}
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
	// A redirect goes before the alias it points at, so one write may remove
	// both (RegistryDoc.RemoveAlias refuses a redirect target while a
	// redirect still points at it); the ids returned keep the order given.
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		ordered := slices.Clone(ids)
		slices.SortStableFunc(ordered, func(a, b string) int {
			return cmpBool(d.AliasRedirectTo(a) != "", d.AliasRedirectTo(b) != "")
		})
		for _, id := range ordered {
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

// AliasView is one alias as `wt-model alias list` shows it: its effective
// targets in file order (Config.AliasTargets: a redirect shows its target
// alias's; an order the file leaves out reads as the position), the alias it
// redirects to (#687, "" for none) and its window (windows.Alias: the smallest over every target, 0 when any is
// unknown, Limiting the target that sets or withholds it).
type AliasView struct {
	ID         string
	RedirectTo string
	Targets    []config.AliasTarget
	Context    int
	Limiting   string
}

// AliasViews is every registry alias, malformed ones included (the listing
// warns about them), in file order.
func AliasViews(cfg *config.Config, ix tiers.Index) []AliasView {
	out := make([]AliasView, 0, len(cfg.Aliases))
	for _, a := range cfg.Aliases {
		v := AliasView{ID: a.ID, RedirectTo: a.RedirectTo, Targets: slices.Clone(cfg.AliasTargets(a))}
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

// cmpBool orders true before false.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

// RenameRequest is one `alias rename`: the alias Old becomes New, and Old
// stays as a redirect to New (#687).
type RenameRequest struct {
	Old, New string
}

// CheckRename refuses a rename the writer would refuse, before anything is
// asked or written: Old must be an ordinary, well-formed alias (not a
// redirect, which has nothing of its own to move; rename the alias it
// redirects to), nothing may redirect to Old (a chain would be two hops), New
// must be a new, valid alias id, and the registry's alias rules must hold
// over the result. It returns the config as it would be after the rename and
// the new alias, for the window and the dry run's rows.
func CheckRename(cfg *config.Config, r RenameRequest) (*config.Config, config.Alias, error) {
	old, ok := cfg.AliasByID(r.Old)
	switch {
	case !ok:
		return nil, config.Alias{}, fmt.Errorf("%w: %q (`wt-model alias list` shows every alias)", config.ErrAliasNotFound, r.Old)
	case old.Malformed != "":
		return nil, config.Alias{}, fmt.Errorf("alias %q is malformed (%s); repair it first", r.Old, old.Malformed)
	case old.RedirectTo != "":
		return nil, config.Alias{}, fmt.Errorf("alias %q is already a redirect to %q; rename %s instead, or `wt-model alias rm %s` once its sessions end", r.Old, old.RedirectTo, old.RedirectTo, r.Old)
	case r.New == r.Old:
		return nil, config.Alias{}, fmt.Errorf("alias %q: the new id is the same as the old one", r.Old)
	}
	if _, exists := cfg.AliasByID(r.New); exists {
		return nil, config.Alias{}, fmt.Errorf("%w: %q; pick another id or remove it first", config.ErrAliasExists, r.New)
	}
	if by := cfg.AliasRedirectsTo(r.Old); len(by) > 0 {
		return nil, config.Alias{}, fmt.Errorf("alias %q is the redirect target of %s; run `wt-model alias rm --finished` once their sessions end, then rename", r.Old, strings.Join(by, ", "))
	}
	req := AliasRequest{ID: r.New, Targets: slices.Clone(cfg.AliasTargets(old))}
	if err := CheckAlias(cfg, req); err != nil {
		return nil, config.Alias{}, err
	}
	after, nw := ConfigWithAlias(cfg, req)
	for i := range after.Aliases {
		if after.Aliases[i].ID == r.Old {
			after.Aliases[i] = config.Alias{ID: r.Old, RedirectTo: r.New}
		}
	}
	return after, nw, nil
}

// RenameAlias writes the rename in one locked registry write: alias New is
// created with Old's current targets and Old becomes a redirect to it, so
// both ids keep rows from the same targets and nothing has a gap. The apply
// is pure (config.UpdateRegistry may run it up to three times) and checks the
// request again against the document it is handed.
func RenameAlias(r RenameRequest) error {
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		if _, _, err := CheckRename(d.RefConfig(), r); err != nil {
			return err
		}
		old, _ := d.RefConfig().AliasByID(r.Old)
		row := tomlw.NewTable()
		row.Set("id", r.New)
		row.Set("targets", []any{})
		if err := d.AddAlias(row); err != nil {
			return err
		}
		req := AliasRequest{ID: r.New, Targets: old.Targets}
		// The reader's positions are written out, so New holds exactly the
		// order Old had even if a later edit reorders the file.
		if err := d.SetAliasTargets(r.New, req.targetRows()); err != nil {
			return err
		}
		return d.RedirectAlias(r.Old, r.New)
	})
	return err
}

// FinishedRedirects is the ids of the redirect aliases in cfg that no client
// session uses: sessions is the running server's per-alias count (a missing
// id counts 0). Redirects still in use are returned in inUse with their
// counts, so a sweep can say why it kept them.
func FinishedRedirects(cfg *config.Config, sessions map[string]int) (finished []string, inUse map[string]int) {
	inUse = map[string]int{}
	for _, a := range cfg.Aliases {
		if a.RedirectTo == "" || slices.Contains(finished, a.ID) {
			continue
		}
		if n := sessions[a.ID]; n > 0 {
			inUse[a.ID] = n
			continue
		}
		finished = append(finished, a.ID)
	}
	return finished, inUse
}
