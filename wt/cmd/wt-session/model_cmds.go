// wt start / wt stop — direct control of local models without launching an
// agent. `wt start` and `wt stop <model>` go through the model server
// (model_cmds_remote.go); a provider name or --all is `wt-model stop`'s (a
// pointer here).
package main

import (
	"strings"

	"github.com/spf13/cobra"
)

func stopCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop [model]",
		Short: "Stop a running local model through the model server",
		Long: "Stop a running local model (<provider>/<name>). With no argument, shows\n" +
			"a numbered picker (requires a TTY) over the model server's running local\n" +
			"models, marked with the client sessions using each.\n\n" +
			"On omlx, stopping a model unloads that model and leaves the service and its other\n" +
			"models up.\n\n" +
			"A model id (and no argument: a numbered picker over the server's running local\n" +
			"models) is stopped through the model server ([model-server] in config.toml, or\n" +
			"WT_MODEL_URL; with none configured wt refuses, exit 1). The server asks before\n" +
			"stopping a model other client sessions use (--yes answers it; with no terminal\n" +
			"and no --yes: exit 3, nothing stopped). Exit 0\n" +
			"stopped (also when the provider is still finishing the stop), 1 could not start\n" +
			"(no terminal for the picker, an unreachable server), 2 the stop failed or was\n" +
			"detached with Ctrl+C (the job keeps running: 'wt job ID'), 3 approval missing.\n\n" +
			"A provider name or --all moved to wt-model: run 'wt-model stop <provider>' or\n" +
			"'wt-model stop --all' on the machine that runs the model server. wt prints that\n" +
			"pointer and exits 1, stopping nothing.",
		Example: "  wt stop ollama/qwen3.8:27b-mlx\n  wt stop\n  wt stop ollama/qwen3.8:27b-mlx --yes",
		Args:    cobra.MaximumNArgs(1),
		// A usage mistake (a bad argument count) shows usage; every other
		// failure does not (RunE below), so the walk in rootCmd leaves this
		// one alone.
		Annotations: map[string]string{usageOnMistakeAnnotation: "stop"},
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			// A provider name or --all is wt-model's now: one pointer line,
			// exit 1, no registry read and no server asked.
			if all, _ := cmd.Flags().GetBool("all"); all {
				cmd.SilenceUsage = true
				return movedError("stop --all", "stop --all")
			}
			if arg != "" && !strings.Contains(arg, "/") {
				cmd.SilenceUsage = true
				return movedError("stop "+arg, "stop "+arg)
			}
			yes, _ := cmd.Flags().GetBool("yes")
			// A model id (or no argument: the server's picker) is stopped
			// through the model server; with none configured that refuses
			// before anything is stopped.
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			ms, err := a.openModels(cmd.Context(), "nothing was stopped")
			if err != nil {
				return remoteExit(err)
			}
			return runStopRemote(cmd.Context(), cmd.OutOrStdout(), ms, arg, yes)
		},
	}
	cmd.Flags().Bool("yes", false, "Answer the model server's question when the model is in use by other client sessions")
	cmd.Flags().Bool("all", false, "Moved: prints a pointer to 'wt-model stop --all' and exits 1")
	return cmd
}

func startCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "start [model]",
		Short: "Start a local model",
		Long: "Start a local model (<provider>/<name>, as with -M). With no argument, shows\n" +
			"the full model picker over every local model that is on disk or running,\n" +
			"registered or detected (requires a TTY).\n\n" +
			"A model that is already running is left running; its LiteLLM route is\n" +
			"written if it is missing. If omlx is still loading the model, wt waits for\n" +
			"the load.\n\n" +
			"On a provider that serves one model (mtplx), starting another replaces it.\n" +
			"On omlx a model loads beside the ones already loaded; when it does not fit,\n" +
			"omlx unloads the least recently used. wt asks before either; --replace skips\n" +
			"the question.\n\n" +
			"When other providers' models are loaded (an mtplx server beside an omlx start,\n" +
			"an ollama model in memory), wt lists them with the memory each holds and asks\n" +
			"whether to stop them first, keep them, or cancel. --others=stop stops every one\n" +
			"wt can stop and --others=keep keeps them, without asking; --replace never stops\n" +
			"them. When stdin is not a terminal and --others is not given, wt keeps them\n" +
			"and prints one warning.\n\n" +
			"The start goes through the model server ([model-server] in config.toml, or\n" +
			"WT_MODEL_URL; with none configured wt refuses, exit 1, nothing started): the\n" +
			"model id is a model the server lists, the picker shows the server's local\n" +
			"models, and the questions above are the server's. wt\n" +
			"registers a client session for the model for the start, ends it when the model is up,\n" +
			"and says on stderr that the server stops the model after its idle grace unless a\n" +
			"client session uses it. Exit 0 started (or already running), 1 could not start (no\n" +
			"terminal for the picker, an unreachable server), 2 the start failed or was\n" +
			"detached with Ctrl+C (the job keeps running: 'wt job ID'), 3 declined or approval\n" +
			"missing.",
		Example: "  wt start ollama/qwen3.8:27b-mlx\n  wt start",
		Args:    cobra.MaximumNArgs(1),
		// A refused or failed start is not a usage mistake, and main prints
		// the one error line: left to cobra as well, a failed start's message
		// came out twice, the server's log tail with it (#344).
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			replace, _ := cmd.Flags().GetBool("replace")
			// The start goes through the model server: no registry, no
			// local provider. With none configured it refuses before
			// anything starts.
			ms, err := a.openModels(cmd.Context(), "nothing was started")
			if err != nil {
				return remoteExit(err)
			}
			ans := &cliAnswerer{id: id, replace: replace, others: string(othersMode), out: osStderr, tty: interactiveTTY}
			return runStartRemote(cmd.Context(), cmd.OutOrStdout(), ms, a.theme, id, replace, ans)
		},
	}
}
