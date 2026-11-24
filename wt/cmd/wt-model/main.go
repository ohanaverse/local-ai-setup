// Command wt-model is wt's host side: it serves the model control API and
// gateway, and administers the registry, the LiteLLM routes and this
// machine's model-server setup.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelserver"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags, as wt's is:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/wt-model
var version = "0.1.0"

// serveRuntime holds the per-call seams of the commands: tests pass a scratch
// store. Record is where serve keeps its runtime record (serve.json).
// StartSync is the one route sync serve runs once it is listening
// (serveStartSync; #598): nil runs none, which is what every test runtime
// gets unless it hands one in.
type serveRuntime struct {
	Record    servestate.Store
	StartSync func(ctx context.Context, cfg *config.Config, errOut io.Writer)
}

// rootCmd builds the wt-model command tree; mk supplies the server's
// dependencies and rt where serve keeps its runtime record.
func rootCmd(mk depsFactory, rt serveRuntime) *cobra.Command {
	return rootCmdOver(mk, newHost(), rt)
}

// rootCmdWith is rootCmd over a given host: tests hand in one whose seams
// reach nothing on the machine. Its runtime is the zero one, whose empty
// record store writes, reads and removes nothing.
func rootCmdWith(mk depsFactory, h *host) *cobra.Command {
	return rootCmdOver(mk, h, serveRuntime{})
}

// rootCmdOver is the command tree over a given host and runtime.
func rootCmdOver(mk depsFactory, h *host, rt serveRuntime) *cobra.Command {
	root := &cobra.Command{
		Use:           "wt-model",
		Short:         "Serve wt's models and administer the registry, routes and this machine's setup",
		Version:       version,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("wt-model {{.Version}}\n")
	// One host config for the whole tree: every admin command reads the
	// registry through the same h.
	root.AddCommand(serveCmd(mk, rt, h), litellmCmd(h), modelCmd(h), setupCmd(h), servedCmd(h), warmCmd(h), cloudSyncCmd(h), stopCmd(h, rt.Record), aliasCmd(h, rt.Record), gatewayCmd(h))
	silenceUsageOnError(root)
	return root
}

// silenceUsageOnError sets SilenceUsage on every runnable subcommand: an
// error such a command returns is a failure to report, not a usage mistake,
// and main prints it as the one "wt-model:" line (style guide M3). A command
// that shows usage for its own usage mistakes turns it back on in its RunE
// (`wt-model model edit`). It is a walk, not a literal on each command, so a
// new subcommand cannot forget it (cmd/wt-session's is the same walk). The
// root is left out: SilenceUsage on it would silence every subcommand,
// including the usage block `wt-model model edit` asks for.
func silenceUsageOnError(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.Runnable() {
			c.SilenceUsage = true
		}
		silenceUsageOnError(c)
	}
}

func main() {
	// Lines the shared config loader prints name this binary, not wt.
	config.BinaryName = "wt-model"
	// SIGINT and SIGTERM end the serve context, which shuts the server down.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := rootCmd(modelserver.DefaultDeps, serveRuntime{Record: servestate.Default(), StartSync: serveStartSync(localmodels.Inventory)})
	// After the first signal cancels ctx, stop() restores the default signal
	// behavior, so a second SIGINT/SIGTERM kills the process even when the
	// graceful shutdown hangs.
	go func() {
		<-ctx.Done()
		stop()
	}()
	err := root.ExecuteContext(ctx)
	// Never exit while an async LiteLLM proxy restart a route write started is
	// still in flight: goroutines do not survive main returning.
	lifecycle.WaitPendingRoutes()
	if err != nil {
		os.Exit(exitcode.Report("wt-model", err, os.Stderr))
	}
}
