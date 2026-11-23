// `wt-model litellm` — the route commands moved from `wt litellm` in phase 4
// stage 2 (sync, list, providers, restart), beside the set|status twins of `wt
// litellm`'s proxy pair commands. sync reads the registry through the host
// and writes config.yaml (litellm.RunSync, the one route sync); list and
// providers read only LiteLLM's config.yaml and the provider policy table,
// never the registry; restart reads only the proxy pair. The pair wt-model's
// gateway forwards to LiteLLM with lives in the wt-model config home: `set` is
// its one writer (config.SetProxy) and `status` resolves it home-first against
// the legacy [litellm] fallback (config.ResolveProxy). Command definitions are
// per-binary (each help names its own binary); the documents are shared
// config code (WriteProxyStatus, MaskKey, ProxyFallbackNote), so the twins
// cannot diverge.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/spf13/cobra"
)

// litellmCmd is `wt-model litellm`: sync and list the routes, list the
// mapped providers, restart the proxy, and set and show the LiteLLM proxy
// url and key. h is the host config sync reads the registry through.
func litellmCmd(h *host) *cobra.Command {
	c := &cobra.Command{
		Use:   "litellm",
		Short: "Manage LiteLLM routes for registry models, and the proxy url and key",
	}
	var syncJSON, syncDryRun bool
	syncC := &cobra.Command{
		Use:   "sync",
		Short: "Make LiteLLM routes match the registry's cloud models and the running (or pulled ollama) local models",
		Long: `Make LiteLLM's config.yaml routes match the registry: every configured cloud
model, plus every running local model (an ollama model while it is pulled).
It reads registry.toml (a load error refuses, exit 1, nothing written; a
registry that only fails validation is still synced) and restarts the proxy
only when config.yaml changed. With the proxy pair set ('wt-model litellm
set'), local routes live in LiteLLM's database, written through its model
API with no restart; config.yaml keeps the cloud routes.

Flags:
  --json      print one JSON document (docs/contracts/litellm-cli.sample.json)
  --dry-run   print the route plan and the probe warnings the real sync would
              report; change nothing (does not report litellm_settings fixes).
              With the proxy pair set it lists LiteLLM's database too (no
              write) and previews the move of wt's local routes there

Exit codes:
  0  done
  1  any failure, including a model whose route could not be built`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, loadErr := h.hostConfig()
			if loadErr != nil {
				return config.ConfigError(loadErr)
			}
			return h.runLitellmSync(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, syncJSON, syncDryRun)
		},
	}
	syncC.Flags().BoolVar(&syncJSON, "json", false, "print one JSON document (docs/contracts/litellm-cli.sample.json)")
	syncC.Flags().BoolVar(&syncDryRun, "dry-run", false, "print the route plan and the probe warnings the real sync would report; change nothing (does not report litellm_settings fixes)")
	var statusJSON bool
	statusC := &cobra.Command{
		Use:   "status",
		Short: "Show the LiteLLM proxy url and whether its api key is set (the key is never printed)",
		Long: `Show the LiteLLM proxy pair wt-model forwards to LiteLLM with: the base
URL and whether an api key is set. The key itself is never printed. The pair
is resolved from the wt-model config home (~/.config/wt-model/config.toml and
its key file), with the legacy [litellm] table of config.toml a read-only
fallback when the home holds no [litellm] table; a fallback run prints one
note line on stderr.

Flags:
  --json   print one JSON document (docs/contracts/litellm-cli.sample.json)

Exit codes:
  0  done
  1  any failure`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLitellmStatus(cmd.OutOrStdout(), cmd.ErrOrStderr(), statusJSON)
		},
	}
	statusC.Flags().BoolVar(&statusJSON, "json", false, "print one JSON document (docs/contracts/litellm-cli.sample.json)")
	var setURL, setKey string
	setC := &cobra.Command{
		Use:   "set",
		Short: "Set the LiteLLM proxy url and/or api key",
		Long: `Set the LiteLLM proxy pair wt-model forwards to LiteLLM with. This is the
setup command other messages name:

	'wt-model litellm set --url URL --api-key KEY'

The url is written to ~/.config/wt-model/config.toml and the api key to the
0600 file its key_file names (litellm.key by default); the legacy [litellm]
table of config.toml is never written. A flag left out keeps its current
value; --api-key "" clears the key without deleting the file. A url with
an @ anywhere in it is refused and nothing is written: the url carries no
credentials (write an @ a path needs as %40).

Flags:
  --url URL        set the proxy base URL
  --api-key KEY    set the proxy api key

Exit codes:
  0  done
  1  any failure`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var u, k *string
			if cmd.Flags().Changed("url") {
				u = &setURL
			}
			if cmd.Flags().Changed("api-key") {
				k = &setKey
			}
			if u == nil && k == nil {
				return errors.New("nothing to set: pass --url and/or --api-key")
			}
			return runLitellmSet(cmd.OutOrStdout(), u, k)
		},
	}
	setC.Flags().StringVar(&setURL, "url", "", "LiteLLM proxy base URL")
	setC.Flags().StringVar(&setKey, "api-key", "", "LiteLLM proxy API key")
	var listJSON bool
	listC := &cobra.Command{
		Use:   "list",
		Short: "List routed model ids from config.yaml and LiteLLM's database",
		Long: `Print every route the LiteLLM proxy has, one row per route: the routes in
config.yaml (WT_LITELLM_CONFIG, default ~/.config/litellm/config.yaml) and,
when the proxy pair is set ('wt-model litellm set'), the wt-managed routes
in LiteLLM's database, read through its model API. Columns: ID, STORE (file
for config.yaml, db for the database) and OWNER (wt, or hand-written for a
config.yaml row wt did not write). When the database cannot be listed, one
warning line on stderr says so and the config.yaml rows are still printed.
Reads no registry.

Flags:
  --json   print one JSON document (docs/contracts/litellm-cli.sample.json)

Exit codes:
  0  done
  1  any failure`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The pair is read as restart reads it (home first, the legacy
			// [litellm] table its read-only fallback); no registry.
			pair, perr := config.ResolveProxy()
			return runLitellmList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), listJSON, adminFromPair(pair.URL, pair.APIKey), perr)
		},
	}
	listC.Flags().BoolVar(&listJSON, "json", false, "print one JSON document (docs/contracts/litellm-cli.sample.json)")
	var provJSON bool
	provC := &cobra.Command{
		Use:   "providers",
		Short: "List providers with a LiteLLM mapping",
		Long: `Print every provider id wt has a LiteLLM mapping for and whether it is a
cloud provider. Reads no registry.

Flags:
  --json   print one JSON document (docs/contracts/litellm-cli.sample.json)

Exit codes:
  0  done
  1  any failure`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLitellmProviders(cmd.OutOrStdout(), provJSON)
		},
	}
	provC.Flags().BoolVar(&provJSON, "json", false, "print one JSON document (docs/contracts/litellm-cli.sample.json)")
	restartC := &cobra.Command{
		Use:   "restart",
		Short: "Restart the LiteLLM proxy, letting a request in flight finish, and wait for it to answer",
		Long: `Restart the LiteLLM proxy the way ` + "`wt-model litellm sync`" + ` does after a route
change.

With WT_LITELLM_RESTART_CMD set, that command is run as given. Otherwise
wt-model sends the proxy's process one SIGTERM, waits up to 3 minutes for a
request in flight to finish, and has launchd start it again; ` + "`launchctl\nkickstart -k`" + ` would kill that request after 5 seconds. It then waits up to
30 seconds for the proxy at the pair's url to answer (the wt-model home,
else the legacy [litellm] table; with no url it is not waited for). It reads
no registry. bin/update/litellm runs it after an upgrade.

Exit codes:
  0  restarted
  1  the pair could not be read, the restart failed, or the proxy did not
     answer again`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// No host config and no registry read: the restart needs only the
		// proxy pair, so a broken registry cannot stop the updater's restart.
		RunE: func(cmd *cobra.Command, _ []string) error {
			pair, err := config.ResolveProxy()
			return runLitellmRestart(cmd.OutOrStdout(), pair, err)
		},
	}
	c.AddCommand(statusC, setC, syncC, listC, provC, restartC)
	return c
}

