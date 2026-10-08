// wt model add / edit / rm — the commands that change the registry's model
// rows. Each does one locked registry write (internal/modeladmin) and then
// one LiteLLM route sync.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/spf13/cobra"
)

// Test seams: production asks ollama and the user's terminal; cmd/wt's
// TestMain stubs both.
var (
	// ollamaCaps is the best-effort capability lookup an add of an ollama
	// model runs before it writes.
	ollamaCaps = modeladmin.OllamaCapabilities
	// confirmRemove asks before `wt model rm` removes rows: the y/N prompt
	// `wt stop` asks with, on the controlling terminal, so piped input can
	// never authorise a removal.
	confirmRemove = promptStop
)

// modelFieldFlags registers the flags that carry a model's editable fields
// and returns a function that reads the ones the user actually passed: a
// flag left out is a nil field, which an edit leaves alone.
func modelFieldFlags(c *cobra.Command) func() modeladmin.Fields {
	f := c.Flags()
	// Local flags: --family and --tags here set one model's value, and shadow
	// the root's persistent -F/--family and -T/--tags filters.
	f.String(modeladmin.FieldFamily, "", "model family, e.g. qwen3.8 (what -F filters on)")
	f.String(modeladmin.FieldTags, "", "comma-separated tags, e.g. code,design (what -T filters on)")
	f.String(modeladmin.FieldLocation, "", "local or cloud; empty inherits the provider's")
	f.String(modeladmin.FieldInputPrice, "", "input price, $ per million tokens")
	f.String(modeladmin.FieldCachePrice, "", "cached-input price, $ per million tokens")
	f.String(modeladmin.FieldOutputPrice, "", "output price, $ per million tokens")
	f.String(modeladmin.FieldSubscriptionPrice, "", "subscription price, $ per period")
	f.String(modeladmin.FieldSubscriptionPeriod, "", "month or year")
	return func() modeladmin.Fields {
		get := func(name string) *string {
			if !f.Changed(name) {
				return nil
			}
			v, _ := f.GetString(name)
			return &v
		}
		return modeladmin.Fields{
			Family: get(modeladmin.FieldFamily), Tags: get(modeladmin.FieldTags), Location: get(modeladmin.FieldLocation),
			InputPrice: get(modeladmin.FieldInputPrice), CachePrice: get(modeladmin.FieldCachePrice), OutputPrice: get(modeladmin.FieldOutputPrice),
			SubscriptionPrice: get(modeladmin.FieldSubscriptionPrice), SubscriptionPeriod: get(modeladmin.FieldSubscriptionPeriod),
		}
	}
}

func modelAddCmd(a *app) *cobra.Command {
	var id, draft string
	c := &cobra.Command{
		Use:   "add <provider> <name> --family <family>",
		Short: "Add a model to the registry",
		Long: "Add one model to the registry.\n\n" +
			"<name> is the model as its provider lists it: an ollama tag (qwen3:8b), the\n" +
			"name of an omlx model directory, an mtplx org/name, or a cloud provider's\n" +
			"model id. wt downloads nothing: pull or download a local model with its\n" +
			"provider's own tool, before or after adding it (`wt model list` shows what\n" +
			"is on disk and not yet registered as STATUS new).\n\n" +
			"The id is <provider-family>/<name> for a local provider — the id wt already\n" +
			"lists a discovered model under — and <provider>/<name> with each \"/\" in the\n" +
			"name written \"--\" for any other provider. --id overrides it, with an id\n" +
			"of that shape: <provider>/<name>, no spaces. The id, the provider and the\n" +
			"name cannot be changed afterwards.\n\n" +
			"A missing default provider row (ollama, omlx, mtplx, openrouter) is added\n" +
			"in the same write; a provider wt has no default row for must have its\n" +
			"[[providers]] block in registry.toml first. For an ollama model wt runs\n" +
			"`ollama show` once and records whether the model supports tools and\n" +
			"vision; if that fails the model is added without them and a warning says\n" +
			"so.\n\n" +
			"An mlx_lm_server model is a target+draft pairing: <name> is the target and\n" +
			"--draft the draft, each a Hugging Face repo or a local path. wt registers a\n" +
			"pairing and cannot start one; it prints the llmbench command that does. Two\n" +
			"pairings whose target and draft end in the same names need --id for the\n" +
			"second.\n\n" +
			"The LiteLLM routes are synced once afterwards; a sync that cannot run is a\n" +
			"warning, and the exit status is still 0.",
		Example: "  wt model add ollama qwen3:8b --family qwen3 --tags code\n" +
			"  wt model add openrouter qwen/qwen3.8-27b --family qwen3.8 --input-price 0.5 --output-price 2\n" +
			"  wt model add mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit --family qwen3.8",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
	}
	fields := modelFieldFlags(c)
	c.Flags().StringVar(&id, "id", "", "the model's id, instead of the derived one")
	c.Flags().StringVar(&draft, modeladmin.FieldDraft, "", "mlx_lm_server only: the pairing's draft model (a repo or a local path)")
	_ = c.MarkFlagRequired(modeladmin.FieldFamily)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		req := modeladmin.AddRequest{ProviderID: args[0], ModelName: args[1], ID: id, Draft: draft, Fields: fields()}
		return runModelAdd(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, req)
	}
	return c
}

