// wt-model alias list / set / rm — the registry's [[aliases]] (#596): a
// gateway id (alias/<name>) that names one model or several in priority
// order. set and rm do one locked registry write (internal/modeladmin) and
// then the one route sync every registry write runs, which writes the
// alias's LiteLLM database rows. No output carries a row's litellm_params: a
// cloud target's row holds its resolved key (decision 13).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/ohanaverse/local-ai-setup/wt/internal/windows"
	"github.com/spf13/cobra"
)

// aliasListTimeout bounds `alias list`'s one read of LiteLLM's database (the
// ROUTED column): a proxy that does not answer makes the column `?`.
const aliasListTimeout = 10 * time.Second

// aliasCmd is `wt-model alias`. rec is where a running model server keeps its
// runtime record: the client sessions list and rm read are found through it.
func aliasCmd(h *host, rec servestate.Store) *cobra.Command {
	c := &cobra.Command{
		Use:   "alias",
		Short: "List, set and remove registry aliases (one gateway id for one model or a priority group)",
		Long: "An alias is a registry id alias/<name> that a slot profile, pi or codex can name\n" +
			"like a model: it routes to one model, or to several in priority order (a group;\n" +
			"LiteLLM tries a lower order first; targets with equal order share the requests).\n" +
			"Its routes live in LiteLLM's database and follow its targets: a cloud target always, a local\n" +
			"one while it runs. `wt-model alias set` re-points an alias with no gap and no\n" +
			"restart.",
	}
	c.AddCommand(aliasListCmd(h, rec), aliasSetCmd(h, rec), aliasRmCmd(h, rec))
	return c
}

func aliasListCmd(h *host, rec servestate.Store) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the registry's aliases with their targets, window, client sessions and routes",
		Long: "List every alias in the registry, one row each:\n\n" +
			"  ALIAS     the alias id\n" +
			"  TARGETS   order:model for each target, lowest order first tried\n" +
			"  CONTEXT   the window the alias declares: the smallest of its targets'\n" +
			"            windows, - when any target's window is unknown\n" +
			"  SESSIONS  client sessions of the running model server using it, - when no\n" +
			"            server is running (read through serve's runtime record)\n" +
			"  ROUTED    yes when LiteLLM's database has a row for it, no when it has none,\n" +
			"            ? when the proxy pair is not set or the database cannot be read\n\n" +
			"It needs neither a running model server nor the proxy. An alias the registry\n" +
			"cannot use (malformed, or a target that names no model) is listed, and a\n" +
			"warning says why. No output carries a route's parameters.\n\n" +
			"Flags:\n" +
			"  --json   print one JSON document (docs/contracts/alias-list.sample.json)\n\n" +
			"Exit codes:\n" +
			"  0  done\n" +
			"  1  the registry could not be read",
		Example:      "  wt-model alias list\n  wt-model alias list --json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return h.runAliasList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), rec, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print one JSON document (docs/contracts/alias-list.sample.json)")
	return c
}

// aliasListJSON is `wt-model alias list --json`'s one document (J1, J4).
// Lists are always present (J6); a scalar that exists but is unknown is null:
// context when a target's window is unknown, sessions with no server record,
// routed when the database was not read.
type aliasListJSON struct {
	Version  int            `json:"version"`
	Aliases  []aliasListRow `json:"aliases"`
	Warnings []string       `json:"warnings"`
	Error    *jsonErrorBody `json:"error,omitempty"`
}

type aliasListRow struct {
	ID       string            `json:"id"`
	Targets  []aliasListTarget `json:"targets"`
	Context  *int              `json:"context"`
	Sessions *int              `json:"sessions"`
	Routed   *bool             `json:"routed"`
}

type aliasListTarget struct {
	Model string `json:"model"`
	Order int    `json:"order"`
}

// jsonErrorBody is J4's error object.
type jsonErrorBody struct {
	Message string `json:"message"`
}

