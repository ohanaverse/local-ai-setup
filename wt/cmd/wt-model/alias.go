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
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
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
		Short: "List, set, rename and remove registry aliases (one gateway id for one model or a priority group)",
		Long: "An alias is a registry id alias/<name> that a slot profile, pi or codex can name\n" +
			"like a model: it routes to one model, or to several in priority order (a group;\n" +
			"LiteLLM tries a lower order first; targets with equal order share the requests).\n" +
			"Its routes live in LiteLLM's database and follow its targets: a cloud target always, a local\n" +
			"one while it runs. `wt-model alias set` re-points an alias with no gap and no\n" +
			"restart. `wt-model alias rename` gives an alias a new id and keeps the old id\n" +
			"working as a redirect until its sessions end. `wt-model model` has an Aliases\n" +
			"tab that does the same on a terminal.",
	}
	c.AddCommand(aliasListCmd(h, rec), aliasSetCmd(h, rec), aliasRenameCmd(h), aliasRmCmd(h, rec))
	return c
}

func aliasListCmd(h *host, rec servestate.Store) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the registry's aliases with their targets, window, client sessions and routes",
		Long: "List every alias in the registry, one row each:\n\n" +
			"  ALIAS     the alias id\n" +
			"  TARGETS   order:model for each target, lowest order first tried (a redirect\n" +
			"            shows the targets of the alias it redirects to)\n" +
			"  CONTEXT   the window the alias declares: the smallest of its targets'\n" +
			"            windows, - when any target's window is unknown\n" +
			"  SESSIONS  client sessions of the running model server using it, - when no\n" +
			"            server is running (read through serve's runtime record)\n" +
			"  ROUTED    yes when LiteLLM's database has a row for it, no when it has none,\n" +
			"            ? when the proxy pair is not set or the database cannot be read\n" +
			"  REDIRECT  `-> alias/new` for an alias kept by `alias rename`; the column\n" +
			"            shows only when some alias is one\n\n" +
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
	ID string `json:"id"`
	// RedirectTo is the alias this one redirects to (#687), null for an
	// ordinary alias; Targets are then the effective ones.
	RedirectTo *string           `json:"redirect_to"`
	Targets    []aliasListTarget `json:"targets"`
	Context    *int              `json:"context"`
	Sessions   *int              `json:"sessions"`
	Routed     *bool             `json:"routed"`
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
			if v.RedirectTo != "" {
				row.RedirectTo = &v.RedirectTo
			}
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
		anyRedirect := false
		for _, v := range views {
			anyRedirect = anyRedirect || v.RedirectTo != ""
		}
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
			row := []string{cli.VisibleID(v.ID), cli.VisibleID(strings.Join(parts, ",")), window, sess, route}
			if anyRedirect {
				redirect := "-"
				if v.RedirectTo != "" {
					redirect = "-> " + cli.VisibleID(v.RedirectTo)
				}
				row = append(row, redirect)
			}
			cells = append(cells, row)
		}
		cols := []cli.PlainColumn{{Head: "ALIAS"}, {Head: "TARGETS"}, {Head: "CONTEXT"}, {Head: "SESSIONS"}, {Head: "ROUTED"}}
		if anyRedirect {
			cols = append(cols, cli.PlainColumn{Head: "REDIRECT"})
		}
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
			"is read whole. An alias targets no alias and no native model. It may have\n" +
			"several local targets (a start starts each in turn), but no two of one\n" +
			"exclusive family such as mtplx, which runs one model at a time.\n\n" +
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

