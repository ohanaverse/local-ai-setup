package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
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
restarted: bootout, then bootstrap ('wt-model setup', x then s; or the
launchctl commands 'wt-model gateway' prints). A kickstart does not do it.

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
writes nothing. serve must be restarted for the change to take effect: a
bootout then a bootstrap ('wt-model setup', x then s), not a kickstart. The
Setup tab's g key writes the same switch.

With the mode on, serve checks the LiteLLM proxy for what would make a keyless
anthropic/ row unsafe, at start and then every minute. The first hazard turns
the mode off, and it stays off until serve is restarted.

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
// A kickstart is not offered (#693): it restarts the definition launchd already
// loaded and serve is only told the file changed by a fresh start, so the
// advice is a bootout and a bootstrap: x then s in 'wt-model setup', or the two
// launchctl commands. The plist's contents are never printed, only its path.
func restartHint() string {
	uid := os.Getuid()
	return fmt.Sprintf("restart wt-model serve for it to take effect: in 'wt-model setup' press x then s (bootout, bootstrap); or run launchctl bootout gui/%d/%s, then launchctl bootstrap gui/%d %s",
		uid, modelsetup.AgentLabel, uid, "~/Library/LaunchAgents/"+modelsetup.AgentLabel+".plist")
}

// readGatewaySwitch is config.GatewaySettings for a command that only needs
// the switch: a TLS refusal (*config.GatewayKeyError, which keeps serve from
// starting) does not stop it from showing or changing the switch, so the
// operator can still turn the mode off. tlsErr is that refusal, "" when there
// is none; any other error (a config.toml that does not parse) is returned.
func readGatewaySwitch() (gw config.GatewayConf, warnings []string, tlsErr string, err error) {
	gw, warnings, err = config.GatewaySettings()
	var ke *config.GatewayKeyError
	if errors.As(err, &ke) {
		return gw, warnings, ke.Msg, nil
	}
	return gw, warnings, "", err
}

func runGatewayShow(out, errOut io.Writer) error {
	gw, warnings, tlsErr, err := readGatewaySwitch()
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
	if tlsErr != "" {
		fmt.Fprintf(out, "  tls: serve would refuse to start: %s\n", tlsErr)
	} else if gw.TLSCert == "" {
		fmt.Fprintln(out, "  tls: off (plain http)")
	} else {
		fmt.Fprintf(out, "  tls: %s (key %s)\n", gw.TLSCert, gw.TLSKey)
	}
	fmt.Fprintln(out, "serve reads [gateway] once, at start: "+restartHint())
	return nil
}

func runGatewaySubscription(out, errOut io.Writer, on, dryRun bool) error {
	cur, warnings, tlsErr, err := readGatewaySwitch()
	if err != nil {
		return err
	}
	if tlsErr != "" {
		fmt.Fprintln(errOut, "wt-model: warning: "+tlsErr+" (serve will not start until it is fixed)")
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
		fmt.Fprintln(out, "serve checks the proxy for subscription hazards at start and then every minute, and turns the mode off at the first hazard; it stays off until serve is restarted.")
	}
	return nil
}