// runAliasList is `wt-model alias list`.
func (h *host) runAliasList(ctx context.Context, out, errOut io.Writer, rec servestate.Store, asJSON bool) error {
	cfg, loadErr := h.hostConfig()
	if loadErr != nil {
		err := config.ConfigError(loadErr)
		if asJSON {
			doc := aliasListJSON{Version: 1, Aliases: []aliasListRow{}, Warnings: []string{}, Error: &jsonErrorBody{Message: err.Error()}}
			if werr := writeIndentedJSON(out, doc); werr != nil {
				return werr
			}
			return exitcode.New(1, err, true)
		}
		return err
	}
	views := modeladmin.AliasViews(cfg, h.tiersIndex())
	var warnings []string
	for _, a := range cfg.Aliases {
		if err := config.AliasFault(cfg, a); err != nil {
			warnings = append(warnings, oneLineText(err.Error())+"; its routes are left as they are")
		}
	}
	sessions := h.clientSessions(ctx, errOut, rec, nil)()
	routed, routeWarn := h.routedAliases(ctx, cfg)
	if routeWarn != "" {
		warnings = append(warnings, routeWarn)
	}
	if asJSON {
		doc := aliasListJSON{Version: 1, Aliases: []aliasListRow{}, Warnings: append([]string{}, warnings...)}
		for _, v := range views {
			row := aliasListRow{ID: v.ID, Targets: []aliasListTarget{}}
			for _, t := range v.Targets {
				row.Targets = append(row.Targets, aliasListTarget{Model: t.Model, Order: t.Order})
			}
			if v.Context > 0 {
				row.Context = &v.Context
			}
			if sessions != nil {
				n := sessions[v.ID]
				row.Sessions = &n
			}
			if routed != nil {
				r := routed[v.ID]
				row.Routed = &r
			}
			doc.Aliases = append(doc.Aliases, row)
		}
		return writeIndentedJSON(out, doc)
	}
	if len(views) == 0 {
		fmt.Fprintln(out, "no aliases: `wt-model alias set alias/<name> --target ID` adds one")
	} else {
		cells := make([][]string, 0, len(views))
		for _, v := range views {
			parts := make([]string, 0, len(v.Targets))
			for _, t := range v.Targets {
				parts = append(parts, strconv.Itoa(t.Order)+":"+t.Model)
			}
			window, sess, route := "-", "-", "?"
			if v.Context > 0 {
				window = strconv.Itoa(v.Context)
			}
			if sessions != nil {
				sess = strconv.Itoa(sessions[v.ID])
			}
			if routed != nil {
				route = "no"
				if routed[v.ID] {
					route = "yes"
				}
			}
			// Registry text: no control sequence may reach the terminal.
			cells = append(cells, []string{cli.VisibleID(v.ID), cli.VisibleID(strings.Join(parts, ",")), window, sess, route})
		}
		cols := []cli.PlainColumn{{Head: "ALIAS"}, {Head: "TARGETS"}, {Head: "CONTEXT"}, {Head: "SESSIONS"}, {Head: "ROUTED"}}
		fmt.Fprintln(out, cli.RenderPlainTable(cols, cells, 0))
	}
	for _, w := range warnings {
		fmt.Fprintf(errOut, "wt-model: warning: %s\n", cli.VisibleID(w))
	}
	return nil
}

