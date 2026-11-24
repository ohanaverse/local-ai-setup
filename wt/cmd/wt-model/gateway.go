package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// gatewayCmd is `wt-model gateway`: the host's [gateway] settings in the
// wt-model home's config.toml (#693). With no argument it shows them;
// `gateway subscription on|off` writes the one key that has a writer, the
// switch of the Claude subscription header mode. tls_cert and tls_key stay
// hand-edited.
func gatewayCmd(_ *host) *cobra.Command {
	c := &cobra.Command{
		Use:   "gateway",
		Short: "Show or change the host's [gateway] settings (the Claude subscription switch)",
		Long: `Show the [gateway] table of the wt-model home's config.toml
(~/.config/wt-model/config.toml): whether the Claude subscription header
mode is switched on and which TLS pair serve speaks with. Nothing is
changed. The key is never printed, only the paths.

'wt-model gateway subscription on|off' writes the switch. serve reads
[gateway] once, at start, so a change takes effect when wt-model serve is
restarted ('wt-model setup' and R, or launchctl kickstart -k).

Exit codes:
  0  done
  1  any failure`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGatewayShow(cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	var dryRun bool
	sub := &cobra.Command{
		Use:   "subscription on|off",
		Short: "Turn the Claude subscription header mode on or off for the next serve start",
		Long: `Write [gateway] subscription_passthrough in the wt-model home's config.toml.
It changes that one key and leaves the rest of the file (the [litellm] table,
tls_cert, tls_key) as it is, under the lock the proxy-pair writer uses; the
file it replaces is first copied to backups/config.toml.bak-<date>-<time>
beside it (0700 directory, 0600 file). Asking for what the file already says
writes nothing. serve must be restarted for the change to take effect.

With the mode on, serve checks the LiteLLM proxy for what would make a keyless
anthropic/ row unsafe, at start and then every minute, and keeps the mode off
while it finds a hazard.

Flags:
  --dry-run   say what would change and write nothing

Exit codes:
  0  done
  1  any failure (a word other than on or off, a config.toml that does not parse)`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			var on bool
			switch args[0] {
			case "on":
				on = true
			case "off":
			default:
				return fmt.Errorf("subscription takes on or off, got %q", args[0])
			}
			return runGatewaySubscription(cmd.OutOrStdout(), cmd.ErrOrStderr(), on, dryRun)
		},
	}
	sub.Flags().BoolVar(&dryRun, "dry-run", false, "say what would change and write nothing")
	c.AddCommand(sub)
	return c
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// restartHint is the one line every change ends with: serve reads [gateway]
// once, at start.
func restartHint() string {
	return fmt.Sprintf("restart wt-model serve for it to take effect (wt-model setup, then R; or launchctl kickstart -k gui/%d/%s)", os.Getuid(), "local.wt-model.serve")
}

func runGatewayShow(out, errOut io.Writer) error {
	gw, warnings, err := config.GatewaySettings()
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(errOut, "wt-model: warning: "+w)
	}
	path := config.ProxyHomePath()
	if path == "" {
		path = "no config home (HOME and XDG_CONFIG_HOME are unset)"
	}
	fmt.Fprintf(out, "gateway: %s\n", path)
	fmt.Fprintf(out, "  subscription passthrough: %s\n", onOff(gw.SubscriptionPassthrough))
	if gw.TLSCert == "" {
		fmt.Fprintln(out, "  tls: off (plain http)")
	} else {
		fmt.Fprintf(out, "  tls: %s (key %s)\n", gw.TLSCert, gw.TLSKey)
	}
	fmt.Fprintln(out, "serve reads [gateway] once, at start: restart wt-model serve after a change.")
	return nil
}

func runGatewaySubscription(out, errOut io.Writer, on, dryRun bool) error {
	cur, warnings, err := config.GatewaySettings()
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(errOut, "wt-model: warning: "+w)
	}
	if cur.SubscriptionPassthrough == on && len(warnings) == 0 {
		fmt.Fprintf(out, "subscription passthrough is already %s; nothing was changed\n", onOff(on))
		return nil
	}
	if dryRun {
		fmt.Fprintf(out, "would turn subscription passthrough %s (it is %s)\n", onOff(on), onOff(cur.SubscriptionPassthrough))
		fmt.Fprintln(out, "dry run: nothing was changed")
		return nil
	}
	res, err := config.SetGatewayPassthrough(on)
	if err != nil {
		return err
	}
	if !res.Changed {
		fmt.Fprintf(out, "subscription passthrough is already %s; nothing was changed\n", onOff(on))
		return nil
	}
	fmt.Fprintf(out, "subscription passthrough: %s (was %s) in %s\n", onOff(on), onOff(cur.SubscriptionPassthrough), res.Path)
	if res.Backup != "" {
		fmt.Fprintf(out, "backup: %s\n", res.Backup)
	}
	fmt.Fprintln(out, restartHint())
	if on {
		fmt.Fprintln(out, "serve checks the proxy for subscription hazards at start and then every minute, and keeps the mode off while it finds one.")
	}
	return nil
}
