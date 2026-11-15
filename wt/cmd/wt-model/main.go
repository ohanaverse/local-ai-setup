// Command wt-model serves wt's model control API and gateway: it starts and
// stops local models on behalf of agent sessions that run elsewhere.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelserver"
	"github.com/spf13/cobra"
)

// rootCmd builds the wt-model command tree; mk supplies the server's
// dependencies.
func rootCmd(mk depsFactory) *cobra.Command {
	root := &cobra.Command{
		Use:           "wt-model",
		Short:         "wt's model control API and gateway",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(serveCmd(mk))
	return root
}

func main() {
	// SIGINT and SIGTERM end the serve context, which shuts the server down.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := rootCmd(modelserver.DefaultDeps)
	err := root.ExecuteContext(ctx)
	// Never exit while an async LiteLLM proxy restart a route write started is
	// still in flight: goroutines do not survive main returning.
	lifecycle.WaitPendingRoutes()
	if err != nil {
		os.Exit(reportExit(err, os.Stderr))
	}
}