// runLitellmRestart is `wt-model litellm restart`: litellm.RestartAndAwait
// on the pair's url, waiting for the proxy to answer even if it was down
// before. A pair that could not be resolved (pairErr) refuses before
// anything restarts: a restart that skipped its wait as "no url" would hide
// the broken pair. Its notices (a request in flight being waited for, or cut
// off at the bound) go to os.Stderr as they happen (litellm.RestartNotice's
// default); any warning it returns is the command's error, so a script that
// restarts the proxy learns it failed.
func runLitellmRestart(out io.Writer, pair config.ProxyPair, pairErr error) error {
	if pairErr != nil {
		return config.ConfigError(pairErr)
	}
	url := strings.TrimRight(pair.URL, "/")
	if warnings := litellm.RestartAndAwait(url); len(warnings) > 0 {
		return fmt.Errorf("LiteLLM proxy restart: %s", strings.Join(warnings, "; "))
	}
	if url == "" {
		fmt.Fprintln(out, "restarted the LiteLLM proxy (no [litellm] url, so it was not waited for)")
		return nil
	}
	fmt.Fprintf(out, "restarted the LiteLLM proxy at %s\n", config.ShownProxyURL(url))
	return nil
}

// runLitellmSync is `wt-model litellm sync`: the one route sync
// (litellm.RunSync) over cfg, with the host's inventory probe and the model
// API client of the proxy pair (adminFor; nil leaves every route in
// config.yaml).
func (h *host) runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error {
	return litellm.RunSync(out, errOut, cfg, h.probeInventory, h.adminFor(cfg), asJSON, dryRun)
}

// adminFromPair is the LiteLLM model-API client for the proxy pair (url and
// master key, as `wt-model litellm set` stores them), or nil when either is
// empty: then every route stays in config.yaml, as before #598. The key is
// only ever sent as the Authorization header; nothing prints it.
func adminFromPair(url, key string) *litellm.AdminClient {
	return litellm.AdminFromPair(url, key)
}

