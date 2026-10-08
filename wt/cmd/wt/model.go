package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/spf13/cobra"
)

// seedEnv describes the machine `wt model init` seeds for: the configured
// agents, the providers they list, and what is on PATH. A seam so tests do
// not depend on what the developer has installed or configured.
var seedEnv = config.DefaultSeedEnv

// syncRoutesAfterWrite runs the one LiteLLM route sync that follows a
// registry write and returns a warning for the user, or "" when the routes
// are in step. A seam: unstubbed, a test that writes the registry would go on
// to probe the developer's providers and rewrite their config.yaml.
var syncRoutesAfterWrite = realSyncRoutesAfterWrite

// realSyncRoutesAfterWrite reloads the config (the registry just changed) and
// reconciles the routes exactly as `wt litellm sync` does. It never fails the
// command that wrote the registry: the write succeeded, and a sync that could
// not run is a warning. No LiteLLM setup at all (no config.yaml) is not even
// that. A redirected registry with no WT_LITELLM_CONFIG is refused by the
// sync (litellm.ErrRegistryRedirected), and the refusal is the warning.
func realSyncRoutesAfterWrite(out, errOut io.Writer) string {
	if _, err := os.Stat(litellm.DefaultPath()); os.IsNotExist(err) {
		// No LiteLLM here: nothing to sync, and no reason to probe providers.
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		return "LiteLLM routes not synced: " + err.Error()
	}
	err = runLitellmSync(out, errOut, cfg, false, false)
	switch {
	case err == nil, errors.Is(err, litellm.ErrMissing):
		return ""
	case errors.Is(err, litellm.ErrRegistryRedirected):
		return err.Error()
	}
	return "LiteLLM routes not synced: " + err.Error()
}

// modelCmd is the `wt model` group: bare, it opens `wt config` on the Models
// tab; list (model_list.go), add, edit and rm (model_write.go) and init, which
// creates the registry and seeds its provider rows, work without a terminal.
func modelCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "model",
		Short: "Manage the model registry (registry.toml)",
		Long: "Manage the models in the registry.\n\n" +
			"With no subcommand, opens `wt config` on its Models tab (needs a terminal).\n" +
			"The subcommands do the same work without one.",
		Args: cobra.NoArgs,
		// A missing terminal is not a usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdinTTY() {
				return errors.New("wt model needs a terminal to open the Models tab; without one use `wt model list`, `wt model add`, `wt model edit` or `wt model rm`")
			}
			return runConfigEditor(cmd.OutOrStdout(), cmd.ErrOrStderr(), a, configeditor.TabModels)
		},
	}
	var initJSON bool
	initC := &cobra.Command{
		Use:   "init",
		Short: "Create registry.toml if it is missing and add the default provider rows",
		Long: "Create the model registry if it does not exist, and add the provider rows wt\n" +
			"needs and the registry lacks:\n\n" +
			"  - ollama, omlx, mtplx: when the command is installed, or a model or a\n" +
			"                         configured agent uses it\n" +
			"  - mlx_lm_server:       when a model or a configured agent uses it\n" +
			"  - openrouter:          when a model or a configured agent uses it; the row\n" +
			"                         holds no key, only auth.secret_ref =\n" +
			"                         \"OPENROUTER_API_KEY\", the environment variable wt\n" +
			"                         reads the key from (edit it in registry.toml if\n" +
			"                         you keep the key elsewhere)\n" +
			"  - each configured agent: a native provider row under the agent's name\n\n" +
			"A provider an agent lists that wt has no default row for is named in the\n" +
			"output and left for you to add to registry.toml.\n\n" +
			"A config.toml that cannot be read is a warning: no row is added for its\n" +
			"agents until it is fixed and the command is run again.\n\n" +
			"A row that exists is never changed, so running it again is safe. When it\n" +
			"changes the registry it then syncs the LiteLLM routes once; a sync that\n" +
			"cannot run is a warning, and the exit status is still 0.",
		Example: "  wt model init\n  wt model init --json",
		Args:    cobra.NoArgs,
		// A registry that cannot be read or written is not a usage mistake.
		SilenceUsage: true,
		// No config gate: this is the command that repairs a missing
		// registry, and it reads config.toml itself, tolerating a broken one.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelInit(cmd.OutOrStdout(), cmd.ErrOrStderr(), initJSON)
		},
	}
	initC.Flags().BoolVar(&initJSON, "json", false, "machine-readable output")
	c.AddCommand(initC, modelListCmd(a), modelAddCmd(a), modelEditCmd(a), modelRmCmd(a))
	return c
}

// modelInitJSON is `wt model init --json`'s one document.
type modelInitJSON struct {
	Registry       string   `json:"registry"`
	Created        bool     `json:"created"`
	Changed        bool     `json:"changed"`
	ProvidersAdded []string `json:"providers_added"`
	// ProvidersUnseeded are providers an agent lists that have no row and no
	// default: wt reports a config error until the user adds them.
	ProvidersUnseeded []string `json:"providers_unseeded"`
	Warnings          []string `json:"warnings"`
}

func runModelInit(out, errOut io.Writer, asJSON bool) error {
	path := config.RegistryPath()
	_, statErr := os.Lstat(path)
	missing := os.IsNotExist(statErr)

	env := seedEnv()
	var added, unseeded []string
	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Assigned afresh on every run: apply may run more than once.
		var err error
		added, unseeded, err = config.SeedRegistryDefaults(d, env)
		return err
	})
	if err != nil {
		if hint := config.RegistryFixHint(err); hint != "" {
			return fmt.Errorf("%w (%s)", err, hint)
		}
		return err
	}
	doc := modelInitJSON{
		Registry: path, Created: missing && changed, Changed: changed,
		ProvidersAdded:    append([]string{}, added...),
		ProvidersUnseeded: append([]string{}, unseeded...),
		Warnings:          []string{},
	}
	if env.ConfigErr != nil {
		doc.Warnings = append(doc.Warnings, "config.toml could not be read, so no provider row was added for its agents (fix it and run `wt model init` again): "+env.ConfigErr.Error())
	}
	// The sync's own lines follow the registry lines in text mode and are
	// left out of the JSON document; its probe warnings go to stderr either way.
	var syncOut bytes.Buffer
	if changed {
		if w := syncRoutesAfterWrite(&syncOut, errOut); w != "" {
			doc.Warnings = append(doc.Warnings, w)
		}
	}
	if asJSON {
		return json.NewEncoder(out).Encode(doc)
	}
	if doc.Created {
		fmt.Fprintf(out, "registry: %s (created)\n", path)
	} else {
		fmt.Fprintf(out, "registry: %s\n", path)
	}
	for _, id := range added {
		fmt.Fprintf(out, "added provider: %s\n", id)
	}
	if len(added) == 0 {
		fmt.Fprintln(out, "nothing to add")
	}
	for _, id := range unseeded {
		fmt.Fprintf(out, "no default row for provider: %s (an agent lists it; add it to registry.toml by hand)\n", id)
	}
	_, _ = io.Copy(out, &syncOut)
	for _, w := range doc.Warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	return nil
}