// runAliasSet is `wt-model alias set`.
func (h *host) runAliasSet(ctx context.Context, out, errOut io.Writer, rec servestate.Store, id string, specs []string, acceptSmaller, dryRun bool) error {
	cfg, loadErr := h.hostConfig()
	if loadErr != nil {
		return config.ConfigError(loadErr)
	}
	targets, err := modeladmin.ParseAliasTargets(cfg, specs)
	if err != nil {
		return err
	}
	req := modeladmin.AliasRequest{ID: strings.TrimSpace(id), Targets: targets}
	if err := modeladmin.CheckAlias(cfg, req); err != nil {
		return err
	}
	// The shrink guard (decision 7) reads the same cfg the check did; the
	// write checks the request again against the file under its lock. The rule
	// itself is modeladmin.AliasWindowChange's, shared with the Aliases tab.
	wc := modeladmin.AliasWindowChange(cfg, h.tiersIndex(), req)
	after, alias, oldW, newW, had, shrinks := wc.After, wc.Alias, wc.Old, wc.New, wc.Had, wc.Shrinks
	if dryRun {
		return h.printAliasDryRun(out, after, alias, oldW, newW, had, shrinks)
	}
	if shrinks && !acceptSmaller {
		count := -1
		if sessions := h.clientSessions(ctx, errOut, rec, nil)(); sessions != nil {
			count = sessions[req.ID]
		}
		sentence := wc.ShrinkSentence(req.ID, count)
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
	req.Guard = aliasShrinkGuard(h.tiersIndex(), req, acceptSmaller, shrinks, oldW, newW)
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

// aliasShrinkGuard is the shrink check run again inside the registry write
// (#689): the window decision above read the registry before the lock, so a
// concurrent `model edit --context-window` could turn an approved (or
// harmless) set into an unapproved shrink. Under the lock it recomputes the
// windows over the document and refuses a shrink unless it is exactly the
// (old, new) pair that was approved, or --accept-smaller-window was given.
// Refusing, not re-asking, keeps a prompt out of the lock. It is pure (the
// apply may run up to three times) and ix is the index the first check used.
func aliasShrinkGuard(ix tiers.Index, req modeladmin.AliasRequest, acceptSmaller, approvedShrink bool, approvedOld, approvedNew int) func(*config.Config) error {
	return func(c *config.Config) error {
		if acceptSmaller {
			return nil
		}
		wc := modeladmin.AliasWindowChange(c, ix, req)
		if !wc.Shrinks || approvedShrink && wc.Old == approvedOld && wc.New == approvedNew {
			return nil
		}
		approved := "nothing was approved"
		if approvedShrink {
			approved = fmt.Sprintf("you approved %s to %s", modeladmin.WindowText(approvedOld), modeladmin.WindowText(approvedNew))
		}
		return fmt.Errorf("%s's context window would now go from %s to %s tokens (%s); the window changed since the check; nothing was changed, rerun",
			req.ID, modeladmin.WindowText(wc.Old), modeladmin.WindowText(wc.New), approved)
	}
}

// printAliasDryRun is `alias set --dry-run`: the targets, the window change
// and each database row the write's sync would plan for the alias, as id,
// target, order and h8 — never its params (a cloud target's row holds its
// resolved key). A local target is live only while it runs (the inventory
// probe says), so a stopped one plans no row and says so; a Claude
// subscription target is live only while the write's subscription check is
// clean, and is skipped with the check's own reason otherwise.
func (h *host) printAliasDryRun(out io.Writer, cfg *config.Config, a config.Alias, oldW, newW int, had, shrinks bool) error {
	fmt.Fprintf(out, "would set alias: %s\n", a.ID)
	for _, t := range cfg.AliasTargets(a) {
		fmt.Fprintf(out, "  target %s order %d\n", t.Model, t.Order)
	}
	old := "-"
	if had {
		old = modeladmin.WindowText(oldW)
	}
	line := fmt.Sprintf("context: %s -> %s", old, modeladmin.WindowText(newW))
	if shrinks {
		line += " (smaller: the real run asks, or needs --accept-smaller-window)"
	}
	fmt.Fprintln(out, line)
	h.printAliasRows(out, cfg, a)
	fmt.Fprintln(out, "dry run: nothing was changed")
	return nil
}

// printAliasRows is the rows half of a dry run: each database row the
// write's sync would plan for alias a over cfg, or why there are none.
func (h *host) printAliasRows(out io.Writer, cfg *config.Config, a config.Alias) {
	admin := h.adminFor(cfg)
	if admin == nil {
		// The real run writes no row without the pair (decision 4), and an
		// h8 under no key would match no row the proxy ever holds.
		fmt.Fprintln(out, "no proxy pair: no rows would be written")
		return
	}
	// The rows come from the planner the real write uses (PlanSync), so the
	// liveness and the Claude subscription check are the write's own: a local
	// target is live while it runs, a subscription target while its check is
	// clean (#703).
	desired, o, _ := litellm.SyncInputs(cfg, h.probeInventory)
	o.Admin = admin
	rows, skips, err := litellm.PreviewAlias(cfg, desired, o, a)
	if err != nil {
		fmt.Fprintf(out, "cannot preview the rows: %s\n", oneLineText(err.Error()))
		return
	}
	name := strings.TrimPrefix(a.ID, config.AliasIDPrefix)
	for _, m := range rows {
		target, _ := m.Info[litellm.AliasTargetKey].(string)
		h8, _, _ := strings.Cut(strings.TrimPrefix(m.ID, litellm.DBAliasIDPrefix+name+"/"), "/")
		fmt.Fprintf(out, "  row %s target %s order %v h8 %s\n", m.ID, target, m.Params["order"], h8)
	}
	for _, s := range skips {
		fmt.Fprintf(out, "  no row for %s: %s\n", s.Target, oneLineText(s.Reason))
	}
}

func aliasRmCmd(h *host, rec servestate.Store) *cobra.Command {
	var yes, forceInUse, finished bool
	c := &cobra.Command{
		Use:   "rm <alias/name>... | --finished",
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
			"nothing is removed. An alias that a redirect (`alias rename`) points at is\n" +
			"removed only after the redirect is, or in the same command.\n\n" +
			"--finished takes no ids: it removes every redirect that `alias rename` left\n" +
			"and no client session uses any more, and says which it kept. With the running\n" +
			"server's sessions unreadable it refuses (exit 3) unless --force-in-use, which\n" +
			"also removes the redirects in use.\n\n" +
			"Flags:\n" +
			"  --finished       remove every redirect no client session uses\n" +
			"  --yes            do not ask for confirmation\n" +
			"  --force-in-use   also remove an alias client sessions use\n\n" +
			"Exit codes:\n" +
			"  0  removed\n" +
			"  1  a plain error: an id that is not an alias, a registry write that failed\n" +
			"  3  approval missing: no --yes and no terminal, the answer was no, or an\n" +
			"     alias in use without --force-in-use; nothing was removed",
		Example:      "  wt-model alias rm alias/old\n  wt-model alias rm alias/old --yes\n  wt-model alias rm --finished",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case finished && len(args) > 0:
				cmd.SilenceUsage = false
				return errors.New("--finished takes no alias ids: it removes every redirect no client session uses")
			case !finished && len(args) == 0:
				cmd.SilenceUsage = false
				return errors.New("nothing to remove: pass an alias id, or --finished")
			}
			return h.runAliasRm(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), rec, args, yes, forceInUse, finished)
		},
	}
	c.Flags().BoolVar(&finished, "finished", false, "remove every redirect left by `alias rename` that no client session uses")
	c.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	c.Flags().BoolVar(&forceInUse, "force-in-use", false, "also remove an alias that client sessions of the running model server use")
	return c
}