// routedAliases is, per alias id, whether LiteLLM's database has a row for
// it, read once through the proxy pair's model API; nil (and a warning when
// the read failed) when it could not be read. It reads ids, targets and
// orders only (AdminClient.RouteList), never a row's params.
func (h *host) routedAliases(ctx context.Context, cfg *config.Config) (map[string]bool, string) {
	admin := h.adminFor(cfg)
	if admin == nil {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(ctx, aliasListTimeout)
	defer cancel()
	listed, err := admin.RouteList(ctx)
	if err != nil {
		return nil, "routes not read from LiteLLM's database (" + oneLineText(err.Error()) + "); ROUTED shows ?"
	}
	out := map[string]bool{}
	for _, r := range listed {
		if r.Kind == "alias" {
			out[r.Name] = true
		}
	}
	return out, ""
}

// writeIndentedJSON writes doc as one document indented two spaces (J1).
func writeIndentedJSON(out io.Writer, doc any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// oneLineText is s with every run of whitespace one space.
func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

func aliasSetCmd(h *host, rec servestate.Store) *cobra.Command {
	var specs []string
	var acceptSmaller, dryRun bool
	c := &cobra.Command{
		Use:   "set <alias/name> --target ID[:ORDER]...",
		Short: "Create an alias or replace its whole target list",
		Long: "Create the alias, or replace its whole target list with the --target flags\n" +
			"given (repeat the flag, one target each). ID is a registry model id\n" +
			"(`wt-model model list`); :ORDER is its LiteLLM order, 1 first tried (left\n" +
			"out: its position among the flags). A model id that itself ends in :<digits>\n" +
			"is read whole. An alias targets no alias and no native model, and has at most\n" +
			"one local target.\n\n" +
			"The window the alias declares is the smallest of its targets' windows, and\n" +
			"none when any target's is unknown. A set that makes it smaller, or unknown\n" +
			"where it was known, asks first (client sessions already running keep the\n" +
			"window they launched with); --accept-smaller-window answers yes.\n\n" +
			"The LiteLLM routes are synced once afterwards: the alias's database rows are\n" +
			"added before the old ones are deleted, so a re-point has no gap. When the\n" +
			"database cannot be reached the registry is still written, a warning says so,\n" +
			"and `wt-model litellm sync` adds the rows later.\n\n" +
			"Flags:\n" +
			"  --target ID[:ORDER]       one target; repeat for a group\n" +
			"  --accept-smaller-window   set it even when the window gets smaller or unknown\n" +
			"  --dry-run                 say what would happen and change nothing\n\n" +
			"Exit codes:\n" +
			"  0  done (also when the routes could not be written; a warning says so)\n" +
			"  1  a plain error: a bad target, a refused alias, a registry write that failed\n" +
			"  3  approval missing: the window gets smaller and there is no terminal to ask\n" +
			"     (or the answer was no); nothing was changed",
		Example: "  wt-model alias set alias/main --target openrouter/x --target ollama/y:2 --dry-run\n" +
			"  wt-model alias set alias/main --target openrouter/x --target ollama/y:2\n" +
			"  wt-model alias set alias/main --target openrouter/z --accept-smaller-window",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(specs) == 0 {
				cmd.SilenceUsage = false
				return errors.New("nothing to set: pass at least one --target ID[:ORDER]")
			}
			return h.runAliasSet(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), rec, args[0], specs, acceptSmaller, dryRun)
		},
	}
	c.Flags().StringArrayVar(&specs, "target", nil, "one target, a registry model id with an optional :ORDER; repeat for a group")
	c.Flags().BoolVar(&acceptSmaller, "accept-smaller-window", false, "set it even when the alias's window gets smaller or unknown")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "say what would happen and change nothing")
	return c
}

// orderSuffixRE is an order written after a target's last colon.
var orderSuffixRE = regexp.MustCompile(`^-?[0-9]+$`)

