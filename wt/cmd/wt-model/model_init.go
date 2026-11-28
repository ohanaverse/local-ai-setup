// wt-model model init — create the registry if it is missing and add the
// provider rows wt needs and the registry lacks, then one LiteLLM route sync.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/spf13/cobra"
)

// modelInitCmd is `wt-model model init`, over h's seams.
func modelInitCmd(h *host) *cobra.Command {
	var initJSON bool
	c := &cobra.Command{
		Use:   "init",
		Short: "Create registry.toml if it is missing and add the default provider rows",
		Long: "Create the model registry if it does not exist, and add the provider rows wt\n" +
			"needs and the registry lacks:\n\n" +
			"  - ollama, omlx, mtplx, sushi: when the command is installed, or a model\n" +
			"                         uses it\n" +
			"  - mlx_lm_server, rec:  when a model uses it (an installed `rec` proves\n" +
			"                         nothing: SoX has one too)\n" +
			"  - openrouter:          when a model uses it; the row holds no key, only\n" +
			"                         auth.secret_ref = \"OPENROUTER_API_KEY\", the\n" +
			"                         environment variable wt reads the key from (edit\n" +
			"                         it in registry.toml if you keep the key elsewhere)\n" +
			"  - claude, codex, copilot, agy: a native provider row each, whether or\n" +
			"                         not the agent is installed on this machine (an unused\n" +
			"                         one is offered only to the agent it is named for;\n" +
			"                         delete its [[providers]] block (or its line, when the\n" +
			"                         file lists providers inline) from registry.toml if\n" +
			"                         you do not want it; `model init` adds it back)\n\n" +
			"Seeding reads no config.toml. pi and opencode have no native provider and\n" +
			"get no row. The route sync after a change still loads config.toml, and a\n" +
			"file it cannot read is that sync's warning.\n\n" +
			"A row that exists is never changed, so running it again is safe. When it\n" +
			"changes the registry it then syncs the LiteLLM routes once; a sync that\n" +
			"cannot run is a warning, and the exit status is still 0.",
		Example: "  wt-model model init\n  wt-model model init --json",
		Args:    cobra.NoArgs,
		// A registry that cannot be read or written is not a usage mistake.
		SilenceUsage: true,
		// No config gate: this is the command that repairs a missing
		// registry. Seeding reads no config.toml (the route sync after a write does).
		RunE: func(cmd *cobra.Command, _ []string) error {
			return h.runModelInit(cmd.OutOrStdout(), cmd.ErrOrStderr(), initJSON)
		},
	}
	c.Flags().BoolVar(&initJSON, "json", false, "machine-readable output")
	return c
}

// modelInitJSON is `wt-model model init --json`'s one document.
type modelInitJSON struct {
	Registry       string   `json:"registry"`
	Created        bool     `json:"created"`
	Changed        bool     `json:"changed"`
	ProvidersAdded []string `json:"providers_added"`
	Warnings       []string `json:"warnings"`
}

// runModelInit implements `wt-model model init` over h's seedEnv and route
// sync seams.
func (h *host) runModelInit(out, errOut io.Writer, asJSON bool) error {
	path := config.RegistryPath()
	_, statErr := os.Lstat(path)
	missing := os.IsNotExist(statErr)

	env := h.seedEnv()
	var added []string
	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Assigned afresh on every run: apply may run more than once.
		var err error
		added, err = config.SeedRegistryDefaults(d, env)
		if err != nil {
			return err
		}
		// The fixed native set, after the defaults, in the same write.
		natives, err := config.SeedNativeProviders(d)
		added = append(added, natives...)
		return err
	})
	if err != nil {
		return withRegistryHint(err)
	}
	doc := modelInitJSON{
		Registry: path, Created: missing && changed, Changed: changed,
		ProvidersAdded: append([]string{}, added...),
		Warnings:       []string{},
	}
	// The sync's own lines follow the registry lines in text mode and are
	// left out of the JSON document; its probe warnings go to stderr either way.
	var syncOut bytes.Buffer
	if changed {
		if w := h.syncRoutesAfterWrite(&syncOut, errOut); w != "" {
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
	_, _ = io.Copy(out, &syncOut)
	for _, w := range doc.Warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	return nil
}