// runAliasRm is `wt-model alias rm`.
func (h *host) runAliasRm(ctx context.Context, out, errOut io.Writer, rec servestate.Store, ids []string, yes, forceInUse, finished bool) error {
	ids = uniqueInOrder(ids)
	cfg, loadErr := h.hostConfig()
	if finished {
		return h.runAliasRmFinished(ctx, out, errOut, rec, cfg, loadErr, yes, forceInUse)
	}
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
	return h.removeAliasesAndSync(out, errOut, ids)
}

// removeAliasesAndSync is the write half of `alias rm`: one registry write,
// a line per alias, then the sync that deletes their rows.
func (h *host) removeAliasesAndSync(out, errOut io.Writer, ids []string) error {
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

// runAliasRmFinished is `wt-model alias rm --finished` (#687): the sweep of
// the redirects `alias rename` left. It reads the running server's sessions
// as `alias rm` does and refuses the same way when they cannot be read
// (exit 3, --force-in-use); a redirect a session still uses is kept and
// named, unless --force-in-use removes it too. The removal, the question
// and the route sync are `alias rm`'s own.
func (h *host) runAliasRmFinished(ctx context.Context, out, errOut io.Writer, rec servestate.Store, cfg *config.Config, loadErr error, yes, forceInUse bool) error {
	if loadErr != nil {
		return config.ConfigError(loadErr)
	}
	var unread error
	sessions := h.clientSessions(ctx, errOut, rec, &unread)()
	if unread != nil && !forceInUse {
		return exitcode.New(cli.ExitApprovalMissing,
			errors.New("approval missing: pass --force-in-use (the running model server's client sessions could not be read, so a redirect may be in use); nothing was removed"), false)
	}
	if forceInUse {
		sessions = nil
	}
	ids, inUse := modeladmin.FinishedRedirects(cfg, sessions)
	for _, a := range cfg.Aliases {
		if n, kept := inUse[a.ID]; kept {
			fmt.Fprintf(out, "kept redirect: %s (used by %d client session(s))\n", cli.VisibleID(a.ID), n)
			delete(inUse, a.ID)
		}
	}
	if len(ids) == 0 {
		fmt.Fprintln(out, "no finished redirects")
		return nil
	}
	if !yes {
		ok, err := h.confirmRemove(fmt.Sprintf("Remove %d finished redirect(s) from the registry (%s)? Their LiteLLM routes go with them.", len(ids), strings.Join(ids, ", ")))
		if err != nil {
			return cli.NoTerminalRefusal(err, "--yes", "nothing was removed")
		}
		if !ok {
			return cli.Declined("nothing was removed")
		}
	}
	return h.removeAliasesAndSync(out, errOut, ids)
}

func aliasRenameCmd(h *host) *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "rename <alias/old> <alias/new>",
		Short: "Give an alias a new id and keep the old id working as a redirect",
		Long: "Create alias/new with alias/old's current targets and turn alias/old into a\n" +
			"redirect to it (`redirect_to` in registry.toml), in one registry write. A\n" +
			"redirect has no targets of its own: it routes to whatever alias/new targets, so\n" +
			"a later `alias set alias/new` moves both ids. Both ids keep LiteLLM rows from\n" +
			"the same targets through the one route sync that follows, so running client\n" +
			"sessions and profiles still naming alias/old keep working with no gap and no\n" +
			"proxy restart. A rename never changes the window.\n\n" +
			"Refused: an old id that is not an alias or is itself a redirect, an old id\n" +
			"another redirect points at (run `alias rm --finished` first), and a new id\n" +
			"that exists.\n\n" +
			"The output ends with the line to run on each machine that launches slot\n" +
			"profiles, `wt profile slots repoint alias/old alias/new`: wt-model cannot\n" +
			"reach another machine's slot-profiles.toml, so repointing is explicit. When no\n" +
			"session uses alias/old any more, `wt-model alias rm --finished` removes it.\n\n" +
			"Flags:\n" +
			"  --dry-run   say what would happen and change nothing\n\n" +
			"Exit codes:\n" +
			"  0  done (also when the routes could not be written; a warning says so)\n" +
			"  1  a plain error: a refused rename, a registry write that failed",
		Example:      "  wt-model alias rename alias/sonnet-main alias/main --dry-run\n  wt-model alias rename alias/sonnet-main alias/main",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return h.runAliasRename(cmd.OutOrStdout(), cmd.ErrOrStderr(), strings.TrimSpace(args[0]), strings.TrimSpace(args[1]), dryRun)
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "say what would happen and change nothing")
	return c
}

