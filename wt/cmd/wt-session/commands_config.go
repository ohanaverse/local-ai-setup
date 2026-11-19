// wt config command — user-level preferences. Bare, it opens the session
// editor (internal/sessioneditor): the Agents tab and the Mode tab
// (runConfigEditor). It reads config.toml only, never the registry; the models
// in registry.toml are managed by `wt-model model` and this machine's model
// server is set up by `wt-model setup`. `wt config theme` manages the active
// color theme.
//
// The theme subcommand family uses the active theme (loaded by newApp)
// for its own output where appropriate (table borders, list names in their
// accent colors). The exception is `wt config theme set`: the user is
// actively changing the theme, so the prompt stays unthemed.

package main

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessioneditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/spf13/cobra"
)

// sessioneditorRun is the entry point for the session editor TUI. It is a
// package-level var so tests can verify it is called without needing a TTY.
var sessioneditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error, o sessioneditor.Options) (sessioneditor.Result, error) {
	return sessioneditor.Run(theme, cfg, cfgErr, o)
}

// modelSetupError is a `wt-model setup` child, asked for by the Mode tab,
// that did not finish. wt config returns it ("wt-model setup: <err>"); the
// first-run gate words it as a warning and goes on, since the mode is stored.
type modelSetupError struct{ err error }

func (e *modelSetupError) Error() string { return "wt-model setup: " + e.err.Error() }
func (e *modelSetupError) Unwrap() error { return e.err }

// runConfigEditor opens the session editor on tab start over config.toml
// alone (config.LoadSession, no registry). A parse error leaves cfg nil (the
// editor builds its own stand-in and disables writes); a session validation
// error is shown in the banner on open. When the Mode tab asked for it, it
// then runs `wt-model setup` (runModelSetupChild); a failed child is a
// *modelSetupError.
func runConfigEditor(a *app, start sessioneditor.Tab) error {
	cfg, err := config.LoadSession()
	cfgErr := err
	if err == nil {
		cfgErr = cfg.ValidateSession()
	}
	res, err := sessioneditorRun(a.theme, cfg, cfgErr, sessioneditor.Options{
		StartTab: start,
		Mode:     sessioneditor.ModeDepsFromEnv(modelmode.DefaultEnv()),
	})
	if err != nil {
		return err
	}
	if res.RunModelSetup {
		if err := runModelSetupChild(res.ModelSetup); err != nil {
			return &modelSetupError{err: err}
		}
	}
	return nil
}

// configCmd returns the `wt config` command. With no subcommand, launches
// the interactive session editor: agents in config.toml and the mode.
// Subcommands configure specific concerns without entering the TUI.
func configCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage wt preferences and config.toml",
		Long: "Manage wt user preferences.\n\n" +
			"With no subcommand, launches an interactive TUI with two tabs (tab\n" +
			"switches): Agents, the agents in config.toml, saved with ctrl+s; and Mode,\n" +
			"how sessions reach a model server: native, remote (its url and token are\n" +
			"checked here) or local (set up this machine with `wt-model setup`). The\n" +
			"models in registry.toml are managed with `wt-model model`.\n\n" +
			"Subcommands:\n  theme   active color theme\n  path    print the config directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigEditor(a, sessioneditor.TabAgents)
		},
	}
	cmd.AddCommand(configPathCmd(a), configThemeCmd(a))
	return cmd
}

// configPathCmd prints the config directory.
func configPathCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), config.Dir())
			return nil
		},
	}
}

// configThemeCmd returns the `wt config theme` parent command. With no
// subcommand, prints the active theme (convenience — the user wants to
// know what's currently set). With a subcommand, dispatches to the
// appropriate action.
func configThemeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "theme",
		Short: "Manage the active color theme",
		Long: "Manage the active color theme.\n" +
			"  list    list available themes\n" +
			"  show    show a theme's tokens (active theme if no name given)\n" +
			"  set     activate a theme (effective on next wt launch)\n" +
			"  unset   remove the theme choice (use default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// No subcommand: show active theme. Use cobra's output stream
			// so callers can capture it (tests, scripts).
			accentStyle := lipgloss.NewStyle().Foreground(a.theme.Token(themes.TokenAccent))
			available := themes.Names()
			fmt.Fprintf(cmd.OutOrStdout(), "active: %s\navailable: %s\n",
				accentStyle.Render(a.theme.Name), available)
			return nil
		},
	}
	cmd.AddCommand(
		configThemeListCmd(a),
		configThemeShowCmd(a),
		configThemeSetCmd(),
		configThemeUnsetCmd(),
	)
	return cmd
}