// parseAliasTargets reads the --target values: ID or ID:ORDER. A value that
// names a registry model whole is that model (an ollama tag holds a colon:
// ollama/qwen3:8b), else an all-digit suffix after the last colon is the
// order, which must be from 1. A target given twice is refused.
func parseAliasTargets(cfg *config.Config, specs []string) ([]config.AliasTarget, error) {
	var out []config.AliasTarget
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			return nil, errors.New("--target is empty: pass a registry model id")
		}
		t := config.AliasTarget{Model: spec}
		if config.IndexModelByID(cfg.Models, spec) < 0 {
			if i := strings.LastIndex(spec, ":"); i > 0 && orderSuffixRE.MatchString(spec[i+1:]) {
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

// windowText is a window for a line: its tokens, or - when unknown.
func windowText(w int) string {
	if w <= 0 {
		return "-"
	}
	return strconv.Itoa(w)
}

// runAliasSet is `wt-model alias set`.
func (h *host) runAliasSet(ctx context.Context, out, errOut io.Writer, rec servestate.Store, id string, specs []string, acceptSmaller, dryRun bool) error {
	cfg, loadErr := h.hostConfig()
	if loadErr != nil {
		return config.ConfigError(loadErr)
	}
	targets, err := parseAliasTargets(cfg, specs)
	if err != nil {
		return err
	}
	req := modeladmin.AliasRequest{ID: strings.TrimSpace(id), Targets: targets}
	if err := modeladmin.CheckAlias(cfg, req); err != nil {
		return err
	}
	// The shrink guard (decision 7) reads the same cfg the check did; the
	// write checks the request again against the file under its lock.
	ix := h.tiersIndex()
	after, alias := modeladmin.ConfigWithAlias(cfg, req)
	newW, _ := windows.Alias(after, alias, ix)
	oldW, had := 0, false
	if old, ok := cfg.AliasByID(req.ID); ok {
		had = true
		oldW, _ = windows.Alias(cfg, old, ix)
	}
	shrinks := had && oldW > 0 && (newW <= 0 || newW < oldW)
	if dryRun {
		return h.printAliasDryRun(out, after, alias, oldW, newW, had, shrinks)
	}
	if shrinks && !acceptSmaller {
		sentence := fmt.Sprintf("%s's context window would go from %s to %s tokens", req.ID, windowText(oldW), windowText(newW))
		if sessions := h.clientSessions(ctx, errOut, rec, nil)(); sessions != nil {
			sentence += fmt.Sprintf("; %d client session(s) use it and keep the window they launched with", sessions[req.ID])
		}
		ok, err := h.confirmRemove(sentence + ". Set it anyway?")
		if errors.Is(err, cli.ErrNoTerminal) {
			fmt.Fprintf(errOut, "wt-model: note: %s\n", sentence)
		}
		if err != nil {
			return cli.NoTerminalRefusal(err, "--accept-smaller-window", "nothing was changed")
		}
		if !ok {
			return cli.Declined("nothing was changed")
		}
	}
	changed, err := modeladmin.SetAlias(req)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(out, "no change: %s\n", req.ID)
		return nil
	}
	fmt.Fprintf(out, "set alias: %s\n", req.ID)
	return h.syncAndWarn(out, errOut, nil)
}

// printAliasDryRun is `alias set --dry-run`: the targets, the window change
// and each database row the write's sync would plan for the alias, as id,
// target, order and h8 — never its params (a cloud target's row holds its
// resolved key). A local target is live only while it runs (the inventory
// probe says), so a stopped one plans no row and says so.
func (h *host) printAliasDryRun(out io.Writer, cfg *config.Config, a config.Alias, oldW, newW int, had, shrinks bool) error {
	fmt.Fprintf(out, "would set alias: %s\n", a.ID)
	for _, t := range a.Targets {
		fmt.Fprintf(out, "  target %s order %d\n", t.Model, t.Order)
	}
	old := "-"
	if had {
		old = windowText(oldW)
	}
	line := fmt.Sprintf("context: %s -> %s", old, windowText(newW))
	if shrinks {
		line += " (smaller: the real run asks, or needs --accept-smaller-window)"
	}
	fmt.Fprintln(out, line)
	admin := h.adminFor(cfg)
	if admin == nil {
		// The real run writes no row without the pair (decision 4), and an
		// h8 under no key would match no row the proxy ever holds.
		fmt.Fprintln(out, "no proxy pair: no rows would be written")
		fmt.Fprintln(out, "dry run: nothing was changed")
		return nil
	}
	local, _, _ := litellm.SyncInputs(cfg, h.probeInventory)
	live := func(id string) bool {
		return slices.ContainsFunc(local, func(m config.Model) bool { return m.ID == id })
	}
	rows, skips := litellm.DBModelsFromAlias(cfg, a, live, admin.Key)
	name := strings.TrimPrefix(a.ID, config.AliasIDPrefix)
	for _, m := range rows {
		target, _ := m.Info[litellm.AliasTargetKey].(string)
		h8, _, _ := strings.Cut(strings.TrimPrefix(m.ID, litellm.DBAliasIDPrefix+name+"/"), "/")
		fmt.Fprintf(out, "  row %s target %s order %v h8 %s\n", m.ID, target, m.Params["order"], h8)
	}
	for _, s := range skips {
		fmt.Fprintf(out, "  no row for %s: %s\n", s.Target, oneLineText(s.Reason))
	}
	fmt.Fprintln(out, "dry run: nothing was changed")
	return nil
}

func aliasRmCmd(h *host, rec servestate.Store) *cobra.Command {
	var yes, forceInUse bool
	c := &cobra.Command{
		Use:   "rm <alias/name>...",
		Short: "Remove aliases from the registry and their LiteLLM routes",
		Long: "Remove one or more aliases from the registry; the route sync that follows\n" +
			"deletes their LiteLLM database rows. The models they target are not touched.\n" +
			"When LiteLLM's database cannot be reached (no proxy pair, store_model_in_db\n" +
			"off, the proxy down), the registry is still written and one warning says\n" +
			"`alias routes unavailable (<reason>); registry written, LiteLLM not updated;\n" +
			"run 'wt-model litellm sync' when the proxy answers`: the alias's rows keep\n" +
			"routing until that sync deletes them.\n\n" +
			"It asks for confirmation on the terminal; --yes skips it. An alias that client\n" +
			"sessions of the running model server use is removed only with --force-in-use\n" +
			"as well: those sessions lose their route. When one id is not an alias,\n" +
			"nothing is removed.\n\n" +
			"Flags:\n" +
			"  --yes            do not ask for confirmation\n" +
			"  --force-in-use   also remove an alias client sessions use\n\n" +
			"Exit codes:\n" +
			"  0  removed\n" +
			"  1  a plain error: an id that is not an alias, a registry write that failed\n" +
			"  3  approval missing: no --yes and no terminal, the answer was no, or an\n" +
			"     alias in use without --force-in-use; nothing was removed",
		Example:      "  wt-model alias rm alias/old\n  wt-model alias rm alias/old --yes",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return h.runAliasRm(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), rec, args, yes, forceInUse)
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	c.Flags().BoolVar(&forceInUse, "force-in-use", false, "also remove an alias that client sessions of the running model server use")
	return c
}

// runAliasRm is `wt-model alias rm`.
func (h *host) runAliasRm(ctx context.Context, out, errOut io.Writer, rec servestate.Store, ids []string, yes, forceInUse bool) error {
	ids = uniqueInOrder(ids)
	cfg, loadErr := h.hostConfig()
	if loadErr == nil {
		// Refused before the question, as model rm does; without a loaded
		// config the write is what finds out.
		for _, id := range ids {
			if _, ok := cfg.AliasByID(id); !ok {
				return fmt.Errorf("%w: %q (`wt-model alias list` shows every alias)", config.ErrAliasNotFound, id)
			}
		}
	}
	if !forceInUse {
		var unread error
		sessions := h.clientSessions(ctx, errOut, rec, &unread)()
		if unread != nil {
			// A running server whose sessions are unknown: any alias may
			// be in use, so the in-use approval is needed.
			return exitcode.New(cli.ExitApprovalMissing,
				errors.New("approval missing: pass --force-in-use (the running model server's client sessions could not be read, so an alias may be in use); nothing was removed"), false)
		}
		for _, id := range ids {
			if n := sessions[id]; n > 0 {
				return exitcode.New(cli.ExitApprovalMissing,
					fmt.Errorf("approval missing: pass --force-in-use (%s is used by %d client session(s)); nothing was removed", id, n), false)
			}
		}
	}
	if !yes {
		ok, err := h.confirmRemove(fmt.Sprintf("Remove %d alias(es) from the registry (%s)? Their LiteLLM routes go with them.", len(ids), strings.Join(ids, ", ")))
		if err != nil {
			return cli.NoTerminalRefusal(err, "--yes", "nothing was removed")
		}
		if !ok {
			return cli.Declined("nothing was removed")
		}
	}
	removed, err := modeladmin.RemoveAliases(ids)
	if err != nil {
		return err
	}
	for _, id := range removed {
		fmt.Fprintf(out, "removed alias: %s\n", id)
	}
	// The sync that deletes the removed aliases' rows; when its database
	// half cannot run it says so with decision 4's line and the repair.
	return litellm.SyncAndWarn(out, errOut, nil, h.syncRoutesAfterAliasRemoval)
}