// runModelAdd implements `wt model add`. cfg is the config as loaded (an
// empty one when there is no registry yet; a test may pass nil); it is read
// only for the ollama address.
func runModelAdd(out, errOut io.Writer, cfg *config.Config, req modeladmin.AddRequest) error {
	// What can be refused without the registry is refused first: a mistyped
	// price must not wait on `ollama show`, up to its timeout when the
	// daemon is down.
	if err := modeladmin.CheckAdd(req); err != nil {
		return err
	}
	var warnings []string
	if modeladmin.UsesOllama(req.ProviderID) {
		// Before the write: the registry's apply function must not run a
		// command.
		info, err := ollamaCaps(cfg, req.ModelName)
		if err != nil {
			warnings = append(warnings, "added without model_info.supports_function_calling / supports_vision: "+err.Error())
		}
		req.ModelInfo = info
	}
	res, err := modeladmin.Add(req, seedEnv())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "added model: %s\n", res.ID)
	if req.Draft != "" {
		// wt registers a pairing and has no engine to start one.
		fmt.Fprintf(out, "start it with: %s\n", catalog.PairingStartCommand(req.ModelName, req.Draft))
	}
	for _, p := range res.ProvidersAdded {
		fmt.Fprintf(out, "added provider: %s\n", p)
	}
	for _, p := range res.ProvidersUnseeded {
		fmt.Fprintf(out, "no default row for provider: %s (an agent lists it; add it to registry.toml by hand)\n", p)
	}
	warnings = append(warnings, res.Warnings...)
	return syncAndWarn(out, errOut, warnings)
}

// syncAndWarn runs the one route sync that follows a registry write, prints
// its lines, and prints every warning on stderr. It never fails: the write
// succeeded, so the command exits 0.
func syncAndWarn(out, errOut io.Writer, warnings []string) error {
	var syncOut bytes.Buffer
	if w := syncRoutesAfterWrite(&syncOut, errOut); w != "" {
		warnings = append(warnings, w)
	}
	_, _ = io.Copy(out, &syncOut)
	for _, w := range warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	return nil
}

func modelEditCmd(_ *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "edit <id> [flags]",
		Short: "Change a registry model's family, tags, location or prices",
		Long: "Change the named fields of one registry model. A flag that is not passed\n" +
			"leaves its field alone; an empty value clears it (--tags \"\", --input-price\n" +
			"\"\"), and an empty --location makes the model inherit its provider's.\n" +
			"Every other key of the row is kept as it is.\n\n" +
			"The id, the provider and the model name cannot be edited: usage history,\n" +
			"rotation and launch profiles key on the id. Remove the model and add it\n" +
			"again to change one.\n\n" +
			"An id that more than one registry row carries is refused: wt cannot tell\n" +
			"which row is meant, and the message names the file to repair.\n\n" +
			"The LiteLLM routes are synced once when something changed.",
		Example:      "  wt model edit ollama/qwen3:8b --tags code,design\n  wt model edit openrouter/qwen--qwen3.8-27b --output-price 2.5",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
	}
	fields := modelFieldFlags(c)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		f := fields()
		if f.Empty() {
			// A usage mistake, unlike every other failure here: show the flags.
			cmd.SilenceUsage = false
			return errors.New("nothing to change: pass at least one of --family, --tags, --location or a price flag")
		}
		return runModelEdit(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], f)
	}
	return c
}