// configThemeListCmd lists all built-in themes. Names are rendered in that
// theme's accent color so users can scan and see which themes look
// distinct.
func configThemeListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available themes",
		RunE: func(cmd *cobra.Command, args []string) error {
			themeList := themes.Builtins()
			// Sort by name first, then build rows in that order so the
			// comparison keys align with the row slice.
			sort.SliceStable(themeList, func(i, j int) bool {
				return themeList[i].Name < themeList[j].Name
			})
			rows := make([][]string, 0, len(themeList))
			for _, th := range themeList {
				// Render the name in that theme's accent color.
				nameStyle := lipgloss.NewStyle().Foreground(th.Token(themes.TokenAccent))
				rows = append(rows, []string{
					nameStyle.Render(th.Name),
					themeDescription(th.Name),
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), renderTable(
				[]string{"NAME", "DESCRIPTION"},
				rows,
				a.theme,
			))
			return nil
		},
	}
}

// themeDescription returns a short blurb for a built-in theme. Used in the
// `wt config theme list` output.
func themeDescription(name string) string {
	switch name {
	case "default":
		return "subtle, terminal-native colors"
	case "solarized":
		return "warm palette inspired by Solarized"
	case "mono":
		return "grayscale with a single blue accent"
	case "tokyo-night":
		return "Enkia's Tokyo Night (Night/Day pair)"
	}
	return ""
}

// configThemeShowCmd shows a single theme's tokens. With no argument, shows
// the active theme. Format:
//
//	<name> — <description>
//	<token>  <dark hex> / <light hex>  dark light
//	...
//
// The "dark" and "light" preview words render in the token's own dark/light
// color respectively.
func configThemeShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show [<name>]",
		Short: "Show a theme's tokens",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var theme themes.Theme
			if len(args) == 0 {
				theme = a.theme
			} else {
				th, ok := themes.Get(args[0])
				if !ok {
					return fmt.Errorf("unknown theme %q — available: %s",
						args[0], themes.Names())
				}
				theme = th
			}
			renderThemePreview(cmd, theme)
			return nil
		},
	}
}

// renderThemePreview prints a single theme's tokens with dark/light
// previews.
func renderThemePreview(cmd *cobra.Command, theme themes.Theme) {
	out := cmd.OutOrStdout()
	desc := themeDescription(theme.Name)
	if desc != "" {
		fmt.Fprintf(out, "%s — %s\n\n", theme.Name, desc)
	} else {
		fmt.Fprintf(out, "%s\n\n", theme.Name)
	}
	for _, token := range themes.AllTokens() {
		c := theme.Token(token)
		darkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(c.Dark))
		lightStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(c.Light))
		// 12-char left-aligned token name; hex pairs; preview words.
		fmt.Fprintf(out, "  %-12s %s / %s  %s %s\n",
			token,
			c.Dark, c.Light,
			darkStyle.Render("dark"),
			lightStyle.Render("light"),
		)
	}
	fmt.Fprintf(out, "\nset with: wt config theme set %s\n", theme.Name)
}

// configThemeSetCmd activates a theme. Writes themes.toml atomically. The
// message to stderr notes that the new theme takes effect on the next
// launch — themes don't hot-reload.
func configThemeSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name>",
		Short: "Activate a theme",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := themes.Save(name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"wt: theme set to %q (effective on next wt launch)\n", name)
			return nil
		},
	}
}

// configThemeUnsetCmd removes the theme choice. No-op success if no theme
// is set.
func configThemeUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset",
		Short: "Remove the theme choice (use default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := themes.Unset(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(),
				"wt: no theme set (using \"default\")")
			return nil
		},
	}
}