// repointLine is the last line of `alias rename`: the command to run on each
// machine that launches slot profiles (wt profile slots repoint).
func repointLine(old, nw string) string {
	return fmt.Sprintf("next, on each machine that launches slot profiles: wt profile slots repoint %s %s", old, nw)
}

// runAliasRename is `wt-model alias rename`. A rename never changes the
// window (the new alias has the old one's targets), so it asks nothing: there
// is no --accept-smaller-window to pass.
func (h *host) runAliasRename(out, errOut io.Writer, old, nw string, dryRun bool) error {
	cfg, loadErr := h.hostConfig()
	if loadErr != nil {
		return config.ConfigError(loadErr)
	}
	req := modeladmin.RenameRequest{Old: old, New: nw}
	after, alias, err := modeladmin.CheckRename(cfg, req)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(out, "would rename alias: %s -> %s\n", old, nw)
		fmt.Fprintf(out, "  %s keeps routing as a redirect to %s\n", old, nw)
		for _, t := range after.AliasTargets(alias) {
			fmt.Fprintf(out, "  target %s order %d\n", t.Model, t.Order)
		}
		h.printAliasRows(out, after, alias)
		fmt.Fprintln(out, "dry run: nothing was changed")
		fmt.Fprintln(out, repointLine(old, nw))
		return nil
	}
	if err := modeladmin.RenameAlias(req); err != nil {
		return err
	}
	fmt.Fprintf(out, "renamed alias: %s -> %s\n", old, nw)
	fmt.Fprintf(out, "%s stays as a redirect to %s until `wt-model alias rm --finished`\n", old, nw)
	err = h.syncAndWarn(out, errOut, nil)
	fmt.Fprintln(out, repointLine(old, nw))
	return err
}