func runModelEdit(out, errOut io.Writer, id string, f modeladmin.Fields) error {
	changed, err := modeladmin.Edit(id, f)
	if err != nil {
		return unknownModelHint(err)
	}
	if !changed {
		fmt.Fprintf(out, "no change: %s\n", id)
		return nil
	}
	fmt.Fprintf(out, "updated model: %s\n", id)
	return syncAndWarn(out, errOut, nil)
}

// unknownModelHint says where the ids are when one was not found. Any other
// refusal is returned as it is: an id the registry holds more than once
// (config.ErrModelAmbiguous) already names the file to repair.
func unknownModelHint(err error) error {
	if errors.Is(err, config.ErrModelNotFound) {
		return fmt.Errorf("%w in the registry (`wt model list` shows every id)", err)
	}
	return err
}

func modelRmCmd(a *app) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "rm <id>...",
		Short: "Remove models from the registry (never their weights)",
		Long: "Remove one or more models from the registry. Nothing else is touched: wt\n" +
			"never deletes weights, and it prints where each removed model's are so you\n" +
			"can delete them yourself. A local model that is still on disk shows up\n" +
			"again in `wt model list` as STATUS new.\n\n" +
			"It asks for confirmation on the terminal; --yes skips it. When one id is\n" +
			"not in the registry, or is the id of more than one row, nothing is\n" +
			"removed.\n\n" +
			"The LiteLLM routes are synced once afterwards.",
		Example:      "  wt model rm ollama/qwen3:8b\n  wt model rm openrouter/a--b openrouter/c--d --yes",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := a.cfg
			if a.loadErr != nil {
				cfg = nil // the removal may be the repair; only the weights lines are lost
			}
			return runModelRm(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, args, yes)
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return c
}

// runModelRm implements `wt model rm`. cfg is nil when the config did not
// load; the rows are then removed without the lines that say where their
// weights are.
func runModelRm(out, errOut io.Writer, cfg *config.Config, ids []string, yes bool) error {
	ids = uniqueInOrder(ids)
	// Where the weights are is learned before the write: afterwards the rows
	// are gone, and a local model's path comes from the probe.
	notes := map[string]string{}
	if cfg != nil {
		// An id that is not there is refused before the question, not after
		// the user has answered it — and so is an id more than one row
		// carries (config.ErrModelAmbiguous), which the write refuses too.
		// Without a loaded config the write is what finds out, as it does
		// for a row that went since the load.
		for _, id := range ids {
			if err := cfg.CheckModelRow(id); err != nil {
				return unknownModelHint(err)
			}
		}
		local := slices.ContainsFunc(cfg.Models, func(m config.Model) bool {
			loc, err := cfg.ResolveLocation(m)
			return slices.Contains(ids, m.ID) && err == nil && loc == config.LocationLocal
		})
		if local {
			for _, r := range modeladmin.Rows(cfg, probeInventory(cfg)) {
				if r.Registered && slices.Contains(ids, r.ID) {
					notes[r.ID] = modeladmin.WeightsNote(r)
				}
			}
		}
	}
	if !yes {
		ok, err := confirmRemove(fmt.Sprintf("Remove %d model(s) from the registry (%s)? Weights are not deleted.", len(ids), strings.Join(ids, ", ")))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled — nothing was removed")
		}
	}
	if err := modeladmin.Remove(ids); err != nil {
		return unknownModelHint(err)
	}
	for _, id := range ids {
		fmt.Fprintf(out, "removed model: %s\n", id)
		if note := notes[id]; note != "" {
			fmt.Fprintf(out, "  %s\n", note)
		}
	}
	return syncAndWarn(out, errOut, nil)
}

// uniqueInOrder drops repeated ids, keeping the order they were given in: an
// id named twice would otherwise fail the second removal and undo the first.
func uniqueInOrder(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
