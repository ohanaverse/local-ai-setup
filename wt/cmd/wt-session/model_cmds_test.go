package main

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// modelCmdConfig is a minimal local-only registry shared by the start/stop
// command tests: two ollama models and one omlx model, no agents needed.
func modelCmdConfig() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal},
			{ID: "ollama/b:1", ProviderID: "ollama", ModelName: "b:1", Location: config.LocationLocal},
			{ID: "omlx/c", ProviderID: "omlx", ModelName: "c", Location: config.LocationLocal},
		},
	}
	return cfg
}

// sessionCommandFloor is the non-stub commands (the root included)
// wt-session has after phase 4 stage 2; lower it only when a command leaves
// on purpose.
const sessionCommandFloor = 35

// TestNoHelpTextNamesModelman pins success criterion 1 of the retirement for
// wt's own help: no command's help may send a reader to modelman, which no
// longer exists. It walks the whole command tree, so a command added later is
// covered too. For each command it reads the short line, the long text, the
// examples, the deprecation notice and the usage text, and then every flag's
// description and deprecation notice one by one: the usage text alone leaves
// out the command's own short line and every hidden flag. The flags of a
// parent that a subcommand inherits (root's persistent ones) are read at the
// parent, where they are declared. The check is for
// the lowercase tool name: the env alias names (MODELMAN_REGISTRY and the
// three MODELMAN_LITELLM_ ones) are uppercase, are read forever, and may be
// named.
func TestNoHelpTextNamesModelman(t *testing.T) {
	seen := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if _, stub := c.Annotations[removedCmdAnnotation]; !stub {
			seen++
		}
		for _, text := range []string{c.Short, c.Long, c.Example, c.Deprecated, c.UsageString()} {
			if strings.Contains(text, "modelman") {
				t.Errorf("%s: help names modelman:\n%s", c.CommandPath(), text)
			}
		}
		checkFlags := func(f *pflag.Flag) {
			if strings.Contains(f.Usage+f.Deprecated, "modelman") {
				t.Errorf("%s --%s: flag help names modelman: %s", c.CommandPath(), f.Name, f.Usage)
			}
		}
		// c.Flags() takes in the parent's persistent flags only when they are
		// merged for it — a side effect of resolving the usage template the
		// text loop above happened to trigger, not anything it promised.
		// Reading the command's own flags, local and persistent, directly
		// keeps the coverage from depending on that rendering; a flag both
		// sets hold after a merge may then be named twice, as two failures.
		c.Flags().VisitAll(checkFlags)
		c.PersistentFlags().VisitAll(checkFlags)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmdWith(testHooks()))
	if seen < sessionCommandFloor {
		t.Fatalf("walked %d commands, want at least %d", seen, sessionCommandFloor)
	}
}
