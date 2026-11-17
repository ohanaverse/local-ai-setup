// Command wt-model serves wt's model control API and gateway: it starts and
// stops local models on behalf of agent sessions that run elsewhere.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelserver"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags, as wt's is:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/wt-model
var version = "0.1.0"

// rootCmd builds the wt-model command tree; mk supplies the server's
// dependencies.
func rootCmd(mk depsFactory) *cobra.Command {
	root := &cobra.Command{
		Use:           "wt-model",
		Short:         "Serve wt's models over a control API and gateway",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("wt-model {{.Version}}\n")
	root.AddCommand(serveCmd(mk), litellmCmd())
	return root
}

func main() {
	// Lines the shared config loader prints name this binary, not wt.
	config.BinaryName = "wt-model"
	// SIGINT and SIGTERM end the serve context, which shuts the server down.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := rootCmd(modelserver.DefaultDeps)
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