// runLitellmStatus is `wt-model litellm status`: the shared status body
// (config.ShowProxyStatus) — the pair as every reader resolves it, never the
// key itself, with the fallback note on errOut when the legacy table supplied
// it (ruling 7).
func runLitellmStatus(out, errOut io.Writer, asJSON bool) error {
	return config.ShowProxyStatus(out, errOut, asJSON)
}

// runLitellmSet is `wt-model litellm set`: the one writer of the wt-model
// home (SetProxy), a nil field leaving that field unchanged. No note here:
// after a set the home is authoritative.
func runLitellmSet(out io.Writer, url, key *string) error {
	if err := config.SetProxy(url, key); err != nil {
		return err
	}
	fmt.Fprintln(out, "litellm: updated")
	return nil
}

// runLitellmList is `wt-model litellm list`: every route the proxy has, from
// config.yaml and (#598) the wt-managed rows of LiteLLM's database, with where
// each lives (STORE: file or db) and who owns it, and (#596) one row per
// alias deployment with its TARGET and ORDER. It reads no registry. admin
// is the proxy pair's model-API client (nil: no pair, so only config.yaml is
// read, as before #598); adminErr is why the pair could not be read. A
// database that cannot be listed costs one warning line on errOut and the
// list goes on from the file.
func runLitellmList(ctx context.Context, out, errOut io.Writer, asJSON bool, admin *litellm.AdminClient, adminErr error) error {
	f, err := litellm.Open(litellm.DefaultPath())
	if err != nil {
		return err
	}
	// kind, target and order were added beside the older keys (#596): an
	// alias deployment is one row each, its id the alias id (repeated once per
	// target), and routed names each id once.
	type rowJSON struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Managed bool   `json:"managed"`
		Store   string `json:"store"`
		Target  string `json:"target,omitempty"`
		Order   int    `json:"order,omitempty"`
	}
	var rows []rowJSON
	for _, r := range f.Rows() {
		rows = append(rows, rowJSON{ID: r.ID, Kind: "model", Managed: r.Managed, Store: "file"})
	}
	switch {
	case adminErr != nil:
		fmt.Fprintf(errOut, "wt-model: warning: database routes not listed (the LiteLLM proxy pair could not be read: %v); showing config.yaml only\n", adminErr)
	case admin != nil:
		listed, lerr := admin.RouteList(ctx)
		if lerr != nil {
			fmt.Fprintf(errOut, "wt-model: warning: database routes not listed (%s); showing config.yaml only\n", strings.Join(strings.Fields(lerr.Error()), " "))
		}
		for _, r := range listed {
			rows = append(rows, rowJSON{ID: r.Name, Kind: r.Kind, Managed: true, Store: "db", Target: r.Target, Order: r.Order})
		}
	}
	if asJSON {
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			if !slices.Contains(ids, r.ID) {
				ids = append(ids, r.ID)
			}
		}
		if rows == nil {
			rows = []rowJSON{}
		}
		return json.NewEncoder(out).Encode(map[string]any{"routed": ids, "rows": rows})
	}
	// TARGET and ORDER appear only when an alias row is listed, so a list
	// with no alias reads as it did before #596.
	aliases := slices.ContainsFunc(rows, func(r rowJSON) bool { return r.Kind == "alias" })
	cells := make([][]string, 0, len(rows))
	for _, r := range rows {
		owner := "wt"
		if !r.Managed {
			owner = "hand-written"
		}
		row := []string{r.ID, r.Store, owner}
		if aliases {
			target, order := "-", "-"
			if r.Kind == "alias" {
				target, order = r.Target, strconv.Itoa(r.Order)
			}
			row = append(row, target, order)
		}
		cells = append(cells, row)
	}
	// The id column is never cut (cli.RenderPlainTable), so a reader can copy
	// it into another command; width 0: no terminal limit, one row per line.
	cols := []cli.PlainColumn{{Head: "ID"}, {Head: "STORE"}, {Head: "OWNER"}}
	if aliases {
		cols = append(cols, cli.PlainColumn{Head: "TARGET"}, cli.PlainColumn{Head: "ORDER"})
	}
	fmt.Fprintln(out, cli.RenderPlainTable(cols, cells, 0))
	return nil
}

// runLitellmProviders is `wt-model litellm providers`: every provider with a
// LiteLLM mapping and whether it is a cloud provider. It reads no registry.
func runLitellmProviders(out io.Writer, asJSON bool) error {
	provs := litellm.Providers()
	if asJSON {
		doc := map[string]map[string]map[string]bool{"providers": {}}
		for id, cloud := range provs {
			doc["providers"][id] = map[string]bool{"cloud": cloud}
		}
		return json.NewEncoder(out).Encode(doc)
	}
	ids := make([]string, 0, len(provs))
	for id := range provs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(out, "%s cloud=%v\n", id, provs[id])
	}
	return nil
}
