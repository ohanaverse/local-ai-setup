package modeladmin

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
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
	// Guard, when set, is called inside SetAlias's apply with the document's
	// rows (RegistryDoc.RefConfig, which carries each model's provider,
	// model_name and context_window) after the shape checks pass; an error
	// refuses the write and nothing is changed. `wt-model alias set` supplies
	// the shrink re-check (#689): the window decision was made before the
	// lock, so a window another program changed since is caught here. The
	// apply is pure and may run up to three times (config.UpdateRegistry), so
	// Guard must be pure too: no prompt, no write.
	Guard func(cfg *config.Config) error
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
// exactly one model that is neither an alias nor native, no two local targets
// of one Exclusive-tenancy family (#690), an id no model has).
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
		if r.Guard != nil {
			if err := r.Guard(d.RefConfig()); err != nil {
				return err
			}
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

// AliasTargetsEqual reports whether the registry in cfg already holds alias id
// with exactly targets: the same set of (model, effective order) pairs as
// Config.AliasTargets gives, an omitted order (0) being the target's 1-based
// position in its list. The list order among targets that share an order does
// not matter. An alias cfg does not have is never equal.
func AliasTargetsEqual(cfg *config.Config, id string, targets []config.AliasTarget) bool {
	cur, ok := cfg.AliasByID(id)
	if !ok {
		return false
	}
	have := effectiveTargets(cfg.AliasTargets(cur))
	want := effectiveTargets(targets)
	return slices.Equal(have, want)
}

// effectiveTargets is ts with every omitted order filled in with the 1-based
// position, sorted by (order, model) so that the list order among equal
// orders is gone.
func effectiveTargets(ts []config.AliasTarget) []config.AliasTarget {
	out := slices.Clone(ts)
	for i := range out {
		if out[i].Order < 1 {
			out[i].Order = i + 1
		}
	}
	slices.SortFunc(out, func(a, b config.AliasTarget) int {
		if c := cmp.Compare(a.Order, b.Order); c != 0 {
			return c
		}
		return cmp.Compare(a.Model, b.Model)
	})
	return out
}

// SetAliases writes every request in one registry update, all or nothing: each
// request is checked (CheckAlias, then its Guard) against the document with
// the earlier requests of the call already applied, and any failure refuses
// all and changes nothing. A request whose effective targets already equal the
// registry's alias (AliasTargetsEqual) is skipped, so the file keeps its own
// spelling and, when every request is equal, is not written at all. changed
// lists the ids written, in request order. An id given twice is refused. The
// apply is pure (config.UpdateRegistry may run it up to three times), so a
// Guard must be pure too.
func SetAliases(reqs []AliasRequest) (changed []string, err error) {
	seen := map[string]bool{}
	for _, r := range reqs {
		if seen[r.ID] {
			return nil, fmt.Errorf("alias %q is given more than once", r.ID)
		}
		seen[r.ID] = true
	}
	var wrote bool
	var ids []string
	wrote, err = config.UpdateRegistry(func(d *config.RegistryDoc) error {
		ids = nil
		for _, r := range reqs {
			if err := CheckAlias(d.RefConfig(), r); err != nil {
				return err
			}
			if r.Guard != nil {
				if err := r.Guard(d.RefConfig()); err != nil {
					return err
				}
			}
			if AliasTargetsEqual(d.RefConfig(), r.ID, r.Targets) {
				continue
			}
			if !slices.ContainsFunc(d.Aliases(), func(t *tomlw.Table) bool {
				id, _ := t.Get("id")
				return id == r.ID
			}) {
				row := tomlw.NewTable()
				row.Set("id", r.ID)
				row.Set("targets", []any{})
				if err := d.AddAlias(row); err != nil {
					return err
				}
			}
			if err := d.SetAliasTargets(r.ID, r.targetRows()); err != nil {
				return err
			}
			ids = append(ids, r.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !wrote {
		return nil, nil
	}
	return ids, nil
}

// SkippedAlias is an alias AliasesTOML leaves out, and why (one short line).
type SkippedAlias struct{ ID, Why string }

// AliasesTOML is the registry's aliases as `alias apply` reads them: one
// [[aliases]] table each, in the order `alias list` lists them, with an id and
// targets as inline tables holding model and an explicit order (the effective
// one, Config.AliasTargets with an omitted order read as the position), so the
// registry's own spelling never matters. Only aliases apply would accept are
// printed: a redirect, a malformed alias and one config.AliasFault refuses
// are returned in skipped, in the same order, with a short reason each. notes
// are not printed (they are not loaded). With nothing to print text is empty.
func AliasesTOML(cfg *config.Config) (text string, skipped []SkippedAlias, err error) {
	var rows []any
	for _, a := range cfg.Aliases {
		switch {
		case a.RedirectTo != "":
			skipped = append(skipped, SkippedAlias{a.ID, "a redirect to " + a.RedirectTo})
			continue
		case a.Malformed != "":
			skipped = append(skipped, SkippedAlias{a.ID, "malformed: " + oneLine(a.Malformed)})
			continue
		}
		if ferr := config.AliasFault(cfg, a); ferr != nil {
			why := strings.TrimPrefix(oneLine(ferr.Error()), fmt.Sprintf("alias %q: ", a.ID))
			skipped = append(skipped, SkippedAlias{a.ID, why})
			continue
		}
		targets := make([]any, 0, len(a.Targets))
		for i, t := range cfg.AliasTargets(a) {
			order := t.Order
			if order < 1 {
				order = i + 1
			}
			row := tomlw.NewTable()
			row.Set("model", t.Model)
			row.Set("order", int64(order))
			targets = append(targets, row)
		}
		row := tomlw.NewTable()
		row.Set("id", a.ID)
		row.Set("targets", targets)
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return "", skipped, nil
	}
	root := tomlw.NewTable()
	root.Set("aliases", rows)
	b, err := tomlw.Encode(root)
	if err != nil {
		return "", nil, fmt.Errorf("writing the aliases as TOML: %w", err)
	}
	return string(b), skipped, nil
}

// oneLine is s with every run of whitespace one space.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

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

// orderSuffixRE is an order written after a target's last colon.
var orderSuffixRE = regexp.MustCompile(`^-?[0-9]+$`)

// ParseAliasTargets reads target specs, ID or ID:ORDER, as `wt-model alias
// set --target` and the TUI's Aliases tab both do (one parser, so the two
// cannot drift). A value that names a registry model whole is that model (an
// ollama tag holds a colon: ollama/qwen3:8b). Otherwise an all-digit suffix
// after the last colon is the order, which must be from 1, and is split off
// only when what is left is itself a registry model (#691): a half-typed tag
// such as ollama/qwen3:8 is refused whole, naming the value as typed, not
// read as order 8 of a model that does not exist. A target given twice is
// refused.
func ParseAliasTargets(cfg *config.Config, specs []string) ([]config.AliasTarget, error) {
	var out []config.AliasTarget
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			return nil, errors.New("--target is empty: pass a registry model id")
		}
		t := config.AliasTarget{Model: spec}
		if config.IndexModelByID(cfg.Models, spec) < 0 {
			if i := strings.LastIndex(spec, ":"); i > 0 && orderSuffixRE.MatchString(spec[i+1:]) {
				if config.IndexModelByID(cfg.Models, spec[:i]) < 0 {
					return nil, fmt.Errorf("--target %s: not a registry model (an order is written ID:ORDER with a registered ID)", spec)
				}
				n, err := strconv.Atoi(spec[i+1:])
				if err != nil || n < 1 {
					return nil, fmt.Errorf("--target %s: the order must be a whole number from 1", spec)
				}
				t = config.AliasTarget{Model: spec[:i], Order: n}
			}
		}
		if slices.ContainsFunc(out, func(o config.AliasTarget) bool { return o.Model == t.Model }) {
			return nil, fmt.Errorf("--target %s: target %s is given more than once", spec, t.Model)
		}
		out = append(out, t)
	}
	return out, nil
}

// AliasTargetsText is a's targets in the syntax ParseAliasTargetsText reads:
// "model:order, model:order", the order written for every target (a
// position the file leaves out is its 1-based place).
func AliasTargetsText(cfg *config.Config, a config.Alias) string {
	ts := cfg.AliasTargets(a)
	parts := make([]string, 0, len(ts))
	for i, t := range ts {
		order := t.Order
		if order < 1 {
			order = i + 1
		}
		parts = append(parts, t.Model+":"+strconv.Itoa(order))
	}
	return strings.Join(parts, ", ")
}

// ParseAliasTargetsText reads the text AliasTargetsText writes (and any
// comma-separated ID[:ORDER] list) with ParseAliasTargets. Empty pieces
// between commas are ignored; no piece at all is an error.
func ParseAliasTargetsText(cfg *config.Config, text string) ([]config.AliasTarget, error) {
	var specs []string
	for _, p := range strings.Split(text, ",") {
		if p = strings.TrimSpace(p); p != "" {
			specs = append(specs, p)
		}
	}
	if len(specs) == 0 {
		return nil, errors.New("no targets: write model[:order], model[:order]")
	}
	return ParseAliasTargets(cfg, specs)
}

// WindowChange is what an alias set does to the window the alias declares
// (decision 7): Old is its window now (Had says the alias exists), New the
// window after, 0 for unknown. Shrinks is true when the alias exists with a
// known window and the new one is smaller or unknown: the set then asks
// first, because client sessions keep the window they launched with. After
// and Alias are the config with the request in place and the alias as the
// reader reads it, for the dry run's rows.
type WindowChange struct {
	Old, New int
	Had      bool
	Shrinks  bool
	After    *config.Config
	Alias    config.Alias
}

// AliasWindowChange works out the window change of setting req over cfg. It
// is the one home of the shrink rule: `wt-model alias set` and the Aliases tab
// both ask it. cfg is not changed.
func AliasWindowChange(cfg *config.Config, ix tiers.Index, req AliasRequest) WindowChange {
	w := WindowChange{}
	w.After, w.Alias = ConfigWithAlias(cfg, req)
	w.New, _ = windows.Alias(w.After, w.Alias, ix)
	if old, ok := cfg.AliasByID(req.ID); ok {
		w.Had = true
		w.Old, _ = windows.Alias(cfg, old, ix)
	}
	w.Shrinks = w.Had && w.Old > 0 && (w.New <= 0 || w.New < w.Old)
	return w
}

// WindowText is a window for a line: its tokens, or - when unknown.
func WindowText(w int) string {
	if w <= 0 {
		return "-"
	}
	return strconv.Itoa(w)
}

// ShrinkSentence is the sentence that precedes the question a shrinking set
// asks, in the CLI's and the tab's own words. sessions is the number of
// client sessions using the alias, or negative when they could not be read
// (the sentence then says nothing of them).
func (w WindowChange) ShrinkSentence(id string, sessions int) string {
	s := fmt.Sprintf("%s's context window would go from %s to %s tokens", id, WindowText(w.Old), WindowText(w.New))
	if sessions >= 0 {
		s += fmt.Sprintf("; %d client session(s) use it and keep the window they launched with", sessions)
	}
	return s
}
